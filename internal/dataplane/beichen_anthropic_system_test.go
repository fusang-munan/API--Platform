package dataplane

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func decodeMessages(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}

func TestPromoteBeichenAnthropicSystemString(t *testing.T) {
	t.Parallel()
	payload := map[string]json.RawMessage{
		"model":    json.RawMessage(`"m"`),
		"system":   json.RawMessage(`"You are helpful."`),
		"messages": json.RawMessage(`[{"role":"user","content":"hi"}]`),
	}
	if err := promoteBeichenAnthropicSystem(payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["system"]; ok {
		t.Fatal("top-level system was not removed")
	}
	messages := decodeMessages(t, payload["messages"])
	if len(messages) != 2 || string(messages[0]["role"]) != `"system"` || !bytes.Contains(messages[0]["content"], []byte("You are helpful.")) {
		t.Fatalf("system message not promoted: %s", payload["messages"])
	}
}

func TestPromoteBeichenAnthropicSystemPreservesCacheControl(t *testing.T) {
	t.Parallel()
	payload := map[string]json.RawMessage{
		"system":   json.RawMessage(`[{"type":"text","text":"stable","cache_control":{"type":"ephemeral","ttl":"1h"}}]`),
		"messages": json.RawMessage(`[{"role":"user","content":"hi"}]`),
	}
	if err := promoteBeichenAnthropicSystem(payload); err != nil {
		t.Fatal(err)
	}
	messages := decodeMessages(t, payload["messages"])
	if len(messages) != 2 || !bytes.Contains(messages[0]["content"], []byte(`"cache_control"`)) ||
		!bytes.Contains(messages[0]["content"], []byte(`"ttl":"1h"`)) {
		t.Fatalf("cache_control lost: %s", payload["messages"])
	}
}

func TestPromoteBeichenAnthropicSystemKeepsExistingSystemMessage(t *testing.T) {
	t.Parallel()
	payload := map[string]json.RawMessage{
		"system":   json.RawMessage(`"ignored"`),
		"messages": json.RawMessage(`[{"role":"system","content":"already"}]`),
	}
	if err := promoteBeichenAnthropicSystem(payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["system"]; !ok {
		t.Fatal("existing system message should leave the payload untouched")
	}
}

func TestRewriteUpstreamBodyPromotesBeichenAnthropicSystem(t *testing.T) {
	t.Parallel()
	body := `{"model":"public","system":"SYS","messages":[{"role":"user","content":"hi"}]}`
	replay, err := captureBody(io.NopCloser(strings.NewReader(body)), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	out, _, _, cleanup, err := rewriteUpstreamBody("application/json", replay, "upstream",
		modelbinding.AdapterAnthropic, "https://api.beichenapi.com", "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"system":"SYS"`)) || !bytes.Contains(raw, []byte(`"role":"system"`)) || !bytes.Contains(raw, []byte("SYS")) {
		t.Fatalf("beichen system not promoted: %s", raw)
	}
}

func TestRewriteUpstreamBodyKeepsSystemForStandardAnthropic(t *testing.T) {
	t.Parallel()
	body := `{"model":"public","system":"SYS","messages":[{"role":"user","content":"hi"}]}`
	replay, err := captureBody(io.NopCloser(strings.NewReader(body)), 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	out, _, _, cleanup, err := rewriteUpstreamBody("application/json", replay, "upstream",
		modelbinding.AdapterAnthropic, "https://api.anthropic.com", "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"system":"SYS"`)) || bytes.Contains(raw, []byte(`"role":"system"`)) {
		t.Fatalf("standard Anthropic system shape changed: %s", raw)
	}
}
