package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

// Uses a private schema inside a rolled-back transaction. It can run beside the
// migration upgrade test without changing the disposable database's public schema.
func TestBeichenPostgresDailyComparison(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || config.Database != "nebula_test" ||
		config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("comparison test requires a loopback-only nebula_test database with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := stdlib.OpenDB(*config)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("could not open disposable PostgreSQL")
	}
	defer tx.Rollback()
	schema := pgx.Identifier{"beichen_test_" + uuid.NewString()}.Sanitize()
	execTestSQL(t, ctx, tx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema)
	execTestSQL(t, ctx, tx, `
CREATE TABLE providers (id uuid PRIMARY KEY, base_url text NOT NULL);
CREATE TABLE provider_credentials (id uuid PRIMARY KEY, provider_id uuid, label text NOT NULL DEFAULT '');
CREATE TABLE users (id uuid PRIMARY KEY);
CREATE TABLE gateway_calls (
  provider_id uuid, provider_credential_id uuid, binding_id uuid, upstream_model_name text,
  created_at timestamptz, sent_at timestamptz, outcome text, protocol text, usage_state text,
  prompt_tokens int, completion_tokens int, cache_hit_tokens bigint, cache_miss_tokens bigint, cache_write_tokens bigint,
  reasoning_tokens bigint, tool_prompt_tokens bigint, usage_reason text,
  beichen_state text, beichen_credit_exact numeric, beichen_dispatch_kind text, beichen_dispatch_at timestamptz,
  beichen_dispatch_credential_id uuid, beichen_dispatch_model text, beichen_price_at timestamptz
)`)
	migration, err := os.ReadFile("../infrastructure/db/migrations/0030_beichen.sql")
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, ctx, tx, string(migration))
	aliases, err := os.ReadFile("../infrastructure/db/migrations/0046_beichen_billing_checks_and_aliases.sql")
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, ctx, tx, string(aliases))

	job := syncJob{provider: uuid.New(), credential: uuid.New()}
	actor := uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO users (id) VALUES ($1)", actor)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials (id) VALUES ($1)", job.credential)
	execTestSQL(t, ctx, tx, `
INSERT INTO beichen_price_versions (credential_id, model, track, basis, source, effective_at, input_rate, output_rate)
VALUES ($1,'priced','upstream','effective','billing','2026-11-17 15:00:00+00',20,0),
       ($1,'priced','upstream','base','billing','2026-11-17 15:59:30+00',5,0)`, job.credential)
	execTestSQL(t, ctx, tx, `
INSERT INTO beichen_multiplier_versions (credential_id, effective_at, multiplier)
VALUES ($1,'2026-11-17 15:59:30+00',0.5)`, job.credential)
	execTestSQL(t, ctx, tx, `
INSERT INTO gateway_calls (provider_id, provider_credential_id, binding_id, upstream_model_name,
  created_at, sent_at, outcome, protocol, usage_state, prompt_tokens, completion_tokens, cache_hit_tokens, cache_write_tokens)
VALUES
  ($1,$2,$3,'priced','2026-11-17 15:59:00+00','2026-11-17 16:00:00+00','succeeded','openai_responses','reported',170000000,0,0,0),
  ($1,$2,$3,'explicit','2026-11-17 16:00:00+00','2026-11-17 16:00:01+00','succeeded','openai_compatible','reported',20,1,0,2),
  ($1,$2,$3,'unknown','2026-11-17 16:00:00+00','2026-11-17 16:00:01+00','succeeded','anthropic','reported',20,1,0,NULL)
`, job.provider, job.credential, uuid.New())
	execTestSQL(t, ctx, tx, `
INSERT INTO gateway_calls (provider_id, outcome, beichen_state, beichen_credit_exact,
  beichen_dispatch_kind, beichen_dispatch_at, beichen_dispatch_credential_id, beichen_dispatch_model,
  protocol, usage_state, prompt_tokens, completion_tokens, cache_hit_tokens, cache_write_tokens)
VALUES
  ($1,'succeeded','charged',0.000000123456789,'beichen','2026-11-18 03:00:00+00',$2,'explicit',
   'openai_compatible','reported',0,0,0,0),
  ($1,'succeeded','pending_review',NULL,'beichen','2026-11-18 03:00:00+00',$2,'unknown',
   'anthropic',NULL,NULL,NULL,NULL,NULL),
  ($1,'succeeded','pending_review',NULL,'beichen','2026-11-18 03:00:05+00',$2,'missing-write',
   'openai_responses','reported',4225,5,4096,NULL)
`, job.provider, job.credential)

	const day = "2026-11-18"
	one, zero, two, billed := int64(1), int64(0), int64(2), int64(43)
	obsolete := usageRow{date: day, model: "obsolete", requests: &one}
	if err := replaceUsageDay(ctx, tx, job, day, []usageRow{obsolete}); err != nil {
		t.Fatal(err)
	}
	imported := usageRow{date: day, model: "imported", requests: &two, input: &zero, output: &zero, billed: &billed}
	if err := saveUsage(ctx, tx, job.provider, job.credential, imported, "csv", &actor); err != nil {
		t.Fatal(err)
	}
	if err := replaceUsageDay(ctx, tx, job, day, nil); err != nil {
		t.Fatal(err)
	}
	// Removing a credential cascades its sync state, but the archived comparison stays visible.
	execTestSQL(t, ctx, tx, "INSERT INTO beichen_datasets (credential_id,kind) VALUES ($1,'usage')", job.credential)
	execTestSQL(t, ctx, tx, "DELETE FROM provider_credentials WHERE id = $1", job.credential)
	var stateCount int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM beichen_datasets").Scan(&stateCount); err != nil || stateCount != 0 {
		t.Fatalf("sync state was not cascaded: %d, %v", stateCount, err)
	}
	start, _ := dayStart(day)
	var view, prior Overview
	if err := readComparison(ctx, tx, job.provider, start, &view); err != nil {
		t.Fatal(err)
	}
	if err := readComparison(ctx, tx, job.provider, start.AddDate(0, 0, -1), &prior); err != nil || len(prior.Comparison) != 0 {
		t.Fatalf("call or snapshot assigned to the wrong Beijing day: %+v, %v", prior.Comparison, err)
	}
	if len(view.Comparison) != 5 {
		t.Fatalf("unexpected model rows: %+v", view.Comparison)
	}
	for _, row := range view.Comparison {
		switch row.Model {
		case "priced":
			checkCount(t, row.LocalCacheWriteTokens, 0)
			checkCount(t, row.EstimatedUpstreamMicros, 425)
			checkCount(t, row.TrialCreditMilli, 43)
			if row.TrialCredits == nil || *row.TrialCredits != "0.0425" || row.ChargedCredits != nil {
				t.Fatalf("unexpected legacy trial or charge: %+v", row)
			}
			checkCount(t, row.TrialUnpricedCalls, 0)
			checkCount(t, row.UpstreamRequests, 0)
			checkUsageSource(t, row.UpstreamUsageSource, "usage")
			if row.BilledUpstreamMicros != nil {
				t.Fatal("empty API snapshot fabricated an actual cost")
			}
		case "explicit":
			checkCount(t, row.LocalRequests, 2)
			checkCount(t, row.LocalInputTokens, 18)
			checkCount(t, row.LocalCacheWriteTokens, 2)
			checkCount(t, row.PendingReviewCalls, 0)
			if row.ChargedCredits == nil || *row.ChargedCredits != "0.000000123456789" {
				t.Fatalf("unexpected exact charge: %+v", row)
			}
		case "unknown":
			checkCount(t, row.PendingReviewCalls, 1)
			if row.ChargedCredits != nil {
				t.Fatal("pending review was reported as charged")
			}
			if row.LocalCacheWriteTokens != nil {
				t.Fatal("unknown Anthropic cache writes were fabricated")
			}
		case "imported":
			checkCount(t, row.UpstreamRequests, 2)
			checkCount(t, row.BilledUpstreamMicros, 43)
			checkUsageSource(t, row.UpstreamUsageSource, "csv")
		case "missing-write":
			checkCount(t, row.PendingReviewCalls, 1)
			checkCount(t, row.LocalCacheReadTokens, 4096)
			if row.LocalCacheWriteTokens != nil || row.LocalInputTokens != nil || row.EstimatedUpstreamMicros != nil || row.TrialCredits != nil {
				t.Fatalf("incomplete Responses cache data was treated as known: %+v", row)
			}
		default:
			t.Fatalf("obsolete model still present: %s", row.Model)
		}
	}

	info := billingInfo{Multiplier: json.Number("1.25"), Pricing: json.RawMessage(`{
	  "basis":"base","unit":"usd_micros_per_million_tokens",
	  "models":[{"model":"example-a","input":1,"output":1},{"model":"example-b","input":2,"output":2}]
	}`)}
	newCredential := uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials (id) VALUES ($1)", newCredential)
	if err := saveBilling(ctx, tx, newCredential, info); err != nil {
		t.Fatal(err)
	}
	var effectiveTimes int
	err = tx.QueryRowContext(ctx, `
SELECT count(DISTINCT effective_at) FROM (
  SELECT effective_at FROM beichen_price_versions WHERE credential_id = $1
  UNION ALL
  SELECT effective_at FROM beichen_multiplier_versions WHERE credential_id = $1
) versions`, newCredential).Scan(&effectiveTimes)
	if err != nil || effectiveTimes != 1 {
		t.Fatalf("billing batch has %d effective timestamps: %v", effectiveTimes, err)
	}

	yesterday := usageRow{date: "2026-11-17", model: "gemini-3.1-flash-lite", requests: &two}
	if err := saveUsage(ctx, tx, job.provider, job.credential, yesterday, "usage", nil); err != nil {
		t.Fatal(err)
	}
	service := New(nil, nil)
	service.now = func() time.Time { return start }
	service.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(geminiUsageSample))}, nil
	})
	result := service.fetchUsage(ctx, job, "fake-key", true)
	if result.save == nil || result.err == nil || !strings.Contains(result.err.Error(), "2026-11-17") {
		t.Fatalf("expected one verified day and a warning for the previous day: %v", result.err)
	}
	if err := result.save(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var priorRequests int64
	err = tx.QueryRowContext(ctx, `
SELECT requests FROM beichen_daily_usage WHERE credential_id = $1 AND day = '2026-11-17' AND model = $2::text
`, job.credential, yesterday.model).Scan(&priorRequests)
	if err != nil || priorRequests != two {
		t.Fatalf("verified prior snapshot changed: %d, %v", priorRequests, err)
	}
	// Apply the export migration to a database with existing 0030 records.
	upgrade, err := os.ReadFile("../infrastructure/db/migrations/0031_beichen_usage_export.sql")
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, ctx, tx, string(upgrade))
	var todayBilled int64
	err = tx.QueryRowContext(ctx, `
SELECT billed_upstream_micros FROM beichen_daily_usage WHERE credential_id = $1 AND day = '2026-11-18' AND model = $2::text
`, job.credential, yesterday.model).Scan(&todayBilled)
	if err != nil || todayBilled != 1 {
		t.Fatalf("verified Gemini sample was not stored: %d, %v", todayBilled, err)
	}

	t.Run("official usage export", func(t *testing.T) {
		testUsageExportPostgres(t, ctx, tx, job.provider, newCredential, actor)
	})
}

func testUsageExportPostgres(t *testing.T, ctx context.Context, tx *sql.Tx, provider, credential, actor uuid.UUID) {
	t.Helper()
	execTestSQL(t, ctx, tx, "INSERT INTO providers(id,base_url) VALUES ($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET provider_id=$2,label=$3 WHERE id=$1",
		credential, provider, exportFixtureLabel)
	service := New(nil, nil)
	service.now = func() time.Time { return time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC) }
	if err := service.lockCredential(ctx, tx, provider, credential); err != nil {
		t.Fatal(err)
	}
	rows, label, err := parseUsageExportText(usageExportFixture)
	if err != nil {
		t.Fatal(err)
	}
	err = service.saveImportedUsage(ctx, tx, provider, credential, actor, "Different fixture", rows)
	if domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatalf("wrong credential label was accepted: %v", err)
	}
	var stored int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM beichen_daily_usage WHERE credential_id=$1", credential).Scan(&stored)
	if err != nil || stored != 0 {
		t.Fatalf("label mismatch wrote usage: rows=%d, error=%v", stored, err)
	}
	readDay := func(day string) map[string]Comparison {
		t.Helper()
		start, err := dayStart(day)
		if err != nil {
			t.Fatal(err)
		}
		var view Overview
		if err := readComparison(ctx, tx, provider, start, &view); err != nil {
			t.Fatal(err)
		}
		result := make(map[string]Comparison)
		for _, item := range view.Comparison {
			if item.CredentialID == credential {
				result[item.Model] = item
			}
		}
		return result
	}
	day := rows[0].date
	otherModel := rows[1].model
	legacyRequests := int64(4)
	if err := saveUsage(ctx, tx, provider, credential, usageRow{date: day, model: otherModel, requests: &legacyRequests}, "usage", nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := service.saveImportedUsage(ctx, tx, provider, credential, actor, label, rows); err != nil {
			t.Fatal(err)
		}
	}
	byModel := readDay(day)
	imported := byModel["example-a"]
	checkCount(t, imported.UpstreamRequests, 2)
	checkCount(t, imported.UpstreamInputTokens, 12)
	checkCount(t, imported.UpstreamCacheReadTokens, 0)
	checkCount(t, imported.BilledUpstreamMicros, 2)
	checkUsageSource(t, imported.UpstreamUsageSource, "export")
	if imported.UpstreamOutputTokens != nil || imported.UpstreamCacheWriteTokens != nil {
		t.Fatal("re-import changed the null fields")
	}
	checkCount(t, byModel[otherModel].UpstreamRequests, legacyRequests)
	checkUsageSource(t, byModel[otherModel].UpstreamUsageSource, "usage")

	manualRequests := int64(9)
	manual := usageRow{date: day, model: otherModel, requests: &manualRequests}
	if err := service.saveImportedUsage(ctx, tx, provider, credential, actor, "", []usageRow{manual}); err != nil {
		t.Fatal(err)
	}
	stale := rows[0]
	stale.model = "export-only"
	if err := service.saveImportedUsage(ctx, tx, provider, credential, actor, label, append(rows, stale)); err != nil {
		t.Fatal(err)
	}
	byModel = readDay(day)
	checkCount(t, byModel[otherModel].UpstreamRequests, manualRequests)
	checkUsageSource(t, byModel[otherModel].UpstreamUsageSource, "csv")
	checkUsageSource(t, byModel[stale.model].UpstreamUsageSource, "export")

	job := syncJob{provider: provider, credential: credential}
	automaticRequests := int64(7)
	automatic := []usageRow{
		{date: day, model: rows[0].model, requests: &automaticRequests},
		{date: day, model: otherModel, requests: &automaticRequests},
	}
	if err := replaceUsageDay(ctx, tx, job, day, automatic); err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, ctx, tx, `
INSERT INTO gateway_calls (provider_id, provider_credential_id, binding_id, upstream_model_name, sent_at, outcome)
VALUES ($1,$2,$3,'local-only','2026-11-18 08:00:00+08','succeeded')
`, provider, credential, uuid.New())
	err = service.saveImportedUsage(ctx, tx, provider, credential, actor, label, rows[:1])
	if domain.ErrorCode(err) != domain.CodeValidation || !strings.Contains(err.Error(), "自动确认") {
		t.Fatalf("partial export could replace a confirmed day: %v", err)
	}
	byModel = readDay(day)
	checkCount(t, byModel[rows[0].model].UpstreamRequests, automaticRequests)
	checkUsageSource(t, byModel[rows[0].model].UpstreamUsageSource, "usage")
	checkCount(t, byModel[otherModel].UpstreamRequests, manualRequests)
	checkUsageSource(t, byModel[otherModel].UpstreamUsageSource, "csv")
	if _, ok := byModel[stale.model]; ok {
		t.Fatal("automatic snapshot did not remove an export-only model")
	}
	localOnly := byModel["local-only"]
	checkCount(t, localOnly.LocalRequests, 1)
	if localOnly.UpstreamUsageSource != nil {
		t.Fatal("local-only row has a fabricated upstream source")
	}

	zeroExport := rows[1]
	zeroExport.date = "2026-11-17"
	zeroDayManual := usageRow{date: zeroExport.date, model: "manual-only", requests: &manualRequests}
	if err := service.saveImportedUsage(ctx, tx, provider, credential, actor, "", []usageRow{zeroDayManual}); err != nil {
		t.Fatal(err)
	}
	if err := service.saveImportedUsage(ctx, tx, provider, credential, actor, label, []usageRow{zeroExport}); err != nil {
		t.Fatal(err)
	}
	if err := replaceUsageDay(ctx, tx, job, zeroExport.date, nil); err != nil {
		t.Fatal(err)
	}
	err = service.saveImportedUsage(ctx, tx, provider, credential, actor, label, []usageRow{zeroExport})
	if domain.ErrorCode(err) != domain.CodeValidation || !strings.Contains(err.Error(), "自动确认") {
		t.Fatalf("export could replace a confirmed zero day: %v", err)
	}
	byModel = readDay(zeroExport.date)
	if len(byModel) != 1 {
		t.Fatal("confirmed zero day did not clear the older export and preserve manual usage")
	}
	checkCount(t, byModel[zeroDayManual.model].UpstreamRequests, manualRequests)
	checkUsageSource(t, byModel[zeroDayManual.model].UpstreamUsageSource, "csv")
}

func checkUsageSource(t *testing.T, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("source is null, want %s", want)
	}
	if *got != want {
		t.Fatalf("source=%s, want %s", *got, want)
	}
}

func execTestSQL(t *testing.T, ctx context.Context, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func checkCount(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		if got == nil {
			t.Fatalf("got null, want %d", want)
		}
		t.Fatalf("got %d, want %d", *got, want)
	}
}
