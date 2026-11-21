package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	dbstore "github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

func TestChannelMatchEvidence(t *testing.T) {
	key := listedKey{ID: 13, GroupID: 23, UserID: 11, Name: "gpt", Status: "active"}
	check := func(keyList []listedKey, group, id int64, name string, want bool) {
		t.Helper()
		got, err := matchAccountKey(keyList, "sk-abcd-12345678", "G3", "G3", group, id, name)
		if (err == nil) != want || want && got.ID != 13 {
			t.Fatal("unexpected identity match", got.ID, err)
		}
	}
	check([]listedKey{key}, 23, 0, "", true)
	check([]listedKey{key}, 24, 13, "", false)
	check([]listedKey{key}, 0, 0, "", false)
	other := key
	other.ID = 14
	other.Name = "other"
	check([]listedKey{key, other}, 23, 0, "", false)
	check([]listedKey{key, other}, 23, 0, "gpt", true)
	key.Masked = "sk-abcd****5678"
	check([]listedKey{key, other}, 23, 0, "", true)
	key.Masked = "sk-abcd****1234"
	check([]listedKey{key}, 23, 13, "gpt", false)
	key.Masked = ""
	key.Name = "国模"
	check([]listedKey{key}, 23, 13, "", false)
	key.Name = "gpt"
	key.Status = "inactive"
	check([]listedKey{key}, 23, 13, "", false)
}

func TestSchoolSessionRefreshOnProtectedReadFailure(t *testing.T) {
	s := New(nil, nil)
	if err := s.ConfigureWebSession("fixture@example.invalid", "fake-password"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.client.Transport = receiptTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var fields map[string]string
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			if r.URL.String() != webAuthURL+"login" || fields["email"] != "fixture@example.invalid" || fields["password"] != "fake-password" {
				t.Fatal("unexpected login request")
			}
		case 2:
			if r.URL.String() != webAuthURL+"refresh" || len(fields) != 1 || fields["refresh_token"] != "refresh-fixture" {
				t.Fatal("unexpected refresh request")
			}
		default:
			t.Fatal("unnecessary session request")
		}
		body := fmt.Sprintf(`{"code":0,"data":{"access_token":"web-fixture-%d","token_type":"Bearer","refresh_token":"refresh-fixture","expires_in":700}}`, calls)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	token, err := s.webPanelToken(context.Background())
	if err != nil || token != "web-fixture-1" {
		t.Fatal("login failed")
	}
	s.webReadError(token, upstreamHTTPStatus(401))
	newToken, err := s.webPanelToken(context.Background())
	if err != nil || newToken != "web-fixture-2" {
		t.Fatal("expired session was not refreshed")
	}
	s.webReadError(token, upstreamHTTPStatus(401))
	again, err := s.webPanelToken(context.Background())
	if err != nil || again != newToken || calls != 2 {
		t.Fatal("old response invalidated a new session")
	}
}

func TestUpstreamUsageWithoutSchoolCallsPostgres(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable loopback nebula_test database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Database != "nebula_test" || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Fatal("requires loopback nebula_test with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := pgx.Identifier{"archive_" + uuid.NewString()}.Sanitize()
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
	db.SetMaxOpenConns(5)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, want int, args ...any) {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, sql, args...).Scan(&n); err != nil || n != want {
			t.Fatalf("want %d got %d: %v (%s)", want, n, err, sql)
		}
	}
	manager, err := security.NewManager(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{4}, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, manager)
	if err := s.ConfigureWebSession("fixture@school.invalid", "fake-password"); err != nil {
		t.Fatal(err)
	}
	var provider uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url=$1", origin).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at) VALUES($1,'fixture','fixture@audit.invalid','!','student','disabled',now(),now())`, ProvisionActorID)
	names := []string{"Gemini", "GM1", "G3", "CC1"}
	var creds []uuid.UUID
	for i, name := range names {
		id := uuid.New()
		creds = append(creds, id)
		cipher, err := manager.EncryptCredential(fmt.Sprintf("sk-fixture-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		exec("INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES($1,$2,$3,$4,now(),now())", id, provider, name, cipher)
		exec("INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids) VALUES($1,$2,$3,true)", id, name, bytes.Repeat([]byte{byte(i)}, 32))
		exec("INSERT INTO beichen_panel_access(credential_id,updated_by,updated_at) VALUES($1,$2,now())", id, ProvisionActorID)
	}
	stamp := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	history := make(map[int][]receiptRow)
	for channel := range names {
		key := 41 + channel
		total := 2
		if channel == 0 {
			total = 522
		}
		for i := 0; i < total; i++ {
			row := receiptFixture(stamp, int64(10000*key+2*i+2), fmt.Sprintf("external-%d-%d", key, i))
			row.KeyID = int64(key)
			history[key] = append(history[key], row)
		}
	}
	logins := 0
	ambiguous := true
	websiteDown := true
	badPage := false
	s.client = &http.Client{Transport: receiptTestTransport(func(r *http.Request) (*http.Response, error) {
		response := func(code int, v any) (*http.Response, error) {
			data, err := json.Marshal(v)
			return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(data))}, err
		}
		if r.URL.Scheme != "https" {
			t.Fatal("unexpected scheme")
		}
		switch r.URL.Host + r.URL.Path {
		case "beichenapi.com/api/v1/auth/login":
			logins++
			if websiteDown {
				return response(503, nil)
			}
			if r.Method != "POST" {
				t.Fatal("expected normal login")
			}
			return response(200, map[string]any{"code": 0, "data": map[string]any{"access_token": "panel-fixture", "token_type": "Bearer", "expires_in": 3600}})
		case "beichenapi.com/api/v1/keys":
			if r.Header.Get("Authorization") != "Bearer panel-fixture" {
				t.Fatal("wrong key-list identity")
			}
			var keys []map[string]any
			for i, n := range names {
				key := map[string]any{"id": 41 + i, "user_id": 17, "group_id": 21 + i, "name": n, "status": "active"}
				key["key"] = fmt.Sprintf("sk-fixture-%d", i)
				keys = append(keys, key)
			}
			if ambiguous {
				keys = append(keys, map[string]any{"id": 95, "group_id": 24, "user_id": 17, "name": "CC1", "status": "active", "key": "sk-fixture-3"})
			}
			return response(200, map[string]any{"code": 0, "data": map[string]any{"items": keys, "page": 1, "pages": 1, "total": len(keys), "page_size": 100}})
		case "beichenapi.com/api/v1/usage":
			q := r.URL.Query()
			if r.Header.Get("Authorization") != "Bearer panel-fixture" || q.Get("model") != "" || q.Get("start_date") != "1970-01-01" || q.Get("sort_order") != "asc" {
				t.Fatal("full account ledger filter changed")
			}
			key, _ := strconv.Atoi(q.Get("api_key_id"))
			p, _ := strconv.Atoi(q.Get("page"))
			all := history[key]
			total := len(all)
			start := min(total, (p-1)*100)
			end := min(start+100, total)
			items := append([]receiptRow{}, all[start:end]...)
			if badPage && key == 41 && p == 5 && len(items) > 0 {
				items[len(items)-1].UserID = 19
			}
			return response(200, map[string]any{"code": 0, "data": map[string]any{"items": items, "total": total, "pages": max(1, (total+99)/100), "page": p, "page_size": 100}})
		default:
			t.Fatal("unexpected request")
		}
		return nil, errors.New("unexpected request")
	})}
	now := stamp.AddDate(2, 0, 0)
	s.now = func() time.Time { return now }
	if err := s.discoverChannels(ctx); err == nil {
		t.Fatal("offline login reported success")
	}
	count("SELECT count(*) FROM beichen_receipt_active_bindings", 0)
	count("SELECT count(*) FROM provider_credentials WHERE status='active'", 4)
	websiteDown = false
	now = now.Add(3 * time.Minute)
	if err := s.discoverChannels(ctx); err == nil {
		t.Fatal("ambiguous label reported success")
	}
	count("SELECT count(*) FROM beichen_receipt_active_bindings", 3)
	ambiguous = false
	if err := s.discoverChannels(ctx); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM beichen_receipt_active_bindings", 4)
	count("SELECT count(*) FROM beichen_provision_channels WHERE upstream_user_id=17 AND discovery_error IS NULL", 4)
	if logins != 2 {
		t.Fatalf("unexpected login count %d", logins)
	}
	if err := s.archiveUsage(ctx); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM beichen_upstream_usage_rows", 406)
	count("SELECT page FROM beichen_usage_checkpoints WHERE credential_id=$1", 5, creds[0])
	badPage = true
	if err := s.archiveUsage(ctx); err == nil {
		t.Fatal("cross-account row accepted")
	}
	count("SELECT count(*) FROM beichen_upstream_usage_rows", 406)
	count("SELECT page FROM beichen_usage_checkpoints WHERE credential_id=$1", 5, creds[0])
	badPage = false
	// New process state resumes the persisted cursor.
	fresh := New(db, manager)
	fresh.client = s.client
	fresh.now = s.now
	if err := fresh.ConfigureWebSession("fixture@school.invalid", "fake-password"); err != nil {
		t.Fatal(err)
	}
	if err := fresh.archiveUsage(ctx); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM beichen_upstream_usage_rows WHERE actual_cost=0.0001435200000000001", 528)
	count("SELECT count(*) FROM beichen_usage_checkpoints WHERE caught_up AND last_error IS NULL", 4)
	extra := history[41][521]
	extra.ID++
	extra.RequestID = "late-tail"
	adjustment := json.Number("1.140000000000000000000001")
	extra.RateMultiplier = &adjustment
	extra.ActualCost = json.RawMessage("-0.000000000000000000000002")
	extra.CacheRead = nil
	history[41] = append(history[41], extra)
	if err := fresh.archiveUsage(ctx); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM beichen_upstream_usage_rows", 529)
	count("SELECT count(*) FROM beichen_upstream_usage_rows WHERE actual_cost=-0.000000000000000000000002 AND cache_read_tokens IS NULL AND (cost_details->>'rate_multiplier')::numeric=1.140000000000000000000001", 1)
	// An inserted old row shifts the saved page boundary. Rewind and backfill,
	// keeping all already ingested evidence and avoiding duplicate records.
	old := history[41][0]
	old.ID--
	old.RequestID = "late-history"
	history[41] = append([]receiptRow{old}, history[41]...)
	for i := 0; i < 2; i++ {
		if err := fresh.archiveUsage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	count("SELECT count(*) FROM beichen_upstream_usage_rows", 530)
	count("SELECT count(*) FROM gateway_calls", 0)
	count("SELECT count(*) FROM beichen_call_receipts", 0)
	fresh.client.Transport = receiptTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"requires_2fa":true}}`))}, nil
	})
	if err := fresh.ConfigureWebSession("fixture@example.invalid", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.webPanelToken(ctx); err == nil {
		t.Fatal("expected upstream account challenge")
	}
	view, err := fresh.Overview(ctx, provider, stamp.In(beijing).Format(dayLayout))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	if !view.WebLoginRequired {
		t.Fatal("school overview must expose the upstream account challenge")
	}
	for _, group := range view.ArchivedUsage {
		if group.UnmatchedRows != group.Rows || group.ObservedCost == nil || group.MarginCredits != nil {
			t.Fatalf("unassigned upstream charges must remain independent: %+v", group)
		}
		total += group.Rows
	}
	if total != 530 {
		t.Fatalf("overview counted %d archived rows", total)
	}
}
