package dataplane_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/catalog"
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
	addBinding := func(id, providerID, credential uuid.UUID, adapter, upstream string) {
		t.Helper()
		exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, providerID, credential, upstream)
		exec(`INSERT INTO model_bindings(id,model_id,provider_id,upstream_model_name,adapter,credential_id,created_at,updated_at)
			VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,now(),now())`, id, providerID, upstream, adapter, credential)
	}
	addModel := func(publicID, status, adapter, upstream string, common bool) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO models(id,model_id,display_name,status,is_common,credit_multiplier_milli,created_at,updated_at)
			VALUES($1,$2::text,$2::text,$3,$4,1000,now(),now())`, id, publicID, status, common)
		if adapter != "" {
			addBinding(id, providerID, credential, adapter, upstream)
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
	linkModel := func(id uuid.UUID) {
		t.Helper()
		exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
			VALUES(gen_random_uuid(),$1,$2,now())`, keyID, id)
	}
	linkModel(oldID)
	var beichenProvider uuid.UUID
	if err := db.QueryRowContext(ctx, `SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'`).Scan(&beichenProvider); err != nil {
		t.Fatal(err)
	}
	beichenCredential := uuid.New()
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
		VALUES($1,$2,'Stored probe fixture',$3,now(),now())`, beichenCredential, beichenProvider, ciphertext)
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES($1,'catalog',clock_timestamp())`, beichenCredential)
	seedProbe := func(id uuid.UUID, modelID string, status int, errorCode string) {
		t.Helper()
		addBinding(id, beichenProvider, beichenCredential, "openai_responses", modelID)
		exec(`INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at)
			VALUES($1,$2,$3,'openai_responses',$4,NULLIF($5,''),1,clock_timestamp())`, beichenProvider, beichenCredential, modelID, status, errorCode)
	}
	const failedModel = "gpt-fixture-failed"
	seedProbe(addModel(failedModel, "active", "", "", true), failedModel, 503, "http_error")
	legacyIDs := []string{existingModel}
	var verifiedIDs []string
	for i := 1; i <= 8; i++ {
		modelID := fmt.Sprintf("gpt-fixture-old-%d", i)
		id := addModel(modelID, "active", "", "", true)
		linkModel(id)
		legacyIDs = append(legacyIDs, modelID)
		if i <= 2 {
			seedProbe(id, modelID, 200, "")
			verifiedIDs = append(verifiedIDs, modelID)
		}
	}
	// The most recent probe can fail while an earlier successful probe still
	// verifies a different protocol for the same model.
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,upstream_model_name,adapter,credential_id,created_at,updated_at)
		SELECT gen_random_uuid(),m.id,$1,m.model_id::text,'openai_compatible',$2,now(),now()
		FROM models m WHERE m.model_id=$3`, beichenProvider, beichenCredential, verifiedIDs[0])
	var latestProbe time.Time
	if err := db.QueryRowContext(ctx, `INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at)
		SELECT provider_id,credential_id,model,'openai_compatible',503,'http_error',1,checked_at+interval '1 second'
		FROM upstream_probes WHERE credential_id=$1 AND model=$2 AND protocol='openai_responses'
		RETURNING checked_at`, beichenCredential, verifiedIDs[0]).Scan(&latestProbe); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO api_key_audits (id,api_key_id,actor_user_id,action,comment,created_at)
		VALUES(gen_random_uuid(),$1,$2,'student_model_added',$3,now())`, keyID, student, existingModel)

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
	type gatewayModel struct {
		ID string `json:"id"`
		catalog.ModelAvailability
	}
	readGateway := func(query string) []gatewayModel {
		t.Helper()
		rec := serve("GET", "/v1/models"+query, studentKey, "")
		var response struct{ Data []gatewayModel }
		if err := json.Unmarshal([]byte(requireStatus(rec, 200)), &response); err != nil {
			t.Fatal(err)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("model list is cacheable")
		}
		return response.Data
	}
	modelList := func() []string {
		t.Helper()
		models := readGateway("")
		ids := make([]string, 0, len(models))
		for _, item := range models {
			ids = append(ids, item.ID)
		}
		return ids
	}
	readCodex := func(query string) []string {
		t.Helper()
		rec := serve("GET", "/v1/codex/model_catalog"+query, studentKey, "")
		var response struct{ Models []struct{ Slug string } }
		if err := json.Unmarshal([]byte(requireStatus(rec, 200)), &response); err != nil {
			t.Fatal(err)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Codex catalog is cacheable")
		}
		ids := make([]string, 0, len(response.Models))
		for _, item := range response.Models {
			ids = append(ids, item.Slug)
		}
		return ids
	}
	keyPath := "/api/v1/student/api-keys/" + keyID.String()
	endpoint := keyPath + "/models"
	removeEndpoint := endpoint + "/remove"
	newBody := `{"model_ids":["gpt-fixture-new","gpt-fixture-new"]}`
	responsesBody := `{"model":"gpt-fixture-new","input":"offline fixture"}`
	if got := modelList(); !slices.Equal(got, verifiedIDs) {
		t.Fatalf("pre-grant models: %v", got)
	}
	if got := readCodex(""); !slices.Equal(got, verifiedIDs) {
		t.Fatalf("pre-grant Codex models: %v", got)
	}
	if got := readCodex("?include_unavailable=1"); !slices.Equal(got, legacyIDs) {
		t.Fatalf("Codex catalog omitted old authorizations: %v", got)
	}
	var keyDetail struct{ Data controlplane.APIKeyView }
	body := requireStatus(serve("GET", keyPath, studentToken, ""), 200)
	if err := json.Unmarshal([]byte(body), &keyDetail); err != nil {
		t.Fatal(err)
	}
	diagnostics := readGateway("?include_unavailable=1")
	if len(diagnostics) != 9 || len(keyDetail.Data.Models) != 9 {
		t.Fatalf("legacy links missing in diagnostics or key detail: %d, %d", len(diagnostics), len(keyDetail.Data.Models))
	}
	detailByID := make(map[string]*catalog.ModelAvailability)
	for _, model := range keyDetail.Data.Models {
		detailByID[model.ModelID] = model.ModelAvailability
	}
	for _, model := range diagnostics {
		want := catalog.AvailabilityMaintenance
		if slices.Contains(verifiedIDs, model.ID) {
			want = catalog.AvailabilityAvailable
		}
		detail := detailByID[model.ID]
		if detail == nil || model.Availability != want || detail.Availability != want ||
			!reflect.DeepEqual(detail.ProtocolSummary, model.ProtocolSummary) || !reflect.DeepEqual(detail.LastProbeAt, model.LastProbeAt) ||
			model.AvailabilityState != "" || detail.AvailabilityState != want {
			t.Fatalf("%s availability mismatch: gateway=%+v detail=%+v", model.ID, model.ModelAvailability, detailByID[model.ID])
		}
	}
	requireStatus(serve("GET", "/v1/models?include_unavailable=1", "invalid-fixture-key", ""), 401)
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
		{`{"model_ids":["gpt-fixture-new","fixture-disabled"]}`, "停用或还未发布", 409},
		{`{"model_ids":["fixture-no-route"]}`, "没有可接入的线路", 409},
		{`{"model_ids":["fixture-stale"]}`, "没有可接入的线路", 409},
		{`{"model_ids":["gpt-fixture-failed"]}`, "探活均失败", 409},
		{`{"model_ids":["fixture-nonexistent"]}`, "model not found in platform catalog", 404},
	} {
		body := requireStatus(serve("POST", endpoint, studentToken, refusal.body), refusal.status)
		if !strings.Contains(body, refusal.reason) {
			t.Fatalf("missing rejection reason %q: %s", refusal.reason, body)
		}
	}
	if got := modelList(); !slices.Equal(got, verifiedIDs) || sends != 0 {
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
	var warnings []controlplane.StudentAPIKeyModelWarning
	wantGranted := append(slices.Clone(legacyIDs), newModel)
	slices.Sort(wantGranted)
	for range 2 {
		got := readGrant(<-replies)
		if !slices.Equal(got.ModelIDs, wantGranted) {
			t.Fatalf("grant returned a stale model list: %v", got.ModelIDs)
		}
		added = append(added, got.AddedModelIDs...)
		warnings = append(warnings, got.Warnings...)
	}
	if !slices.Equal(added, []string{newModel}) {
		t.Fatalf("duplicate concurrent grants: %v", added)
	}
	if len(warnings) != 1 || warnings[0].ModelID != newModel || warnings[0].Code != "MODEL_UNVERIFIED" || warnings[0].Message == "" {
		t.Fatalf("missing unverified warning: %+v", warnings)
	}
	if got := readGrant(serve("POST", endpoint, studentToken, newBody)); got.AddedModelIDs == nil || len(got.AddedModelIDs) != 0 {
		t.Fatalf("repeat grant did not return an empty addition list: %+v", got)
	}
	wantVisible := append([]string{newModel}, verifiedIDs...)
	if got := modelList(); !slices.Equal(got, wantVisible) {
		t.Fatalf("model list did not change immediately: %v", got)
	}
	if got := readCodex(""); !slices.Equal(got, wantVisible) {
		t.Fatalf("Codex catalog did not include the unverified addition: %v", got)
	}
	assertProbeTimes := func(models []json.RawMessage) {
		t.Helper()
		probeTimes := make(map[string]json.RawMessage)
		for _, raw := range models {
			var model struct {
				ID          string          `json:"model_id"`
				LastProbeAt json.RawMessage `json:"last_probe_at"`
			}
			if err := json.Unmarshal(raw, &model); err != nil {
				t.Fatal(err)
			}
			probeTimes[model.ID] = model.LastProbeAt
		}
		var checkedAt time.Time
		if err := json.Unmarshal(probeTimes[verifiedIDs[0]], &checkedAt); err != nil || !checkedAt.Equal(latestProbe) {
			t.Fatalf("last_probe_at should show the newer failed probe: got %v (%v), want %v", checkedAt, err, latestProbe)
		}
		if got := string(probeTimes[newModel]); got != "null" {
			t.Fatalf("model without probe should have last_probe_at=null, got %q", got)
		}
	}
	var keys struct {
		Data []struct {
			ID     string
			Models []json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(requireStatus(serve("GET", "/api/v1/student/api-keys", studentToken, ""), 200)), &keys); err != nil {
		t.Fatal(err)
	}
	var keyModels []json.RawMessage
	for _, key := range keys.Data {
		if key.ID == keyID.String() {
			keyModels = key.Models
		}
	}
	assertProbeTimes(keyModels)
	var plaza struct {
		Data struct {
			CommonModels []json.RawMessage `json:"common_models"`
		}
	}
	if err := json.Unmarshal([]byte(requireStatus(serve("GET", "/api/v1/student/models", studentToken, ""), 200)), &plaza); err != nil {
		t.Fatal(err)
	}
	assertProbeTimes(plaza.Data.CommonModels)
	requireStatus(serve("POST", "/v1/responses", studentKey, responsesBody), 200)

	if got := readGrant(serve("POST", endpoint, studentToken, `{"model_ids":["claude-fixture-new"]}`)); !slices.Equal(got.AddedModelIDs, []string{libraryModel}) {
		t.Fatalf("model outside the personal library could not be granted: %+v", got)
	}
	wantList := append([]string{libraryModel}, wantVisible...)
	if got := modelList(); !slices.Equal(got, wantList) {
		t.Fatalf("gateway list omitted a granted model: %v", got)
	}
	requireStatus(serve("POST", "/v1/messages", studentKey, `{"model":"claude-fixture-new","max_tokens":8,"messages":[{"role":"user","content":"offline fixture"}]}`), 200)
	removeBody := `{"model_ids":["gpt-fixture-existing","gpt-fixture-new"]}`
	requireStatus(serve("POST", removeEndpoint, token(otherStudent, "student"), removeBody), 404)
	requireStatus(serve("POST", removeEndpoint, token(teacher, "teacher"), removeBody), 403)
	requireStatus(serve("POST", removeEndpoint, studentToken, `{"model_ids":["gpt-fixture-existing"," "]}`), 400)
	exec(`ALTER TABLE api_key_audits ADD CONSTRAINT fixture_fail_removal_audit
		CHECK (action <> 'student_model_removed' OR comment <> 'gpt-fixture-new')`)
	requireStatus(serve("POST", removeEndpoint, studentToken, removeBody), 503)
	if got := readGateway("?include_unavailable=1"); len(got) != 11 || !slices.Equal(modelList(), wantList) {
		t.Fatalf("audit failure partially removed authorizations: %v", got)
	}
	exec(`ALTER TABLE api_key_audits DROP CONSTRAINT fixture_fail_removal_audit`)
	remove := func(body string) controlplane.StudentAPIKeyModelRemovalView {
		t.Helper()
		rec := serve("POST", removeEndpoint, studentToken, body)
		var envelope struct {
			Data controlplane.StudentAPIKeyModelRemovalView
		}
		if err := json.Unmarshal([]byte(requireStatus(rec, 200)), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	if got := remove(`{"model_ids":["GPT-fixture-existing"]}`); !slices.Equal(got.RemovedModelIDs, []string{existingModel}) || len(got.ModelIDs) != 10 {
		t.Fatalf("legacy maintenance grant was not removed: %+v", got)
	}
	if got := remove(newBody); !slices.Equal(got.RemovedModelIDs, []string{newModel}) || len(got.ModelIDs) != 9 {
		t.Fatalf("active grant was not removed: %+v", got)
	}
	if got := remove(removeBody); got.RemovedModelIDs == nil || len(got.RemovedModelIDs) != 0 || len(got.ModelIDs) != 9 {
		t.Fatalf("removal retry was not idempotent: %+v", got)
	}
	if got := modelList(); !slices.Equal(got, append([]string{libraryModel}, verifiedIDs...)) {
		t.Fatalf("removed grant still in gateway catalog: %v", got)
	}
	if got := readCodex("?include_unavailable=1"); !slices.Equal(got, legacyIDs[1:]) {
		t.Fatalf("removed grant still in Codex diagnostics: %v", got)
	}
	if body := requireStatus(serve("POST", "/v1/responses", studentKey, responsesBody), 400); !strings.Contains(body, "model_not_allowed") {
		t.Fatalf("removed model call did not fail authorization: %s", body)
	}
	if sends != 2 {
		t.Fatalf("expected two calls to the in-memory upstream, got %d", sends)
	}
	keysBody := requireStatus(serve("GET", "/api/v1/student/api-keys", studentToken, ""), 200)
	if !strings.Contains(keysBody, libraryModel) || strings.Contains(keysBody, studentKey) {
		t.Fatal("student key list did not refresh models safely")
	}
	var auditCount, addedAudits, removedAudits int
	if err := db.QueryRowContext(ctx, `SELECT count(*),
		count(DISTINCT comment) FILTER (WHERE actor_user_id=$2 AND action='student_model_added' AND comment IN ($3,$4,$5) AND created_at IS NOT NULL),
		count(DISTINCT comment) FILTER (WHERE actor_user_id=$2 AND action='student_model_removed' AND comment IN ($3,$4) AND created_at IS NOT NULL)
		FROM api_key_audits WHERE api_key_id=$1`, keyID, student, existingModel, newModel, libraryModel).Scan(&auditCount, &addedAudits, &removedAudits); err != nil {
		t.Fatal(err)
	}
	if auditCount != 5 || addedAudits != 3 || removedAudits != 2 {
		t.Fatalf("incorrect per-model audits: total=%d added=%d removed=%d", auditCount, addedAudits, removedAudits)
	}

	// Credential status alone must hide orphaned models even if their bindings
	// and publication are still active. Historical key grants stay visible.
	exec(`UPDATE provider_credentials SET status='inactive' WHERE id=$1`, beichenCredential)
	for _, path := range []string{"/api/v1/student/models", "/api/v1/student/common-models", "/api/v1/student/model-catalog"} {
		body := requireStatus(serve("GET", path, studentToken, ""), 200)
		if strings.Contains(body, verifiedIDs[0]) || !strings.Contains(body, newModel) {
			t.Fatalf("%s did not hide the retired model while retaining a model on another route: %s", path, body)
		}
	}
	if got := modelList(); !slices.Equal(got, []string{libraryModel}) {
		t.Fatalf("gateway catalog after credential retirement: %v", got)
	}
	keysBody = requireStatus(serve("GET", "/api/v1/student/api-keys", studentToken, ""), 200)
	if !strings.Contains(keysBody, verifiedIDs[0]) {
		t.Fatal("credential retirement hid a historical grant from key details")
	}
	grant := requireStatus(serve("POST", endpoint, studentToken, `{"model_ids":["gpt-fixture-failed"]}`), 409)
	if !strings.Contains(grant, "MODEL_NOT_READY") {
		t.Fatalf("adding a model on the retired credential: %s", grant)
	}

	exec("UPDATE provider_credentials SET status='active' WHERE id=$1", beichenCredential)
	exec("UPDATE upstream_probes SET last_passed_at=checked_at WHERE credential_id=$1 AND model=$2", beichenCredential, failedModel)
	grant = requireStatus(serve("POST", endpoint, studentToken, `{"model_ids":["gpt-fixture-failed"]}`), 200)
	if !strings.Contains(grant, "MODEL_DEGRADED") || !strings.Contains(grant, "可能请求失败") {
		t.Fatalf("student grant did not warn about the failing probe: %s", grant)
	}
	body = requireStatus(serve("GET", "/api/v1/student/models", studentToken, ""), 200)
	if err := json.Unmarshal([]byte(body), &plaza); err != nil {
		t.Fatal(err)
	}
	degradedInPlaza := false
	for _, raw := range plaza.Data.CommonModels {
		var item struct {
			ModelID      string `json:"model_id"`
			Availability string `json:"availability"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		if item.ModelID == failedModel && item.Availability == catalog.AvailabilityDegraded {
			degradedInPlaza = true
		}
	}
	if !degradedInPlaza || !slices.Contains(modelList(), failedModel) {
		t.Fatal("degraded model disappeared from the student plaza or default gateway list")
	}

	const terra = "gpt-6.1-terra"
	terraID := addModel(terra, "active", "", "", true)
	const terraUpstream = "fixture-terra-upstream"
	addBinding(terraID, beichenProvider, beichenCredential, "openai_responses", terraUpstream)
	exec(`UPDATE provider_model_catalog SET status='inactive' WHERE credential_id=$1 AND model_id=$2`, beichenCredential, terraUpstream)
	exec(`UPDATE model_bindings SET status='inactive' WHERE model_id=$1`, terraID)
	exec(`INSERT INTO beichen_website_catalogs(credential_id, credential_revision, group_id, checked_at, models)
		SELECT id, updated_at, 42, clock_timestamp(), ARRAY[$2::text] FROM provider_credentials WHERE id=$1`, beichenCredential, terraUpstream)
	linkModel(terraID)
	exec("UPDATE upstream_probes SET last_passed_at=NULL WHERE credential_id=$1 AND model=$2", beichenCredential, failedModel)

	assertKeyState := func(modelID, legacy, state, reason string) {
		t.Helper()
		body := requireStatus(serve("GET", "/api/v1/student/api-keys", studentToken, ""), 200)
		var response struct{ Data []controlplane.APIKeyView }
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		for _, key := range response.Data {
			if key.ID != keyID.String() {
				continue
			}
			for _, model := range key.Models {
				if model.ModelID == modelID {
					if model.Availability != legacy || model.AvailabilityState != state || !strings.Contains(model.AvailabilityReason, reason) {
						t.Fatalf("%s: want %s/%s with reason %q, got %+v", modelID, legacy, state, reason, model.ModelAvailability)
					}
					return
				}
			}
		}
		t.Fatalf("missing authorization for %s", modelID)
	}
	assertKeyState(terra, "maintenance", "channel_unavailable", "目录未列出")
	assertKeyState(failedModel, "maintenance", "probe_failed", "连通测试均失败")
	assertKeyState(verifiedIDs[0], "available", "available", "")
	body = requireStatus(serve("GET", "/api/v1/student/model-catalog?q="+verifiedIDs[0], studentToken, ""), 200)
	var search struct{ Data controlplane.ModelCatalogPage }
	if err := json.Unmarshal([]byte(body), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Data.Items) != 1 || search.Data.Items[0].RawEntry["availability_state"] != "available" {
		t.Fatalf("missing student state in directory: %s", body)
	}

	exec("UPDATE beichen_datasets SET last_success=NULL WHERE credential_id=$1 AND kind='catalog'", beichenCredential)
	assertKeyState(verifiedIDs[0], "available", "available", "")
	assertKeyState(terra, "maintenance", "unverified", "尚未扫描")
	exec("UPDATE beichen_datasets SET last_success=clock_timestamp() WHERE credential_id=$1 AND kind='catalog'", beichenCredential)

	exec("UPDATE beichen_datasets SET last_error='sensitive fixture detail' WHERE credential_id=$1 AND kind='catalog'", beichenCredential)
	assertKeyState(terra, "maintenance", "unverified", "渠道目录读取失败")
	assertKeyState(verifiedIDs[0], "available", "unverified", "渠道目录读取失败")
	exec("UPDATE beichen_datasets SET last_error=NULL, last_success=clock_timestamp()-interval '31 minutes' WHERE credential_id=$1 AND kind='catalog'", beichenCredential)
	assertKeyState(terra, "maintenance", "unverified", "过期")
	exec("UPDATE beichen_datasets SET last_success=clock_timestamp() WHERE credential_id=$1 AND kind='catalog'", beichenCredential)
	exec("UPDATE provider_model_catalog SET status='active' WHERE credential_id=$1 AND model_id=$2", beichenCredential, terraUpstream)
	assertKeyState(terra, "maintenance", "maintenance", "当前线路无法接通")
	exec("UPDATE models SET status='inactive' WHERE id=$1", terraID)
	assertKeyState(terra, "maintenance", "disabled", "老师已停用")

	exec(`INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,effective_at)
SELECT id,1,42,updated_at FROM provider_credentials WHERE id=$1`, beichenCredential)
	exec(`INSERT INTO beichen_multiplier_versions(credential_id,multiplier,group_id,effective_at)
VALUES($1,1,43,clock_timestamp())`, beichenCredential)
	assertKeyState(verifiedIDs[0], "available", "unverified", "渠道目录")
}
