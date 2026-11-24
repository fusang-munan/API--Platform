package usage

import "testing"

func TestObservedCacheTTL(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	for _, tc := range []struct {
		name string
		m    CallMetrics
		want string
	}{
		{"zero_one_hour_infers_five_minute", CallMetrics{cacheWrite1h: n(0), CacheWriteTokens: n(10)}, "5m"},
		{"zero_five_minute_infers_one_hour", CallMetrics{cacheWrite5m: n(0), CacheWriteTokens: n(10)}, "1h"},
		{"one_hour_only", CallMetrics{cacheWrite1h: n(10), CacheWriteTokens: n(10)}, "1h"},
		{"five_minute_only", CallMetrics{cacheWrite5m: n(4)}, "5m"},
		{"partial_uses_lower_rate", CallMetrics{cacheWrite1h: n(1), CacheWriteTokens: n(10)}, "min"},
		{"both_nonzero_is_mixed", CallMetrics{cacheWrite5m: n(9), cacheWrite1h: n(1), CacheWriteTokens: n(10)}, "mixed"},
		{"parent_only_falls_back_to_request", CallMetrics{CacheWriteTokens: n(10)}, ""},
	} {
		if got := observedCacheTTL(tc.m); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
