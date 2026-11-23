package dataplane

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

const maxSSEEventBytes = 64 << 10

// sseObserver keeps one line and one event. Overflow discards only the
// current event from observation. The proxy forwards all original bytes.
type sseObserver struct {
	adapter       modelbinding.Adapter
	beichen       bool
	expectedModel string
	modelReported bool
	review        string
	metrics       ResponseMetrics
	line          []byte
	data          []byte
	eventName     string
	skipLF        bool
	started       bool
	overflow      bool
	longLine      bool
	outputSeen    bool
	terminal      bool
	incomplete    bool
	failed        bool
	retryable     bool
}

func (s *sseObserver) billable() bool {
	return s.outputSeen || s.terminal
}

func (s *sseObserver) observe(chunk []byte) {
	for len(chunk) != 0 {
		if s.skipLF {
			s.skipLF = false
			if chunk[0] == '\n' {
				chunk = chunk[1:]
				continue
			}
		}
		end := bytes.IndexAny(chunk, "\r\n")
		if end < 0 {
			s.appendLine(chunk)
			return
		}
		s.appendLine(chunk[:end])
		s.endLine()
		s.skipLF = chunk[end] == '\r'
		chunk = chunk[end+1:]
	}
}

func (s *sseObserver) appendLine(part []byte) {
	if len(part) == 0 || s.longLine {
		return
	}
	if s.line == nil {
		s.line = make([]byte, 0, maxSSEEventBytes)
	}
	room := cap(s.line) - len(s.line)
	s.line = append(s.line, part[:min(room, len(part))]...)
	s.longLine = len(part) > room
}

func (s *sseObserver) endLine() {
	line := s.line
	s.line = s.line[:0]
	if !s.started {
		s.started = true
		line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
	}
	if s.longLine {
		s.longLine = false
		if bytes.HasPrefix(line, []byte("event:")) {
			s.eventName = ""
			s.overflow = true
		} else if bytes.HasPrefix(line, []byte("data:")) {
			s.overflow = true
		}
		return
	}
	if len(line) == 0 {
		s.endEvent()
		s.data = s.data[:0]
		s.eventName = ""
		s.overflow = false
		return
	}
	field, value, _ := bytes.Cut(line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch {
	case bytes.Equal(field, []byte("event")):
		s.eventName = ""
		switch string(value) {
		case "error", "response.failed", "response.completed", "response.done", "response.incomplete", "message_stop":
			s.eventName = string(value)
		}
	case bytes.Equal(field, []byte("data")) && !s.overflow:
		if len(value)+len(s.data)+1 > maxSSEEventBytes {
			s.overflow = true
			return
		}
		if s.data == nil {
			s.data = make([]byte, 0, maxSSEEventBytes)
		}
		s.data = append(s.data, value...)
		s.data = append(s.data, '\n')
	}
}

func (s *sseObserver) endEvent() {
	if s.beichen && s.terminal && s.adapter != modelbinding.AdapterGoogleGeminiV1beta {
		return
	}
	if s.overflow {
		s.metrics.UsageReason = "usage_observation_limit"
		if s.beichen {
			s.incomplete = true
			s.review = beichenUnsupportedPrice
		}
		// Trust a protocol terminal event name even if its JSON body exceeds the
		// observation limit. Its metrics remain unavailable.
		if s.eventName == "error" || s.eventName == "response.failed" {
			s.failed = true
			s.retryable = s.eventName == "error"
		}
		s.noteTerminal(s.eventName)
		return
	}
	data := bytes.TrimSpace(s.data)
	if len(data) == 0 {
		return
	}
	if bytes.Equal(data, []byte("[DONE]")) && s.eventName != "error" && s.eventName != "response.failed" {
		if s.adapter == modelbinding.AdapterOpenaiCompatible && !s.failed {
			s.terminal = true
		}
		return
	}
	payload := jsonObject(data)
	eventType := rawString(payload["type"])
	eventError := jsonObject(payload["error"])
	if s.eventName == "error" || s.eventName == "response.failed" || eventType == "error" || eventType == "response.failed" || eventError != nil {
		if !s.failed && s.adapter == modelbinding.AdapterOpenaiResponses &&
			(s.eventName == "response.failed" || eventType == "response.failed") {
			s.metrics.applyPayload(s.adapter, payload)
		}
		s.failed = true
		s.retryable = s.eventName == "error" || eventType == "error" ||
			retryableSSECode(rawString(eventError["code"])) || retryableSSECode(rawString(eventError["type"]))
		return
	}
	if payload == nil || eventType == "ping" || s.failed {
		if s.beichen && payload == nil && s.review == "" {
			s.review = beichenUnsupportedPrice
		}
		return
	}
	if s.beichen && s.review == "" {
		s.review = beichenPayloadReview(data, s.expectedModel, false)
		s.modelReported = s.modelReported || beichenModelReported(payload, s.expectedModel)
	}
	responsesDone := s.adapter == modelbinding.AdapterOpenaiResponses &&
		(responsesTerminalEvent(eventType) || responsesTerminalEvent(s.eventName))
	if s.beichen && responsesDone {
		response := jsonObject(payload["response"])
		status := rawString(response["status"])
		incompleteEvent := eventType == "response.incomplete" || s.eventName == "response.incomplete"
		limited := eventType != "response.completed" && s.eventName != "response.completed" &&
			beichenResponsesOutputLimited(response)
		s.incomplete = s.incomplete || ((incompleteEvent || (status != "" && status != "completed")) &&
			!limited)
	}
	if s.beichen && (responsesDone || (s.adapter == modelbinding.AdapterOpenaiCompatible && jsonObject(payload["usage"]) != nil)) {
		s.metrics.applyUsageSnapshot(s.adapter, payload)
	} else {
		s.metrics.applyPayload(s.adapter, payload)
	}
	s.noteTerminal(eventType)
	s.noteTerminal(s.eventName)
	if !s.billable() {
		s.outputSeen = sseHasOutput(s.adapter, eventType, payload)
	}
	if s.adapter == modelbinding.AdapterGoogleGeminiV1beta {
		var candidates []struct {
			FinishReason string `json:"finishReason"`
		}
		if json.Unmarshal(payload["candidates"], &candidates) == nil {
			for _, candidate := range candidates {
				s.terminal = s.terminal || (candidate.FinishReason != "" && candidate.FinishReason != "FINISH_REASON_UNSPECIFIED")
			}
		}
		promptFeedback := jsonObject(payload["promptFeedback"])
		s.terminal = s.terminal || strings.HasPrefix(rawString(promptFeedback["blockReason"]), "BLOCK_REASON_") &&
			rawString(promptFeedback["blockReason"]) != "BLOCK_REASON_UNSPECIFIED"
	}
}

func (s *sseObserver) noteTerminal(eventType string) {
	if s.failed {
		return
	}
	if !s.beichen && eventType == "response.incomplete" {
		s.incomplete = true
	}
	if (s.adapter == modelbinding.AdapterAnthropic && eventType == "message_stop") ||
		(s.adapter == modelbinding.AdapterOpenaiResponses && responsesTerminalEvent(eventType)) {
		s.terminal = true
	}
}

func responsesTerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return true
	}
	return false
}

func retryableSSECode(value string) bool {
	switch value {
	case "server_is_overloaded", "overloaded_error", "service_unavailable", "service_unavailable_error", "upstream_unavailable":
		return true
	default:
		return false
	}
}
