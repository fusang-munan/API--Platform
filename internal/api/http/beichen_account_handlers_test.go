package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
)

const beichenAccountTestPath = "/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen"

func TestBeichenPanelAccountRoutesRequireAuthentication(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, HealthDependencies{}, Config{})
	for _, endpoint := range []struct{ method, suffix string }{
		{http.MethodGet, "/account"}, {http.MethodPost, "/account/login"}, {http.MethodPost, "/account/verify"},
		{http.MethodPost, "/account/export"},
		{http.MethodDelete, "/account"}, {http.MethodPost, "/crawl"},
	} {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest(endpoint.method, "https://school.example"+beichenAccountTestPath+endpoint.suffix, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401", endpoint.method, endpoint.suffix, w.Code)
		}
	}
}

func TestBeichenPanelAccountLoginRejectsInsecureAndInvalidRequests(t *testing.T) {
	server := &Server{}
	router := gin.New()
	router.POST("/api/v1/teacher/providers/:provider_id/beichen/account/login", func(c *gin.Context) {
		c.Set(identityKey, controlplane.Identity{Role: c.GetHeader("Fixture-Role")})
	}, requireRole("teacher"), server.beichenAccountLogin)
	const secret = "unique-panel-handler-password"
	const credentials = `{"mode":"password","email":"teacher@example.test","password":"` + secret + `"}`
	for _, tc := range []struct {
		name, scheme, role, body, message string
		status                            int
	}{
		{"role", "https", "student", credentials, "", http.StatusForbidden},
		{"HTTPS", "http", "teacher", credentials, "可信 HTTPS", http.StatusBadRequest},
		{"unknown field", "https", "teacher", `{"unknown":"` + secret + `"}`, "请求格式无效", http.StatusBadRequest},
		{"trailing JSON", "https", "teacher", credentials + "{}", "请求格式无效", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.scheme+"://school.example"+beichenAccountTestPath+"/account/login", strings.NewReader(tc.body))
			req.Header.Set("Fixture-Role", tc.role)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) || strings.Contains(w.Body.String(), secret) {
				t.Fatalf("错误或泄漏的登录响应: status %d", w.Code)
			}
			if tc.role == "teacher" && !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("登录响应可被缓存")
			}
		})
	}
}
