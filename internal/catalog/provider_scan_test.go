package catalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestBeichenModelSnapshotRecognizesPageTokensAndIncompleteFirstPage(t *testing.T) {
	for _, tc := range []struct {
		name         string
		first, final string
		query        string
		complete     bool
	}{
		{"unmarked first page", `{"data":[{"id":"a"}]}`, "", "", false},
		{"explicit final page", `{"data":[{"id":"a"}],"hasMore":false}`, "", "", true},
		{"camel token", `{"data":{"items":[{"id":"a"}],"nextPageToken":"p +/"}}`,
			`{"models":[{"id":"b"}],"pagination":{"nextPageToken":null}}`, "pageToken", true},
		{"snake token", `{"data":[{"id":"a"}],"pagination":{"next_page_token":"p +/"}}`,
			`{"data":[{"id":"b"}]}`, "page_token", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads int
			client := &http.Client{Transport: modelScanTransport(func(req *http.Request) (*http.Response, error) {
				reads++
				body := tc.first
				if req.URL.RawQuery != "" {
					if req.URL.Query().Get(tc.query) != "p +/" {
						t.Fatalf("unexpected pagination query: %s", req.URL.RawQuery)
					}
					body = tc.final
				}
				if reads > 2 {
					t.Fatal("continuation repeated")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			snapshot, err := FetchBeichenModelSnapshot(context.Background(), "fixture", client)
			want := 1
			if tc.query != "" {
				want = 2
			}
			if err != nil || snapshot.Complete != tc.complete || len(snapshot.Entries) != want || reads != want {
				t.Fatalf("complete=%t entries=%d requests=%d error=%v", snapshot.Complete, len(snapshot.Entries), reads, err)
			}
		})
	}
}

func TestBeichenModelSnapshotDefersLongRetryAfterOnSecondPage(t *testing.T) {
	for _, header := range []string{"120", time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)} {
		reads := 0
		client := &http.Client{Transport: modelScanTransport(func(r *http.Request) (*http.Response, error) {
			reads++
			if r.URL.Query().Has("pageToken") {
				return &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody,
					Header: http.Header{"Retry-After": {header}}}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
				`{"data":[{"id":"partial"}],"nextPageToken":"second"}`))}, nil
		})}
		snapshot, err := FetchBeichenModelSnapshot(context.Background(), "fixture", client)
		if reads != 2 || snapshot.Complete || len(snapshot.Entries) != 0 ||
			!errors.Is(err, BeichenHTTPStatus(http.StatusTooManyRequests)) || time.Until(BeichenRetryAt(err)) < time.Minute {
			t.Fatalf("incomplete scan was not deferred: requests=%d complete=%t count=%d error=%v",
				reads, snapshot.Complete, len(snapshot.Entries), err)
		}
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
