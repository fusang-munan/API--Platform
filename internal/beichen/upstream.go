package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	billingPath      = "/v1/sub2api/billing"
	usagePath        = "/v1/usage"
	modelsPath       = "/v1/models"
	maxResponseBytes = 2 << 20
)

var decimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})(\.[0-9]{1,24})?([eE][+-]?[0-9]{1,2})?$`)

func newClient() *http.Client {
	dialer := &net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
			TLSHandshakeTimeout: 4 * time.Second, ResponseHeaderTimeout: 6 * time.Second,
			IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4,
			MaxResponseHeaderBytes: 32 << 10,
		},
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (s *Service) fetch(ctx context.Context, key, path string, query url.Values) ([]byte, error) {
	switch path {
	case billingPath, usagePath, modelsPath:
	default:
		return nil, errors.New("北辰接口路径无效")
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("北辰凭据格式无效")
	}
	target := url.URL{Scheme: "https", Host: "api.beichenapi.com", Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, errors.New("构建北辰请求失败")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	return s.readResponse(req)
}

func (s *Service) readResponse(req *http.Request) ([]byte, error) {
	return readBeichenResponse(s.client, req)
}

type upstreamHTTPStatus int

func (status upstreamHTTPStatus) Error() string {
	return fmt.Sprintf("北辰接口返回 HTTP %d", int(status))
}

func readBeichenResponse(client *http.Client, req *http.Request) ([]byte, error) {
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("北辰网络请求失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, upstreamHTTPStatus(response.StatusCode)
	}
	if response.ContentLength > maxResponseBytes {
		return nil, errors.New("北辰接口响应超过 2 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("读取北辰接口响应失败")
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("北辰接口响应超过 2 MiB")
	}
	return body, nil
}

type billingInfo struct {
	Object        string          `json:"object"`
	SchemaVersion int             `json:"schema_version"`
	Scope         string          `json:"billing_scope"`
	Multiplier    json.Number     `json:"effective_rate_multiplier"`
	GroupRate     json.Number     `json:"group_rate_multiplier"`
	ResolvedRate  json.Number     `json:"resolved_rate_multiplier"`
	UserRate      *json.Number    `json:"user_rate_multiplier"`
	Timezone      string          `json:"timezone"`
	Pricing       json.RawMessage `json:"pricing"`
	GroupID       *int64          `json:"group_id"`
	KeyID         int64           `json:"key_id"`
	APIKeyID      int64           `json:"api_key_id"`
	APIKeyName    string          `json:"api_key_name"`
	ObservedAt    time.Time       `json:"observed_at"`
	PeakEnabled   *bool           `json:"peak_rate_enabled"`
	PeakStart     string          `json:"peak_start"`
	PeakEnd       string          `json:"peak_end"`
	PeakRate      json.Number     `json:"peak_rate_multiplier"`
	AppliedPeak   json.Number     `json:"applied_peak_multiplier"`
}

func parseBilling(body []byte) (billingInfo, error) {
	var info billingInfo
	if err := json.Unmarshal(body, &info); err != nil ||
		info.Object != "sub2api.key_billing" || info.SchemaVersion != 1 || info.Scope != "token" {
		return billingInfo{}, errors.New("北辰账单响应缺少可确认的 Key 计费范围")
	}
	multiplier := exactDecimal(info.Multiplier)
	if multiplier == nil || multiplier.Cmp(big.NewRat(1000, 1)) > 0 {
		return billingInfo{}, errors.New("北辰账单响应缺少有效倍率")
	}
	if (info.GroupID != nil && *info.GroupID != 0 && billingPanelGroup(info) == 0) ||
		(info.GroupRate != "" && exactDecimal(info.GroupRate) == nil) ||
		(info.PeakRate != "" && exactDecimal(info.PeakRate) == nil) {
		return billingInfo{}, errors.New("北辰账单响应的分组或分组倍率无效")
	}
	if len(info.PeakStart) > 16 || len(info.PeakEnd) > 16 ||
		strings.ContainsRune(info.PeakStart+info.PeakEnd, 0) {
		return billingInfo{}, errors.New("北辰账单响应的高峰时段无效")
	}
	if len(info.Timezone) > 64 || strings.ContainsRune(info.Timezone, 0) {
		info.Timezone = ""
	}
	return info, nil
}

// Sub2API v1 provides the multiplier only. A provider pricing extension must
// declare both its token-rate unit and whether the rates are base or effective.
func parsePricing(data json.RawMessage) (string, []priceRow, error) {
	var pricing struct {
		Basis  string     `json:"basis"`
		Unit   string     `json:"unit"`
		Models []priceRow `json:"models"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&pricing) != nil || pricing.Unit != "usd_micros_per_million_tokens" ||
		(pricing.Basis != "base" && pricing.Basis != "effective") || len(pricing.Models) == 0 || len(pricing.Models) > maxRows {
		return "", nil, errors.New("北辰账单未提供可确认单位和口径的模型单价，可导入报价 CSV")
	}
	seen := make(map[string]bool, len(pricing.Models))
	for _, model := range pricing.Models {
		if !validModel(model.Model) || !validRates(&model.Rates) || seen[model.Model] {
			return "", nil, errors.New("北辰账单的模型单价不完整或存在重复")
		}
		seen[model.Model] = true
	}
	return pricing.Basis, pricing.Models, nil
}

type usageCounters struct {
	Requests   *int64       `json:"requests"`
	Input      *int64       `json:"input_tokens"`
	Output     *int64       `json:"output_tokens"`
	CacheRead  *int64       `json:"cache_read_tokens"`
	ActualCost *json.Number `json:"actual_cost"`
}

func (c usageCounters) counts(cacheWrite *int64) [5]*int64 {
	return [5]*int64{c.Requests, c.Input, c.Output, c.CacheRead, cacheWrite}
}

type dailyUsagePoint struct {
	Date string `json:"date"`
	usageCounters
	CacheWrite *int64 `json:"cache_write_tokens"`
}

type usageResponse struct {
	Mode     string `json:"mode"`
	Valid    *bool  `json:"isValid"`
	Date     string `json:"date"`
	Timezone string `json:"timezone"`
	Unit     string `json:"unit"`
	Models   []struct {
		Model string `json:"model"`
		usageCounters
		CacheCreation *int64 `json:"cache_creation_tokens"`
	} `json:"model_stats"`
	Daily []dailyUsagePoint `json:"daily_usage"`
}

func parseUsage(body []byte, day string) ([]usageRow, error) {
	var response usageResponse
	if err := json.Unmarshal(body, &response); err != nil || (response.Valid != nil && !*response.Valid) ||
		(response.Mode != "quota_limited" && response.Mode != "unrestricted") ||
		response.Models == nil || len(response.Models) > maxRows {
		return nil, errors.New("北辰用量未返回当前 Key 的单日模型明细，可导入用量 CSV")
	}
	if (response.Date != "" && response.Date != day) ||
		(response.Timezone != "" && response.Timezone != "Asia/Shanghai") {
		return nil, errors.New("北辰模型用量的北京时间日期范围无法确认，可导入用量 CSV")
	}
	rows := make([]usageRow, 0, len(response.Models))
	seen := make(map[string]bool, len(response.Models))
	for _, model := range response.Models {
		if !validModel(model.Model) || seen[model.Model] ||
			hasNegative(model.Requests, model.Input, model.Output, model.CacheRead, model.CacheCreation) {
			return nil, errors.New("北辰单日模型用量字段无效或存在重复")
		}
		var billed *int64
		if response.Unit == "USD" && model.ActualCost != nil {
			amount := exactDecimal(*model.ActualCost)
			if amount == nil {
				return nil, errors.New("北辰实际费用格式无效")
			}
			// Storage uses whole microdollars: positive amounts below $0.0000005
			// are displayed as zero. Date verification compares the raw amounts.
			value, ok := roundedUSDMicros(amount)
			if !ok {
				return nil, errors.New("北辰实际费用超出范围")
			}
			billed = &value
		}
		seen[model.Model] = true
		if model.Requests == nil && model.Input == nil && model.Output == nil &&
			model.CacheRead == nil && model.CacheCreation == nil && billed == nil {
			continue
		}
		rows = append(rows, usageRow{date: day, model: model.Model, requests: model.Requests, input: model.Input,
			output: model.Output, cacheRead: model.CacheRead, cacheWrite: model.CacheCreation, billed: billed})
	}
	if len(rows) == 0 && len(response.Models) != 0 {
		return nil, errors.New("北辰单日模型用量没有可核对的计数或实付金额")
	}
	if response.Date == "" || response.Timezone == "" ||
		(len(response.Models) == 0 && len(response.Daily) > 0) {
		if err := verifyDailyUsage(response, day); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// The caller requests the last four daily points in Asia/Shanghai. A partial
// set of model counters can never establish the day. Nonempty model totals
// must match exactly one day; an explicit zero day can coexist with other zero days.
func verifyDailyUsage(response usageResponse, day string) error {
	days := make(map[string]dailyUsagePoint, len(response.Daily))
	for _, point := range response.Daily {
		_, duplicate := days[point.Date]
		if _, err := dayStart(point.Date); err != nil || duplicate {
			return errors.New("北辰逐日用量日期无效或重复，可导入用量 CSV")
		}
		days[point.Date] = point
	}
	target, ok := days[day]
	if !ok {
		return errors.New("北辰逐日用量缺少目标日期，可导入用量 CSV")
	}

	const requiredCounts = 3 // requests, input tokens, output tokens
	totals := [5]int64{}
	complete := [5]bool{true, true, true, true, true}
	cost := new(big.Rat)
	costKnown := response.Unit == "USD"
	for _, model := range response.Models {
		for index, value := range model.counts(model.CacheCreation) {
			if value == nil {
				complete[index] = false
				continue
			}
			if *value > math.MaxInt64-totals[index] {
				return errors.New("北辰模型用量汇总超出范围")
			}
			totals[index] += *value
		}
		if model.ActualCost == nil {
			costKnown = false
		}
		if costKnown {
			// parseUsage already validated every USD amount.
			cost.Add(cost, exactDecimal(*model.ActualCost))
		}
	}
	matches := func(point dailyUsagePoint) (bool, error) {
		match := true
		for index, value := range point.counts(point.CacheWrite) {
			if index < requiredCounts && (!complete[index] || value == nil) {
				return false, errors.New("北辰用量缺少可核对的请求数或输入输出 Token 明细，可导入用量 CSV")
			}
			if value != nil && *value < 0 {
				return false, errors.New("北辰逐日用量计数无效")
			}
			if value != nil && complete[index] && totals[index] != *value {
				match = false
			}
		}
		if costKnown && point.ActualCost != nil {
			amount := exactDecimal(*point.ActualCost)
			if amount == nil {
				return false, errors.New("北辰逐日用量实际费用格式无效")
			}
			match = match && cost.Cmp(amount) == 0
		}
		return match, nil
	}
	match, err := matches(target)
	if err != nil {
		return err
	}
	if !match {
		return errors.New("北辰目标日的用量汇总与模型明细不一致，可导入用量 CSV")
	}
	empty := len(response.Models) == 0
	if empty {
		if target.ActualCost == nil {
			return errors.New("北辰目标日缺少实付金额，无法确认零用量快照")
		}
		amount := exactDecimal(*target.ActualCost)
		if amount == nil || amount.Sign() != 0 {
			return errors.New("北辰目标日未明确实付为零，无法确认零用量快照")
		}
	}
	for _, point := range response.Daily {
		if point.Date == day {
			continue
		}
		match, err := matches(point)
		if err != nil {
			return err
		}
		if match && !empty {
			return errors.New("北辰模型用量同时匹配多个日期，无法确认归属，可导入用量 CSV")
		}
	}
	return nil
}

func hasNegative(values ...*int64) bool {
	for _, number := range values {
		if number != nil && *number < 0 {
			return true
		}
	}
	return false
}

func exactDecimal(number json.Number) *big.Rat {
	if !decimal.MatchString(number.String()) {
		return nil
	}
	value, _ := new(big.Rat).SetString(number.String())
	return value
}

func roundedUSDMicros(amount *big.Rat) (int64, bool) {
	var scaled big.Rat
	scaled.Mul(amount, big.NewRat(1_000_000, 1))
	scaled.Add(&scaled, big.NewRat(1, 2))
	micros := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	if !micros.IsInt64() {
		return 0, false
	}
	return micros.Int64(), true
}
