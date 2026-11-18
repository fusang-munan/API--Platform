package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
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
	if !validPanelBearer(token) {
		return errors.New("BEICHEN_PANEL_TOKEN 须为专用面板 Bearer 凭据")
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
	rows      []priceRow
	expiresAt time.Time
	err       error
}

func (s *Service) fetchPanelQuote(ctx context.Context, token, key string, group int64, info billingInfo) panelQuote {
	if info.GroupID != nil && *info.GroupID != group {
		return panelQuote{err: errors.New("北辰 Key 账单分组与面板映射不一致")}
	}
	if !validPanelBearer(token) || strings.TrimSpace(key) == token {
		return panelQuote{err: errors.New("北辰面板须使用独立的读取凭据")}
	}
	expires, err := panelExpiry(info, s.now())
	if err != nil {
		return panelQuote{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, panelURL, nil)
	if err != nil {
		return panelQuote{err: errors.New("构建北辰面板请求失败")}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	body, err := s.readResponse(req)
	if err != nil {
		return panelQuote{err: err}
	}
	models, err := s.fetch(ctx, key, modelsPath, nil)
	if err != nil {
		return panelQuote{err: err}
	}
	entries, err := catalog.ParseProviderModels(models)
	if err != nil || len(entries) > maxRows {
		return panelQuote{err: errors.New("北辰 Key 模型目录无效")}
	}
	rows, err := parsePanelPrices(body, group, info, entries)
	if err != nil {
		return panelQuote{err: err}
	}
	if len(info.Pricing) == 0 || bytes.Equal(info.Pricing, []byte("null")) {
		return panelQuote{rows: rows, expiresAt: expires}
	}
	basis, confirmed, err := parsePricingUnit(info.Pricing, panelRateUnit)
	if err != nil || basis != "base" {
		return panelQuote{err: errors.New("北辰 Key 账单提供了无法确认的逐模型基础报价")}
	}
	verified := make(map[string]Rates, len(confirmed))
	for _, row := range confirmed {
		verified[row.Model] = row.Rates
	}
	accepted := rows[:0]
	for _, row := range rows {
		if ratesEqual(row.Rates, verified[row.Model]) {
			accepted = append(accepted, row)
		}
	}
	if len(accepted) == 0 {
		return panelQuote{err: errors.New("北辰面板报价与 Key 账单的逐模型基础价无可信交集")}
	}
	return panelQuote{rows: accepted, expiresAt: expires}
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

func parsePanelPrices(body []byte, groupID int64, info billingInfo, entries []catalog.ProviderEntry) ([]priceRow, error) {
	var payload struct {
		Code *int `json:"code"`
		Data []struct {
			Platforms []panelSection `json:"platforms"`
			Status    string         `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Code == nil || *payload.Code != 0 || payload.Data == nil {
		return nil, errors.New("北辰面板价表格式无效")
	}
	var section *panelSection
	for _, channel := range payload.Data {
		for i := range channel.Platforms {
			candidate := &channel.Platforms[i]
			for _, group := range candidate.Groups {
				if group.ID != groupID {
					continue
				}
				if section != nil || candidate.Platform == "" || group.Platform != candidate.Platform || group.Platform == "composite" {
					return nil, errors.New("北辰面板分组无法唯一对应单个渠道和平台")
				}
				if (channel.Status != "" && channel.Status != "active") || (group.Status != "" && group.Status != "active") {
					return nil, errors.New("北辰面板分组或渠道已停用")
				}
				if group.SubscriptionType != "standard" {
					return nil, errors.New("北辰面板分组未确认为标准余额组")
				}
				if !matchesPanelMultiplier(group, info) {
					return nil, errors.New("北辰面板与 Key 账单的分组倍率或高峰规则不一致")
				}
				section = candidate
			}
		}
	}
	if section == nil || len(section.Models) > maxRows {
		return nil, errors.New("北辰面板找不到映射分组或模型数量超限")
	}
	available := make(map[string]bool, len(entries))
	for _, model := range entries {
		available[model.ID] = true
	}
	seen := make(map[string]bool, len(section.Models))
	var rows []priceRow
	for _, model := range section.Models {
		if !available[model.Name] || !validModel(model.Name) {
			continue
		}
		if seen[model.Name] || model.Platform != section.Platform {
			return nil, errors.New("北辰面板模型重复或平台不匹配")
		}
		seen[model.Name] = true
		if rates, ok := flatPanelRates(model.Pricing); ok {
			rows = append(rows, priceRow{Model: model.Name, Rates: rates})
		}
	}
	if len(rows) == 0 {
		return nil, errors.New("北辰面板无可确认的平价 Token 单价")
	}
	return rows, nil
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
		err := invalidatePanelQuote(ctx, tx, credentialID, revision, effectiveAt)
		if err != nil {
			return err
		}
		return recordPriceError(ctx, tx, credentialID, quote.err.Error())
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_panel_snapshots (credential_id, effective_at, credential_revision, group_id, expires_at, multiplier_version)
SELECT $1, $2, $3, $4, $5, id
FROM beichen_multiplier_versions
WHERE credential_id = $1 AND effective_at <= $2
ORDER BY effective_at DESC, id DESC LIMIT 1`, credentialID, effectiveAt, revision, group, quote.expiresAt)
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
	return recordSuccess(ctx, tx, credentialID, "prices")
}

func invalidatePanelQuote(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, revision, effectiveAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_panel_snapshots (credential_id, effective_at, credential_revision)
VALUES ($1, $2, $3)
ON CONFLICT (credential_id, effective_at) DO NOTHING`, credentialID, effectiveAt, revision)
	return err
}

// InvalidatePanelQuote records a config change and schedules verification. Call
// in the same transaction after locking the credential and changing its revision.
func (s *Service) InvalidatePanelQuote(ctx context.Context, tx *sql.Tx, credentialID uuid.UUID, revision time.Time) error {
	if err := invalidatePanelQuote(ctx, tx, credentialID, revision, revision); err != nil {
		return err
	}
	if err := recordPriceError(ctx, tx, credentialID, "北辰面板配置已变更，等待重新同步"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_datasets (credential_id, kind, next_attempt)
VALUES ($1, 'billing', clock_timestamp())
ON CONFLICT (credential_id, kind) DO UPDATE SET
  next_attempt = EXCLUDED.next_attempt, lease_id = NULL, lease_until = NULL`, credentialID)
	return err
}
