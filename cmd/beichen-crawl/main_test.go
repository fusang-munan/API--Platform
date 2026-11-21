package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/beichen"
)

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
