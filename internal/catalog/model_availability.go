package catalog

import (
	"fmt"
	"net/http"
	"slices"
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
	case len(view.AvailableProtocols) == 0 && recentlyProvenAvailable(capabilities, time.Now()):
		view.Availability = AvailabilityDegraded
		view.AvailabilityReason = "过去 7 天内曾探活通过。仍可尝试调用，可能请求失败。" +
			probeAvailabilityReasons(capabilities, true)
	case view.ProbeState == probeFailed:
		view.Availability = AvailabilityMaintenance
		view.AvailabilityReason = "维护中。已配置线路最近一次连通测试均失败。" +
			probeAvailabilityReasons(capabilities, false)
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

// SetStudentState adds a student-facing explanation and excludes routes with a
// confirmed directory restriction from its published protocol summary.
func (view *ModelAvailability) SetStudentState(modelID string, status entmodel.Status, channels []StudentChannelCatalog) {
	view.AvailabilityState, view.AvailabilityReason = view.studentState(modelID, status, channels)
}

func (view *ModelAvailability) studentState(modelID string, status entmodel.Status, channels []StudentChannelCatalog) (string, string) {
	switch status {
	case entmodel.StatusInactive:
		return "disabled", "老师已停用该模型。"
	case entmodel.StatusPendingConfiguration:
		return "not_published", "老师尚未开放该模型。"
	}

	routed := *view
	if len(channels) > 0 {
		currentRoutes := make([]ProtocolCapability, 0, len(view.routes))
		eligibleRoutes := make([]ProtocolCapability, 0, len(view.routes))
		for _, route := range view.routes {
			current, restricted := studentRouteCatalogState(route, channels)
			if current {
				currentRoutes = append(currentRoutes, route)
			}
			if !restricted {
				eligibleRoutes = append(eligibleRoutes, route)
			}
		}
		if len(eligibleRoutes) != len(view.routes) {
			*view = SummarizeModelAvailability(modelID, status, eligibleRoutes)
		}
		routed = *view
		if len(currentRoutes) != len(eligibleRoutes) {
			routed = SummarizeModelAvailability(modelID, status, currentRoutes)
		}
	}
	switch routed.Availability {
	case AvailabilityAvailable, AvailabilityDegraded, AvailabilityUnverified:
		return routed.Availability, routed.AvailabilityReason
	}
	if routed.ProbeState == probeFailed {
		return "probe_failed", routed.AvailabilityReason
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
			return "channel_unavailable", "渠道目录未列出该模型，或渠道分组权限已失效。"
		}
		return AvailabilityUnverified, "渠道目录读取失败、过期或尚未扫描，请等待重新核验。"
	}
	return AvailabilityMaintenance, "当前没有可用线路，请等待老师恢复。"
}

func studentRouteCatalogState(route ProtocolCapability, channels []StudentChannelCatalog) (current, restricted bool) {
	for _, channel := range channels {
		if channel.CredentialID == route.credentialID && strings.EqualFold(channel.UpstreamModel, route.upstream) {
			return channel.Current && channel.Listed, channel.Current && !channel.Listed
		}
	}
	// Other suppliers do not use the Beichen scheduled directory snapshots.
	return true, false
}

func recentlyProvenAvailable(capabilities []ProtocolCapability, now time.Time) bool {
	for _, option := range capabilities {
		if !option.BindingReady || option.ProbeState != probeFailed || option.Probe == nil || option.Probe.LastPassedAt == nil {
			continue
		}
		temporary, _ := availabilityProbeFailure(*option.Probe)
		age := now.Sub(*option.Probe.LastPassedAt)
		if temporary && age >= 0 && age <= stickyAvailabilityWindow {
			return true
		}
	}
	return false
}

func probeAvailabilityReasons(routes []ProtocolCapability, temporaryOnly bool) string {
	var reasons []string
	for _, route := range routes {
		if !route.BindingReady || route.ProbeState != probeFailed || route.Probe == nil {
			continue
		}
		temporary, reason := availabilityProbeFailure(*route.Probe)
		if (!temporaryOnly || temporary) && !slices.Contains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		return ""
	}
	slices.Sort(reasons)
	return strings.Join(reasons, "、") + "。"
}

func availabilityProbeFailure(probe ProbeEvidence) (temporary bool, reason string) {
	code, status := probe.ErrorCode, probe.StatusCode
	switch {
	case code == "GROUP_NOT_ALLOWED":
		return false, "渠道 Key 无分组权限"
	case code == "credential_unreadable":
		return false, "学校服务无法读取渠道 Key"
	case code == "credential_invalid" || status == http.StatusUnauthorized || status == http.StatusForbidden:
		return false, "渠道 Key 未通过上游认证"
	case code == "endpoint_unsupported" || status == http.StatusNotFound:
		return false, "上游未提供该协议端点（HTTP 404）"
	case status == http.StatusRequestTimeout:
		return true, "上游请求超时（HTTP 408）"
	case status == http.StatusTooManyRequests:
		return true, "上游暂时限流（HTTP 429）"
	case status >= 500 && status < 600:
		return true, fmt.Sprintf("上游服务暂时不可用（HTTP %d）", status)
	case status >= 300:
		return false, fmt.Sprintf("上游返回 HTTP %d", status)
	case code == "timeout":
		return true, "上游请求超时"
	case code == "network_error":
		return true, "网络请求失败"
	case code == "invalid_response":
		return false, "上游响应与协议格式不符"
	default:
		return false, "当前线路未通过连通测试"
	}
}
