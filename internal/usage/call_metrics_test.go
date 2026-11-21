package usage

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestCompleteCallMetricsAppliesOnlyTheChangeInTurnTokens(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db)
	callID, turnID := uuid.New(), uuid.New()
	explicitZero, output, hit := int64(0), int64(7), int64(9)
	metrics := CallMetrics{PromptTokens: &explicitZero, CompletionTokens: &output, CacheHitTokens: &hit, State: UsageInterrupted}
	for _, old := range [][2]int64{{5, 4}, {0, 7}} {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT turn_id,COALESCE.*FOR UPDATE`).WithArgs(callID).
			WillReturnRows(sqlmock.NewRows([]string{"turn_id", "prompt", "completion", "total"}).AddRow(turnID, old[0], old[1], 12))
		mock.ExpectExec(`UPDATE gateway_calls SET upstream_response_id`).WithArgs(callID, "", int64(0), int64(7), nil, nil, int64(9), nil, nil, nil, nil, "interrupted", "", nil).
			WillReturnResult(sqlmock.NewResult(0, 1))
		if old[0] != 0 {
			mock.ExpectExec(`UPDATE gateway_call_turns SET`).WithArgs(turnID, int64(-5), int64(3), int64(0)).
				WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectCommit()
		if err := store.CompleteCallMetrics(context.Background(), callID, metrics); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeBillsTheReservedMultiplierOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db)
	sentAt := time.Date(2026, time.November, 30, 16, 0, 1, 0, time.UTC)
	reservation := Reservation{
		CallID: uuid.New(), OrganizationID: uuid.New(), ProjectID: uuid.New(), UserID: uuid.New(),
		APIKeyID: uuid.New(), ModelID: uuid.New(), Month: Month(sentAt.Add(-2 * time.Second)), MultiplierMilli: 1450,
	}
	route := CallRoute{
		ProviderID: uuid.New(), ProviderName: "test provider", CredentialID: uuid.New(),
		BindingID: uuid.New(), UpstreamModelName: "actual-model", SentAt: sentAt,
	}
	micros := int64(999999)
	metrics := CallMetrics{UpstreamCreditMicros: &micros, State: UsageReported}
	for _, affected := range []int64{1, 0} {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT beichen_state IS NOT NULL,billing_state='pending'`).WithArgs(reservation.CallID).
			WillReturnRows(sqlmock.NewRows([]string{"routed", "pending", "expired"}).AddRow(false, affected != 0, false))
		mock.ExpectExec(`UPDATE gateway_calls SET billing_state=.*sent_at=COALESCE\(\$11::timestamptz,sent_at\) WHERE id=\$7 AND billing_state='pending'`).WithArgs(
			"charged", "succeeded", "", "", route.ProviderID, route.ProviderName, reservation.CallID,
			route.CredentialID, route.BindingID, route.UpstreamModelName, sentAt,
		).WillReturnResult(sqlmock.NewResult(0, affected))
		if affected != 0 {
			mock.ExpectExec(`UPDATE project_month_credit_buckets SET pending_milli=\s*pending_milli-\$1,charged_milli=charged_milli\+\$1`).
				WithArgs(int64(1450), reservation.ProjectID, reservation.Month).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE project_member_month_credit_buckets SET pending_milli=\s*pending_milli-\$1,charged_milli=charged_milli\+\$1`).
				WithArgs(int64(1450), reservation.ProjectID, reservation.UserID, reservation.Month).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE api_key_month_credit_buckets SET pending_milli=\s*pending_milli-\$1,charged_milli=charged_milli\+\$1`).
				WithArgs(int64(1450), reservation.APIKeyID, reservation.Month).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO monthly_usage_cube`).WithArgs(
				sqlmock.AnyArg(), reservation.Month, reservation.OrganizationID, reservation.ProjectID,
				reservation.UserID, reservation.APIKeyID, reservation.ModelID, "", "", "", "", "", int64(1450), 1, 0,
			).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE monitored_inputs SET visible=true`).WithArgs(reservation.CallID).
				WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectQuery(`SELECT turn_id,COALESCE.*FOR UPDATE`).WithArgs(reservation.CallID).
			WillReturnRows(sqlmock.NewRows([]string{"turn_id", "prompt", "completion", "total"}).AddRow(nil, 0, 0, 0))
		mock.ExpectExec(`UPDATE gateway_calls SET upstream_response_id`).WithArgs(
			reservation.CallID, "", nil, nil, nil, nil, nil, nil, nil, nil, int64(999999), "reported", "", nil,
		).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := store.Finalize(context.Background(), reservation, true, "succeeded", "", "", route, metrics); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
