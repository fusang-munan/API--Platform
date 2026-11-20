package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
)

func TestSchoolWorkspaceOverviewVisibility(t *testing.T) {
	id, other := uuid.New(), uuid.New()
	const application = "https://workspace.school.example/apps/beichen"
	view := beichen.Overview{WebLoginRequired: true}
	for _, tc := range []struct {
		name, configID, link, role, scheme string
		user                               uuid.UUID
		wantField, wantLink                bool
	}{
		{"unconfigured", "", "", "teacher", "https", id, false, false},
		{"waiting for workspace", id.String(), "", "teacher", "https", id, true, false},
		{"designated teacher", id.String(), application, "teacher", "https", id, true, true},
		{"other teacher", id.String(), application, "teacher", "https", other, false, false},
		{"student", id.String(), application, "student", "https", id, false, false},
		{"mentor", id.String(), application, "mentor", "https", id, false, false},
		{"forwarded header alone", id.String(), application, "teacher", "http", id, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, err := ParseSchoolWorkspace(tc.configID, tc.link, "https://school.example")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, tc.scheme+"://school.example/", nil)
			req.Header.Set("X-Forwarded-Proto", "https")
			principal := controlplane.Identity{UserID: tc.user, Role: tc.role}
			payload, err := json.Marshal(ws.overview(view, principal, req))
			if err != nil {
				t.Fatal(err)
			}
			body := string(payload)
			if strings.Contains(body, `"maintenance_workspace"`) != tc.wantField ||
				strings.Contains(body, application) != tc.wantLink || strings.Contains(body, id.String()) {
				t.Fatal("incorrect workspace disclosure")
			}
			if !strings.Contains(body, `"web_login_required":true`) || !strings.Contains(body, `"website_sync"`) {
				t.Fatal("workspace configuration changed the upstream login or sync evidence")
			}
		})
	}
}

func TestSchoolWorkspaceInvalidSettings(t *testing.T) {
	const sensitive = "fixture-private-value"
	id := uuid.New().String()
	for _, tc := range []struct{ teacher, url string }{
		{"", "https://workspace.example/app"},
		{uuid.Nil.String(), "https://workspace.example/app"},
		{"invalid-id", ""},
		{id, "https://user:" + sensitive + "@workspace.example/app"},
		{id, "https://workspace.example/app?ticket=" + sensitive},
		{id, "https://workspace.example/#" + sensitive},
		{id, "https://SCHOOL.EXAMPLE.:8443/apps/browser"},
		{id, "http://workspace.example/app"},
		{id, "//workspace.example/app"},
		{id, "https://workspace.example:65536/app"},
		{id, "https://workspace.example/app\\redirect"},
	} {
		ws, err := ParseSchoolWorkspace(tc.teacher, tc.url, "https://school.example")
		if err == nil || ws != (SchoolWorkspace{}) || strings.Contains(err.Error(), sensitive) {
			t.Fatal("invalid settings must disable the workspace without disclosing the supplied value")
		}
	}
}

func TestBeichenOverviewPreventsSharedCaching(t *testing.T) {
	router := gin.New()
	server := &Server{}
	router.GET("/:provider_id/beichen", server.beichenOverview)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/invalid/beichen", nil))
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unexpected status or cache policy: %d %q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
}
