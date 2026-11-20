package usage

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestStudentRecentCallsOnlyReadsOwnCallsAndPaginates(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	student := uuid.New()
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, shanghai).UTC()
	now := start.Add(3 * time.Hour)
	rows := sqlmock.NewRows([]string{
		"id", "request_id", "model_id", "model_name", "project_name",
		"prompt_tokens", "completion_tokens", "cache_hit_tokens", "cache_write_tokens",
		"credits", "billing_state", "status", "created_at",
	}).
		AddRow(uuid.New(), "req-3", "gpt-5", "GPT-5", "项目一", 120, 30, int64(80), int64(10), "1.250", "charged", "succeeded", now).
		AddRow(uuid.New(), "req-2", "claude", "Claude", "项目一", nil, nil, nil, nil, "0.000", "pending_review", "pending_review", now.Add(-time.Minute)).
		AddRow(uuid.New(), "req-1", "gpt-5", "GPT-5", "项目一", 10, 2, int64(0), int64(0), "0.100", "charged", "succeeded", now.Add(-2*time.Minute))

	mock.ExpectQuery(regexp.QuoteMeta(callDisplayCreditsSQL)+`,`+regexp.QuoteMeta(callDisplayBillingSQL)+`,`+regexp.QuoteMeta(callDisplayOutcomeSQL)).
		WithArgs(student, start, 3).
		WillReturnRows(rows)

	page, err := NewStore(database).StudentRecentCalls(context.Background(), student, &start, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil || page.Items[0].RequestID != "req-3" || page.Items[1].Status != "pending_review" {
		t.Fatalf("unexpected page: %#v", page)
	}
	if page.Items[0].InputTokens == nil || *page.Items[0].InputTokens != 120 || page.Items[0].CacheWriteTokens == nil || *page.Items[0].CacheWriteTokens != 10 {
		t.Fatalf("token fields were not preserved: %#v", page.Items[0])
	}
	if page.Items[1].InputTokens != nil {
		t.Fatalf("null tokens should stay null: %#v", page.Items[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStudentRecentStartUsesShanghaiBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 21, 16, 30, 0, 0, time.UTC)
	today, err := StudentRecentStart("today", now)
	if err != nil || today == nil || today.Format(time.RFC3339) != "2026-11-21T16:00:00Z" {
		t.Fatalf("today start = %v, %v", today, err)
	}
	month, err := StudentRecentStart("month", now)
	if err != nil || month == nil || month.Format(time.RFC3339) != "2026-10-31T16:00:00Z" {
		t.Fatalf("month start = %v, %v", month, err)
	}
	all, err := StudentRecentStart("all", now)
	if err != nil || all != nil {
		t.Fatalf("all start = %v, %v", all, err)
	}
	if _, err := StudentRecentStart("week", now); err == nil {
		t.Fatal("invalid range must fail")
	}
}
