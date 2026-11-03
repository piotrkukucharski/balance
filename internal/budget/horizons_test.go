package budget

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
)

func TestHorizonsLogicAndWindowCalculations(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		loc = time.UTC
	}

	// 1. Test Day 10 calculations
	// Case A: Today is 2026-10-05 (before 10th) -> should be 5 days (10 - 5 = 5)
	tA := time.Date(2026, 10, 5, 12, 0, 0, 0, loc)
	daysA := DaysRemainingUntilDayOfMonth(10, tA, loc)
	if daysA != 5 {
		t.Errorf("expected 5 days until Oct 10 from Oct 5, got %d", daysA)
	}

	// Case B: Today is exactly 2026-10-10 -> should be 1 day
	tB := time.Date(2026, 10, 10, 15, 0, 0, 0, loc)
	daysB := DaysRemainingUntilDayOfMonth(10, tB, loc)
	if daysB != 1 {
		t.Errorf("expected 1 day on Oct 10, got %d", daysB)
	}

	// Case C: Today is 2026-10-11 (after 10th) -> should be days from Oct 11 to Nov 10 (30 days)
	tC := time.Date(2026, 10, 11, 8, 0, 0, 0, loc)
	daysC := DaysRemainingUntilDayOfMonth(10, tC, loc)
	if daysC != 30 {
		t.Errorf("expected 30 days until Nov 10 from Oct 11, got %d", daysC)
	}

	// 2. Test Rolling Days
	cleanID, wDays, ok := ParseHorizonIdentifier("5d", tA, loc)
	if !ok || cleanID != "5d" || wDays != 5 {
		t.Errorf("expected 5d / 5 days, got %s / %d / %v", cleanID, wDays, ok)
	}

	// 3. Test Calendar Day parsing
	cleanID, wDays, ok = ParseHorizonIdentifier("day-10", tA, loc)
	if !ok || cleanID != "day-10" || wDays != 5 {
		t.Errorf("expected day-10 / 5 days, got %s / %d / %v", cleanID, wDays, ok)
	}

	cleanID, wDays, ok = ParseHorizonIdentifier("day:15", tA, loc)
	if !ok || cleanID != "day-15" || wDays != 10 {
		t.Errorf("expected day-15 / 10 days, got %s / %d / %v", cleanID, wDays, ok)
	}
}

func TestHorizonsTenantDBOperations(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "tenant.db")

	tenantDB, err := db.OpenDB("file:" + dbPath)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer tenantDB.Close()

	if err := db.ApplyMigrations(tenantDB, "migrations/tenant"); err != nil {
		t.Fatalf("ApplyMigrations failed: %v", err)
	}

	// Check default seeded horizons
	horizons, err := ListBudgetHorizons(ctx, tenantDB, false)
	if err != nil {
		t.Fatalf("ListBudgetHorizons failed: %v", err)
	}
	if len(horizons) != 5 {
		t.Errorf("expected 5 seeded horizons, got %d", len(horizons))
	}

	userID := domain.NewUUIDv7()

	// Default user horizon should fallback to 'monthly'
	defH, err := GetUserDefaultHorizon(ctx, tenantDB, userID)
	if err != nil || defH != "monthly" {
		t.Errorf("expected default horizon monthly, got %s, err: %v", defH, err)
	}

	// Set user default to 30d
	if err := SetUserDefaultHorizon(ctx, tenantDB, userID, "30d"); err != nil {
		t.Fatalf("SetUserDefaultHorizon 30d failed: %v", err)
	}
	defH, _ = GetUserDefaultHorizon(ctx, tenantDB, userID)
	if defH != "30d" {
		t.Errorf("expected default horizon 30d, got %s", defH)
	}

	// Create custom horizon 5d
	h5d, err := CreateCustomHorizon(ctx, tenantDB, CreateCustomHorizonInput{
		HorizonType: HorizonTypeRollingDays,
		DaysValue:   5,
	})
	if err != nil || h5d.ID != "5d" {
		t.Fatalf("CreateCustomHorizon 5d failed: %v", err)
	}

	// Create custom calendar horizon day-10
	hDay10, err := CreateCustomHorizon(ctx, tenantDB, CreateCustomHorizonInput{
		HorizonType: HorizonTypeCalendarDayOfMonth,
		DaysValue:   10,
	})
	if err != nil || hDay10.ID != "day-10" {
		t.Fatalf("CreateCustomHorizon day-10 failed: %v", err)
	}

	// User sets day-10 as default
	_ = SetUserDefaultHorizon(ctx, tenantDB, userID, "day-10")

	// Attempting to delete day-10 without fallback should fail
	err = DeleteCustomHorizon(ctx, tenantDB, "day-10", "")
	if err == nil {
		t.Error("expected error deleting horizon assigned as default without fallback")
	}

	// Delete day-10 with fallback to 5d
	err = DeleteCustomHorizon(ctx, tenantDB, "day-10", "5d")
	if err != nil {
		t.Fatalf("DeleteCustomHorizon with fallback failed: %v", err)
	}

	// User's default should now be 5d
	defH, _ = GetUserDefaultHorizon(ctx, tenantDB, userID)
	if defH != "5d" {
		t.Errorf("expected user default to be reassigned to 5d, got %s", defH)
	}
}
