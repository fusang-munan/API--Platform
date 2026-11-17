package dataplane

import (
	"bufio"
	"bytes"
	"io"
)

const ssePeekLimit = 64 << 10

func peekSSEBody(body io.ReadCloser, limit int) (prefix []byte, remainder io.ReadCloser, err error) {
	if limit <= 0 {
		limit = ssePeekLimit
	}
	reader := bufio.NewReader(body)
	buffer := bytes.NewBuffer(make([]byte, 0, min(limit, 4096)))
	for buffer.Len() < limit {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			buffer.Write(line)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			_ = body.Close()
			return buffer.Bytes(), nil, readErr
		}
	}
	return buffer.Bytes(), struct {
		io.Reader
		io.Closer
	}{Reader: reader, Closer: body}, nil
}

func sseStreamRetryable(prefix []byte) bool {
	if len(prefix) == 0 {
		return false
	}
	lowered := bytes.ToLower(prefix)
	if sseStreamHasCommittedOutput(lowered) {
		return false
	}
	retryTokens := [][]byte{
		[]byte("server_is_overloaded"),
		[]byte("overloaded_error"),
		[]byte("service_unavailable"),
		[]byte("servers are currently overloaded"),
		[]byte("upstream_unavailable"),
		[]byte("\"type\":\"error\""),
		[]byte("\"type\": \"error\""),
	}
	for _, token := range retryTokens {
		if bytes.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func sseStreamHasCommittedOutput(lowered []byte) bool {
	successTokens := [][]byte{
		[]byte("response.output_text.delta"),
		[]byte("response.output_item.done"),
		[]byte("response.completed"),
		[]byte("\"status\":\"completed\""),
		[]byte("\"status\": \"completed\""),
	}
	for _, token := range successTokens {
		if bytes.Contains(lowered, token) {
			return true
		}
	}
	return false
}

func sseRetryReason(prefix []byte) string {
	lowered := bytes.ToLower(prefix)
	switch {
	case bytes.Contains(lowered, []byte("server_is_overloaded")):
		return "server_is_overloaded"
	case bytes.Contains(lowered, []byte("overloaded_error")):
		return "overloaded_error"
	case bytes.Contains(lowered, []byte("servers are currently overloaded")):
		return "upstream_overloaded"
	default:
		return "retryable_sse_error"
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
