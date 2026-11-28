package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	store "github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

const provisionFixture = `[
{"channel":"Gemini","api_key":"sk-fixture-one","group_id":11,"upstream_key_id":51},
{"channel":"GM1","api_key":"sk-fixture-two","group_id":12,"upstream_key_id":52},
{"channel":"G3","api_key":"sk-fixture-three","group_id":12,"upstream_key_id":53},
{"channel":"CC1","api_key":"sk-fixture-four","group_id":14,"upstream_key_id":54}]`
const panelFixture = "panel-fixture"

func TestBeichenProvisionStartupInputs(t *testing.T) {
	s := controlplane.NewService(nil, nil, nil, nil, nil, controlplane.Config{})
	ctx := context.Background()
	if err := configureBeichen(ctx, s, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := configureBeichen(ctx, s, "", panelFixture, `{"`+uuid.NewString()+`":4}`); err != nil {
		t.Fatal("legacy price-only configuration failed", err)
	}
	if err := configureBeichen(ctx, s, "", panelFixture, ""); err != nil {
		t.Fatal("standalone panel bearer was rejected", err)
	}
	for _, tc := range []struct{ name, raw, token, groups string }{
		{"malformed legacy", provisionFixture, panelFixture, "[]"},
		{"wrong root", "{}", panelFixture, ""},
		{"absent channels", "[]", panelFixture, ""},
		{"duplicate field", strings.Replace(provisionFixture, `"group_id":11`, `"group_id":3,"group_id":11`, 1), panelFixture, ""},
		{"unknown field", strings.Replace(provisionFixture, "group_id", "unexpected", 1), panelFixture, ""},
		{"duplicate name", strings.Replace(provisionFixture, "Gemini", "GM1", 1), panelFixture, ""},
		{"duplicate key", strings.Replace(provisionFixture, "sk-fixture-one", "sk-fixture-two", 1), panelFixture, ""},
		{"duplicate ID", strings.Replace(provisionFixture, "51", "52", 1), panelFixture, ""},
		{"missing ID", strings.Replace(provisionFixture, `,"upstream_key_id":51`, "", 1), panelFixture, ""},
		{"unsafe integer", strings.Replace(provisionFixture, "51", "9007199254740992", 1), panelFixture, ""},
		{"quoted ID", strings.Replace(provisionFixture, "51", `"51"`, 1), panelFixture, ""},
		{"no group", strings.Replace(provisionFixture, "11", "0", 1), panelFixture, ""},
		{"same token", strings.Replace(provisionFixture, "sk-fixture-one", panelFixture, 1), panelFixture, ""},
		{"second object", provisionFixture + "{}", panelFixture, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := configureBeichen(ctx, s, tc.raw, tc.token, tc.groups)
			if err == nil || strings.Contains(err.Error(), "fixture") {
				t.Fatal("configuration accepted or a secret appeared in the error")
			}
		})
	}
}

func TestBeichenProvisionPostgres(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable loopback nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || !isIsolatedTestDatabase(config.Database) || config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("requires loopback nebula_test with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin := stdlib.OpenDB(*config)
	defer admin.Close()
	schema := pgx.Identifier{"provision_" + uuid.NewString()}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	key := bytes.Repeat([]byte{37}, 32)
	manager, err := security.NewManager(key, key, key, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := controlplane.NewService(nil, db, nil, manager, nil, controlplane.Config{})
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	checkCount := func(q string, want int, args ...any) {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil || n != want {
			t.Fatalf("expected %d rows, got %d: %v (%s)", want, n, err, q)
		}
	}
	run := func(raw, token string) {
		t.Helper()
		if err := configureBeichen(ctx, s, raw, token, ""); err != nil {
			t.Fatal(err)
		}
	}
	fail := func(raw, token string) {
		t.Helper()
		err := configureBeichen(ctx, s, raw, token, "")
		if err == nil || strings.Contains(err.Error(), "fixture") {
			t.Fatal("expected a sanitized conflict")
		}
	}
	snapshot := func() string {
		t.Helper()
		var snapshot string
		err := db.QueryRowContext(ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(c ORDER BY id) FROM provider_credentials c),
 (SELECT jsonb_agg(a ORDER BY credential_id) FROM beichen_panel_access a),
 (SELECT jsonb_agg(p ORDER BY credential_id,effective_at) FROM beichen_panel_snapshots p),
 (SELECT jsonb_agg(d ORDER BY credential_id,kind) FROM beichen_datasets d))::text`).Scan(&snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	var provider uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT id FROM providers WHERE name='北辰'").Scan(&provider); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE providers SET name='历史北辰',base_url=' HTTPS://API.BEICHENAPI.COM/v1/ ' WHERE id=$1", provider)
	other := uuid.New()
	exec(`INSERT INTO providers (id,name,base_url,status,created_at,updated_at) VALUES ($1,'北辰','https://other.invalid','active',now(),now())`, other)
	// An unrelated provider can even have an unreadable credential under the same label.
	exec(`INSERT INTO provider_credentials (id,provider_id,label,credential_ciphertext,status,created_at,updated_at)
VALUES ($1,$2,'G3',decode('FF','hex'),'inactive',now(),now())`, uuid.New(), other)
	run("", "")
	checkCount("SELECT count(*) FROM users", 0)
	checkCount("SELECT count(*) FROM beichen_panel_access", 0)

	// The fourth label collides after the first three would have been created.
	last := uuid.New()
	legacy, err := manager.EncryptCredential("sk-historical-other")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO provider_credentials (id,provider_id,label,credential_ciphertext,status,created_at,updated_at)
VALUES ($1,$2,'cc不满血',$3,'active',now(),now())`, last, provider, legacy)
	before := snapshot()
	fail(provisionFixture, panelFixture)
	checkCount("SELECT count(*) FROM users", 0)
	if snapshot() != before {
		t.Fatal("failed last channel left partial writes")
	}
	correct, err := manager.EncryptCredential("sk-fixture-four")
	if err != nil {
		t.Fatal(err)
	}
	exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", last, correct)
	// This is the plaintext contract after the school runtime opens its sealed envelope.
	const automatic = `[{"channel":"Gemini","api_key":"sk-fixture-one"},{"channel":"GM1","api_key":"sk-fixture-two"},
{"channel":"G3","api_key":"sk-fixture-three"},{"channel":"CC1","api_key":"sk-fixture-four"}]`
	waiting, err := s.ProvisionBeichenChannels(ctx, automatic, "")
	if err != nil || waiting.Bound != 0 || len(waiting.AwaitingAccess) != 4 || len(waiting.Disabled) != 0 {
		t.Fatal("catalog-only configuration failed or claimed a usable website binding")
	}
	checkCount("SELECT count(*) FROM provider_credentials WHERE provider_id=$1 AND status='active'", 4, provider)
	checkCount("SELECT count(*) FROM beichen_receipt_active_bindings", 0)
	checkCount("SELECT count(*) FROM beichen_provision_channels WHERE discover_ids", 4)
	before = snapshot()
	run(automatic, "")
	if snapshot() != before {
		t.Fatal("restarting without a school account changed pending bindings")
	}
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { done <- configureBeichen(ctx, s, provisionFixture, panelFixture, "") }()
	}
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	checkCount("SELECT count(*) FROM beichen_receipt_active_bindings", 4)
	checkCount("SELECT count(*) FROM provider_credentials WHERE provider_id=$1", 4, provider)
	checkCount("SELECT count(*) FROM users WHERE status='disabled' AND role='student' AND password_hash='!'", 1)
	checkCount("SELECT count(*) FROM users WHERE role='teacher' OR status='active'", 0)
	checkCount("SELECT count(*) FROM student_profiles", 0)
	checkCount("SELECT count(*) FROM mentor_profiles", 0)
	checkCount("SELECT count(*) FROM beichen_panel_snapshots", 0)
	before = snapshot()
	run(provisionFixture, panelFixture)
	if snapshot() != before {
		t.Fatal("restart changed credential data, revisions, snapshots or jobs")
	}
	var records []struct {
		Channel       string
		APIKey        string `json:"api_key"`
		GroupID       int64  `json:"group_id"`
		UpstreamKeyID int64  `json:"upstream_key_id"`
	}
	if err := json.Unmarshal([]byte(provisionFixture), &records); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		var id uuid.UUID
		var ciphertext []byte
		if err := db.QueryRowContext(ctx, `SELECT c.id,c.credential_ciphertext FROM provider_credentials c JOIN beichen_panel_access a ON a.credential_id=c.id WHERE a.upstream_key_id=$1`, r.UpstreamKeyID).Scan(&id, &ciphertext); err != nil {
			t.Fatal(err)
		}
		plain, err := manager.DecryptCredential(ciphertext)
		if err != nil || plain != r.APIKey || bytes.Contains(ciphertext, []byte(plain)) {
			t.Fatal("runtime credential decryption mismatch")
		}
		status, err := s.Beichen().PanelStatus(ctx, provider, id)
		if err != nil || !status.Configured || status.GroupID != r.GroupID {
			t.Fatal("panel group mismatch")
		}
		token, upstream, revision, err := s.Beichen().PanelUsageAccess(ctx, id)
		if err != nil || token != panelFixture || upstream != r.UpstreamKeyID || revision.IsZero() {
			t.Fatal("receipt reader failed to resolve the provisioned binding")
		}
	}
	newToken := "panel-rotated-fixture"
	newRaw := strings.Replace(provisionFixture, `"group_id":14`, `"group_id":17`, 1)
	_, _, oldRevision, _ := s.Beichen().PanelUsageAccess(ctx, last)
	run(newRaw, newToken)
	token, _, newRevision, err := s.Beichen().PanelUsageAccess(ctx, last)
	if err != nil || token != newToken || !newRevision.After(oldRevision) {
		t.Fatal("authorization rotation did not advance the revision")
	}
	rotated := strings.NewReplacer("sk-fixture-four", "sk-rotated-fixture", "54", "64").Replace(newRaw)
	run(rotated, newToken)
	checkCount("SELECT count(*) FROM provider_credentials WHERE provider_id=$1", 5, provider)
	checkCount("SELECT count(*) FROM provider_credentials WHERE id=$1 AND status='inactive'", 1, last)
	checkCount("SELECT count(*) FROM beichen_receipt_active_bindings WHERE upstream_key_id=64 AND credential_id<>$1", 1, last)
	checkCount("SELECT count(*) FROM beichen_receipt_active_bindings WHERE upstream_key_id=54", 0)
	before = snapshot()
	run(rotated, newToken)
	fail(newRaw, newToken) // A stale deployment cannot re-import the archived secret.
	if snapshot() != before {
		t.Fatal("archive was duplicated or rolled back")
	}

	teacher := uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at)
VALUES ($1,'fixture','fixture@teacher.invalid','!','teacher','active',now(),now())`, teacher)
	var gm, g3 uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT credential_id FROM beichen_receipt_active_bindings WHERE upstream_key_id=52").Scan(&gm); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT credential_id FROM beichen_receipt_active_bindings WHERE upstream_key_id=53").Scan(&g3); err != nil {
		t.Fatal(err)
	}
	if err := s.Beichen().SetPanelAccess(ctx, provider, gm, teacher, beichen.PanelAccessInput{}); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE provider_credentials SET status='inactive',updated_at=clock_timestamp() WHERE id=$1", g3)
	before = snapshot()
	result, err := s.ProvisionBeichenChannels(ctx, rotated, newToken)
	if err != nil || result.Bound != 2 || strings.Join(result.Disabled, ",") != "GM1,G3" || snapshot() != before {
		t.Fatal("disabled settings changed or were counted as ready")
	}
	checkCount("SELECT count(*) FROM beichen_receipt_active_bindings", 2)
	exec("UPDATE providers SET status='inactive' WHERE id=$1", provider)
	result, err = s.ProvisionBeichenChannels(ctx, rotated, newToken)
	if err != nil || result.Bound != 0 || len(result.Disabled) != 4 || snapshot() != before {
		t.Fatal("inactive provider changed or was reported ready")
	}
	checkCount("SELECT count(*) FROM beichen_receipt_active_bindings", 0)
	exec("UPDATE providers SET status='active' WHERE id=$1", provider)
	exec("UPDATE providers SET base_url='https://api.beichenapi.com' WHERE id=$1", other)
	fail(rotated, newToken)
	exec("UPDATE providers SET base_url='https://other.invalid' WHERE id=$1", other)
	webService := func(email, password string) *controlplane.Service {
		t.Helper()
		web := controlplane.NewService(nil, db, nil, manager, nil, controlplane.Config{})
		if err := web.Beichen().ConfigureWebSession(email, password); err != nil {
			t.Fatal(err)
		}
		return web
	}
	const email = "school-fixture@example.invalid"
	const password = "school-password-fixture"
	web := webService(email, password)
	if err := configureBeichen(ctx, web, rotated, "", ""); err != nil {
		t.Fatal(err)
	}
	checkCount("SELECT count(*) FROM beichen_receipt_active_bindings", 2)
	var webID uuid.UUID
	var marker []byte
	if err := db.QueryRowContext(ctx, `SELECT credential_id,token_ciphertext FROM beichen_panel_access WHERE upstream_key_id=51`).Scan(&webID, &marker); err != nil {
		t.Fatal(err)
	}
	value, err := manager.DecryptCredential(marker)
	account := sha256.Sum256([]byte(email))
	if err != nil || value != "beichen-web-v1:"+webID.String()+":"+hex.EncodeToString(account[:]) {
		t.Fatal("managed rows must store only a credential- and account-specific encrypted marker")
	}
	before = snapshot()
	web = webService(email, "rotated-school-password-fixture")
	if err := configureBeichen(ctx, web, rotated, "", ""); err != nil || snapshot() != before {
		t.Fatal("password rotation changed a pinned authorization for the same account")
	}
	otherWeb := webService("other-fixture@example.invalid", password)
	if token, _, _, err := otherWeb.Beichen().PanelUsageAccess(ctx, webID); err == nil || token != "" {
		t.Fatal("a different school's session could resolve the original receipt binding")
	}
	if err := configureBeichen(ctx, otherWeb, rotated, "", ""); err != nil || snapshot() == before {
		t.Fatal("account change failed to revise the pinned bindings")
	}
	exec("UPDATE provider_credentials SET label='retired Gemini fixture',status='inactive',retired_at=clock_timestamp() WHERE id=$1", webID)
	before = snapshot()
	for _, raw := range []string{
		rotated,
		strings.Replace(rotated, `{"channel":"Gemini","api_key":"sk-fixture-one","group_id":11,"upstream_key_id":51},`, "", 1),
		strings.Replace(rotated, `{"channel":"Gemini","api_key":"sk-fixture-one","group_id":11,"upstream_key_id":51}`,
			`{"channel":"Gemini","status":"retired"}`, 1),
	} {
		if err := configureBeichen(ctx, otherWeb, raw, "", ""); err != nil || snapshot() != before {
			t.Fatal("optional or retired Gemini configuration re-provisioned historical credentials", err)
		}
	}
	before = snapshot()
	exec("UPDATE users SET role='teacher' WHERE status='disabled'")
	fail(rotated, newToken)
	if snapshot() != before {
		t.Fatal("identity or provider collision changed stored settings")
	}
	checkCount("SELECT count(*) FROM provider_credentials WHERE provider_id=$1 AND label='G3' AND status='inactive' AND credential_ciphertext=decode('FF','hex')", 1, other)
}
func isIsolatedTestDatabase(name string) bool {
	switch name {
	case "nebula_test", "nebula_test_rev_c1", "nebula_test_rev_c1_2", "nebula_test_hjc1":
		return true
	default:
		return false
	}
}
