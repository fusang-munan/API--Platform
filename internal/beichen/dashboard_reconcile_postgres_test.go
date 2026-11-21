package beichen

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDashboardLocalReconciliationPostgres(t *testing.T) {
	ctx, tx := reportTestTransaction(t)
	execTestSQL(t, ctx, tx, `
ALTER TABLE gateway_calls ADD COLUMN id uuid, ADD COLUMN request_path text,
 ADD COLUMN beichen_receipt_api_key_id bigint, ADD COLUMN beichen_receipt_response_id uuid,
 ADD COLUMN beichen_receipt_conflict boolean NOT NULL DEFAULT false, ADD COLUMN beichen_upstream_price_version bigint,
 ADD COLUMN price_snapshot jsonb, ADD COLUMN price_tier text, ADD COLUMN student_credits numeric;
CREATE TABLE beichen_receipt_response_claims(response_id uuid, first_call_id uuid, conflicted_at timestamptz);
CREATE TABLE beichen_call_receipts(call_id uuid, response_id uuid, credential_id uuid, api_key_id bigint, model text, actual_cost numeric);
`)
	provider, credential, call, receipt := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, "INSERT INTO providers VALUES($1,$2)", provider, origin)
	execTestSQL(t, ctx, tx, "INSERT INTO provider_credentials VALUES($1,$2,'test','2026-11-10T00:00:00+08:00')", credential, provider)
	execTestSQL(t, ctx, tx, `
INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,effective_at,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES($1,'fixture-model','upstream','base','billing','2026-11-20T00:00:00+08:00',10000000000,0,0,0)
`, credential)
	execTestSQL(t, ctx, tx, `
INSERT INTO gateway_calls(id,provider_id,request_path,outcome,protocol,usage_state,prompt_tokens,completion_tokens,
 cache_hit_tokens,cache_miss_tokens,cache_write_tokens,total_tokens,beichen_state,beichen_dispatch_kind,beichen_dispatch_credential_id,
 beichen_dispatch_model,beichen_price_at,beichen_receipt_api_key_id,beichen_receipt_response_id,beichen_upstream_price_version,
 student_credits,beichen_credit_exact)
VALUES($1,$2,'/v1/responses','succeeded','openai_responses','reported',4,1,1,2,1,5,'charged','beichen',$3,
 'fixture-model','2026-11-20 16:01:00+00',12,$4,(SELECT min(id) FROM beichen_price_versions WHERE credential_id=$3),1.1,1.1),
 ($5,$2,'/v1/responses','succeeded','openai_responses',NULL,NULL,NULL,NULL,NULL,NULL,NULL,'pending_review','beichen',$3,
 'fixture-unmapped','2026-11-20 16:02:00+00',NULL,NULL,NULL,NULL,NULL),
 ($6,$2,'/v1/responses','succeeded','openai_responses',NULL,NULL,NULL,NULL,NULL,NULL,NULL,'pending_review','beichen',$3,
 'outside', '2026-11-20 15:59:00+00',NULL,NULL,NULL,NULL,NULL)
`, call, provider, credential, receipt, uuid.New(), uuid.New())
	execTestSQL(t, ctx, tx, "INSERT INTO beichen_receipt_response_claims VALUES($1,$2,NULL)", receipt, call)
	execTestSQL(t, ctx, tx, "INSERT INTO beichen_call_receipts VALUES($1,$2,$3,12,'fixture-model',0.01)", call, receipt, credential)

	window := dashboardWindow{Range: "today", Granularity: "day", StartDate: "2026-11-21", EndDate: "2026-11-21", Timezone: "Asia/Shanghai"}
	local, err := readDashboardLocalRows(ctx, tx, provider, window)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 2 {
		t.Fatalf("expected one charged and one unmapped row in the Beijing window, found %d", len(local))
	}
	first := dashboardFixture(t).Record
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var web dashboardUsageRecord
	if err := json.Unmarshal(firstJSON, &web); err != nil {
		t.Fatal(err)
	}
	second := web
	second.ID, second.KeyID = 102, 11
	second.CreatedAt = time.Date(2026, 11, 21, 10, 0, 0, 0, beijing)
	var reference dashboardSum
	for _, record := range []dashboardUsageRecord{web, second} {
		if err := reference.add(record.metrics()); err != nil {
			t.Fatal(err)
		}
	}
	d := dashboardSession{}
	report := DashboardReport{dashboardWindow: window, Totals: reference.metrics(), Keys: []dashboardKey{{ID: 12, Name: "fixture-claude"}}}
	got, err := d.compareLocalRows(report, []dashboardUsageRecord{web, second}, local)
	if err != nil {
		t.Fatal(err)
	}
	coverage := got.Coverage
	if got.Currency != "CNY" || !got.WebsiteCheck.Matches || coverage.MatchedRows != 1 ||
		coverage.WebsiteOnlyRows != 1 || coverage.LocalOnlyRows != 1 || coverage.UnmappedLocalRows != 1 ||
		coverage.LocalCoverage == nil || *coverage.LocalCoverage != 0.5 ||
		coverage.WebsiteCoverage == nil || *coverage.WebsiteCoverage != 0.5 ||
		coverage.LocalReceiptCoverage == nil || *coverage.LocalReceiptCoverage != 0.5 {
		t.Fatalf("wrong coverage: %+v", coverage)
	}
	for _, line := range got.Lines {
		switch line.Status {
		case "matched":
			if !line.Delta.allZero() || line.Local.ReceiptCalls != 1 || line.Local.PendingCalls != 0 ||
				line.StudentDelta == nil || *line.StudentDelta != "0.001" || line.Local.StudentCredits == nil ||
				*line.Local.StudentCredits != "1.1" || line.Local.StandardSource != "pinned_base_token_rate_estimate" ||
				len(line.MissingReasons) != 0 {
				t.Fatalf("wrong matched amounts: %+v", line)
			}
		case "website_only":
			if line.Delta.Requests != 1 || line.Delta.Actual != nil || len(line.MissingReasons) < 2 {
				t.Fatalf("wrong website-only amounts: %+v", line)
			}
		case "local_only":
			if line.KeyID != 0 || line.Delta.Requests != -1 || line.Delta.Actual != nil ||
				line.Local.PendingCalls != 1 || line.Local.Tokens != nil {
				t.Fatalf("wrong unmapped local amounts: %+v", line)
			}
		default:
			t.Fatalf("unexpected matching status: %s", line.Status)
		}
	}
}
