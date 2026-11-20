package beichen

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func TestReportPeriodBeijingBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, now, value, start, end string
	}{
		{"午夜前", "2026-11-19T15:59:59Z", "day", "2026-11-18T16:00:00Z", "2026-11-19T16:00:00Z"},
		{"午夜后", "2026-11-19T16:00:00Z", "day", "2026-11-19T16:00:00Z", "2026-11-20T16:00:00Z"},
		{"跨月", "2026-11-30T16:00:00Z", "month", "2026-11-30T16:00:00Z", "2026-12-31T16:00:00Z"},
		{"闰年", "2024-02-28T16:30:00Z", "month", "2024-01-31T16:00:00Z", "2024-02-29T16:00:00Z"},
		{"总计", "2026-11-19T16:00:00.123456Z", "all", "", "2026-11-19T16:00:00.123456Z"},
		{"默认", "2026-11-19T16:00:00Z", "", "", "2026-11-19T16:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339Nano, tc.now)
			got, err := reportPeriod(tc.value, now)
			if err != nil || got.Timezone != "Asia/Shanghai" || got.End.Format(time.RFC3339Nano) != tc.end {
				t.Fatalf("period = %+v, err = %v", got, err)
			}
			if tc.start == "" {
				if got.Start != nil || got.Range != "all" {
					t.Fatalf("总计应没有下界: %+v", got)
				}
			} else if got.Start == nil || got.Start.Format(time.RFC3339Nano) != tc.start {
				t.Fatalf("start = %v, want %s", got.Start, tc.start)
			}
		})
	}
	for _, value := range []string{"week", "ALL", " day", "all "} {
		if _, err := reportPeriod(value, time.Now()); domain.ErrorCode(err) != domain.CodeValidation {
			t.Errorf("range %q: %v", value, err)
		}
	}
}

func TestUsageReportRejectsInvalidInputBeforeDatabase(t *testing.T) {
	s := New(nil, nil)
	if _, err := s.ModelUsage(context.Background(), nil, "week", time.Now()); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatal(err)
	}
	id := uuid.Nil
	if _, err := s.ModelUsage(context.Background(), &id, "all", time.Now()); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatal(err)
	}
	if _, err := s.Reconciliation(context.Background(), uuid.New(), "week", time.Now()); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatal(err)
	}
	if _, err := s.Reconciliation(context.Background(), uuid.Nil, "all", time.Now()); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatal(err)
	}
}

func TestUsageReportProviderValidationAndRollback(t *testing.T) {
	for _, reconciliation := range []bool{false, true} {
		t.Run(map[bool]string{false: "模型用量", true: "区间对账"}[reconciliation], func(t *testing.T) {
			for _, dbErr := range []error{sql.ErrNoRows, errors.New("fixture database unavailable")} {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				provider := uuid.New()
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta("SELECT p.id FROM providers p WHERE p.id = $1 AND " + beichenProvider)).
					WithArgs(provider).WillReturnError(dbErr)
				mock.ExpectRollback()
				s := New(db, nil)
				if reconciliation {
					_, err = s.Reconciliation(context.Background(), provider, "all", time.Now())
				} else {
					_, err = s.ModelUsage(context.Background(), &provider, "all", time.Now())
				}
				want := domain.CodeDependencyUnavailable
				if errors.Is(dbErr, sql.ErrNoRows) {
					want = domain.CodeNotFound
				}
				if domain.ErrorCode(err) != want {
					t.Fatalf("error = %v, want %s", err, want)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestModelUsageCommitsReadSnapshotAndPreservesDecimals(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, beijing)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(modelUsageSQL)).WithArgs(nil, nil, now.UTC()).
		WillReturnRows(sqlmock.NewRows([]string{"report"}).AddRow(`{"totals":{"credits":"0.00000000000000000001","tokens":7,"calls":2,"pending_calls":1,"students":1,"keys":1},"models":[],"students":[],"keys":[]}`))
	mock.ExpectCommit()
	got, err := New(db, nil).ModelUsage(context.Background(), nil, "", now)
	if err != nil || got.Totals.Credits != "0.00000000000000000001" || got.Range != "all" || got.Start != nil ||
		got.Models == nil || got.Students == nil || got.Keys == nil {
		t.Fatalf("report = %+v, err = %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
