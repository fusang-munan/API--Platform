package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/starprince1234/Nebula-api/internal/config"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "report changes without committing")
	flag.Parse()

	databaseURL, err := config.LoadDatabaseURL()
	if err != nil {
		slog.Error("load database url", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client, sqlDB, err := db.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	stats, err := usage.NewStore(sqlDB).RepairCallTurnHistory(ctx, *dryRun)
	if err != nil {
		slog.Error("repair call turns", "error", err)
		os.Exit(1)
	}
	slog.Info("repair call turns finished",
		"dry_run", *dryRun,
		"task_keys_backfilled", stats.TaskKeysBackfilled,
		"prompts_fixed", stats.PromptsFixed,
		"turn_groups_merged", stats.TurnsMerged,
		"turns_removed", stats.TurnsRemoved,
	)
}
