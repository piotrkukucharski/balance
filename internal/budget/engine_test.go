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
	// Since anchor is within 90-day window, 1 occurrence (1200 PLN) + 30 days tail prorated for next year (30/365 * 120000 = 9864) = 129864 minor.
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
	if reqYears != 129864 {
		t.Fatalf("expected 129864 for annual target due in 60d in 90d window, got %d", reqYears)
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

func TestDynamicHorizonCalculation_TaskExample(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	dueDate := time.Date(2027, 10, 20, 0, 0, 0, 0, loc)
	targetDueMicro := dueDate.UnixMicro()

	target := &TargetDefinition{
		ID:                "savings-1",
		AmountMinor:       12000, // 120 zł
		RecurrenceCadence: CadenceOneOff,
		DueDate:           targetDueMicro,
	}

	// Verify days until due is 360
	totalDays := CalendarDaysBetween(now, dueDate, loc)
	if totalDays != 360 {
		t.Fatalf("expected 360 total days until due, got %d", totalDays)
	}

	// 1. Monthly horizon: remaining in current month (2026-10-25 to 2026-10-31 = 6 days)
	horizonM, windowM := CalculateHorizonDays("monthly", now, loc)
	if horizonM != "monthly" || windowM != 6 {
		t.Fatalf("expected monthly with 6 days, got %s with %d days", horizonM, windowM)
	}
	reqM := ComputeTargetRequirementInLoc(target, windowM, now, loc)
	// (6 / 360) * 120 = 2 zł (200 minor)
	if reqM != 200 {
		t.Fatalf("expected 200 minor (2 zł), got %d", reqM)
	}

	// 2. 30d horizon: 30 days
	horizon30, window30 := CalculateHorizonDays("30d", now, loc)
	if horizon30 != "30d" || window30 != 30 {
		t.Fatalf("expected 30d with 30 days, got %s with %d days", horizon30, window30)
	}
	req30 := ComputeTargetRequirementInLoc(target, window30, now, loc)
	// (30 / 360) * 120 = 10 zł (1000 minor)
	if req30 != 1000 {
		t.Fatalf("expected 1000 minor (10 zł), got %d", req30)
	}

	// 3. 90d horizon: 90 days
	horizon90, window90 := CalculateHorizonDays("90d", now, loc)
	if horizon90 != "90d" || window90 != 90 {
		t.Fatalf("expected 90d with 90 days, got %s with %d days", horizon90, window90)
	}
	req90 := ComputeTargetRequirementInLoc(target, window90, now, loc)
	// (90 / 360) * 120 = 30 zł (3000 minor)
	if req90 != 3000 {
		t.Fatalf("expected 3000 minor (30 zł), got %d", req90)
	}

	// 4. Annual horizon (calendar year remaining: 2026-10-25 to 2026-12-31 = 67 days)
	horizonAnn, windowAnn := CalculateHorizonDays("annual", now, loc)
	if horizonAnn != "annual" || windowAnn != 67 {
		t.Fatalf("expected annual with 67 days, got %s with %d days", horizonAnn, windowAnn)
	}
	reqAnn := ComputeTargetRequirementInLoc(target, windowAnn, now, loc)
	// ceil(12000 * 67 / 360) = ceil(2233.333...) = 2234 minor (22.34 zł)
	if reqAnn != 2234 {
		t.Fatalf("expected 2234 minor (22.34 zł), got %d", reqAnn)
	}

	// 5. 365d rolling horizon: 365 days window. Since 360 <= 365, full 120 zł is required
	horizon365, window365 := CalculateHorizonDays("365d", now, loc)
	if horizon365 != "365d" || window365 != 365 {
		t.Fatalf("expected 365d with 365 days, got %s with %d days", horizon365, window365)
	}
	req365 := ComputeTargetRequirementInLoc(target, window365, now, loc)
	// 120 zł (12000 minor)
	if req365 != 12000 {
		t.Fatalf("expected 12000 minor (120 zł), got %d", req365)
	}
}

func TestDynamicHorizonCalculation_EdgeCases(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)

	// Edge case 1: Target due in 15 days, evaluated in 30-day window (due sooner than window)
	due15 := now.AddDate(0, 0, 15).UnixMicro()
	targetSoon := &TargetDefinition{
		ID:                "t-soon",
		AmountMinor:       5000,
		RecurrenceCadence: CadenceOneOff,
		DueDate:           due15,
	}
	reqSoon := ComputeTargetRequirementInLoc(targetSoon, 30, now, loc)
	if reqSoon != 5000 {
		t.Fatalf("expected 100%% requirement (5000), got %d", reqSoon)
	}

	// Edge case 2: Overdue one-off target (due 5 days ago)
	duePast := now.AddDate(0, 0, -5).UnixMicro()
	targetOverdue := &TargetDefinition{
		ID:                "t-overdue",
		AmountMinor:       7500,
		RecurrenceCadence: CadenceOneOff,
		DueDate:           duePast,
	}
	reqOverdue := ComputeTargetRequirementInLoc(targetOverdue, 30, now, loc)
	if reqOverdue != 7500 {
		t.Fatalf("expected 100%% requirement for overdue target (7500), got %d", reqOverdue)
	}

	// Edge case 3: Undated one-off target (due_date == 0)
	targetUndated := &TargetDefinition{
		ID:                "t-undated",
		AmountMinor:       10000,
		RecurrenceCadence: CadenceOneOff,
		DueDate:           0,
	}
	reqUndated := ComputeTargetRequirementInLoc(targetUndated, 30, now, loc)
	if reqUndated != 0 {
		t.Fatalf("expected 0 requirement for undated target, got %d", reqUndated)
	}

	// Edge case 4: Last day of month (2026-10-31)
	lastDayOfMonth := time.Date(2026, 10, 31, 15, 0, 0, 0, loc)
	daysLeftM := DaysRemainingInCurrentMonth(lastDayOfMonth, loc)
	if daysLeftM != 1 {
		t.Fatalf("expected min 1 day on last day of month, got %d", daysLeftM)
	}

	// Edge case 5: Last day of year (2026-12-31)
	lastDayOfYear := time.Date(2026, 12, 31, 22, 0, 0, 0, loc)
	daysLeftY := DaysRemainingInCurrentYear(lastDayOfYear, loc)
	if daysLeftY != 1 {
		t.Fatalf("expected min 1 day on last day of year, got %d", daysLeftY)
	}

	// Edge case 6: Ceiling rounding test (1000 minor, 10 days window, 33 days until due)
	// float: 1000 * 10 / 33 = 303.0303... -> ceil should be 304
	due33 := now.AddDate(0, 0, 33).UnixMicro()
	targetCeil := &TargetDefinition{
		ID:                "t-ceil",
		AmountMinor:       1000,
		RecurrenceCadence: CadenceOneOff,
		DueDate:           due33,
	}
	reqCeil := ComputeTargetRequirementInLoc(targetCeil, 10, now, loc)
	if reqCeil != 304 {
		t.Fatalf("expected ceil rounding to produce 304 minor, got %d", reqCeil)
	}

	// Edge case 7: Continuous pro-rating task example (BALANCE-65)
	// now = 2026-10-25, horizon = 30d -> windowEnd = 2026-11-24, target = 100 PLN monthly (10000 minor)
	// next due: 2026-11-06 (in 12 days) -> 100% required = 100 PLN
	// remaining window after due date: 18 days (2026-11-24 - 2026-11-06)
	// next cycle (2026-11-06 to 2026-12-06): 30 days
	// tail prorated: 18/30 * 10000 = 6000 minor
	// total required in 30d window: 10000 + 6000 = 16000 minor (160 PLN)
	nowBalance65 := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	dueBalance65 := time.Date(2026, 11, 6, 0, 0, 0, 0, loc).UnixMicro()
	targetBalance65 := &TargetDefinition{
		ID:                  "t-b65",
		AmountMinor:         10000, // 100 PLN
		RecurrenceCadence:   CadenceMonthly,
		DueDate:             dueBalance65,
		NonSpendingStrategy: StrategyRollOver,
	}
	reqB65 := ComputeTargetRequirementInLoc(targetBalance65, 30, nowBalance65, loc)
	if reqB65 != 16000 {
		t.Fatalf("expected 16000 minor (160 PLN) for BALANCE-65 continuous pro-rating example, got %d", reqB65)
	}

	// Edge case 8: computeRecurring continuous pro-rating without due date
	// target = 100 PLN every 20 days, evaluated in 30 days window:
	// 1 occurrence = 100 PLN (10000 minor)
	// remainder 10 days out of 20 days = 50% = 50 PLN (5000 minor)
	// total = 15000 minor (150 PLN)
	targetCustomUndated := &TargetDefinition{
		ID:                  "t-undated-rec",
		AmountMinor:         10000,
		RecurrenceCadence:   CadenceCustomDays,
		RecurrenceInterval:  20,
		NonSpendingStrategy: StrategyRollOver,
	}
	reqUndatedRec := ComputeTargetRequirementInLoc(targetCustomUndated, 30, nowBalance65, loc)
	if reqUndatedRec != 15000 {
		t.Fatalf("expected 15000 minor for custom undated recurring target, got %d", reqUndatedRec)
	}
}
