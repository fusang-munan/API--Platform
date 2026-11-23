package usage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func TestStudentCallReceiptJSON(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db)
	student := uuid.New()
	requestID := uuid.Must(uuid.NewV7()).String()

	for _, tc := range []struct {
		name, state   string
		credits, cost *string
		applied       *bool
	}{
		{name: "pending", state: "pending_review"},
		{name: "precise", state: "charged", credits: ptrReceiptField("0.0000000000000000123456"),
			cost: ptrReceiptField("0.000000000000000000123456"), applied: ptrReceiptField(true)},
		{name: "only associated", state: "charged", credits: ptrReceiptField("0"),
			cost: ptrReceiptField("0.0000123456"), applied: ptrReceiptField(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock.ExpectQuery(studentCallReceiptSQL).WithArgs(student.String(), requestID).
				WillReturnRows(sqlmock.NewRows([]string{"request_id", "model_id", "state", "credits", "cost", "applied", "base", "source", "factor", "usage_state", "amount_source"}).
					AddRow(requestID, "public-model", tc.state, tc.credits, tc.cost, tc.applied, tc.credits, nil, "1", nil, nil))
			receipt, err := store.StudentCallReceipt(context.Background(), student, requestID)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"request_id": requestID, "model_id": "public-model", "channel": "北辰", "billing_state": tc.state,
				"charged_credits": nil, "actual_cost": nil, "receipt_applied": nil,
				"base_credits": nil, "base_source": nil, "school_multiplier": "1",
			}
			if tc.credits != nil {
				want["base_credits"] = *tc.credits
				want["charged_credits"], want["actual_cost"], want["receipt_applied"] = *tc.credits, *tc.cost, *tc.applied
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("unexpected receipt JSON: %s", body)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStudentCallReceiptRejectsInvalidIDs(t *testing.T) {
	store := &Store{}
	for _, id := range []string{"", strings.Repeat("a", 129), "a/b", "a\x00b", " a", "\xff"} {
		_, err := store.StudentCallReceipt(context.Background(), uuid.New(), id)
		if domain.ErrorCode(err) != domain.CodeNotFound {
			t.Errorf("%q: want not found, got %v", id, err)
		}
	}
}

func ptrReceiptField[T any](value T) *T { return &value }
