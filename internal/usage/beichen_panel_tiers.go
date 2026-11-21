package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
)

var beichenTierPriceMultipliers = [...]struct{ price, multiplier string }{
	{"input_price", "input_multiplier"},
	{"output_price", "output_multiplier"},
	{"cache_read_price", "cache_read_multiplier"},
	{"cache_write_price", "cache_write_multiplier"},
}

// The website's multiplier intervals override the base price for the entire
// request. Legacy intervals with absolute prices are already in ledger form.
func normalizeBeichenMultiplierTiers(base beichenQuoteFields, tiers []beichenQuoteFields, nonTokenField string) (bool, error) {
	factors := make([]map[string]*big.Rat, len(tiers))
	usesMultipliers := false
	for i, tier := range tiers {
		for _, field := range beichenTierPriceMultipliers {
			raw := tier[field.multiplier]
			delete(tier, field.multiplier)
			if !hasBeichenTierValue(raw) {
				continue
			}
			var n json.Number
			if nonTokenField != "" || json.Unmarshal(raw, &n) != nil {
				return false, fmt.Errorf("第 %d 档 %s", i+1, field.multiplier)
			}
			factor := beichenPriceDecimal(&n)
			if factor == nil {
				return false, fmt.Errorf("第 %d 档 %s", i+1, field.multiplier)
			}
			if factors[i] == nil {
				factors[i] = make(map[string]*big.Rat)
			}
			factors[i][field.multiplier] = factor
			usesMultipliers = true
		}
	}
	if !usesMultipliers {
		return false, nil
	}
	for i, tier := range tiers {
		if err := normalizeBeichenMultiplierBounds(tier); err != nil {
			return false, fmt.Errorf("第 %d 档 %w", i+1, err)
		}
		if err := resolveBeichenTierPrices(base, tier, factors[i]); err != nil {
			return false, fmt.Errorf("第 %d 档 %w", i+1, err)
		}
	}
	return true, nil
}

func hasBeichenTierValue(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(raw, []byte("null"))
}

func normalizeBeichenMultiplierBounds(tier beichenQuoteFields) error {
	var interval beichenPanelTier
	if json.Unmarshal(tier["min_tokens"], &interval.Min) != nil {
		return errors.New("min_tokens")
	}
	if upper := tier["max_tokens"]; len(upper) != 0 && json.Unmarshal(upper, &interval.Max) != nil {
		return errors.New("max_tokens")
	}
	bounds, ok := interval.tokenRange()
	if !ok || bounds.min == math.MaxInt64 {
		return errors.New("min_tokens / max_tokens")
	}
	// Sub2API uses (min, max]. Convert to the ledger's [min, max) notation.
	tier["min_tokens"] = json.RawMessage(strconv.FormatInt(bounds.min+1, 10))
	tier["max_tokens"] = json.RawMessage("null")
	if bounds.max >= 0 && bounds.max < math.MaxInt64 {
		tier["max_tokens"] = json.RawMessage(strconv.FormatInt(bounds.max+1, 10))
	}
	return nil
}

func resolveBeichenTierPrices(base, tier beichenQuoteFields, factors map[string]*big.Rat) error {
	fixedCacheWrite := hasBeichenTierValue(tier["cache_write_price"])
	for _, field := range beichenTierPriceMultipliers {
		if err := inheritBeichenTierPrice(base, tier, field.price, factors[field.multiplier]); err != nil {
			return err
		}
	}
	if fixedCacheWrite {
		// An absolute cache-write override also sets the 1h price and clears
		// the 12h price unless those durations have their own tier price.
		if !hasBeichenTierValue(tier["cache_write_1h_price"]) && hasBeichenTierValue(base["cache_write_1h_price"]) {
			tier["cache_write_1h_price"] = tier["cache_write_price"]
		}
		return nil
	}
	for _, name := range []string{"cache_write_1h_price", "cache_write_12h_price"} {
		if err := inheritBeichenTierPrice(base, tier, name, factors["cache_write_multiplier"]); err != nil {
			return err
		}
	}
	return nil
}

func inheritBeichenTierPrice(base, tier beichenQuoteFields, price string, multiplier *big.Rat) error {
	raw := base[price]
	if hasBeichenTierValue(tier[price]) || !hasBeichenTierValue(raw) {
		return nil
	}
	if multiplier != nil {
		var number json.Number
		if json.Unmarshal(raw, &number) != nil {
			return errors.New(price)
		}
		amount := beichenPriceDecimal(&number)
		if amount == nil {
			return errors.New(price)
		}
		product := json.Number(beichenDecimalString(amount.Mul(amount, multiplier)))
		if beichenPriceDecimal(&product) == nil {
			return errors.New(price)
		}
		raw = json.RawMessage(product.String())
	}
	tier[price] = raw
	return nil
}
