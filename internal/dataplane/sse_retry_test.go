package dataplane

import (
	"io"
	"strings"
	"testing"
)

func TestSSEStreamRetryableDetectsOverload(t *testing.T) {
	t.Parallel()
	prefix := []byte(`event: error
data: {"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}

`)
	if !sseStreamRetryable(prefix) {
		t.Fatal("expected overload SSE to be retryable")
	}
}

func TestSSEStreamRetryableSkipsCommittedOutput(t *testing.T) {
	t.Parallel()
	prefix := []byte(`event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hello"}

event: error
data: {"error":{"code":"server_is_overloaded"}}

`)
	if sseStreamRetryable(prefix) {
		t.Fatal("should not retry after output already started")
	}
}

func TestPeekSSEBodyPreservesRemainder(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader("line1\nline2\nline3\n"))
	prefix, remainder, err := peekSSEBody(body, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer remainder.Close()
	if string(prefix) != "line1\nline2\nline3\n" {
		t.Fatalf("unexpected prefix %q", prefix)
	}
	rest, err := io.ReadAll(remainder)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected empty remainder, got %q", rest)
	}
}
