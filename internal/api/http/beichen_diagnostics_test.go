package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
)

func TestBeichenDiagnosticsHandler(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, credential := uuid.New(), uuid.New()
	const day = "2026-01-02"
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT p.id FROM providers p`).WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(provider))
	mock.ExpectQuery(`WITH calls AS`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), provider, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"local", "upstream", "windows"}).AddRow(3, 4, `[]`))
	mock.ExpectQuery(`FROM billing_checks`).WithArgs(provider, day, "2026-01-03").
		WillReturnRows(sqlmock.NewRows([]string{"credential_id", "model", "day", "local", "upstream", "diff", "status"}).
			AddRow(credential, "fixture", day, "2", nil, nil, "missing"))
	mock.ExpectCommit()
	server := &Server{service: controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(identityKey, controlplane.Identity{UserID: uuid.New(), Role: c.GetHeader("Fixture-Role")})
	})
	router.GET("/api/v1/teacher/providers/:provider_id/beichen/diagnostics", requireRole("teacher"), server.beichenDiagnostics)
	path := "/api/v1/teacher/providers/" + provider.String() + "/beichen/diagnostics"
	for _, tc := range []struct {
		role, query string
		code        int
	}{
		{"student", "?day=" + day, http.StatusForbidden},
		{"teacher", "?day=2026-02-30", http.StatusBadRequest},
		{"teacher", "?day=" + day, http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, path+tc.query, nil)
		r.Header.Set("Fixture-Role", tc.role)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("status=%d, want %d: %s", w.Code, tc.code, w.Body.String())
		}
		if tc.code != http.StatusOK {
			continue
		}
		var result struct {
			Data beichen.BillingDiagnostics `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		got := result.Data
		if w.Header().Get("Cache-Control") != "private, no-store" || got.StartDay != day || got.EndDay != day ||
			got.LocalCalls != 3 || got.UpstreamRows != 4 || len(got.DailyChecks) != 1 ||
			got.DailyChecks[0].Status != "missing" || got.DailyChecks[0].UpstreamCredits != nil ||
			got.SuspectedBypassWindows == nil {
			t.Fatalf("unexpected diagnostics envelope: %s", w.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
