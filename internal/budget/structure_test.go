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
