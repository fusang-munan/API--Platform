package catalog

import (
	"time"

	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
)

const (
	AvailabilityAvailable   = "available"
	AvailabilityDegraded    = "degraded"
	AvailabilityUnverified  = "unverified"
	AvailabilityMaintenance = "maintenance"
)

// stickyAvailabilityWindow keeps a model listed after one failed sweep when it
// passed a probe recently. A brief upstream outage should not erase a model
// that has been usable; the UI marks it as risky instead.
const stickyAvailabilityWindow = 7 * 24 * time.Hour

type ModelAvailability struct {
	ProtocolSummary
	Availability       string     `json:"availability"`
	AvailabilityReason string     `json:"availability_reason"`
	LastProbeAt        *time.Time `json:"last_probe_at"`
}

// SummarizeModelAvailability describes the current published routes using
// stored probe results. It does not reach out to an upstream provider.
func SummarizeModelAvailability(modelID string, status entmodel.Status, capabilities []ProtocolCapability) ModelAvailability {
	view := ModelAvailability{
		ProtocolSummary: SummarizeProtocols(modelID, capabilities),
		Availability:    AvailabilityAvailable,
	}
	for _, option := range capabilities {
		if option.Probe != nil && (view.LastProbeAt == nil || option.Probe.CheckedAt.After(*view.LastProbeAt)) {
			checkedAt := option.Probe.CheckedAt.UTC()
			view.LastProbeAt = &checkedAt
		}
	}
	switch {
	case status != entmodel.StatusActive:
		view.Availability = AvailabilityMaintenance
		view.AvailabilityReason = "维护中。校内模型已停用或还未发布，请联系老师。"
	case view.ProbeState == probeFailed && recentlyProvenAvailable(capabilities, time.Now()):
		view.Availability = AvailabilityDegraded
		view.AvailabilityReason = "最近一次探活失败，过去 7 天内曾探活通过。仍可尝试调用，可能请求失败。"
	case view.ProbeState == probeFailed:
		view.Availability = AvailabilityMaintenance
		view.AvailabilityReason = "维护中。所有已配置线路最近一次探活均失败，请联系老师检查渠道 Key 后重新探活。"
	case len(view.Protocols) == 0:
		view.Availability = AvailabilityMaintenance
		view.AvailabilityReason = "维护中。当前没有可接入的线路，请联系老师核对渠道 Key 和模型绑定。"
	case len(view.AvailableProtocols) == 0:
		view.Availability = AvailabilityUnverified
		view.AvailabilityReason = "已有绑定线路，当前还没有通过探活的协议。"
	}
	return view
}

func recentlyProvenAvailable(capabilities []ProtocolCapability, now time.Time) bool {
	for _, option := range capabilities {
		if !option.BindingReady || option.Probe == nil || option.Probe.LastPassedAt == nil ||
			option.Probe.ErrorCode == "credential_unreadable" {
			continue
		}
		age := now.Sub(*option.Probe.LastPassedAt)
		if age >= 0 && age <= stickyAvailabilityWindow {
			return true
		}
	}
	return false
}
