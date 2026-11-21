package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

type dashboardMetrics struct {
	Requests   int64        `json:"requests"`
	Tokens     *int64       `json:"total_tokens"`
	Input      *int64       `json:"input_tokens"`
	Output     *int64       `json:"output_tokens"`
	CacheRead  *int64       `json:"cache_read_tokens"`
	CacheWrite *int64       `json:"cache_write_tokens"`
	Standard   *json.Number `json:"standard_cny"`
	Actual     *json.Number `json:"actual_cny"`
}

func (m dashboardMetrics) counts() [5]*int64 {
	return [5]*int64{m.Tokens, m.Input, m.Output, m.CacheRead, m.CacheWrite}
}

func decodeDashboardMetrics(data map[string]json.RawMessage, prefix string) (dashboardMetrics, error) {
	tokenField := "total_tokens"
	if prefix == "today_" {
		tokenField = "today_tokens"
	}
	var requests *int64
	var m dashboardMetrics
	for _, field := range []struct {
		name string
		dest any
	}{
		{prefix + "requests", &requests},
		{tokenField, &m.Tokens},
		{prefix + "input_tokens", &m.Input},
		{prefix + "output_tokens", &m.Output},
		{prefix + "cache_read_tokens", &m.CacheRead},
		{prefix + "cache_creation_tokens", &m.CacheWrite},
		{prefix + "cost", &m.Standard},
		{prefix + "actual_cost", &m.Actual},
	} {
		raw := data[field.name]
		if websiteMissing(raw) || json.Unmarshal(raw, field.dest) != nil {
			return dashboardMetrics{}, errDashboardFields
		}
	}
	if requests == nil || *requests < 0 || m.Standard == nil || dashboardAmount(*m.Standard) == nil ||
		m.Actual == nil || dashboardAmount(*m.Actual) == nil {
		return dashboardMetrics{}, errDashboardFields
	}
	for _, count := range m.counts() {
		if count == nil || *count < 0 {
			return dashboardMetrics{}, errDashboardFields
		}
	}
	m.Requests = *requests
	return m, nil
}

func dashboardAmount(n json.Number) *big.Rat {
	if !receiptNumber.MatchString(strings.TrimPrefix(n.String(), "-")) {
		return nil
	}
	r, _ := new(big.Rat).SetString(n.String())
	return r
}

func dashboardAmountNumber(value *big.Rat) *json.Number {
	n := json.Number(websiteDecimalText(value))
	return &n
}

// Keep decimal arithmetic exact and disregard only wire-level float noise.
var dashboardMoneyTolerance = big.NewRat(1, 1_000_000_000_000)

func dashboardMoneyDelta(left, right *json.Number) *json.Number {
	if left == nil || right == nil {
		return nil
	}
	a, b := dashboardAmount(*left), dashboardAmount(*right)
	if a == nil || b == nil {
		return nil
	}
	diff := a.Sub(a, b)
	if new(big.Rat).Abs(diff).Cmp(dashboardMoneyTolerance) <= 0 {
		diff.SetInt64(0)
	}
	return dashboardAmountNumber(diff)
}

func dashboardCountDelta(left, right *int64) *int64 {
	if left == nil || right == nil {
		return nil
	}
	diff := *left - *right
	return &diff
}

func (m dashboardMetrics) minus(other dashboardMetrics) dashboardMetrics {
	return dashboardMetrics{
		Requests:   m.Requests - other.Requests,
		Tokens:     dashboardCountDelta(m.Tokens, other.Tokens),
		Input:      dashboardCountDelta(m.Input, other.Input),
		Output:     dashboardCountDelta(m.Output, other.Output),
		CacheRead:  dashboardCountDelta(m.CacheRead, other.CacheRead),
		CacheWrite: dashboardCountDelta(m.CacheWrite, other.CacheWrite),
		Standard:   dashboardMoneyDelta(m.Standard, other.Standard),
		Actual:     dashboardMoneyDelta(m.Actual, other.Actual),
	}
}

func (m dashboardMetrics) allZero() bool {
	if m.Requests != 0 || m.Standard == nil || *m.Standard != "0" || m.Actual == nil || *m.Actual != "0" {
		return false
	}
	for _, count := range m.counts() {
		if count == nil || *count != 0 {
			return false
		}
	}
	return true
}

type dashboardSum struct {
	requests      int64
	counts        [5]int64
	money         [2]big.Rat
	missingCounts [5]bool
	missingMoney  [2]bool
}

func (s *dashboardSum) add(m dashboardMetrics) error {
	if m.Requests < 0 || m.Requests > math.MaxInt64-s.requests {
		return errDashboardFields
	}
	s.requests += m.Requests
	for i, count := range m.counts() {
		if count == nil {
			s.missingCounts[i] = true
			continue
		}
		if *count < 0 || *count > math.MaxInt64-s.counts[i] {
			return errDashboardFields
		}
		s.counts[i] += *count
	}
	for i, amount := range []*json.Number{m.Standard, m.Actual} {
		if amount == nil {
			s.missingMoney[i] = true
			continue
		}
		value := dashboardAmount(*amount)
		if value == nil {
			return errDashboardFields
		}
		s.money[i].Add(&s.money[i], value)
	}
	return nil
}

func (s *dashboardSum) metrics() dashboardMetrics {
	var counts [5]*int64
	for i, count := range s.counts {
		if !s.missingCounts[i] {
			counts[i] = &count
		}
	}
	var money [2]*json.Number
	for i := range s.money {
		if !s.missingMoney[i] {
			money[i] = dashboardAmountNumber(&s.money[i])
		}
	}
	return dashboardMetrics{s.requests, counts[0], counts[1], counts[2], counts[3], counts[4], money[0], money[1]}
}

type dashboardTotalsCheck struct {
	Sum     dashboardMetrics `json:"sum"`
	Delta   dashboardMetrics `json:"account_minus_sum"`
	Matches bool             `json:"matches"`
}

func compareDashboardTotals(reference dashboardMetrics, metrics []dashboardMetrics) (dashboardTotalsCheck, error) {
	var sum dashboardSum
	for _, row := range metrics {
		if err := sum.add(row); err != nil {
			return dashboardTotalsCheck{}, err
		}
	}
	total := sum.metrics()
	delta := reference.minus(total)
	return dashboardTotalsCheck{total, delta, delta.allZero()}, nil
}

type dashboardPlatformUsage struct {
	Requests int64        `json:"requests"`
	Tokens   *int64       `json:"total_tokens"`
	Actual   *json.Number `json:"actual_cny"`
}

func (p dashboardPlatformUsage) valid() bool {
	return p.Requests >= 0 && p.Tokens != nil && *p.Tokens >= 0 && p.Actual != nil && dashboardAmount(*p.Actual) != nil
}

type dashboardPlatformCard struct {
	Platform string                 `json:"platform"`
	Today    dashboardPlatformUsage `json:"today"`
	All      dashboardPlatformUsage `json:"all"`
}

func (p *dashboardPlatformCard) UnmarshalJSON(body []byte) error {
	var wire struct {
		Platform      string       `json:"platform"`
		TodayRequests *int64       `json:"today_requests"`
		TodayTokens   *int64       `json:"today_tokens"`
		TodayActual   *json.Number `json:"today_actual_cost"`
		TotalRequests *int64       `json:"total_requests"`
		TotalTokens   *int64       `json:"total_tokens"`
		TotalActual   *json.Number `json:"total_actual_cost"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.TodayRequests == nil || wire.TotalRequests == nil {
		return errDashboardFields
	}
	*p = dashboardPlatformCard{
		Platform: wire.Platform,
		Today:    dashboardPlatformUsage{*wire.TodayRequests, wire.TodayTokens, wire.TodayActual},
		All:      dashboardPlatformUsage{*wire.TotalRequests, wire.TotalTokens, wire.TotalActual},
	}
	return nil
}

type dashboardPlatformRow struct {
	Platform string `json:"platform"`
	dashboardPlatformUsage
}

type dashboardPlatforms struct {
	Source string                 `json:"source"`
	Items  []dashboardPlatformRow `json:"items"`
	Delta  dashboardPlatformUsage `json:"account_minus_platforms"`
}

func summarizeDashboardPlatforms(account dashboardMetrics, source string, rows []dashboardPlatformRow) (dashboardPlatforms, error) {
	var sum dashboardSum
	for _, row := range rows {
		if err := sum.add(dashboardMetrics{Requests: row.Requests, Tokens: row.Tokens, Actual: row.Actual}); err != nil {
			return dashboardPlatforms{}, err
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Platform < rows[j].Platform })
	diff := account.minus(sum.metrics())
	return dashboardPlatforms{source, rows, dashboardPlatformUsage{diff.Requests, diff.Tokens, diff.Actual}}, nil
}

func dashboardCardPlatforms(account dashboardMetrics, cards []dashboardPlatformCard, period string) (dashboardPlatforms, error) {
	rows := make([]dashboardPlatformRow, 0, len(cards))
	for _, card := range cards {
		current := card.All
		if period == "today" {
			current = card.Today
		}
		rows = append(rows, dashboardPlatformRow{card.Platform, current})
	}
	return summarizeDashboardPlatforms(account, "dashboard.by_platform", rows)
}

func dashboardLedgerPlatforms(account dashboardMetrics, records []dashboardUsageRecord) (dashboardPlatforms, error) {
	byPlatform := make(map[string]*dashboardSum)
	for _, record := range records {
		platform := record.Group.Platform
		if platform == "" {
			platform = "unattributed"
		}
		if byPlatform[platform] == nil {
			byPlatform[platform] = new(dashboardSum)
		}
		if err := byPlatform[platform].add(record.metrics()); err != nil {
			return dashboardPlatforms{}, err
		}
	}
	rows := make([]dashboardPlatformRow, 0, len(byPlatform))
	for platform, sums := range byPlatform {
		m := sums.metrics()
		rows = append(rows, dashboardPlatformRow{platform, dashboardPlatformUsage{m.Requests, m.Tokens, m.Actual}})
	}
	return summarizeDashboardPlatforms(account, "usage.group.platform", rows)
}

type dashboardUsageRecord struct {
	ID        int64        `json:"id"`
	KeyID     int64        `json:"api_key_id"`
	UserID    int64        `json:"user_id"`
	Model     string       `json:"model"`
	CreatedAt time.Time    `json:"created_at"`
	Input     *int64       `json:"input_tokens"`
	Output    *int64       `json:"output_tokens"`
	Read      *int64       `json:"cache_read_tokens"`
	Write     *int64       `json:"cache_creation_tokens"`
	Standard  *json.Number `json:"total_cost"`
	Actual    *json.Number `json:"actual_cost"`
	Group     struct {
		Platform string `json:"platform"`
	} `json:"group"`
}

func (r dashboardUsageRecord) metrics() dashboardMetrics {
	m := dashboardMetrics{Requests: 1, Input: r.Input, Output: r.Output, CacheRead: r.Read,
		CacheWrite: r.Write, Standard: r.Standard, Actual: r.Actual}
	var count int64
	for _, n := range []*int64{r.Input, r.Output, r.Read, r.Write} {
		if n == nil || *n < 0 || *n > math.MaxInt64-count {
			return m
		}
		count += *n
	}
	m.Tokens = &count
	return m
}

type dashboardRecent struct {
	CreatedAt     time.Time `json:"created_at"`
	Model         string    `json:"model"`
	KeyID         int64     `json:"key_id"`
	DisplayTokens *int64    `json:"display_tokens"`
	dashboardMetrics
}

type dashboardUsagePage struct {
	Items []dashboardUsageRecord `json:"items"`
	Total *int                   `json:"total"`
	Page  int                    `json:"page"`
	Size  int                    `json:"page_size"`
	Pages int                    `json:"pages"`
}

func (d dashboardSession) usagePage(ctx context.Context, w dashboardWindow, owner int64, page, pageSize int, sortBy, order string) (dashboardUsagePage, error) {
	var out dashboardUsagePage
	q := w.query(0)
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	q.Set("sort_by", sortBy)
	q.Set("sort_order", order)
	if err := d.get(ctx, dashboardUsage, q, &out); err != nil {
		return out, err
	}
	if out.Total == nil || *out.Total < 0 || out.Items == nil || out.Page != page || out.Size != pageSize {
		return out, dashboardReadError(dashboardUsage, errDashboardFields)
	}
	total := *out.Total
	pages := total / pageSize
	if total%pageSize != 0 {
		pages++
	}
	if total == 0 && out.Pages == 0 {
		out.Pages = 1
	}
	if out.Pages != max(1, pages) || page > out.Pages || len(out.Items) != min(pageSize, total-(page-1)*pageSize) {
		return out, dashboardReadError(dashboardUsage, errDashboardFields)
	}
	for i := range out.Items {
		row := &out.Items[i]
		date := row.CreatedAt.In(beijing).Format(dayLayout)
		if row.ID <= 0 || row.UserID != owner || row.KeyID < 0 || !validModel(row.Model) ||
			row.CreatedAt.IsZero() || date < w.StartDate || date > w.EndDate ||
			hasNegative(row.Input, row.Output, row.Read, row.Write) ||
			row.Standard != nil && dashboardAmount(*row.Standard) == nil ||
			row.Actual != nil && dashboardAmount(*row.Actual) == nil {
			return out, dashboardReadError(dashboardUsage, errDashboardFields)
		}
		row.Group.Platform = d.safeText(row.Group.Platform)
	}
	return out, nil
}

func (d dashboardSession) recent(ctx context.Context, w dashboardWindow, owner int64) ([]dashboardRecent, error) {
	data, err := d.usagePage(ctx, w, owner, 1, 5, "created_at", "desc")
	if err != nil {
		return nil, err
	}
	result := make([]dashboardRecent, 0, len(data.Items))
	for _, row := range data.Items {
		var display *int64
		if row.Input != nil && row.Output != nil && *row.Input <= math.MaxInt64-*row.Output {
			value := *row.Input + *row.Output
			display = &value
		}
		result = append(result, dashboardRecent{row.CreatedAt.In(beijing), d.safeText(row.Model), row.KeyID, display, row.metrics()})
	}
	return result, nil
}

func (d dashboardSession) ledger(ctx context.Context, w dashboardWindow, owner int64) ([]dashboardUsageRecord, error) {
	var rows []dashboardUsageRecord
	var total int
	var lastID int64
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, errors.New("读取北辰逐笔用量超时")
		}
		data, err := d.usagePage(ctx, w, owner, page, receiptPageSize, "id", "asc")
		if err != nil {
			return nil, fmt.Errorf("逐笔用量第 %d 页 %w", page, err)
		}
		if page == 1 {
			total = *data.Total
		}
		if *data.Total != total {
			return nil, errors.New("北辰逐笔用量总条数在翻页期间变化，请重新抓取")
		}
		for _, row := range data.Items {
			if row.ID <= lastID {
				return nil, errors.New("北辰逐笔用量 ID 重复或顺序变化，请重新抓取")
			}
			lastID = row.ID
		}
		rows = append(rows, data.Items...)
		if page == data.Pages {
			return rows, nil
		}
	}
}
