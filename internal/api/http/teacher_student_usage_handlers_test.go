package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
)

func TestTeacherStudentUsageRoutes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	key := make([]byte, 32)
	manager, err := security.NewManager(key, key, key, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(controlplane.NewService(client, db, nil, manager, nil, controlplane.Config{}),
		manager, nil, nil, HealthDependencies{}, Config{})
	routes := "/api/v1/teacher/usage/students"
	studentID := uuid.New()
	studentURL := routes + "/" + studentID.String()
	now := time.Now()

	signIn := func(role, status string) string {
		t.Helper()
		id := uuid.New()
		accountRole := role
		if role == "mentor" {
			accountRole = "student"
		}
		mock.ExpectQuery(`SELECT .* FROM "users"`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at", "name", "email", "password_hash", "role", "status"}).
				AddRow(id, now, now, "Fixture", "fixture@example.test", "fixture", accountRole, status))
		if role == "student" || role == "mentor" {
			mock.ExpectQuery(`SELECT .* FROM "student_profiles"`).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			profiles := sqlmock.NewRows([]string{"id"})
			if role == "mentor" {
				profiles.AddRow(uuid.New())
			}
			mock.ExpectQuery(`SELECT .* FROM "mentor_profiles"`).WillReturnRows(profiles)
		}
		token, _, err := manager.IssueAccessToken(id, role)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	get := func(path, token string, status int, snippets ...string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: status=%d headers=%v body=%s", path, w.Code, w.Header(), w.Body)
		}
		for _, snippet := range snippets {
			if !strings.Contains(w.Body.String(), snippet) {
				t.Errorf("%s: missing %q in %s", path, snippet, w.Body)
			}
		}
	}
	for _, path := range []string{routes, studentURL} {
		get(path, "", http.StatusUnauthorized, `"code":"AUTHENTICATION_REQUIRED"`)
		get(path, signIn("student", "active"), http.StatusForbidden, `"code":"FORBIDDEN"`)
		get(path, signIn("mentor", "active"), http.StatusForbidden, `"code":"FORBIDDEN"`)
		get(path, signIn("teacher", "disabled"), http.StatusForbidden, `"code":"ACCOUNT_DISABLED"`)
	}
	for _, path := range []string{
		routes + "?month=2025-13",
		routes + "?project_id=invalid",
		routes + "?page=0",
		routes + "?page_size=101",
		routes + "?search=%00",
		routes + "?sort=unknown",
		routes + "?order=unknown",
		routes + "/invalid",
		studentURL + "?project_id=invalid",
	} {
		get(path, signIn("teacher", "active"), http.StatusBadRequest, `"code":"VALIDATION_ERROR"`)
	}

	token := signIn("teacher", "active")
	start := time.Date(2024, 12, 31, 16, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 31)
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH ledger AS .*ORDER BY r\.milli DESC NULLS LAST, r\.user_id`).
		WithArgs(start, nil, "", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"total", "user_id", "name", "email", "projects", "keys", "credits", "calls", "zero",
			"tokens", "coverage", "known", "complete", "review", "review_credits", "last_call_at",
		}).AddRow(1, studentID, "Student", "student@example.test", 2, 3, "0.000000000000000001", 1, 0,
			"9007199254740993", 1, 1, 0, 0, "0", start))
	mock.ExpectQuery(`FROM beichen_upstream_usage_rows`).WithArgs(start, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"rows", "debit_rows", "debit"}).AddRow(2, 1, "0"))
	mock.ExpectCommit()
	get(routes+"?month=2025-01", token, http.StatusOK, `"request_id":`, `"data":`, `"total":1`,
		`"user_email":"student@example.test"`, `"credits":"0.000000000000000001"`, `"page_size":20`,
		`"total_tokens":"9007199254740993"`, `"completeness":"partial"`,
		`"unattributed_upstream":{"rows":2,"debit_rows":1,"balance_debit":"0"}`)
	token = signIn("teacher", "active")
	mock.ExpectQuery(`FROM rollups r`).WithArgs(end, nil, studentID).
		WillReturnRows(sqlmock.NewRows([]string{"project_id"}))
	get(studentURL+"?month=2025-02", token, http.StatusNotFound, `"code":"RESOURCE_NOT_FOUND"`)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
