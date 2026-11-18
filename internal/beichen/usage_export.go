package beichen

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var usageExportHeader = []string{
	"Time", "API Key Name", "Model", "Reasoning Effort", "Inbound Endpoint", "IP Address", "Type", "Billing Mode",
	"Input Tokens", "Output Tokens", "Cache Read Tokens", "Cache Creation Tokens", "Rate Multiplier",
	"Billed Cost", "Original Cost", "First Token (ms)", "Duration (ms)",
}

var exportTimeZone = regexp.MustCompile(`(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

const (
	exportTokenStart = 8
	exportTokenEnd   = 12
	exportBilledCost = 13
)

type usageExportTotals struct {
	requests     int64
	tokens       [4]int64
	tokenMissing [4]bool
	cost         big.Rat
	costMissing  bool
}

func parseUsageExport(records [][]string) ([]usageRow, string, error) {
	var day, label string
	totals := make(map[string]*usageExportTotals)
	var models []string
	for index, record := range records {
		line := index + 1
		timestamp, err := time.Parse(time.RFC3339Nano, record[0])
		if err != nil || !exportTimeZone.MatchString(record[0]) {
			return nil, "", invalid(fmt.Sprintf("北辰明细 CSV 第 %d 条时间须含有效时区", line))
		}
		rowDay := timestamp.In(beijing).Format(dayLayout)
		if _, err := dayStart(rowDay); err != nil {
			return nil, "", invalid(fmt.Sprintf("北辰明细 CSV 第 %d 条北京时间日期无效", line))
		}
		if day == "" {
			day = rowDay
		} else if day != rowDay {
			return nil, "", invalid("北辰明细 CSV 只允许一个北京时间日期")
		}

		rowLabel, model := record[1], record[2]
		if strings.TrimSpace(rowLabel) == "" || len(rowLabel) > 128 ||
			strings.IndexFunc(rowLabel, unicode.IsControl) >= 0 {
			return nil, "", invalid(fmt.Sprintf("北辰明细 CSV 第 %d 条 API Key Name 无效", line))
		}
		if label == "" {
			label = rowLabel
		} else if label != rowLabel {
			return nil, "", invalid("北辰明细 CSV 只允许一个 API Key Name")
		}
		if !validModel(model) {
			return nil, "", invalid(fmt.Sprintf("北辰明细 CSV 第 %d 条模型名无效", line))
		}
		counts, err := csvNumbers(record[exportTokenStart:exportTokenEnd], 0)
		if err != nil {
			return nil, "", invalid(fmt.Sprintf("北辰明细 CSV 第 %d 条 Token 计数无效或超出范围", line))
		}
		total := totals[model]
		if total == nil {
			total = &usageExportTotals{}
			totals[model] = total
			models = append(models, model)
		}
		total.requests++
		for i, count := range counts {
			if count == nil {
				total.tokenMissing[i] = true
				continue
			}
			if *count > math.MaxInt64-total.tokens[i] {
				return nil, "", invalid("北辰明细 CSV 的模型 Token 累计超出范围")
			}
			total.tokens[i] += *count
		}
		cost := strings.TrimSpace(record[exportBilledCost])
		if cost == "" {
			total.costMissing = true
			continue
		}
		// This export column is denominated in USD and contains a bare decimal.
		amount := exactDecimal(json.Number(cost))
		if amount == nil || strings.ContainsAny(cost, "eE") {
			return nil, "", invalid(fmt.Sprintf("北辰明细 CSV 第 %d 条 Billed Cost 须为不带货币符号的非负十进制数", line))
		}
		total.cost.Add(&total.cost, amount)
	}

	rows := make([]usageRow, 0, len(models))
	for _, model := range models {
		total := totals[model]
		// Round the exact sum once, even if some rows were missing the amount.
		// The known part must fit before we decide whether to persist it.
		micros, ok := roundedUSDMicros(&total.cost)
		if !ok {
			return nil, "", invalid("北辰明细 CSV 的模型实付金额累计超出范围")
		}
		var tokens [4]*int64
		for i := range total.tokens {
			if !total.tokenMissing[i] {
				tokens[i] = &total.tokens[i]
			}
		}
		row := usageRow{date: day, model: model, requests: &total.requests,
			input: tokens[0], output: tokens[1], cacheRead: tokens[2], cacheWrite: tokens[3]}
		if !total.costMissing {
			row.billed = &micros
		}
		rows = append(rows, row)
	}
	return rows, label, nil
}
