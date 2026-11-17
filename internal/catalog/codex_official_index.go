package catalog

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

// Synced from openai/codex codex-rs/models-manager/models.json (Codex bundled catalog).
//
//go:embed data/codex_official_index.json
var codexOfficialIndexJSON []byte

type codexOfficialIndexEntry struct {
	Slug                     string                `json:"slug"`
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                `json:"default_reasoning_level,omitempty"`
}

var (
	codexOfficialIndexOnce sync.Once
	codexOfficialIndex     map[string]codexOfficialIndexEntry
)

func loadCodexOfficialIndex() map[string]codexOfficialIndexEntry {
	codexOfficialIndexOnce.Do(func() {
		codexOfficialIndex = map[string]codexOfficialIndexEntry{}
		_ = json.Unmarshal(codexOfficialIndexJSON, &codexOfficialIndex)
	})
	return codexOfficialIndex
}

// CodexSupportedReasoningEfforts returns effort ids from the official Codex catalog when known.
func CodexSupportedReasoningEfforts(modelID string) []string {
	levels := codexReasoningLevelsForModel(modelID)
	efforts := make([]string, 0, len(levels))
	for _, level := range levels {
		efforts = append(efforts, level.Effort)
	}
	return efforts
}

func codexReasoningLevelsForModel(modelID string) []codexReasoningLevel {
	key := strings.ToLower(strings.TrimSpace(modelID))
	if entry, ok := loadCodexOfficialIndex()[key]; ok && len(entry.SupportedReasoningLevels) > 0 {
		return entry.SupportedReasoningLevels
	}
	return codexReasoningLevels("low", "medium", "high")
}
