package main

import (
	"testing"
	"time"
)

func TestLoginEmailReadsJsonAndFormBodies(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{`{"email":"school@example.test","password":"discarded"}`, "school@example.test"},
		{`email=school%40example.test&password=discarded`, "school@example.test"},
		{`{"username":"school@example.test","password":"discarded"}`, "school@example.test"},
		{`{"password":"discarded"}`, ""},
	} {
		if got := loginEmail(tc.body); got != tc.want {
			t.Fatalf("loginEmail(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestTokenExpiryFormats(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		want time.Time
	}{
		{"2027-01-01T00:00:00Z", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"1798761600000", time.UnixMilli(1798761600000)},
		{"1798761600", time.Unix(1798761600, 0)},
	} {
		got, err := parseTokenExpiry(tc.raw, now)
		if err != nil || !got.Equal(tc.want) {
			t.Fatalf("parseTokenExpiry(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"", "3600", "tomorrow"} {
		if _, err := parseTokenExpiry(raw, now); err == nil {
			t.Fatalf("relative or invalid expiry %q accepted", raw)
		}
	}
}
