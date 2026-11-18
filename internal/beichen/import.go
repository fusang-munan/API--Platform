package beichen

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxCSVBytes = 1 << 20
	maxRows     = 2000
)

var priceHeader = []string{"model", "input", "output", "cache_read", "cache_write"}
var usageHeader = []string{"date", "model", "requests", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "billed_upstream_micros"}

type priceRow struct {
	Model string `json:"model"`
	Rates
}

type usageRow struct {
	date       string
	model      string
	requests   *int64
	input      *int64
	output     *int64
	cacheRead  *int64
	cacheWrite *int64
	billed     *int64
}

func (s *Service) Import(ctx context.Context, providerID, credentialID, actorID uuid.UUID, kind, csvText string) error {
	if actorID == uuid.Nil {
		return invalid("需要有效的教师 ID")
	}
	var action func(*sql.Tx) error
	var dataset string
	switch kind {
	case "prices", "prices_base":
		rows, err := readCSV(csvText, priceHeader)
		if err != nil {
			return err
		}
		prices, err := parseCSVPrices(rows)
		if err != nil {
			return err
		}
		basis := "effective"
		if kind == "prices_base" {
			basis = "base"
		}
		dataset = "prices"
		action = func(tx *sql.Tx) error {
			effectiveAt, err := priceTimestamp(ctx, tx)
			if err != nil {
				return err
			}
			for _, row := range prices {
				if err := savePrice(ctx, tx, credentialID, row.Model, "upstream", basis, "csv", &actorID, row.Rates, effectiveAt); err != nil {
					return err
				}
			}
			return nil
		}
	case "usage":
		rows, err := readCSV(csvText, usageHeader)
		if err != nil {
			return err
		}
		usage, err := parseCSVUsage(rows)
		if err != nil {
			return err
		}
		dataset = "usage"
		action = func(tx *sql.Tx) error {
			for _, row := range usage {
				if err := saveUsage(ctx, tx, providerID, credentialID, row, "csv", &actorID); err != nil {
					return err
				}
			}
			return nil
		}
	default:
		return invalid("导入类型须为 prices、prices_base 或 usage")
	}
	return s.write(ctx, providerID, credentialID, func(tx *sql.Tx) error {
		if err := action(tx); err != nil {
			return err
		}
		return recordSuccess(ctx, tx, credentialID, dataset)
	})
}

func readCSV(input string, header []string) ([][]string, error) {
	if len(input) > maxCSVBytes || !utf8.ValidString(input) {
		return nil, invalid("CSV 须为 UTF-8 格式且不超过 1 MiB")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(input, "\ufeff")))
	reader.FieldsPerRecord = len(header)
	fields, err := reader.Read()
	if err != nil || !slices.Equal(fields, header) {
		return nil, invalid("CSV 表头须为 " + strings.Join(header, ","))
	}
	var rows [][]string
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, invalid(fmt.Sprintf("CSV 第 %d 条数据格式不正确", len(rows)+1))
		}
		if len(rows) == maxRows {
			return nil, invalid("CSV 最多允许 2000 条数据")
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, invalid("CSV 至少需要一条数据")
	}
	return rows, nil
}

func parseCSVPrices(rows [][]string) ([]priceRow, error) {
	prices := make([]priceRow, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for index, row := range rows {
		model := row[0]
		values, err := csvNumbers(row[1:], 2)
		if err != nil || !validModel(model) || seen[model] {
			return nil, invalid(fmt.Sprintf("报价 CSV 第 %d 条数据的模型、单价或唯一性无效", index+1))
		}
		rates := Rates{Input: values[0], Output: values[1], CacheRead: values[2], CacheWrite: values[3]}
		if !validRates(&rates) {
			return nil, invalid(fmt.Sprintf("报价 CSV 第 %d 条数据的单价超出范围", index+1))
		}
		seen[model] = true
		prices = append(prices, priceRow{Model: model, Rates: rates})
	}
	return prices, nil
}

func parseCSVUsage(rows [][]string) ([]usageRow, error) {
	usage := make([]usageRow, 0, len(rows))
	seen := make(map[[2]string]bool, len(rows))
	for index, row := range rows {
		if _, err := dayStart(row[0]); err != nil {
			return nil, invalid(fmt.Sprintf("用量 CSV 第 %d 条数据的北京时间日期无效", index+1))
		}
		key := [2]string{row[0], row[1]}
		values, err := csvNumbers(row[2:], 3)
		if err != nil || !validModel(row[1]) || seen[key] {
			return nil, invalid(fmt.Sprintf("用量 CSV 第 %d 条数据的模型、计数、金额或唯一性无效", index+1))
		}
		seen[key] = true
		usage = append(usage, usageRow{date: row[0], model: row[1], requests: values[0], input: values[1],
			output: values[2], cacheRead: values[3], cacheWrite: values[4], billed: values[5]})
	}
	return usage, nil
}

func csvNumbers(fields []string, required int) ([]*int64, error) {
	values := make([]*int64, len(fields))
	for index, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" && index >= required {
			continue
		}
		if field == "" || strings.IndexFunc(field, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return nil, invalid("CSV 计数和金额须为非负整数")
		}
		number, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, invalid("CSV 数值超出范围")
		}
		values[index] = &number
	}
	return values, nil
}

func saveUsage(ctx context.Context, tx *sql.Tx, providerID, credentialID uuid.UUID, row usageRow, source string, actor *uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_daily_usage(provider_id, credential_id, day, model, requests, input_tokens, output_tokens,
  cache_read_tokens, cache_write_tokens, billed_upstream_micros, source, actor_id)
VALUES ($1, $2, $3::text::date, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (credential_id, day, model) DO UPDATE
SET requests = EXCLUDED.requests, input_tokens = EXCLUDED.input_tokens, output_tokens = EXCLUDED.output_tokens,
  cache_read_tokens = EXCLUDED.cache_read_tokens, cache_write_tokens = EXCLUDED.cache_write_tokens,
  billed_upstream_micros = EXCLUDED.billed_upstream_micros, source = EXCLUDED.source, actor_id = EXCLUDED.actor_id,
  updated_at = clock_timestamp()
WHERE EXCLUDED.source = 'csv' OR beichen_daily_usage.source <> 'csv'
`, providerID, credentialID, row.date, row.model, row.requests, row.input, row.output, row.cacheRead, row.cacheWrite, row.billed, source, actor)
	return err
}

func recordSuccess(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, kind string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets(credential_id, kind, last_success) VALUES ($1, $2, clock_timestamp())
ON CONFLICT (credential_id, kind) DO UPDATE SET last_success = EXCLUDED.last_success, last_error = NULL
`, credentialID, kind)
	return err
}
