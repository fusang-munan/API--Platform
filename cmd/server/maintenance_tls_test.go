package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/labtls"
)

func TestMaintenanceHTTPSUsesNormalAuthentication(t *testing.T) {
	cert, err := labtls.LoadOrCreate(t.TempDir(), "https://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.NewServer(nil, nil, nil, nil, httpapi.HealthDependencies{}, httpapi.Config{CookieSecure: true})
	server := httptest.NewUnstartedServer(maintenanceHandler(api.Handler(), false))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for path, want := range map[string]int{"/api/v1/teacher/providers": 401, "/health/ready": 404, "/.well-known/nebula-maintenance": 200} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s got %d, want %d", path, resp.StatusCode, want)
		}
	}
	untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}
	if resp, err := untrusted.Get(server.URL + "/api/v1/teacher/providers"); err == nil {
		resp.Body.Close()
		t.Fatal("untrusted certificate accepted")
	}
}
