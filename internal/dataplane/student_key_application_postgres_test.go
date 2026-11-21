package dataplane_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

func TestStudentKeyApplicationWithoutModelsPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager, err := security.NewManager([]byte("fixture-signing-key"), nil, []byte("fixture-api-pepper"), make([]byte, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	db, client, gateway := dataplane.NewBeichenActivationFixture(t, ctx, manager, func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected upstream request: %s", r.URL.Host)
		return nil, nil
	})
	service := controlplane.NewService(client, db, nil, manager, nil, controlplane.Config{})
	handler := httpapi.NewServer(service, manager, nil, gateway, httpapi.HealthDependencies{}, httpapi.Config{}).Handler()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE providers SET status='inactive'`)
	student, mentor, unrelated := uuid.New(), uuid.New(), uuid.New()
	org, project, legacyProject := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at) VALUES
		($1,'Student','s-apply@example.test','fixture','student',now(),now()),
		($2,'Mentor','m-apply@example.test','fixture','student',now(),now()),
		($3,'Unrelated mentor','other-apply@example.test','fixture','student',now(),now())`, student, mentor, unrelated)
	exec(`INSERT INTO student_profiles(id,user_id,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,now(),now())`, student)
	exec(`INSERT INTO mentor_profiles(id,user_id,created_at,updated_at)
		VALUES(gen_random_uuid(),$1,now(),now()),(gen_random_uuid(),$2,now(),now())`, mentor, unrelated)
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture',now(),now())`, org)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at) VALUES
		($1,$3,'Course',10000,now(),now()),($2,$3,'Legacy course',10000,now(),now())`, project, legacyProject, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,created_at)
		VALUES(gen_random_uuid(),$1,$3,now()),(gen_random_uuid(),$2,$3,now())`, project, legacyProject, mentor)
	serve := func(method, path, body string, userID uuid.UUID, role string, want int) []byte {
		t.Helper()
		token, _, err := manager.IssueAccessToken(userID, role)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s got %d, want %d: %s", method, path, res.Code, want, res.Body.String())
		}
		return res.Body.Bytes()
	}

	const studentPath = "/api/v1/student/api-keys"
	submit := func(projectID uuid.UUID, name, legacyField string) string {
		t.Helper()
		body := fmt.Sprintf(`{"name":%q,"organization_id":%q,"project_id":%q,"requested_monthly_credits":"2.00"%s}`,
			name, org.String(), projectID.String(), legacyField)
		response := serve("POST", studentPath, body, student, "student", 201)
		var envelope struct{ Data controlplane.APIKeyView }
		if err := json.Unmarshal(response, &envelope); err != nil {
			t.Fatal(err)
		}
		key := envelope.Data
		if key.Status != "pending_mentor" || key.Models == nil || len(key.Models) != 0 {
			t.Fatalf("new application was not created with empty authorization: %s", response)
		}
		return key.ID
	}
	key := submit(project, "no models", "")
	legacyKey := submit(legacyProject, "old client", `,"models":[{"model_id":"unknown-model"},{"display_name":"no model ID"}]`)
	approvePath := func(key string) string { return "/api/v1/mentor/api-key-reviews/" + key + "/approve" }
	serve("POST", approvePath(key), `{"monthly_credits":"2.00"}`, unrelated, "mentor", 404)
	exec(`UPDATE projects SET status='inactive' WHERE id=$1`, project)
	serve("POST", approvePath(key), `{"monthly_credits":"2.00"}`, mentor, "mentor", 409)
	exec(`UPDATE projects SET status='active' WHERE id=$1`, project)
	serve("POST", approvePath(key), `{"monthly_credits":"11.00"}`, mentor, "mentor", 409)
	var status string
	var models, membership, audits int
	read := func(keyID string) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT k.status,
			(SELECT count(*) FROM api_key_models km WHERE km.api_key_id=k.id),
			(SELECT count(*) FROM project_members pm WHERE pm.project_id=k.project_id AND pm.user_id=k.owner_user_id AND pm.monthly_credit_quota_milli=2000),
			(SELECT count(*) FROM api_key_audits a WHERE a.api_key_id=k.id AND a.action='mentor_approved' AND a.actor_user_id=$2)
			FROM api_keys k WHERE k.id=$1`, keyID, mentor).Scan(&status, &models, &membership, &audits); err != nil {
			t.Fatal(err)
		}
	}
	read(key)
	if status != "pending_mentor" || models != 0 || membership != 0 || audits != 0 {
		t.Fatalf("rejected approval modified state: %s %d %d %d", status, models, membership, audits)
	}
	serve("POST", approvePath(key), `{"monthly_credits":"2.00"}`, mentor, "mentor", 204)
	read(key)
	if status != "approved" || models != 0 || membership != 1 || audits != 1 {
		t.Fatalf("zero-model approval did not commit together: %s %d %d %d", status, models, membership, audits)
	}
	var member bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM organization_members WHERE organization_id=$1 AND user_id=$2)`, org, student).Scan(&member); err != nil || !member {
		t.Fatalf("missing organization membership: %v", err)
	}
	var claim struct{ Data controlplane.ClaimView }
	if err := json.Unmarshal(serve("POST", studentPath+"/"+key+"/claim", "", student, "student", 200), &claim); err != nil {
		t.Fatal(err)
	}
	var modelList struct{ Data []json.RawMessage }
	req := httptest.NewRequest("GET", "/v1/models", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+claim.Data.APIKey)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if err := json.Unmarshal(res.Body.Bytes(), &modelList); res.Code != 200 || err != nil || modelList.Data == nil || len(modelList.Data) != 0 {
		t.Fatalf("newly claimed key did not expose an empty model list: %d %s", res.Code, res.Body.Bytes())
	}

	// Pending applications created before the upgrade may already have links.
	model := uuid.New()
	exec(`INSERT INTO models(id,model_id,display_name,status,created_at,updated_at)
		VALUES($1,'retired-model','Retired model','inactive',now(),now())`, model)
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
		VALUES(gen_random_uuid(),$1,$2,now())`, legacyKey, model)
	serve("POST", approvePath(legacyKey), `{"monthly_credits":"2.00"}`, mentor, "mentor", 204)
	read(legacyKey)
	if status != "approved" || models != 1 || membership != 1 || audits != 1 {
		t.Fatalf("legacy model blocked approval or was dropped: %s %d %d %d", status, models, membership, audits)
	}
}
