package db

import "testing"

func TestDefaultBeichenProviderMigration(t *testing.T) {
	const migration = "0037_default_beichen_provider.sql"
	const canonical = "https://api.beichenapi.com"
	type providerRow struct{ name, url, status string }

	for _, tc := range []struct {
		name      string
		existing  []providerRow
		addedName string
	}{
		{"clean", nil, "北辰"},
		{"existing VulcanAPI", []providerRow{{"VulcanAPI", "https://vulcanapi.com", "active"}}, "北辰"},
		{"preserve inactive Beichen", []providerRow{{"北辰", canonical, "inactive"}}, ""},
		{"preserve active Beichen", []providerRow{{"历史北辰", canonical + "/", "active"}}, ""},
		{"existing v1 URL", []providerRow{{"历史北辰", canonical + "/v1", "active"}}, ""},
		{"existing v1 slash URL", []providerRow{{"历史北辰", " HTTPS://API.BEICHENAPI.COM/v1/ ", "active"}}, ""},
		{"unsupported URL variants", []providerRow{
			{"端口", "https://api.beichenapi.com:443", "active"},
			{"重复斜杠", canonical + "/v1//", "active"},
			{"大写路径", canonical + "/V1", "active"},
		}, "北辰"},
		{"same name other domain", []providerRow{
			{"北辰", "https://api.beichenapi.com.example.test/v1", "active"},
			{"北辰 (2)", "https://other.example.test", "inactive"},
		}, "北辰 (3)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := openMigrationTestDB(t)
			snapshot := func(name string) string {
				t.Helper()
				var row string
				if err := conn.QueryRowContext(ctx, "SELECT to_jsonb(p)::text FROM providers p WHERE name=$1", name).Scan(&row); err != nil {
					t.Fatal(err)
				}
				return row
			}
			before := make(map[string]string)
			if len(tc.existing) > 0 {
				applyMigrationTestBaseline(t, ctx, conn, "0036")
				for _, row := range tc.existing {
					if _, err := conn.ExecContext(ctx, `
INSERT INTO providers (id, name, base_url, status, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, '2020-01-01 UTC', '2021-01-01 UTC')`, row.name, row.url, row.status); err != nil {
						t.Fatal(err)
					}
					before[row.name] = snapshot(row.name)
				}
			}
			var vulcanBefore string
			if tc.name == "existing VulcanAPI" {
				if _, err := conn.ExecContext(ctx, `
WITH credential AS (
  INSERT INTO provider_credentials (id, provider_id, label, credential_ciphertext, created_at, updated_at)
  SELECT gen_random_uuid(), id, 'fixture', decode('01', 'hex'), now(), now()
  FROM providers WHERE name='VulcanAPI'
  RETURNING id, provider_id
), model AS (
  INSERT INTO models (id, model_id, display_name, status, credit_multiplier_milli, created_at, updated_at)
  VALUES (gen_random_uuid(), 'fixture-model', 'Fixture model', 'active', 1000, now(), now())
  RETURNING id
)
INSERT INTO model_bindings (id, model_id, provider_id, credential_id, upstream_model_name, adapter, created_at, updated_at)
SELECT gen_random_uuid(), model.id, credential.provider_id, credential.id,
  'fixture-model', 'openai_compatible', now(), now() FROM model, credential`); err != nil {
					t.Fatal(err)
				}
			}
			vulcanSnapshot := func() string {
				t.Helper()
				var state string
				if err := conn.QueryRowContext(ctx, `
SELECT jsonb_build_array(to_jsonb(c) - 'retired_at', to_jsonb(b))::text
FROM model_bindings b JOIN provider_credentials c ON c.id=b.credential_id
JOIN providers p ON p.id=b.provider_id WHERE p.name='VulcanAPI'`).Scan(&state); err != nil {
					t.Fatal(err)
				}
				return state
			}
			if tc.name == "existing VulcanAPI" {
				vulcanBefore = vulcanSnapshot()
			}
			if err := Migrate(ctx, conn); err != nil {
				t.Fatal(err)
			}
			verify := func() {
				t.Helper()
				for name, want := range before {
					if got := snapshot(name); got != want {
						t.Errorf("provider %q changed: %s", name, got)
					}
				}
				count := func(query string, want int, args ...any) {
					t.Helper()
					var got int
					if err := conn.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if got != want {
						t.Errorf("%s returned %d, want %d", query, got, want)
					}
				}
				wantProviders := len(tc.existing)
				if tc.addedName != "" {
					wantProviders++
					var id, baseURL, status string
					if err := conn.QueryRowContext(ctx, "SELECT id, base_url, status FROM providers WHERE name=$1", tc.addedName).
						Scan(&id, &baseURL, &status); err != nil {
						t.Fatal(err)
					}
					if baseURL != canonical || status != "active" || len(id) != 36 || id[14] != '7' {
						t.Fatalf("unexpected seed: id=%s url=%s status=%s", id, baseURL, status)
					}
					count("SELECT count(*) FROM provider_credentials WHERE provider_id=$1", 0, id)
					count("SELECT count(*) FROM model_bindings WHERE provider_id=$1", 0, id)
				}
				count("SELECT count(*) FROM providers", wantProviders)
				wantBindings := 0
				if vulcanBefore != "" {
					wantBindings = 1
					if got := vulcanSnapshot(); got != vulcanBefore {
						t.Error("VulcanAPI credential or binding changed")
					}
					count(`SELECT count(*) FROM provider_credentials c
JOIN providers p ON p.id=c.provider_id WHERE p.name='VulcanAPI' AND c.retired_at IS NOT NULL`, 0)
				}
				count("SELECT count(*) FROM provider_credentials", wantBindings)
				count("SELECT count(*) FROM model_bindings", wantBindings)
				count("SELECT count(*) FROM schema_migrations WHERE version=$1", 1, migration)
			}
			verify()
			if err := Migrate(ctx, conn); err != nil {
				t.Fatal(err)
			}
			// Exercise the SQL itself again as well as the migration ledger skip.
			script, err := migrationFiles.ReadFile("migrations/" + migration)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, string(script)); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			verify()
		})
	}
}
