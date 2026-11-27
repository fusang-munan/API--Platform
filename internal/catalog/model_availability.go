package catalog

import (
	"strings"
	"time"

	"github.com/google/uuid"
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
	AvailabilityState  string     `json:"availability_state,omitempty"`
	AvailabilityReason string     `json:"availability_reason"`
	LastProbeAt        *time.Time `json:"last_probe_at"`
	routes             []ProtocolCapability
}

// SummarizeModelAvailability describes the current published routes using
// stored probe results. It does not reach out to an upstream provider.
func SummarizeModelAvailability(modelID string, status entmodel.Status, capabilities []ProtocolCapability) ModelAvailability {
	view := ModelAvailability{
		ProtocolSummary: SummarizeProtocols(modelID, capabilities),
		Availability:    AvailabilityAvailable,
		routes:          capabilities,
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

// StudentChannelCatalog is the stored Key directory evidence for a bound
// upstream model. Inactive bindings are included to explain older grants.
type StudentChannelCatalog struct {
	CredentialID  uuid.UUID
	UpstreamModel string
	Current       bool
	Listed        bool
}

// SetStudentState adds a student-facing explanation. The legacy availability
// and protocol fields keep the shared gateway semantics.
func (view *ModelAvailability) SetStudentState(modelID string, status entmodel.Status, channels []StudentChannelCatalog) {
	view.AvailabilityState, view.AvailabilityReason = view.studentState(modelID, status, channels)
}

func (view ModelAvailability) studentState(modelID string, status entmodel.Status, channels []StudentChannelCatalog) (string, string) {
	switch status {
	case entmodel.StatusInactive:
		return "disabled", "老师已停用该模型。"
	case entmodel.StatusPendingConfiguration:
		return "not_published", "老师尚未开放该模型。"
	}

	routed := view
	if len(channels) > 0 {
		currentRoutes := make([]ProtocolCapability, 0, len(view.routes))
		for _, route := range view.routes {
			if studentRouteCurrent(route, channels) {
				currentRoutes = append(currentRoutes, route)
			}
		}
		if len(currentRoutes) != len(view.routes) {
			routed = SummarizeModelAvailability(modelID, status, currentRoutes)
		}
	}
	switch routed.Availability {
	case AvailabilityAvailable, AvailabilityDegraded, AvailabilityUnverified:
		return routed.Availability, routed.AvailabilityReason
	}
	if routed.ProbeState == probeFailed {
		return "probe_failed", "已配置线路最近一次连通测试均失败，请等待老师恢复。"
	}

	if len(channels) > 0 {
		allCurrent := true
		for _, channel := range channels {
			if channel.Current && channel.Listed {
				return AvailabilityMaintenance, "渠道目录仍列出该模型，当前线路无法接通，请等待老师恢复。"
			}
			allCurrent = allCurrent && channel.Current
		}
		if allCurrent {
			return "channel_unavailable", "最近一次成功扫描的渠道目录未列出该模型。"
		}
		return AvailabilityUnverified, "渠道目录读取失败、过期或尚未扫描，请等待重新核验。"
	}
	return AvailabilityMaintenance, "当前没有可用线路，请等待老师恢复。"
}

func studentRouteCurrent(route ProtocolCapability, channels []StudentChannelCatalog) bool {
	for _, channel := range channels {
		if channel.CredentialID == route.credentialID && strings.EqualFold(channel.UpstreamModel, route.upstream) {
			return channel.Current && channel.Listed
		}
	}
	// Other suppliers do not use the Beichen scheduled directory snapshots.
	return true
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
