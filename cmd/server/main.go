package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	httpapi "github.com/starprince1234/Nebula-api/internal/api/http"
	"github.com/starprince1234/Nebula-api/internal/config"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/dataplane"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/cache"
	security "github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db"
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
	api := httpapi.NewServer(
		controlService, securityManager, cacheStore, gateway,
		httpapi.HealthDependencies{Database: sqlDB, Cache: cacheStore},
		httpapi.Config{
			CookieSecure:  cfg.CookieSecure(),
			RefreshTTL:    cfg.RefreshTTL,
			SSEHeartbeat:  cfg.SSEHeartbeat,
			AllowedOrigin: cfg.PublicAppURL,
		},
	)
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

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
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("Nebula server listening", "address", cfg.HTTPAddress)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownContext)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
