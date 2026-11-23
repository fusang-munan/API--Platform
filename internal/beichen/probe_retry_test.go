package beichen

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/catalog"
)

func TestProbeFailureRetryable(t *testing.T) {
	cases := []struct {
		name        string
		observation probeObservation
		want        bool
	}{
		{name: "passed", observation: probeObservation{statusCode: 200}, want: false},
		{name: "network", observation: probeObservation{errorCode: "network_error"}, want: true},
		{name: "server error", observation: probeObservation{statusCode: 503}, want: true},
		{name: "timeout", observation: probeObservation{statusCode: 408}, want: true},
		{name: "rate limited", observation: probeObservation{statusCode: 429}, want: true},
		{name: "transport timeout", observation: probeObservation{errorCode: "timeout"}, want: true},
		{name: "response body timeout", observation: probeObservation{statusCode: 200, errorCode: "timeout"}, want: true},
		{name: "no status", observation: probeObservation{}, want: false},
		{name: "invalid response", observation: probeObservation{statusCode: 200, errorCode: "invalid_response"}, want: false},
		{name: "unauthorized", observation: probeObservation{statusCode: 401}, want: false},
		{name: "unauthorized with a transport error", observation: probeObservation{statusCode: 401, errorCode: "network_error"}, want: false},
		{name: "bad request", observation: probeObservation{statusCode: 400}, want: false},
		{name: "unreadable key", observation: probeObservation{errorCode: "credential_unreadable"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeFailureRetryable(tc.observation); got != tc.want {
				t.Fatalf("retryable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRetryFailedProbesStopsAfterSuccessOrPermanentFailure(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	client := &catalog.RouteProbe{BaseURL: origin, Credential: "test-key", Client: &http.Client{
		Transport: panelTransport(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			model := "persistent"
			if strings.Contains(string(body), "recovering") {
				model = "recovering"
			}
			if strings.Contains(string(body), "forbidden") {
				model = "forbidden"
			}
			mu.Lock()
			calls[model]++
			mu.Unlock()
			status := http.StatusServiceUnavailable
			if model == "recovering" {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"choices":[{}]}`))}, nil
		}),
	}}
	observations := []probeObservation{
		{target: probeTarget{client: client, model: "recovering", protocol: "openai_compatible"}, statusCode: 503},
		{target: probeTarget{client: client, model: "persistent", protocol: "openai_compatible"}, statusCode: 429},
		{target: probeTarget{client: client, model: "forbidden", protocol: "openai_compatible"}, statusCode: 401},
	}
	run := func(ctx context.Context, targets []probeTarget) ([]probeObservation, error) {
		var results []probeObservation
		for _, target := range targets {
			results = append(results, observeTarget(ctx, target))
		}
		return results, nil
	}
	if err := retryFailedProbes(context.Background(), observations, 0, run); err != nil {
		t.Fatal(err)
	}
	if calls["recovering"] != 1 || calls["persistent"] != 2 || calls["forbidden"] != 0 {
		t.Fatalf("unexpected retries: %v", calls)
	}
	if observations[0].statusCode != 200 || observations[0].errorCode != "" ||
		observations[1].statusCode != 503 || observations[2].statusCode != 401 {
		t.Fatalf("unexpected final observations: %+v", observations)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := retryFailedProbes(ctx, observations, time.Hour, run); err != context.Canceled {
		t.Fatalf("canceled retry: %v", err)
	}
	if calls["persistent"] != 2 {
		t.Fatal("canceled context made another request")
	}
}
