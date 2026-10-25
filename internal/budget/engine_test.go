package budget

import (
	"testing"
	"time"
)

func TestRollingWindowTargetRequirement(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// 1. One-off target due in 180 days with a 90-day window: prorated by 90/180 = 50%
	due180 := now.AddDate(0, 0, 180).UnixMicro()
	target1 := &TargetDefinition{
		ID:                "t1",
		AmountMinor:       300000, // 3,000 PLN
		RecurrenceCadence: CadenceOneOff,
		DueDate:           due180,
	}

	req := ComputeTargetRequirement(target1, 90, now)
	// 300,000 * 90 / 180 = 150,000
	if req != 150000 {
		t.Fatalf("expected 150000, got %d", req)
	}

	// 2. One-off target due in 30 days evaluated in a 90-day window: 100% required
	due30 := now.AddDate(0, 0, 30).UnixMicro()
	target2 := &TargetDefinition{
		ID:                "t2",
		AmountMinor:       500000, // 5,000 PLN
		RecurrenceCadence: CadenceOneOff,
		DueDate:           due30,
	}
	req2 := ComputeTargetRequirement(target2, 90, now)
	if req2 != 500000 {
		t.Fatalf("expected 500000, got %d", req2)
	}

	// 3. Recurring target: English lessons 100 PLN every 10 days in 90-day window: 9 occurrences = 900 PLN
	target3 := &TargetDefinition{
		ID:                  "t3",
		AmountMinor:         10000, // 100 PLN
		RecurrenceCadence:   CadenceCustomDays,
		RecurrenceInterval:  10,
		NonSpendingStrategy: StrategyRollOver,
	}
	req3 := ComputeTargetRequirement(target3, 90, now)
	if req3 != 90000 {
		t.Fatalf("expected 90000, got %d", req3)
	}

	// 4. Custom Months target: Quarterly insurance 300 PLN every 3 months, anchor due in 90 days, 30-day window
	// Prorated: 30000 * 30 / 90 = 10000 (100 PLN)
	due90Months := now.AddDate(0, 3, 0).UnixMicro()
	targetMonths := &TargetDefinition{
		ID:                  "t4",
		AmountMinor:         30000,
		RecurrenceCadence:   CadenceCustomMonths,
		RecurrenceInterval:  3,
		DueDate:             due90Months,
		NonSpendingStrategy: StrategyRollOver,
	}
	reqMonths := ComputeTargetRequirement(targetMonths, 30, now)
	if reqMonths <= 0 || reqMonths > 30000 {
		t.Fatalf("expected reasonable prorated requirement for custom months, got %d", reqMonths)
	}

	// 5. Custom Years target: Car insurance 1200 PLN every 1 year, anchor due in 60 days, evaluated in 90-day window
	// Since anchor is within 90-day window, full 1 occurrence (1200 PLN) is required.
	due60Days := now.AddDate(0, 0, 60).UnixMicro()
	targetYears := &TargetDefinition{
		ID:                  "t5",
		AmountMinor:         120000,
		RecurrenceCadence:   CadenceCustomYears,
		RecurrenceInterval:  1,
		DueDate:             due60Days,
		NonSpendingStrategy: StrategyRollOver,
	}
	reqYears := ComputeTargetRequirement(targetYears, 90, now)
	if reqYears != 120000 {
		t.Fatalf("expected 120000 for annual target due in window, got %d", reqYears)
	}
}

func TestWaterfallSortAndAutoAssign(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dueSoon := now.AddDate(0, 0, 10).UnixMicro()
	dueLater := now.AddDate(0, 0, 25).UnixMicro()

	// Priority 3 (Mandatory), Priority 2 (Recommended), Priority 1 (Optional)
	tOpt := &TargetDefinition{
		ID:          "opt",
		Name:        "New Gadget",
		Priority:    1,
		AmountMinor: 10000,
	}
	tRec := &TargetDefinition{
		ID:          "rec",
		Name:        "Groceries",
		Priority:    2,
		AmountMinor: 40000,
		DueDate:     dueLater,
	}
	tMand1 := &TargetDefinition{
		ID:          "mand1",
		Name:        "Rent",
		Priority:    3,
		AmountMinor: 200000,
		DueDate:     dueSoon,
	}
	tMand2 := &TargetDefinition{
		ID:          "mand2",
		Name:        "Utilities",
		Priority:    3,
		AmountMinor: 30000,
		DueDate:     dueLater,
	}

	targets := []*TargetDefinition{tOpt, tRec, tMand2, tMand1}
	WaterfallSort(targets)

	if targets[0].ID != "mand1" || targets[1].ID != "mand2" || targets[2].ID != "rec" || targets[3].ID != "opt" {
		t.Fatalf("waterfall order incorrect: %v, %v, %v, %v", targets[0].ID, targets[1].ID, targets[2].ID, targets[3].ID)
	}

	// Auto-assign with 220,000 minor pool:
	// Should fully fund mand1 (200k), and 20k to mand2 (needs 30k), 0 to rec, 0 to opt.
	allocs, remaining := PriorityAutoAssign(targets, 30, now, 220000)
	if remaining != 0 {
		t.Fatalf("expected remaining pool 0, got %d", remaining)
	}
	if len(allocs) != 2 {
		t.Fatalf("expected 2 allocations, got %d", len(allocs))
	}
	if allocs[0].TargetID != "mand1" || allocs[0].Assigned != 200000 {
		t.Fatalf("alloc 0 incorrect: %+v", allocs[0])
	}
	if allocs[1].TargetID != "mand2" || allocs[1].Assigned != 20000 {
		t.Fatalf("alloc 1 incorrect: %+v", allocs[1])
	}
}
