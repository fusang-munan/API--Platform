package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

var beichenTokenPriceFields = []string{
	"input_price", "output_price", "cache_read_price", "cache_write_price", "cache_write_1h_price", "cache_write_12h_price",
}

var beichenNonTokenPriceFields = []string{
	"per_request_price", "image_input_price", "image_output_price", "video_input_price", "video_output_price",
}

type normalizedBeichenQuote struct {
	terms         json.RawMessage
	ignoredFields []string
	nonTokenField string
}

type beichenQuoteFields map[string]json.RawMessage

type beichenPriceConditionsJSON beichenPriceConditions

// Apply the same normalization to stored and newly fetched panel quotes at
// the decoding boundary used by both the publication check and settlement.
func (c *beichenPriceConditions) UnmarshalJSON(raw []byte) error {
	quote, err := normalizeBeichenPanelQuote(raw)
	if err != nil {
		return err
	}
	return decodeBeichenPanelJSON(quote.terms, (*beichenPriceConditionsJSON)(c))
}

func decodeBeichenPanelJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func normalizeBeichenPanelQuote(raw json.RawMessage) (normalizedBeichenQuote, error) {
	var fields beichenQuoteFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		return normalizedBeichenQuote{}, errors.New("pricing")
	}
	mode, err := fields.billingMode()
	if err != nil {
		return normalizedBeichenQuote{}, errors.New("billing_mode")
	}
	var tiers []json.RawMessage
	if rawTiers := fields["intervals"]; len(rawTiers) != 0 && json.Unmarshal(rawTiers, &tiers) != nil {
		return normalizedBeichenQuote{}, errors.New("intervals")
	}

	var quote, tierNotes normalizedBeichenQuote
	baseToken := mode == "token" || (mode == "" && fields.hasTokenPricePair())
	tokenTier := false
	for i, rawTier := range tiers {
		var tier beichenQuoteFields
		prefix := fmt.Sprintf("第 %d 档 ", i+1)
		if json.Unmarshal(rawTier, &tier) != nil {
			return normalizedBeichenQuote{}, fmt.Errorf("%sintervals[%d]", prefix, i)
		}
		tierMode, err := tier.billingMode()
		if err != nil {
			return normalizedBeichenQuote{}, errors.New(prefix + "billing_mode")
		}
		primaryToken := tierMode == "token" || (tierMode == "" && (baseToken || tier.hasTokenPricePair()))
		tokenTier = tokenTier || primaryToken
		if err := tier.normalize(&tierNotes, tierMode, primaryToken, prefix, fmt.Sprintf("intervals[%d].", i)); err != nil {
			return normalizedBeichenQuote{}, err
		}
		tiers[i], err = json.Marshal(tier)
		if err != nil {
			return normalizedBeichenQuote{}, err
		}
	}
	if len(tiers) != 0 {
		fields["intervals"], err = json.Marshal(tiers)
		if err != nil {
			return normalizedBeichenQuote{}, err
		}
	}
	primaryToken := baseToken || (mode == "" && tokenTier)
	if err := fields.normalize(&quote, mode, primaryToken, "", ""); err != nil {
		return normalizedBeichenQuote{}, err
	}
	quote.ignoredFields = append(quote.ignoredFields, tierNotes.ignoredFields...)
	if quote.nonTokenField == "" {
		quote.nonTokenField = tierNotes.nonTokenField
	}
	quote.terms, err = json.Marshal(fields)
	return quote, err
}

func (fields beichenQuoteFields) billingMode() (string, error) {
	var mode string
	if raw := fields["billing_mode"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &mode); err != nil {
			return "", err
		}
		if trimmed := strings.TrimSpace(mode); trimmed != mode {
			fields["billing_mode"], _ = json.Marshal(trimmed)
			mode = trimmed
		}
	}
	return mode, nil
}

func (fields beichenQuoteFields) hasTokenPricePair() bool {
	var input, output json.Number
	if json.Unmarshal(fields["input_price"], &input) != nil || json.Unmarshal(fields["output_price"], &output) != nil {
		return false
	}
	in, out := beichenPriceDecimal(&input), beichenPriceDecimal(&output)
	// With no mode, two zero token rates cannot disambiguate a nonzero fee.
	return in != nil && out != nil && (in.Sign() != 0 || out.Sign() != 0)
}

func blankBeichenPrice(raw json.RawMessage) bool {
	var value string
	return len(raw) != 0 && raw[0] == '"' && json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) == ""
}

func (fields beichenQuoteFields) normalize(quote *normalizedBeichenQuote, mode string, primaryToken bool, label, path string) error {
	if quote.nonTokenField == "" && mode != "" && mode != "token" {
		quote.nonTokenField = path + "billing_mode"
	}
	for _, name := range beichenNonTokenPriceFields {
		raw, present := fields[name]
		if !present || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if primaryToken || blankBeichenPrice(raw) || zeroBeichenNonTokenPrice(raw) {
			delete(fields, name)
			quote.ignoredFields = append(quote.ignoredFields, path+name)
			continue
		}
		if quote.nonTokenField == "" {
			quote.nonTokenField = path + name
		}
		var number json.Number
		if json.Unmarshal(raw, &number) != nil || beichenPriceDecimal(&number) == nil {
			return errors.New(label + name)
		}
	}
	for _, name := range beichenTokenPriceFields {
		raw, present := fields[name]
		if !present {
			continue
		}
		if blankBeichenPrice(raw) {
			fields[name] = json.RawMessage("null")
			continue
		}
		var number json.Number
		if json.Unmarshal(raw, &number) != nil {
			return errors.New(label + name)
		}
	}
	return nil
}

func zeroBeichenNonTokenPrice(raw json.RawMessage) bool {
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return false
	}
	number = json.Number(strings.TrimPrefix(number.String(), "-"))
	price := beichenPriceDecimal(&number)
	return price != nil && price.Sign() == 0
}

// BeichenPanelQuoteBlocker adds the upstream field to the shared readiness
// result for teachers. The blocker codes also identify the same stored quote.
func BeichenPanelQuoteBlocker(raw json.RawMessage) (code, message string) {
	code, message = BeichenTokenQuoteBlocker(raw)
	var field string
	switch code {
	case "billing_mode_non_token", "price_conditions_invalid":
		quote, err := normalizeBeichenPanelQuote(raw)
		if err != nil {
			field = err.Error()
		} else if code == "billing_mode_non_token" {
			field = quote.nonTokenField
		} else {
			field = invalidBeichenQuoteField(quote.terms)
		}
	case "missing_input", "tier_missing_input":
		field = "input_price"
	case "missing_output", "tier_missing_output":
		field = "output_price"
	case "tier_range_invalid":
		field = "min_tokens / max_tokens"
	case "effort_multiplier_invalid":
		field = "reasoning_effort_multipliers"
	}
	if field != "" {
		message += "（" + field + "）"
	}
	return code, message
}

func invalidBeichenQuoteField(raw json.RawMessage) string {
	var fields struct {
		Intervals []json.RawMessage `json:"intervals"`
	}
	if json.Unmarshal(raw, &fields) == nil {
		for i, tier := range fields.Intervals {
			if err := decodeBeichenPanelJSON(tier, new(beichenPanelTier)); err != nil {
				return fmt.Sprintf("第 %d 档 %s", i+1, beichenJSONErrorField(err))
			}
		}
	}
	return beichenJSONErrorField(decodeBeichenPanelJSON(raw, new(beichenPriceConditionsJSON)))
}

func beichenJSONErrorField(err error) string {
	if err == nil {
		return ""
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return typeErr.Field
	}
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return field
	}
	return "pricing"
}

// BeichenPanelDisplayTerms projects the fields used for text pricing. The
// raw terms remain available for display of unsupported quotes and auditing.
func BeichenPanelDisplayTerms(raw json.RawMessage) json.RawMessage {
	quote, err := normalizeBeichenPanelQuote(raw)
	if err != nil || quote.nonTokenField != "" {
		return raw
	}
	return quote.terms
}

// BeichenPanelIgnoredFields lists the price fields left out by normalization.
func BeichenPanelIgnoredFields(raw json.RawMessage) []string {
	quote, err := normalizeBeichenPanelQuote(raw)
	if err != nil {
		return nil
	}
	return quote.ignoredFields
}

// BeichenFlatTokenPrices projects the four fixed prices in upstream units
// per token. Quotes with other rate conditions use the conditional ledger.
func BeichenFlatTokenPrices(raw json.RawMessage) (input, output, cacheRead, cacheWrite *json.Number) {
	terms := decodeBeichenConditions(raw)
	if terms == nil || !terms.usesTokenBilling() || terms.hasTimedCacheWrite() || len(terms.Intervals) != 0 {
		return nil, nil, nil, nil
	}
	for _, multiplier := range terms.EffortMultiplier {
		amount := beichenPriceDecimal(multiplier)
		if amount == nil || amount.Cmp(big.NewRat(1, 1)) != 0 {
			return nil, nil, nil, nil
		}
	}
	return terms.Input, terms.Output, terms.CacheRead, terms.CacheWrite
}

func recordIgnoredBeichenPanelPrices(terms, detail json.RawMessage) json.RawMessage {
	quote, err := normalizeBeichenPanelQuote(terms)
	if err != nil || quote.nonTokenField != "" || len(quote.ignoredFields) == 0 {
		return detail
	}
	fields := make(map[string]json.RawMessage)
	if len(detail) != 0 && (json.Unmarshal(detail, &fields) != nil || fields == nil) {
		return detail
	}
	fields["ignored_non_token_fields"], _ = json.Marshal(quote.ignoredFields)
	updated, err := json.Marshal(fields)
	if err != nil {
		return detail
	}
	return updated
}
