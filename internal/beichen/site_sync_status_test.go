package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestOverviewWebsiteSyncPostgres(t *testing.T) {
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("requires a disposable loopback nebula_test database")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Database != "nebula_test" ||
		cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Fatal("requires loopback nebula_test with sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := pgx.Identifier{"site_sync_" + uuid.NewString()}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	paths, err := filepath.Glob("../infrastructure/db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatal("locate migrations")
	}
	for _, path := range paths {
		script, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, ctx, tx, string(script))
	}
	if err := tx.Commit(); err != nil {
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
	service := New(db, nil)
	check := func(stage string, channels, priced, checked, caughtUp int, priceAt, checkedAt *time.Time) {
		t.Helper()
		view, err := service.Overview(ctx, provider, "2020-01-01")
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Status map[string]json.RawMessage `json:"website_sync"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(wire.Status)
		want, _ := json.Marshal(map[string]any{
			"channels": channels, "priced_channels": priced, "price_last_seen": priceAt,
			"receipt_checked_channels": checked, "receipt_last_checked": checkedAt, "receipt_caught_up_channels": caughtUp,
		})
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: got %s, want %s", stage, got, want)
		}
	}
	check("unprovisioned", 0, 0, 0, 0, nil, nil)

	now := time.Now().UTC().Truncate(time.Second)
	revision := now.Add(-24 * time.Hour)
	previous := revision.Add(-time.Hour)
	hourAgo, halfHourAgo, recent := now.Add(-time.Hour), now.Add(-30*time.Minute), now.Add(-time.Minute)
	live := now.Add(time.Hour)
	actor, otherProvider := uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at)
	      VALUES($1,'fixture','website@example.invalid','!','student','disabled',now(),now())`, actor)
	exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES($1,'Other','https://api.beichenapi.com/v1',now(),now())`, otherProvider)
	addChannel := func(owner uuid.UUID, label, channel, state string, keyID int) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,status,created_at,updated_at)
		      VALUES($1,$2,$3,''::bytea,$4,$5,$5)`, id, owner, label, state, revision)
		if channel != "" {
			exec(`INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids)
			      VALUES($1,$2,decode(repeat('00',32),'hex'),true)`, id, channel)
		}
		exec(`INSERT INTO beichen_panel_access(credential_id,group_id,upstream_key_id,token_ciphertext,updated_by,updated_at)
		      VALUES($1,1,$2,''::bytea,$3,$4)`, id, keyID, actor, revision)
		return id
	}
	snapshot := func(id uuid.UUID, rev, at, expiry time.Time, terms string) {
		t.Helper()
		exec(`WITH m AS (
		          INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier) VALUES($1,$3,1) RETURNING id
		      ) INSERT INTO beichen_panel_snapshots(credential_id,credential_revision,effective_at,expires_at,group_id,multiplier_version,model_pricing)
		        SELECT $1,$2,$3,$4,1,id,$5::jsonb FROM m`, id, rev, at, expiry, terms)
	}
	checkpoint := func(id uuid.UUID, keyID int, rev time.Time, success *time.Time, caughtUp bool, problem any) {
		t.Helper()
		exec(`INSERT INTO beichen_usage_checkpoints(credential_id,upstream_key_id,revision,last_success,caught_up,last_error)
		      VALUES($1,$2,$3,$4,$5,$6)`, id, keyID, rev, success, caughtUp, problem)
	}
	gemini := addChannel(provider, "renamed", "Gemini", "active", 11)
	gm1 := addChannel(provider, "GM1", "GM1", "active", 12)
	g3 := addChannel(provider, "G3", "G3", "active", 13)
	cc1 := addChannel(provider, "CC1", "CC1", "active", 14)
	for _, ignored := range []uuid.UUID{
		addChannel(provider, "ordinary", "", "active", 20),
		addChannel(provider, "retired", "CC1", "inactive", 20),
		addChannel(otherProvider, "foreign", "CC1", "active", 20),
	} {
		snapshot(ignored, revision, recent, live, `{"model":{"input_price":8}}`)
		checkpoint(ignored, 20, revision, &recent, true, nil)
	}
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success)
	      SELECT $1,kind,$2 FROM unnest(ARRAY['catalog','billing','prices','usage']) kind`, cc1, recent)
	exec(`INSERT INTO beichen_daily_usage(provider_id,credential_id,day,model,requests,source)
	      VALUES($1,$2,current_date,'daily',5,'usage')`, provider, cc1)
	snapshot(g3, previous, recent, live, `{"obsolete":{"input_price":1}}`)
	snapshot(g3, revision, live, live.Add(time.Hour), `{"future":{"output_price":1}}`)
	snapshot(cc1, revision, halfHourAgo, live, `{"empty":{},"mock":{"billing_mode":"token","multiplier":5,"input_price":null}}`)
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,effective_at,actor_id)
	      VALUES($1,'csv','upstream','effective','csv',1,2,$2,$3),
	            ($1,'billing','upstream','effective','billing',1,2,$2,NULL),
	            ($1,'different-sample','upstream','base','panel',1,2,$4,NULL)`, cc1, halfHourAgo, actor, hourAgo)
	checkpoint(g3, 13, previous, &recent, true, nil)
	checkpoint(cc1, 99, revision, &recent, true, nil)
	check("other sources, stale bindings and unsupported terms", 4, 0, 0, 0, nil, nil)

	snapshot(gemini, revision, hourAgo, live, `{"tiered":{"intervals":[{"min_tokens":100,"input_price":"0.000004"}]}}`)
	snapshot(gm1, revision, halfHourAgo, recent, `{}`)
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,effective_at)
	      VALUES($1,'legacy-flat','upstream','base','panel',0,2,$2)`, gm1, halfHourAgo)
	checkpoint(gemini, 11, revision, &hourAgo, false, "last attempt failed")
	checkpoint(gm1, 12, revision, &halfHourAgo, true, nil)
	check("live tiered price, expired historical price, partial and empty final page", 4, 1, 2, 1, &halfHourAgo, &halfHourAgo)

	snapshot(cc1, revision, recent, live, `{"free":{"per_request_price":0}}`)
	check("explicit zero is a price", 4, 2, 2, 1, &recent, &halfHourAgo)
	snapshot(gemini, revision, recent, live, `{}`)
	exec(`INSERT INTO beichen_panel_snapshots(credential_id,credential_revision,effective_at) VALUES($1,$2,$3)`, cc1, revision, now)
	exec(`UPDATE beichen_panel_access SET upstream_key_id=NULL WHERE credential_id=$1`, gm1)
	check("empty and revoked latest snapshots, removed receipt binding", 4, 0, 1, 0, &recent, &hourAgo)
	exec(`UPDATE provider_credentials SET updated_at=$1 WHERE id IN ($2,$3)`, now, cc1, gemini)
	check("history stays with its revision", 4, 0, 0, 0, &halfHourAgo, nil)
	exec(`UPDATE providers SET status='inactive' WHERE id=$1`, provider)
	check("inactive provider", 0, 0, 0, 0, nil, nil)
}
