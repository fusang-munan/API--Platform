package beichen

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenBillingDiagnosticsPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	now := time.Now().In(beijing)
	day, err := dayStart(now.AddDate(0, 0, -1).Format(dayLayout))
	if err != nil {
		t.Fatal(err)
	}
	label := day.Format(dayLayout)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var alias, updatedAlias uuid.UUID
	const saveAlias = `INSERT INTO upstream_model_aliases(provider_id,credential_id,upstream_model,school_model,created_by)
		VALUES($1,$2,'fixture-upstream',$3,$4) ON CONFLICT (provider_id,credential_id,upstream_model)
		DO UPDATE SET school_model=excluded.school_model RETURNING id`
	if err := db.QueryRowContext(ctx, saveAlias, provider, nil, "alias-a", actor).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, saveAlias, provider, nil, "alias-b", actor).Scan(&updatedAlias); err != nil || alias != updatedAlias {
		t.Fatalf("provider-wide alias was duplicated: %v", err)
	}
	if err := db.QueryRowContext(ctx, saveAlias, provider, keys[0], "alias-channel", actor).Scan(&updatedAlias); err != nil || alias == updatedAlias {
		t.Fatalf("channel alias did not have separate identity: %v", err)
	}

	// Capped student charges are intentionally different from the calculated base.
	call := func(credential uuid.UUID, model string, at time.Time, credits any, source string) {
		t.Helper()
		exec(`INSERT INTO gateway_calls(id,request_id,month,organization_id,project_id,user_id,api_key_id,model_id,
			provider_id,organization_name,project_name,user_name,api_key_name,model_name,protocol,request_path,
			multiplier_milli,credit_milli,billing_state,outcome,sent_at,created_at,beichen_state,beichen_credit_exact,
			beichen_dispatch_kind,beichen_dispatch_credential_id,beichen_dispatch_model,beichen_review_reason,
			actual_credits,student_credits,school_absorbed_credits,beichen_school_multiplier,pricing_detail)
		SELECT gen_random_uuid(),gen_random_uuid()::text,$4,gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
			gen_random_uuid(),gen_random_uuid(),$1,'Org','Project','Student','Fixture','Public model','openai_responses','/v1/responses',
			1000,0,CASE WHEN $5::numeric IS NULL THEN 'failed' ELSE 'charged' END,'succeeded',$4,$4,
			CASE WHEN $5::numeric IS NULL THEN 'pending_review' ELSE 'charged' END,$5::numeric/2,
			'beichen',$2,$3,CASE WHEN $5::numeric IS NULL THEN 'missing_terminal_usage' END,
			$5::numeric*1.10,$5::numeric/2,COALESCE($5::numeric*0.60,0),1.10,
			jsonb_build_object('base_credits',$5::numeric,'base_source',$6::text)`,
			provider, credential, model, at, credits, source)
	}
	daily := func(credential uuid.UUID, model string, at time.Time, amount any) {
		t.Helper()
		exec(`INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,billed_upstream_micros,source,actor_id)
			VALUES($1,$2,$3::text::date,$4,1,$5,'csv',$6)`, provider, credential, at.Format(dayLayout), model, amount, actor)
	}
	type wantCheck struct {
		credential                        uuid.UUID
		model, status, local, upper, diff string
	}
	cases := []struct {
		wantCheck
		amount int64
	}{
		{wantCheck{keys[0], "equal", "ok", "2", "2", "0"}, 20000},
		{wantCheck{keys[0], "cent-edge", "ok", "2", "1", "1"}, 10000},
		{wantCheck{keys[0], "cent-over", "anomaly", "2.00001", "1", "1.00001"}, 10000},
		{wantCheck{keys[0], "percent-edge", "ok", "505", "500", "5"}, 5000000},
		{wantCheck{keys[0], "percent-over", "anomaly", "494.99999", "500", "-5.00001"}, 5000000},
	}
	var want []wantCheck
	for _, tc := range cases {
		call(tc.credential, tc.model, day.Add(59*time.Minute), tc.local, "gateway_usage_price")
		daily(tc.credential, tc.model, day, tc.amount)
		want = append(want, tc.wantCheck)
	}
	call(keys[0], "late-day", day, "8", "gateway_usage_price")
	call(keys[0], "missing-cost", day, "2", "gateway_usage_price")
	daily(keys[0], "missing-cost", day, nil)
	call(keys[0], "pending", day, nil, "")
	daily(keys[0], "pending", day, 10000)
	call(keys[0], "receipt-only", day, "3", "receipt_actual_cost")
	daily(keys[0], "receipt-only", day, 30000)
	daily(keys[1], "upstream-only", day, 25000)
	call(keys[2], "empty-day", day, "4", "gateway_usage_price")
	// The verified empty day is evidence of zero, while an older public empty day is not.
	seedVerifiedKeyDay(t, ctx, db, keys[2], 42, label, now, "unknown", `{"requests":0,"actual_cost":0}`, `[]`)
	exec("INSERT INTO beichen_usage_days(provider_id,credential_id,day,empty) VALUES($1,$2,$3::text::date,true)", provider, keys[0], label)
	prior := day.AddDate(0, 0, -1)
	call(keys[0], "equal", prior, "6", "gateway_usage_price")
	daily(keys[0], "equal", prior, 60000)
	call(keys[3], "verified-website", day, "2", "gateway_usage_price")
	seedVerifiedKeyDay(t, ctx, db, keys[3], 43, label, now, "CNY", `{"requests":1,"actual_cost":0.02}`,
		`[{"model":"verified-website","requests":1,"actual_cost":0.02}]`)
	exec(`INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,billed_upstream_micros,source)
		VALUES($1,$2,$3::text::date,'verified-website',9,123456,'usage')`, provider, keys[3], label)
	call(keys[0], "open-day", day.AddDate(0, 0, 1), "999", "gateway_usage_price")
	want = append(want,
		wantCheck{keys[0], "late-day", "missing", "8", "", ""},
		wantCheck{keys[0], "missing-cost", "missing", "2", "", ""},
		wantCheck{keys[0], "pending", "missing", "", "1", ""},
		wantCheck{keys[0], "receipt-only", "missing", "", "3", ""},
		wantCheck{keys[1], "upstream-only", "anomaly", "0", "2.5", "-2.5"},
		wantCheck{keys[2], "empty-day", "anomaly", "4", "0", "4"},
		wantCheck{keys[3], "verified-website", "ok", "2", "2", "0"},
	)

	other := uuid.New()
	exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at)
		VALUES($1,'Other Beichen','https://api.beichenapi.com/v1',now(),now())`, other)
	exec(`INSERT INTO beichen_upstream_usage_rows(upstream_user_id,row_id,upstream_key_id,provider_id,credential_id,channel,model,occurred_at,cost_details)
		VALUES (1,1,1,$1,$2,'Gemini','equal',$5,'{}'),
		       (1,2,2,$1,$3,'GM1','upstream-only',$6,'{}'),
		       (1,3,2,$1,$3,'GM1','upstream-only',$7,'{}'),
		       (1,4,4,$4,$8,'CC1','other-provider',$6,'{}')`,
		provider, keys[0], keys[1], other, day.Add(61*time.Minute), day.Add(10*time.Hour+time.Minute),
		day.Add(10*time.Hour+2*time.Minute), uuid.New())

	store := usage.NewStore(db)
	if err := store.RunMaintenance(ctx, true); err != nil {
		t.Fatal(err)
	}
	service := New(db, nil)
	service.now = func() time.Time { return now }
	service.client.Transport = panelTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("diagnostics must not fetch an upstream")
		return nil, nil
	})
	report, err := service.Diagnostics(ctx, provider, label)
	if err != nil {
		t.Fatal(err)
	}
	if report.LocalCalls != 11 || report.UpstreamRows != 3 || report.StartDay != label || report.EndDay != label ||
		report.Timezone != "Asia/Shanghai" || len(report.SuspectedBypassWindows) != 1 {
		t.Fatalf("wrong diagnostic totals/windows: %+v", report)
	}
	window := report.SuspectedBypassWindows[0]
	if window.CredentialID != keys[1] || window.UpstreamRows != 2 ||
		!window.Start.Equal(day.Add(10*time.Hour)) || !window.End.Equal(day.Add(11*time.Hour)) {
		t.Fatalf("wrong bypass window: %+v", window)
	}
	if len(report.DailyChecks) != len(want) {
		t.Fatalf("expected %d daily checks, got %d", len(want), len(report.DailyChecks))
	}
	cost := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	expected := make(map[string]wantCheck)
	for _, check := range want {
		expected[check.model] = check
	}
	for _, check := range report.DailyChecks {
		got := wantCheck{check.CredentialID, check.Model, check.Status, cost(check.LocalCredits), cost(check.UpstreamCredits), cost(check.DiffCredits)}
		if got != expected[check.Model] || check.Day != label {
			t.Errorf("check %+v, want %+v for %s", got, expected[check.Model], label)
		}
		delete(expected, check.Model)
	}
	recent, err := service.Diagnostics(ctx, provider, "")
	if err != nil || recent.StartDay != day.AddDate(0, 0, -6).Format(dayLayout) || recent.EndDay != label ||
		recent.LocalCalls != report.LocalCalls+1 || recent.UpstreamRows != report.UpstreamRows ||
		len(recent.DailyChecks) != len(report.DailyChecks)+1 {
		t.Fatalf("recent diagnostics: %+v, %v", recent, err)
	}
	previousCheck := recent.DailyChecks[len(recent.DailyChecks)-1]
	if previousCheck.Day != prior.Format(dayLayout) || previousCheck.Model != "equal" ||
		previousCheck.Status != "ok" || cost(previousCheck.LocalCredits) != "6" {
		t.Fatalf("same model was not checked independently by day: %+v", previousCheck)
	}

	var checkID, refreshedID uuid.UUID
	var status string
	const readLateCheck = `SELECT id,status FROM billing_checks WHERE provider_id=$1 AND credential_id=$2
		AND model='late-day' AND day=$3::text::date`
	if err := db.QueryRowContext(ctx, readLateCheck, provider, keys[0], label).Scan(&checkID, &status); err != nil || status != "missing" {
		t.Fatalf("expected missing check before day bill: %q, %v", status, err)
	}
	daily(keys[0], "late-day", day, 80000)
	if err := store.RunMaintenance(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, readLateCheck, provider, keys[0], label).Scan(&refreshedID, &status); err != nil || status != "ok" || refreshedID != checkID {
		t.Fatalf("late bill should update the existing check: %s %v", status, err)
	}
	var openChecks int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM billing_checks WHERE provider_id=$1 AND model='open-day'", provider).Scan(&openChecks)
	if err != nil || openChecks != 0 {
		t.Fatalf("an open day was checked: %d, %v", openChecks, err)
	}
	openReport, err := service.Diagnostics(ctx, provider, now.Format(dayLayout))
	if err != nil || len(openReport.DailyChecks) != 0 || openReport.LocalCalls != 1 {
		t.Fatalf("open-day diagnostics: %+v, %v", openReport, err)
	}
	if err := db.QueryRowContext(ctx, readLateCheck, other, keys[0], label).Scan(&refreshedID, &status); err != sql.ErrNoRows {
		t.Fatalf("provider checks were not isolated: %v", err)
	}
}
