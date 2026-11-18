package dataplane

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func TestDeepSeekAnthropicFillsMissingThinkingOnToolReplay(t *testing.T) {
	t.Parallel()
	body := `{
		"model":"deepseek-v4-pro",
		"tools":[{"name":"Bash","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"user","content":"check the gpu"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"nvidia-smi"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},
			{"role":"assistant","content":"still working"}
		]
	}`
	raw := rewriteDeepSeek(t, body, "https://api.deepseek.com")
	if !bytes.Contains(raw, []byte(`{"type":"thinking","thinking":""},{"type":"tool_use","id":"toolu_1"`)) {
		t.Fatalf("tool turn did not gain a thinking block: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`{"type":"thinking","thinking":""},{"type":"text","text":"still working"}`)) {
		t.Fatalf("text turn did not gain a thinking block: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"command":"nvidia-smi"`)) {
		t.Fatalf("tool input was rewritten: %s", raw)
	}
}

func TestDeepSeekAnthropicKeepsExistingThinking(t *testing.T) {
	t.Parallel()
	body := `{
		"model":"deepseek-flash",
		"tools":[{"name":"Bash","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"assistant","content":[{"type":"thinking","thinking":"keep me","signature":"sig"},{"type":"text","text":"done"}]}
		]
	}`
	raw := rewriteDeepSeek(t, body, "https://api.deepseek.com/v1")
	if bytes.Count(raw, []byte(`"type":"thinking"`)) != 1 || !bytes.Contains(raw, []byte(`"thinking":"keep me"`)) || !bytes.Contains(raw, []byte(`"signature":"sig"`)) {
		t.Fatalf("existing thinking was changed: %s", raw)
	}
}

func TestDeepSeekThinkingRepairStaysOffWithoutToolsOrWhenDisabled(t *testing.T) {
	t.Parallel()
	withoutTools := `{"model":"deepseek-flash","messages":[{"role":"assistant","content":"hello"}]}`
	raw := rewriteDeepSeek(t, withoutTools, "https://api.deepseek.com")
	if bytes.Contains(raw, []byte(`"type":"thinking"`)) {
		t.Fatalf("thinking was injected without tools: %s", raw)
	}
	disabled := `{"model":"deepseek-flash","thinking":{"type":"disabled"},"tools":[{"name":"Bash"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]}]}`
	raw = rewriteDeepSeek(t, disabled, "https://api.deepseek.com/anthropic")
	if bytes.Contains(raw, []byte(`"type":"thinking"`)) {
		t.Fatalf("thinking was injected while thinking is disabled: %s", raw)
	}
	otherHost := `{"model":"claude","tools":[{"name":"Bash"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]}]}`
	raw = rewriteDeepSeek(t, otherHost, "https://api.anthropic.com")
	if bytes.Contains(raw, []byte(`"type":"thinking"`)) {
		t.Fatalf("non-DeepSeek host was rewritten: %s", raw)
	}
}

func rewriteDeepSeek(t *testing.T, body, baseURL string) []byte {
	t.Helper()
	replay, err := captureBody(io.NopCloser(strings.NewReader(body)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	upstream, _, _, cleanup, err := rewriteUpstreamBody("application/json", replay, "deepseek-v4-pro", modelbinding.AdapterAnthropic, baseURL, "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := io.ReadAll(upstream)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
