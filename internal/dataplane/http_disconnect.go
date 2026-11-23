package dataplane

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"
)

const beichenDisconnectGrace = 15 * time.Second

// Decide before Do: the client may leave before even the response headers.
func beichenNonStreamingRequest(r *http.Request, replay *replayBody) bool {
	accept, _, _ := mime.ParseMediaType(r.Header.Get("Accept"))
	if replay == nil || accept == "text/event-stream" ||
		strings.HasSuffix(r.URL.Path, ":streamGenerateContent") ||
		strings.EqualFold(r.URL.Query().Get("alt"), "sse") {
		return false
	}
	body, err := replay.Open()
	if err != nil {
		return false
	}
	defer body.Close()
	media, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch media {
	case "application/json":
		var flags struct {
			Stream       json.RawMessage `json:"stream"`
			StreamFormat string          `json:"stream_format"`
		}
		return json.NewDecoder(body).Decode(&flags) == nil && flags.StreamFormat != "sse" &&
			(flags.Stream == nil || string(flags.Stream) == "false")
	case "multipart/form-data":
		parts := multipart.NewReader(body, params["boundary"])
		for {
			part, err := parts.NextPart()
			if err != nil {
				return err == io.EOF
			}
			switch part.FormName() {
			case "stream":
				flag, err := io.ReadAll(io.LimitReader(part, 6))
				if err != nil || string(flag) != "false" {
					return false
				}
			case "stream_format":
				format, err := io.ReadAll(io.LimitReader(part, 4))
				if err != nil || string(format) == "sse" {
					return false
				}
			}
		}
	default:
		return false
	}
}

// Only the handler reads and settles. This watcher merely interrupts a
// blocked dial, header wait or body read, and exits when the attempt closes.
func beichenHTTPAttempt(request *http.Request, grace time.Duration) (*http.Request, func() (time.Time, bool), context.CancelFunc) {
	client := request.Context()
	attempt, cancel := context.WithCancel(context.WithoutCancel(client))
	request, observed := traceUpstreamWrite(request.WithContext(attempt), true)
	// A dial can finish after the client has gone but before the watcher
	// is scheduled. Check before the transport starts writing that request.
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
		if client.Err() != nil {
			cancel()
		}
	}}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	go func() {
		select {
		case <-attempt.Done():
			return
		case <-client.Done():
		}
		if sent, connected := observed(); connected || !sent.IsZero() {
			timer := time.NewTimer(grace)
			defer timer.Stop()
			select {
			case <-attempt.Done():
				return
			case <-timer.C:
			}
		}
		cancel()
	}()
	return request, observed, cancel
}
