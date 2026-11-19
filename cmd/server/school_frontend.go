package main

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const schoolFrontendOrigin = "http://frontend:8082"

var schoolAsset = regexp.MustCompile(`^/assets/(?:[\pL\pN_-][\pL\pN_.-]*/)*[\pL\pN_-][\pL\pN_.-]*\.[\pL\pN_-]+$`)

func schoolRequestPath(u *url.URL) bool {
	canonical := url.URL{Path: u.Path}
	if u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") ||
		strings.ContainsAny(u.Path, "\\%?#") || strings.Contains(u.Path, "//") ||
		u.EscapedPath() != canonical.EscapedPath() {
		return false
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func schoolStaticPath(path string) string {
	switch path {
	case "/":
		return "/index.html"
	case "/index.html", "/user_manual.html", "/codex-guide.html", "/cc-switch-guide.html", "/.well-known/nebula-release.txt":
		return path
	}
	switch strings.TrimSuffix(path, "/") {
	case "/login", "/register", "/recovery",
		"/student/api-keys", "/student/models", "/student/usage",
		"/mentor/projects", "/mentor/reviews", "/mentor/call-review", "/mentor/call-logs", "/mentor/input-monitor",
		"/teacher/students", "/teacher/organizations", "/teacher/project-budgets", "/teacher/projects",
		"/teacher/project-spend", "/teacher/providers", "/teacher/models":
		return "/index.html"
	}
	if schoolAsset.MatchString(path) {
		return path
	}
	return ""
}

type schoolFrontend struct {
	origin string
	client *http.Client
}

func newSchoolFrontend(origin string) *schoolFrontend {
	// Use the private service directly, independently of any environment proxy.
	return &schoolFrontend{
		origin: origin,
		client: &http.Client{
			Transport: &http.Transport{DisableCompression: true, IdleConnTimeout: 90 * time.Second},
			Timeout:   10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (f *schoolFrontend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	staticPath := schoolStaticPath(r.URL.Path)
	if staticPath == "" || !schoolRequestPath(r.URL) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	target := url.URL{Path: staticPath}
	// Build a fresh request without browser credentials, query parameters or body.
	req, err := http.NewRequestWithContext(r.Context(), r.Method, f.origin+target.EscapedPath(), nil)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if strings.HasPrefix(staticPath, "/assets/") {
		for _, name := range []string{"If-None-Match", "If-Modified-Since"} {
			for _, value := range r.Header.Values(name) {
				req.Header.Add(name, value)
			}
		}
	}
	resp, err := f.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		http.NotFound(w, r)
	case http.StatusOK, http.StatusNotModified:
		for _, name := range []string{"Content-Type", "Content-Length", "Cache-Control", "ETag", "Last-Modified"} {
			if value := resp.Header.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.WriteHeader(resp.StatusCode)
		if resp.StatusCode == http.StatusOK && r.Method == http.MethodGet {
			_, _ = io.Copy(w, resp.Body)
		}
	default:
		w.WriteHeader(http.StatusBadGateway)
	}
}
