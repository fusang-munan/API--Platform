package beichen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestBeichenPerKeyCatalogOverviewPostgres(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable local nebula_test database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || (cfg.User != "nebula_qa" && cfg.User != "nebula_test") || cfg.Database != "nebula_test" ||
		cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Fatal("requires a loopback-only nebula_test database and test role with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	schema := pgx.Identifier{"beichen_catalog_" + uuid.NewString()}.Sanitize()
	execTestSQL(t, ctx, tx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema+", public")
	paths, err := filepath.Glob("../infrastructure/db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatal("locate migrations")
	}
	for _, path := range paths {
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, string(sql))
	}
	var provider uuid.UUID
	if err := tx.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url=$1", origin).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	first, second, alias := uuid.New(), uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
		VALUES ($1,$3,'Gemini','fake',now(),now()),($2,$3,'Claude','fake',now(),now())`, first, second, provider)
	execTestSQL(t, ctx, tx, `INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
		VALUES($1,'school-alias','Alias','active',1000,now(),now()),
		(gen_random_uuid(),'special','Special','pending_configuration',NULL,now(),now())`, alias)
	execTestSQL(t, ctx, tx, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,status,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,$3,'shared','openai_responses',0,'active',now(),now())`, alias, provider, first)

	s := New(nil, nil)
	snapshot := func(id uuid.UUID, body string) {
		t.Helper()
		s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.String() != origin+modelsPath {
				t.Fatal("expected a read-only catalog request")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		result := s.fetchJob(ctx, syncJob{provider: provider, credential: id, kind: "catalog"}, credentialRecord{}, "fake-key", false)
		if result.err != nil || result.save == nil {
			t.Fatalf("fetch catalog: %v", result.err)
		}
		if err := result.save(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	snapshot(first, `{"data":[{"id":"shared"},{"id":"removed"}]}`)
	snapshot(first, `{"data":[{"id":"shared"},{"id":"special"},{"id":"no-quote"}]}`)
	snapshot(second, `{"data":[{"id":"shared"}]}`)
	// A previous price, another key's quote and even an explicit zero have distinct meanings.
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate)
		VALUES($1,'shared','upstream','effective','billing',0,4),($2,'shared','upstream','effective','billing',19,7),
		($1,'removed','upstream','effective','billing',5,4),($2,'no-quote','upstream','effective','billing',7,4)`, first, second)
	read := func() map[uuid.UUID]map[string]Model {
		t.Helper()
		var view Overview
		if err := readModels(ctx, tx, provider, &view); err != nil {
			t.Fatal(err)
		}
		rows := map[uuid.UUID]map[string]Model{first: {}, second: {}}
		for _, row := range view.Models {
			rows[row.CredentialID][row.Model] = row
		}
		return rows
	}
	rows := read()
	a, b := rows[first]["shared"], rows[second]["shared"]
	if len(rows[first]) != 3 || len(rows[second]) != 1 || !a.RouteReady || b.RouteReady ||
		a.SchoolStatus != "active" || b.SchoolStatus != "not_configured" || a.PriceStatus != "confirmed" ||
		a.UpstreamRates == nil || *a.UpstreamRates.Input != "0" || b.UpstreamRates == nil || *b.UpstreamRates.Input != "19" {
		t.Fatalf("incorrect per-key route or price: %+v", rows)
	}
	for name, status := range map[string]string{"special": "pending_configuration", "no-quote": "not_configured"} {
		row := rows[first][name]
		if row.PriceStatus != "pending_confirmation" || row.UpstreamRates != nil || row.SchoolRates != nil ||
			row.SchoolStatus != status || row.RouteReady {
			t.Fatalf("unexpected unpriced model: %+v", row)
		}
	}
	// A new group sample supersedes an older flat quote if the upstream
	// changes only that model to an unsupported pricing format.
	var revision time.Time
	if err := tx.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", first).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	rate := int64(12)
	quote := panelQuote{rows: []priceRow{{Model: "shared", Rates: Rates{Input: &rate, Output: &rate}},
		{Model: "special", Rates: Rates{Input: &rate, Output: &rate}}}, expiresAt: time.Now().Add(time.Minute)}
	saveQuote := func() {
		t.Helper()
		if err := saveBillingObservation(ctx, tx, first, revision, billingInfo{Multiplier: "0.03125"}, nil, 1, quote); err != nil {
			t.Fatal(err)
		}
	}
	saveQuote()
	if read()[first]["special"].PriceStatus != "confirmed" {
		t.Fatal("missing original flat quote")
	}
	quote.rows = quote.rows[:1]
	const actualTerms = `{"billing_mode":"token","input_price":0.000008,"output_price":0.000016,
		"cache_read_price":0,"cache_write_price":0.000001,"cache_write_1h_price":0.000002,
		"cache_write_12h_price":0.000004,"intervals":[{"min_tokens":200000,"input_price":0.000012000000000000000019}]}`
	quote.terms = map[string]json.RawMessage{"special": json.RawMessage(actualTerms)}
	saveQuote()
	rows = read()
	special := rows[first]["special"]
	if special.PriceStatus != "conditional" || special.UpstreamRates != nil || special.SchoolRates != nil ||
		rows[first]["shared"].PriceStatus != "confirmed" || *rows[first]["shared"].UpstreamRates.Input != "0.375" ||
		*rows[first]["shared"].SchoolRates.Input != "37.5" || *rows[second]["shared"].UpstreamRates.Input != "19" {
		t.Fatalf("obsolete or cross-key quote survived a new panel snapshot: %+v", rows)
	}
	var terms struct {
		CacheRead     *json.Number `json:"cache_read_price"`
		CacheWrite1h  json.Number  `json:"cache_write_1h_price"`
		CacheWrite12h json.Number  `json:"cache_write_12h_price"`
		Intervals     []struct {
			Min   int         `json:"min_tokens"`
			Input json.Number `json:"input_price"`
		} `json:"intervals"`
	}
	if json.Unmarshal(special.PriceConditions, &terms) != nil || terms.CacheRead == nil ||
		!samePanelRate(*terms.CacheRead, "0") || !samePanelRate(terms.CacheWrite1h, "0.000002") ||
		!samePanelRate(terms.CacheWrite12h, "0.000004") || len(terms.Intervals) != 1 ||
		terms.Intervals[0].Min != 200000 || !samePanelRate(terms.Intervals[0].Input, "0.000012000000000000000019") {
		t.Fatal("cache durations or interval conditions changed")
	}
	exact, err := json.Marshal(special.PriceConditionsExact)
	if err != nil || !strings.Contains(string(exact), `"input_price":"0.000012000000000000000019"`) ||
		!strings.Contains(string(exact), `"min_tokens":"200000"`) {
		t.Fatal("conditional quote lost precision")
	}
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_datasets(credential_id,kind,last_success)
		VALUES($1,'billing',clock_timestamp()),($2,'billing',clock_timestamp())`, first, second)
	execTestSQL(t, ctx, tx, `INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,0.01)`, second)
	readRoute := func(upstream, school, adapter string) PreferredRoute {
		t.Helper()
		var view Overview
		if err := readPreferredRoutes(ctx, tx, provider, &view); err != nil {
			t.Fatal(err)
		}
		if len(view.ModelCatalog) != 3 {
			t.Fatalf("expected three distinct models: %+v", view.ModelCatalog)
		}
		for _, m := range view.ModelCatalog {
			if m.Model != upstream {
				continue
			}
			if upstream == "shared" && len(m.CredentialIDs) != 2 {
				t.Fatal("lost shared credential")
			}
			for _, r := range m.PreferredRoutes {
				if r.SchoolModel == school && r.Adapter == adapter {
					return r
				}
			}
		}
		t.Fatalf("missing route %s %s %s", upstream, school, adapter)
		return PreferredRoute{}
	}
	preferred := readRoute("shared", "school-alias", "openai_responses")
	if preferred.State != "ready" || preferred.CredentialID == nil || *preferred.CredentialID != first ||
		preferred.EffectiveMultiplier == nil || !samePanelRate(*preferred.EffectiveMultiplier, "0.03125") {
		t.Fatalf("configured key should win even when another key charges 0.01: %+v", preferred)
	}
	preview := readRoute("no-quote", "no-quote", "openai_responses")
	if preview.State != "candidate" || preview.CredentialID == nil || *preview.CredentialID != first || preview.Priority != 50 {
		t.Fatalf("unconfigured model must retain a prospective channel: %+v", preview)
	}
	// A different supplier can win the school's route for the same protocol.
	other, otherKey := uuid.New(), uuid.New()
	execTestSQL(t, ctx, tx, `INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES($1,'Other','https://other.invalid',now(),now())`, other)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES($1,$2,'external','fake',now(),now())`, otherKey, other)
	execTestSQL(t, ctx, tx, `INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,'remote-alias',now(),now(),now(),now())`, other, otherKey)
	execTestSQL(t, ctx, tx, `UPDATE model_bindings SET priority=90 WHERE model_id=$1`, alias)
	execTestSQL(t, ctx, tx, `INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,status,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,$2,$3,'remote-alias','openai_responses',10,'active',now(),now())`, alias, other, otherKey)
	external := readRoute("shared", "school-alias", "openai_responses")
	if external.ProviderID != other || external.EffectiveMultiplier != nil || external.UpstreamModel != "remote-alias" {
		t.Fatalf("preferred external route borrowed Beichen's rate: %+v", external)
	}
	execTestSQL(t, ctx, tx, "UPDATE provider_credentials SET status='inactive' WHERE id=$1", first)
	if read()[first]["shared"].RouteReady {
		t.Fatal("inactive key reported an active route")
	}
	snapshot(first, `{"data":[]}`)
	if rows := read(); len(rows[first]) != 0 || len(rows[second]) != 1 {
		t.Fatal("empty snapshot changed another key or retained historical models")
	}
}
