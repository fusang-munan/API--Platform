package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const certificateNoticePath = "/.well-known/nebula-maintenance/certificate-notice"

type certificateNoticeRequest struct {
	EmailDigest string `json:"email_digest"`
	Nonce       string `json:"request_nonce"`
}

func newCertificateNotice(parent context.Context, release, origin, grant, expiry string,
	send func(context.Context, []byte) error,
) (http.Handler, func()) {
	approved, err := hex.DecodeString(grant)
	until, timeErr := time.Parse(time.RFC3339, expiry)
	validFor := time.Until(until)
	if err != nil || len(approved) != sha256.Size || timeErr != nil || release == "" ||
		validFor <= 0 || validFor > 24*time.Hour {
		return nil, func() {}
	}

	ctx, cancel := context.WithDeadline(parent, until)
	inbox := make(chan []byte, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case digest := <-inbox:
			if ctx.Err() != nil {
				return
			}
			delivery, finish := context.WithTimeout(ctx, 20*time.Second)
			defer finish()
			if err := send(delivery, digest); err != nil {
				slog.Warn("certificate notice delivery unavailable")
			}
		}
	}()

	var submitOnce sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || ctx.Err() != nil {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if browserOrigin := r.Header.Get("Origin"); browserOrigin != "" && browserOrigin != origin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.ForceQuery || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err == nil {
			defer controller.SetReadDeadline(time.Time{})
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
		decoder.DisallowUnknownFields()
		var req certificateNoticeRequest
		if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF ||
			len(req.EmailDigest) != 64 || len(req.Nonce) != 64 ||
			req.EmailDigest != strings.ToLower(req.EmailDigest) || req.Nonce != strings.ToLower(req.Nonce) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		digest, err := hex.DecodeString(req.EmailDigest)
		_, nonceErr := hex.DecodeString(req.Nonce)
		if err != nil || nonceErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		proof := sha256.Sum256([]byte("nebula-notice-request-v1:" + release + ":" + req.EmailDigest + ":" + req.Nonce))
		if subtle.ConstantTimeCompare(approved, proof[:]) == 1 {
			submitOnce.Do(func() { inbox <- digest })
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return handler, func() { cancel(); <-done }
}
