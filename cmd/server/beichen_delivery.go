package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

type beichenSettings struct {
	channels, email, password string
	session                   *schoolseal.SessionStore
	recipient                 *schoolseal.Recipient
}

func initializeBeichenDelivery(ctx context.Context, service *controlplane.Service) *schoolseal.Recipient {
	settings, err := loadBeichenSettings()
	if err != nil {
		slog.Error("北辰部署配置尚不可用", "error", err)
		// A box intended for another recipient must be replaced. An empty first
		// release, however, must not break a school's existing runtime settings.
		if schoolseal.HasRelease() {
			return settings.recipient
		}
	}
	var sessionErr error
	if settings.session != nil {
		sessionErr = service.Beichen().ConfigureSavedWebSession(settings.session)
	} else {
		sessionErr = service.Beichen().ConfigureWebSession(settings.email, settings.password)
	}
	if sessionErr != nil {
		slog.Error("北辰学校账号配置无效", "error", sessionErr)
	}
	if err := configureBeichen(ctx, service, settings.channels, os.Getenv("BEICHEN_PANEL_TOKEN"), os.Getenv("BEICHEN_PANEL_GROUPS")); err != nil {
		slog.Error("北辰渠道预配置未完成", "error", err)
	}
	return settings.recipient
}

func loadBeichenSettings() (beichenSettings, error) {
	settings := beichenSettings{channels: os.Getenv("BEICHEN_CHANNELS_JSON"), email: os.Getenv("BEICHEN_WEB_EMAIL"), password: os.Getenv("BEICHEN_WEB_PASSWORD")}
	dir := os.Getenv("LAB_BEICHEN_SEAL_DIR")
	if dir == "" {
		return settings, nil
	}
	identity, err := schoolseal.LoadOrCreate(dir)
	if err != nil {
		return settings, err
	}
	r := identity.Recipient()
	settings.recipient = &r
	sealed, err := identity.OpenRelease()
	if err != nil {
		return settings, err
	}
	if sealed != nil {
		raw, err := json.Marshal(sealed.Channels)
		if err != nil {
			return settings, errors.New("读取北辰渠道密文失败")
		}
		settings.channels = string(raw)
		if sealed.WebEmail != "" {
			settings.email, settings.password = sealed.WebEmail, sealed.WebPassword
		}
	}
	session, err := identity.OpenSessionRelease()
	if err != nil {
		return settings, err
	}
	if session != nil {
		settings.session, err = identity.SessionStore(*session)
		if err != nil {
			return settings, err
		}
	}
	return settings, nil
}

func recipientHandler(r schoolseal.Recipient, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != schoolseal.PublicPath {
			next.ServeHTTP(w, req)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(r)
		}
	})
}
