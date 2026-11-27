package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	dbstore "github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

func TestScheduledWebsitePricesPostgres(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable loopback nebula_test database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Database != "nebula_test" || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Fatal("requires loopback nebula_test with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := pgx.Identifier{"scheduled_prices_" + uuid.NewString()}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	cfg.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := dbstore.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(6)

	testKey := bytes.Repeat([]byte{6}, 32)
	manager, err := security.NewManager(testKey, testKey, testKey, testKey, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := New(db, manager)
	if err := service.ConfigureWebSessionTokens("fixture@school.invalid", "initial-access", "initial-refresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var provider uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url=$1", origin).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at)
VALUES($1,'fixture','fixture@audit.invalid','!','student','disabled',now(),now())`, ProvisionActorID)
	names := [...]string{"Gemini", "GM1", "G3", "CC1"}
	platforms := [...]string{"gemini", "deepseek", "openai", "anthropic"}
	multipliers := [...]int{30, 8, 20, 15}
	currentGroups := [...]int{20, 21, 22, 83}
	secrets := [...]string{"sk-fixture-gemini", "sk-fixture-gm1", "sk-fixture-g3", "sk-fixture-cc1"}
	ids := [len(names)]uuid.UUID{}
	var keys []map[string]any
	for i, name := range names {
		ids[i] = uuid.New()
		cipher, err := manager.EncryptCredential(secrets[i])
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,$3,$4,now(),now())`, ids[i], provider, name, cipher)
		exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
VALUES($1,$2,$3,$4)`, ids[i], name, bytes.Repeat([]byte{byte(i)}, 32), i != 3)
		if i == 1 || i == 2 {
			exec("INSERT INTO beichen_panel_access(credential_id,updated_by,updated_at) VALUES($1,$2,now())", ids[i], ProvisionActorID)
		}
		if i == 3 {
			if err := service.SetPanelAccess(ctx, provider, ids[i], uuid.MustParse(ProvisionActorID),
				PanelAccessInput{Enabled: true, GroupID: int64(20 + i), Token: "legacy-panel"}); err != nil {
				t.Fatal(err)
			}
		}
		exec(`INSERT INTO beichen_datasets (credential_id,kind,next_attempt) VALUES ($1,'key_usage',now()+interval '1 hour')`, ids[i])
		keys = append(keys, map[string]any{"id": 40 + i, "user_id": 13, "key": secrets[i], "name": name, "group_id": currentGroups[i]})
	}

	useAmount := func(n int) json.Number { return json.Number(fmt.Sprintf("0.%02d", n)) }
	var websiteReads, keyListReads, refreshes atomic.Int32
	round := 1
	websiteBody := func() any {
		channels := make([]any, 0, len(names))
		if round == 4 {
			return map[string]any{"code": 0, "data": channels}
		}
		for i, platform := range platforms {
			groupPlatform := platform
			if i == 1 {
				groupPlatform = "composite"
			}
			group := map[string]any{"id": currentGroups[i], "platform": groupPlatform, "subscription_type": "standard",
				"rate_multiplier": useAmount(multipliers[i]), "peak_rate_enabled": false}
			model := func(id, platform string, base int) map[string]any {
				return map[string]any{"name": id, "platform": platform, "pricing": map[string]any{
					"billing_mode": "token", "input_price": json.Number(fmt.Sprintf("%de-6", base)),
					"output_price": 5e-6, "cache_read_price": 1e-7, "cache_write_price": 2e-7,
				}}
			}
			models := []any{model("shared", platform, i+1), model("web-only", platform, 8)}
			if i == 0 {
				models = append(models, map[string]any{"name": "tiered", "platform": platform, "pricing": map[string]any{
					"billing_mode": "token", "intervals": []any{map[string]any{"min_tokens": 200000, "input_price": 3e-6}},
				}}, map[string]any{"name": "listed-without-price", "platform": platform, "pricing": nil})
				if round >= 3 {
					models = append(models, model("new-model", platform, 7))
				}
			}
			sections := []any{map[string]any{"platform": platform, "groups": []any{group}, "supported_models": models}}
			if i == 1 {
				sections = append(sections, map[string]any{"platform": "minimax", "groups": []any{group},
					"supported_models": []any{model("mixed-model", "minimax", 6)}})
			}
			channels = append(channels, map[string]any{"id": 100 + i, "name": names[i], "status": "active", "platforms": sections})
			if i == 3 {
				// The obsolete fixed group remains visible and uses the same
				// multiplier but has a different price for the same model.
				staleGroup := map[string]any{"id": 23, "platform": platform, "subscription_type": "standard",
					"rate_multiplier": useAmount(multipliers[i]), "peak_rate_enabled": false}
				channels = append(channels, map[string]any{"id": 200, "name": "旧 CC1 分组", "platforms": []any{map[string]any{
					"platform": platform, "groups": []any{staleGroup},
					"supported_models": []any{model("shared", platform, 9)},
				}}})
			}
		}
		return map[string]any{"code": 0, "data": channels}
	}
	service.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		response := func(status int, value any) (*http.Response, error) {
			body, err := json.Marshal(value)
			return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body))}, err
		}
		if r.Method == http.MethodPost && r.URL.String() == webAuthURL+"refresh" {
			var data map[string]string
			if json.NewDecoder(r.Body).Decode(&data) != nil || data["refresh_token"] != "initial-refresh" {
				return nil, fmt.Errorf("the captured refresh authority was not used")
			}
			refreshes.Add(1)
			return response(200, map[string]any{"code": 0, "data": map[string]any{
				"access_token": "refreshed-access", "refresh_token": "refreshed-refresh", "token_type": "Bearer", "expires_in": 3600}})
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method for metadata endpoint %s", r.URL.Path)
			return nil, fmt.Errorf("unexpected metadata request")
		}
		if round == 4 && r.URL.Host == "api.beichenapi.com" && r.URL.Path == billingPath {
			return response(http.StatusServiceUnavailable, nil)
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.URL.String() == origin+modelsPath {
			for i, secret := range secrets {
				if token != secret {
					continue
				}
				entries := []map[string]string{{"id": "shared"}}
				switch i {
				case 0:
					entries = append(entries, map[string]string{"id": "tiered"}, map[string]string{"id": "listed-without-price"})
					if round >= 3 {
						entries = append(entries, map[string]string{"id": "new-model"})
					}
				case 1:
					entries = append(entries, map[string]string{"id": "mixed-model"})
				}
				return response(http.StatusOK, map[string]any{"data": entries})
			}
		}
		if r.URL.Host == "beichenapi.com" {
			if round == 2 && token == "initial-access" {
				return response(http.StatusUnauthorized, nil)
			}
			if (round == 1 && token != "initial-access") || (round > 1 && token != "refreshed-access") {
				t.Error("website request did not use the saved school session")
				return nil, fmt.Errorf("wrong website authorization")
			}
			switch r.URL.Path {
			case "/api/v1/groups/rates":
				rates := make(map[int]json.Number, len(currentGroups))
				for i, group := range currentGroups {
					rates[group] = useAmount(multipliers[i])
				}
				return response(http.StatusOK, map[string]any{"code": 0, "data": rates})
			case "/api/v1/channels/available":
				websiteReads.Add(1)
				return response(200, websiteBody())
			case "/api/v1/keys":
				keyListReads.Add(1)
				items := keys
				if round == 5 {
					items = items[:len(items)-1]
				}
				return response(200, map[string]any{"code": 0, "data": map[string]any{
					"items": items, "page": 1, "pages": 1, "page_size": 100, "total": len(items)}})
			}
		}
		t.Errorf("unexpected outbound request to %s%s", r.URL.Host, r.URL.Path)
		return nil, fmt.Errorf("unexpected outbound request")
	})

	var pricedChannels int64 = 4
	current := func(model string, credential uuid.UUID, expectedStatus, input string) {
		t.Helper()
		overview, err := service.Overview(ctx, provider, "")
		if err != nil || overview.WebsiteSync.PricedChannels != pricedChannels || overview.WebsiteSync.Channels != 4 {
			t.Fatalf("website status %+v, error %v", overview.WebsiteSync, err)
		}
		for _, row := range overview.Models {
			if row.Model != model || row.CredentialID != credential {
				continue
			}
			if expectedStatus == "" || row.PriceStatus != expectedStatus || (input != "" && (row.UpstreamRates == nil || row.UpstreamRates.Input == nil || *row.UpstreamRates.Input != input)) ||
				(input == "" && row.UpstreamRates != nil) {
				t.Fatalf("round %d: %s status is %s, want %s and input %s", round, model, row.PriceStatus, expectedStatus, input)
			}
			return
		}
		if expectedStatus != "" {
			t.Fatalf("round %d: %s missing from overview", round, model)
		}
	}
	for ; round <= 3; round++ {
		exec("UPDATE beichen_datasets SET next_attempt=clock_timestamp() WHERE kind='billing'")
		if err := service.syncJobs(ctx, nil); err != nil {
			view, readErr := service.Overview(ctx, provider, "")
			for _, dataset := range view.Datasets {
				if dataset.LastError != nil {
					t.Logf("round %d, fixture Key %s, %s: %s", round, dataset.CredentialID, dataset.Kind, *dataset.LastError)
				}
			}
			t.Fatalf("round %d: %v (overview: %v)", round, err, readErr)
		}
		if websiteReads.Load() != int32(round*len(names)) || keyListReads.Load() != websiteReads.Load() {
			t.Fatalf("round %d did not confirm four Key identities and their price tables", round)
		}
		for i, id := range ids {
			var group int64
			var base int
			var observedRevision bool
			var multiplier, input, output, read, write string
			err := db.QueryRowContext(ctx, `
SELECT s.group_id,v.input_rate,s.credential_revision=c.updated_at,trim_scale(m.multiplier)::text,
       trim_scale(p.upstream_input)::text,trim_scale(p.upstream_output)::text,
       trim_scale(p.upstream_cache_read)::text,trim_scale(p.upstream_cache_write)::text
FROM beichen_panel_snapshots s JOIN provider_credentials c ON c.id=s.credential_id
JOIN beichen_price_versions v ON v.credential_id=s.credential_id AND v.effective_at=s.effective_at
JOIN beichen_multiplier_versions m ON m.id=s.multiplier_version
CROSS JOIN LATERAL beichen_price_at(c.id,'shared',statement_timestamp()) p
WHERE c.id=$1 AND v.model='shared' AND v.source='panel' AND v.basis='base'
ORDER BY s.effective_at DESC LIMIT 1`, id).Scan(&group, &base, &observedRevision, &multiplier, &input, &output, &read, &write)
			if err != nil || !observedRevision || group != int64(currentGroups[i]) || base != (i+1)*1_000_000 ||
				!samePanelRate(json.Number(multiplier), useAmount(multipliers[i])) ||
				input != strconv.Itoa((i+1)*10_000*multipliers[i]) || output != strconv.Itoa(50_000*multipliers[i]) ||
				read != strconv.Itoa(1_000*multipliers[i]) || write != strconv.Itoa(2_000*multipliers[i]) {
				t.Fatalf("round %d: invalid price mapping for %s: %v", round, names[i], err)
			}
		}
		current("tiered", ids[0], "conditional", "")
		current("mixed-model", ids[1], "confirmed", "480000")
		current("listed-without-price", ids[0], "pending_confirmation", "")
		if round == 2 {
			current("new-model", ids[0], "", "")
		} else if round == 3 {
			current("new-model", ids[0], "confirmed", "2100000")
		}
	}
	if refreshes.Load() != 1 {
		t.Fatal("parallel expired-session reads did not share one refresh")
	}

	exec("UPDATE provider_credentials SET updated_at=clock_timestamp()")
	current("new-model", ids[0], "confirmed", "2100000")
	var priorSuccess time.Time
	if err := db.QueryRowContext(ctx, "SELECT max(last_success) FROM beichen_datasets WHERE kind='prices'").Scan(&priorSuccess); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE beichen_datasets SET next_attempt=clock_timestamp() WHERE kind='billing'")
	if err := service.syncJobs(ctx, nil); err == nil {
		t.Fatal("empty authenticated response reported success")
	}
	pricedChannels = 0
	current("new-model", ids[0], "pending_confirmation", "")
	var failed, snapshotCount int
	var latestSuccess time.Time
	err = db.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM beichen_datasets WHERE kind='prices' AND last_success IS NOT NULL AND last_error=$1),
  (SELECT count(*) FROM beichen_panel_snapshots WHERE expires_at IS NOT NULL),
   (SELECT max(last_success) FROM beichen_datasets WHERE kind='prices')`, errWebsiteDirectoryEmpty.Error()).Scan(&failed, &snapshotCount, &latestSuccess)
	if err != nil || failed != 4 || snapshotCount != 12 || !latestSuccess.Equal(priorSuccess) {
		t.Fatalf("empty read lost history or advanced last success: errors %d snapshots %d (%v)", failed, snapshotCount, err)
	}

	round, pricedChannels = 5, 3
	exec("UPDATE beichen_datasets SET next_attempt=clock_timestamp() WHERE kind='billing'")
	if err := service.syncJobs(ctx, nil); err == nil {
		t.Fatal("unverified fixed-group membership reported success")
	}
	current("shared", ids[3], "pending_confirmation", "")
	var reason string
	if err := db.QueryRowContext(ctx, `SELECT last_error FROM beichen_datasets WHERE kind='prices' AND credential_id=$1`, ids[3]).Scan(&reason); err != nil ||
		!strings.HasPrefix(reason, errKeyMatch.Error()) || !strings.Contains(reason, "遮罩证据不足") {
		t.Fatalf("missing group verification failure: %v", err)
	}
	t.Log("three four-channel rounds, deferred model price, and fixed-group rejection verified")
}
