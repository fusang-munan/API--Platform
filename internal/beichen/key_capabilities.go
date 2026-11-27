package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const keyUsageFreshness = 30 * time.Minute

type KeyCapability struct {
	CredentialRef
	WebsiteKeyID     *int64           `json:"website_key_id"`
	WebsiteGroup     *WebsiteGroup    `json:"website_group"`
	GroupStatus      string           `json:"group_status"`
	Models           []string         `json:"models"`
	HasClaude        *bool            `json:"has_claude"`
	ModelsState      string           `json:"models_state"`
	ModelsFetchedAt  *time.Time       `json:"models_fetched_at"`
	ModelsError      *string          `json:"models_error"`
	UsageState       string           `json:"usage_state"`
	UsageFetchedAt   *time.Time       `json:"usage_fetched_at"`
	UsageError       *string          `json:"usage_error"`
	Balance          *string          `json:"balance"`
	Unit             *string          `json:"unit"`
	DayUnit          *string          `json:"day_unit"`
	DayFetchedAt     *time.Time       `json:"day_fetched_at"`
	DailyUsage       *KeyUsageMetric  `json:"daily_usage"`
	ModelStats       []KeyUsageMetric `json:"model_stats"`
	ModelStatsDay    *string          `json:"model_stats_day"`
	ModelStatsAt     *time.Time       `json:"model_stats_fetched_at"`
	StoredMultiplier *string          `json:"stored_multiplier"`
	TargetMultiplier *string          `json:"target_multiplier"`
	ExpectClaude     bool             `json:"expect_claude"`
	GroupCheck       string           `json:"group_check"`
}

type KeyUsageMetric struct {
	Date               string  `json:"date,omitempty"`
	Model              string  `json:"model,omitempty"`
	Requests           *int64  `json:"requests"`
	Input              *int64  `json:"input_tokens"`
	Output             *int64  `json:"output_tokens"`
	CacheRead          *int64  `json:"cache_read_tokens"`
	CacheWrite         *int64  `json:"cache_write_tokens"`
	TotalTokens        *int64  `json:"total_tokens"`
	Cost               *string `json:"cost"`
	ActualCost         *string `json:"actual_cost"`
	AccountCost        *string `json:"account_cost"`
	ObservedMultiplier *string `json:"observed_multiplier"`
	MultiplierDiff     bool    `json:"multiplier_diff"`
}

func readKeyCapabilities(ctx context.Context, tx *sql.Tx, provider uuid.UUID, day string, now time.Time, view *Overview) error {
	rows, err := tx.QueryContext(ctx, readKeyCapabilitiesSQL, provider, day)
	if err != nil {
		return err
	}
	defer rows.Close()
	billing := view.billingByCredential()
	view.KeyCapabilities = []KeyCapability{}
	for rows.Next() {
		var key KeyCapability
		var modelList, group, daily, stats []byte
		if err := rows.Scan(&key.CredentialID, &key.Label, &key.WebsiteKeyID, &group, &modelList, &key.ModelsFetchedAt, &key.ModelsError,
			&key.Unit, &key.UsageFetchedAt, &key.UsageError,
			&key.TargetMultiplier, &key.ExpectClaude, &key.DayUnit, &key.DayFetchedAt, &daily, &stats, &key.ModelStatsDay, &key.ModelStatsAt); err != nil {
			return err
		}
		if len(group) != 0 {
			if err := json.Unmarshal(group, &key.WebsiteGroup); err != nil {
				return err
			}
		}
		billingData := billing[key.CredentialID]
		if binding := billingData.confirmedKeyBinding; binding != nil {
			if key.WebsiteKeyID != nil && *key.WebsiteKeyID != binding.KeyID {
				modelList, daily, stats = nil, nil, nil
				key.WebsiteGroup, key.ModelsFetchedAt, key.ModelsError = nil, nil, nil
				key.Unit, key.UsageFetchedAt, key.UsageError = nil, nil, nil
				key.DayUnit, key.DayFetchedAt, key.ModelStatsDay, key.ModelStatsAt = nil, nil, nil, nil
			}
			key.WebsiteKeyID = &binding.KeyID
		}
		if billingData.GroupStatus != "" && (billingData.GroupID == nil ||
			key.WebsiteGroup == nil || *billingData.GroupID != key.WebsiteGroup.ID) {
			// A model list from another group cannot describe the current group.
			modelList, key.ModelsFetchedAt = nil, nil
			if key.WebsiteGroup != nil {
				key.ModelsError = nil
			}
		}
		if snapshot := key.WebsiteGroup; snapshot != nil && snapshot.AccountRate != nil {
			binding, fetched := billingData.confirmedKeyBinding, key.ModelsFetchedAt
			// A successful model refresh timestamps the group snapshot written
			// in the same Key usage check.
			if billingData.GroupStatus != "assigned" || binding == nil || binding.GroupID == nil || *binding.GroupID != snapshot.ID ||
				key.WebsiteKeyID == nil || binding.KeyID != *key.WebsiteKeyID || key.ModelsError != nil ||
				fetched == nil || fetched.After(now) || now.Sub(*fetched) > keyUsageFreshness {
				snapshot.AccountRate = nil
			}
		}
		key.applyGroup(billingData)
		key.ModelsError = billingData.groupReadError(key.ModelsError)
		key.UsageError = billingData.groupReadError(key.UsageError)
		key.ModelsState = keyDataState(key.ModelsFetchedAt, key.ModelsError, now)
		key.UsageState = keyDataState(key.UsageFetchedAt, key.UsageError, now)
		if value := billingData.EffectiveMultiplierText; value != "" &&
			(!billingData.personalMultiplier || billingData.AccountRateSource == keyBillingRateSource) {
			key.StoredMultiplier = &value
		}
		savedRate := rateFromText(key.StoredMultiplier)
		key.Models = []string{}
		if len(modelList) != 0 {
			if err := json.Unmarshal(modelList, &key.Models); err != nil {
				return err
			}
			claude := slices.ContainsFunc(key.Models, isClaudeModel)
			key.HasClaude = &claude
		}
		if len(daily) != 0 {
			var point dailyUsagePoint
			if err := json.Unmarshal(daily, &point); err != nil {
				return err
			}
			metric, _ := usageMetric(point.usageCounters, point.CacheWrite, savedRate)
			metric.Date = point.Date
			key.DailyUsage = &metric
		}
		key.ModelStats = []KeyUsageMetric{}
		if len(stats) != 0 {
			var points []modelUsagePoint
			if err := json.Unmarshal(stats, &points); err != nil {
				return err
			}
			for _, point := range points {
				metric, _ := usageMetric(point.usageCounters, point.CacheWrite, savedRate)
				metric.Model = point.Model
				if key.ModelStatsDay != nil {
					metric.Date = *key.ModelStatsDay
				}
				key.ModelStats = append(key.ModelStats, metric)
			}
		}
		key.GroupCheck = "pending"
		if key.GroupStatus == "assigned" && key.ModelsState == "fresh" && key.WebsiteGroup != nil {
			key.GroupCheck = assessGroup(key, exactWebsiteGroupRate(key.WebsiteGroup), savedRate)
		}
		view.KeyCapabilities = append(view.KeyCapabilities, key)
	}
	return rows.Err()
}

func (key *KeyCapability) applyGroup(billing Dataset) {
	key.GroupStatus = billing.GroupStatus
	switch {
	case key.GroupStatus == "":
		key.GroupStatus = "pending"
		if key.WebsiteGroup != nil {
			key.GroupStatus = "assigned"
		}
	case billing.GroupID == nil:
		key.WebsiteGroup = nil
	default:
		if key.WebsiteGroup == nil || key.WebsiteGroup.ID != *billing.GroupID {
			key.WebsiteGroup = &WebsiteGroup{ID: *billing.GroupID}
		}
		if billing.GroupName != nil {
			key.WebsiteGroup.Name = *billing.GroupName
		}
		if billing.GroupRate != nil {
			rate := json.Number(*billing.GroupRate)
			key.WebsiteGroup.Rate = &rate
		}
	}
	group := key.WebsiteGroup
	if group == nil {
		return
	}
	// Once billing has a result, use its account rate and identity as one observation.
	if group.AccountRateSource != "" || group.AccountRateKeyID != 0 ||
		billing.BillingState == "current" || billing.BillingState == "stale" || billing.LastError != nil {
		group.AccountRate, group.AccountRateSource, group.AccountRateKeyID = nil, "", 0
	}
	if billing.AccountRate != nil {
		group.AccountRate, group.AccountRateSource, group.AccountRateKeyID = billing.AccountRate, billing.AccountRateSource, billing.accountRateKeyID
	} else if billing.websiteAccountRate != nil {
		group.AccountRate = billing.websiteAccountRate
	}
}

func keyDataState(at *time.Time, lastError *string, now time.Time) string {
	switch {
	case lastError != nil && at == nil:
		return "failed"
	case lastError != nil || at != nil && now.Sub(*at) > keyUsageFreshness:
		return "stale"
	case at != nil:
		return "fresh"
	default:
		return "pending"
	}
}

func isClaudeModel(name string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '/' || r == ':' || r == '.' }) {
		if part == "claude" || strings.HasPrefix(part, "claude-") {
			return true
		}
	}
	return false
}

func numberText(n *json.Number) *string {
	if n == nil {
		return nil
	}
	value := n.String()
	return &value
}

func rateFromText(value *string) *big.Rat {
	if value == nil {
		return nil
	}
	rate, _ := new(big.Rat).SetString(*value)
	return rate
}

func usageMetric(c usageCounters, write *int64, saved *big.Rat) (KeyUsageMetric, *big.Rat) {
	m := KeyUsageMetric{Requests: c.Requests, Input: c.Input, Output: c.Output, CacheRead: c.CacheRead,
		CacheWrite: write, TotalTokens: c.TotalTokens, Cost: numberText(c.Cost), ActualCost: numberText(c.ActualCost), AccountCost: numberText(c.AccountCost)}
	rate := observedUsageMultiplier(c)
	if rate == nil {
		return m, nil
	}
	text := strings.TrimRight(strings.TrimRight(rate.FloatString(8), "0"), ".")
	m.ObservedMultiplier, m.MultiplierDiff = &text, rateDiffers(rate, saved)
	return m, rate
}

func observedUsageMultiplier(c usageCounters) *big.Rat {
	cost, actual := rateFromText(numberText(c.Cost)), rateFromText(numberText(c.ActualCost))
	if cost == nil || cost.Sign() <= 0 || actual == nil {
		return nil
	}
	return new(big.Rat).Quo(actual, cost)
}

func rateDiffers(observed, reference *big.Rat) bool {
	if observed == nil || reference == nil {
		return false
	}
	var delta, limit big.Rat
	delta.Abs(delta.Sub(observed, reference))
	limit.Mul(reference, big.NewRat(5, 100))
	return delta.Cmp(&limit) > 0
}

func exactWebsiteGroupRate(group *WebsiteGroup) *big.Rat {
	if group.Rate == nil {
		return nil
	}
	return exactDecimal(*group.Rate)
}

func assessGroup(key KeyCapability, observed, saved *big.Rat) string {
	target := rateFromText(key.TargetMultiplier)
	mismatch := rateDiffers(observed, target)
	modelMismatch := key.ExpectClaude && key.ModelsState == "fresh" && key.HasClaude != nil && !*key.HasClaude
	if mismatch && saved != nil && !rateDiffers(observed, saved) {
		return "old_group"
	}
	if mismatch || modelMismatch {
		return "mismatch"
	}
	if observed != nil && target != nil && (!key.ExpectClaude || (key.ModelsState == "fresh" && key.HasClaude != nil && *key.HasClaude)) {
		return "target_observed"
	}
	return "pending"
}

const readKeyCapabilitiesSQL = `
SELECT c.id,c.label::text,o.website_key_id,o.website_group,
  CASE WHEN o.website_key_id IS NOT NULL THEN o.models END,
  CASE WHEN o.website_key_id IS NOT NULL THEN o.models_fetched_at END,
  COALESCE(o.models_error, CASE WHEN o.credential_id IS NULL THEN sync.last_error END),
  CASE WHEN o.website_key_id IS NOT NULL THEN o.unit END,
  CASE WHEN o.website_key_id IS NOT NULL THEN o.usage_fetched_at END,
  COALESCE(o.usage_error, CASE WHEN o.credential_id IS NULL THEN sync.last_error END),
  trim_scale(o.target_multiplier)::text,COALESCE(o.expect_claude,false),
  d.unit,d.fetched_at,d.daily_usage,d.model_stats,
  CASE WHEN d.model_stats IS NOT NULL THEN $2::text END,d.model_fetched_at
FROM provider_credentials c JOIN providers p ON p.id=c.provider_id
LEFT JOIN beichen_key_observations o ON o.credential_id=c.id AND o.key_digest=sha256(c.credential_ciphertext)
LEFT JOIN beichen_key_usage_days d ON d.credential_id=o.credential_id AND d.key_digest=o.key_digest
  AND d.website_key_id=o.website_key_id AND d.day=$2::text::date
LEFT JOIN beichen_datasets sync ON sync.credential_id=c.id AND sync.kind='key_usage'
WHERE c.provider_id=$1 AND p.status='active' AND c.status='active'
ORDER BY c.label,c.id`
