package beichen

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestDerivedPriceSuggestionReturnsNewestUsageRates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	credential := uuid.New()
	observed := time.Date(2026, 11, 21, 20, 14, 0, 0, time.FixedZone("CST", 8*3600))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT input_rate, output_rate`).
		WithArgs(credential, "claude-sonnet-5-5").
		WillReturnRows(sqlmock.NewRows([]string{"input_rate", "output_rate", "cache_read_rate", "cache_write_rate", "effective_at"}).
			AddRow(int64(4_000_000), int64(20_000_000), int64(200_000), int64(5_000_000), observed))
	mock.ExpectRollback()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}

	got := derivedPriceSuggestion(context.Background(), tx, []PreferredRoute{
		{UpstreamModel: "claude-sonnet-5-5"},
		{CredentialID: &credential, UpstreamModel: "claude-sonnet-5-5"},
	})
	if got == nil || got.InputRate != 4_000_000 || got.OutputRate != 20_000_000 ||
		got.CacheReadRate != 200_000 || got.CacheWriteRate != 5_000_000 || !got.ObservedAt.Equal(observed) ||
		got.Source != derivedPriceSource {
		t.Fatalf("unexpected suggestion: %+v", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDerivedPriceSuggestionSkipsRoutesWithoutObservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	credential := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT input_rate, output_rate`).
		WithArgs(credential, "gpt-6-astra").
		WillReturnRows(sqlmock.NewRows([]string{"input_rate", "output_rate", "cache_read_rate", "cache_write_rate", "effective_at"}))
	mock.ExpectRollback()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}

	if got := derivedPriceSuggestion(context.Background(), tx, []PreferredRoute{{CredentialID: &credential, UpstreamModel: "gpt-6-astra"}}); got != nil {
		t.Fatalf("expected no suggestion, got %+v", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
