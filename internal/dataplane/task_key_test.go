package dataplane

import "testing"

func TestUserTurnTaskKeyScopesByPrompt(t *testing.T) {
	t.Parallel()
	session := "cc:405a740f-fd23-4d2f-bf9d-c47290ffb049"
	a := userTurnTaskKey(session, "round one")
	b := userTurnTaskKey(session, "round two")
	c := userTurnTaskKey(session, "round one")
	if a == b {
		t.Fatalf("expected different keys for different prompts")
	}
	if a != c {
		t.Fatalf("expected stable key for same prompt")
	}
	if !stringsHasPrefix(a, session+":") {
		t.Fatalf("expected session prefix, got %q", a)
	}
}

func TestSlashCommandStaysOnSessionTurn(t *testing.T) {
	t.Parallel()
	session := "cc:405a740f-fd23-4d2f-bf9d-c47290ffb049"
	got := enrichCallCorrelationForUserTurn(CallCorrelation{TaskKey: session}, "/model")
	if got.TaskKey != session {
		t.Fatalf("slash command opened a new turn key %q", got.TaskKey)
	}
	again := enrichCallCorrelationForUserTurn(CallCorrelation{TaskKey: session}, "继续工作")
	if again.TaskKey != session {
		t.Fatalf("continuation opened a new turn key %q", again.TaskKey)
	}
}

func TestNormalizeSessionScopeFromClaudeMetadataJSON(t *testing.T) {
	t.Parallel()
	raw := `{"device_id":"dev","account_uuid":"","session_id":"405a740f-fd23-4d2f-bf9d-c47290ffb049"}`
	if got := normalizeSessionScope(raw); got != "cc:405a740f-fd23-4d2f-bf9d-c47290ffb049" {
		t.Fatalf("unexpected scope %q", got)
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
