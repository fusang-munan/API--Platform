package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
