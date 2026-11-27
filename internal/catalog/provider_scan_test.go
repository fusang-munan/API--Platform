package catalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type modelScanTransport func(*http.Request) (*http.Response, error)

func (f modelScanTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBeichenScanRobustnessPaginationAndBackoff(t *testing.T) {
	var paths []string
	client := &http.Client{Transport: modelScanTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Host != "api.beichenapi.com" || r.Header.Get("Authorization") != "Bearer sk-test-scan" {
			t.Fatal("request left the Key catalog origin")
		}
		resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
		var body string
		switch len(paths) {
		case 1:
			resp.StatusCode = http.StatusTooManyRequests
			resp.Header.Set("Retry-After", "0")
		case 2:
			body = `{"data":[{"id":"a"}],"has_more":true,"next_cursor":"c2","total":3}`
		case 3:
			return nil, context.DeadlineExceeded
		case 4:
			body = `{"data":[{"id":"b"}],"total":3}`
			resp.Header.Add("Link", `</v1/models?cursor=c3>; rel="next"`)
		case 5:
			body = `{"data":[{"id":"c"}],"has_more":false,"total":3}`
		default:
			t.Fatal("retry budget exceeded")
		}
		resp.Body = io.NopCloser(strings.NewReader(body))
		return resp, nil
	})}
	entries, err := FetchProviderModels(context.Background(), beichenOrigin, "sk-test-scan", client)
	want := []string{"/v1/models", "/v1/models", "/v1/models?cursor=c2", "/v1/models?cursor=c2", "/v1/models?cursor=c3"}
	if err != nil || !slices.Equal(paths, want) || len(entries) != 3 || entries[0].ID != "a" || entries[2].ID != "c" {
		t.Fatalf("incomplete result: pages=%v models=%d err=%v", paths, len(entries), err)
	}
}

func TestBeichenScanRobustnessRejectUnsafeContinuation(t *testing.T) {
	for _, continuation := range []string{
		`<https://not-beichen.invalid/v1/models>; rel="next"`,
		`</v1/models>; rel="next"`,
	} {
		reads := 0
		client := &http.Client{Transport: modelScanTransport(func(r *http.Request) (*http.Response, error) {
			reads++
			if reads > 1 {
				return nil, errors.New("unsafe continuation")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Link": {continuation}},
				Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"a"}]}`))}, nil
		})}
		entries, err := FetchProviderModels(context.Background(), beichenOrigin, "sk-test-scan", client)
		if err == nil || len(entries) != 0 || reads != 1 {
			t.Fatalf("continuation=%q reads=%d models=%d err=%v", continuation, reads, len(entries), err)
		}
	}
}

func TestBeichenScanRobustnessModelPageCountDrift(t *testing.T) {
	reads := 0
	client := &http.Client{Transport: modelScanTransport(func(r *http.Request) (*http.Response, error) {
		reads++
		body := `{"data":[{"id":"a"}],"pagination":{"page":1,"total_pages":3}}`
		if reads == 2 {
			body = `{"data":[{"id":"b"}],"pagination":{"page":2,"total_pages":2}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	entries, err := FetchProviderModels(context.Background(), beichenOrigin, "sk-test-scan", client)
	if reads != 2 || len(entries) != 0 || err == nil || !strings.Contains(err.Error(), "总页数") {
		t.Fatalf("changed page count was accepted: reads=%d entries=%v error=%v", reads, entries, err)
	}
}
