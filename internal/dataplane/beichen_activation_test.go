package dataplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

func TestBeichenTeacherEnablesDiscoveredModelForStudent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager, err := security.NewManager([]byte("fixture-signing-key"), nil, []byte("fixture-api-pepper"), make([]byte, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const model = "fixture-discovered"
	const studentKey = "local-student-key"
	const upstreamKey = "local-north-key"
	sends := 0
	db, client, gateway := dataplane.NewBeichenActivationFixture(t, ctx, manager, func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		if r.URL.String() != "https://api.beichenapi.com/v1/responses" || r.Header.Get("Authorization") != "Bearer "+upstreamKey {
			t.Fatal("unexpected fake upstream route")
		}
		var payload struct{ Model string }
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != model {
			t.Fatal("unexpected upstream model")
		}
		sends++
		httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"fixture-response","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}`))}, nil
	})
	service := controlplane.NewService(client, db, nil, manager, nil, controlplane.Config{})
	server := httpapi.NewServer(service, manager, nil, gateway, httpapi.HealthDependencies{}, httpapi.Config{}).Handler()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var provider uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'").Scan(&provider); err != nil {
		t.Fatal(err)
	}
	key, teacher, student, org, project, studentKeyID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ciphertext, err := manager.EncryptCredential(upstreamKey)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES($1,$2,'line A',$3,now(),now())`, key, provider, ciphertext)
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, provider, key, model)
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES($1,'billing',now())`, key)
	exec(`INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,0.250000000000000000000019)`, key)
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at) VALUES($1,'Teacher','t@example.test','fixture','teacher',now(),now()),($2,'Student','s@example.test','fixture','student',now(),now())`, teacher, student)
	exec(`INSERT INTO student_profiles(id,user_id,created_at,updated_at) VALUES(gen_random_uuid(),$1,now(),now())`, student)
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture',now(),now())`, org)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at) VALUES($1,$2,'Course',10000,now(),now())`, project, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at) VALUES(gen_random_uuid(),$1,$2,10000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,owner_user_id,project_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at) VALUES($1,$2,$3,'course','active',$4,'fixture',now(),now(),now())`, studentKeyID, student, project, manager.HashAPIKey(studentKey))
	jwt := func(id uuid.UUID, role string) string {
		t.Helper()
		token, _, err := manager.IssueAccessToken(id, role)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	teacherToken, studentToken := jwt(teacher, "teacher"), jwt(student, "student")
	request := func(method, path, token, body string, expected int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != expected {
			t.Fatalf("%s %s: %d, %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	path := "/api/v1/teacher/providers/" + provider.String() + "/beichen"
	status := func() beichen.CatalogModel {
		t.Helper()
		var response struct{ Data beichen.Overview }
		if err := json.Unmarshal([]byte(request("GET", path, teacherToken, "", 200)), &response); err != nil {
			t.Fatal(err)
		}
		for _, m := range response.Data.ModelCatalog {
			if m.Model == model {
				return m
			}
		}
		t.Fatal("missing catalog row")
		return beichen.CatalogModel{}
	}
	enable := func() uuid.UUID {
		t.Helper()
		var response struct {
			Data struct {
				ID     uuid.UUID `json:"model_id"`
				Status string
			}
		}
		if err := json.Unmarshal([]byte(request("POST", path+"/models/enable", teacherToken, `{"model":"fixture-discovered"}`, 200)), &response); err != nil || response.Data.ID == uuid.Nil || response.Data.Status != "active" {
			t.Fatalf("enable response: %v %v", response, err)
		}
		return response.Data.ID
	}
	pre := status()
	if !pre.CanEnable || pre.SchoolStatus != "not_configured" || len(pre.PreferredRoutes) == 0 || pre.PreferredRoutes[0].EffectiveMultiplier == nil || pre.PreferredRoutes[0].State != "candidate" {
		t.Fatalf("initial catalog: %+v", pre)
	}
	wantRate, _ := pre.PreferredRoutes[0].EffectiveMultiplier.Float64()
	if wantRate != 0.25 || pre.PreferredRoutes[0].EffectiveMultiplierText != "0.250000000000000000000019" {
		t.Fatal("wrong preview rate")
	}
	request("POST", path+"/models/enable", studentToken, `{"model":"fixture-discovered"}`, 403)
	if r := request("GET", "/v1/models", studentKey, "", 200); strings.Contains(r, model) {
		t.Fatal("unpublished model leaked")
	}
	id := enable()
	if id != enable() {
		t.Fatal("repeated enable returned a different model")
	}
	published := status()
	if published.CanEnable || published.SchoolStatus != "active" || published.PreferredRoutes[0].State != "ready" {
		t.Fatalf("published catalog: %+v", published)
	}
	postBody := `{"model":"fixture-discovered","input":"offline fixture"}`
	request("POST", "/v1/responses", studentKey, postBody, 400)
	if sends != 0 {
		t.Fatal("publication granted an existing key access")
	}
	// Simulate the separate, approved key-model entitlement.
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at) VALUES(gen_random_uuid(),$1,$2,now())`, studentKeyID, id)
	if !strings.Contains(request("GET", "/v1/models", studentKey, "", 200), model) {
		t.Fatal("authorized model missing from student gateway")
	}
	request("POST", "/v1/responses", studentKey, postBody, 200)
	if sends != 1 {
		t.Fatal("expected one in-memory student call")
	}
	// Model-level disable survives discovery; only a second explicit click repairs
	// the retired key. A stale or manually disabled protocol is never revived by GET.
	exec("UPDATE models SET status='inactive',credit_multiplier_milli=NULL WHERE id=$1", id)
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", key)
	if status().CanEnable {
		t.Fatal("retired key remained eligible")
	}
	replacement := uuid.New()
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES($1,$2,'line B',$3,now(),now())`, replacement, provider, ciphertext)
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, provider, replacement, model)
	exec("UPDATE model_bindings SET status='inactive' WHERE model_id=$1 AND adapter='openai_compatible'", id)
	exec("UPDATE models SET model_id='school-alias' WHERE id=$1", id)
	before := status()
	if !before.CanEnable || before.SchoolStatus != "inactive" || before.SchoolModel != "school-alias" {
		t.Fatalf("alias and rotated key: %+v", before)
	}
	if enable() != id {
		t.Fatal("created a second model for the same school alias")
	}
	var ok bool
	if err := db.QueryRowContext(ctx, `SELECT m.credit_multiplier_milli=1000 AND EXISTS(SELECT FROM model_bindings WHERE model_id=m.id AND status='active' AND credential_id=$2 AND adapter='openai_responses')
 AND EXISTS(SELECT FROM model_bindings WHERE model_id=m.id AND status='inactive' AND credential_id=$3 AND adapter='openai_compatible') FROM models m WHERE id=$1`, id, replacement, key).Scan(&ok); err != nil || !ok {
		t.Fatalf("rotation or neutral multiplier: %v", err)
	}
	request("POST", "/v1/responses", studentKey, `{"model":"school-alias","input":"offline fixture"}`, 200)

	t.Run("existing supplier keeps its priority and price", func(t *testing.T) {
		other, otherKey, school := uuid.New(), uuid.New(), uuid.New()
		exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES($1,'External','https://other.invalid',now(),now())`, other)
		exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at) VALUES($1,$2,'external','fixture',now(),now())`, otherKey, other)
		exec(`INSERT INTO models(id,model_id,display_name,created_at,updated_at) VALUES($1,'mixed','Mixed',now(),now())`, school)
		for _, owner := range [][2]uuid.UUID{{provider, replacement}, {other, otherKey}} {
			exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,'mixed',now(),now(),now(),now())`, owner[0], owner[1])
		}
		exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,$3,'mixed','openai_responses',250,now(),now())`, school, other, otherKey)
		request("POST", path+"/models/enable", teacherToken, `{"model":"mixed"}`, 400)
		request("POST", path+"/models/enable", teacherToken, `{"model":"mixed","school_model":"school-alias"}`, 400)
		exec("UPDATE models SET credit_multiplier_milli=1250 WHERE id=$1", school)
		request("POST", path+"/models/enable", teacherToken, `{"model":"mixed"}`, 200)
		if err := db.QueryRowContext(ctx, `SELECT m.credit_multiplier_milli=1250 AND EXISTS(SELECT FROM model_bindings WHERE model_id=m.id AND provider_id=$2 AND priority=250 AND status='active')
 AND EXISTS(SELECT FROM model_bindings WHERE model_id=m.id AND provider_id=$3 AND priority=251 AND adapter='openai_responses') FROM models m WHERE id=$1`, school, other, provider).Scan(&ok); err != nil || !ok {
			t.Fatalf("changed supplier governance or priority: %v", err)
		}
	})
	t.Run("simultaneous first clicks", func(t *testing.T) {
		exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,'concurrent',now(),now(),now(),now())`, provider, replacement)
		db.SetMaxOpenConns(4)
		start := make(chan struct{})
		done := make(chan error, 4)
		for range cap(done) {
			go func() {
				<-start
				_, err := service.Beichen().EnableModel(ctx, provider, teacher, "concurrent", "")
				done <- err
			}()
		}
		close(start)
		for range cap(done) {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*)=1 FROM model_multiplier_audits a JOIN models m ON m.id=a.model_id WHERE m.model_id='concurrent'`).Scan(&ok); err != nil || !ok {
			t.Fatalf("duplicate governance audit: %v", err)
		}
	})
	t.Run("teacher API reports only current coverage", func(t *testing.T) {
		checkPriceStatus := func(want string) {
			t.Helper()
			var response struct{ Data beichen.Overview }
			if err := json.Unmarshal([]byte(request("GET", path, teacherToken, "", 200)), &response); err != nil {
				t.Fatal(err)
			}
			for _, row := range response.Data.Models {
				if row.CredentialID == replacement && row.Model == model {
					if row.PriceStatus != want || row.UpstreamRates != nil || string(row.PriceConditions) != "null" {
						t.Fatalf("expected %q with no inferred price, got %q", want, row.PriceStatus)
					}
					return
				}
			}
			t.Fatal("missing teacher price row")
		}
		checkPriceStatus("pending_confirmation")
		exec(`
WITH multiplier AS (
  INSERT INTO beichen_multiplier_versions(credential_id, effective_at, multiplier)
  VALUES ($1,statement_timestamp()-interval '1 minute',1) RETURNING id, effective_at
)
INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,
  multiplier_version,model_pricing,credential_ciphertext_sha256)
SELECT c.id, m.effective_at, c.updated_at, 7, statement_timestamp()+interval '1 minute', m.id,
  jsonb_build_object($2::text, NULL, 'listed', '{"input_price":0.000001}'::jsonb),sha256(c.credential_ciphertext)
FROM provider_credentials c CROSS JOIN multiplier m WHERE c.id=$1`, replacement, model)
		checkPriceStatus("not_listed")
		exec(`UPDATE beichen_panel_snapshots SET expires_at=statement_timestamp()-interval '30 seconds' WHERE credential_id=$1`, replacement)
		checkPriceStatus("pending_confirmation")
	})
}
