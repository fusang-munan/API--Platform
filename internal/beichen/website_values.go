package beichen

import (
	"bytes"
	"encoding/json"
	"strings"
)

func websiteMissing(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return true
	}
	var value string
	return raw[0] == '"' && json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) == ""
}

// Keep unknown fields for the existing price eligibility checks. Only the
// documented numeric fields and interval bounds accept a blank as missing.
func normalizeWebsitePricing(raw json.RawMessage) json.RawMessage {
	if websiteMissing(raw) {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw
	}
	normalizeWebsiteRates(fields)
	var intervals []map[string]json.RawMessage
	if json.Unmarshal(fields["intervals"], &intervals) == nil && intervals != nil {
		for _, interval := range intervals {
			normalizeWebsiteRates(interval)
		}
		fields["intervals"], _ = json.Marshal(intervals)
	} else if value, ok := fields["intervals"]; ok && websiteMissing(value) {
		fields["intervals"] = json.RawMessage("null")
	}
	body, _ := json.Marshal(fields)
	return body
}

func normalizeWebsiteRates(fields map[string]json.RawMessage) {
	for name, raw := range fields {
		if (panelAmountField.MatchString(name) || name == "min_tokens" || name == "max_tokens") && websiteMissing(raw) {
			fields[name] = json.RawMessage("null")
		}
	}
}

func directoryModelGroups(section directoryPlatform, model directoryModel) []directoryGroup {
	groups := make([]directoryGroup, 0, len(section.Groups))
	for _, group := range section.Groups {
		if (model.GroupID == 0 && !group.OwnModels) || (model.GroupID != 0 && model.GroupID == group.ID) {
			groups = append(groups, group)
		}
	}
	return groups
}
