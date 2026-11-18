package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCertificateNoticeOnlyDeliversApprovedPublicNotice(t *testing.T) {
	const origin = "https://school.example"
	const release = "0.11.0"
	digest := sha256.Sum256([]byte("nebula-certificate-notice-v1:reader@example.test"))
	selector := hex.EncodeToString(digest[:])
	nonce := strings.Repeat("ab", 32)
	grant := sha256.Sum256([]byte("nebula-notice-request-v1:" + release + ":" + selector + ":" + nonce))
	delivered := make(chan []byte, 2)
	h, stop := newCertificateNotice(context.Background(), release, origin, hex.EncodeToString(grant[:]),
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339), func(_ context.Context, value []byte) error {
			delivered <- value
			return nil
		})
	defer stop()
	body := fmt.Sprintf(`{"email_digest":%q,"request_nonce":%q}`, selector, nonce)
	post := func(payload string, tlsEnabled bool) int {
		t.Helper()
		req := httptest.NewRequest("POST", origin+certificateNoticePath, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.TLS = nil
		if tlsEnabled {
			req.TLS = &tls.ConnectionState{}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusAccepted && w.Body.Len() != 0 {
			t.Fatal("unexpected receipt information")
		}
		return w.Code
	}
	if post(body, false) != 404 || post(body+"{}", true) != 400 || post(`{"email":"x@example.test"}`, true) != 400 {
		t.Fatal("unexpected handling of malformed or plaintext requests")
	}
	if post(strings.Replace(body, selector, strings.Repeat("fd", 32), 1), true) != 202 ||
		post(strings.Replace(body, nonce, strings.Repeat("fe", 32), 1), true) != 202 {
		t.Fatal("unapproved request disclosed a distinction")
	}
	if post(body, true) != 202 || post(body, true) != 202 {
		t.Fatal("expected generic acceptance")
	}
	select {
	case result := <-delivered:
		if !bytes.Equal(result, digest[:]) {
			t.Fatal("delivered to an unapproved target")
		}
	case <-time.After(time.Second):
		t.Fatal("missing approved delivery")
	}
	stop()
	if len(delivered) != 0 {
		t.Fatal("duplicate notification")
	}
}

func TestCertificateNoticeRequiresShortLivedReleaseGrant(t *testing.T) {
	for _, expiry := range []time.Time{time.Now().Add(-time.Minute), time.Now().Add(48 * time.Hour)} {
		h, stop := newCertificateNotice(context.Background(), "0.11.0", "https://school.example", strings.Repeat("bb", 32), expiry.Format(time.RFC3339), nil)
		stop()
		if h != nil {
			t.Fatal("invalid expiry enabled certificate mail")
		}
	}
}
