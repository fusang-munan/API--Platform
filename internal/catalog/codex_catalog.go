package catalog

import (
	"encoding/json"
	"strings"
)

type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexCatalogModel struct {
	Slug                       string                `json:"slug"`
	DisplayName                string                `json:"display_name"`
	Description                any                   `json:"description"`
	BaseInstructions           string                `json:"base_instructions"`
	SupportedReasoningLevels   []codexReasoningLevel `json:"supported_reasoning_levels"`
	DefaultReasoningLevel      string                `json:"default_reasoning_level,omitempty"`
	ShellType                  string                `json:"shell_type"`
	Visibility                 string                `json:"visibility"`
	SupportedInAPI             bool                  `json:"supported_in_api"`
	Priority                   int                   `json:"priority"`
	ApplyPatchToolType         string                `json:"apply_patch_tool_type"`
	TruncationPolicy           map[string]any        `json:"truncation_policy"`
	ContextWindow              int                   `json:"context_window"`
	MaxContextWindow           int                   `json:"max_context_window"`
	EffectiveContextWindowPercent int                `json:"effective_context_window_percent"`
	InputModalities            []string              `json:"input_modalities"`
	SupportsParallelToolCalls  bool                  `json:"supports_parallel_tool_calls"`
	ExperimentalSupportedTools []string              `json:"experimental_supported_tools"`
}

const defaultCodexBaseInstructions = "You are Codex, a coding assistant connected through the Nebula gateway. Help the user with software engineering tasks."

type codexCatalogDocument struct {
	Models []codexCatalogModel `json:"models"`
}

func codexDisplayName(modelID, displayName string) string {
	trimmed := strings.TrimSpace(displayName)
	if trimmed != "" && trimmed != modelID {
		return trimmed
	}
	return modelID
}

var codexReasoningDescriptions = map[string]string{
	"none":    "Fastest responses with no extended reasoning",
	"minimal": "Light reasoning with minimal latency overhead",
	"low":     "Fast responses with lighter reasoning",
	"medium":  "Balances speed and reasoning depth for everyday tasks",
	"high":    "Greater reasoning depth for complex problems",
	"xhigh":   "Extra high reasoning depth for complex problems",
	"max":     "Maximum reasoning depth for the hardest problems",
	"ultra":   "Maximum reasoning with automatic task delegation",
}

func codexReasoningLevels(efforts ...string) []codexReasoningLevel {
	levels := make([]codexReasoningLevel, 0, len(efforts))
	for _, effort := range efforts {
		description := codexReasoningDescriptions[effort]
		if description == "" {
			description = effort
		}
		levels = append(levels, codexReasoningLevel{Effort: effort, Description: description})
	}
	return levels
}

// BuildCodexModelCatalog builds Codex model_catalog_json entries for gateway models.
func BuildCodexModelCatalog(modelIDs []string, displayNames map[string]string) codexCatalogDocument {
	seen := map[string]struct{}{}
	models := make([]codexCatalogModel, 0, len(modelIDs))
	for index, rawID := range modelIDs {
		modelID := strings.TrimSpace(rawID)
		key := strings.ToLower(modelID)
		if modelID == "" || !IsOpenAIModelsListable(modelID) {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, codexCatalogModel{
			Slug:                          modelID,
			DisplayName:                   codexDisplayName(modelID, displayNames[modelID]),
			Description:                   nil,
			BaseInstructions:              defaultCodexBaseInstructions,
			SupportedReasoningLevels:      codexReasoningLevelsForModel(modelID),
			ShellType:                     "unified_exec",
			Visibility:                    "list",
			SupportedInAPI:                true,
			Priority:                      index + 1,
			ApplyPatchToolType:            "freeform",
			TruncationPolicy:              map[string]any{"mode": "tokens", "limit": 10000},
			ContextWindow:                 272000,
			MaxContextWindow:              272000,
			EffectiveContextWindowPercent: 95,
			InputModalities:               []string{"text"},
			SupportsParallelToolCalls:     true,
			ExperimentalSupportedTools:    []string{},
		})
	}
	return codexCatalogDocument{Models: models}
}

// MarshalCodexModelCatalog returns pretty JSON for Codex model_catalog_json files.
func MarshalCodexModelCatalog(modelIDs []string, displayNames map[string]string) ([]byte, error) {
	return json.MarshalIndent(BuildCodexModelCatalog(modelIDs, displayNames), "", "  ")
}
