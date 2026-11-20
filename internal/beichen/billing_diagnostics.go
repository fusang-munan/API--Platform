package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type BillingDiagnostics struct {
	StartDay               string                `json:"start_day"`
	EndDay                 string                `json:"end_day"`
	Timezone               string                `json:"timezone"`
	LocalCalls             int64                 `json:"local_calls"`
	UpstreamRows           int64                 `json:"upstream_rows"`
	DailyChecks            []BillingCheck        `json:"daily_checks"`
	SuspectedBypassWindows []BillingBypassWindow `json:"suspected_bypass_windows"`
}

type BillingBypassWindow struct {
	CredentialID uuid.UUID `json:"credential_id"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	UpstreamRows int64     `json:"upstream_rows"`
}

// Diagnostics reads archived evidence and completed billing checks as of a
// single database snapshot. The date range in the response is inclusive.
func (s *Service) Diagnostics(ctx context.Context, provider uuid.UUID, day string) (BillingDiagnostics, error) {
	if provider == uuid.Nil {
		return BillingDiagnostics{}, invalid("需要供应商 ID")
	}
	now := s.now().In(beijing)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, beijing)
	start, end := today.AddDate(0, 0, -7), today
	if day != "" {
		var err error
		start, err = dayStart(day)
		if err != nil {
			return BillingDiagnostics{}, err
		}
		if start.After(today) {
			return BillingDiagnostics{}, invalid("诊断日期不能晚于今天")
		}
		end = start.AddDate(0, 0, 1)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return BillingDiagnostics{}, unavailable("开启北辰账务诊断失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, provider); err != nil {
		return BillingDiagnostics{}, err
	}
	report := BillingDiagnostics{
		StartDay: start.Format(dayLayout), EndDay: end.AddDate(0, 0, -1).Format(dayLayout), Timezone: beijing.String(),
	}
	const grace = 2 * time.Minute
	var windows []byte
	err = tx.QueryRowContext(ctx, billingDiagnosticsSQL, start.Add(-grace), end.Add(grace), provider, start, end).
		Scan(&report.LocalCalls, &report.UpstreamRows, &windows)
	if err == nil {
		err = json.Unmarshal(windows, &report.SuspectedBypassWindows)
	}
	if err == nil {
		report.DailyChecks, err = readBillingChecks(ctx, tx, provider, start, end)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return BillingDiagnostics{}, unavailable("读取北辰账务诊断失败", err)
	}
	for i := range report.SuspectedBypassWindows {
		window := &report.SuspectedBypassWindows[i]
		window.Start = window.Start.In(beijing)
		window.End = window.End.In(beijing)
	}
	return report, nil
}

// A whole channel-hour with upstream rows and no nearby gateway sends is
// suspicious. A verified receipt excludes a row even across hour boundaries.
const billingDiagnosticsSQL = `
WITH calls AS (` + usage.BeichenSentCallsSQL + ` AND COALESCE(c.provider_id,pc.provider_id)=$3
), observed AS (
  SELECT u.credential_id,
    date_trunc('hour', u.occurred_at, 'Asia/Shanghai') AS hour_start,
    c.id IS NOT NULL AS matched` + archivedMatchesSQL + `
  WHERE u.provider_id=$3 AND u.occurred_at >= $4::timestamptz AND u.occurred_at < $5::timestamptz
), hours AS (
  SELECT credential_id, hour_start, count(*) AS upstream_rows
  FROM observed WHERE NOT matched
  GROUP BY credential_id, hour_start
)
SELECT
  (SELECT count(*) FROM calls WHERE sent_at >= $4 AND sent_at < $5),
  (SELECT count(*) FROM observed),
  COALESCE((SELECT jsonb_agg(jsonb_build_object(
    'credential_id', h.credential_id, 'start', h.hour_start,
    'end', h.hour_start+interval '1 hour', 'upstream_rows', h.upstream_rows
  ) ORDER BY h.hour_start, h.credential_id)
  FROM hours h WHERE NOT EXISTS (
    SELECT 1 FROM calls c WHERE c.credential_id=h.credential_id
      AND c.sent_at >= h.hour_start-interval '2 minutes'
      AND c.sent_at < h.hour_start+interval '1 hour 2 minutes'
  )), '[]'::jsonb)`
