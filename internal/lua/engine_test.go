package lua

import (
	"context"
	"testing"
	"time"
)

func TestEvaluateFormulaBasic(t *testing.T) {
	ctx := context.Background()
	snap := &InMemoryBudgetSnapshot{
		Targets: map[string]*TargetMeta{
			"Required Mortgage Installment": {
				ID:       "018d0000-0000-7000-8000-000000000001",
				Name:     "Required Mortgage Installment",
				Required: 90000, // 900.00 PLN
				Priority: 3,
			},
		},
		BucketBalances: map[string]int64{
			"PLN": 50000,
		},
		FXRates: map[string]int64{
			"EUR_PLN": 4321500, // 4.321500
		},
		InflowsPast: map[string][]int64{
			"PLN": {100000, 200000, 300000},
		},
		WindowDays:     30,
		TargetCurrency: "PLN",
	}

	// 1. Mortgage overpayment example: return math.max(0, money(2000) - target("Required Mortgage Installment").required)
	script1 := `return math.max(0, money(2000) - target("Required Mortgage Installment").required)`
	res, err := EvaluateFormula(ctx, script1, snap)
	if err != nil {
		t.Fatalf("script1 failed: %v", err)
	}
	// 200,000 - 90,000 = 110,000 minor units (1100.00 PLN)
	if res != 110000 {
		t.Fatalf("expected 110000, got %d", res)
	}

	// 2. FX convert example
	script2 := `return fx.convert(10000, "EUR", "PLN")`
	res, err = EvaluateFormula(ctx, script2, snap)
	if err != nil {
		t.Fatalf("script2 failed: %v", err)
	}
	if res != 43215 {
		t.Fatalf("expected 43215, got %d", res)
	}

	// 3. Math functions
	script3 := `return math.min(1000, math.floor(550.8))`
	res, err = EvaluateFormula(ctx, script3, snap)
	if err != nil {
		t.Fatalf("script3 failed: %v", err)
	}
	if res != 550 {
		t.Fatalf("expected 550, got %d", res)
	}
}

func TestSecurityHardeningBans(t *testing.T) {
	ctx := context.Background()
	snap := &InMemoryBudgetSnapshot{}

	// Loops banned
	loopScript := `
	local x = 0
	while x < 10 do
		x = x + 1
	end
	return x
	`
	_, err := EvaluateFormula(ctx, loopScript, snap)
	if err == nil {
		t.Fatalf("expected error for while loop")
	}

	// Functions banned
	fnScript := `
	local function foo() return 10 end
	return foo()
	`
	_, err = EvaluateFormula(ctx, fnScript, snap)
	if err == nil {
		t.Fatalf("expected error for user-defined function")
	}

	// Concat banned
	concatScript := `
	local s = "a" .. "b"
	return 10
	`
	_, err = EvaluateFormula(ctx, concatScript, snap)
	if err == nil {
		t.Fatalf("expected error for string concatenation")
	}
}

func TestEvaluateAutomationJudgeAndExecutor(t *testing.T) {
	ctx := context.Background()
	snap := &InMemoryBudgetSnapshot{
		Buckets: map[string]*BucketMeta{
			"groceries": {ID: "b-groc", Name: "Groceries", Currency: "PLN"},
			"b-groc":    {ID: "b-groc", Name: "Groceries", Currency: "PLN"},
			"transport": {ID: "b-trans", Name: "Transport", Currency: "PLN"},
			"b-trans":   {ID: "b-trans", Name: "Transport", Currency: "PLN"},
		},
		Targets: map[string]*TargetMeta{
			"metro pass": {ID: "t-metro", Name: "Metro Pass", BucketID: "b-trans"},
			"t-metro":    {ID: "t-metro", Name: "Metro Pass", BucketID: "b-trans"},
		},
		Parties: map[string]*PartyMeta{
			"uber": {ID: "p-uber", Name: "Uber", IsUserOwned: false},
		},
	}

	notes := "Uber trip home from supermarket"
	tx := AutomationTxContext{
		ID:                "tx-123",
		SourceAmountMinor: 4500,
		SourceCurrency:    "PLN",
		DestAmountMinor:   4500,
		DestCurrency:      "PLN",
		Notes:             &notes,
		PartyName:         "Uber",
		PartyID:           "p-uber",
		IsUserOwnedSource: true,
		IsUserOwnedDest:   false,
	}

	// 1. Judge test: matches
	judgeScript := `text.contains(tx.notes, "Uber") and tx.is_outflow`
	matched, err := EvaluateAutomationJudge(ctx, judgeScript, tx, snap)
	if err != nil {
		t.Fatalf("judge error: %v", err)
	}
	if !matched {
		t.Fatalf("expected judge to match")
	}

	// 2. Judge test: does not match
	judgeNoMatch := `text.contains(tx.notes, "Netflix")`
	matched, err = EvaluateAutomationJudge(ctx, judgeNoMatch, tx, snap)
	if err != nil {
		t.Fatalf("judge error: %v", err)
	}
	if matched {
		t.Fatalf("expected judge not to match")
	}

	// 3. Executor test: set_target (auto sets bucket), set_notes
	execScript := `
	tx.set_target("Metro Pass")
	tx.set_notes("Subsidized ride")
	`
	outcome, err := EvaluateAutomationExecutor(ctx, execScript, tx, snap)
	if err != nil {
		t.Fatalf("executor error: %v", err)
	}
	if !outcome.Modified {
		t.Fatalf("expected outcome to be modified")
	}
	if outcome.UpdatedTx.TargetID == nil || *outcome.UpdatedTx.TargetID != "t-metro" {
		t.Fatalf("expected target t-metro, got %v", outcome.UpdatedTx.TargetID)
	}
	if outcome.UpdatedTx.BucketID == nil || *outcome.UpdatedTx.BucketID != "b-trans" {
		t.Fatalf("expected bucket b-trans, got %v", outcome.UpdatedTx.BucketID)
	}
	if *outcome.UpdatedTx.Notes != "Subsidized ride" {
		t.Fatalf("expected notes updated, got %s", *outcome.UpdatedTx.Notes)
	}

	// 4. Executor test: split
	splitScript := `
	tx.split({
		{ amount = 2000, bucket = "Groceries", notes = "Snacks" },
		{ amount = 2500, bucket = "Transport", notes = "Fare" }
	})
	`
	outcome, err = EvaluateAutomationExecutor(ctx, splitScript, tx, snap)
	if err != nil {
		t.Fatalf("split error: %v", err)
	}
	if len(outcome.Splits) != 2 {
		t.Fatalf("expected 2 splits, got %d", len(outcome.Splits))
	}
	if outcome.Splits[0].AmountMinor != 2000 || *outcome.Splits[0].BucketID != "b-groc" {
		t.Fatalf("split 0 mismatch: %+v", outcome.Splits[0])
	}
	if outcome.Splits[1].AmountMinor != 2500 || *outcome.Splits[1].BucketID != "b-trans" {
		t.Fatalf("split 1 mismatch: %+v", outcome.Splits[1])
	}

	// 5. Executor test: split sum mismatch
	splitMismatchScript := `
	tx.split({
		{ amount = 2000, bucket = "Groceries" },
		{ amount = 2000, bucket = "Transport" }
	})
	`
	_, err = EvaluateAutomationExecutor(ctx, splitMismatchScript, tx, snap)
	if err == nil {
		t.Fatalf("expected error on split total mismatch")
	}

	// 6. Executor test: delete
	delScript := `tx.delete()`
	outcome, err = EvaluateAutomationExecutor(ctx, delScript, tx, snap)
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if !outcome.Deleted {
		t.Fatalf("expected deleted = true")
	}
}

func TestFinancialPMT(t *testing.T) {
	ctx := context.Background()
	snap := &InMemoryBudgetSnapshot{}

	// Standard Excel PMT: rate = 0.05/12, nper = 360, pv = 100000 -> -536.82 PLN -> -53682 minor
	script := `
	local r = 0.05 / 12
	local nper = 360
	local pv = 100000
	local pmt = financial.pmt(r, nper, pv)
	return money(-pmt)
	`
	val, err := EvaluateFormula(ctx, script, snap)
	if err != nil {
		t.Fatalf("EvaluateFormula failed: %v", err)
	}
	// 536.82 * 100 = 53682
	if val != 53682 {
		t.Fatalf("expected 53682 minor units, got %d", val)
	}

	// Direct return of positive PMT using math.abs
	scriptAbs := `
	local pmt = financial.pmt(0.05 / 12, 360, 100000)
	return money(math.abs(pmt))
	`
	valAbs, err := EvaluateFormula(ctx, scriptAbs, snap)
	if err != nil {
		t.Fatalf("EvaluateFormula with math.abs failed: %v", err)
	}
	if valAbs != 53682 {
		t.Fatalf("expected 53682 minor units, got %d", valAbs)
	}

	// Zero interest rate
	scriptZeroRate := `
	local pmt = financial.pmt(0, 10, 1000)
	return money(-pmt)
	`
	valZero, err := EvaluateFormula(ctx, scriptZeroRate, snap)
	if err != nil {
		t.Fatalf("EvaluateFormula zero rate failed: %v", err)
	}
	// 1000 / 10 = 100 -> 10000 minor
	if valZero != 10000 {
		t.Fatalf("expected 10000 minor units, got %d", valZero)
	}

	// Annuity due (type = 1)
	scriptType1 := `
	local pmt = financial.pmt(0.05 / 12, 360, 100000, 0, 1)
	return money(-pmt)
	`
	valType1, err := EvaluateFormula(ctx, scriptType1, snap)
	if err != nil {
		t.Fatalf("EvaluateFormula type=1 failed: %v", err)
	}
	// In Excel: PMT(0.05/12, 360, 100000, 0, 1) = -534.59 -> 53459 minor
	if valType1 != 53459 {
		t.Fatalf("expected 53459 minor units, got %d", valType1)
	}

	// Invalid nper <= 0 raises error
	scriptInvalidNper := `return financial.pmt(0.05, 0, 1000)`
	_, err = EvaluateFormula(ctx, scriptInvalidNper, snap)
	if err == nil {
		t.Fatalf("expected error for non-positive nper")
	}
}

func TestSelfAndRuntimeContext(t *testing.T) {
	ctx := context.Background()

	// Mock today as 2026-10-25 15:30:00 in Europe/Warsaw
	loc, _ := time.LoadLocation("Europe/Warsaw")
	mockNow := time.Date(2026, 10, 25, 15, 30, 0, 0, loc)
	snap := &InMemoryBudgetSnapshot{
		Timezone: "Europe/Warsaw",
		NowTime:  &mockNow,
	}

	// Target due in 10 years: 2036-10-25
	dueT := time.Date(2036, 10, 25, 0, 0, 0, 0, time.UTC)
	dueSec := dueT.Unix()

	targetCtx := &TargetFormulaContext{
		Name:          "Mortgage Loan",
		Planned:       30000000,
		Required:      30000000,
		Funded:        500000,
		Priority:      3,
		AnchorDueDate: &dueSec,
	}

	// Full formula with self.anchor_due_date, runtime.today, date.diff_months and financial.pmt
	formula := `
	local nper = date.diff_months(self.anchor_due_date, runtime.today)
	local r = 0.06 / 12
	local pv = 300000
	local pmt = -financial.pmt(r, nper, pv)
	return money(pmt)
	`
	val, err := EvaluateFormulaWithContext(ctx, formula, snap, targetCtx)
	if err != nil {
		t.Fatalf("EvaluateFormulaWithContext failed: %v", err)
	}
	// nper = 120 months.
	// PMT(0.06/12, 120, 300000) = -3330.62 PLN -> 333062 minor
	if val != 333062 {
		t.Fatalf("expected 333062 minor units, got %d", val)
	}

	// Test missing anchor_due_date on self raises error
	targetCtxNoDue := &TargetFormulaContext{
		Name:     "No Due Target",
		Priority: 1,
	}
	scriptMissingDue := `return self.anchor_due_date`
	_, err = EvaluateFormulaWithContext(ctx, scriptMissingDue, snap, targetCtxNoDue)
	if err == nil {
		t.Fatalf("expected error when accessing missing self.anchor_due_date")
	}

	// Test other self properties
	scriptSelfProps := `return self.priority + money(10)`
	valProps, err := EvaluateFormulaWithContext(ctx, scriptSelfProps, snap, targetCtx)
	if err != nil {
		t.Fatalf("self properties access failed: %v", err)
	}
	if valProps != 1003 { // 3 + 1000
		t.Fatalf("expected 1003, got %d", valProps)
	}
}

func TestTargetAnchorDueDate(t *testing.T) {
	ctx := context.Background()
	dueT := time.Date(2028, 5, 1, 0, 0, 0, 0, time.UTC)
	dueSec := dueT.Unix()

	snap := &InMemoryBudgetSnapshot{
		Targets: map[string]*TargetMeta{
			"Car Loan": {
				ID:            "t-car",
				Name:          "Car Loan",
				Required:      150000,
				AnchorDueDate: &dueSec,
			},
			"Flexible Target": {
				ID:       "t-flex",
				Name:     "Flexible Target",
				Required: 50000,
			},
		},
	}

	// 1. Target with anchor due date returns epoch seconds
	script1 := `return target("Car Loan").anchor_due_date`
	val, err := EvaluateFormula(ctx, script1, snap)
	if err != nil {
		t.Fatalf("failed reading target anchor_due_date: %v", err)
	}
	if val != dueSec {
		t.Fatalf("expected %d, got %d", dueSec, val)
	}

	// 2. Target without anchor due date raises error
	script2 := `return target("Flexible Target").anchor_due_date`
	_, err = EvaluateFormula(ctx, script2, snap)
	if err == nil {
		t.Fatalf("expected error accessing anchor_due_date on target without due date")
	}
}
