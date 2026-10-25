package budget

import (
	"context"
	"testing"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/domain"
)

func TestApplyBudgetStructure_CustomDaysCadence(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()

	days14 := 14
	structure := &ai.BudgetStructure{
		Groups: []ai.ProposedGroup{
			{
				GroupName: "Transport & Living",
				Buckets: []ai.ProposedBucket{
					{
						Name: "Public Transit",
						Targets: []ai.ProposedTarget{
							{
								Name:                "Karta miejska (kwartalna)",
								TargetType:          "EXPENSE",
								Priority:            2,
								AmountMinor:         12500,
								Currency:            "PLN",
								RecurrenceCadence:   "CUSTOM_DAYS",
								RecurrenceDays:      nil, // nil to test fallback/inference
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
							{
								Name:                "Bilety dwutygodniowe",
								TargetType:          "EXPENSE",
								Priority:            1,
								AmountMinor:         5000,
								Currency:            "PLN",
								RecurrenceCadence:   "CUSTOM_DAYS",
								RecurrenceDays:      &days14,
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
						},
					},
				},
			},
		},
	}

	bCreated, tCreated, err := ApplyBudgetStructure(ctx, tenantDB, structure, agentID, corrID)
	if err != nil {
		t.Fatalf("ApplyBudgetStructure failed on CUSTOM_DAYS: %v", err)
	}
	if bCreated != 1 || tCreated != 2 {
		t.Fatalf("expected 1 bucket and 2 targets, got %d buckets and %d targets", bCreated, tCreated)
	}

	// Verify the targets in database
	var rCadence1 string
	var rDays1 int
	err = tenantDB.QueryRowContext(ctx, `SELECT recurrence_cadence, recurrence_days FROM targets_projection WHERE name = ?`, "Karta miejska (kwartalna)").Scan(&rCadence1, &rDays1)
	if err != nil {
		t.Fatalf("failed querying target 1: %v", err)
	}
	if rCadence1 != "CUSTOM_DAYS" || rDays1 != 90 {
		t.Errorf("expected CUSTOM_DAYS with 90 days for quarterly ticket, got %s / %d", rCadence1, rDays1)
	}

	var rCadence2 string
	var rDays2 int
	err = tenantDB.QueryRowContext(ctx, `SELECT recurrence_cadence, recurrence_days FROM targets_projection WHERE name = ?`, "Bilety dwutygodniowe").Scan(&rCadence2, &rDays2)
	if err != nil {
		t.Fatalf("failed querying target 2: %v", err)
	}
	if rCadence2 != "CUSTOM_DAYS" || rDays2 != 14 {
		t.Errorf("expected CUSTOM_DAYS with 14 days, got %s / %d", rCadence2, rDays2)
	}

	// Verify ExportBudgetStructure
	exported, err := ExportBudgetStructure(ctx, tenantDB)
	if err != nil {
		t.Fatalf("ExportBudgetStructure failed: %v", err)
	}
	if len(exported.Groups) != 1 || len(exported.Groups[0].Buckets) != 1 || len(exported.Groups[0].Buckets[0].Targets) != 2 {
		t.Fatalf("unexpected exported structure: %+v", exported)
	}
	exportedTargets := exported.Groups[0].Buckets[0].Targets
	if exportedTargets[0].RecurrenceDays == nil || *exportedTargets[0].RecurrenceDays != 90 {
		t.Errorf("expected exported target 1 recurrence_days to be 90, got: %v", exportedTargets[0].RecurrenceDays)
	}
	if exportedTargets[1].RecurrenceDays == nil || *exportedTargets[1].RecurrenceDays != 14 {
		t.Errorf("expected exported target 2 recurrence_days to be 14, got: %v", exportedTargets[1].RecurrenceDays)
	}
}

func TestApplyBudgetStructure_AtomicityRollback(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()

	// Structure with a valid first bucket + target, followed by an invalid target in a second bucket
	structure := &ai.BudgetStructure{
		Groups: []ai.ProposedGroup{
			{
				GroupName: "Living",
				Buckets: []ai.ProposedBucket{
					{
						Name: "Groceries",
						Targets: []ai.ProposedTarget{
							{
								Name:                "Weekly Groceries",
								TargetType:          "EXPENSE",
								Priority:            1,
								AmountMinor:         20000,
								Currency:            "PLN",
								RecurrenceCadence:   "WEEKLY",
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
						},
					},
					{
						Name: "Utilities",
						Targets: []ai.ProposedTarget{
							{
								Name:                "Faulty Target",
								TargetType:          "EXPENSE",
								Priority:            2,
								AmountMinor:         -500, // Invalid negative amount causing failure
								Currency:            "PLN",
								RecurrenceCadence:   "MONTHLY",
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
						},
					},
				},
			},
		},
	}

	bCreated, tCreated, err := ApplyBudgetStructure(ctx, tenantDB, structure, agentID, corrID)
	if err == nil {
		t.Fatalf("expected error from ApplyBudgetStructure due to invalid target, got nil (buckets=%d, targets=%d)", bCreated, tCreated)
	}
	if bCreated != 0 || tCreated != 0 {
		t.Errorf("expected 0 buckets and 0 targets returned on failure, got buckets=%d, targets=%d", bCreated, tCreated)
	}

	// Verify complete rollback: no buckets, targets, or events should exist
	var bucketCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM buckets_projection`).Scan(&bucketCount); err != nil {
		t.Fatalf("failed querying buckets count: %v", err)
	}
	if bucketCount != 0 {
		t.Errorf("expected 0 buckets in projection after rollback, got %d", bucketCount)
	}

	var targetCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection`).Scan(&targetCount); err != nil {
		t.Fatalf("failed querying targets count: %v", err)
	}
	if targetCount != 0 {
		t.Errorf("expected 0 targets in projection after rollback, got %d", targetCount)
	}

	var bucketEventsCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM bucket_events`).Scan(&bucketEventsCount); err != nil {
		t.Fatalf("failed querying bucket_events count: %v", err)
	}
	if bucketEventsCount != 0 {
		t.Errorf("expected 0 events in bucket_events after rollback, got %d", bucketEventsCount)
	}

	var targetEventsCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM target_events`).Scan(&targetEventsCount); err != nil {
		t.Fatalf("failed querying target_events count: %v", err)
	}
	if targetEventsCount != 0 {
		t.Errorf("expected 0 events in target_events after rollback, got %d", targetEventsCount)
	}
}

