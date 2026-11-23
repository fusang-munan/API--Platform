package catalog

import (
	"slices"
	"strings"
	"time"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

const (
	probeUnprobed = "unprobed"
	probePassed   = "passed"
	probeFailed   = "failed"
)

// ProbeEvidence is the latest stored probe for the current Key and protocol.
type ProbeEvidence struct {
	CheckedAt    time.Time  `json:"checked_at"`
	LastPassedAt *time.Time `json:"last_passed_at,omitempty"`
	StatusCode   int        `json:"status_code"`
	ErrorCode    string     `json:"error_code,omitempty"`
}

type ProtocolCapability struct {
	Protocol     string         `json:"protocol"`
	BindingReady bool           `json:"binding_ready"`
	ProbeState   string         `json:"probe_state"`
	Probe        *ProbeEvidence `json:"probe,omitempty"`
	upstream     string
}

func protocolCapability(protocol, upstream string, bound bool, probe *ProbeEvidence) ProtocolCapability {
	item := ProtocolCapability{Protocol: protocol, upstream: upstream, BindingReady: bound, ProbeState: probeUnprobed, Probe: probe}
	if probe != nil {
		item.ProbeState = probeFailed
		if probe.ErrorCode == "" && probe.StatusCode >= 200 && probe.StatusCode < 300 {
			item.ProbeState = probePassed
		}
	}
	return item
}

type ProtocolSummary struct {
	Protocols           []string `json:"protocols"`
	AvailableProtocols  []string `json:"available_protocols"`
	RecommendedProtocol *string  `json:"recommended_protocol"`
	ProbeState          string   `json:"probe_state"`
}

// SummarizeProtocols prefers a passing probe over a route without a probe.
// A failed protocol can only recover its rank with a current passing result
// on another eligible Key or by passing its next probe.
func SummarizeProtocols(model string, options []ProtocolCapability) ProtocolSummary {
	result := ProtocolSummary{
		Protocols: []string{}, AvailableProtocols: []string{}, ProbeState: "not_applicable",
	}
	type choice struct {
		evidence int
		family   int
	}
	best := choice{}
	evidence := make(map[string]int)
	for _, option := range options {
		rank := 0
		switch option.ProbeState {
		case probePassed:
			rank = 2
		case probeUnprobed:
			rank = 1
		}
		evidence[option.Protocol] = max(evidence[option.Protocol], rank)
		family := protocolFamilyRank(option.upstream, option.Protocol)
		if option.upstream == "" {
			family = protocolFamilyRank(model, option.Protocol)
		}
		if rank > 0 && (result.RecommendedProtocol == nil || rank > best.evidence ||
			rank == best.evidence && (family > best.family || family == best.family && option.Protocol < *result.RecommendedProtocol)) {
			best = choice{rank, family}
			protocol := option.Protocol
			result.RecommendedProtocol = &protocol
		}
	}
	for protocol, rank := range evidence {
		result.Protocols = append(result.Protocols, protocol)
		if rank == 2 {
			result.AvailableProtocols = append(result.AvailableProtocols, protocol)
		}
	}
	slices.Sort(result.Protocols)
	slices.Sort(result.AvailableProtocols)
	switch {
	case best.evidence == 2:
		result.ProbeState = probePassed
	case best.evidence == 1:
		result.ProbeState = probeUnprobed
	case len(result.Protocols) > 0:
		result.ProbeState = probeFailed
	}
	return result
}

func protocolFamilyRank(model, protocol string) int {
	name := NormalizeSignal(model)
	chat, responses := modelbinding.AdapterOpenaiCompatible, modelbinding.AdapterOpenaiResponses
	preferred := []modelbinding.Adapter{chat, responses}
	switch {
	case IsAnthropicFamilyModel(name):
		preferred = []modelbinding.Adapter{modelbinding.AdapterAnthropic, chat, responses}
	case strings.Contains(name, "gemini"):
		preferred = []modelbinding.Adapter{modelbinding.AdapterGoogleGeminiV1beta, chat, responses}
	}
	if index := slices.Index(preferred, modelbinding.Adapter(protocol)); index >= 0 {
		return 3 - index
	}
	return 0
}

type ProtocolEndpoint struct {
	Label string
	Path  string
}

func EndpointForProtocol(protocol string) ProtocolEndpoint {
	switch modelbinding.Adapter(protocol) {
	case modelbinding.AdapterOpenaiCompatible:
		return ProtocolEndpoint{"OpenAI Chat", "/v1/chat/completions"}
	case modelbinding.AdapterOpenaiResponses:
		return ProtocolEndpoint{"OpenAI Responses", "/v1/responses"}
	case modelbinding.AdapterAnthropic:
		return ProtocolEndpoint{"Anthropic Messages", "/v1/messages"}
	case modelbinding.AdapterGoogleGeminiV1beta:
		return ProtocolEndpoint{"Gemini 原生", "/v1beta/models/{model}:generateContent"}
	case modelbinding.AdapterOpenaiEmbeddings:
		return ProtocolEndpoint{"OpenAI Embeddings", "/v1/embeddings"}
	case modelbinding.AdapterOpenaiImages:
		return ProtocolEndpoint{"OpenAI Images", "/v1/images/generations"}
	case modelbinding.AdapterOpenaiAudio:
		return ProtocolEndpoint{"OpenAI Audio", "/v1/audio/speech"}
	case modelbinding.AdapterOpenaiVideo:
		return ProtocolEndpoint{"OpenAI Video", "/v1/videos"}
	case modelbinding.AdapterOpenaiRealtime:
		return ProtocolEndpoint{"OpenAI Realtime", "/v1/realtime"}
	case modelbinding.AdapterOpenaiModerations:
		return ProtocolEndpoint{"OpenAI Moderations", "/v1/moderations"}
	case modelbinding.AdapterCohereRerankV2:
		return ProtocolEndpoint{"Cohere Rerank", "/v2/rerank"}
	default:
		return ProtocolEndpoint{Label: protocol}
	}
}
