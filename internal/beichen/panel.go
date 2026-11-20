package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
)

const (
	panelURL      = "https://beichenapi.com/api/v1/channels/available"
	panelQuoteTTL = 16 * time.Minute
	panelRateUnit = "beichen_micros_per_million_tokens"
)

// ConfigurePanel is called during startup, before using the service.
func (s *Service) ConfigurePanel(token, groups string) error {
	if token == "" && groups == "" {
		return nil
	}
	if token != "" && !validPanelBearer(token) {
		return errors.New("北辰面板授权格式无效")
	}
	bindings := make(map[uuid.UUID]int64)
	decoder := json.NewDecoder(strings.NewReader(groups))
	badMapping := errors.New("BEICHEN_PANEL_GROUPS 须为凭据 UUID 到正整数分组 ID 的 JSON 对象，且键不得重复")
	if first, err := decoder.Token(); err != nil || first != json.Delim('{') {
		return badMapping
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return badMapping
		}
		id, err := uuid.Parse(key.(string))
		var group int64
		if err != nil || id == uuid.Nil || bindings[id] != 0 || decoder.Decode(&group) != nil || group <= 0 {
			return badMapping
		}
		bindings[id] = group
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || len(bindings) == 0 {
		return badMapping
	}
	if _, err := decoder.Token(); err != io.EOF {
		return badMapping
	}
	s.panelToken, s.panelGroups = token, bindings
	return nil
}

func validPanelBearer(token string) bool {
	return token != "" && len(token) <= 4096 && !strings.HasPrefix(strings.ToLower(token), "sk-") &&
		strings.IndexFunc(token, func(r rune) bool { return r <= ' ' || r >= 127 }) < 0
}

type panelQuote struct {
	groupID           int64
	rows              []priceRow
	terms             map[string]json.RawMessage
	notListed         []string
	expiresAt         time.Time
	err               error
	verificationError error
}

var errPanelLoginRequired = errors.New("北辰渠道页要求登录")
var errPanelEmpty = errors.New("北辰渠道页没有当前 Key 的有效模型报价")
var errPanelGroupUnverified = errors.New("无法核实北辰 Key 当前所属分组")

func (s *Service) fetchChannelQuote(ctx context.Context, access priceAccess, key string, info billingInfo, entries []catalog.ProviderEntry) panelQuote {
	group := billingPanelGroup(info)
	if access.disabled {
		return panelQuote{groupID: group, err: errors.New("北辰面板采价已停用")}
	}
	webConfigured := s.WebSessionConfigured()
	// The school's channels use the saved account every round. Legacy bindings
	// outside that provisioning path can still use their own read authorization.
	if access.legacyToken != "" && access.channel == "" {
		quote := s.fetchPanelQuote(ctx, access.legacyToken, key, group, access, info, entries)
		if (quote.err == nil && len(quote.terms) > 0) || !webConfigured {
			return quote
		}
	}
	if !webConfigured {
		if access.channel != "" {
			return panelQuote{groupID: group, err: errors.New("未配置北辰学校网页会话")}
		}
		if group == 0 {
			if access.groupID != 0 {
				return panelQuote{err: errPanelGroupUnverified}
			}
			// Existing billing-extension and CSV deployments have no website source.
			return panelQuote{}
		}
		return s.fetchPanelQuote(ctx, "", key, group, access, info, entries)
	}
	token, err := s.webPanelToken(ctx)
	if err != nil {
		return panelQuote{groupID: group, err: fmt.Errorf("学校北辰网页会话不可用：%w", err)}
	}
	quote := s.fetchPanelQuote(ctx, token, key, group, access, info, entries)
	if !panelAuthFailed(quote.err) {
		return quote
	}
	// A website authorization can expire before its locally recorded expiry.
	s.webReadError(token, upstreamHTTPStatus(http.StatusUnauthorized))
	token, err = s.webPanelToken(ctx)
	if err != nil {
		return panelQuote{groupID: quote.groupID, err: fmt.Errorf("续期学校的北辰网页会话失败：%w", err)}
	}
	retried := s.fetchPanelQuote(ctx, token, key, group, access, info, entries)
	if panelAuthFailed(retried.err) {
		s.webReadError(token, upstreamHTTPStatus(http.StatusUnauthorized))
		retried.err = errors.New("续期后北辰渠道页仍拒绝学校网页会话")
	}
	return retried
}

func panelAuthFailed(err error) bool {
	return errors.Is(err, errPanelLoginRequired) ||
		errors.Is(err, upstreamHTTPStatus(http.StatusUnauthorized)) ||
		errors.Is(err, upstreamHTTPStatus(http.StatusForbidden))
}

func (s *Service) fetchPanelQuote(ctx context.Context, token, key string, group int64, access priceAccess, info billingInfo, entries []catalog.ProviderEntry) panelQuote {
	quote := panelQuote{groupID: group}
	if info.GroupID != nil && *info.GroupID > 0 && *info.GroupID != group {
		quote.err = errors.New("北辰 Key 账单分组与面板映射不一致")
		return quote
	}
	if token != "" && (!validPanelBearer(token) || strings.TrimSpace(key) == token) {
		quote.err = errors.New("北辰渠道页授权无效或与推理 Key 相同")
		return quote
	}
	expires, err := panelExpiry(info, s.now())
	if err != nil {
		quote.err = err
		return quote
	}
	if group == 0 {
		if token == "" {
			quote.err = errPanelGroupUnverified
			return quote
		}
		keys, err := s.accountKeys(ctx, token)
		if err != nil {
			quote.err = fmt.Errorf("%w：%w", errPanelGroupUnverified, err)
			return quote
		}
		match, err := info.matchWebsiteKey(keys, key, access.channel, access.label)
		if err != nil {
			quote.err = errPanelGroupUnverified
			return quote
		}
		quote.groupID = match.GroupID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, panelURL, nil)
	if err != nil {
		quote.err = errors.New("构建北辰面板请求失败")
		return quote
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	body, err := s.readResponse(req)
	if err != nil {
		quote.err = err
		return quote
	}
	sections, err := decodePanelChannels(body)
	if err != nil {
		quote.err = err
		return quote
	}
	matched, err := matchPanelPrices(sections, quote.groupID, info, entries)
	if err != nil {
		quote.err = err
		return quote
	}
	for model, raw := range matched.terms {
		if !hasPanelPrice(raw) {
			delete(matched.terms, model)
		}
	}
	if !matched.hasPrices {
		quote.err = errPanelEmpty
		return quote
	}
	quote.rows, quote.terms, quote.expiresAt = matched.rows, matched.terms, expires
	if len(matched.rows) > 0 && len(info.Pricing) != 0 && !bytes.Equal(info.Pricing, []byte("null")) {
		quote.rows = nil
		verified, err := panelBillingRates(info.Pricing)
		if err != nil {
			quote.verificationError = errors.New("北辰 Key 账单提供了无法确认的逐模型基础报价")
			return quote
		}
		for _, row := range matched.rows {
			if ratesEqual(row.Rates, verified[row.Model]) {
				quote.rows = append(quote.rows, row)
			}
		}
		if len(quote.rows) == 0 {
			quote.verificationError = errors.New("北辰面板报价与 Key 账单的逐模型基础价无可信交集")
			return quote
		}
	}
	quote.notListed = matched.notListed
	return quote
}

// The optional billing extension independently confirms panel prices. A tiered
// or duplicate model cannot corroborate a flat rate for that model.
func panelBillingRates(raw json.RawMessage) (map[string]Rates, error) {
	var pricing struct {
		Basis  string            `json:"basis"`
		Unit   string            `json:"unit"`
		Models []json.RawMessage `json:"models"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&pricing) != nil || pricing.Basis != "base" || pricing.Unit != panelRateUnit ||
		len(pricing.Models) == 0 || len(pricing.Models) > maxRows {
		return nil, errors.New("北辰 Key 账单基础价格式或单位无效")
	}
	seen := make(map[string]bool, len(pricing.Models))
	verified := make(map[string]Rates)
	for _, data := range pricing.Models {
		var row priceRow
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&row)
		if seen[row.Model] {
			delete(verified, row.Model)
			continue
		}
		seen[row.Model] = true
		if err == nil && validModel(row.Model) && validRates(&row.Rates) {
			verified[row.Model] = row.Rates
		}
	}
	return verified, nil
}

func ratesEqual(a, b Rates) bool {
	for _, pair := range [][2]*int64{{a.Input, b.Input}, {a.Output, b.Output}, {a.CacheRead, b.CacheRead}, {a.CacheWrite, b.CacheWrite}} {
		x, y := pair[0], pair[1]
		if (x == nil) != (y == nil) || (x != nil && *x != *y) {
			return false
		}
	}
	return true
}
func panelExpiry(info billingInfo, now time.Time) (time.Time, error) {
	badTime := errors.New("北辰 Key 倍率缺少可信采样时间或高峰时段")
	if info.ObservedAt.Before(now.Add(-jobTimeout)) || info.ObservedAt.After(now) || info.PeakEnabled == nil {
		return time.Time{}, badTime
	}
	expires := info.ObservedAt.Add(panelQuoteTTL)
	if !*info.PeakEnabled {
		return expires, nil
	}
	loc, err := time.LoadLocation(info.Timezone)
	if err != nil || info.Timezone == "" || info.Timezone == "Local" {
		return time.Time{}, badTime
	}
	start, firstErr := time.Parse("15:04", info.PeakStart)
	end, lastErr := time.Parse("15:04", info.PeakEnd)
	if firstErr != nil || lastErr != nil || !start.Before(end) {
		return time.Time{}, badTime
	}
	inPeak := func(t time.Time) bool {
		return inPanelPeak(t.In(loc), start, end)
	}
	sampledPeak := inPeak(info.ObservedAt)
	// Walk local minute changes in real time, including timezone offset changes.
	for t := info.ObservedAt.Truncate(time.Minute).Add(time.Minute); t.Before(expires); t = t.Add(time.Minute) {
		if inPeak(t) != sampledPeak {
			return t, nil
		}
	}
	return expires, nil
}

type panelGroup struct {
	ID               int64       `json:"id"`
	Platform         string      `json:"platform"`
	SubscriptionType string      `json:"subscription_type"`
	Status           string      `json:"status"`
	PeakEnabled      *bool       `json:"peak_rate_enabled"`
	PeakStart        string      `json:"peak_start"`
	PeakEnd          string      `json:"peak_end"`
	Rate             json.Number `json:"rate_multiplier"`
	PeakRate         json.Number `json:"peak_rate_multiplier"`
}

func samePanelRate(a, b json.Number) bool {
	x, y := exactDecimal(a), exactDecimal(b)
	return x != nil && y != nil && x.Cmp(y) == 0
}

func matchesPanelMultiplier(group panelGroup, info billingInfo) bool {
	if !samePanelRate(group.Rate, info.GroupRate) || group.PeakEnabled == nil || info.PeakEnabled == nil ||
		*group.PeakEnabled != *info.PeakEnabled {
		return false
	}
	resolved := info.GroupRate
	if info.UserRate != nil {
		resolved = *info.UserRate
	}
	if !samePanelRate(resolved, info.ResolvedRate) {
		return false
	}
	if !*info.PeakEnabled {
		return samePanelRate(info.Multiplier, resolved)
	}
	if group.PeakStart != info.PeakStart || group.PeakEnd != info.PeakEnd || !samePanelRate(group.PeakRate, info.PeakRate) {
		return false
	}
	loc, err := time.LoadLocation(info.Timezone)
	start, startErr := time.Parse("15:04", info.PeakStart)
	end, endErr := time.Parse("15:04", info.PeakEnd)
	if err != nil || startErr != nil || endErr != nil || !start.Before(end) {
		return false
	}
	expected := json.Number("1")
	if inPanelPeak(info.ObservedAt.In(loc), start, end) {
		expected = info.PeakRate
	}
	if !samePanelRate(info.AppliedPeak, expected) {
		return false
	}
	base, _ := resolved.Float64()
	factor, _ := expected.Float64()
	effective, err := info.Multiplier.Float64()
	// Sub2API multiplies float64 values before encoding the effective rate.
	return err == nil && base*factor == effective
}

func inPanelPeak(local, start, end time.Time) bool {
	minute := local.Hour()*60 + local.Minute()
	return minute >= start.Hour()*60+start.Minute() && minute < end.Hour()*60+end.Minute()
}

type panelSection struct {
	Platform string       `json:"platform"`
	Groups   []panelGroup `json:"groups"`
	Models   []struct {
		Name     string          `json:"name"`
		Platform string          `json:"platform"`
		Pricing  json.RawMessage `json:"pricing"`
	} `json:"supported_models"`
}

type panelChannel struct {
	Platforms []panelSection `json:"platforms"`
	Status    string         `json:"status"`
}

func decodePanelChannels(body []byte) ([]panelChannel, error) {
	var payload struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("北辰面板价表格式无效")
	}
	switch string(payload.Code) {
	case "0":
		var channels []panelChannel
		if len(payload.Data) != 0 && json.Unmarshal(payload.Data, &channels) != nil {
			return nil, errors.New("北辰面板价表格式无效")
		}
		if len(channels) == 0 {
			return nil, errPanelEmpty
		}
		return channels, nil
	case "401", "403", `"UNAUTHORIZED"`, `"FORBIDDEN"`, `"TOKEN_EXPIRED"`:
		return nil, errPanelLoginRequired
	default:
		return nil, errors.New("北辰面板价表格式无效")
	}
}

type matchedPanelPrices struct {
	rows      []priceRow
	terms     map[string]json.RawMessage
	notListed []string
	hasPrices bool
}

func matchPanelPrices(channels []panelChannel, groupID int64, info billingInfo, entries []catalog.ProviderEntry) (matchedPanelPrices, error) {
	var sections []panelSection
	selectedChannel := -1
	groupPlatform := ""
	seenPlatforms := make(map[string]bool)
	modelCount := 0
	for i, channel := range channels {
		for _, candidate := range channel.Platforms {
			for _, group := range candidate.Groups {
				if group.ID != groupID {
					continue
				}
				// The site lists a composite group under each concrete platform
				// within the same channel. It must never span different channels.
				if groupID <= 0 || candidate.Platform == "" || seenPlatforms[candidate.Platform] ||
					(group.Platform != candidate.Platform && group.Platform != "composite") ||
					(selectedChannel >= 0 && (selectedChannel != i || groupPlatform != group.Platform)) {
					return matchedPanelPrices{}, errors.New("北辰面板分组无法唯一对应同一渠道的平台")
				}
				if (channel.Status != "" && channel.Status != "active") || (group.Status != "" && group.Status != "active") {
					return matchedPanelPrices{}, errors.New("北辰面板分组或渠道已停用")
				}
				if group.SubscriptionType != "standard" {
					return matchedPanelPrices{}, errors.New("北辰面板分组未确认为标准余额组")
				}
				if !matchesPanelMultiplier(group, info) {
					return matchedPanelPrices{}, errors.New("北辰面板与 Key 账单的分组倍率或高峰规则不一致")
				}
				selectedChannel, groupPlatform = i, group.Platform
				seenPlatforms[candidate.Platform] = true
				sections = append(sections, candidate)
				modelCount += len(candidate.Models)
			}
		}
	}
	if len(sections) == 0 || modelCount > maxRows {
		return matchedPanelPrices{}, errors.New("北辰面板找不到映射分组或模型数量超限")
	}
	available := make(map[string]bool, len(entries))
	for _, model := range entries {
		available[model.ID] = true
	}
	occurrences := make(map[string]int, modelCount)
	complete := true
	for _, section := range sections {
		complete = complete && section.Models != nil
		for _, model := range section.Models {
			occurrences[model.Name]++
			complete = complete && validModel(model.Name) && model.Platform == section.Platform
		}
	}
	matched := matchedPanelPrices{terms: make(map[string]json.RawMessage)}
	for _, section := range sections {
		for _, model := range section.Models {
			if !validModel(model.Name) || occurrences[model.Name] != 1 ||
				model.Platform != section.Platform || len(model.Pricing) == 0 || model.Pricing[0] != '{' {
				continue
			}
			matched.hasPrices = matched.hasPrices || hasPanelPrice(model.Pricing)
			if !available[model.Name] {
				continue
			}
			matched.terms[model.Name] = model.Pricing
			if rates, ok := flatPanelRates(model.Pricing); ok {
				matched.rows = append(matched.rows, priceRow{Model: model.Name, Rates: rates})
			}
		}
	}
	if complete && matched.hasPrices {
		for _, model := range entries {
			if validModel(model.ID) && occurrences[model.ID] == 0 {
				matched.notListed = append(matched.notListed, model.ID)
			}
		}
	}
	return matched, nil
}

var panelAmountField = regexp.MustCompile(`^(input|output|cache_read|cache_write(_[0-9]+[hm])?|image_input|image_output|per_request)_price$`)

func hasPanelPrice(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	if hasPanelAmounts(fields) {
		return true
	}
	var intervals []map[string]json.RawMessage
	if json.Unmarshal(fields["intervals"], &intervals) == nil {
		for _, interval := range intervals {
			if hasPanelAmounts(interval) {
				return true
			}
		}
	}
	return false
}

func hasPanelAmounts(fields map[string]json.RawMessage) bool {
	for name, raw := range fields {
		if panelAmountField.MatchString(name) {
			var number json.Number
			if json.Unmarshal(raw, &number) == nil && exactDecimal(number) != nil {
				return true
			}
		}
	}
	return false
}

func flatPanelRates(raw json.RawMessage) (Rates, bool) {
	var pricing struct {
		Mode                 string                     `json:"billing_mode"`
		Input                *json.Number               `json:"input_price"`
		Output               *json.Number               `json:"output_price"`
		CacheRead            *json.Number               `json:"cache_read_price"`
		CacheWrite           *json.Number               `json:"cache_write_price"`
		CacheWrite1h         *json.Number               `json:"cache_write_1h_price"`
		CacheWrite12h        *json.Number               `json:"cache_write_12h_price"`
		ImageInput           *json.Number               `json:"image_input_price"`
		ImageOutput          *json.Number               `json:"image_output_price"`
		PerRequest           *json.Number               `json:"per_request_price"`
		Intervals            []json.RawMessage          `json:"intervals"`
		ReasoningMultipliers map[string]json.RawMessage `json:"reasoning_effort_multipliers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&pricing) != nil || pricing.Mode != "token" || pricing.Input == nil || pricing.Output == nil ||
		pricing.CacheWrite1h != nil || pricing.CacheWrite12h != nil || pricing.ImageInput != nil || pricing.ImageOutput != nil ||
		pricing.PerRequest != nil || len(pricing.Intervals) != 0 {
		return Rates{}, false
	}
	for _, multiplier := range pricing.ReasoningMultipliers {
		var value json.Number
		if json.Unmarshal(multiplier, &value) != nil || !samePanelRate(value, "1") {
			return Rates{}, false
		}
	}
	var rates Rates
	for _, field := range []struct {
		number *json.Number
		rate   **int64
	}{
		{pricing.Input, &rates.Input}, {pricing.Output, &rates.Output},
		{pricing.CacheRead, &rates.CacheRead}, {pricing.CacheWrite, &rates.CacheWrite},
	} {
		if field.number == nil {
			continue
		}
		value := exactDecimal(*field.number)
		if value == nil {
			return Rates{}, false
		}
		// One million tokens × one million micro-units per North Star unit.
		value.Mul(value, big.NewRat(1_000_000_000_000, 1))
		if !value.IsInt() || !value.Num().IsInt64() || value.Num().Int64() > maxRate {
			return Rates{}, false
		}
		rate := value.Num().Int64()
		*field.rate = &rate
	}
	return rates, true
}

// Conditional terms are displayable even when the gateway has neither the
// cache TTL nor the context tier required to calculate this request's charge.
func conditionalPanelPricing(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for name, value := range fields {
		switch {
		case strings.HasPrefix(name, "cache_write_") && strings.HasSuffix(name, "_price"),
			name == "per_request_price", name == "image_input_price", name == "image_output_price":
			var n json.Number
			if json.Unmarshal(value, &n) == nil && exactDecimal(n) != nil {
				return true
			}
		case name == "intervals":
			var tiers []json.RawMessage
			if json.Unmarshal(value, &tiers) == nil && len(tiers) > 0 {
				return true
			}
		case name == "reasoning_effort_multipliers":
			var factors map[string]json.Number
			if json.Unmarshal(value, &factors) == nil {
				for _, n := range factors {
					if exactDecimal(n) != nil && !samePanelRate(n, "1") {
						return true
					}
				}
			}
		}
	}
	return false
}

func saveBillingObservation(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, revision time.Time, info billingInfo, billingErr error, group int64, quote panelQuote) error {
	if group == 0 && quote.err == nil {
		var managed bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM beichen_panel_snapshots WHERE credential_id = $1)`, credentialID).Scan(&managed)
		if err != nil {
			return err
		}
		if !managed {
			if billingErr != nil {
				return nil
			}
			return saveBilling(ctx, tx, credentialID, info)
		}
		quote.err = errors.New("北辰面板分组映射已移除")
	}
	effectiveAt, err := priceTimestamp(ctx, tx)
	if err != nil {
		return err
	}
	if billingErr != nil {
		quote.err = billingErr
	} else if err := saveMultiplier(ctx, tx, credentialID, info, effectiveAt); err != nil {
		return err
	}
	if quote.err == nil && (effectiveAt.Before(info.ObservedAt) || !quote.expiresAt.After(effectiveAt)) {
		quote.err = errors.New("北辰报价已过期或同步期间跨越高峰边界")
	}
	if quote.err != nil {
		// A failed read retains the last quote only while this round can still
		// verify its group and multiplier.
		if billingErr == nil {
			if err := invalidateChangedPanelQuote(ctx, tx, credentialID, revision, effectiveAt, group, info.Multiplier.String()); err != nil {
				return err
			}
		}
		return recordDatasetError(ctx, tx, credentialID, "prices", quote.err.Error())
	}
	terms := []byte("{}")
	for _, model := range quote.notListed {
		// JSON null records coverage without a matching model. An absent key
		// means coverage was not established for that model.
		quote.terms[model] = json.RawMessage("null")
	}
	if quote.terms != nil {
		terms, err = json.Marshal(quote.terms)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_panel_snapshots (credential_id, effective_at, credential_revision, group_id, expires_at, multiplier_version, model_pricing, credential_ciphertext_sha256)
SELECT c.id, $2, $3, $4, $5, m.id, $6::jsonb, sha256(c.credential_ciphertext)
FROM provider_credentials c JOIN beichen_multiplier_versions m ON m.credential_id = c.id
WHERE c.id = $1 AND m.effective_at <= $2
ORDER BY m.effective_at DESC, m.id DESC LIMIT 1`, credentialID, effectiveAt, revision, group, quote.expiresAt, string(terms))
	if err != nil {
		return err
	}
	for _, row := range quote.rows {
		_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_price_versions (credential_id, model, track, basis, source, input_rate, output_rate, cache_read_rate, cache_write_rate, effective_at)
VALUES ($1, $2, 'upstream', 'base', 'panel', $3, $4, $5, $6, $7)`,
			credentialID, row.Model, row.Rates.Input, row.Rates.Output, row.Rates.CacheRead, row.Rates.CacheWrite, effectiveAt)
		if err != nil {
			return err
		}
	}
	if quote.verificationError != nil {
		return recordDatasetError(ctx, tx, credentialID, "prices", quote.verificationError.Error())
	}
	return recordSuccess(ctx, tx, credentialID, "prices")
}

func invalidatePanelQuote(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, revision, effectiveAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_panel_snapshots (credential_id, effective_at, credential_revision)
VALUES ($1, $2, $3)
ON CONFLICT (credential_id, effective_at) DO NOTHING`, credentialID, effectiveAt, revision)
	return err
}

func invalidateChangedPanelQuote(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, revision, effectiveAt time.Time, group int64, multiplier string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_panel_snapshots (credential_id, effective_at, credential_revision)
SELECT $1, $2, $3
FROM (
  SELECT group_id, expires_at, multiplier_version FROM beichen_panel_snapshots
  WHERE credential_id = $1 ORDER BY effective_at DESC LIMIT 1
) previous JOIN beichen_multiplier_versions m ON m.id = previous.multiplier_version
WHERE previous.expires_at > $2
  AND ($4::bigint = 0 OR previous.group_id <> $4 OR m.multiplier <> NULLIF($5::text, '')::numeric)
ON CONFLICT (credential_id, effective_at) DO NOTHING`, credentialID, effectiveAt, revision, group, multiplier)
	return err
}

func schedulePanelRefresh(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID) error {
	if err := recordDatasetError(ctx, tx, credentialID, "prices", "北辰面板配置已变更，等待重新同步"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id, kind, next_attempt)
VALUES ($1, 'billing', clock_timestamp())
ON CONFLICT (credential_id, kind) DO UPDATE SET
  next_attempt = EXCLUDED.next_attempt, lease_id = NULL, lease_until = NULL`, credentialID)
	return err
}
