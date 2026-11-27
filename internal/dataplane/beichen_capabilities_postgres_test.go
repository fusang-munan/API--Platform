package dataplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestBeichenCapabilitiesAndGatewayPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	manager, err := security.NewManager([]byte("fixture-signing-key"), nil, []byte("fixture-api-pepper"), make([]byte, 32), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	db, client, gateway := dataplane.NewBeichenActivationFixture(t, ctx, manager, func(_ *http.Request) (*http.Response, error) {
		sends++
		return nil, errors.New("capability and mismatch requests must finish locally")
	})
	service := controlplane.NewService(client, db, nil, manager, nil, controlplane.Config{})
	handler := httpapi.NewServer(service, manager, nil, gateway, httpapi.HealthDependencies{Database: db}, httpapi.Config{}).Handler()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var provider uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url='https://api.beichenapi.com'").Scan(&provider); err != nil {
		t.Fatal(err)
	}
	teacher, student, org, project, keyID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	const studentKey = "fixture-student-key"
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
 VALUES($1,'Teacher','t-capabilities@example.test','fixture','teacher',now(),now()),
       ($2,'Student','s-capabilities@example.test','fixture','student',now(),now())`, teacher, student)
	exec(`INSERT INTO organizations(id,name,created_at,updated_at) VALUES($1,'Fixture',now(),now())`, org)
	exec(`INSERT INTO projects(id,organization_id,name,monthly_credit_quota_milli,created_at,updated_at)
 VALUES($1,$2,'Fixture',10000,now(),now())`, project, org)
	exec(`INSERT INTO api_keys(id,owner_user_id,project_id,name,status,key_hash,key_prefix,claimed_at,created_at,updated_at)
 VALUES($1,$2,$3,'Fixture','active',$4,'fixture',now(),now(),now())`, keyID, student, project, manager.HashAPIKey(studentKey))
	type line struct {
		label, model string
		protocols    []string
		preferred    string
		id           uuid.UUID
	}
	lines := []line{
		{"CC1", "claude-fixture", []string{"anthropic", "openai_compatible"}, "anthropic", uuid.New()},
		{"G3", "gpt-fixture", []string{"openai_responses"}, "openai_responses", uuid.New()},
		{"GM1", "deepseek-fixture", []string{"openai_compatible"}, "openai_compatible", uuid.New()},
		{"Gemini", "gemini-fixture", []string{"google_gemini_v1beta", "openai_compatible"}, "google_gemini_v1beta", uuid.New()},
	}
	store := catalog.ProviderCatalogSyncer{DB: db}
	const forbidden = "fixture-not-in-key"
	const missingProtocol = "fixture-protocol-gap"
	for _, l := range lines {
		ciphertext, err := manager.EncryptCredential("fake-upstream-" + l.label)
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
 VALUES($1,$2,$3,$4,now(),now())`, l.id, provider, l.label, ciphertext)
		entries := []catalog.ProviderEntry{{ID: l.model, SupportedProtocols: l.protocols}}
		if l.label == "G3" {
			entries = append(entries, catalog.ProviderEntry{ID: forbidden, SupportedProtocols: []string{"openai_responses"}},
				catalog.ProviderEntry{ID: missingProtocol, Raw: map[string]any{"supported_endpoint_types": []string{"unknown-wire-protocol"}}})
		}
		var observedAt time.Time
		if err := db.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&observedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertCredentialEntries(ctx, provider, l.id, entries, observedAt, nil); err != nil {
			t.Fatal(err)
		}
		school := uuid.New()
		exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
 VALUES($1,$2::text,$2::text,'active',1000,now(),now())`, school, l.model)
		exec(`INSERT INTO api_key_models(id,api_key_id,model_id,created_at)
 VALUES(gen_random_uuid(),$1,$2,now())`, keyID, school)
		for _, protocol := range l.protocols {
			exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
 VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,100,now(),now())`, school, provider, l.id, l.model, protocol)
		}
	}
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
 VALUES(gen_random_uuid(),$1::text,$1::text,'active',1000,now(),now())`, forbidden)

	jwt := func(id uuid.UUID, role string) string {
		t.Helper()
		token, _, err := manager.IssueAccessToken(id, role)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	serve := func(method, path, token, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s status %d, want %d: %s", method, path, res.Code, status, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "fake-upstream-") {
			t.Fatal("response leaked fixture credential")
		}
		return res
	}
	teacherToken := jwt(teacher, "teacher")
	pathPrefix := "/api/v1/teacher/providers/"
	path := pathPrefix + provider.String() + "/beichen/capabilities"
	read := func() catalog.BeichenCapabilities {
		t.Helper()
		res := serve("GET", path, teacherToken, "", 200)
		var envelope struct{ Data catalog.BeichenCapabilities }
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil || res.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("invalid capabilities response: %v", err)
		}
		return envelope.Data
	}
	readLine := func(model string) catalog.ModelCapability {
		t.Helper()
		for _, item := range read().Capabilities {
			if item.Model == model {
				return item
			}
		}
		t.Fatalf("missing %s", model)
		return catalog.ModelCapability{}
	}
	type modelItem struct {
		ID string `json:"id"`
		catalog.ProtocolSummary
		Availability       string `json:"availability"`
		AvailabilityReason string `json:"availability_reason"`
	}
	modelsList := func(query string) map[string]modelItem {
		t.Helper()
		res := serve("GET", "/v1/models"+query, studentKey, "", 200)
		var envelope struct{ Data []modelItem }
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		result := make(map[string]modelItem, len(envelope.Data))
		for _, model := range envelope.Data {
			result[model.ID] = model
		}
		return result
	}
	type protocolError struct {
		Supported []string `json:"supported_protocols"`
		Recommend string   `json:"recommended_protocol"`
		Message   string   `json:"message"`
		Code      any      `json:"code"`
		Reason    string   `json:"reason"`
	}
	readError := func(res *httptest.ResponseRecorder) protocolError {
		t.Helper()
		var envelope struct{ Error protocolError }
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Error
	}

	t.Run("A/four keys and native protocol errors", func(t *testing.T) {
		serve("GET", path, "", "", 401)
		serve("GET", path, jwt(student, "student"), "", 403)
		serve("GET", pathPrefix+"invalid/beichen/capabilities", teacherToken, "", 400)
		serve("GET", pathPrefix+uuid.NewString()+"/beichen/capabilities", teacherToken, "", 404)
		last := modelsList("")
		if len(last) != 4 {
			t.Fatalf("want only the four whitelisted models, got %v", last)
		}
		for _, l := range lines {
			row := readLine(l.model)
			want := slices.Clone(l.protocols)
			slices.Sort(want)
			if row.CredentialID == nil || *row.CredentialID != l.id || row.CredentialLabel != l.label ||
				!slices.Equal(row.SupportedProtocols, want) || row.RecommendedProtocol == nil || *row.RecommendedProtocol != l.preferred {
				t.Fatalf("wrong %s capability: %+v", l.label, row)
			}
			model := last[l.model]
			if !slices.Equal(model.Protocols, want) || model.RecommendedProtocol == nil || *model.RecommendedProtocol != l.preferred {
				t.Fatalf("gateway and capabilities disagree: %+v", model)
			}
		}
		var teacherModels struct{ Data []controlplane.ModelView }
		res := serve("GET", "/api/v1/teacher/models", teacherToken, "", 200)
		if err := json.Unmarshal(res.Body.Bytes(), &teacherModels); err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			idx := slices.IndexFunc(teacherModels.Data, func(row controlplane.ModelView) bool {
				return row.ModelID == l.model && row.CredentialID == l.id.String()
			})
			if idx < 0 {
				t.Fatalf("missing teacher model %s", l.model)
			}
			info, cap := teacherModels.Data[idx], readLine(l.model)
			if info.ResponsesReady != cap.ResponsesReady || !slices.Equal(info.SupportedProtocols, cap.SupportedProtocols) ||
				info.ResponsesReady != slices.Contains(l.protocols, "openai_responses") ||
				!slices.Equal(info.AvailableProtocols, cap.AvailableProtocols) {
				t.Fatalf("teacher model and capabilities disagree: %+v %+v", info, cap)
			}
		}
		for _, tc := range []struct {
			path, body, recommended, endpoint string
			support                           []string
			code                              any
		}{
			{"/v1/responses", `{"model":"claude-fixture"}`, "anthropic", "/v1/messages", lines[0].protocols, "protocol_mismatch"},
			{"/v1/messages", `{"model":"gpt-fixture"}`, "openai_responses", "/v1/responses", lines[1].protocols, "protocol_mismatch"},
			{"/v1beta/models/deepseek-fixture:generateContent", `{}`, "openai_compatible", "/v1/chat/completions", lines[2].protocols, float64(400)},
			{"/v1/responses", `{"model":"gemini-fixture"}`, "google_gemini_v1beta", "/v1beta/models/{model}:generateContent", lines[3].protocols, "protocol_mismatch"},
		} {
			got := readError(serve("POST", tc.path, studentKey, tc.body, 400))
			if got.Code != tc.code || got.Recommend != tc.recommended || !slices.Equal(got.Supported, tc.support) ||
				!strings.Contains(got.Message, "请改用") || !strings.Contains(got.Message, tc.endpoint) || !strings.Contains(got.Message, "Base URL") ||
				(tc.code == float64(400) && got.Reason != "protocol_mismatch") {
				t.Fatalf("%s wrong advice: %+v", tc.path, got)
			}
		}
		denied := readError(serve("POST", "/v1/responses", studentKey, `{"model":"fixture-not-in-key"}`, 400))
		if denied.Code != "model_not_allowed" || denied.Supported != nil {
			t.Fatalf("protocol details leaked for a model outside whitelist: %+v", denied)
		}
	})
	t.Run("B/failed Messages falls back to verified Chat", func(t *testing.T) {
		claude := lines[0]
		probe := func(protocol string, status int, code string) {
			t.Helper()
			exec(`INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at)
 VALUES($1,$2,$3,$4,$5,NULLIF($6,''),1,clock_timestamp())
 ON CONFLICT(provider_id,credential_id,model,protocol) DO UPDATE SET
 status_code=excluded.status_code,error_code=excluded.error_code,checked_at=excluded.checked_at`, provider, claude.id, claude.model, protocol, status, code)
		}
		probe("anthropic", 403, "http_error")
		probe("openai_compatible", 200, "")
		row, model := readLine(claude.model), modelsList("")[claude.model]
		if row.RecommendedProtocol == nil || *row.RecommendedProtocol != "openai_compatible" ||
			model.RecommendedProtocol == nil || *model.RecommendedProtocol != "openai_compatible" ||
			!slices.Equal(row.AvailableProtocols, []string{"openai_compatible"}) || !slices.Equal(model.AvailableProtocols, row.AvailableProtocols) {
			t.Fatalf("failed Messages stayed recommended: %+v %+v", row, model)
		}
		mismatch := readError(serve("POST", "/v1/responses", studentKey, `{"model":"claude-fixture"}`, 400))
		if mismatch.Recommend != "openai_compatible" {
			t.Fatalf("mismatch did not recommend passing Chat: %+v", mismatch)
		}
		probe("openai_compatible", 503, "http_error")
		if _, listed := modelsList("")[claude.model]; listed {
			t.Fatal("maintenance model appeared in the default list")
		}
		row, model = readLine(claude.model), modelsList("?include_unavailable=1")[claude.model]
		if row.Availability != "maintenance" || row.ProbeState != "failed" || row.RecommendedProtocol != nil ||
			model.Availability != "maintenance" || model.RecommendedProtocol != nil || !strings.Contains(model.AvailabilityReason, "探活均失败") {
			t.Fatalf("failed route disappeared or remained recommended: %+v %+v", row, model)
		}
		maintenance := readError(serve("POST", "/v1/responses", studentKey, `{"model":"claude-fixture"}`, 503))
		if maintenance.Code != "model_unavailable" || !strings.Contains(maintenance.Message, "维护中") {
			t.Fatalf("missing gateway maintenance reason: %+v", maintenance)
		}
		exec(`UPDATE upstream_probes SET last_passed_at=checked_at WHERE credential_id=$1 AND model=$2 AND protocol='openai_compatible'`,
			claude.id, claude.model)
		row, model = readLine(claude.model), modelsList("")[claude.model]
		if row.Availability != catalog.AvailabilityDegraded || model.Availability != catalog.AvailabilityDegraded ||
			model.ProbeState != "failed" || !strings.Contains(model.AvailabilityReason, "可能请求失败") {
			t.Fatalf("recently successful model was removed or labeled incorrectly: %+v %+v", row, model)
		}
		hasEvidence := false
		for _, detail := range row.ProtocolDetails {
			if detail.Protocol == "openai_compatible" && detail.Probe != nil && detail.Probe.LastPassedAt != nil &&
				detail.Probe.LastPassedAt.Equal(detail.Probe.CheckedAt) {
				hasEvidence = true
			}
		}
		if !hasEvidence {
			t.Fatalf("teacher capabilities lost last_passed_at: %+v", row.ProtocolDetails)
		}
		exec("UPDATE provider_credentials SET updated_at=clock_timestamp()+interval '1 millisecond' WHERE id=$1", claude.id)
		row, model = readLine(claude.model), modelsList("?include_unavailable=1")[claude.model]
		if row.CatalogState != "catalog_stale" || row.Availability != "unverified" || row.RecommendedProtocol != nil ||
			model.RecommendedProtocol != nil || model.ProbeState != "not_applicable" {
			t.Fatalf("rotated Key kept previous catalog or probe evidence: %+v %+v", row, model)
		}
		exec(`UPDATE provider_model_catalog k SET last_seen_at=c.updated_at
 FROM provider_credentials c WHERE k.credential_id=c.id AND c.id=$1`, claude.id)
		row, model = readLine(claude.model), modelsList("")[claude.model]
		if row.ProbeState != "unprobed" || model.ProbeState != "unprobed" {
			t.Fatalf("fresh catalog inherited a previous Key's probes: %+v %+v", row, model)
		}
		// A fresh failure on the new Key will be present in the gap report.
		probe("anthropic", 403, "http_error")
	})
	t.Run("D/actionable coverage report", func(t *testing.T) {
		l := lines[1]
		exec("INSERT INTO beichen_multiplier_versions(credential_id,multiplier) VALUES($1,1.25)", l.id)
		exec("INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES($1,'billing',now()-interval '1 hour')", l.id)
		exec("UPDATE model_bindings SET status='inactive' WHERE provider_id=$1 AND credential_id=$2", provider, l.id)
		row := readLine(l.model)
		if row.PriceState != "missing" || row.PriceBlocker == nil || row.PriceBlocker.Code != "missing_price" ||
			row.MultiplierState != "expired" || row.BindingState != "invalid" {
			t.Fatalf("missing diagnostic states: %+v", row)
		}
		want := map[string]string{
			"missing_price": "报价", "multiplier_expired": "倍率", "binding_invalid": "重绑",
			"probe_failed": "探活", "protocol_missing": "协议",
		}
		for _, gap := range read().Gaps {
			if action, ok := want[gap.Code]; ok && strings.Contains(gap.ActionHint, action) && gap.Message != "" {
				delete(want, gap.Code)
			}
		}
		if len(want) > 0 {
			t.Fatalf("missing actionable report rows: %v", want)
		}
	})
	if sends != 0 {
		t.Fatalf("read-only capability or rejected requests attempted %d upstream calls", sends)
	}
}
