package beichen

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProbeDailyLimitDefaultsAndReadsEnvironment(t *testing.T) {
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "")
	if got := probeDailyLimit(); got != 500 {
		t.Fatalf("default limit = %d, want 500", got)
	}
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "37")
	if got := probeDailyLimit(); got != 37 {
		t.Fatalf("configured limit = %d, want 37", got)
	}
	for _, invalid := range []string{"0", "-3", "many"} {
		t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", invalid)
		if got := probeDailyLimit(); got != 500 {
			t.Fatalf("invalid limit %q = %d, want fallback 500", invalid, got)
		}
	}
}

func TestProbeBudgetClampsAndSerializes(t *testing.T) {
	now := time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)
	budget := probeBudget(499, 500, now)
	if budget.Date != "2026-11-22" || budget.Used != 499 || budget.Remaining != 1 || budget.Exhausted {
		t.Fatalf("unexpected budget: %#v", budget)
	}
	if got := probeBudget(800, 500, now); got.Remaining != 0 || !got.Exhausted {
		t.Fatalf("over-limit budget must be exhausted: %#v", got)
	}
	if got := probeBudget(-1, 0, now); got.Used != 0 || got.Limit != 500 || got.Remaining != 500 {
		t.Fatalf("invalid budget was not normalized: %#v", got)
	}
	raw, err := json.Marshal(ProbeRun{State: "completed", Budget: &budget})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"date":"2026-11-22"`, `"used":499`, `"limit":500`, `"remaining":1`, `"exhausted":false`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("missing %s in %s", field, raw)
		}
	}
}

func TestProbeBudgetCountUsesShanghaiDay(t *testing.T) {
	if !strings.Contains(probeBudgetUsedSQL, "Asia/Shanghai") ||
		!strings.Contains(probeBudgetUsedSQL, "date_trunc('day'") {
		t.Fatalf("daily count must use the Shanghai calendar day: %s", probeBudgetUsedSQL)
	}
}
