package budget

import (
	"context"
	"testing"

	"github.com/balance/balance/internal/domain"
)

func TestFirstDayOfWeekPreferences(t *testing.T) {
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	ctx := context.Background()
	userID := domain.NewUUIDv7()

	// 1. Initial lookup with no preference set -> defaults to MONDAY (or fallback if provided)
	val, err := GetUserFirstDayOfWeek(ctx, tenantDB, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != FirstDayMonday {
		t.Errorf("expected default MONDAY, got %q", val)
	}

	valFallback, err := GetUserFirstDayOfWeek(ctx, tenantDB, userID, "SUNDAY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valFallback != FirstDaySunday {
		t.Errorf("expected fallback SUNDAY, got %q", valFallback)
	}

	// 2. Set each of 7 days of the week and verify persistence
	days := []string{
		FirstDayMonday,
		FirstDayTuesday,
		FirstDayWednesday,
		FirstDayThursday,
		FirstDayFriday,
		FirstDaySaturday,
		FirstDaySunday,
	}

	for _, d := range days {
		if err := SetUserFirstDayOfWeek(ctx, tenantDB, userID, d); err != nil {
			t.Fatalf("failed to set first day of week to %s: %v", d, err)
		}

		got, err := GetUserFirstDayOfWeek(ctx, tenantDB, userID)
		if err != nil {
			t.Fatalf("failed to get first day of week: %v", err)
		}
		if got != d {
			t.Errorf("expected %s, got %s", d, got)
		}
	}

	// 3. Invalid day rejection
	if err := SetUserFirstDayOfWeek(ctx, tenantDB, userID, "INVALID_DAY"); err == nil {
		t.Errorf("expected error when setting invalid first day of week, got nil")
	}
}
