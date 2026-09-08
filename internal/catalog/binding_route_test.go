package catalog

import "testing"

func TestNormalizeUpstreamModelID(t *testing.T) {
	t.Parallel()
	if got := normalizeUpstreamModelID(" Claude-Opus-5 "); got != "claude-opus-5" {
		t.Fatalf("expected claude-opus-5, got %q", got)
	}
}
