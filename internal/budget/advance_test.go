package budget

import (
	"context"
	"testing"
	"time"

	"github.com/balance/balance/internal/domain"
)

func TestAdvanceOverdueTargetDueDates(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	correlationID := domain.NewUUIDv7()

	bucketID, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Monthly Bills",
		Name:          "Internet",
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating bucket: %v", err)
	}

	loc := time.UTC
	// Set initial due date in the past: Jan 20, 2026
	pastDueMicros := time.Date(2026, 1, 20, 0, 0, 0, 0, loc).UnixMicro()

	// 1. Target with day 20 of month
	targetID, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:             bucketID,
		Name:                 "Fiber 1Gbps",
		AmountMinor:          10000,
		Currency:             "PLN",
		RecurrenceCadence:    CadenceMonthly,
		RecurrenceDayOfMonth: ptr(20),
		DueDate:              &pastDueMicros,
		AgentID:              agentID,
		CorrelationID:        correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating target: %v", err)
	}

	// Case A: Simulating Feb 15, 2026 -> Past due date Jan 20 is overdue -> should advance to Feb 20, 2026
	simFeb15 := time.Date(2026, 2, 15, 10, 0, 0, 0, loc)
	res, err := AdvanceOverdueTargetDueDatesForTenant(ctx, tenantDB, "UTC", simFeb15)
	if err != nil {
		t.Fatalf("advance error: %v", err)
	}
	if res.TargetsAdvanced != 1 {
		t.Fatalf("expected 1 target advanced, got %d", res.TargetsAdvanced)
	}

	var newDueMicros int64
	err = tenantDB.QueryRowContext(ctx, `SELECT due_date FROM targets_projection WHERE id = ?`, targetID).Scan(&newDueMicros)
	if err != nil {
		t.Fatalf("failed querying target: %v", err)
	}
	expectedFeb20 := time.Date(2026, 2, 20, 0, 0, 0, 0, loc).UnixMicro()
	if newDueMicros != expectedFeb20 {
		t.Fatalf("expected due date 2026-02-20 (%d), got %d", expectedFeb20, newDueMicros)
	}

	// Case B: Simulating Feb 20, 2026 (exact due day) -> should NOT advance yet
	simFeb20 := time.Date(2026, 2, 20, 18, 0, 0, 0, loc)
	resB, err := AdvanceOverdueTargetDueDatesForTenant(ctx, tenantDB, "UTC", simFeb20)
	if err != nil {
		t.Fatalf("advance error: %v", err)
	}
	if resB.TargetsAdvanced != 0 {
		t.Fatalf("expected 0 targets advanced on due date itself, got %d", resB.TargetsAdvanced)
	}

	// Case C: Simulating Feb 21, 2026 at 00:01:00 (day after due date) -> should advance to Mar 20, 2026
	simFeb21 := time.Date(2026, 2, 21, 0, 1, 0, 0, loc)
	resC, err := AdvanceOverdueTargetDueDatesForTenant(ctx, tenantDB, "UTC", simFeb21)
	if err != nil {
		t.Fatalf("advance error: %v", err)
	}
	if resC.TargetsAdvanced != 1 {
		t.Fatalf("expected 1 target advanced after midnight, got %d", resC.TargetsAdvanced)
	}

	err = tenantDB.QueryRowContext(ctx, `SELECT due_date FROM targets_projection WHERE id = ?`, targetID).Scan(&newDueMicros)
	expectedMar20 := time.Date(2026, 3, 20, 0, 0, 0, 0, loc).UnixMicro()
	if newDueMicros != expectedMar20 {
		t.Fatalf("expected due date 2026-03-20 (%d), got %d", expectedMar20, newDueMicros)
	}
}
