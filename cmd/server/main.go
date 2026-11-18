package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/config"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/cache"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/labtls"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/mail"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	startupContext, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startupCancel()

	dbClient, sqlDB, err := db.Open(startupContext, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer dbClient.Close()
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	cacheStore, err := cache.New(cfg.RedisURL, cfg.SSEStreamMaxLength)
	if err != nil {
		return err
	}
	defer cacheStore.Close()
	if err := cacheStore.Ping(startupContext); err != nil {
		return err
	}

	securityManager, err := security.NewManager(
		cfg.JWTSigningKey,
		cfg.AuthPepper,
		cfg.APIKeyPepper,
		cfg.ProviderEncryptionKey,
		cfg.AccessTTL,
	)
	if err != nil {
		return err
	}
	var mailer mail.Sender
	switch cfg.SMTPMode {
	case "log":
		mailer = mail.NewLogSender()
		slog.Warn("SMTP_MODE=log: verification emails are written to server logs only")
		if cfg.ExposeVerificationCodeInResponse {
			slog.Warn("EXPOSE_VERIFICATION_CODE_IN_RESPONSE=true: registration codes are returned in API responses (demo only)")
		}
	default:
		smtpMailer, err := mail.NewSMTP(
			cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass,
			cfg.SMTPFrom, cfg.SMTPFromName, cfg.SMTPTLSMode, cfg.SMTPTimeout,
		)
		if err != nil {
			return err
		}
		mailer = smtpMailer
	}
	controlService := controlplane.NewService(
		dbClient, sqlDB, cacheStore, securityManager, mailer,
		controlplane.Config{
			AccessTTL: cfg.AccessTTL, RefreshTTL: cfg.RefreshTTL,
			VerificationTTL: cfg.VerificationTTL, SendCooldown: cfg.SendCooldown,
			MaxAttempts: cfg.MaxAttempts, Lockout: cfg.Lockout,
			InvitationTTL: cfg.InvitationTTL, PublicAppURL: cfg.PublicAppURL,
			ExposeVerificationCodeInResponse: cfg.ExposeVerificationCodeInResponse,
		},
	)
	if err := controlService.Beichen().ConfigurePanel(os.Getenv("BEICHEN_PANEL_TOKEN"), os.Getenv("BEICHEN_PANEL_GROUPS")); err != nil {
		return err
	}
	if err := controlService.BootstrapTeacher(
		startupContext,
		cfg.BootstrapTeacherName,
		cfg.BootstrapTeacherEmail,
		cfg.BootstrapTeacherPassword,
	); err != nil {
		return err
	}
	bootstrapTestAccounts := make([]controlplane.BootstrapAccount, 0, len(cfg.BootstrapTestAccounts))
	for _, account := range cfg.BootstrapTestAccounts {
		bootstrapTestAccounts = append(bootstrapTestAccounts, controlplane.BootstrapAccount{
			Role: account.Role, Name: account.Name, Email: account.Email, Password: account.Password,
		})
	}
	if err := controlService.BootstrapTestAccounts(startupContext, bootstrapTestAccounts); err != nil {
		return err
	}

	usageStore := usage.NewStore(sqlDB)
	gateway := dataplane.NewGateway(dbClient, usageStore, cacheStore, securityManager, dataplane.Config{
		ConnectTimeout:        cfg.UpstreamConnectTimeout,
		ResponseHeaderTimeout: cfg.UpstreamResponseHeaderTimeout,
		MaxRequestBytes:       cfg.GatewayMaxRequestBytes,
		ResourceRouteTTL:      cfg.GatewayResourceRouteTTL,
		AllowedOrigin:         cfg.PublicAppURL,
	})
	newServer := func(addr, origin string, secure bool) *http.Server {
		api := httpapi.NewServer(controlService, securityManager, cacheStore, gateway,
			httpapi.HealthDependencies{Database: sqlDB, Cache: cacheStore},
			httpapi.Config{CookieSecure: secure, RefreshTTL: cfg.RefreshTTL, SSEHeartbeat: cfg.SSEHeartbeat, AllowedOrigin: origin},
		)
		return &http.Server{
			Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20,
		}
	}
	httpServer := newServer(cfg.HTTPAddress, cfg.PublicAppURL, cfg.CookieSecure())

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	syncContext, cancelSync := context.WithCancel(signalContext)
	syncDone := make(chan struct{})
	receiptsDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		controlService.Beichen().Run(syncContext)
	}()
	go func() {
		defer close(receiptsDone)
		controlService.Beichen().RunReceipts(syncContext, usageStore)
	}()
	defer func() {
		cancelSync()
		<-syncDone
		<-receiptsDone
	}()
	var secureServer *http.Server
	if addr := os.Getenv("LAB_MAINTENANCE_TLS_ADDR"); addr != "" {
		origin := os.Getenv("LAB_MAINTENANCE_TLS_ORIGIN")
		secureServer = newServer(addr, origin, true)
		cert, err := labtls.LoadOrCreate(os.Getenv("LAB_MAINTENANCE_TLS_DIR"), origin)
		if err == nil {
			var listener net.Listener
			listener, err = net.Listen("tcp", addr)
			if err == nil {
				secureServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
				fingerprint := labtls.Fingerprint(cert)
				var notice http.Handler
				if cfg.SMTPMode == "smtp" {
					release := os.Getenv("LAB_MAINTENANCE_RELEASE")
					var stopNotice func()
					notice, stopNotice = newCertificateNotice(signalContext, release, origin,
						os.Getenv("LAB_CERTIFICATE_NOTICE_REQUEST_SHA256"), os.Getenv("LAB_CERTIFICATE_NOTICE_EXPIRES_AT"),
						func(ctx context.Context, digest []byte) error {
							return controlService.SendCertificateNotice(ctx, digest, release, origin, fingerprint)
						})
					defer stopNotice()
				}
				secureServer.Handler = maintenanceHandler(secureServer.Handler, cfg.SMTPMode == "smtp", notice)
				slog.Info("maintenance HTTPS certificate", "origin", origin, "sha256", fingerprint)
				go func() {
					if err := secureServer.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
						slog.Error("maintenance HTTPS unavailable", "error", err)
					}
				}()
			}
		}
		if err != nil {
			slog.Error("maintenance HTTPS unavailable", "error", err)
		}
		defer secureServer.Close()
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("Nebula server listening", "address", cfg.HTTPAddress)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if secureServer == nil {
			return httpServer.Shutdown(shutdownContext)
		}
		tlsStopped := make(chan error, 1)
		go func() { tlsStopped <- secureServer.Shutdown(shutdownContext) }()
		return errors.Join(httpServer.Shutdown(shutdownContext), <-tlsStopped)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func maintenanceHandler(next http.Handler, mailEnabled bool, notice http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if notice != nil && r.URL.Path == certificateNoticePath {
			notice.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/.well-known/nebula-maintenance" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			if mailEnabled {
				_, _ = w.Write([]byte(`{"email_delivery":"smtp_configured"}`))
			} else {
				_, _ = w.Write([]byte(`{"email_delivery":"log_only"}`))
			}
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") && !strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
