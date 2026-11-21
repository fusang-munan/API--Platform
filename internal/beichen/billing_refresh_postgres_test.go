package beichen

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestBeichenBillingGroupRefreshPostgres(t *testing.T) {
	ctx, db, provider, teacher, keys := newActionsPostgres(t)
	exec := func(query string, args ...any) { t.Helper(); execCatalogTestSQL(t, ctx, db, query, args...) }
	const secret, bearer, model = "sk-fixture-cc1", "fixture-web-session", "claude-sonnet-5"
	key := keys[3]
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential(secret)
	if err != nil {
		t.Fatal(err)
	}
	exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", key, ciphertext)
	exec("UPDATE provider_credentials SET status='inactive' WHERE id<>$1", key)
	exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,'CC1',sha256('cc1-fixture'::bytea),false)`, key)
	exec(`INSERT INTO beichen_datasets(credential_id,kind,next_attempt) VALUES($1,'key_usage',now()+interval '1 hour')`, key)
	service := New(db, manager)
	if err := service.ConfigureWebSessionTokens("billing@example.test", bearer, "fixture-web-refresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	access, err := service.DeploymentPanelAccess(15, 41, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetPanelAccess(ctx, provider, key, teacher, access); err != nil {
		t.Fatal(err)
	}

	var directoryReads atomic.Int32
	group, peak := 15, "1"
	websiteStatus := http.StatusOK
	directory, err := os.ReadFile("testdata/billing-group-switch.json")
	if err != nil {
		t.Fatal(err)
	}
	service.client.Transport = panelTransport(func(req *http.Request) (*http.Response, error) {
		reply := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		if req.Method == http.MethodGet && req.URL.Host == publicKeyHost && req.Header.Get("Authorization") == "Bearer "+bearer {
			switch req.URL.Path {
			case "/api/v1/keys":
				return reply(http.StatusOK, websiteKeyListFixture(secret, "CC1", 41, group))
			case "/api/v1/channels/available":
				directoryReads.Add(1)
				body := strings.ReplaceAll(string(directory), `"peak_rate_enabled": false`, `"peak_rate_enabled": false, "peak_rate_multiplier": `+peak)
				return reply(websiteStatus, body)
			case "/api/v1/usage":
				return reply(http.StatusOK, emptyWebsiteUsageFixture)
			}
		}
		t.Errorf("unexpected fixture request: %s %s", req.Method, req.URL.Path)
		return nil, errors.New("fixture only supports metadata requests")
	})

	overview := func() Overview {
		t.Helper()
		view, err := service.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	billing := func(view Overview, state, multiplier string, group int64) Dataset {
		t.Helper()
		for _, row := range view.Datasets {
			if row.CredentialID == key && row.Kind == "billing" && row.BillingState == state &&
				row.GroupID != nil && *row.GroupID == group && row.EffectiveMultiplier != nil &&
				row.EffectiveMultiplier.String() == multiplier && row.ObservedAt != nil && row.LastSuccess != nil {
				return row
			}
		}
		t.Fatalf("billing overview should report group %d, rate %s, state %s: %+v", group, multiplier, state, view.Datasets)
		return Dataset{}
	}
	multiplierVersion := func(count int) int64 {
		t.Helper()
		var id int64
		var versions int
		if err := db.QueryRowContext(ctx, `SELECT max(id),count(*) FROM beichen_multiplier_versions WHERE credential_id=$1`, key).
			Scan(&id, &versions); err != nil || versions != count {
			t.Fatalf("expected %d multiplier versions, got %d: %v", count, versions, err)
		}
		return id
	}
	findModel := func(view Overview, name string) CatalogModel {
		t.Helper()
		for _, row := range view.ModelCatalog {
			if row.Model == name {
				return row
			}
		}
		t.Fatalf("missing %s in teacher catalogue", name)
		return CatalogModel{}
	}
	if err := service.syncWithWebsite(ctx, nil); err != nil {
		t.Fatal(err)
	}
	billing(overview(), "current", "0.15", 15)
	oldVersion := multiplierVersion(1)
	if directoryReads.Load() != 2 {
		t.Fatal("automatic sync must refresh both the provider directory and the selected Key's group")
	}

	org, project, student, studentKey, schoolModel := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec("INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture Org',now(),now())", org)
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
VALUES($1,'Fixture Student','learner@example.test','fixture','student',now(),now())`, student)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
VALUES($1,$2,'Fixture Project',10000,now(),now())`, project, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
VALUES(gen_random_uuid(),$1,$2,10000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,project_id,owner_user_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
VALUES($1,$2,$3,'Fixture Key','active','fixture-hash','fixture',now(),now(),now())`, studentKey, project, student)
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,$2,$3,'active',1000,now(),now())`, schoolModel, model, model)
	exec("INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())", studentKey, schoolModel)
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,status,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,'anthropic','active',10,now(),now())`, schoolModel, provider, key, model)
	store := usage.NewStore(db)
	dispatch := func() (usage.Reservation, usage.CallRoute) {
		t.Helper()
		route := usage.CallRoute{ProviderID: provider, CredentialID: key, UpstreamModelName: model, Beichen: true}
		call, err := store.Reserve(ctx, studentKey, schoolModel, uuid.NewString(), "anthropic", "/v1/messages", "", "", usage.CallCorrelation{}, route)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareDispatch(ctx, call.CallID, route); err != nil {
			t.Fatal(err)
		}
		route.SentAt = time.Now().UTC()
		return call, route
	}
	finalize := func(call usage.Reservation, route usage.CallRoute) {
		t.Helper()
		input, zero := int64(10_000), int64(0)
		metrics := usage.CallMetrics{State: usage.UsageReported, PromptTokens: &input, CompletionTokens: &zero,
			CacheHitTokens: &zero, CacheWriteTokens: &zero}
		if err := store.Finalize(ctx, call, true, "succeeded", "", "", route, metrics); err != nil {
			t.Fatal(err)
		}
	}
	oldCall, oldRoute := dispatch()

	group = 60
	if err := service.SyncCredential(ctx, provider, key); err != nil {
		t.Fatal(err)
	}
	if directoryReads.Load() != 4 {
		t.Fatal("manual sync must read the selected group's price and Key capability")
	}
	view := overview()
	current := billing(view, "current", "0.6", 60)
	if current.GroupRate == nil || *current.GroupRate != "0.6" || current.PeakRate == nil || *current.PeakRate != "1" {
		t.Fatalf("group and peak rate missing from overview: %+v", current)
	}
	newVersion := multiplierVersion(2)
	if routes := findModel(view, model).PreferredRoutes; len(routes) != 1 || routes[0].EffectiveMultiplierText != "0.6" {
		t.Fatalf("model page routes should report 0.6 after one manual sync: %+v", routes)
	}
	for name, reason := range map[string]string{"claude-opus-5": "model_outside_key_group", "claude-key-listed": "price_outside_key_group"} {
		row := findModel(view, name)
		if len(row.CatalogKeys) != 1 || row.CatalogKeys[0].GroupID == nil || *row.CatalogKeys[0].GroupID != 60 ||
			row.CatalogKeys[0].GroupDiagnosis != reason || len(row.WebsiteLocations) == 0 ||
			len(row.WebsiteLocations[0].Groups) != 1 || row.WebsiteLocations[0].Groups[0].ID != 15 ||
			row.WebsiteLocations[0].Groups[0].Rate == nil || row.WebsiteLocations[0].Groups[0].Rate.String() != "0.15" ||
			row.WebsiteLocations[0].PriceConditionsExact["input_price"] == nil {
			t.Fatalf("expected %s with both group prices/rates for %s: %+v", reason, name, row)
		}
		for _, quoted := range view.Models {
			if quoted.Model == name && quoted.UpstreamRates != nil {
				t.Fatalf("price from group 15 was reused for group 60: %+v", quoted)
			}
		}
	}
	newCall, newRoute := dispatch()
	finalize(newCall, newRoute)
	finalize(oldCall, oldRoute) // The old request completes after the group was switched.
	checkCharge := func(call usage.Reservation, version int64, base, charged string) {
		t.Helper()
		var debit, baseDebit, state string
		var billedVersion int64
		err := db.QueryRowContext(ctx, `SELECT trim_scale(beichen_base_credits)::text,trim_scale(student_credits)::text,
beichen_multiplier_version,beichen_state FROM gateway_calls WHERE id=$1`, call.CallID).Scan(&baseDebit, &debit, &billedVersion, &state)
		if err != nil || state != "charged" || baseDebit != base || debit != charged || billedVersion != version {
			t.Fatalf("want base=%s, charged=%s, version=%d; got %s, %s, %d, %s (%v)", base, charged, version, baseDebit, debit, billedVersion, state, err)
		}
	}
	checkCharge(oldCall, oldVersion, "0.15", "0.165")
	checkCharge(newCall, newVersion, "1.2", "1.32")
	t.Logf("1 sync: group 15 → 60, multiplier 0.15 → 0.6, versions %d → %d, charges 0.165 → 1.32", oldVersion, newVersion)

	if err := service.SyncCredential(ctx, provider, key); err != nil {
		t.Fatal(err)
	}
	multiplierVersion(2)
	peak = "0.8"
	if err := service.SyncCredential(ctx, provider, key); err != nil {
		t.Fatal(err)
	}
	multiplierVersion(3)
	current = billing(overview(), "current", "0.6", 60)
	if current.PeakRate == nil || *current.PeakRate != "0.8" {
		t.Fatal("a peak rule change with the same effective multiplier must be saved")
	}
	websiteStatus = http.StatusServiceUnavailable
	for range 3 {
		if err := service.SyncCredential(ctx, provider, key); err == nil {
			t.Fatal("failed website billing read should be reported")
		}
	}
	var oldPriceUsable bool
	if err := db.QueryRowContext(ctx, `SELECT upstream_input IS NOT NULL FROM beichen_price_at($1,$2,statement_timestamp())`, key, model).
		Scan(&oldPriceUsable); err != nil || oldPriceUsable {
		t.Fatalf("old quote remained usable after the multiplier rule changed: %v", err)
	}
	stale := billing(overview(), "stale", "0.6", 60)
	if !stale.LastSuccess.Equal(*current.LastSuccess) || !stale.ObservedAt.Equal(*current.ObservedAt) ||
		stale.LastError == nil || stale.FailureCount != 3 || !stale.Alert {
		t.Fatalf("stale should retain the last success and alert at three failures: %+v", stale)
	}
	multiplierVersion(3)
	unpriced, unpricedRoute := dispatch()
	finalize(unpriced, unpricedRoute)
	var state string
	var debit sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT beichen_state,student_credits::text FROM gateway_calls WHERE id=$1`, unpriced.CallID).
		Scan(&state, &debit); err != nil || state != "pending_review" || debit.Valid {
		t.Fatalf("stale price used for a new call: %s %+v (%v)", state, debit, err)
	}
	websiteStatus = http.StatusOK
	if err := service.SyncCredential(ctx, provider, key); err != nil {
		t.Fatal(err)
	}
	recovered := billing(overview(), "current", "0.6", 60)
	if recovered.FailureCount != 0 || recovered.Alert || !recovered.LastSuccess.After(*stale.LastSuccess) {
		t.Fatalf("billing did not recover: %+v", recovered)
	}
	recoveredVersion := multiplierVersion(4)
	recoveredCall, recoveredRoute := dispatch()
	finalize(recoveredCall, recoveredRoute)
	checkCharge(recoveredCall, recoveredVersion, "1.2", "1.32")
	checkCharge(oldCall, oldVersion, "0.15", "0.165")
	checkCharge(newCall, newVersion, "1.2", "1.32")
	finalize(unpriced, unpricedRoute)
	if err := db.QueryRowContext(ctx, `SELECT beichen_state FROM gateway_calls WHERE id=$1`, unpriced.CallID).
		Scan(&state); err != nil || state != "pending_review" {
		t.Fatalf("recovery should not backdate pricing into the stale interval: %s (%v)", state, err)
	}
	t.Log("stale preserves 0.6 and last_success, alerts at 3 failures, and leaves new costs pending until billing recovers")
}
