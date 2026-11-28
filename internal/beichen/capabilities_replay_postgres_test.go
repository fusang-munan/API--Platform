package beichen

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

func TestBeichenCapabilityRescanAndReplayPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	key, school := keys[3], uuid.New()
	exec := func(query string, args ...any) {
		t.Helper()
		execCatalogTestSQL(t, ctx, db, query, args...)
	}
	manager, err := security.NewManager([]byte("fixture-signing-key"), nil, []byte("fixture-api-pepper"), make([]byte, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const model = "claude-fixture-replay"
	const fakeKey = "sk-fixture-replay"
	cipher, err := manager.EncryptCredential(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	exec("UPDATE provider_credentials SET status='inactive' WHERE id<>$1", key)
	exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", key, cipher)
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
 VALUES($1,$2::text,$2::text,'active',1000,now(),now())`, school, model)
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
 VALUES(gen_random_uuid(),$1,$2,$3,$4,'anthropic',25,now(),now())`, school, provider, key, model)
	saveCatalogFixture(t, ctx, db, provider, key, nil, []string{"Claude-Fixture-Replay"})

	read := func() catalog.ModelCapability {
		t.Helper()
		view, err := catalog.ReadBeichenCapabilities(ctx, db, provider)
		if err != nil || len(view.Capabilities) != 1 {
			t.Fatalf("wanted one capability for the scoped key, got %+v (%v)", view, err)
		}
		return view.Capabilities[0]
	}
	if before := read(); before.CatalogState != "website_only" || before.CredentialID == nil || *before.CredentialID != key ||
		!strings.Contains(before.ActionHint, "CC1") || !strings.Contains(before.ActionHint, "重扫") {
		t.Fatalf("website-only model did not identify its Key: %+v", before)
	}

	s := New(db, manager)
	const bearer = "fixture-replay-web"
	if err := s.ConfigureWebSessionTokens("replay@example.test", bearer, "fixture-replay-refresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	directory := websiteGroupFixture(7, "1.25", `
	  {"name":"claude-fixture-replay","supported_protocols":["anthropic","openai_compatible"],
	   "pricing":{"input_price":0.0000000001,"output_price":0.0000000005,"cache_read_price":0,"cache_write_price":0}}`)
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "20")
	var scans, groupReads, usageReads, messages, chat atomic.Int32
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		status := 200
		if r.URL.Host == publicKeyHost && r.Method == http.MethodGet && r.Header.Get("Authorization") == "Bearer "+bearer {
			switch r.URL.Path {
			case "/api/v1/keys":
				body = websiteKeyListFixture(fakeKey, "CC1", 41, 7)
			case "/api/v1/groups/rates":
				body = `{"code":0,"data":{"7":1.25}}`
			case "/api/v1/channels/available":
				groupReads.Add(1)
				body = directory
			case "/api/v1/usage":
				usageReads.Add(1)
				at := r.URL.Query().Get("start_date") + "T00:00:01+08:00"
				if r.URL.Query().Get("api_key_id") != "41" {
					return nil, errors.New("usage request used an unexpected Key")
				}
				body = fmt.Sprintf(`{"code":0,"data":{"page":1,"pages":1,"page_size":100,"total":1,"items":[
 {"id":1,"user_id":13,"api_key_id":41,"model":%q,"created_at":%q,"currency":"CNY","input_tokens":10,
  "output_tokens":1,"cache_read_tokens":0,"cache_creation_tokens":0,"actual_cost":0.000003}]}}`, model, at)
			}
		} else if r.URL.Host == "api.beichenapi.com" {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == modelsPath && r.Header.Get("Authorization") == "Bearer "+fakeKey:
				scans.Add(1)
				body = `{"data":[{"id":"claude-fixture-replay","supported_protocols":["anthropic","openai_compatible"]}]}`
			case r.Method == http.MethodPost && r.URL.Path == "/v1/messages" && r.Header.Get("x-api-key") == fakeKey:
				messages.Add(1)
				status, body = 403, `{"type":"error","error":{"type":"permission_error"}}`
			case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" && r.Header.Get("Authorization") == "Bearer "+fakeKey:
				chat.Add(1)
				body = `{"choices":[{}]}`
			}
		}
		if body == "" {
			return nil, errors.New("unexpected path or credential in fake upstream")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	if err := s.RescanCatalog(ctx, provider, key); err != nil {
		t.Fatal(err)
	}
	if after := read(); after.CatalogState != "key_listed" || after.BindingState != "ready" ||
		after.Availability != catalog.AvailabilityUnavailable || after.ProbeState != "unprobed" ||
		after.PriceBlocker == nil || after.PriceBlocker.Code != "missing_price" {
		t.Fatalf("rescan did not discover and restore the registered route: %+v", after)
	}

	type counts struct {
		bindings, catalogRows, probes, priceVersions, multiplierVersions, days, actual, studentCalls int
	}
	check := func() counts {
		t.Helper()
		var v counts
		err := db.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM model_bindings), (SELECT count(*) FROM provider_model_catalog),
       (SELECT count(*) FROM upstream_probes), (SELECT count(*) FROM beichen_price_versions),
       (SELECT count(*) FROM beichen_multiplier_versions), (SELECT count(*) FROM beichen_key_usage_days),
       (SELECT (COALESCE(sum((daily_usage->>'actual_cost')::numeric),0)*1000000)::bigint FROM beichen_key_usage_days),
       (SELECT count(*) FROM gateway_calls)`).
			Scan(&v.bindings, &v.catalogRows, &v.probes, &v.priceVersions, &v.multiplierVersions, &v.days, &v.actual, &v.studentCalls)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	want := counts{bindings: 2, catalogRows: 1, probes: 2, multiplierVersions: 1, days: 2, actual: 6}
	for attempt := range 2 {
		if err := s.Sync(ctx, provider); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RequestProbes(ctx, provider); err != nil {
			t.Fatal(err)
		}
		job, err := s.claimProbeJob(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		s.runProbeJob(ctx, job)
		recent, err := s.LatestProbes(ctx, provider)
		if err != nil || recent.Run.State != "completed" || len(recent.Results) != 2 {
			t.Fatalf("probe run %d did not persist both protocols: %+v %v", attempt+1, recent, err)
		}
		preview, err := s.Rebind(ctx, provider, actor, true, "")
		if err != nil || len(preview.Changes) != 1-attempt {
			t.Fatalf("rebind preview %d: %+v %v", attempt+1, preview, err)
		}
		if _, err := s.Rebind(ctx, provider, actor, false, preview.PreviewToken); err != nil {
			t.Fatal(err)
		}
		if got := read(); got.Availability != "available" || got.RecommendedProtocol == nil || *got.RecommendedProtocol != "openai_compatible" {
			t.Fatalf("passing Chat did not become the available recommendation: %+v", got)
		}
		current := check()
		want.priceVersions = attempt + 1
		if current != want {
			t.Fatalf("replay round %d: got %+v, want %+v", attempt+1, current, want)
		}
	}
	// Both manual sweeps re-probe the protocols without duplicating stored rows.
	if scans.Load() != 3 || groupReads.Load() != 6 || usageReads.Load() != 4 || messages.Load() != 2 || chat.Load() != 2 {
		t.Fatalf("unexpected fake requests: catalog=%d group=%d usage=%d messages=%d chat=%d",
			scans.Load(), groupReads.Load(), usageReads.Load(), messages.Load(), chat.Load())
	}
}

func websiteKeyListFixture(secret, name string, id, group int) string {
	return fmt.Sprintf(`{"code":0,"data":{"items":[{"id":%d,"user_id":13,"key":%q,"group_id":%d,"name":%q,"status":"active"}],
		"total":1,"page":1,"page_size":100,"pages":1}}`, id, secret, group, name)
}

func websiteGroupFixture(group int, multiplier, models string) string {
	return fmt.Sprintf(`{"code":0,"data":[{"id":5,"name":"Fixture website channel","status":"active","platforms":[
		{"platform":"anthropic","groups":[{"id":%d,"platform":"anthropic","subscription_type":"standard",
		"rate_multiplier":%s,"peak_rate_enabled":false}],"supported_models":[%s]}]}]}`, group, multiplier, models)
}

const emptyWebsiteUsageFixture = `{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":100,"pages":0}}`
