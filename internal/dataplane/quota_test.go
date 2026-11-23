package dataplane

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func TestOutputBudgetTokens(t *testing.T) {
	for _, tt := range []struct {
		name      string
		milli     int64
		price     string
		requested json.RawMessage
		want      int64
	}{
		{name: "one credit at eight yuan", milli: 1000, price: "8", want: 1250},
		{name: "no remaining milli-credits", price: "8", want: 1},
		{name: "less than one token", milli: 1, price: "80", want: 1},
		{name: "floor fractional tokens", milli: 1000, price: "8.01", want: 1248},
		{name: "client asked for less", milli: 1000, price: "8", requested: json.RawMessage("37"), want: 37},
		{name: "client asked for more", milli: 1000, price: "8", requested: json.RawMessage("2000"), want: 1250},
		{name: "client set zero", milli: 1000, price: "8", requested: json.RawMessage("0"), want: 1},
		{name: "unknown price", milli: 1000, requested: json.RawMessage("37")},
		{name: "free output", milli: 1000, price: "0"},
		{name: "integer upper bound", milli: 1000, price: "0.000000000000000001", want: math.MaxInt64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := make(map[string]json.RawMessage)
			if tt.requested != nil {
				payload["max_tokens"] = tt.requested
			}
			got := applyOutputBudget(payload, "max_tokens", outputBudgetTokens(tt.milli, tt.price))
			if got != tt.want {
				t.Fatalf("budget=%d, want %d", got, tt.want)
			}
			if got == 0 && !reflect.DeepEqual(payload["max_tokens"], tt.requested) {
				t.Fatalf("unknown price changed the request limit: %s", payload["max_tokens"])
			}
		})
	}
}

func TestQuotaBudgetJSONRewrite(t *testing.T) {
	for _, tt := range []struct {
		path    string
		adapter modelbinding.Adapter
		baseURL string
		body    string
		field   string
		want    string
	}{
		{"/v1/chat/completions", modelbinding.AdapterOpenaiCompatible, "https://api.beichenapi.com",
			`{"model":"m","messages":[{"role":"user","content":"你好","meta":{"integer":9007199254740993}}],"max_tokens":2000,"stream":false}`,
			"max_tokens", "1250"},
		{"/v1/responses", modelbinding.AdapterOpenaiResponses, "https://api.beichenapi.com",
			`{"model":"m","input":"你好","metadata":{"user":"u"},"reasoning":{"effort":"low"}}`,
			"max_output_tokens", "1250"},
		{"/v1/messages", modelbinding.AdapterAnthropic, "https://api.anthropic.com",
			`{"model":"m","messages":[{"role":"user","content":"你好"}],"max_tokens":321,"system":"instructions"}`,
			"max_tokens", "321"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			replay, err := captureBody(io.NopCloser(strings.NewReader(tt.body)), 4096)
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close()
			body, _, _, cleanup, err := rewriteUpstreamBody("application/json", replay, "m", tt.adapter, tt.baseURL, tt.path,
				func(payload map[string]json.RawMessage) error {
					applyOutputBudget(payload, quotaTokenField(tt.path), 1250)
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			defer body.Close()
			got := make(map[string]json.RawMessage)
			if err := json.NewDecoder(body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			want := jsonObject([]byte(tt.body))
			want[tt.field] = json.RawMessage(tt.want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("only %s should change: got %v, want %v", tt.field, got, want)
			}
		})
	}
	// A legacy route must not look up a Beichen price.
	if budget := (&Gateway{}).quotaTokenLimit(context.Background(), usage.Reservation{}, usage.CallRoute{}, "max_tokens"); budget != 0 {
		t.Fatalf("legacy route got a quota budget of %d", budget)
	}
}

func TestQuotaRateLimitCodes(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		for _, code := range []string{usage.StudentConcurrencyExceeded, usage.PendingReviewLimit, usage.UnresolvedExposureLimit} {
			w := httptest.NewRecorder()
			writeModelCallRejection(w, path, &usage.QuotaError{Code: code})
			if w.Code != http.StatusTooManyRequests || w.Header().Get("X-Nebula-Error-Code") != code {
				t.Fatalf("%s: quota HTTP error=%d %v", path, w.Code, w.Header())
			}
			if path == "/v1/chat/completions" {
				var body struct {
					Error struct{ Code string }
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != code {
					t.Fatalf("quota response body=%s, want code %s: %v", w.Body, code, err)
				}
			}
		}
	}
}
