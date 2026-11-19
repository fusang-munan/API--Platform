package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

func TestSchoolRecipientOnlyPublishesPublicIdentity(t *testing.T) {
	id, err := schoolseal.LoadOrCreate(t.TempDir() + "/identity")
	if err != nil {
		t.Fatal(err)
	}
	handler := recipientHandler(id.Recipient(), http.NotFoundHandler())
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", schoolseal.PublicPath, http.StatusOK},
		{"HEAD", schoolseal.PublicPath, http.StatusOK},
		{"POST", schoolseal.PublicPath, http.StatusMethodNotAllowed},
		{"GET", schoolseal.PublicPath + "/recipient.key", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s returned %d", tc.method, tc.path, w.Code)
		}
		if tc.status != http.StatusOK {
			continue
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal("recipient response headers are incomplete")
		}
		if tc.method == "HEAD" {
			if w.Body.Len() != 0 {
				t.Fatal("HEAD returned a response body")
			}
			continue
		}
		var fields map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || len(fields) != 2 ||
			fields["algorithm"] != schoolseal.Algorithm || fields["public_key"] != id.Recipient().PublicKey {
			t.Fatal("recipient response must contain only the algorithm and persistent public key")
		}
	}
}
