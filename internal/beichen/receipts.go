package beichen

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

const (
	receiptURL          = "https://beichenapi.com/api/v1/usage"
	receiptInterval     = time.Minute
	receiptSweepTimeout = 45 * time.Second
	receiptKeyTimeout   = 12 * time.Second
	receiptPageInterval = 250 * time.Millisecond
	receiptMaxPages     = 4
	receiptPageSize     = 100
	receiptMaxKeys      = 8
	receiptBalanceType  = 0 // Sub2API v0.2.15 service.BillingTypeBalance.
)

const eligibleReceiptCalls = `
FROM gateway_calls c
JOIN beichen_receipt_response_claims r ON r.response_id=c.beichen_receipt_response_id
 AND r.first_call_id=c.id AND r.conflicted_at IS NULL
JOIN beichen_receipt_active_bindings a ON a.credential_id=c.beichen_dispatch_credential_id
 AND a.upstream_key_id=c.beichen_receipt_api_key_id AND a.updated_at=c.beichen_receipt_panel_revision
WHERE c.beichen_dispatch_kind='beichen'
 AND (c.beichen_state='pending_review' OR
      (c.beichen_state='charged' AND c.created_at >= now()-interval '7 days'))
 AND NOT EXISTS (SELECT 1 FROM beichen_call_receipts linked WHERE linked.call_id=c.id)
 AND c.beichen_receipt_response_id IS NOT NULL AND c.beichen_receipt_api_key_id IS NOT NULL
 AND NOT c.beichen_receipt_conflict AND c.sent_at IS NOT NULL AND c.finalized_at IS NOT NULL`

type receiptApplier interface {
	ApplyBeichenReceipt(context.Context, uuid.UUID, usage.BeichenReceipt) (bool, error)
}

type pendingReceiptCall struct {
	id         uuid.UUID
	responseID uuid.UUID
	keyID      int64
	revision   time.Time
	model      string
	sentAt     time.Time
	finalized  time.Time
}

type receiptCursor struct {
	day    time.Time
	model  string
	callID uuid.UUID
}

type receiptProgress struct {
	cursor receiptCursor
	scan   *receiptScan
}

type receiptScan struct {
	next       receiptCursor
	keyID      int64
	revision   time.Time
	start, end time.Time
	targets    map[string]pendingReceiptCall
	matches    map[string]receiptRow
	seen       map[string]bool
	page       int
	total      int
	lastID     int64
	account    int64
	anchor     [sha256.Size]byte
}

type receiptPage struct {
	rawResponse []byte
	Code        *int `json:"code"`
	Data        struct {
		Items    []receiptRow `json:"items"`
		Total    *int         `json:"total"`
		Page     int          `json:"page"`
		PageSize int          `json:"page_size"`
		Pages    int          `json:"pages"`
	} `json:"data"`
}

type receiptRow struct {
	receiptCostDetails
	ID             int64           `json:"id"`
	UserID         int64           `json:"user_id"`
	KeyID          int64           `json:"api_key_id"`
	RequestID      string          `json:"request_id"`
	Model          string          `json:"model"`
	GroupID        *int64          `json:"group_id"`
	Cost           *json.Number    `json:"cost"`
	ActualCost     json.RawMessage `json:"actual_cost"`
	Currency       string          `json:"currency"`
	BillingType    *int            `json:"billing_type"`
	SubscriptionID *int64          `json:"subscription_id"`
	Input          *int64          `json:"input_tokens"`
	Output         *int64          `json:"output_tokens"`
	CacheRead      *int64          `json:"cache_read_tokens"`
	CacheWrite     *int64          `json:"cache_creation_tokens"`
	CreatedAt      time.Time       `json:"created_at"`
}

func (row *receiptRow) UnmarshalJSON(body []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return errReceiptPagination
	}
	for _, name := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
		"cache_write_tokens", "cache_creation_5m_tokens", "cache_creation_1h_tokens", "billing_type", "subscription_id", "group_id",
		"cost", "total_cost", "actual_cost", "input_cost", "output_cost", "cache_read_cost", "cache_creation_cost", "rate_multiplier"} {
		if raw, ok := fields[name]; ok && websiteMissing(raw) {
			fields[name] = json.RawMessage("null")
		}
	}
	if creation, write := fields["cache_creation_tokens"], fields["cache_write_tokens"]; !websiteMissing(write) {
		if !websiteMissing(creation) && string(creation) != string(write) {
			return errReceiptPagination
		}
		fields["cache_creation_tokens"] = write
	}
	if raw := fields["actual_cost"]; !websiteMissing(raw) {
		var number json.Number
		if json.Unmarshal(raw, &number) != nil || !receiptNumber.MatchString(strings.TrimPrefix(number.String(), "-")) {
			return errReceiptPagination
		}
		fields["actual_cost"] = json.RawMessage(number)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return errReceiptPagination
	}
	type receipt receiptRow
	return json.Unmarshal(normalized, (*receipt)(row))
}

// Only website billing fields are archived. Arbitrary response metadata, prompts,
// session material and raw API keys are never part of this projection.
type receiptCostDetails struct {
	InputCost      *json.Number `json:"input_cost,omitempty"`
	OutputCost     *json.Number `json:"output_cost,omitempty"`
	CacheReadCost  *json.Number `json:"cache_read_cost,omitempty"`
	CacheWriteCost *json.Number `json:"cache_creation_cost,omitempty"`
	TotalCost      *json.Number `json:"total_cost,omitempty"`
	RateMultiplier *json.Number `json:"rate_multiplier,omitempty"`
	CacheWrite5m   *int64       `json:"cache_creation_5m_tokens,omitempty"`
	CacheWrite1h   *int64       `json:"cache_creation_1h_tokens,omitempty"`
}

// RunReceipts polls independently of the price and daily usage workers.
func (s *Service) RunReceipts(ctx context.Context, ledger *usage.Store) {
	if s.db == nil || s.crypto == nil || ledger == nil {
		return
	}
	archiveDone := make(chan struct{})
	go func() {
		defer close(archiveDone)
		s.runUpstreamUsage(ctx)
	}()
	defer func() { <-archiveDone }()
	var keyCursor uuid.UUID
	progress := make(map[uuid.UUID]*receiptProgress)
	for ctx.Err() == nil {
		sweep, cancel := context.WithTimeout(ctx, receiptSweepTimeout)
		err := s.sweepReceipts(sweep, ledger, &keyCursor, progress)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("北辰逐笔核账扫描未完成")
		}
		// Delay after each sweep so a slow sweep cannot cause a burst.
		if !waitReceiptInterval(ctx, receiptInterval) {
			return
		}
	}
}

func waitReceiptInterval(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) sweepReceipts(ctx context.Context, ledger receiptApplier, keyCursor *uuid.UUID, progress map[uuid.UUID]*receiptProgress) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT credential_id FROM (
  SELECT DISTINCT c.beichen_dispatch_credential_id AS credential_id `+eligibleReceiptCalls+`
) pending
ORDER BY (credential_id>$1) DESC, credential_id LIMIT $2`, *keyCursor, receiptMaxKeys)
	if err != nil {
		return err
	}
	defer rows.Close()
	var credentials []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		credentials = append(credentials, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var failed error
	for i, credential := range credentials {
		if ctx.Err() != nil || (i > 0 && !waitReceiptInterval(ctx, receiptPageInterval)) {
			return ctx.Err()
		}
		*keyCursor = credential
		keyCtx, cancel := context.WithTimeout(ctx, receiptKeyTimeout)
		state := progress[credential]
		if state == nil {
			state = &receiptProgress{}
			progress[credential] = state
		}
		err := s.reconcileReceiptKey(keyCtx, ledger, credential, state)
		cancel()
		if err != nil {
			failed = err
		}
	}
	return failed
}

func (s *Service) pendingReceiptCalls(ctx context.Context, credential uuid.UUID, after receiptCursor) ([]pendingReceiptCall, time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH eligible AS (
  SELECT c.id, c.beichen_receipt_response_id, c.beichen_receipt_api_key_id, c.beichen_receipt_panel_revision,
    c.beichen_dispatch_model, c.sent_at, c.finalized_at,
    date_trunc('day', c.sent_at AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai' AS day_start
  `+eligibleReceiptCalls+` AND c.beichen_dispatch_credential_id=$1
), next_group AS (
  SELECT day_start, beichen_dispatch_model,
    (day_start,beichen_dispatch_model,id)>($2,$3,$4) AS after_cursor
  FROM eligible ORDER BY after_cursor DESC,day_start,beichen_dispatch_model,id LIMIT 1
)
SELECT id,beichen_receipt_response_id,beichen_receipt_api_key_id,beichen_receipt_panel_revision,
       beichen_dispatch_model,sent_at,finalized_at,day_start
FROM eligible JOIN next_group USING (day_start,beichen_dispatch_model)
WHERE NOT after_cursor OR (day_start,beichen_dispatch_model,id)>($2,$3,$4)
ORDER BY id LIMIT $5`, credential, after.day, after.model, after.callID, receiptMaxPages*receiptPageSize)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	var calls []pendingReceiptCall
	var day time.Time
	for rows.Next() {
		var call pendingReceiptCall
		if err := rows.Scan(&call.id, &call.responseID, &call.keyID, &call.revision, &call.model, &call.sentAt, &call.finalized, &day); err != nil {
			return nil, time.Time{}, err
		}
		calls = append(calls, call)
	}
	return calls, day, rows.Err()
}

func (s *Service) reconcileReceiptKey(ctx context.Context, ledger receiptApplier, credential uuid.UUID, state *receiptProgress) error {
	resuming := state.scan != nil
	if state.scan == nil {
		calls, day, err := s.pendingReceiptCalls(ctx, credential, state.cursor)
		if err != nil || len(calls) == 0 {
			return err
		}
		first, last := calls[0], calls[len(calls)-1]
		scan := &receiptScan{
			next:  receiptCursor{day: day, model: first.model, callID: last.id},
			keyID: first.keyID, revision: first.revision,
			start: first.sentAt.Add(-2 * time.Second), end: first.finalized.Add(2 * time.Minute),
			targets: make(map[string]pendingReceiptCall, len(calls)),
			matches: make(map[string]receiptRow), seen: make(map[string]bool), page: 1,
		}
		for _, call := range calls {
			if call.keyID != scan.keyID || !call.revision.Equal(scan.revision) || call.responseID == uuid.Nil {
				continue
			}
			id := "client:" + call.responseID.String()
			if _, duplicate := scan.targets[id]; duplicate {
				scan.targets[id] = pendingReceiptCall{}
				continue
			}
			scan.targets[id] = call
			if at := call.sentAt.Add(-2 * time.Second); at.Before(scan.start) {
				scan.start = at
			}
			if at := call.finalized.Add(2 * time.Minute); at.After(scan.end) {
				scan.end = at
			}
		}
		state.scan = scan
	}
	scan := state.scan
	if resuming {
		pending, err := s.hasPendingReceiptTargets(ctx, credential, scan)
		if err != nil {
			return err
		}
		if !pending {
			state.cursor, state.scan = scan.next, nil
			return nil
		}
	}
	binding, err := s.panelUsageAccess(ctx, credential)
	if err != nil {
		return err
	}
	if binding.auth.bearer == "" || binding.key != scan.keyID || !binding.revision.Equal(scan.revision) || len(scan.targets) == 0 {
		state.cursor, state.scan = scan.next, nil
		return nil
	}
	complete, err := readPanelUsage(ctx, s, &binding, func(token string) (bool, error) {
		return s.scanReceiptPages(ctx, token, scan)
	})
	if errors.Is(err, errReceiptPagination) {
		state.cursor, state.scan = scan.next, nil
	}
	if err != nil || !complete {
		return err
	}
	// A managed web session can refresh its bearer while the same account/key
	// binding remains active. Configuration changes invalidate the pinned revision.
	current, currentKey, currentRevision, err := s.PanelUsageAccess(ctx, credential)
	state.cursor, state.scan = scan.next, nil
	if err != nil || current == "" || currentKey != binding.key || !currentRevision.Equal(binding.revision) {
		return err
	}
	for id, row := range scan.matches {
		receipt, valid := row.match(scan.targets[id])
		if valid {
			if _, err := ledger.ApplyBeichenReceipt(ctx, credential, receipt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) hasPendingReceiptTargets(ctx context.Context, credential uuid.UUID, scan *receiptScan) (bool, error) {
	responses := make([]uuid.UUID, 0, len(scan.targets))
	for _, call := range scan.targets {
		if call.responseID != uuid.Nil {
			responses = append(responses, call.responseID)
		}
	}
	payload, err := json.Marshal(responses)
	if err != nil {
		return false, err
	}
	var pending bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 `+eligibleReceiptCalls+`
 AND c.beichen_dispatch_credential_id=$1 AND c.beichen_receipt_response_id IN (
   SELECT value::uuid FROM jsonb_array_elements_text($2::jsonb)
 ))`, credential, string(payload)).Scan(&pending)
	return pending, err
}

var errReceiptPagination = errors.New("北辰逐笔账单分页不一致")

// The v0.2.15 panel supports offset pagination sorted by id, with no request-id
// filter. Ascending ids let an active day grow at the end. At continuation we
// verify the previous page to detect shifted offsets before accepting new data.
func (s *Service) scanReceiptPages(ctx context.Context, token string, scan *receiptScan) (bool, error) {
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	query := url.Values{
		"api_key_id": {strconv.FormatInt(scan.keyID, 10)},
		"model":      {scan.next.model},
		"timezone":   {"Asia/Shanghai"},
		"start_date": {scan.start.In(beijing).Format(dayLayout)},
		"end_date":   {scan.end.In(beijing).Format(dayLayout)},
		"page_size":  {strconv.Itoa(receiptPageSize)},
		"sort_by":    {"id"},
		"sort_order": {"asc"},
	}
	page := scan.page
	if page > 1 {
		page--
	}
	for i := 0; i < receiptMaxPages; i, page = i+1, page+1 {
		if i > 0 && !waitReceiptInterval(ctx, receiptPageInterval) {
			return false, ctx.Err()
		}
		data, err := fetchReceiptPage(ctx, &client, token, query, page)
		if err != nil {
			return false, s.webReadError(token, err)
		}
		total := *data.Data.Total
		if total < scan.total {
			return false, errReceiptPagination
		}
		items := data.Data.Items
		// Hash only the projected fields. Changes in unrelated metadata are
		// irrelevant to the page boundary and the money evidence.
		content, err := json.Marshal(items)
		if err != nil {
			return false, errReceiptPagination
		}
		fingerprint := sha256.Sum256(content)
		if page < scan.page {
			if fingerprint != scan.anchor {
				return false, errReceiptPagination
			}
			continue
		}
		lastID, account := scan.lastID, scan.account
		for _, row := range items {
			if account == 0 {
				account = row.UserID
			}
			if row.ID <= lastID || row.UserID <= 0 || row.UserID != account || row.KeyID != scan.keyID ||
				row.Model != scan.next.model {
				return false, errReceiptPagination
			}
			lastID = row.ID
		}
		for _, row := range items {
			if _, wanted := scan.targets[row.RequestID]; !wanted {
				continue
			}
			if scan.seen[row.RequestID] {
				delete(scan.matches, row.RequestID)
				continue
			}
			scan.seen[row.RequestID] = true
			scan.matches[row.RequestID] = row
		}
		if page == data.Data.Pages {
			return true, nil
		}
		scan.lastID, scan.account = lastID, account
		scan.page, scan.total, scan.anchor = page+1, total, fingerprint
	}
	return false, nil
}

func fetchReceiptPage(ctx context.Context, client *http.Client, token string, query url.Values, page int) (receiptPage, error) {
	query.Set("page", strconv.Itoa(page))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, receiptURL+"?"+query.Encode(), nil)
	if err != nil {
		return receiptPage{}, errors.New("构建北辰逐笔查账请求失败")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	body, err := readBeichenResponse(client, req)
	if err != nil {
		return receiptPage{}, err
	}
	if _, err := panelData(body); panelAuthFailed(err) {
		return receiptPage{}, err
	}
	var envelope receiptPage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code == nil || *envelope.Code != 0 ||
		envelope.Data.Total == nil || envelope.Data.Items == nil || envelope.Data.Page != page ||
		envelope.Data.PageSize != receiptPageSize || *envelope.Data.Total < 0 {
		if _, err := panelData(body); panelAuthFailed(err) {
			return receiptPage{}, upstreamHTTPStatus(http.StatusUnauthorized)
		}
		return receiptPage{}, errReceiptPagination
	}
	data, total := envelope.Data, *envelope.Data.Total
	pages := total / receiptPageSize
	if total%receiptPageSize != 0 {
		pages++
	}
	pages = max(1, pages)
	if total == 0 && data.Pages == 0 {
		envelope.Data.Pages = 1
	}
	if envelope.Data.Pages != pages || page > pages ||
		len(data.Items) != min(receiptPageSize, total-(page-1)*receiptPageSize) {
		return receiptPage{}, errReceiptPagination
	}
	envelope.rawResponse = body
	return envelope, nil
}

func (row receiptRow) match(call pendingReceiptCall) (usage.BeichenReceipt, bool) {
	if call.responseID == uuid.Nil || row.RequestID != "client:"+call.responseID.String() ||
		row.KeyID != call.keyID || row.Model != call.model || !validModel(row.Model) ||
		row.CreatedAt.IsZero() || row.CreatedAt.Before(call.sentAt.Add(-2*time.Second)) ||
		row.CreatedAt.After(call.finalized.Add(2*time.Minute)) ||
		(row.Currency != "" && row.Currency != "USD") ||
		row.BillingType == nil || *row.BillingType != receiptBalanceType || row.SubscriptionID != nil ||
		row.Input == nil || *row.Input < 0 || row.Output == nil || *row.Output < 0 ||
		row.CacheRead == nil || *row.CacheRead < 0 || row.CacheWrite == nil || *row.CacheWrite < 0 {
		return usage.BeichenReceipt{}, false
	}
	cost, ok := receiptDecimal(json.Number(row.ActualCost))
	if !ok {
		return usage.BeichenReceipt{}, false
	}
	// The panel's actual_cost is already adjusted and denominated in Beichen
	// balance units (displayed as USD). Only the ledger converts it to credits.
	return usage.BeichenReceipt{
		ResponseID: call.responseID, RowID: row.ID, APIKeyID: row.KeyID, Model: row.Model,
		ActualCost: cost, OccurredAt: row.CreatedAt,
		InputTokens: *row.Input, OutputTokens: *row.Output, CacheReadTokens: *row.CacheRead, CacheWriteTokens: *row.CacheWrite,
	}, true
}

var receiptNumber = regexp.MustCompile(`^(0|[1-9][0-9]{0,63})(\.[0-9]{1,128})?([eE][+-]?[0-9]{1,2})?$`)

func receiptDecimal(number json.Number) (string, bool) {
	if !receiptNumber.MatchString(number.String()) {
		return "", false
	}
	value, parsed := new(big.Rat).SetString(number.String())
	if !parsed || value.Cmp(big.NewRat(1_000_000_000_000, 1)) >= 0 {
		return "", false
	}
	// A JSON decimal can be scientific notation. Format the exact rational with
	// enough places, then verify that no rounding occurred at the ledger limit.
	if value.Sign() == 0 {
		return "0", true
	}
	cost := strings.TrimRight(strings.TrimRight(value.FloatString(64), "0"), ".")
	normalized, ok := new(big.Rat).SetString(cost)
	return cost, ok && normalized.Cmp(value) == 0
}
