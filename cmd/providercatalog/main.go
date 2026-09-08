package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/config"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/crypto"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

func main() {
	cfg, err := config.LoadProviderCatalogWorker()
	if err != nil {
		slog.Error("load provider catalog worker configuration", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client, sqlDB, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("open provider catalog database", "error", err)
		os.Exit(1)
	}
	defer client.Close()
	dummyKey := []byte("01234567890123456789012345678901")
	security, err := crypto.NewManager(dummyKey, dummyKey, dummyKey, cfg.ProviderEncryptionKey, time.Minute)
	if err != nil {
		slog.Error("create credential decryptor", "error", err)
		os.Exit(1)
	}
	runner := &catalog.ProviderSyncRunner{
		DB:      sqlDB,
		Ent:     client,
		Decrypt: security.DecryptCredential,
	}
	run := func() {
		synced, err := runner.SyncActiveProviders(ctx)
		if err != nil {
			slog.Error("provider catalog sync failed", "error", err)
			return
		}
		slog.Info("provider catalog sync completed", "providers", synced)
	}
	run()
	location, _ := time.LoadLocation("Asia/Shanghai")
	for {
		now := time.Now().In(location)
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, location)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			run()
		}
	}
}
