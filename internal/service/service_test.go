package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
)

func setupTestService(t *testing.T) (*AppService, func()) {
	t.Helper()
	tempDir := t.TempDir()
	mainDBPath := filepath.Join(tempDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		t.Fatalf("open main db: %v", err)
	}
	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatalf("apply main migrations: %v", err)
	}

	budgetsDir := filepath.Join(tempDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		t.Fatalf("tenant db mgr: %v", err)
	}

	c := clock.NewSystemClock(time.UTC)
	svc := NewAppService(mainDB, tenantMgr, c)

	cleanup := func() {
		tenantMgr.CloseAll()
		mainDB.Close()
	}
	return svc, cleanup
}

func TestAppService_UserAndBudgetLifecyle(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	firstAction := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	userID := domain.NewUUIDv7()

	// 1. Create User
	createdUserID, err := svc.CreateUser(ctx, CreateUserInput{
		ID:            userID,
		Email:         "testuser@example.com",
		Password:      "StrongPassword123!",
		DisplayName:   "Test User",
		FirstActionAt: &firstAction,
	})
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	if createdUserID != userID {
		t.Fatalf("expected userID %s, got %s", userID, createdUserID)
	}

	// 2. Budget cannot be older than owner
	olderBudgetAction := firstAction.Add(-24 * time.Hour)
	_, err = svc.CreateBudget(ctx, CreateBudgetInput{
		Name:          "Old Budget",
		OwnerUserID:   userID,
		FirstActionAt: &olderBudgetAction,
	})
	if err == nil {
		t.Fatalf("expected error when budget is older than owner user, got nil")
	}

	// 3. Create Budget with valid date
	budgetAction := firstAction.Add(1 * time.Hour)
	budgetID := domain.NewUUIDv7()
	bID, err := svc.CreateBudget(ctx, CreateBudgetInput{
		ID:            budgetID,
		Name:          "Family Budget",
		OwnerUserID:   userID,
		FirstActionAt: &budgetAction,
	})
	if err != nil {
		t.Fatalf("create budget failed: %v", err)
	}
	if bID != budgetID {
		t.Fatalf("expected budgetID %s, got %s", budgetID, bID)
	}

	// Verify owner
	isOwner, err := svc.CheckOwnerMembership(ctx, budgetID, userID)
	if err != nil || !isOwner {
		t.Fatalf("expected user to be owner: %v, %v", isOwner, err)
	}

	// 4. Load Fixtures - validation error if < 100 days
	lastTxTooShort := budgetAction.AddDate(0, 0, 50)
	err = svc.LoadFixtures(ctx, LoadFixturesInput{
		BudgetID:        budgetID,
		UserID:          userID,
		FirstActionAt:   budgetAction,
		LastTransaction: lastTxTooShort,
	})
	if err == nil {
		t.Fatalf("expected error when days diff < 100, got nil")
	}

	// 5. Load Fixtures with 120 days
	lastTxValid := budgetAction.AddDate(0, 0, 120)
	err = svc.LoadFixtures(ctx, LoadFixturesInput{
		BudgetID:        budgetID,
		UserID:          userID,
		FirstActionAt:   budgetAction,
		LastTransaction: lastTxValid,
	})
	if err != nil {
		t.Fatalf("load fixtures failed: %v", err)
	}

	// Verify 4 accounts created
	tenantDB, err := svc.tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		t.Fatalf("get tenant db: %v", err)
	}
	var acctCount int
	err = tenantDB.QueryRow("SELECT count(*) FROM accounts_current WHERE party_id IN (SELECT id FROM parties_current WHERE is_user_owned = 1)").Scan(&acctCount)
	if err != nil || acctCount != 4 {
		t.Fatalf("expected 4 user accounts, got %d (err: %v)", acctCount, err)
	}

	// Verify buckets created >= 15
	var bucketCount int
	err = tenantDB.QueryRow("SELECT count(*) FROM buckets_projection WHERE is_archived = 0").Scan(&bucketCount)
	if err != nil || bucketCount < 15 {
		t.Fatalf("expected at least 15 buckets, got %d (err: %v)", bucketCount, err)
	}

	// Verify transactions generated
	var txCount int
	err = tenantDB.QueryRow("SELECT count(*) FROM budget_transactions_current").Scan(&txCount)
	if err != nil || txCount < 50 {
		t.Fatalf("expected rich transactions, got %d (err: %v)", txCount, err)
	}

	// 6. Remove Budget
	if err := svc.RemoveBudget(ctx, budgetID); err != nil {
		t.Fatalf("remove budget failed: %v", err)
	}

	// 7. Remove User
	if err := svc.RemoveUser(ctx, userID); err != nil {
		t.Fatalf("remove user failed: %v", err)
	}
}
