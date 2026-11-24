package dataplane

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenPendingBacklogPostgres(t *testing.T) {
	f := newPendingGateway(t)
	send := func(id, counters string) {
		t.Helper()
		f.respond(counters)
		if w := f.request(id); w.Code != 200 {
			t.Fatalf("%s: HTTP %d", id, w.Code)
		}
	}
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	send("narrow", `,"total_tokens":102`)
	send("wide", `,"total_tokens":100002`)
	send("unpriced", `,"total_tokens":102`)
	f.exec(t, `UPDATE gateway_calls SET beichen_price_at=NULL WHERE request_id='unpriced'`)
	send("other", `,"total_tokens":102`)
	other := uuid.New()
	f.exec(t, `INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES ($1,'other','https://fixture.invalid',now(),now())`, other)
	f.exec(t, `UPDATE gateway_calls SET provider_id=$1 WHERE request_id='other'`, other)
	f.exec(t, `UPDATE gateway_calls SET unresolved_credit_milli=1 WHERE request_id='wide'`)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "1")
	audit := func(apply bool, limit int, token string) usage.PendingBacklogReport {
		t.Helper()
		data, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, apply, limit, 0, token)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	stale := func(limit int, token string) {
		t.Helper()
		_, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, true, limit, 0, token)
		if domain.ErrorCode(err) != domain.CodePendingAuditChanged {
			t.Fatalf("expected stale token: %v", err)
		}
	}
	// xmin changes on every UPDATE, even if all business values stay equal.
	versions := func() string {
		t.Helper()
		var parts []string
		for _, table := range []string{"gateway_calls", "gateway_call_turns", "monitored_inputs", "monthly_usage_cube", "project_month_credit_buckets", "project_member_month_credit_buckets", "api_key_month_credit_buckets"} {
			var version string
			if err := f.db.QueryRowContext(f.ctx, "SELECT COALESCE(string_agg(xmin::text,',' ORDER BY xmin::text),'') FROM "+table).Scan(&version); err != nil {
				t.Fatal(err)
			}
			parts = append(parts, version)
		}
		return strings.Join(parts, "/")
	}
	before := versions()
	first := audit(false, 1, "")
	all := audit(false, 0, "")
	if all.Total != 3 || all.Narrow != 1 || all.Wide != 1 || all.Unknown != 1 || all.AutoSettle != 1 || all.Rows[0].StudentCredits != "0.01562" || all.MaxErrorYuan <= 0 || all.EstimatedAutoYuan != 0.000142 {
		t.Fatalf("preview: %+v", all)
	}
	if all.BacklogTotal != 3 || all.RemainingPending != 3 || len(all.Rows) != 3 || all.Rows[2].Class != "unknown" || all.Rows[2].Auto || !strings.Contains(all.Rows[2].Note, "缺少派发价格") {
		t.Fatalf("unpriced call missing from backlog: %+v", all)
	}
	if first.Total != 1 || first.BacklogTotal != 3 || first.Rows[0].ID != all.Rows[0].ID {
		t.Fatalf("limit: %+v", first)
	}
	stale(200, "")
	stale(200, "wrong")
	if before != versions() {
		t.Fatal("preview or invalid token wrote to the ledger")
	}
	send("new-unknown", "")
	f.exec(t, `UPDATE gateway_calls SET beichen_price_at=NULL WHERE request_id='new-unknown'`)
	stale(1, first.PreviewToken)
	stale(200, all.PreviewToken)
	latest := audit(false, 5000, "")
	booked := audit(true, 500, latest.PreviewToken)
	if booked.Charged != 1 || booked.StillPending != 3 || booked.Skipped != 0 || booked.Errors != 0 || booked.CreditsCharged != "0.01562" || booked.RemainingPending != 3 {
		t.Fatalf("booked: %+v", booked)
	}
	stale(500, latest.PreviewToken)
	var ok bool
	err := f.db.QueryRowContext(f.ctx, `SELECT bool_and(CASE request_id
 WHEN 'narrow' THEN beichen_state='charged' AND student_credits=0.01562 AND unresolved_credit_milli=0
 WHEN 'wide' THEN beichen_state='pending_review' AND unresolved_credit_milli=1
 WHEN 'unpriced' THEN beichen_state='pending_review' AND beichen_price_at IS NULL AND student_credits IS NULL
 WHEN 'other' THEN beichen_state='pending_review' AND unresolved_credit_milli=1
 ELSE beichen_state='pending_review' AND unresolved_credit_milli=10 END) FROM gateway_calls`).Scan(&ok)
	if err != nil || !ok {
		t.Fatalf("unexpected balances: %v", err)
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, "narrow", "0.01562", 1)
}

func TestBeichenPendingBacklogPagesPostgres(t *testing.T) {
	f := newPendingGateway(t)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	f.respond(`,"total_tokens":102`)
	if w := f.request("last"); w.Code != 200 {
		t.Fatalf("HTTP %d", w.Code)
	}
	// Put 200 unpriced calls before the single eligible call.
	f.exec(t, `INSERT INTO gateway_calls SELECT (jsonb_populate_record(NULL::gateway_calls,
 to_jsonb(c) || jsonb_build_object('id',gen_random_uuid(),'request_id','old-'||i,'turn_id',NULL,
 'beichen_price_at',NULL,'created_at',c.created_at - interval '1 day'))).*
 FROM gateway_calls c CROSS JOIN generate_series(1,200) i WHERE request_id='last'`)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "1")
	page := func(offset int) usage.PendingBacklogReport {
		t.Helper()
		r, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, false, 0, offset, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := page(0)
	if first.Total != 200 || first.Unknown != 200 || first.AutoSettle != 0 || first.BacklogTotal != 201 || first.NextOffset == nil || *first.NextOffset != 200 {
		t.Fatalf("first page: total=%d unknown=%d backlog=%d next=%v", first.Total, first.Unknown, first.BacklogTotal, first.NextOffset)
	}
	last := page(*first.NextOffset)
	if last.Total != 1 || last.AutoSettle != 1 || last.BacklogTotal != 201 || last.NextOffset != nil || last.PreviewToken == first.PreviewToken {
		t.Fatalf("last page: %+v", last)
	}
	for _, mismatch := range []struct {
		offset int
		token  string
	}{{200, first.PreviewToken}, {0, last.PreviewToken}, {202, page(201).PreviewToken}} {
		if _, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, true, 200, mismatch.offset, mismatch.token); domain.ErrorCode(err) != domain.CodePendingAuditChanged {
			t.Fatalf("accepted wrong page at %d: %v", mismatch.offset, err)
		}
	}
	r, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, true, 200, 200, last.PreviewToken)
	if err != nil || r.Charged != 1 || r.RemainingPending != 200 || r.CreditsCharged != "0.01562" {
		t.Fatalf("book last page: %+v %v", r, err)
	}
}

func TestBeichenPendingBacklogConfirmationPostgres(t *testing.T) {
	f := newPendingGateway(t)
	f.db.SetMaxOpenConns(8)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	f.respond(`,"total_tokens":102`)
	if w := f.request("eligible"); w.Code != 200 {
		t.Fatalf("HTTP %d", w.Code)
	}
	// A historical call with complete counters has an exact upper bound.
	f.exec(t, `UPDATE gateway_calls SET cache_hit_tokens=50,cache_write_tokens=50,usage_state='reported',usage_reason=NULL WHERE request_id='eligible'`)
	f.respond(`,"total_tokens":102,"cache_creation":{"ephemeral_5m_input_tokens":60}`)
	if w := f.request("subtotal"); w.Code != 200 {
		t.Fatalf("HTTP %d", w.Code)
	}
	f.exec(t, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,actor_id)
 VALUES($1,$2,'school','manual','teacher',2000000000,2000000000,2000000000,4000000000,$3)`, f.credential, upstreamModel, f.teacher)
	f.respond(`,"total_tokens":102`)
	if w := f.request("manual-over-bound"); w.Code != 200 {
		t.Fatalf("HTTP %d", w.Code)
	}
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "1")
	preview, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, false, 200, 0, "")
	if err != nil || preview.AutoSettle != 1 || preview.Narrow != 2 || preview.Exact != 1 || preview.Rows[0].StudentCredits != "0.01562" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	type outcome struct {
		report usage.PendingBacklogReport
		err    error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, true, 200, 0, preview.PreviewToken)
			done <- outcome{result, err}
		}()
	}
	var charged int
	for range 2 {
		got := <-done
		if got.err != nil && domain.ErrorCode(got.err) != domain.CodePendingAuditChanged {
			t.Fatal(got.err)
		}
		if got.report.Errors != 0 {
			t.Fatalf("row failures: %+v", got.report)
		}
		charged += got.report.Charged
	}
	if charged != 1 {
		t.Fatalf("charged %d, expected exactly one debit", charged)
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, "eligible", "0.01562", 1)
}

func TestBeichenPendingBacklogPartialFailurePostgres(t *testing.T) {
	f := newPendingGateway(t)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	f.respond(`,"total_tokens":102`)
	for _, id := range []string{"fault", "ok"} {
		if w := f.request(id); w.Code != 200 {
			t.Fatalf("HTTP %d", w.Code)
		}
	}
	f.exec(t, `UPDATE gateway_calls SET unresolved_credit_milli=9 WHERE request_id='fault'`)
	f.exec(t, `CREATE FUNCTION fail_test_charge() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'test charge failed'; END $$`)
	f.exec(t, `CREATE TRIGGER fail_charge BEFORE UPDATE ON gateway_calls FOR EACH ROW
 WHEN (NEW.request_id='fault' AND NEW.beichen_state='charged') EXECUTE FUNCTION fail_test_charge()`)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "1")
	run := func() usage.PendingBacklogReport {
		t.Helper()
		preview, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, false, 200, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		result, err := usage.PendingAuditBacklog(f.ctx, f.db, f.provider, true, 200, 0, preview.PreviewToken)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	booked := run()
	if booked.Errors != 1 || booked.Charged != 1 || booked.RemainingPending != 1 || booked.CreditsCharged != "0.01562" {
		t.Fatalf("partial result: %+v", booked)
	}
	var reserve int
	if err := f.db.QueryRowContext(f.ctx, `SELECT unresolved_credit_milli FROM gateway_calls WHERE request_id='fault'`).Scan(&reserve); err != nil || reserve != 9 {
		t.Fatalf("failed row must retain its original reserve: %d, %v", reserve, err)
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, "ok", "0.01562", 1)
	f.exec(t, `DROP TRIGGER fail_charge ON gateway_calls`)
	retry := run()
	if retry.Errors != 0 || retry.Charged != 1 || retry.RemainingPending != 0 {
		t.Fatalf("retry: %+v", retry)
	}
	assertBeichenGatewayMonth(t, f.ctx, f.db, "ok", "0.03124", 2)
}
