package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
)

func TestTeacherMemberQuotaRequiresAuthentication(t *testing.T) {
	s := NewServer(nil, nil, nil, nil, HealthDependencies{}, Config{})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPatch,
		"/api/v1/teacher/projects/018a0000-0000-7000-8000-000000000001/members/018a0000-0000-7000-8000-000000000002/monthly-credit-quota", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestTeacherMemberQuotaTopUpRejectsOutdatedAmount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	projectID, studentID, teacherID := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT true FROM projects p.*FOR UPDATE OF p`).
		WithArgs(projectID, teacherID, studentID).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
	mock.ExpectQuery(`SELECT monthly_credit_quota_milli,teacher_quota_assigned FROM project_members.*FOR UPDATE`).
		WithArgs(projectID, studentID).WillReturnRows(sqlmock.NewRows([]string{"quota", "teacher_assigned"}).AddRow(120000, true))
	mock.ExpectRollback()

	s := &Server{service: controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(identityKey, controlplane.Identity{UserID: teacherID, Role: "teacher"}) })
	router.PATCH("/projects/:project_id/members/:user_id/monthly-credit-quota", s.updateTeacherMemberQuota)
	req := httptest.NewRequest(http.MethodPatch, "/projects/"+projectID.String()+"/members/"+studentID.String()+"/monthly-credit-quota",
		strings.NewReader(`{"monthly_credits":"110","increase_only":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"INVALID_STATE_TRANSITION"`) {
		t.Fatalf("expected conflict for a stale top-up, got %d %s", w.Code, w.Body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
