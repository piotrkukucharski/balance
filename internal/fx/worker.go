package fx

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/logger"
)

// RunWorkerCLI executes the worker:sync-fx CLI command with argument parsing.
func RunWorkerCLI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("worker:sync-fx", flag.ContinueOnError)
	days := fs.Int("days", 0, "Number of days of history to sync (<=0 auto-detects: 90 if empty DB, 1 if routine)")
	source := fs.String("source", "ALL", "Source filter: ALL, ECB, NBP")
	dataDir := fs.String("data-dir", os.Getenv("DATA_DIR"), "Path to data directory containing main.db")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	dir := *dataDir
	if dir == "" {
		dir = "./data"
	}

	mainDBPath := filepath.Join(dir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		return fmt.Errorf("failed opening main DB at %s: %w", mainDBPath, err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		return fmt.Errorf("failed applying migrations: %w", err)
	}

	svc := NewService(mainDB, nil, nil)
	opts := SyncOptions{
		Days:   *days,
		Source: *source,
	}

	logger.Info(ctx, "Starting exchange rate synchronization worker", "days", *days, "source", *source, "db", mainDBPath)
	synced, err := svc.Sync(ctx, opts)
	if err != nil {
		logger.Error(ctx, "Exchange rate sync worker failed", "error", err.Error())
		return err
	}

	logger.Info(ctx, "Exchange rate synchronization completed", "synced_count", synced)
	fmt.Printf("Successfully synchronized %d exchange rates from %s (days: %d)\n", synced, *source, *days)
	return nil
}
