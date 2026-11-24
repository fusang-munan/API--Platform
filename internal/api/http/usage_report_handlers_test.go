package httpapi

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
)

func reportHandlerRouter(s *Server, role string) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(identityKey, controlplane.Identity{UserID: uuid.New(), Role: role}) })
	teacher := router.Group("/api/v1/teacher", privateNoStore, requireRole("teacher"))
	teacher.POST("/providers/:provider_id/beichen/pending-audit", s.beichenPendingAudit)
	teacher.POST("/providers/:provider_id/beichen/resolve-terminal-pending", s.beichenResolveTerminalPending)
	teacher.GET("/model-usage", s.teacherModelUsage)
	teacher.GET("/providers/:provider_id/beichen/reconciliation", s.beichenReconciliation)
	teacher.GET("/providers/:provider_id/beichen/reconciliation/calls", s.beichenReconciliationCalls)
	return router
}

func TestUsageReportHandlersRejectRangeAndInvalidIDs(t *testing.T) {
	router := reportHandlerRouter(&Server{}, "teacher")
	for _, path := range []string{
		"/api/v1/teacher/model-usage?range=week",
		"/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen/reconciliation?range=ALL",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"code":"VALIDATION_ERROR"`) ||
			!strings.Contains(w.Body.String(), `"field":"range"`) || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body)
		}
	}
	for _, path := range []string{
		"/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen/reconciliation?range=all&unmatched_scope=all",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"unmatched_scope"`) ||
			w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body)
		}
	}
	for _, path := range []string{
		"/api/v1/teacher/model-usage?provider_id=invalid",
		"/api/v1/teacher/providers/invalid/beichen/reconciliation",
		"/api/v1/teacher/providers/invalid/beichen/reconciliation/calls",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d", path, w.Code)
		}
	}
}

func TestUsageReportHandlersRequireTeacherRole(t *testing.T) {
	for _, role := range []string{"student", "mentor"} {
		router := reportHandlerRouter(&Server{}, role)
		for _, path := range []string{
			"/api/v1/teacher/model-usage",
			"/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen/reconciliation",
		} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"FORBIDDEN"`) {
				t.Fatalf("%s %s: status=%d body=%s", role, path, w.Code, w.Body)
			}
		}
	}
}

func TestUsageReportHandlersReturn404ForUnknownOrNonBeichenProvider(t *testing.T) {
	for _, resource := range []string{"不存在", "非北辰"} {
		t.Run(resource, func(t *testing.T) {
			for _, path := range []string{
				"/api/v1/teacher/model-usage?provider_id=00000000-0000-4000-8000-000000000001",
				"/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen/reconciliation",
			} {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				mock.ExpectBegin()
				mock.ExpectQuery(`SELECT p.id FROM providers p WHERE p.id = \$1 AND lower\(btrim\(p.base_url\)\) IN`).
					WithArgs(uuid.MustParse("00000000-0000-4000-8000-000000000001")).WillReturnError(sql.ErrNoRows)
				mock.ExpectRollback()
				router := reportHandlerRouter(&Server{service: controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})}, "teacher")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"RESOURCE_NOT_FOUND"`) {
					t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestModelUsageHandlerDefaultsToAllAndReturnsEnvelope(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH attributed AS`).WithArgs(nil, nil, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"report"}).
		AddRow(`{"totals":{"credits":"1.00000000000000001","tokens":20,"calls":2,"cache_hit_rate":0.25,"tokens_missing":7,"completeness":"partial"},
		"models":[{"model":"model","cache_hit_rate":null,"tokens_missing":7,"completeness":"missing"}],
		"students":[{"cache_hit_rate":0,"tokens_missing":0,"completeness":"complete"}],"keys":[]}`))
	mock.ExpectCommit()
	router := reportHandlerRouter(&Server{service: controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})}, "teacher")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/teacher/model-usage", nil))
	for _, part := range []string{`"data":`, `"range":"all"`, `"start":null`, `"timezone":"Asia/Shanghai"`, `"credits":"1.00000000000000001"`, `"tokens":20`,
		`"cache_hit_rate":0.25,"tokens_missing":7,"completeness":"partial"`,
		`"cache_hit_rate":null,"tokens_missing":7,"completeness":"missing"`, `"cache_hit_rate":0,"tokens_missing":0,"completeness":"complete"`} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), part) {
			t.Fatalf("missing %s: status=%d body=%s", part, w.Code, w.Body)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTerminalPendingRequiresTeacherConfirmation(t *testing.T) {
	const path = "/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen/resolve-terminal-pending"
	t.Setenv("NEBULA_TERMINAL_PENDING_POLICY", "hold")
	for _, tc := range []struct {
		role, body string
		status     int
	}{
		{"student", `{"confirm":true}`, 403}, {"mentor", `{"confirm":true}`, 403},
		{"teacher", `{}`, 400}, {"teacher", `{"confirm":false}`, 400}, {"teacher", `{"confirm":true}`, 200},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if tc.status == 200 {
			mock.ExpectQuery("SELECT p.id FROM providers").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("00000000-0000-4000-8000-000000000001"))
		}
		router := reportHandlerRouter(&Server{service: controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})}, tc.role)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(tc.body)))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s %s: %d %s", tc.role, tc.body, w.Code, w.Body)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPendingAuditResponseCachePolicy(t *testing.T) {
	const path = "/api/v1/teacher/providers/00000000-0000-4000-8000-000000000001/beichen/pending-audit"
	for _, tc := range []struct {
		role, body string
		status     int
	}{
		{"student", `{"apply":false}`, 403}, {"student", `{"apply":true}`, 403},
		{"mentor", `{"apply":false}`, 403}, {"mentor", `{"apply":true}`, 403},
		{"teacher", `{"apply":false}`, 200}, {"teacher", `{"apply":true,"preview_token":"stale"}`, 409},
		{"teacher", `{"offset":-1}`, 422},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if tc.status == 200 || tc.status == 409 {
			mock.ExpectQuery("SELECT p.id FROM providers").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("00000000-0000-4000-8000-000000000001"))
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT id,beichen_review_reason").WillReturnRows(sqlmock.NewRows([]string{"id", "terminal_missing"}))
			mock.ExpectCommit()
		}
		router := reportHandlerRouter(&Server{service: controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})}, tc.role)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(tc.body)))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" || (tc.status == 409 && !strings.Contains(w.Body.String(), "pending_audit_changed")) {
			t.Fatalf("%s %s: HTTP %d headers=%v body=%s", tc.role, tc.body, w.Code, w.Header(), w.Body)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}
