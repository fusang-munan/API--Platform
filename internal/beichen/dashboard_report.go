package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const (
	dashboardAPI   = "https://beichenapi.com/api/v1"
	dashboardStats = "/usage/dashboard/stats"
	dashboardUsage = "/usage"
	dashboardUnit  = "CNY"
)

var errDashboardFields = errors.New("北辰网页返回字段缺失或格式变化")
var dashboardSecretShape = regexp.MustCompile("sk-[A-Za-z0-9_-]{12,}|eyJ[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+")

type DashboardOptions struct {
	Range       string
	Granularity string
	Reconcile   bool
	ProviderID  uuid.UUID
}

type dashboardWindow struct {
	Range       string `json:"range"`
	Granularity string `json:"granularity"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	Timezone    string `json:"timezone"`
}

type DashboardReport struct {
	dashboardWindow
	Currency   string                `json:"currency"`
	StartedAt  time.Time             `json:"started_at"`
	CapturedAt time.Time             `json:"captured_at"`
	Dashboard  dashboardCards        `json:"dashboard"`
	Totals     dashboardMetrics      `json:"totals"`
	Platforms  dashboardPlatforms    `json:"platforms"`
	Models     []dashboardModel      `json:"models"`
	Trend      []dashboardDay        `json:"trend"`
	Recent     []dashboardRecent     `json:"recent"`
	Keys       []dashboardKey        `json:"keys"`
	KeyCheck   dashboardKeyCheck     `json:"key_reconciliation"`
	CardCheck  *dashboardTotalsCheck `json:"dashboard_total_check,omitempty"`
	Local      *dashboardLocalReport `json:"local_reconciliation,omitempty"`
}

type dashboardCards struct {
	Today      dashboardMetrics        `json:"today"`
	All        dashboardMetrics        `json:"all"`
	Balance    json.Number             `json:"balance_cny"`
	TotalKeys  int                     `json:"total_api_keys"`
	ActiveKeys int                     `json:"active_api_keys"`
	ByPlatform []dashboardPlatformCard `json:"by_platform"`
}

type dashboardModel struct {
	Model string `json:"model"`
	dashboardMetrics
}

type dashboardDay struct {
	Date string `json:"date"`
	dashboardMetrics
}

type dashboardKey struct {
	ID         int64                `json:"id"`
	Name       string               `json:"name"`
	GroupID    int64                `json:"group_id,omitempty"`
	GroupName  string               `json:"group_name,omitempty"`
	GroupRate  *json.Number         `json:"group_rate_multiplier"`
	Status     string               `json:"status"`
	Totals     dashboardMetrics     `json:"totals"`
	Models     []dashboardModel     `json:"models"`
	ModelCheck dashboardTotalsCheck `json:"model_reconciliation"`
}

type dashboardKeyCheck struct {
	ListedKeys       int  `json:"listed_keys"`
	InventoryMatches bool `json:"inventory_matches_dashboard"`
	dashboardTotalsCheck
}

type dashboardSession struct {
	service *Service
	localPanelLogin
}

func (s *Service) ReadDashboard(ctx context.Context, options DashboardOptions) (DashboardReport, error) {
	switch options.Range {
	case "today", "7d", "all":
	default:
		return DashboardReport{}, errors.New("--range 需要 today、7d 或 all")
	}
	if options.Granularity != "day" {
		return DashboardReport{}, errors.New("--granularity 目前支持 day")
	}
	if s.localLogin == nil {
		return DashboardReport{}, errLocalLoginMissing
	}
	// Read the existing sealed session. The normal crawler can refresh on
	// expiration, which would make a POST and update the session file.
	session, err := s.localLogin.load()
	if errors.Is(err, os.ErrNotExist) {
		return DashboardReport{}, errLocalLoginMissing
	}
	if err != nil {
		return DashboardReport{}, err
	}
	started := s.now().In(beijing)
	if session.expired(started) {
		return DashboardReport{}, errLocalReauthenticate
	}
	reader := dashboardSession{service: s, localPanelLogin: session}
	return reader.read(ctx, options, started)
}

func (d dashboardSession) get(ctx context.Context, path string, query url.Values, dst any) error {
	values := url.Values{"timezone": {beijing.String()}}
	for k, v := range query {
		values[k] = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dashboardAPI+path+"?"+values.Encode(), nil)
	if err != nil {
		return errors.New("无法构造北辰仪表盘请求")
	}
	req.Header.Set("Authorization", "Bearer "+d.Access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("X-User-UI-Request", "1")
	body, err := d.service.readResponse(req)
	if err != nil {
		return dashboardReadError(path, err)
	}
	defer clear(body)
	data, err := panelData(body)
	if panelAuthFailed(err) {
		return dashboardReadError(path, err)
	}
	if err != nil || len(data) == 0 || data[0] != '{' || json.Unmarshal(data, dst) != nil {
		return dashboardReadError(path, errDashboardFields)
	}
	return nil
}

func dashboardReadError(path string, err error) error {
	if panelAuthFailed(err) {
		return fmt.Errorf("%s %w（%v）", path, errLocalReauthenticate, err)
	}
	return fmt.Errorf("%s %w", path, err)
}

func (d dashboardSession) safeText(value string) string {
	secrets := []string{d.Access, d.Refresh, d.Password}
	for _, cookie := range d.Cookies {
		secrets = append(secrets, cookie.Value)
	}
	return dashboardSecretShape.ReplaceAllString(safeLocalAuthText(value, secrets...), "[已隐藏]")
}

func (d dashboardSession) read(ctx context.Context, opt DashboardOptions, started time.Time) (DashboardReport, error) {
	var account struct {
		ID        int64       `json:"id"`
		CreatedAt time.Time   `json:"created_at"`
		Balance   json.Number `json:"balance"`
	}
	if err := d.get(ctx, "/auth/me", nil, &account); err != nil {
		return DashboardReport{}, err
	}
	if account.ID <= 0 || dashboardAmount(account.Balance) == nil {
		return DashboardReport{}, dashboardReadError("/auth/me", errDashboardFields)
	}

	startDate := started
	switch opt.Range {
	case "7d":
		startDate = started.AddDate(0, 0, -6)
	case "all":
		if account.CreatedAt.IsZero() || account.CreatedAt.After(started) {
			return DashboardReport{}, dashboardReadError("/auth/me", errDashboardFields)
		}
		startDate = account.CreatedAt.In(beijing)
	}
	window := dashboardWindow{opt.Range, opt.Granularity, startDate.Format(dayLayout), started.Format(dayLayout), beijing.String()}
	out := DashboardReport{dashboardWindow: window, Currency: dashboardUnit, StartedAt: started}
	keys, err := d.service.accountKeys(ctx, d.Access)
	if err != nil {
		return DashboardReport{}, dashboardReadError("/keys", err)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })
	out.Dashboard, err = d.cards(ctx, account.Balance)
	if err != nil {
		return DashboardReport{}, err
	}
	out.Totals, err = d.stats(ctx, window.query(0))
	if err != nil {
		return DashboardReport{}, err
	}
	out.Models, err = d.models(ctx, window.query(0))
	if err != nil {
		return DashboardReport{}, err
	}
	out.Trend, err = d.trend(ctx, window)
	if err != nil {
		return DashboardReport{}, err
	}
	out.Recent, err = d.recent(ctx, window, account.ID)
	if err != nil {
		return DashboardReport{}, err
	}

	out.Keys = make([]dashboardKey, 0, len(keys))
	sums := make([]dashboardMetrics, 0, len(keys))
	for _, key := range keys {
		if key.UserID != account.ID {
			return DashboardReport{}, dashboardReadError("/keys", errDashboardFields)
		}
		item, err := d.key(ctx, window, key)
		if err != nil {
			return DashboardReport{}, err
		}
		out.Keys = append(out.Keys, item)
		sums = append(sums, item.Totals)
	}
	check, err := compareDashboardTotals(out.Totals, sums)
	if err != nil {
		return DashboardReport{}, err
	}
	out.KeyCheck = dashboardKeyCheck{len(keys), len(keys) == out.Dashboard.TotalKeys, check}

	var ledger []dashboardUsageRecord
	if opt.Range == "7d" || opt.Reconcile {
		ledger, err = d.ledger(ctx, window, account.ID)
		if err != nil {
			return DashboardReport{}, err
		}
	}
	if opt.Range == "7d" {
		out.Platforms, err = dashboardLedgerPlatforms(out.Totals, ledger)
	} else {
		card := out.Dashboard.All
		if opt.Range == "today" {
			card = out.Dashboard.Today
		}
		check, cardErr := compareDashboardTotals(out.Totals, []dashboardMetrics{card})
		if cardErr != nil {
			return DashboardReport{}, cardErr
		}
		out.CardCheck = &check
		out.Platforms, err = dashboardCardPlatforms(out.Totals, out.Dashboard.ByPlatform, opt.Range)
	}
	if err != nil {
		return DashboardReport{}, err
	}

	out.CapturedAt = d.service.now().In(beijing)
	if out.CapturedAt.Format(dayLayout) != out.EndDate {
		return DashboardReport{}, errors.New("抓取跨过北京时间午夜，请重新抓取同一账日")
	}
	if opt.Reconcile {
		out.Local, err = d.reconcile(ctx, out, opt.ProviderID, ledger)
		if err != nil {
			return DashboardReport{}, err
		}
	}
	return out, nil
}

func (w dashboardWindow) query(key int64) url.Values {
	q := url.Values{"start_date": {w.StartDate}, "end_date": {w.EndDate}}
	if key > 0 {
		q.Set("api_key_id", strconv.FormatInt(key, 10))
	}
	return q
}

func (d dashboardSession) cards(ctx context.Context, balance json.Number) (dashboardCards, error) {
	var raw map[string]json.RawMessage
	if err := d.get(ctx, dashboardStats, nil, &raw); err != nil {
		return dashboardCards{}, err
	}
	today, err := decodeDashboardMetrics(raw, "today_")
	if err != nil {
		return dashboardCards{}, dashboardReadError(dashboardStats, err)
	}
	all, err := decodeDashboardMetrics(raw, "total_")
	if err != nil {
		return dashboardCards{}, dashboardReadError(dashboardStats, err)
	}
	var keys, active *int
	var platforms []dashboardPlatformCard
	if json.Unmarshal(raw["total_api_keys"], &keys) != nil || keys == nil || *keys < 0 ||
		json.Unmarshal(raw["active_api_keys"], &active) != nil || active == nil || *active < 0 || *active > *keys ||
		json.Unmarshal(raw["by_platform"], &platforms) != nil {
		return dashboardCards{}, dashboardReadError(dashboardStats, errDashboardFields)
	}
	if platforms == nil {
		platforms = []dashboardPlatformCard{}
	}
	for i := range platforms {
		row := &platforms[i]
		row.Platform = d.safeText(row.Platform)
		if row.Platform == "" || !row.Today.valid() || !row.All.valid() {
			return dashboardCards{}, dashboardReadError(dashboardStats, errDashboardFields)
		}
	}
	return dashboardCards{Today: today, All: all, Balance: balance, TotalKeys: *keys, ActiveKeys: *active, ByPlatform: platforms}, nil
}

func (d dashboardSession) stats(ctx context.Context, query url.Values) (dashboardMetrics, error) {
	const path = "/usage/stats"
	var raw map[string]json.RawMessage
	if err := d.get(ctx, path, query, &raw); err != nil {
		return dashboardMetrics{}, err
	}
	metrics, err := decodeDashboardMetrics(raw, "total_")
	if err != nil {
		return dashboardMetrics{}, dashboardReadError(path, err)
	}
	return metrics, nil
}

func (d dashboardSession) models(ctx context.Context, query url.Values) ([]dashboardModel, error) {
	const path = "/usage/dashboard/models"
	var payload struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := d.get(ctx, path, query, &payload); err != nil {
		return nil, err
	}
	if payload.Models == nil {
		return nil, dashboardReadError(path, errDashboardFields)
	}
	result := make([]dashboardModel, 0, len(payload.Models))
	for _, row := range payload.Models {
		var model string
		metrics, err := decodeDashboardMetrics(row, "")
		if err != nil || json.Unmarshal(row["model"], &model) != nil || !validModel(model) {
			return nil, dashboardReadError(path, errDashboardFields)
		}
		result = append(result, dashboardModel{d.safeText(model), metrics})
	}
	return result, nil
}

func (d dashboardSession) trend(ctx context.Context, window dashboardWindow) ([]dashboardDay, error) {
	const path = "/usage/dashboard/trend"
	query := window.query(0)
	query.Set("granularity", window.Granularity)
	var payload struct {
		Trend []map[string]json.RawMessage `json:"trend"`
	}
	if err := d.get(ctx, path, query, &payload); err != nil {
		return nil, err
	}
	if payload.Trend == nil {
		return nil, dashboardReadError(path, errDashboardFields)
	}
	result := make([]dashboardDay, 0, len(payload.Trend))
	seen := map[string]bool{}
	for _, row := range payload.Trend {
		var date string
		metrics, err := decodeDashboardMetrics(row, "")
		if err != nil || json.Unmarshal(row["date"], &date) != nil || seen[date] {
			return nil, dashboardReadError(path, errDashboardFields)
		}
		if _, err := dayStart(date); err != nil || date < window.StartDate || date > window.EndDate {
			return nil, dashboardReadError(path, errDashboardFields)
		}
		seen[date] = true
		result = append(result, dashboardDay{date, metrics})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date < result[j].Date })
	return result, nil
}

func (d dashboardSession) key(ctx context.Context, window dashboardWindow, key listedKey) (dashboardKey, error) {
	item := dashboardKey{ID: key.ID, Name: d.safeText(key.Name), GroupID: key.GroupID, Status: d.safeText(key.Status)}
	if key.Group != nil {
		item.GroupName = d.safeText(key.Group.Name)
		if key.Group.Rate != "" {
			if rate := dashboardAmount(key.Group.Rate); rate == nil || rate.Sign() < 0 {
				return dashboardKey{}, dashboardReadError("/keys", errDashboardFields)
			}
			item.GroupRate = &key.Group.Rate
		}
	}
	query := window.query(key.ID)
	var err error
	item.Totals, err = d.stats(ctx, query)
	if err != nil {
		return dashboardKey{}, fmt.Errorf("Key %d %w", key.ID, err)
	}
	item.Models, err = d.models(ctx, query)
	if err != nil {
		return dashboardKey{}, fmt.Errorf("Key %d %w", key.ID, err)
	}
	models := make([]dashboardMetrics, 0, len(item.Models))
	for _, row := range item.Models {
		models = append(models, row.dashboardMetrics)
	}
	item.ModelCheck, err = compareDashboardTotals(item.Totals, models)
	return item, err
}
