package beichen

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const exportFixtureLabel = "Export fixture"

const usageExportFixture = "Time,API Key Name,Model,Reasoning Effort,Inbound Endpoint,IP Address,Type,Billing Mode,Input Tokens,Output Tokens,Cache Read Tokens,Cache Creation Tokens,Rate Multiplier,Billed Cost,Original Cost,First Token (ms),Duration (ms)\n" +
	"2026-11-17T16:00:00.00001Z,Export fixture,example-a,high,/v1/responses,192.0.2.5,Stream,按量,4,2,0,2,0.25,0.00000040,0.00000400,20,65\n" +
	"2026-11-18T12:28:09.52938+08:00,Export fixture,example-a,,/v1/chat/completions,192.0.2.6,Sync,按量,8,,0,,1,0.00000120,0.00040000,,54\n" +
	"2026-11-18T23:59:59.999999999+08:00,Export fixture,example-b,,/v1/responses,192.0.2.7,Stream,按量,0,0,,,1,0.00000000,0.00060000,,12\n"

func TestUsageExportAggregatesByBeijingDayAndModel(t *testing.T) {
	input := "\ufeff" + strings.ReplaceAll(usageExportFixture, "\n", "\r\n")
	rows, label, err := parseUsageExportText(input)
	if err != nil || label != exportFixtureLabel || len(rows) != 2 {
		t.Fatalf("unexpected import: rows=%d, err=%v", len(rows), err)
	}
	first, second := rows[0], rows[1]
	if first.date != "2026-11-18" || second.date != first.date || first.model != "example-a" || second.model != "example-b" {
		t.Fatalf("unexpected day or model: %+v", rows)
	}
	checkCount(t, first.requests, 2)
	checkCount(t, first.input, 12)
	checkCount(t, first.cacheRead, 0)
	checkCount(t, first.billed, 2)
	if first.output != nil || first.cacheWrite != nil {
		t.Fatalf("partial token totals were recorded: %+v", first)
	}
	checkCount(t, second.requests, 1)
	checkCount(t, second.input, 0)
	checkCount(t, second.billed, 0)

	rows, _, err = parseUsageExportText(strings.Replace(usageExportFixture, "0.00000040", "", 1))
	if err != nil || rows[0].billed != nil {
		t.Fatalf("partial billed cost was recorded: %v", err)
	}
}

func TestUsageExportRejectsUnsafeOrIncompleteInput(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(usageExportFixture, old, new, 1) }
	header, records, _ := strings.Cut(usageExportFixture, "\n")
	firstRecord, _, _ := strings.Cut(records, "\n")
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"missing time zone", replace(".00001Z", ".00001")},
		{"invalid time zone", replace(".00001Z", ".00001+08:60")},
		{"before Beijing midnight", replace("T16:00:00.00001Z", "T15:59:59.99999Z")},
		{"after Beijing midnight", replace("2026-11-18T23:59:59.999999999+08:00", "2026-11-19T00:00:00+08:00")},
		{"empty label", replace(",Export fixture,", ", ,")},
		{"mixed labels", replace(",Export fixture,", ",Different fixture,")},
		{"negative token", replace(",4,2,0,2,", ",-1,2,0,2,")},
		{"token overflow", replace(",4,2,0,2,", ",9223372036854775808,2,0,2,")},
		{"sum overflow", replace(",4,2,0,2,", ",9223372036854775807,2,0,2,")},
		{"currency mismatch", replace("0.00000040", "0.00000040 EUR")},
		{"USD suffix", replace("0.00000040", "0.00000040 USD")},
		{"exponent notation", replace("0.00000040", "4e-7")},
		{"invalid cost", replace("0.00000040", "-0.00000040")},
		{"cost sum overflow", strings.ReplaceAll(strings.ReplaceAll(usageExportFixture, "0.00000040", "5000000000000"), "0.00000120", "5000000000000")},
		{"too many records", header + "\n" + strings.Repeat(firstRecord+"\n", maxRows+1)},
		{"too many bytes", strings.Repeat("x", maxCSVBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseUsageExportText(tc.input)
			if domain.ErrorCode(err) != domain.CodeValidation {
				t.Fatalf("expected validation error, got %v", err)
			}
			if strings.Contains(err.Error(), exportFixtureLabel) || strings.Contains(err.Error(), "Different fixture") {
				t.Fatal("validation error exposed a credential label")
			}
		})
	}
}

func TestImportUsageExportRejectsWrongCredentialBeforeWriting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, credential, actor := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockCredentialSQL)).WithArgs(provider, credential).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(credential))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label::text FROM provider_credentials WHERE id = $1")).WithArgs(credential).
		WillReturnRows(sqlmock.NewRows([]string{"label"}).AddRow("Different fixture"))
	mock.ExpectRollback()

	service := New(db, nil)
	service.now = func() time.Time { return time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC) }
	err = service.Import(context.Background(), provider, credential, actor, "usage_export", usageExportFixture)
	if domain.ErrorCode(err) != domain.CodeValidation || strings.Contains(err.Error(), exportFixtureLabel) ||
		strings.Contains(err.Error(), "Different fixture") {
		t.Fatalf("expected a redacted label mismatch error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestImportUsageExportRejectsOpenOrConfirmedDay(t *testing.T) {
	for _, tc := range []struct {
		name          string
		now           time.Time
		checkSnapshot bool
		want          string
	}{
		{"future Beijing day", time.Date(2026, 11, 17, 15, 59, 59, 0, time.UTC), false, "已结束"},
		{"still open before midnight", time.Date(2026, 11, 18, 15, 59, 59, 999999999, time.UTC), false, "已结束"},
		{"confirmed after midnight", time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC), true, "自动确认"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			provider, credential, actor := uuid.New(), uuid.New(), uuid.New()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(lockCredentialSQL)).WithArgs(provider, credential).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(credential))
			if tc.checkSnapshot {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT label::text FROM provider_credentials WHERE id = $1")).WithArgs(credential).
					WillReturnRows(sqlmock.NewRows([]string{"label"}).AddRow(exportFixtureLabel))
				mock.ExpectQuery(`SELECT EXISTS \( SELECT 1 FROM beichen_key_usage_days d JOIN provider_credentials c .+o\.website_key_id=d\.website_key_id.+d\.key_digest=sha256\(c\.credential_ciphertext\).+d\.fetched_at >=`).
					WithArgs(provider, credential, "2026-11-18").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			}
			mock.ExpectRollback()
			service := New(db, nil)
			service.now = func() time.Time { return tc.now }
			err = service.Import(context.Background(), provider, credential, actor, "usage_export", usageExportFixture)
			if domain.ErrorCode(err) != domain.CodeValidation || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected validation error %q, got %v", tc.want, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func parseUsageExportText(input string) ([]usageRow, string, error) {
	records, err := readCSV(input, usageExportHeader)
	if err != nil {
		return nil, "", err
	}
	return parseUsageExport(records)
}
