package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/usage/pendingaudit"
)

type row struct {
	ID           string              `json:"id"`
	Student      string              `json:"student"`
	Model        string              `json:"model"`
	Protocol     string              `json:"protocol"`
	UsageState   string              `json:"usage_state"`
	UsageReason  string              `json:"usage_reason"`
	ReviewReason string              `json:"review_reason"`
	CreatedAt    string              `json:"created_at"`
	Input        pendingaudit.Input  `json:"input"`
	Result       pendingaudit.Result `json:"result"`
}

type report struct {
	GeneratedAt string         `json:"generated_at"`
	Threshold   float64        `json:"threshold_yuan"`
	Relative    float64        `json:"relative_threshold"`
	Total       int            `json:"total"`
	Counts      map[string]int `json:"counts"`
	ByReason    map[string]int `json:"by_reason"`
	MaxError    float64        `json:"max_error_yuan"`
	Rows        []row          `json:"rows"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL 只读连接串，默认读取 DATABASE_URL")
	threshold := flag.Float64("threshold", 0.001, "自动结算允许的最大金额误差（元）")
	relative := flag.Float64("relative", 0.01, "自动结算允许的相对误差，例如 0.01 表示 1%")
	limit := flag.Int("limit", 2000, "最多读取多少笔待核")
	out := flag.String("out", "pending-audit.json", "JSON 输出路径")
	flag.Parse()
	if *dsn == "" {
		return errors.New("需要 --dsn 或 DATABASE_URL")
	}
	if *limit < 1 || *threshold < 0 || *relative < 0 {
		return errors.New("limit/threshold/relative 参数不合法")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT c.id::text,c.user_name,COALESCE(c.upstream_model_name,c.model_name),c.protocol,
  COALESCE(c.usage_state,''),COALESCE(c.usage_reason,''),COALESCE(c.beichen_review_reason,''),
  c.prompt_tokens,c.completion_tokens,c.total_tokens,c.cache_hit_tokens,c.cache_write_tokens,
  p.upstream_input,p.upstream_output,p.upstream_cache_read,p.upstream_cache_write,c.created_at
FROM gateway_calls c
LEFT JOIN LATERAL beichen_price_at(c.beichen_dispatch_credential_id,c.beichen_dispatch_model,
  COALESCE(c.beichen_price_at,c.sent_at)) p ON true
WHERE c.beichen_state='pending_review'
ORDER BY c.created_at DESC
LIMIT $1`, *limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	rep := report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Threshold:   *threshold,
		Relative:    *relative,
		Counts:      map[string]int{},
		ByReason:    map[string]int{},
		Rows:        []row{},
	}
	for rows.Next() {
		var item row
		var prompt, completion, total, hit, write sql.NullInt64
		var inputRate, outputRate, readRate, writeRate sql.NullFloat64
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.Student, &item.Model, &item.Protocol,
			&item.UsageState, &item.UsageReason, &item.ReviewReason,
			&prompt, &completion, &total, &hit, &write,
			&inputRate, &outputRate, &readRate, &writeRate, &createdAt); err != nil {
			return err
		}
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		item.Input = pendingaudit.Input{
			Protocol: item.Protocol, PromptTokens: nullableInt(prompt), CompletionTokens: nullableInt(completion),
			TotalTokens: nullableInt(total), CacheReadTokens: nullableInt(hit), CacheWriteTokens: nullableInt(write),
			InputRate: nullableFloat(inputRate), OutputRate: nullableFloat(outputRate),
			CacheReadRate: nullableFloat(readRate), CacheWriteRate: nullableFloat(writeRate),
		}
		item.Result = pendingaudit.Analyze(item.Input, *threshold, *relative)
		rep.Rows = append(rep.Rows, item)
		rep.Counts[item.Result.Class]++
		reason := item.ReviewReason
		if reason == "" {
			reason = item.UsageReason
		}
		if reason == "" {
			reason = "unspecified"
		}
		rep.ByReason[reason]++
		if item.Result.MaxErrorYuan > rep.MaxError {
			rep.MaxError = item.Result.MaxErrorYuan
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rep.Total = len(rep.Rows)
	if err := tx.Commit(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, data, 0o600); err != nil {
		return err
	}
	fmt.Printf("pending=%d exact=%d narrow=%d wide=%d unknown=%d max_error_yuan=%.6f\n",
		rep.Total, rep.Counts[pendingaudit.ClassExact], rep.Counts[pendingaudit.ClassNarrow],
		rep.Counts[pendingaudit.ClassWide], rep.Counts[pendingaudit.ClassUnknown], rep.MaxError)
	reasons := make([]string, 0, len(rep.ByReason))
	for reason := range rep.ByReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		fmt.Printf("  %-40s %d\n", reason, rep.ByReason[reason])
	}
	fmt.Printf("wrote %s\n", *out)
	return nil
}

func nullableInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	value := v.Int64
	return &value
}

func nullableFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	value := v.Float64
	return &value
}
