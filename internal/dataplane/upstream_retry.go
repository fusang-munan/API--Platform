package dataplane

import (
	"context"
	"net/http"
	"time"
)

const (
	upstreamRetryAttempts  = 3
	upstreamRetryBaseDelay = 400 * time.Millisecond
)

func upstreamHTTPRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func sleepUpstreamRetry(ctx context.Context, attempt int) bool {
	if attempt <= 0 {
		return true
	}
	delay := upstreamRetryBaseDelay * time.Duration(attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
