package catalog

import (
	"testing"
	"time"

	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
)

func TestSummarizeModelAvailabilityKeepsRecentlyProvenModelDegraded(t *testing.T) {
	now := time.Now()
	recent := now.Add(-2 * time.Hour)
	view := SummarizeModelAvailability("claude-opus-5-5", entmodel.StatusActive, []ProtocolCapability{{
		Protocol: "anthropic", BindingReady: true, ProbeState: probeFailed,
		Probe: &ProbeEvidence{CheckedAt: now, LastPassedAt: &recent, StatusCode: 503, ErrorCode: "upstream_error"},
	}})
	if view.Availability != AvailabilityDegraded {
		t.Fatalf("availability = %q, want %q", view.Availability, AvailabilityDegraded)
	}
	if view.AvailabilityReason == "" {
		t.Fatal("degraded availability needs a reason")
	}
}

func TestSummarizeModelAvailabilityMaintenanceWithoutRecentPass(t *testing.T) {
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour)
	future := now.Add(24 * time.Hour)
	for name, probe := range map[string]*ProbeEvidence{
		"old pass":       {CheckedAt: now, LastPassedAt: &old, StatusCode: 503, ErrorCode: "upstream_error"},
		"never passed":   {CheckedAt: now, StatusCode: 503, ErrorCode: "upstream_error"},
		"future pass":    {CheckedAt: now, LastPassedAt: &future, StatusCode: 503, ErrorCode: "http_error"},
		"unreadable key": {CheckedAt: now, LastPassedAt: &now, ErrorCode: "credential_unreadable"},
	} {
		t.Run(name, func(t *testing.T) {
			view := SummarizeModelAvailability("claude-opus-5-5", entmodel.StatusActive, []ProtocolCapability{{
				Protocol: "anthropic", BindingReady: true, ProbeState: probeFailed, Probe: probe,
			}})
			if view.Availability != AvailabilityMaintenance {
				t.Fatalf("availability = %q, want %q", view.Availability, AvailabilityMaintenance)
			}
		})
	}
}

func TestRecentlyProvenAvailableWindowAndBinding(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 54, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	pass := now.UTC().Add(-stickyAvailabilityWindow)
	route := ProtocolCapability{BindingReady: true, Probe: &ProbeEvidence{LastPassedAt: &pass}}
	if !recentlyProvenAvailable([]ProtocolCapability{route}, now) {
		t.Fatal("a pass exactly seven days ago should qualify across time zones")
	}
	if recentlyProvenAvailable([]ProtocolCapability{route}, now.Add(time.Nanosecond)) {
		t.Fatal("a pass more than seven days ago should expire")
	}
	route.BindingReady = false
	if recentlyProvenAvailable([]ProtocolCapability{route}, now) {
		t.Fatal("an unbound protocol should not keep the model available")
	}
}
