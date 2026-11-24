package dataplane

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

func reworkGateway(t *testing.T) pendingGateway {
	t.Helper()
	f := newPendingGateway(t)
	f.db.SetMaxOpenConns(10)
	t.Setenv("NEBULA_PENDING_AUTO_SETTLE", "0")
	t.Setenv("NEBULA_TERMINAL_PENDING_POLICY", "")
	t.Setenv("NEBULA_TERMINAL_PENDING_GRACE_SECONDS", "")
	t.Setenv("NEBULA_TERMINAL_PENDING_BATCH", "")
	t.Setenv("NEBULA_STUDENT_PENDING_REVIEW_LIMIT", "100")
	return f
}

func reworkStart(t *testing.T, f pendingGateway, id, model, counters string) {
	t.Helper()
	body := fmt.Sprintf(`{"type":"message_start","model":%q,"message":{"id":"msg_%s","stop_reason":null,"usage":{%s}}}`, model, id, counters)
	f.gateway.client.Transport = beichenGatewayTestTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if w := f.request(id); w.Code != 200 {
		t.Fatalf("%s upstream HTTP %d: %s", id, w.Code, w.Body)
	}
}

func reworkCount(t *testing.T, f pendingGateway, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func reworkRun(t *testing.T, f pendingGateway, limit, want int) {
	t.Helper()
	r, err := usage.ResolveStaleTerminalPending(f.ctx, f.db, limit)
	if err != nil || r.Resolved != want || r.Errors != 0 {
		t.Fatalf("resolve(%d)=%+v,%v want %d", limit, r, err, want)
	}
}
