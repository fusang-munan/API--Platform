package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
)

func TestRegistersControlPlaneAndGatewayRoutes(t *testing.T) {
	t.Parallel()
	server := NewServer(nil, nil, nil, (*dataplane.Gateway)(nil), HealthDependencies{}, Config{})
	routes := make(map[string]struct{})
	deleteRoutes := map[string]bool{
		"/api/v1/teacher/providers/:provider_id":                            true,
		"/api/v1/teacher/providers/:provider_id/credentials/:credential_id": true,
		"/api/v1/teacher/projects/:project_id/mentors/:mentor_id":           true,
	}
	for _, route := range server.engine.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
		if route.Method == "DELETE" && !deleteRoutes[route.Path] {
			t.Fatalf("unexpected DELETE route: %s", route.Path)
		}
		if strings.Contains(route.Path, "/api/nebula/gateway") {
			t.Fatalf("legacy gateway route was registered: %s", route.Path)
		}
	}
	expected := []string{
		"GET /health/live",
		"GET /health/ready",
		"POST /api/v1/auth/verification-codes",
		"POST /api/v1/auth/register/student",
		"POST /api/v1/auth/login",
		"POST /api/v1/auth/session",
		"POST /api/v1/auth/refresh",
		"POST /api/v1/auth/logout",
		"POST /api/v1/auth/password/forgot",
		"POST /api/v1/auth/password/reset",
		"POST /api/v1/auth/teacher-invitations/activate",
		"GET /api/v1/me",
		"GET /api/v1/events",
		"GET /api/v1/student/organizations",
		"GET /api/v1/student/organizations/:organization_id/projects",
		"GET /api/v1/student/models",
		"GET /api/v1/student/common-models",
		"GET /api/v1/student/model-catalog",
		"POST /api/v1/student/models",
		"POST /api/v1/student/models/remove",
		"GET /api/v1/student/usage",
		"GET /api/v1/student/usage/calls/:request_id",
		"POST /api/v1/student/api-keys",
		"GET /api/v1/student/api-keys",
		"GET /api/v1/student/api-keys/:api_key_id",
		"POST /api/v1/student/api-keys/:api_key_id/claim",
		"GET /api/v1/mentor/organizations",
		"GET /api/v1/mentor/organizations/:organization_id/projects",
		"POST /api/v1/mentor/project-applications",
		"GET /api/v1/mentor/project-applications",
		"GET /api/v1/mentor/api-key-reviews",
		"GET /api/v1/mentor/api-key-reviews/:api_key_id",
		"POST /api/v1/mentor/api-key-reviews/:api_key_id/approve",
		"POST /api/v1/mentor/api-key-reviews/:api_key_id/reject",
		"GET /api/v1/mentor/api-keys",
		"GET /api/v1/mentor/api-keys/:api_key_id",
		"GET /api/v1/mentor/projects/:project_id/api-keys",
		"GET /api/v1/mentor/projects/:project_id/usage",
		"PATCH /api/v1/mentor/api-keys/:api_key_id/monthly-credit-quota",
		"GET /api/v1/mentor/call-logs",
		"GET /api/v1/mentor/call-logs/:call_id",
		"GET /api/v1/mentor/call-turns",
		"GET /api/v1/mentor/call-turns/:turn_id",
		"GET /api/v1/mentor/input-monitor",
		"GET /api/v1/mentor/input-monitor/:call_id",
		"POST /api/v1/mentor/api-keys/:api_key_id/deactivate",
		"POST /api/v1/mentor/api-keys/:api_key_id/activate",
		"POST /api/v1/teacher/invitations",
		"GET /api/v1/teacher/students",
		"POST /api/v1/teacher/students/:student_id/mentor-profile",
		"POST /api/v1/teacher/students/:student_id/mentor-profile/revoke",
		"GET /api/v1/teacher/organizations",
		"POST /api/v1/teacher/organizations",
		"PATCH /api/v1/teacher/organizations/:organization_id",
		"GET /api/v1/teacher/projects",
		"GET /api/v1/teacher/projects/:project_id/mentor-candidates",
		"GET /api/v1/teacher/project-spend",
		"POST /api/v1/teacher/projects",
		"PATCH /api/v1/teacher/projects/:project_id",
		"GET /api/v1/teacher/mentor-project-applications",
		"POST /api/v1/teacher/mentor-project-applications/:application_id/approve",
		"POST /api/v1/teacher/mentor-project-applications/:application_id/reject",
		"GET /api/v1/teacher/providers",
		"POST /api/v1/teacher/providers",
		"GET /api/v1/teacher/providers/:provider_id",
		"PATCH /api/v1/teacher/providers/:provider_id",
		"DELETE /api/v1/teacher/providers/:provider_id",
		"GET /api/v1/teacher/providers/:provider_id/credentials",
		"POST /api/v1/teacher/providers/:provider_id/credentials",
		"PATCH /api/v1/teacher/providers/:provider_id/credentials/:credential_id",
		"DELETE /api/v1/teacher/providers/:provider_id/credentials/:credential_id",
		"POST /api/v1/teacher/providers/:provider_id/reset-catalog",
		"GET /api/v1/teacher/providers/:provider_id/beichen",
		"GET /api/v1/teacher/providers/:provider_id/beichen/receipts",
		"POST /api/v1/teacher/providers/:provider_id/beichen/sync",
		"GET /api/v1/teacher/providers/:provider_id/beichen/access/:credential_id",
		"PATCH /api/v1/teacher/providers/:provider_id/beichen/access/:credential_id",
		"PATCH /api/v1/teacher/providers/:provider_id/beichen/price",
		"POST /api/v1/teacher/providers/:provider_id/beichen/import",
		"GET /api/v1/teacher/provider-catalog-candidates",
		"GET /api/v1/teacher/models",
		"POST /api/v1/teacher/models",
		"GET /api/v1/teacher/models/detail",
		"PATCH /api/v1/teacher/models/configuration",
		"POST /api/v1/teacher/model-bindings",
		"POST /api/v1/teacher/model-probes",
		"POST /api/v1/teacher/model-binding-route-probes",
		"PATCH /api/v1/teacher/model-bindings/:binding_id",
		"GET /api/v1/teacher/project-budgets",
		"PATCH /api/v1/teacher/project-budgets/:project_id",
		"GET /api/v1/teacher/projects/:project_id/usage",
		"POST /api/v1/teacher/projects/:project_id/mentors/:mentor_id",
		"DELETE /api/v1/teacher/projects/:project_id/mentors/:mentor_id",
		"POST /v1/*path",
		"GET /v1/*path",
		"POST /v1beta/*path",
		"POST /v2/*path",
	}
	for _, route := range expected {
		if _, exists := routes[route]; !exists {
			t.Fatalf("expected route %s is not registered", route)
		}
	}
}

func TestStudentCallReceiptRequiresAuthentication(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, HealthDependencies{}, Config{})
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/student/usage/calls/018a0000-0000-7000-8000-000000000001", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
}

func TestBeichenTeacherRoutesRequireLogin(t *testing.T) {
	server := NewServer(nil, nil, nil, (*dataplane.Gateway)(nil), HealthDependencies{}, Config{})
	base := "/api/v1/teacher/providers/123/beichen"
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, base},
		{http.MethodGet, base + "/receipts"},
		{http.MethodPost, base + "/sync"},
		{http.MethodPatch, base + "/price"},
		{http.MethodPost, base + "/import"},
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", route.method, route.path, response.Code)
		}
	}
}

func TestBeichenUsageExportRequiresFullDayConfirmation(t *testing.T) {
	// Test only the import handler's validation. The full router tests teacher auth.
	server := &Server{}
	router := gin.New()
	router.POST("/:provider_id/beichen/import", server.importBeichen)
	for _, body := range []string{
		`{"credential_id":"00000000-0000-4000-8000-000000000002","kind":"usage_export","csv":"example"}`,
		`{"credential_id":"00000000-0000-4000-8000-000000000002","kind":"usage_export","csv":"example","full_day_confirmed":false}`,
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/00000000-0000-4000-8000-000000000001/beichen/import", strings.NewReader(body))
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "完整的北京时间账日") {
			t.Fatalf("export without confirmation: status %d, body %s", response.Code, response.Body.String())
		}
	}
}
