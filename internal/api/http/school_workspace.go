package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
)

// The school supplies a link to its separately authenticated remote application.
// The API only advertises that link; the application enforces its own access.
type SchoolWorkspace struct {
	maintainer uuid.UUID
	launchURL  string
}

func ParseSchoolWorkspace(teacher, link, schoolOrigin string) (SchoolWorkspace, error) {
	teacher, link = strings.TrimSpace(teacher), strings.TrimSpace(link)
	if teacher == "" && link == "" {
		return SchoolWorkspace{}, nil
	}
	id, err := uuid.Parse(teacher)
	if err != nil || id == uuid.Nil {
		return SchoolWorkspace{}, errors.New("school workspace requires a designated teacher UUID")
	}
	if link == "" {
		return SchoolWorkspace{maintainer: id}, nil
	}
	invalid := errors.New("school workspace requires a fixed HTTPS application URL without credentials, query or fragment")
	dest, err := url.Parse(link)
	if err != nil || dest.Scheme != "https" || dest.Hostname() == "" || dest.User != nil ||
		len(link) > 2048 || strings.ContainsAny(link, "\\?#\r\n\t") {
		return SchoolWorkspace{}, invalid
	}
	if port := dest.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return SchoolWorkspace{}, invalid
		}
	}
	// Host-only school cookies are shared across ports on the same host.
	school, err := url.Parse(schoolOrigin)
	if err != nil || school.Scheme != "https" || school.Hostname() == "" ||
		strings.EqualFold(strings.TrimSuffix(dest.Hostname(), "."), strings.TrimSuffix(school.Hostname(), ".")) {
		return SchoolWorkspace{}, errors.New("school workspace requires a separate HTTPS hostname from the school website")
	}
	return SchoolWorkspace{maintainer: id, launchURL: dest.String()}, nil
}

type schoolWorkspaceLink struct {
	LaunchURL string `json:"launch_url,omitempty"`
}

type teacherBeichenOverview struct {
	beichen.Overview
	MaintenanceWorkspace *schoolWorkspaceLink `json:"maintenance_workspace,omitempty"`
}

func (w SchoolWorkspace) overview(view beichen.Overview, who controlplane.Identity, req *http.Request) teacherBeichenOverview {
	result := teacherBeichenOverview{Overview: view}
	if req.TLS != nil && w.maintainer != uuid.Nil && who.Role == "teacher" && who.UserID == w.maintainer {
		result.MaintenanceWorkspace = &schoolWorkspaceLink{LaunchURL: w.launchURL}
	}
	return result
}
