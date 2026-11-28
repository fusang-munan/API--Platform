package beichen

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func TestRoutePriceReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, quote, code string
		fixed             bool
	}{
		{name: "tiered default mode", quote: `{"intervals":[{"min_tokens":0,"input_price":0,"output_price":1}]}`},
		{name: "incomplete tier", quote: `{"intervals":[{"min_tokens":0,"input_price":0}]}`, code: "tier_missing_output"},
		{name: "unsupported mode", quote: `{"billing_mode":"per_request","per_request_price":1}`, code: "billing_mode_non_token"},
		{name: "fixed manual price", fixed: true, quote: `{"billing_mode":"per_request","per_request_price":1}`},
		{name: "no quote", code: "missing_price"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			price := routePriceReadiness(tc.fixed, json.RawMessage(tc.quote))
			if tc.code == "" {
				if !price.ready() || price.conditional == tc.fixed {
					t.Fatalf("expected usable price: %+v", price)
				}
			} else if price.ready() || price.blocker.Code != tc.code || price.blocker.Message == "" {
				t.Fatalf("expected blocker %q, got %+v", tc.code, price.blocker)
			}
		})
	}
}

func TestEnableModelsBatchRejectsInvalidInput(t *testing.T) {
	service := New(nil, nil)
	provider, actor := uuid.New(), uuid.New()
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = "model"
	}
	for _, models := range [][]string{nil, {}, tooMany, {""}, {" model"}, {"model\n"}, {strings.Repeat("m", 257)}} {
		if _, err := service.EnableModelsBatch(context.Background(), provider, actor, models); domain.ErrorCode(err) != domain.CodeValidation {
			t.Fatalf("无效模型列表未被拒绝: %v", err)
		}
	}
	for _, ids := range [][2]uuid.UUID{{uuid.Nil, actor}, {provider, uuid.Nil}} {
		if _, err := service.EnableModelsBatch(context.Background(), ids[0], ids[1], []string{"model"}); domain.ErrorCode(err) != domain.CodeValidation {
			t.Fatalf("缺失身份未被拒绝: %v", err)
		}
	}
}

func TestEnableModelsBatchRetriesSerializationAndDeduplicates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, actor := uuid.New(), uuid.New()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM providers p`).WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT k.model_id::text,k.raw_entry`).WithArgs(provider, "Missing", nil).
		WillReturnError(&pgconn.PgError{Code: "40001"})
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT k.model_id::text,k.raw_entry`).WithArgs(provider, "Missing", nil).
		WillReturnRows(sqlmock.NewRows([]string{"model"}))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM model_bindings b`).WithArgs(provider, "Missing").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()
	result, err := New(db, nil).EnableModelsBatch(context.Background(), provider, actor, []string{"Missing", "missing"})
	if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Model != "Missing" || result.Skipped[0].Reason != "not_found" {
		t.Fatalf("重试或去重结果错误: %+v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnableModelsBatchRejectsForeignProvider(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := uuid.New()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM providers p`).WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	if _, err := New(db, nil).EnableModelsBatch(context.Background(), provider, uuid.New(), []string{"model"}); domain.ErrorCode(err) != domain.CodeNotFound {
		t.Fatalf("非北辰供应商未返回 404: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSetModelPriceRejectsInvalidRates(t *testing.T) {
	zero, negative, over := int64(0), int64(-1), maxRate+1
	for _, input := range []PriceInput{
		{Mode: "unknown"}, {Mode: "follow", Rates: &Rates{}}, {Mode: "manual"},
		{Mode: "manual", Rates: &Rates{Input: &zero}},
		{Mode: "manual", Rates: &Rates{Input: &negative, Output: &zero}},
		{Mode: "manual", Rates: &Rates{Input: &zero, Output: &over}},
		{Mode: "manual", Rates: &Rates{Input: &zero, Output: &zero, CacheRead: &negative}},
	} {
		if _, err := New(nil, nil).SetModelPrice(context.Background(), uuid.New(), uuid.New(), "model", input); domain.ErrorCode(err) != domain.CodeValidation {
			t.Fatalf("非法费率未被拒绝: %+v, %v", input, err)
		}
	}
}
