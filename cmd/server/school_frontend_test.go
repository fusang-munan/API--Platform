package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSchoolFrontend(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "example.invalid")

	const manualDiagram = "中转站-申请流程图.drawio.svg"
	browserHeaders := http.Header{
		"Authorization":       {"Bearer fixture"},
		"Cookie":              {"fixture=browser"},
		"Proxy-Authorization": {"Basic fixture"},
		"Referer":             {"https://example.invalid/private"},
		"X-Api-Key":           {"fixture"},
		"X-Goog-Api-Key":      {"fixture"},
		"X-Forwarded-Host":    {"example.invalid"},
	}
	for _, tc := range []struct {
		method, path, originPath, mime string
		want                           int
	}{
		{"GET", "/", "/index.html", "text/html", 200},
		{"GET", "/login?redirect=/teacher/projects", "/index.html", "text/html", 200},
		{"GET", "/teacher/providers/", "/index.html", "text/html", 200},
		{"GET", "/student/api-keys/", "/index.html", "text/html", 200},
		{"HEAD", "/student/usage", "/index.html", "text/html", 200},
		{"GET", "/assets/index-WQ25v4A2.js", "/assets/index-WQ25v4A2.js", "text/javascript", 200},
		{"GET", "/assets/index-WQ25v4A2.css?v=fixture", "/assets/index-WQ25v4A2.css", "text/css", 200},
		{"GET", "/assets/brand/favicon-32.png", "/assets/brand/favicon-32.png", "image/png", 200},
		{"GET", "/assets/images/" + url.PathEscape(manualDiagram), "/assets/images/" + manualDiagram, "image/svg+xml", 200},
		{"GET", "/codex-guide.html", "/codex-guide.html", "text/html", 200},
		{"GET", "/cc-switch-guide.html", "/cc-switch-guide.html", "text/html", 200},
		{"GET", "/user_manual.html", "/user_manual.html", "text/html", 200},
		{"GET", "/.well-known/nebula-release.txt", "/.well-known/nebula-release.txt", "text/plain", 200},
		{"GET", "/assets/missing.js", "/assets/missing.js", "", 404},
		{"GET", "/assets/redirect.js", "/assets/redirect.js", "", 502},
		{"POST", "/login", "", "", 405},
		{"GET", "/teacher/unknown", "", "", 404},
		{"GET", "/teacher/providers//", "", "", 404},
		{"GET", "/student//api-keys", "", "", 404},
		{"GET", "/codex-guide.html/", "", "", 404},
		{"GET", "/assets/", "", "", 404},
		{"GET", "/assets/.hidden.txt", "", "", 404},
		{"GET", "/assets//file.js", "", "", 404},
		{"GET", "/assets/../index.html", "", "", 404},
		{"GET", "/assets/%2e%2e/index.html", "", "", 404},
		{"GET", "/assets%2findex.js", "", "", 404},
		{"GET", "/assets/%252e.js", "", "", 404},
		{"GET", "/assets/a%5cb.js", "", "", 404},
		{"GET", "/%6cogin", "", "", 404},
		{"GET", "//example.invalid/login", "", "", 404},
		{"GET", "http://example.invalid/login", "", "", 404},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			calls := make(chan string, 2)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- r.URL.Path
				if r.Host != "frontend:8082" || r.Method != tc.method || r.URL.RawQuery != "" || r.URL.ForceQuery {
					t.Error("unexpected origin host, method or query")
				}
				for name := range browserHeaders {
					if r.Header.Get(name) != "" {
						t.Errorf("forwarded browser header %s", name)
					}
				}
				switch r.URL.Path {
				case "/assets/missing.js":
					http.NotFound(w, r)
				case "/assets/redirect.js":
					http.Redirect(w, r, "/index.html", http.StatusFound)
				default:
					w.Header().Set("Content-Type", tc.mime)
					w.Header().Set("Cache-Control", "no-store")
					w.Header().Set("Set-Cookie", "fixture=ignored")
					_, _ = w.Write([]byte("static content"))
				}
			}))
			defer origin.Close()
			frontend := newSchoolFrontend(schoolFrontendOrigin)
			transport := frontend.client.Transport.(*http.Transport)
			defer transport.CloseIdleConnections()
			dialer := &net.Dialer{}
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Keep the Compose hostname in the URL so loopback proxy exceptions can't mask a regression.
				if addr != "frontend:8082" {
					return nil, fmt.Errorf("unexpected dial target %s", addr)
				}
				return dialer.DialContext(ctx, network, origin.Listener.Addr().String())
			}

			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header = browserHeaders.Clone()
			w := httptest.NewRecorder()
			maintenanceHandler(http.NotFoundHandler(), false, nil, frontend).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.originPath == "" {
				if len(calls) != 0 {
					t.Fatal("rejected request reached the origin")
				}
			} else if len(calls) != 1 || <-calls != tc.originPath {
				t.Fatalf("expected a single origin request to %s", tc.originPath)
			}
			if w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("forwarded an origin redirect or cookie")
			}
			if tc.want == 200 {
				if w.Header().Get("Content-Type") != tc.mime || w.Header().Get("Cache-Control") != "no-store" ||
					w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("unexpected static response headers: %v", w.Header())
				}
				wantBody := "static content"
				if tc.method == http.MethodHead {
					wantBody = ""
				}
				if w.Body.String() != wantBody {
					t.Fatalf("body = %q, want %q", w.Body.String(), wantBody)
				}
			}
		})
	}
}

func TestSchoolFrontendAssetRevalidation(t *testing.T) {
	const asset = "/assets/brand/nebula-n-32.png"
	const etag = `"brand-fixture"`
	const cacheControl = "public, max-age=0, must-revalidate"
	modified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", cacheControl)
		w.Header().Set("ETag", etag)
		http.ServeContent(w, r, "nebula-n-32.png", modified, strings.NewReader("asset"))
	}))
	defer origin.Close()
	frontend := newSchoolFrontend(origin.URL)
	defer frontend.client.CloseIdleConnections()
	lastModified := modified.Format(http.TimeFormat)
	first := httptest.NewRecorder()
	frontend.ServeHTTP(first, httptest.NewRequest(http.MethodGet, asset, nil))
	if first.Code != http.StatusOK || first.Header().Get("ETag") != etag ||
		first.Header().Get("Last-Modified") != lastModified || first.Header().Get("Cache-Control") != cacheControl {
		t.Fatalf("initial asset response: %d %v", first.Code, first.Header())
	}
	for name, value := range map[string]string{"If-None-Match": etag, "If-Modified-Since": lastModified} {
		req := httptest.NewRequest(http.MethodGet, asset, nil)
		req.Header.Set(name, value)
		w := httptest.NewRecorder()
		frontend.ServeHTTP(w, req)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 ||
			w.Header().Get("ETag") != etag || w.Header().Get("Cache-Control") != cacheControl {
			t.Errorf("%s response: %d %v, %d body bytes", name, w.Code, w.Header(), w.Body.Len())
		}
	}
}
