package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/testutil"
)

func TestPanelAccessPostgresIsolationAndRotation(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("NEBULA_TEST_POSTGRES requires a disposable local nebula_test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Host != "127.0.0.1" || !testutil.IsIsolatedPostgresDatabase(config.Database) ||
		config.TLSConfig != nil || len(config.Fallbacks) != 0 {
		t.Fatal("panel access test requires loopback nebula_test with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := stdlib.OpenDB(*config)
	defer db.Close()
	schema := pgx.Identifier{"beichen_access_test_" + uuid.NewString()}.Sanitize()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	config.RuntimeParams["search_path"] = schema
	isolated := stdlib.OpenDB(*config)
	defer isolated.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := isolated.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`
CREATE TABLE users (id uuid PRIMARY KEY);
CREATE TABLE providers (id uuid PRIMARY KEY, base_url text NOT NULL, status text NOT NULL DEFAULT 'active');
CREATE TABLE provider_credentials (id uuid PRIMARY KEY, provider_id uuid NOT NULL REFERENCES providers(id), credential_ciphertext bytea,
  label text NOT NULL DEFAULT 'test channel', updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  status text NOT NULL DEFAULT 'active');
CREATE TABLE beichen_provision_channels (credential_id uuid PRIMARY KEY, channel text, discover_ids boolean, upstream_user_id bigint);
CREATE TABLE gateway_calls (provider_id uuid, provider_credential_id uuid, upstream_model_name text, sent_at timestamptz)`)
	for _, name := range []string{"0030_beichen.sql", "0033_beichen_panel_prices.sql", "0034_beichen_panel_access.sql", "0053_beichen_panel_accounts.sql"} {
		data, err := os.ReadFile("../infrastructure/db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		exec(string(data))
	}
	key := bytes.Repeat([]byte{7}, 32)
	manager, err := security.NewManager(key, key, key, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := New(isolated, manager)
	provider, otherProvider, actor, a, b, oldLease := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec("INSERT INTO users VALUES ($1)", actor)
	exec("INSERT INTO providers (id,base_url) VALUES ($1,$3),($2,$3)", provider, otherProvider, origin)
	exec("INSERT INTO provider_credentials (id,provider_id) VALUES ($1,$3),($2,$4)", a, b, provider, otherProvider)
	if err := s.ConfigurePanel("panel-env-fixture", `{"`+a.String()+`":7,"`+b.String()+`":8}`); err != nil {
		t.Fatal(err)
	}
	check := func(id uuid.UUID, wantToken string, wantGroup int64) {
		t.Helper()
		access, err := s.loadPriceAccess(ctx, id)
		if err != nil || access.legacyToken != wantToken || access.groupID != wantGroup {
			t.Fatalf("unexpected access for %s: group %d, error %v", id, access.groupID, err)
		}
	}
	check(a, "panel-env-fixture", 7)
	assertUsage := func(id uuid.UUID, wantToken string, wantID int64) time.Time {
		t.Helper()
		token, keyID, revision, err := s.PanelUsageAccess(ctx, id)
		if err != nil || token != wantToken || keyID != wantID || revision.IsZero() != (wantID == 0) {
			t.Fatalf("unexpected usage access for %s: key ID %d, error %v", id, keyID, err)
		}
		return revision
	}
	assertUsage(a, "", 0)
	firstKeyID, secondKeyID := int64(41), int64(42)
	input := PanelAccessInput{Enabled: true, GroupID: 11, Token: "panel-rotation-fixture", UpstreamKeyID: &firstKeyID}
	if err := s.SetPanelAccess(ctx, otherProvider, a, actor, input); err == nil {
		t.Fatal("accepted another provider's credential")
	}
	if _, err := s.PanelStatus(ctx, provider, b); err == nil {
		t.Fatal("read another provider's credential")
	}
	if err := s.SetPanelAccess(ctx, provider, a, actor, input); err != nil {
		t.Fatal(err)
	}
	check(a, input.Token, 11)
	check(b, "panel-env-fixture", 8)
	firstRevision := assertUsage(a, input.Token, firstKeyID)
	var ciphertext []byte
	var before time.Time
	err = isolated.QueryRowContext(ctx, "SELECT token_ciphertext,updated_at FROM beichen_panel_access WHERE credential_id=$1", a).Scan(&ciphertext, &before)
	if err != nil || len(ciphertext) == 0 || bytes.Contains(ciphertext, []byte(input.Token)) {
		t.Fatal("panel token was not stored as ciphertext")
	}
	exec("INSERT INTO beichen_panel_access (credential_id,group_id,token_ciphertext,updated_by,updated_at) VALUES ($1,11,$2,$3,clock_timestamp())", b, ciphertext, actor)
	if access, err := s.loadPriceAccess(ctx, b); err != nil || s.checkLegacyPriceAccount(access, false) == nil || access.legacyToken != "" {
		t.Fatal("transplanted ciphertext was accepted or fell back")
	}
	status, err := s.PanelStatus(ctx, provider, a)
	body, marshalErr := json.Marshal(status)
	if err != nil || marshalErr != nil || string(body) != `{"configured":true,"group_id":11,"upstream_key_id":41}` {
		t.Fatal("status exposed more than configuration state")
	}
	exec("UPDATE beichen_datasets SET lease_id=$2,lease_until=clock_timestamp()+interval '1 minute' WHERE credential_id=$1 AND kind='billing'", a, oldLease)
	input.GroupID, input.Token = 12, "panel-new-fixture"
	input.UpstreamKeyID = &secondKeyID
	if err := s.SetPanelAccess(ctx, provider, a, actor, input); err != nil {
		t.Fatal(err)
	}
	check(a, input.Token, 12)
	if secondRevision := assertUsage(a, input.Token, secondKeyID); secondRevision.Equal(firstRevision) {
		t.Fatal("rotated configuration retained pinned revision")
	}
	var lease uuid.NullUUID
	var until sql.NullTime
	if err := isolated.QueryRowContext(ctx, "SELECT lease_id,lease_until FROM beichen_datasets WHERE credential_id=$1 AND kind='billing'", a).Scan(&lease, &until); err != nil || lease.Valid || until.Valid {
		t.Fatal("rotation did not revoke the old lease")
	}
	oldResult := fetchResult{save: func(context.Context, *sql.Tx) error {
		t.Error("obsolete response reached the price writer")
		return nil
	}}
	if err := s.finishJob(ctx, syncJob{provider: provider, credential: a, kind: "billing"}, oldLease, credentialRecord{updatedAt: before}, oldResult, false); err == nil {
		t.Fatal("obsolete response retained the lease")
	}
	exec("UPDATE provider_credentials SET updated_at=clock_timestamp()+interval '1 second' WHERE id=$1", a)
	assertUsage(a, "", 0)
	input.UpstreamKeyID = nil
	if err := s.SetPanelAccess(ctx, provider, a, actor, input); err != nil {
		t.Fatal(err)
	}
	check(a, input.Token, input.GroupID)
	assertUsage(a, "", 0)
	if err := s.SetPanelAccess(ctx, provider, a, actor, PanelAccessInput{}); err != nil {
		t.Fatal(err)
	}
	check(a, "", 0)
	if access, err := s.loadPriceAccess(ctx, a); err != nil || !access.disabled {
		t.Fatal("explicit teacher disable did not override automatic website pricing")
	}
	assertUsage(a, "", 0)
	if status, err := s.PanelStatus(ctx, provider, a); err != nil || status.Configured || status.GroupID != 0 || status.UpstreamKeyID != nil {
		t.Fatal("disabled configuration fell back to environment")
	}
}
