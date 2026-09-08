package catalog

import (
	"strings"

	entmodel "github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/model"
)

type UpstreamMetadata struct {
	Category         *entmodel.Category
	Capabilities     []string
	InputModalities  []string
	OutputModalities []string
	ContextWindow    *int
	MaxInputTokens   *int
	MaxOutputTokens  *int
}

var (
	categoryFieldAliases     = []string{"category", "model_category", "model_type_category"}
	capabilityFieldAliases   = []string{"capabilities", "capability", "features", "skills"}
	inputModalityAliases     = []string{"input_modalities", "input_modality", "input_modes", "modalities_in"}
	outputModalityAliases    = []string{"output_modalities", "output_modality", "output_modes", "modalities_out"}
	contextWindowFieldAliases = []string{"context_window", "context_length", "max_context_tokens", "context_size", "max_context_length"}
	maxInputTokenAliases     = []string{"max_input_tokens", "input_token_limit", "max_prompt_tokens", "prompt_token_limit"}
	maxOutputTokenAliases    = []string{"max_output_tokens", "output_token_limit", "max_completion_tokens", "completion_token_limit"}
)

func ParseUpstreamMetadata(raw map[string]any) UpstreamMetadata {
	if raw == nil {
		return UpstreamMetadata{}
	}
	meta := UpstreamMetadata{}
	if category, ok := parseUpstreamCategory(raw); ok {
		meta.Category = &category
	}
	if values, ok := ResolveStringSlice(raw, capabilityFieldAliases...); ok {
		meta.Capabilities = cleanStringList(values)
	}
	if values, ok := ResolveStringSlice(raw, inputModalityAliases...); ok {
		meta.InputModalities = cleanStringList(values)
	}
	if values, ok := ResolveStringSlice(raw, outputModalityAliases...); ok {
		meta.OutputModalities = cleanStringList(values)
	}
	if value, ok := ResolvePositiveInt(raw, contextWindowFieldAliases...); ok {
		meta.ContextWindow = &value
	}
	if value, ok := ResolvePositiveInt(raw, maxInputTokenAliases...); ok {
		meta.MaxInputTokens = &value
	}
	if value, ok := ResolvePositiveInt(raw, maxOutputTokenAliases...); ok {
		meta.MaxOutputTokens = &value
	}
	return meta
}

func parseUpstreamCategory(raw map[string]any) (entmodel.Category, bool) {
	value, ok := ResolveString(raw, categoryFieldAliases...)
	if !ok {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(entmodel.CategoryText):
		return entmodel.CategoryText, true
	case string(entmodel.CategoryImage):
		return entmodel.CategoryImage, true
	case string(entmodel.CategoryAudio):
		return entmodel.CategoryAudio, true
	case string(entmodel.CategoryVideo):
		return entmodel.CategoryVideo, true
	case string(entmodel.CategoryMultimodal):
		return entmodel.CategoryMultimodal, true
	case string(entmodel.CategoryEmbedding):
		return entmodel.CategoryEmbedding, true
	case string(entmodel.CategoryRerank):
		return entmodel.CategoryRerank, true
	default:
		return "", false
	}
}

func ResolveString(raw map[string]any, aliases ...string) (string, bool) {
	value, ok := resolveValue(raw, aliases...)
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

func ResolveStringSlice(raw map[string]any, aliases ...string) ([]string, bool) {
	value, ok := resolveValue(raw, aliases...)
	if !ok {
		return nil, false
	}
	switch typed := value.(type) {
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, text)
			}
		}
		return result, len(result) > 0
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if strings.TrimSpace(item) != "" {
				result = append(result, item)
			}
		}
		return result, len(result) > 0
	case string:
		parts := strings.Split(typed, ",")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, part)
			}
		}
		if len(result) == 0 {
			for _, part := range strings.FieldsFunc(typed, func(r rune) bool { return r == ';' || r == '|' }) {
				part = strings.TrimSpace(part)
				if part != "" {
					result = append(result, part)
				}
			}
		}
		return result, len(result) > 0
	default:
		return nil, false
	}
}

func ResolvePositiveInt(raw map[string]any, aliases ...string) (int, bool) {
	value, ok := resolveValue(raw, aliases...)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		if typed <= 0 || typed > float64(^uint(0)>>1) {
			return 0, false
		}
		return int(typed), true
	case int:
		if typed <= 0 {
			return 0, false
		}
		return typed, true
	case int64:
		if typed <= 0 {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

func resolveValue(raw map[string]any, aliases ...string) (any, bool) {
	if raw == nil {
		return nil, false
	}
	aliasSet := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		aliasSet[strings.ToLower(strings.TrimSpace(alias))] = struct{}{}
	}
	for key, value := range raw {
		if _, ok := aliasSet[strings.ToLower(strings.TrimSpace(key))]; ok {
			return value, true
		}
	}
	return nil, false
}
