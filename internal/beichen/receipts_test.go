package beichen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type receiptTestTransport func(*http.Request) (*http.Response, error)

func (f receiptTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type receiptTestLedger struct {
	calls []usage.BeichenReceipt
}

func (l *receiptTestLedger) ApplyBeichenReceipt(_ context.Context, _ uuid.UUID, receipt usage.BeichenReceipt) (bool, error) {
	l.calls = append(l.calls, receipt)
	return true, nil
}

func receiptFixture(now time.Time, id int64, requestID string) receiptRow {
	zero, input, output := int64(0), int64(129), int64(5)
	balance := 0
	return receiptRow{
		ID: id, UserID: 17, KeyID: 42, Model: "model", RequestID: requestID,
		ActualCost: json.RawMessage("1.435200000000001e-4"), BillingType: &balance, CreatedAt: now,
		Input: &input, Output: &output, CacheRead: &zero, CacheWrite: &zero,
	}
}

func receiptListResponse(t *testing.T, req *http.Request, entries []receiptRow) (*http.Response, error) {
	t.Helper()
	q := req.URL.Query()
	if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "beichenapi.com" ||
		req.URL.Path != "/api/v1/usage" || req.Header.Get("Authorization") != "Bearer panel-fixture" ||
		q.Get("api_key_id") != "42" || q.Get("model") != "model" || q.Get("sort_by") != "id" ||
		q.Get("sort_order") != "asc" || q.Get("start_date") != "2026-11-18" || q.Get("end_date") != "2026-11-18" {
		t.Fatal("unexpected panel request")
	}
	page, err := strconv.Atoi(q.Get("page"))
	if err != nil || page < 1 {
		t.Fatal("missing page")
	}
	total := len(entries)
	start := min((page-1)*receiptPageSize, total)
	end := min(start+receiptPageSize, total)
	raw, err := json.Marshal(map[string]any{
		"code": 0, "data": map[string]any{
			"items": entries[start:end], "total": total, "page": page, "page_size": receiptPageSize,
			"pages": max(1, (total+receiptPageSize-1)/receiptPageSize),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw))}, nil
}

func TestReceiptWorkerContinuesBusyDayAndRejectsLateDuplicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := bytes.Repeat([]byte{7}, 32)
	manager, err := security.NewManager(key, key, key, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, manager)
	credential, response, duplicate := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	day := now.In(beijing).Truncate(24 * time.Hour)
	encrypted, err := manager.EncryptCredential(panelCredentialPrefix(credential) + "panel-fixture")
	if err != nil {
		t.Fatal(err)
	}
	expectAccess := func() {
		mock.ExpectQuery("SELECT a.token_ciphertext, a.upstream_key_id, a.updated_at").WithArgs(credential).
			WillReturnRows(sqlmock.NewRows([]string{"token", "key", "revision"}).AddRow(encrypted, 42, now))
	}
	mock.ExpectQuery("WITH eligible AS").WithArgs(credential, time.Time{}, "", uuid.Nil, 400).
		WillReturnRows(sqlmock.NewRows([]string{"id", "response", "key", "revision", "model", "sent", "finalized", "day"}).
			AddRow(uuid.New(), response, 42, now, "model", now, now, day).
			AddRow(uuid.New(), duplicate, 42, now, "model", now, now, day))
	expectAccess()
	mock.ExpectQuery("SELECT EXISTS.*jsonb_array_elements_text").WithArgs(credential, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	expectAccess()
	expectAccess()
	entries := make([]receiptRow, 502)
	for i := range entries {
		entries[i] = receiptFixture(now, int64(i+1), "")
	}
	entries[1].RequestID = "client:" + duplicate.String()
	entries[450].RequestID = "client:" + response.String()
	entries[501].RequestID = "client:" + duplicate.String()
	var pages []string
	s.client.Transport = receiptTestTransport(func(r *http.Request) (*http.Response, error) {
		pages = append(pages, r.URL.Query().Get("page"))
		return receiptListResponse(t, r, entries)
	})
	ledger, state := &receiptTestLedger{}, &receiptProgress{}
	if err := s.reconcileReceiptKey(context.Background(), ledger, credential, state); err != nil ||
		len(ledger.calls) != 0 || state.scan == nil || state.scan.page != 5 {
		t.Fatalf("did not pause at page 5 without applying: %v", err)
	}
	if err := s.reconcileReceiptKey(context.Background(), ledger, credential, state); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1", "2", "3", "4", "4", "5", "6"}; !reflect.DeepEqual(pages, want) {
		t.Fatalf("page traversal = %v, want %v", pages, want)
	}
	if len(ledger.calls) != 1 || ledger.calls[0].ResponseID != response ||
		ledger.calls[0].ActualCost != "0.0001435200000000001" || state.scan != nil {
		t.Fatalf("unexpected applied receipts: %+v", ledger.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptWorkerNoPendingDoesNotFetch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, nil)
	s.client.Transport = receiptTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("queried the panel without a pending call")
		return nil, nil
	})
	mock.ExpectQuery("SELECT credential_id FROM").WithArgs(uuid.Nil, receiptMaxKeys).
		WillReturnRows(sqlmock.NewRows([]string{"credential_id"}))
	if err := s.sweepReceipts(context.Background(), &receiptTestLedger{}, new(uuid.UUID), make(map[uuid.UUID]*receiptProgress)); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptRequiresCompleteUnambiguousBalance(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	call := pendingReceiptCall{responseID: uuid.New(), keyID: 42, model: "model", sentAt: now, finalized: now}
	subscription, subscriptionKey := 1, int64(43)
	tests := map[string]func(*receiptRow){
		"bare UUID":         func(r *receiptRow) { r.RequestID = call.responseID.String() },
		"wrong key":         func(r *receiptRow) { r.KeyID++ },
		"wrong model":       func(r *receiptRow) { r.Model = "other" },
		"missing cost":      func(r *receiptRow) { r.ActualCost = nil },
		"quoted cost":       func(r *receiptRow) { r.ActualCost = json.RawMessage(`"0.1"`) },
		"missing cache":     func(r *receiptRow) { r.CacheWrite = nil },
		"wrong currency":    func(r *receiptRow) { r.Currency = "CNY" },
		"wrong time":        func(r *receiptRow) { r.CreatedAt = now.Add(3 * time.Minute) },
		"missing bill type": func(r *receiptRow) { r.BillingType = nil },
		"subscription":      func(r *receiptRow) { r.BillingType, r.SubscriptionID = &subscription, &subscriptionKey },
		"balance conflict":  func(r *receiptRow) { r.SubscriptionID = &subscriptionKey },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			row := receiptFixture(now, 1, "client:"+call.responseID.String())
			mutate(&row)
			if _, ok := row.match(call); ok {
				t.Fatal("applied an unconfirmed receipt")
			}
		})
	}
}

func TestReceiptWorkerDoesNotFollowRedirect(t *testing.T) {
	s := New(nil, nil)
	s.client.CheckRedirect = nil
	s.client.Transport = receiptTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "beichenapi.com" {
			t.Fatal("followed redirect with the panel credential")
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://example.invalid"}},
			Body: io.NopCloser(strings.NewReader("")),
		}, nil
	})
	complete, err := s.scanReceiptPages(context.Background(), "panel-fixture", &receiptScan{keyID: 42, page: 1})
	if complete || err == nil || !strings.Contains(err.Error(), fmt.Sprint(http.StatusTemporaryRedirect)) {
		t.Fatalf("unexpected redirect result: complete=%t error=%v", complete, err)
	}
}
