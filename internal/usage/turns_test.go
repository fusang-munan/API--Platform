package usage

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestResolveTurnUsesPreviousResponseChain(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT turn_id,call_index FROM gateway_calls WHERE upstream_response_id=$1 AND api_key_id=$2 AND turn_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`)).
		WithArgs("resp_prev", keyID).
		WillReturnRows(sqlmock.NewRows([]string{"turn_id", "call_index"}).AddRow(parentTurn, 2))

	turnID, callIndex, createTurn, err := resolveTurn(context.Background(), tx, keyID, CallCorrelation{PreviousResponseID: "resp_prev"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if createTurn || turnID != parentTurn || callIndex != 3 {
		t.Fatalf("turn=%s index=%d create=%v", turnID, callIndex, createTurn)
	}
}

func TestResolveTurnCreatesNewTurnWithoutParent(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	keyID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT turn_id,call_index FROM gateway_calls WHERE upstream_response_id=$1 AND api_key_id=$2 AND turn_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`)).
		WithArgs("resp_missing", keyID).
		WillReturnError(sql.ErrNoRows)

	turnID, callIndex, createTurn, err := resolveTurn(context.Background(), tx, keyID, CallCorrelation{PreviousResponseID: "resp_missing"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !createTurn || callIndex != 1 || turnID == uuid.Nil {
		t.Fatalf("expected new turn, got turn=%s index=%d create=%v", turnID, callIndex, createTurn)
	}
}

func TestResolveTurnGroupsByTaskKey(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT c.turn_id, MAX(c.call_index)
FROM gateway_calls c
WHERE c.api_key_id=$1 AND c.task_key=$2 AND c.turn_id IS NOT NULL
  AND c.created_at > now() - interval '30 minutes'
GROUP BY c.turn_id
ORDER BY MAX(c.created_at) DESC
LIMIT 1`)).
		WithArgs(keyID, "codex-session-abc").
		WillReturnRows(sqlmock.NewRows([]string{"turn_id", "max"}).AddRow(parentTurn, 2))

	turnID, callIndex, createTurn, err := resolveTurn(context.Background(), tx, keyID, CallCorrelation{TaskKey: "codex-session-abc"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if createTurn || turnID != parentTurn || callIndex != 3 {
		t.Fatalf("turn=%s index=%d create=%v", turnID, callIndex, createTurn)
	}
}
