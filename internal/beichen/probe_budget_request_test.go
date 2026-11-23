package beichen

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
)

func TestRequestProbesQueuesRefreshWhenDailyBudgetIsExhausted(t *testing.T) {
	t.Setenv("BEICHEN_PROBE_DAILY_LIMIT", "500")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager, err := security.NewManager([]byte(strings.Repeat("j", 32)), []byte(strings.Repeat("a", 32)),
		[]byte(strings.Repeat("k", 32)), []byte(strings.Repeat("e", 32)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	provider := uuid.New()
	mock.ExpectQuery(`SELECT p.id FROM providers p WHERE p.id = \$1`).WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(provider))
	mock.ExpectExec("INSERT INTO beichen_probe_jobs").WithArgs(provider).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM beichen_probe_jobs WHERE provider_id = \$1`).WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"state", "started_at", "finished_at", "last_error"}).
			AddRow("queued", nil, nil, nil))
	mock.ExpectQuery(regexp.QuoteMeta(probeBudgetUsedSQL)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(500))

	run, err := New(db, manager).RequestProbes(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "queued" || run.Budget == nil || !run.Budget.Exhausted || run.Budget.Remaining != 0 {
		t.Fatalf("refresh should queue even when no new targets fit the budget: %#v", run)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
