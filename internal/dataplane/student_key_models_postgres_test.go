package dataplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

func TestStudentKeyModelAuthorizationPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager, err := security.NewManager([]byte("fixture-signing-key"), nil, []byte("fixture-api-pepper"), make([]byte, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const studentKey = "fixture-student-key"
	const upstreamKey = "fixture-upstream-key"
	const existingModel = "gpt-fixture-existing"
	const newModel = "gpt-fixture-new"
	const libraryModel = "claude-fixture-new"
	sends := 0
	db, client, gateway := dataplane.NewBeichenActivationFixture(t, ctx, manager, func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		if r.URL.Host != "fixture-upstream.test" {
			t.Fatalf("unexpected upstream host: %s", r.URL.Host)
		}
		var payload struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		var body string
		switch r.URL.Path {
		case "/v1/responses":
			if payload.Model != "fixture-responses" || r.Header.Get("Authorization") != "Bearer "+upstreamKey {
				t.Fatal("unexpected Responses model or credential")
			}
			body = `{"id":"fixture-response","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}`
		case "/v1/messages":
			if payload.Model != "fixture-messages" || r.Header.Get("x-api-key") != upstreamKey {
				t.Fatal("unexpected Messages model or credential")
			}
			body = `{"id":"fixture-message","type":"message","usage":{"input_tokens":2,"output_tokens":1}}`
		default:
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		sends++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	db.SetMaxOpenConns(4)
	service := controlplane.NewService(client, db, nil, manager, nil, controlplane.Config{})
	server := httpapi.NewServer(service, manager, nil, gateway, httpapi.HealthDependencies{}, httpapi.Config{}).Handler()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}

	student, otherStudent, teacher := uuid.New(), uuid.New(), uuid.New()
	org, project, approvedProject, keyID, approvedKeyID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	providerID, credential := uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at) VALUES
		($1,'Student','s@example.test','fixture','student',now(),now()),
		($2,'Other student','other@example.test','fixture','student',now(),now()),
		($3,'Teacher','t@example.test','fixture','teacher',now(),now())`, student, otherStudent, teacher)
	exec(`INSERT INTO student_profiles(id,user_id,created_at,updated_at) VALUES
		(gen_random_uuid(),$1,now(),now()),(gen_random_uuid(),$2,now(),now())`, student, otherStudent)
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture',now(),now())`, org)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at) VALUES
		($1,$3,'Course',10000,now(),now()),($2,$3,'Approved course',10000,now(),now())`, project, approvedProject, org)
	exec(`INSERT INTO project_members(id,project_id,user_id,monthly_credit_quota_milli,created_at)
		VALUES(gen_random_uuid(),$1,$2,10000,now())`, project, student)
	exec(`INSERT INTO api_keys(id,owner_user_id,project_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at) VALUES
		($1,$2,$3,'course','active',$4,'fixture',now(),now(),now()),
		($5,$2,$6,'approved','approved',NULL,NULL,NULL,now(),now())`,
		keyID, student, project, manager.HashAPIKey(studentKey), approvedKeyID, approvedProject)

	ciphertext, err := manager.EncryptCredential(upstreamKey)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at)
		VALUES($1,'Fake upstream','https://fixture-upstream.test',now(),now())`, providerID)
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
		VALUES($1,$2,'Fake key',$3,now(),now())`, credential, providerID, ciphertext)
	addModel := func(publicID, status, adapter, upstream string, common bool) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO models(id,model_id,display_name,status,is_common,credit_multiplier_milli,created_at,updated_at)
			VALUES($1,$2::text,$2::text,$3,$4,1000,now(),now())`, id, publicID, status, common)
		if adapter != "" {
			exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
				VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, providerID, credential, upstream)
			exec(`INSERT INTO model_bindings(id,model_id,provider_id,upstream_model_name,adapter,credential_id,created_at,updated_at)
				VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,now(),now())`, id, providerID, upstream, adapter, credential)
		}
		return id
	}
	oldID := addModel(existingModel, "active", "", "", true)
	addModel(newModel, "active", "openai_responses", "fixture-responses", true)
	addModel(libraryModel, "active", "anthropic", "fixture-messages", false)
	addModel("fixture-disabled", "inactive", "openai_responses", "fixture-disabled-upstream", true)
	addModel("fixture-no-route", "active", "", "", true)
	addModel("fixture-stale", "active", "openai_responses", "fixture-stale-upstream", true)
	exec(`UPDATE provider_model_catalog SET status='inactive' WHERE model_id='fixture-stale-upstream'`)
	exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
		VALUES(gen_random_uuid(),$1,$2,now())`, keyID, oldID)

	token := func(userID uuid.UUID, role string) string {
		t.Helper()
		jwt, _, err := manager.IssueAccessToken(userID, role)
		if err != nil {
			t.Fatal(err)
		}
		return jwt
	}
	studentToken := token(student, "student")
	serve := func(method, path, credential, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	requireStatus := func(rec *httptest.ResponseRecorder, status int) string {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("want status %d, got %d: %s", status, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	readGrant := func(rec *httptest.ResponseRecorder) controlplane.StudentAPIKeyModelsView {
		t.Helper()
		var envelope struct {
			Data controlplane.StudentAPIKeyModelsView
		}
		if err := json.Unmarshal([]byte(requireStatus(rec, 200)), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	modelList := func() []string {
		t.Helper()
		rec := serve("GET", "/v1/models", studentKey, "")
		var response struct{ Data []struct{ ID string } }
		if err := json.Unmarshal([]byte(requireStatus(rec, 200)), &response); err != nil {
			t.Fatal(err)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("model list is cacheable")
		}
		ids := make([]string, 0, len(response.Data))
		for _, item := range response.Data {
			ids = append(ids, item.ID)
		}
		return ids
	}
	endpoint := "/api/v1/student/api-keys/" + keyID.String() + "/models"
	newBody := `{"model_ids":["gpt-fixture-new","gpt-fixture-new"]}`
	responsesBody := `{"model":"gpt-fixture-new","input":"offline fixture"}`
	if got := modelList(); !slices.Equal(got, []string{existingModel}) {
		t.Fatalf("pre-grant models: %v", got)
	}
	requireStatus(serve("POST", "/v1/responses", studentKey, responsesBody), 400)
	requireStatus(serve("POST", endpoint, token(otherStudent, "student"), newBody), 404)
	requireStatus(serve("POST", endpoint, token(teacher, "teacher"), newBody), 403)
	requireStatus(serve("POST", "/api/v1/student/api-keys/"+approvedKeyID.String()+"/models", studentToken, newBody), 409)
	for _, body := range []string{`{"model_ids":[]}`, `{"model_ids":[null]}`, `{"model_ids":["gpt-fixture-new"],"owner":"other"}`} {
		requireStatus(serve("POST", endpoint, studentToken, body), 400)
	}
	for _, refusal := range []struct {
		body, reason string
		status       int
	}{
		{`{"model_ids":["gpt-fixture-new","fixture-disabled"]}`, "model is not enabled for students", 409},
		{`{"model_ids":["fixture-no-route"]}`, "model has no usable route", 409},
		{`{"model_ids":["fixture-stale"]}`, "model has no usable route", 409},
		{`{"model_ids":["claude-fixture-new"]}`, "model is not available in your model plaza", 404},
	} {
		body := requireStatus(serve("POST", endpoint, studentToken, refusal.body), refusal.status)
		if !strings.Contains(body, refusal.reason) {
			t.Fatalf("missing rejection reason %q: %s", refusal.reason, body)
		}
	}
	if got := modelList(); !slices.Equal(got, []string{existingModel}) || sends != 0 {
		t.Fatalf("rejected requests changed the whitelist or used the upstream: models=%v calls=%d", got, sends)
	}
	refusal := requireStatus(serve("POST", "/api/v1/student/models", studentToken, `{"model_id":"fixture-disabled"}`), 400)
	if !strings.Contains(refusal, "model is not enabled for students") {
		t.Fatalf("missing reason for selection from a stale catalog: %s", refusal)
	}

	// Race two identical grants and require exactly one new link and audit.
	start := make(chan struct{})
	replies := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			replies <- serve("POST", endpoint, studentToken, newBody)
		}()
	}
	close(start)
	var added []string
	for range 2 {
		got := readGrant(<-replies)
		if !slices.Equal(got.ModelIDs, []string{existingModel, newModel}) {
			t.Fatalf("grant returned a stale model list: %v", got.ModelIDs)
		}
		added = append(added, got.AddedModelIDs...)
	}
	if !slices.Equal(added, []string{newModel}) {
		t.Fatalf("duplicate concurrent grants: %v", added)
	}
	if got := readGrant(serve("POST", endpoint, studentToken, newBody)); got.AddedModelIDs == nil || len(got.AddedModelIDs) != 0 {
		t.Fatalf("repeat grant did not return an empty addition list: %+v", got)
	}
	if got := modelList(); !slices.Equal(got, []string{existingModel, newModel}) {
		t.Fatalf("model list did not change immediately: %v", got)
	}
	requireStatus(serve("POST", "/v1/responses", studentKey, responsesBody), 200)

	requireStatus(serve("POST", "/api/v1/student/models", studentToken, `{"model_id":"claude-fixture-new"}`), 201)
	readGrant(serve("POST", endpoint, studentToken, `{"model_ids":["claude-fixture-new"]}`))
	wantList := []string{libraryModel, existingModel, newModel}
	if got := modelList(); !slices.Equal(got, wantList) {
		t.Fatalf("gateway list omitted a granted model: %v", got)
	}
	requireStatus(serve("POST", "/v1/messages", studentKey, `{"model":"claude-fixture-new","max_tokens":8,"messages":[{"role":"user","content":"offline fixture"}]}`), 200)
	if sends != 2 {
		t.Fatalf("expected two calls to the in-memory upstream, got %d", sends)
	}
	keysBody := requireStatus(serve("GET", "/api/v1/student/api-keys", studentToken, ""), 200)
	if !strings.Contains(keysBody, newModel) || !strings.Contains(keysBody, libraryModel) || strings.Contains(keysBody, studentKey) {
		t.Fatal("student key list did not refresh models safely")
	}
	var auditCount, matchingAudits int
	if err := db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT comment) FILTER (
		WHERE actor_user_id=$2 AND action='student_model_added' AND comment IN ($3,$4) AND created_at IS NOT NULL
	) FROM api_key_audits WHERE api_key_id=$1`, keyID, student, newModel, libraryModel).Scan(&auditCount, &matchingAudits); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 || matchingAudits != 2 {
		t.Fatalf("expected exactly two matching per-model audits, got %d of %d", matchingAudits, auditCount)
	}
}
