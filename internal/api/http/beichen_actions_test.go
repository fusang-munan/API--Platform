package httpapi

import (
	"encoding/json"
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

func beichenActionsTestRouter(server *Server, actor uuid.UUID) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(identityKey, controlplane.Identity{UserID: actor, Role: "teacher"}) })
	router.POST("/providers/:provider_id/enable-batch", server.enableBeichenModelsBatch)
	router.POST("/providers/:provider_id/probes", server.requestBeichenProbes)
	router.PATCH("/providers/:provider_id/price", server.setBeichenModelPrice)
	return router
}

func TestBeichenActionsRejectInvalidBodies(t *testing.T) {
	models := make([]string, 201)
	for i := range models {
		models[i] = "model"
	}
	tooMany, _ := json.Marshal(map[string]any{"models": models})
	service := controlplane.NewService(nil, nil, nil, nil, nil, controlplane.Config{})
	router := beichenActionsTestRouter(&Server{service: service}, uuid.New())
	for _, tc := range []struct{ name, method, suffix, body string }{
		{"空批次", "POST", "enable-batch", `{"models":[]}`},
		{"缺失批次", "POST", "enable-batch", `{}`},
		{"过大批次", "POST", "enable-batch", string(tooMany)},
		{"未知批次字段", "POST", "enable-batch", `{"models":["model"],"unexpected":true}`},
		{"未知探活字段", "POST", "probes", `{"credential":"unexpected"}`},
		{"无效模型名", "POST", "enable-batch", `{"models":[" model"]}`},
		{"未知定价字段", "PATCH", "price", `{"model":"model","mode":"follow","unexpected":true}`},
		{"未知费率字段", "PATCH", "price", `{"model":"model","mode":"manual","rates":{"input":"1","output":"1","unexpected":"1"}}`},
		{"跟随带价", "PATCH", "price", `{"model":"model","mode":"follow","rates":{"input":"1","output":"2"}}`},
		{"手动缺价", "PATCH", "price", `{"model":"model","mode":"manual"}`},
		{"缺失输出", "PATCH", "price", `{"model":"model","mode":"manual","rates":{"input":"1"}}`},
		{"数字类型", "PATCH", "price", `{"model":"model","mode":"manual","rates":{"input":1,"output":"1"}}`},
		{"未知范围", "PATCH", "price", `{"model":"model","mode":"follow","scope":"one"}`},
		{"缺少渠道", "PATCH", "price", `{"model":"model","mode":"follow","scope":"credentials","credential_ids":[]}`},
		{"无效渠道", "PATCH", "price", `{"model":"model","mode":"follow","scope":"credentials","credential_ids":["invalid"]}`},
		{"空渠道 ID", "PATCH", "price", `{"model":"model","mode":"follow","scope":"credentials","credential_ids":[null]}`},
		{"指定渠道与全渠道冲突", "PATCH", "price", `{"model":"model","mode":"follow","scope":"all","credential_ids":["00000000-0000-4000-8000-000000000001"]}`},
		{"尾随对象", "PATCH", "price", `{"model":"model","mode":"follow"}{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, "/providers/"+uuid.NewString()+"/"+tc.suffix, strings.NewReader(tc.body)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("无效请求未拒绝: %d, %s", w.Code, w.Body.String())
			}
		})
	}
	for _, rate := range []string{"", "-1", "-0", "+1", "1e2", "1/2", "NaN", ".1", "1.", " 1", "1.2.3", "0.0000001", "1000000.000001", strings.Repeat("9", 65)} {
		body, _ := json.Marshal(map[string]any{"model": "model", "mode": "manual", "rates": map[string]string{"input": rate, "output": "0"}})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("PATCH", "/providers/"+uuid.NewString()+"/price", strings.NewReader(string(body))))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法十进制 %q 未拒绝: %d, %s", rate, w.Code, w.Body.String())
		}
	}
}

func TestBeichenModelPriceHandlerScalesDecimalsExactly(t *testing.T) {
	for _, tc := range []struct {
		name, mode, jsonRates      string
		input, output, read, write any
		scoped                     bool
	}{
		{"精确小数及缺省缓存", "manual", `,"rates":{"input":"1.234567","output":"0.000001"}`, int64(1234567), int64(1), int64(0), int64(0), false},
		{"边界与缓存价格", "manual", `,"rates":{"input":"1000000","output":"0","cache_read":"0.000001","cache_write":"12.5000000"}`, int64(1_000_000_000_000), int64(0), int64(1), int64(12500000), false},
		{"恢复选中渠道跟随", "follow", "", nil, nil, nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			provider, actor, credential := uuid.New(), uuid.New(), uuid.New()
			at := time.Now()
			lookup := `SELECT k.model_id::text,c.label::text`
			var jsonScope string
			mock.ExpectBegin()
			if tc.scoped {
				lookup += `.*AND c.status='active'`
				jsonScope = `,"scope":"credentials","credential_ids":["` + credential.String() + `"]`
			} else {
				mock.ExpectQuery(`SELECT c.id,c.label::text`).WithArgs(provider, "model").
					WillReturnRows(sqlmock.NewRows([]string{"id", "label"}).AddRow(credential, "G3"))
			}
			mock.ExpectQuery(`SELECT c.id FROM provider_credentials c JOIN providers p`).WithArgs(provider, credential).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(credential))
			mock.ExpectQuery(lookup).WithArgs(provider, credential, "model").
				WillReturnRows(sqlmock.NewRows([]string{"model", "label"}).AddRow("model", "G3"))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()")).
				WillReturnRows(sqlmock.NewRows([]string{"time"}).AddRow(at))
			mock.ExpectExec(`INSERT INTO beichen_price_versions`).
				WithArgs(credential, "model", "school", tc.mode, "teacher", actor, tc.input, tc.output, tc.read, tc.write, at).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			service := controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})
			router := beichenActionsTestRouter(&Server{service: service}, actor)
			w := httptest.NewRecorder()
			body := `{"model":"model","mode":"` + tc.mode + `"` + tc.jsonRates + jsonScope + `}`
			router.ServeHTTP(w, httptest.NewRequest("PATCH", "/providers/"+provider.String()+"/price", strings.NewReader(body)))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"saved":true`) ||
				!strings.Contains(w.Body.String(), `"credential_id":"`+credential.String()+`"`) || !strings.Contains(w.Body.String(), `"label":"G3"`) ||
				!strings.Contains(w.Body.String(), `"applied_credential_ids":["`+credential.String()+`"]`) {
				t.Fatalf("响应契约错误: %d, %s", w.Code, w.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBeichenActionsNotFoundAndBatchEnvelope(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, actor := uuid.New(), uuid.New()
	service := controlplane.NewService(nil, db, nil, nil, nil, controlplane.Config{})
	router := beichenActionsTestRouter(&Server{service: service}, actor)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c.id,c.label::text`).WithArgs(provider, "absent").WillReturnRows(sqlmock.NewRows([]string{"id", "label"}))
	mock.ExpectRollback()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("PATCH", "/providers/"+provider.String()+"/price", strings.NewReader(`{"model":"absent","mode":"follow"}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("缺少凭据时应返回 404: %d, %s", w.Code, w.Body.String())
	}
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM providers p`).WithArgs(provider).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT k.model_id::text,k.raw_entry`).WithArgs(provider, "absent").WillReturnRows(sqlmock.NewRows([]string{"model"}))
	mock.ExpectRollback()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/providers/"+provider.String()+"/enable-batch", strings.NewReader(`{"models":["absent","ABSENT"]}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":[]`) || !strings.Contains(w.Body.String(), `"reason":"not_found"`) || strings.Count(w.Body.String(), `"model":`) != 1 {
		t.Fatalf("批量跳过响应错误: %d, %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
