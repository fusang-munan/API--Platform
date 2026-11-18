package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const panelManagementPath = "/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen/access/00000000-0000-4000-8000-000000000002"

func TestPanelAccessRoutesRequireAuthentication(t *testing.T) {
	server := NewServer(nil, nil, nil, nil, HealthDependencies{}, Config{})
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest(method, "https://school.example"+panelManagementPath, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401", method, w.Code)
		}
	}
}

func TestPanelAccessHTTPProtection(t *testing.T) {
	const allowed = "https://school.example"
	const secret = "private-panel-test-fixture"
	const validBody = `{"enabled":true,"group_id":1,"token":"` + secret + `"}`
	server := &Server{config: Config{AllowedOrigin: allowed}}
	router := gin.New()
	router.PATCH("/api/v1/teacher/providers/:provider_id/beichen/access/:credential_id", server.beichenPanelAccess)
	for _, tc := range []struct {
		name, url, peer, origin, proto, body, errorText string
	}{
		{"plain HTTP", "http://school.example", "", allowed, "", validBody, "可信 HTTPS"},
		{"spoofed public peer", "http://school.example", "203.0.113.2:45678", allowed, "https", validBody, "可信 HTTPS"},
		{"spoofed loopback", "http://school.example", "127.0.0.1:45678", allowed, "https", validBody, "可信 HTTPS"},
		{"wrong origin", "http://backend", "172.30.10.4:45678", "https://other.example", "https", validBody, "可信 HTTPS"},
		{"TLS unknown field", allowed, "", "", "", `{"enabled":true,"unknown":"` + secret + `"}`, "invalid request body"},
		{"TLS trailing object", allowed, "", "", "", validBody + `{}`, "invalid request body"},
		{"TLS missing enabled", allowed, "", "", "", `{"token":"` + secret + `"}`, "请指定采价授权状态"},
		{"TLS numeric key ID", allowed, "", "", "", `{"upstream_key_id":42,"token":"` + secret + `"}`, "请指定采价授权状态"},
		{"TLS nonnumeric key ID", allowed, "", "", "", `{"enabled":true,"upstream_key_id":"42","token":"` + secret + `"}`, "invalid request body"},
		{"proxied HTTPS", "http://backend", "172.30.10.4:45678", allowed, "https", `{"token":"` + secret + `"}`, "请指定采价授权状态"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, tc.url+panelManagementPath, strings.NewReader(tc.body))
			if tc.peer != "" {
				req.RemoteAddr = tc.peer
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.errorText) ||
				strings.Contains(w.Body.String(), secret) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status %d, missing expected validation response or no-store", w.Code)
			}
		})
	}
}
