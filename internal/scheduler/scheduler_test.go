package scheduler_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/scheduler"
)

func TestParseMode(t *testing.T) {
	tests := []struct {
		input    string
		expected scheduler.Mode
	}{
		{"", scheduler.ModeExternal},
		{"external", scheduler.ModeExternal},
		{"EXTERNAL", scheduler.ModeExternal},
		{"internal", scheduler.ModeInternal},
		{"Internal", scheduler.ModeInternal},
		{"unknown", scheduler.ModeExternal},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := scheduler.ParseMode(tc.input)
			if got != tc.expected {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestScheduler_ExternalModeNoOp(t *testing.T) {
	ctx := context.Background()
	s, err := scheduler.New(scheduler.Config{
		Mode: scheduler.ModeExternal,
	})
	if err != nil {
		t.Fatalf("unexpected error creating scheduler: %v", err)
	}

	if err := s.Start(ctx); err != nil {
		t.Fatalf("unexpected error starting external scheduler: %v", err)
	}

	if s.IsRunning() {
		t.Fatalf("expected scheduler not to be running in external mode")
	}

	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("unexpected error shutting down external scheduler: %v", err)
	}
}

func TestScheduler_InternalModeStartsAndShutsDown(t *testing.T) {
	ctx := context.Background()
	tempDir, err := os.MkdirTemp("", "sched-test-*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mainDBPath := filepath.Join(tempDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		t.Fatalf("failed opening main db: %v", err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatalf("failed applying migrations: %v", err)
	}

	s, err := scheduler.New(scheduler.Config{
		Mode:         scheduler.ModeInternal,
		FXSyncPeriod: 50 * time.Millisecond,
		MainDB:       mainDB,
	})
	if err != nil {
		t.Fatalf("unexpected error creating scheduler: %v", err)
	}

	if err := s.Start(ctx); err != nil {
		t.Fatalf("unexpected error starting internal scheduler: %v", err)
	}

	if !s.IsRunning() {
		t.Fatalf("expected scheduler to be running in internal mode")
	}

	// Sleep briefly so immediate job runs
	time.Sleep(100 * time.Millisecond)

	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("unexpected error shutting down internal scheduler: %v", err)
	}

	if s.IsRunning() {
		t.Fatalf("expected scheduler not to be running after shutdown")
	}
}
