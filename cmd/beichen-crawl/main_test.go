package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/beichen"
)

func TestCrawlCommandFindsSavedLocalSessionByDefault(t *testing.T) {
	base := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", base)
	} else {
		t.Setenv("XDG_CONFIG_HOME", base)
		if runtime.GOOS == "darwin" {
			t.Setenv("HOME", base)
			base = filepath.Join(base, "Library", "Application Support")
		}
	}
	for _, name := range []string{"BEICHEN_PANEL_TOKEN", "BEICHEN_WEB_EMAIL", "BEICHEN_WEB_PASSWORD", "BEICHEN_SESSION_LOCAL_FILE", "DATABASE_URL"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(base, "Nebula", "beichen-session.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	// A malformed file fails locally before any website request. This also
	// detects an accidental fallback to other credentials or fixture mode.
	if err := os.WriteFile(path, []byte(`{"version":-1}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--keys"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "本机北辰会话解密失败") {
			t.Fatalf("default local session was not used: exit %d, stderr %q", code, stderr.String())
		}
	}
}

func TestCrawlCommandKeepsFixtureOffline(t *testing.T) {
	for _, flag := range []string{"--keys", "--save-keys", "--compare-models"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--fixture", "channels.json", flag}, &stdout, &stderr); code != 2 ||
			!strings.Contains(stderr.String(), "离线") || stdout.Len() != 0 {
			t.Fatalf("expected offline option error for %s: exit %d", flag, code)
		}
	}
}

func TestCrawlCommandWritesJSONAndFailsOnParseErrors(t *testing.T) {
	t.Setenv("BEICHEN_WEB_EMAIL", "")
	t.Setenv("BEICHEN_WEB_PASSWORD", "")
	t.Setenv("BEICHEN_PANEL_TOKEN", "")
	for _, tc := range []struct {
		name         string
		fixture      string
		exitCode     int
		modelCount   int
		failureCount int
	}{
		{"complete", `[{"platforms":[{"platform":"first","models":[{"id":"one"}]}]}]`, 0, 1, 0},
		{"partial", `[{"platforms":[{"platform":"first","models":[{"id":"one"}]},{"platform":"second","models":{}}]}]`, 1, 1, 1},
		{"malformed", `{"code":0,"data":`, 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "channels.json")
			if err := os.WriteFile(path, []byte(tc.fixture), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"--fixture", path}, &stdout, &stderr)
			if code != tc.exitCode || !strings.Contains(stderr.String(), "未提供真实凭据，仅 fixture") {
				t.Fatalf("exit %d, stderr %q", code, stderr.String())
			}
			var report beichen.CrawlReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.Source != "fixture" ||
				report.ModelCount != tc.modelCount || len(report.Failures) != tc.failureCount || report.Complete != (code == 0) {
				t.Fatalf("invalid report: %s (%v)", stdout.String(), err)
			}
		})
	}
}
