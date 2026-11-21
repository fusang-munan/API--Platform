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
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "20")
	var scans, billing, usageReads, messages, chat atomic.Int32
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.beichenapi.com" {
			return nil, errors.New("unexpected fixture host")
		}
		var body string
		status := 200
		switch {
		case r.Method == http.MethodGet && r.Header.Get("Authorization") == "Bearer "+fakeKey:
			switch r.URL.Path {
			case modelsPath:
				scans.Add(1)
				body = `{"data":[{"id":"claude-fixture-replay","supported_protocols":["anthropic","openai_compatible"]}]}`
			case billingPath:
				billing.Add(1)
				body = `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","effective_rate_multiplier":1.25,
 "pricing":{"basis":"base","unit":"usd_micros_per_million_tokens","models":[{"model":"claude-fixture-replay","input":100,"output":500,"cache_read":0,"cache_write":0}]}}`
			case usagePath:
				usageReads.Add(1)
				body = fmt.Sprintf(`{"mode":"unrestricted","unit":"USD","date":%q,"timezone":"Asia/Shanghai",
 "model_stats":[{"model":%q,"requests":1,"input_tokens":10,"output_tokens":1,"actual_cost":0.000003}]}`, r.URL.Query().Get("start_date"), model)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages" && r.Header.Get("x-api-key") == fakeKey:
			messages.Add(1)
			status, body = 403, `{"type":"error","error":{"type":"permission_error"}}`
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" && r.Header.Get("Authorization") == "Bearer "+fakeKey:
			chat.Add(1)
			body = `{"choices":[{}]}`
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
		after.Availability != "unverified" || after.RecommendedProtocol == nil || *after.RecommendedProtocol != "anthropic" {
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
       (SELECT count(*) FROM beichen_multiplier_versions), (SELECT count(*) FROM beichen_daily_usage),
       (SELECT COALESCE(sum(billed_upstream_micros),0) FROM beichen_daily_usage),
       (SELECT count(*) FROM gateway_calls)`).
			Scan(&v.bindings, &v.catalogRows, &v.probes, &v.priceVersions, &v.multiplierVersions, &v.days, &v.actual, &v.studentCalls)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	var first counts
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
		if attempt == 0 {
			first = current
			want := counts{bindings: 2, catalogRows: 1, probes: 2, priceVersions: 1, multiplierVersions: 1, days: 2, actual: 6}
			if first != want {
				t.Fatalf("incorrect first reconciliation: got %+v, want %+v", first, want)
			}
		} else if current != first {
			t.Fatalf("replay changed bindings, probes, or billing: first %+v second %+v", first, current)
		}
	}
	if scans.Load() != 3 || billing.Load() != 2 || usageReads.Load() != 4 || messages.Load() != 1 || chat.Load() != 1 {
		t.Fatalf("unexpected fake requests: catalog=%d billing=%d usage=%d messages=%d chat=%d",
			scans.Load(), billing.Load(), usageReads.Load(), messages.Load(), chat.Load())
	}
}
