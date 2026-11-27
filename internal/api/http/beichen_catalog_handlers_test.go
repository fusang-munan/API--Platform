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

func catalogHandlerRouter(s *Server, actor uuid.UUID) *gin.Engine {
	router := gin.New()
	router.Use(cors("https://console.example.test"), func(c *gin.Context) {
		c.Set(identityKey, controlplane.Identity{UserID: actor, Role: "teacher"})
	})
	group := router.Group("/providers/:provider_id/beichen")
	group.GET("/aliases", s.beichenModelAliases)
	group.PUT("/aliases", s.beichenModelAliases)
	group.POST("/rebind", s.rebindBeichenModels)
	group.POST("/catalog/rescan", s.rescanBeichenCatalog)
	group.GET("/channel-switch", s.beichenChannelSwitch)
	group.POST("/channel-switch", s.confirmBeichenChannelSwitch)
	group.POST("/sync", s.syncBeichen)
	return router
}

func TestBeichenCatalogMaintenanceRejectsInvalidRequests(t *testing.T) {
	router := catalogHandlerRouter(&Server{service: controlplane.NewService(nil, nil, nil, nil, nil, controlplane.Config{})}, uuid.New())
	prefix := "/providers/" + uuid.NewString() + "/beichen"
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "/aliases", `{"upstream_model":"one","school_model":"school"}`},
		{"PUT", "/aliases", `{"credential_id":null,"upstream_model":"one"}`},
		{"PUT", "/aliases", `{"credential_id":"invalid","upstream_model":"one","school_model":"school"}`},
		{"PUT", "/aliases", `{"credential_id":null,"upstream_model":"one","school_model":42}`},
		{"PUT", "/aliases", `{"credential_id":null,"upstream_model":" one","school_model":null}`},
		{"POST", "/catalog/rescan", `{"credential_id":"invalid"}`},
		{"POST", "/catalog/rescan", `{"credential_id":"00000000-0000-4000-8000-000000000001","unexpected":true}`},
		{"POST", "/rebind", `{}`},
		{"POST", "/rebind", `{"preview":false}`},
		{"POST", "/rebind", `{"preview":false,"preview_token":"invalid"}`},
		{"POST", "/rebind?preview=other", ``},
		{"POST", "/rebind?preview=true&preview=false", ``},
		{"POST", "/rebind?preview=false", `{"preview":true}`},
		{"GET", "/channel-switch?from_credential_id=wrong", ``},
		{"GET", "/channel-switch?to_credential_id=a&to_credential_id=b", ``},
		{"POST", "/channel-switch", `{"from_credential_id":"wrong"}`},
		{"POST", "/channel-switch", `{}`},
		{"POST", "/channel-switch", `{"retire":true}`},
		{"POST", "/sync", `{"credential_id":"wrong"}`},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, prefix+tc.path, strings.NewReader(tc.body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s 无效请求状态码为 %d", tc.method, tc.path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	preflight := httptest.NewRequest(http.MethodOptions, prefix+"/aliases", nil)
	preflight.Header.Set("Origin", "https://console.example.test")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPut)
	router.ServeHTTP(w, preflight)
	if w.Code != http.StatusNoContent || !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatal("浏览器跨域预检应允许 PUT")
	}
}

func TestBeichenChannelSwitchRequiresTeacherRouteAuthentication(t *testing.T) {
	router := NewServer(nil, nil, nil, nil, HealthDependencies{}, Config{}).Handler()
	path := "/api/v1/teacher/providers/" + uuid.NewString() + "/beichen/channel-switch"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: 匿名渠道操作状态为 %d", method, w.Code)
		}
	}
}

func TestBeichenCatalogMaintenanceAliasAndPreviewContracts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, credential := uuid.New(), uuid.New()
	service := controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})
	router := catalogHandlerRouter(&Server{service: service}, uuid.New())
	prefix := "/providers/" + provider.String() + "/beichen"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT p.id FROM providers p").WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(provider))
	mock.ExpectQuery("SELECT DISTINCT ON").WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"credential_id", "upstream_model", "school_model"}).AddRow(credential, "one", "school"))
	mock.ExpectCommit()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, prefix+"/aliases", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" ||
		!strings.Contains(w.Body.String(), `"school_model":"school"`) {
		t.Fatal("别名读回响应契约错误")
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT p.id FROM providers p").WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(provider))
	mock.ExpectQuery("SELECT b.id, b.model_id").WithArgs(provider).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT model_id,adapter,max").WithArgs(provider).WillReturnRows(sqlmock.NewRows([]string{"model_id"}))
	mock.ExpectQuery("SELECT a.model,k.credential_id").WithArgs(provider).WillReturnRows(sqlmock.NewRows([]string{"model"}))
	evidence := `[{"credential_id":"` + credential.String() + `","credential_revision":"2026-11-27T07:00:00Z","group_id":10,
		"catalog_observed_at":"2026-11-27T07:01:00Z","evidence_since":"2026-11-27T07:00:00Z"}]`
	mock.ExpectQuery("SELECT p.status='active' AND COALESCE").WithArgs(provider).
		WillReturnRows(sqlmock.NewRows([]string{"ready", "versions"}).AddRow(true, []byte(evidence)))
	mock.ExpectCommit()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, prefix+"/rebind?preview=true", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"preview":true`) ||
		!strings.Contains(w.Body.String(), `"changes":[]`) || !strings.Contains(w.Body.String(), `"evidence_versions":[`) ||
		!strings.Contains(w.Body.String(), `"group_id":10,"catalog_observed_at":"2026-11-27T07:01:00Z"`) {
		t.Fatalf("绑定重建预览响应契约错误: %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
