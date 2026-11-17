package dataplane

import "testing"

func TestUpstreamHTTPRetryable(t *testing.T) {
	t.Parallel()
	for _, status := range []int{429, 500, 502, 503, 504} {
		if !upstreamHTTPRetryable(status) {
			t.Fatalf("expected %d to be retryable", status)
		}
	}
	for _, status := range []int{200, 400, 401, 404, 408} {
		if upstreamHTTPRetryable(status) {
			t.Fatalf("expected %d not to be retryable", status)
		}
	}
}
