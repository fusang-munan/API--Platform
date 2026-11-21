package beichen

import (
	"encoding/json"
	"math/big"
	"time"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

const websitePriceUnit = "元（北辰余额单位）"

type groupPriceObservation struct {
	GroupID              int64           `json:"group_id"`
	Model                string          `json:"model"`
	ObservedAt           time.Time       `json:"observed_at"`
	GroupRules           directoryGroup  `json:"group_rules"`
	Pricing              json.RawMessage `json:"pricing"`
	AccountRatesVerified bool            `json:"account_rates_verified"`
}

func websitePriceObservations(crawl directoryCrawl) []groupPriceObservation {
	groups := map[int64]bool{}
	rows := []groupPriceObservation{}
	for _, section := range crawl.platforms {
		for _, group := range section.Groups {
			if group.ID <= 0 || groups[group.ID] {
				continue
			}
			groups[group.ID] = true
			directory, err := selectWebsiteKeyCatalog(crawl.platforms, group.ID)
			if err != nil {
				continue
			}
			rules := directory.group
			// Only the rules participate in price-version changes.
			rules.Name, rules.OwnModels = "", false
			for _, entry := range directory.entries {
				pricing := directory.prices[entry.ID]
				if len(pricing) != 0 && pricing[0] != '{' {
					pricing = nil
				}
				rows = append(rows, groupPriceObservation{group.ID, entry.ID, directory.at, rules, pricing, crawl.accountRatesVerified})
			}
		}
	}
	return rows
}

func websiteMissingPriceFields(raw json.RawMessage) []string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	missing := []string{}
	for _, name := range []string{"billing_mode", "input_price", "output_price", "cache_read_price", "cache_write_price",
		"cache_write_1h_price", "cache_write_12h_price", "per_request_price", "image_input_price", "image_output_price",
		"video_input_price", "video_output_price", "intervals", "reasoning_effort_multipliers"} {
		if websiteMissing(fields[name]) {
			missing = append(missing, name)
		}
	}
	return missing
}

func baseTokenPricesPerMillion(raw json.RawMessage) map[string]string {
	if !usage.BeichenTokenQuoteUsable(raw) {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	prices := make(map[string]string)
	million := big.NewRat(1_000_000, 1)
	for _, field := range []string{
		"input_price", "output_price", "cache_write_price", "cache_write_1h_price", "cache_write_12h_price", "cache_read_price",
	} {
		var value json.Number
		if json.Unmarshal(fields[field], &value) != nil {
			continue
		}
		if price := exactDecimal(value); price != nil {
			prices[field] = websiteDecimalText(price.Mul(price, million))
		}
	}
	return prices
}
