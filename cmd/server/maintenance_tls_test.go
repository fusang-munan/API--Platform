package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/labtls"
)

func TestMaintenanceHTTPSUsesNormalAuthentication(t *testing.T) {
	cert, err := labtls.LoadOrCreate(t.TempDir(), "https://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.NewServer(nil, nil, nil, &dataplane.Gateway{}, httpapi.HealthDependencies{}, httpapi.Config{CookieSecure: true})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.html" {
			t.Errorf("unexpected origin path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<div id=app></div>"))
	}))
	defer origin.Close()
	server := httptest.NewUnstartedServer(maintenanceHandler(api.Handler(), false, nil, newSchoolFrontend(origin.URL)))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/teacher/providers", 200},
		{"GET", "/student/api-keys", 200},
		{"GET", "/api/v1/teacher/providers", 401},
		{"GET", "/api/v1/student/api-keys", 401},
		{"GET", "/api/v1/student/usage/calls/receipt%25id", 401},
		{"GET", "/v1/models", 401},
		{"POST", "/v1/messages", 401},
		{"POST", "/v1beta/models/example:streamGenerateContent?alt=sse", 401},
		{"POST", "/v1beta/models/example%3AembedContent", 401},
		{"POST", "/v1beta/models/example:generateContent?key=fixture", 400},
		{"POST", "/v1beta/models/example:unknown", 404},
		{"POST", "/v2/rerank", 401},
		{"POST", "/v2/unknown", 404},
		{"GET", "/health/ready", 404},
		{"GET", "/.well-known/nebula-maintenance", 200},
		{"HEAD", "/.well-known/nebula-maintenance", 200},
		{"POST", "/.well-known/nebula-maintenance", 405},
		{"POST", certificateNoticePath, 404},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s got %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
	untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}
	if resp, err := untrusted.Get(server.URL + "/api/v1/teacher/providers"); err == nil {
		resp.Body.Close()
		t.Fatal("untrusted certificate accepted")
	}
}
