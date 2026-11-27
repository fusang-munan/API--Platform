package beichen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

const websiteKeysGroupsFixture = `{"code":0,"data":[{"platforms":[
 {"platform":"gemini","groups":[{"id":63,"name":"Gemini","rate_multiplier":"0.3"}]},
 {"platform":"glm","groups":[{"id":64,"name":"GM1"}]},
 {"platform":"anthropic","groups":[{"id":61,"name":"旧组","rate_multiplier":"0.15"}]}
]}]}`

func TestBeichenWebsiteKeysPostgres(t *testing.T) {
	ctx, db, provider, _, credentials := newActionsPostgres(t)
	manager := switchTestManager(t)
	service := New(db, manager)
	teacherView := New(db, manager)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service.now = func() time.Time { return now }
	const bearer = "fixture-web-inventory-session"
	service.ConfigurePanelFallback(bearer)
	secrets := []string{"sk-gma-fixture-P300", "sk-gm1a-fixture-M100", "sk-fixture-gpt42-XXXX", "sk-cc2a-fixture-C222"}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, credential := range credentials {
		encrypted, err := manager.EncryptCredential(secrets[i])
		if err != nil {
			t.Fatal(err)
		}
		exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", credential, encrypted)
	}
	claude := credentials[3]
	exec(`INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier,group_id,group_rate,observed_at)
VALUES ($1,$2,0.15,61,0.15,$2)`, claude, now.Add(-time.Hour))
	exec(`INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version)
SELECT c.id,m.effective_at,c.updated_at,m.group_id,$2,m.id FROM provider_credentials c
JOIN beichen_multiplier_versions m ON m.credential_id=c.id WHERE c.id=$1`, claude, now.Add(time.Hour))
	oldLease := uuid.New()
	exec(`INSERT INTO beichen_datasets(credential_id,kind,lease_id,lease_until)
VALUES ($1,'billing',$2,clock_timestamp()+interval '1 minute')`, claude, oldLease)

	fixture, err := os.ReadFile("testdata/website-keys-screenshot.json")
	if err != nil {
		t.Fatal(err)
	}
	withMissingGroup := strings.Replace(string(fixture), `"group_id": 62, "group": {"id": 62, "name": "[CC2]ClaudeCode高智", "rate_multiplier": 0.6}`,
		`"group_id": null, "group": null`, 1)
	withCollision := strings.Replace(string(fixture), `"name": "gpt", "key": "sk-fixture-gpt42-XXXX"`,
		`"name": "cc不满血", "key_prefix": "sk-cc2a", "key_suffix": "C222"`, 1)
	var responseState, reads atomic.Int32
	usePanelAuthFixture(t, service, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+bearer {
			t.Error("Key inventory used an unexpected method or credential")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v1/keys":
			reads.Add(1)
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "100" {
				t.Error("missing Key list page parameters")
			}
			switch responseState.Load() {
			case 1:
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, bearer)
			case 2:
				fmt.Fprint(w, withMissingGroup)
			case 3:
				fmt.Fprint(w, `{"code":"TOKEN_EXPIRED"}`)
			case 4:
				fmt.Fprint(w, withCollision)
			default:
				w.Write(fixture)
			}
		case "/api/v1/channels/available":
			fmt.Fprint(w, websiteKeysGroupsFixture)
		default:
			t.Error("Key inventory requested an unrelated endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	initial, err := teacherView.WebsiteKeys(ctx, provider)
	if err != nil || initial.Status != "unsynced" || len(initial.Keys) != 0 || reads.Load() != 0 {
		t.Fatalf("empty view: %s, %v", initial.Status, err)
	}
	refresh := func(want string) WebsiteKeyInventoryReport {
		t.Helper()
		view, err := service.RefreshWebsiteKeys(ctx, provider)
		if err != nil || view.Status != want || len(view.Keys) != 4 {
			t.Fatalf("refresh status=%s keys=%d err=%v", view.Status, len(view.Keys), err)
		}
		return view
	}
	view := refresh("current")
	cc, gemini, gm, gpt := view.Keys[0], view.Keys[1], view.Keys[2], view.Keys[3]
	if cc.ID != 1001 || cc.Name != "cc不满血" || cc.GroupID == nil || *cc.GroupID != 62 ||
		cc.GroupName == nil || *cc.GroupName != "[CC2]ClaudeCode高智" || cc.Rate == nil || *cc.Rate != "0.6" ||
		cc.CredentialID == nil || *cc.CredentialID != claude || cc.MatchStatus != "matched" ||
		cc.CreatedAt == nil || cc.Enabled == nil || !*cc.Enabled || cc.KeyPrefix == nil || *cc.KeyPrefix != "sk-cc2a" ||
		cc.LastSeen == nil || !cc.LastSeen.Equal(now) {
		t.Fatal("CC2 identity, rate or metadata did not match the screenshot")
	}
	if gemini.CredentialID == nil || *gemini.CredentialID != credentials[0] || gemini.Rate == nil || *gemini.Rate != "0.3" ||
		gm.GroupName == nil || *gm.GroupName != "GM1" || gm.CredentialID == nil || *gm.CredentialID != credentials[1] ||
		gm.Rate != nil || gpt.GroupID != nil || gpt.Enabled == nil || *gpt.Enabled || gpt.CredentialID == nil || *gpt.CredentialID != credentials[2] {
		t.Fatal("other Keys did not preserve their own group, status and missing facts")
	}
	encoded, err := json.Marshal(view)
	if err != nil || strings.Contains(string(encoded), bearer) {
		t.Fatal("unexpected session value in serialized inventory")
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("teacher inventory contained a complete Key")
		}
	}
	var latestRate string
	var observedAt time.Time
	var quoteInvalidated, leaseCanceled bool
	err = db.QueryRowContext(ctx, `
SELECT trim_scale(m.multiplier)::text,m.observed_at,
       (SELECT expires_at IS NULL FROM beichen_panel_snapshots WHERE credential_id=$1 ORDER BY effective_at DESC LIMIT 1),
       (SELECT lease_id IS NULL FROM beichen_datasets WHERE credential_id=$1 AND kind='billing')
FROM beichen_multiplier_versions m WHERE m.credential_id=$1 ORDER BY m.effective_at DESC,m.id DESC LIMIT 1`, claude).
		Scan(&latestRate, &observedAt, &quoteInvalidated, &leaseCanceled)
	if err != nil || latestRate != "0.6" || !observedAt.Equal(now) || !quoteInvalidated || !leaseCanceled {
		t.Fatalf("group change did not take effect in the same transaction: %v", err)
	}
	assertVersionCount := func() {
		t.Helper()
		var count int
		err := db.QueryRowContext(ctx, "SELECT count(*) FROM beichen_multiplier_versions WHERE credential_id=$1", claude).Scan(&count)
		if err != nil || count != 2 {
			t.Fatalf("CC2 version history is not idempotent: %d (%v)", count, err)
		}
	}
	assertVersionCount()
	job := syncJob{provider: provider, credential: claude, kind: "billing"}
	var revision time.Time
	if err := db.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", claude).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := service.finishJob(ctx, job, oldLease, credentialRecord{updatedAt: revision}, fetchResult{}, false); err == nil {
		t.Fatal("previous billing job could still commit after the website observed the group change")
	}

	now = now.Add(time.Second)
	view = refresh("current")
	assertVersionCount()
	for _, expired := range []int32{1, 3} {
		responseState.Store(expired)
		now = now.Add(time.Second)
		failed := refresh("session_expired")
		if !failed.LastSeen.Equal(*view.LastSeen) || failed.Keys[0].Rate == nil || *failed.Keys[0].Rate != "0.6" {
			t.Fatal("expired session advanced or erased the last successful observation")
		}
		assertVersionCount()
	}
	responseState.Store(2)
	view = refresh("current")
	if view.Keys[0].GroupID != nil || view.Keys[0].Rate != nil || view.Keys[0].MatchStatus != "matched" {
		t.Fatal("missing group inherited old data or lost the Key match")
	}
	assertVersionCount()
	responseState.Store(4)
	view = refresh("current")
	if view.Keys[0].MatchStatus != "ambiguous" || view.Keys[3].MatchStatus != "ambiguous" || view.Keys[0].CredentialID != nil {
		t.Fatal("non-unique Key matches were assigned to a school credential")
	}
	assertVersionCount()
}
