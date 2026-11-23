package db

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

func TestBeichenGeminiRetirementMigrationPostgres(t *testing.T) {
	ctx, conn := openMigrationTestDB(t)
	applyMigrationTestBaseline(t, ctx, conn, "0065")

	script, err := migrationFiles.ReadFile("migrations/0065_retire_beichen_gemini_credential.sql")
	if err != nil {
		t.Fatal(err)
	}
	const beichen = " https://API.BEICHENAPI.COM/v1/ "
	for _, tc := range []struct {
		name, url, label, marker      string
		group                         any
		survivorLabel, survivorStatus string
		want                          string
	}{
		{"group", beichen, "renamed", "", "5", "unrelated", "active", "inactive"},
		{"label", beichen, "哈吉米", "", nil, "unrelated", "active", "inactive"},
		{"panel", beichen, "renamed", "panel", nil, "unrelated", "active", "inactive"},
		{"website", beichen, "renamed", "website", nil, "unrelated", "active", "inactive"},
		{"foreign provider", "https://api.beichenapi.com.example.test/v1", "哈吉米", "panel", "5", "unrelated", "active", "active"},
		{"only active", beichen, "哈吉米", "", "5", "unrelated", "inactive", "active"},
		{"all active match", beichen, "renamed", "", "5", "哈吉米", "active", "active"},
		{"no identity match", beichen, "renamed", "", "50", "unrelated", "active", "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providerID, modelID := uuid.New(), uuid.New()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := conn.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			addCredential := func(label string, group any, status string) uuid.UUID {
				t.Helper()
				id := uuid.New()
				exec(`
INSERT INTO provider_credentials (id, provider_id, label, group_hint, status, credential_ciphertext, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, decode('01','hex'), now(), now())`, id, providerID, label, group, status)
				return id
			}
			exec("INSERT INTO providers (id, name, base_url, created_at, updated_at) VALUES ($1, $2, $3, now(), now())",
				providerID, tc.name, tc.url)
			credentialID := addCredential(tc.label, tc.group, "active")
			survivorID := addCredential(tc.survivorLabel, nil, tc.survivorStatus)
			switch tc.marker {
			case "panel":
				actor := uuid.New()
				exec(`
INSERT INTO users (id, name, email, password_hash, role, created_at, updated_at)
VALUES ($1, 'fixture', $2, 'fixture', 'teacher', now(), now())`, actor, actor.String()+"@example.test")
				exec(`
INSERT INTO beichen_panel_access (credential_id, group_id, upstream_key_id, token_ciphertext, updated_by, updated_at)
VALUES ($1, 5, 12098, decode('01','hex'), $2, now())`, credentialID, actor)
			case "website":
				exec("INSERT INTO beichen_website_key_sync (provider_id) VALUES ($1)", providerID)
				exec(`
INSERT INTO beichen_website_keys (provider_id, upstream_key_id, name, status, credential_id, match_status)
VALUES ($1, 12098, 'fixture', 'active', $2, 'matched')`, providerID, credentialID)
			}
			exec(`
INSERT INTO models (id, model_id, display_name, status, credit_multiplier_milli, created_at, updated_at)
VALUES ($1, $2, 'fixture', 'active', 1000, now(), now())`, modelID, tc.name)
			exec(`
INSERT INTO provider_model_catalog (id, provider_id, credential_id, model_id, last_seen_at, synced_at, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, 'gemini-fixture', now(), now(), now(), now())`, providerID, credentialID)
			exec(`
INSERT INTO model_bindings (id, model_id, provider_id, credential_id, upstream_model_name, adapter, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, 'gemini-fixture', 'google_gemini_v1beta', now(), now())`, modelID, providerID, credentialID)
			exec(`
INSERT INTO upstream_probes (provider_id, credential_id, model, protocol, status_code, error_code, duration_ms, checked_at)
VALUES ($1, $2, 'gemini-fixture', 'google_gemini_v1beta', 403, 'http_error', 10, now())`, providerID, credentialID)

			read := func(query string, id uuid.UUID) string {
				t.Helper()
				var result string
				if err := conn.QueryRowContext(ctx, query, id).Scan(&result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			const retained = `
SELECT jsonb_build_array(to_jsonb(p), to_jsonb(a), to_jsonb(k), to_jsonb(r), to_jsonb(m))::text
FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
JOIN upstream_probes r ON r.credential_id = c.id
JOIN model_bindings b ON b.credential_id = c.id JOIN models m ON m.id = b.model_id
LEFT JOIN beichen_panel_access a ON a.credential_id = c.id
LEFT JOIN beichen_website_keys k ON k.credential_id = c.id
WHERE c.id = $1`
			before := read(retained, credentialID)
			const state = `
SELECT jsonb_build_array(c.status, m.status, b.status, c.updated_at, m.updated_at, b.updated_at)::text
FROM provider_credentials c JOIN provider_model_catalog m ON m.credential_id = c.id
JOIN model_bindings b ON b.credential_id = c.id WHERE c.id = $1`
			beforeState := read(state, credentialID)
			apply := func() {
				t.Helper()
				tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
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
			}
			apply()
			for _, query := range []string{
				"SELECT status FROM provider_credentials WHERE id = $1",
				"SELECT status FROM provider_model_catalog WHERE credential_id = $1",
				"SELECT status FROM model_bindings WHERE credential_id = $1",
			} {
				if got := read(query, credentialID); got != tc.want {
					t.Errorf("%s returned %q, want %q", query, got, tc.want)
				}
			}
			if got := read("SELECT status FROM provider_credentials WHERE id = $1", survivorID); got != tc.survivorStatus {
				t.Errorf("survivor was changed to %s", got)
			}
			if got := read(retained, credentialID); got != before {
				t.Error("migration changed the provider, publication, upstream identity or historical probe")
			}
			firstState := read(state, credentialID)
			if tc.want == "active" && firstState != beforeState {
				t.Error("migration changed an out-of-scope row")
			}
			apply()
			if got := read(state, credentialID); got != firstState {
				t.Error("running the SQL twice changed the status or timestamp")
			}
		})
	}
}
