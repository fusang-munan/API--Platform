package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
	route := ProtocolCapability{BindingReady: true, ProbeState: probeFailed,
		Probe: &ProbeEvidence{StatusCode: 503, ErrorCode: "http_error", LastPassedAt: &pass}}
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

func TestStudentAvailabilityClassifiesHistoricalProbeFailures(t *testing.T) {
	now := time.Now()
	pass := now.Add(-time.Hour)
	for _, tc := range []struct {
		name         string
		status       int
		code         string
		availability string
		studentState string
		reason       string
	}{
		{"legacy upstream failure", 503, "http_error", AvailabilityDegraded, AvailabilityDegraded, "HTTP 503"},
		{"legacy rate limit", 429, "http_error", AvailabilityDegraded, AvailabilityDegraded, "暂时限流"},
		{"legacy HTTP timeout", 408, "http_error", AvailabilityDegraded, AvailabilityDegraded, "HTTP 408"},
		{"transport timeout", 0, "timeout", AvailabilityDegraded, AvailabilityDegraded, "请求超时"},
		{"legacy credential failure", 401, "http_error", AvailabilityMaintenance, "probe_failed", "未通过上游认证"},
		{"legacy forbidden", 403, "http_error", AvailabilityMaintenance, "probe_failed", "未通过上游认证"},
		{"confirmed group denial", 403, "GROUP_NOT_ALLOWED", AvailabilityMaintenance, "probe_failed", "无分组权限"},
		{"legacy unsupported endpoint", 404, "http_error", AvailabilityMaintenance, "probe_failed", "未提供该协议端点"},
		{"bad request", 400, "http_error", AvailabilityMaintenance, "probe_failed", "HTTP 400"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound := protocolCapability("openai_responses", "upstream", true,
				&ProbeEvidence{CheckedAt: now, LastPassedAt: &pass, StatusCode: tc.status, ErrorCode: tc.code})
			view := SummarizeModelAvailability("school", entmodel.StatusActive, []ProtocolCapability{bound})
			view.SetStudentState("school", entmodel.StatusActive, nil)
			if view.Availability != tc.availability || view.AvailabilityState != tc.studentState ||
				!strings.Contains(view.AvailabilityReason, tc.reason) {
				t.Fatalf("want %s/%s with %q, got %+v", tc.availability, tc.studentState, tc.reason, view)
			}
		})
	}
}

func TestStudentAvailabilityRemovesConfirmedRestrictedRoute(t *testing.T) {
	key := uuid.New()
	passed := &ProbeEvidence{CheckedAt: time.Now(), StatusCode: 200}
	blocked := protocolCapability("openai_responses", "upstream", true, passed)
	blocked.credentialID = key
	other := protocolCapability("openai_compatible", "upstream", true, passed)
	other.credentialID = uuid.New()
	channels := []StudentChannelCatalog{
		{CredentialID: key, UpstreamModel: "upstream", Current: true},
		{CredentialID: other.credentialID, UpstreamModel: "upstream", Current: true, Listed: true},
	}
	view := SummarizeModelAvailability("school", entmodel.StatusActive, []ProtocolCapability{blocked})
	view.SetStudentState("school", entmodel.StatusActive, channels[:1])
	if view.Availability != AvailabilityMaintenance || view.AvailabilityState != "channel_unavailable" ||
		view.RecommendedProtocol != nil || !strings.Contains(view.AvailabilityReason, "分组权限") {
		t.Fatalf("a prior pass must not keep a confirmed restricted route available: %+v", view)
	}
	view = SummarizeModelAvailability("school", entmodel.StatusActive, []ProtocolCapability{blocked, other})
	view.SetStudentState("school", entmodel.StatusActive, channels)
	if view.Availability != AvailabilityAvailable || len(view.AvailableProtocols) != 1 ||
		view.AvailableProtocols[0] != "openai_compatible" {
		t.Fatalf("a confirmed restriction should keep other allowed protocols: %+v", view)
	}
}
