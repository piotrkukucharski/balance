package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
)

func TestMainDBAndTenantDBManager(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := OpenDB(mainPath)
	if err != nil {
		t.Fatalf("OpenDB main failed: %v", err)
	}
	defer mainDB.Close()

	if err := ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatalf("ApplyMigrations main failed: %v", err)
	}

	budgetsDir := filepath.Join(tmpDir, "budgets")
	mgr, err := NewTenantDBManager(budgetsDir, mainDB, 2)
	if err != nil {
		t.Fatalf("NewTenantDBManager failed: %v", err)
	}
	defer mgr.CloseAll()

	budgetID1 := domain.NewUUIDv7()
	userID := domain.NewUUIDv7()

	// Verify unauthorized initially
	ctx := context.Background()
	_, err = mgr.VerifyUserMembership(ctx, budgetID1, userID)
	if err == nil {
		t.Fatalf("expected unauthorized error for uninvited user")
	}

	// Insert user and membership in mainDB
	now := int64(1700000000000000)
	_, err = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, "user@test.com", "hash", "User Test", now, now)
	if err != nil {
		t.Fatalf("insert user failed: %v", err)
	}

	_, err = mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		budgetID1, "Family Budget", "PLN", "Europe/Warsaw", userID, now, now)
	if err != nil {
		t.Fatalf("insert budget failed: %v", err)
	}

	_, err = mainDB.Exec(`INSERT INTO budget_members (budget_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)`,
		budgetID1, userID, "OWNER", now)
	if err != nil {
		t.Fatalf("insert member failed: %v", err)
	}

	role, err := mgr.VerifyUserMembership(ctx, budgetID1, userID)
	if err != nil {
		t.Fatalf("verify membership failed: %v", err)
	}
	if role != "OWNER" {
		t.Fatalf("expected OWNER, got %s", role)
	}

	// Open tenant DB
	tenant1, err := mgr.GetTenantDB(budgetID1, true)
	if err != nil {
		t.Fatalf("GetTenantDB 1 failed: %v", err)
	}

	// Test that tenant tables exist
	var count int
	err = tenant1.QueryRow("SELECT count(*) FROM parties").Scan(&count)
	if err != nil {
		t.Fatalf("query tenant parties failed: %v", err)
	}

	// Test LRU pool eviction
	budgetID2 := domain.NewUUIDv7()
	budgetID3 := domain.NewUUIDv7()

	_, err = mgr.GetTenantDB(budgetID2, true)
	if err != nil {
		t.Fatalf("GetTenantDB 2 failed: %v", err)
	}
	_, err = mgr.GetTenantDB(budgetID3, true)
	if err != nil {
		t.Fatalf("GetTenantDB 3 failed: %v", err)
	}

	// Verify path traversal prevention
	_, err = mgr.GetTenantDB("../other", false)
	if err == nil {
		t.Fatalf("expected error on path traversal")
	}
}
