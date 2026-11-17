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

	mock.ExpectBegin()
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

func TestResolveTurnUsesPreviousResponseSibling(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT turn_id,call_index FROM gateway_calls WHERE upstream_response_id=$1 AND api_key_id=$2 AND turn_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`)).
		WithArgs("resp_prev", keyID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT c\.turn_id, MAX\(c\.call_index\)`).
		WithArgs(keyID, "resp_prev").
		WillReturnRows(sqlmock.NewRows([]string{"turn_id", "max"}).AddRow(parentTurn, 4))

	turnID, callIndex, createTurn, err := resolveTurn(context.Background(), tx, keyID, CallCorrelation{PreviousResponseID: "resp_prev"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if createTurn || turnID != parentTurn || callIndex != 5 {
		t.Fatalf("turn=%s index=%d create=%v", turnID, callIndex, createTurn)
	}
}

func TestResolveTurnCreatesNewTurnWithoutParent(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	keyID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT turn_id,call_index FROM gateway_calls WHERE upstream_response_id=$1 AND api_key_id=$2 AND turn_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`)).
		WithArgs("resp_missing", keyID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT c\.turn_id, MAX\(c\.call_index\)`).
		WithArgs(keyID, "resp_missing").
		WillReturnError(sql.ErrNoRows)

	turnID, callIndex, createTurn, err := resolveTurn(context.Background(), tx, keyID, CallCorrelation{PreviousResponseID: "resp_missing"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !createTurn || callIndex != 1 || turnID == uuid.Nil {
		t.Fatalf("expected new turn, got turn=%s index=%d create=%v", turnID, callIndex, createTurn)
	}
}

func TestResolveTurnBreaksPreviousChainOnNewUserPrompt(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT turn_id,call_index FROM gateway_calls WHERE upstream_response_id=$1 AND api_key_id=$2 AND turn_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`)).
		WithArgs("resp_prev", keyID).
		WillReturnRows(sqlmock.NewRows([]string{"turn_id", "call_index"}).AddRow(parentTurn, 2))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT root_prompt FROM gateway_call_turns WHERE id=$1`)).
		WithArgs(parentTurn).
		WillReturnRows(sqlmock.NewRows([]string{"root_prompt"}).AddRow("first user task"))
	mock.ExpectQuery(`SELECT c\.turn_id, MAX\(c\.call_index\)`).
		WithArgs(keyID, "resp_prev").
		WillReturnError(sql.ErrNoRows)
	modelID := uuid.New()
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "task-key-round-two", modelID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "task-key-round-two", modelID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "task-key-round-two").
		WillReturnError(sql.ErrNoRows)

	turnID, callIndex, createTurn, err := resolveTurn(context.Background(), tx, keyID, CallCorrelation{
		PreviousResponseID: "resp_prev",
		TaskKey:            "task-key-round-two",
		ModelID:            modelID,
	}, "second user task")
	if err != nil {
		t.Fatal(err)
	}
	if !createTurn || callIndex != 1 || turnID == uuid.Nil {
		t.Fatalf("expected new turn for new user prompt, got turn=%s index=%d create=%v", turnID, callIndex, createTurn)
	}
}

func TestResolveTurnBySessionPrefix(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	modelID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT t.id, COALESCE(MAX(c.call_index), 0)
FROM gateway_call_turns t
LEFT JOIN gateway_calls c ON c.turn_id = t.id
WHERE t.api_key_id=$1 AND t.task_key=$2`)).
		WithArgs(keyID, "cc:session-1", modelID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "cc:session-1", modelID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "max"}).AddRow(parentTurn, 2))

	turnID, callIndex, ok, err := resolveTurnByTaskKey(context.Background(), tx, keyID, modelID, "cc:session-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || turnID != parentTurn || callIndex != 3 {
		t.Fatalf("turn=%s index=%d ok=%v", turnID, callIndex, ok)
	}
}

func TestCodexThreadReplayJoinsWithoutHumanText(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	modelID := uuid.New()
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "codex-thread:0123456789abcdef", modelID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "codex-thread", modelID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "max"}).AddRow(parentTurn, 4))

	turnID, callIndex, ok, err := resolveTurnByTaskKey(context.Background(), tx, keyID, modelID, "codex-thread:0123456789abcdef", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || turnID != parentTurn || callIndex != 5 {
		t.Fatalf("turn=%s index=%d ok=%v", turnID, callIndex, ok)
	}
}

func TestSameThreadKeepsLaterTextOnTheExistingRow(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	modelID := uuid.New()
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "cc:session-1:abc123", modelID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "cc:session-1", modelID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "max"}).AddRow(parentTurn, 4))

	turnID, callIndex, ok, err := resolveTurnByTaskKey(context.Background(), tx, keyID, modelID, "cc:session-1:abc123", "另一句新问题", QuestionContinue)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || turnID != parentTurn || callIndex != 5 {
		t.Fatalf("later text left the thread: turn=%s index=%d ok=%v", turnID, callIndex, ok)
	}
}

func TestResolveTurnGroupsByTaskKey(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	modelID := uuid.New()
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, "codex-session-abc", modelID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "max"}).AddRow(parentTurn, 2))

	turnID, callIndex, createTurn, err := resolveTurn(context.Background(), tx, keyID, CallCorrelation{TaskKey: "codex-session-abc", ModelID: modelID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if createTurn || turnID != parentTurn || callIndex != 3 {
		t.Fatalf("turn=%s index=%d create=%v", turnID, callIndex, createTurn)
	}
}

func TestModelSwitchWithoutNewPromptJoinsThePromptRow(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	proModel := uuid.New()
	taskKey := "cc:session-1"
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, taskKey, proModel).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, taskKey, proModel).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, taskKey).
		WillReturnRows(sqlmock.NewRows([]string{"id", "max"}).AddRow(parentTurn, 6))

	turnID, callIndex, ok, err := resolveTurnByTaskKey(context.Background(), tx, keyID, proModel, taskKey, "", QuestionContinue)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || turnID != parentTurn || callIndex != 7 {
		t.Fatalf("model switch left the prompt row: turn=%s index=%d ok=%v", turnID, callIndex, ok)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestModelSwitchWithNewQuestionOpensARow(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	proModel := uuid.New()
	mock.ExpectQuery(`reply_closed`).
		WithArgs(keyID, "cc:session-1", proModel).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`reply_closed`).
		WithArgs(keyID, "cc:session-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "max", "root", "reply_closed", "idle"}).AddRow(parentTurn, 4, "上一问", true, false))

	_, _, ok, err := resolveTurnByTaskKey(context.Background(), tx, keyID, proModel, "cc:session-1:abcdef0123456789", "下一问", QuestionNew)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a new question after a model switch stayed on the old prompt")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTurnShowsFailureByRatioOrLatency(t *testing.T) {
	if turnShowsFailure(665, 30, 9, false) {
		t.Fatal("a few fast stream drops should not fail the task")
	}
	if turnShowsFailure(398, 5, 65, false) {
		t.Fatal("a low failure share should not fail the task")
	}
	if !turnShowsFailure(8, 3, 2, false) {
		t.Fatal("failures above one fifth should fail the task")
	}
	if !turnShowsFailure(10, 0, 10*60, false) {
		t.Fatal("a ten-minute call should fail the task")
	}
	if !turnShowsFailure(10, 0, 1, true) {
		t.Fatal("a call still running after ten minutes should fail the task")
	}
	if turnShowsFailure(1, 0, 4*60, false) {
		t.Fatal("a four-minute success is not an outrageous delay")
	}
	if !turnShowsFailure(0, 6, 2, false) {
		t.Fatal("a task with no success should fail")
	}
}

func TestSeparatesQuestionUsesFinishFlagBeforeIdle(t *testing.T) {
	if !separatesQuestion(QuestionNew, "下一问", "上一问", sql.NullBool{}, false) {
		t.Fatal("a finished reply should split without waiting")
	}
	if separatesQuestion(QuestionUnknown, "下一问", "上一问", sql.NullBool{Bool: false, Valid: true}, true) {
		t.Fatal("an open tool should stay one row across a long wait")
	}
	if !separatesQuestion(QuestionUnknown, "下一问", "上一问", sql.NullBool{Bool: true, Valid: true}, false) {
		t.Fatal("a stored end flag should split")
	}
	if separatesQuestion(QuestionUnknown, "下一问", "上一问", sql.NullBool{}, false) {
		t.Fatal("a short gap without a flag should stay")
	}
	if !separatesQuestion(QuestionUnknown, "下一问", "上一问", sql.NullBool{}, true) {
		t.Fatal("a long gap without a flag should split")
	}
	if separatesQuestion(QuestionUnknown, "上一问", "上一问", sql.NullBool{}, true) {
		t.Fatal("the same text after a gap should stay")
	}
	if separatesQuestion(QuestionContinue, "下一问", "上一问", sql.NullBool{Bool: true, Valid: true}, true) {
		t.Fatal("a tool continuation should not split")
	}
	if separatesQuestion(QuestionNew, "下一问", "", sql.NullBool{}, false) {
		t.Fatal("an empty title should be filled")
	}
}

func TestFinishedReplyOpensTheNextQuestion(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	modelID := uuid.New()
	mock.ExpectQuery(`reply_closed`).
		WithArgs(keyID, "cc:session-1", modelID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "max", "root", "reply_closed", "idle"}).AddRow(parentTurn, 4, "找论文", nil, false))

	_, _, ok, err := resolveTurnByTaskKey(context.Background(), tx, keyID, modelID, "cc:session-1:abcdef0123456789", "下一问", QuestionNew)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("finished reply kept the next question on the old row")
	}
}

func TestOpenToolFlagKeepsTheQuestionAcrossALongWait(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	parentTurn := uuid.MustParse("018f5a2a-0000-7000-8000-000000000099")
	keyID := uuid.New()
	modelID := uuid.New()
	taskKey := "cc:session-1:abcdef0123456789"
	mock.ExpectQuery(`SELECT t\.id, COALESCE\(MAX\(c\.call_index\), 0\)`).
		WithArgs(keyID, taskKey, modelID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`reply_closed`).
		WithArgs(keyID, "cc:session-1", modelID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "max", "root", "reply_closed", "idle"}).AddRow(parentTurn, 6, "等四张卡", false, true))

	turnID, callIndex, ok, err := resolveTurnByTaskKey(context.Background(), tx, keyID, modelID, taskKey, "等四张卡的后续说明", QuestionUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || turnID != parentTurn || callIndex != 7 {
		t.Fatalf("open tool split the question: turn=%s index=%d ok=%v", turnID, callIndex, ok)
	}
}
