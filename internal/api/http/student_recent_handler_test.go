package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
)

func studentRecentRouter(s *Server, userID uuid.UUID) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(identityKey, controlplane.Identity{UserID: userID, Role: "student"}) })
	router.GET("/api/v1/student/usage/recent", s.studentRecentCalls)
	return router
}

func TestStudentRecentCallsRejectsInvalidRangeAndLimit(t *testing.T) {
	router := studentRecentRouter(&Server{}, uuid.New())
	for _, path := range []string{
		"/api/v1/student/usage/recent?range=week",
		"/api/v1/student/usage/recent?range=ALL",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"range"`) ||
			w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body)
		}
	}
	for _, path := range []string{
		"/api/v1/student/usage/recent?limit=0",
		"/api/v1/student/usage/recent?limit=101",
		"/api/v1/student/usage/recent?limit=abc",
		"/api/v1/student/usage/recent?cursor=not-base64",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"VALIDATION_ERROR"`) {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body)
		}
	}
}

func TestStudentRecentCallsReturnsOwnCallFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	student := uuid.New()
	created := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT c.id,c.request_id,m.model_id,c.model_name,c.project_name")).
		WithArgs(student, sqlmock.AnyArg(), 21).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "request_id", "model_id", "model_name", "project_name",
			"prompt_tokens", "completion_tokens", "cache_hit_tokens", "cache_write_tokens",
			"credits", "billing_state", "status", "created_at",
		}).AddRow(uuid.New(), "req-1", "gpt-5", "GPT-5", "项目一", 100, 20, int64(40), int64(5),
			"1.250", "charged", "succeeded", created))

	router := studentRecentRouter(&Server{service: controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})}, student)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/student/usage/recent?range=today&limit=20", nil))
	for _, part := range []string{`"items":[`, `"request_id":"req-1"`, `"model_id":"gpt-5"`, `"input_tokens":100`,
		`"output_tokens":20`, `"cache_read_tokens":40`, `"cache_write_tokens":5`, `"credits":"1.250"`, `"status":"succeeded"`} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), part) {
			t.Fatalf("missing %s: status=%d body=%s", part, w.Code, w.Body)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
