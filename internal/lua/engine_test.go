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
			"uber":   {ID: "p-uber", Name: "Uber", IsUserOwned: false},
			"p-uber": {ID: "p-uber", Name: "Uber", IsUserOwned: false},
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

	// 3. Executor test: set_target_by_name (auto sets bucket), set_notes
	execScript := `
	tx.set_target_by_name("Metro Pass")
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

	// 7. Test tx.set_* (by ID) vs tx.set_*_by_name and error handling
	setByIDScript := `
	tx.set_target("t-metro")
	tx.set_bucket("b-groc")
	tx.set_party("p-uber")
	`
	outcome, err = EvaluateAutomationExecutor(ctx, setByIDScript, tx, snap)
	if err != nil {
		t.Fatalf("set by ID failed: %v", err)
	}
	if *outcome.UpdatedTx.BucketID != "b-groc" || outcome.UpdatedTx.BucketName != "Groceries" {
		t.Fatalf("unexpected bucket: %v", outcome.UpdatedTx.BucketID)
	}
	if *outcome.UpdatedTx.TargetID != "t-metro" || outcome.UpdatedTx.TargetName != "Metro Pass" {
		t.Fatalf("unexpected target: %v", outcome.UpdatedTx.TargetID)
	}
	if outcome.UpdatedTx.PartyID != "p-uber" || outcome.UpdatedTx.PartyName != "Uber" {
		t.Fatalf("unexpected party: %v", outcome.UpdatedTx.PartyID)
	}

	// Missing ID should fail with error
	badIDScript := `tx.set_bucket("non-existent-id")`
	_, err = EvaluateAutomationExecutor(ctx, badIDScript, tx, snap)
	if err == nil {
		t.Fatalf("expected error on non-existent bucket ID")
	}

	badNameScript := `tx.set_bucket_by_name("NonExistentBucket")`
	_, err = EvaluateAutomationExecutor(ctx, badNameScript, tx, snap)
	if err == nil {
		t.Fatalf("expected error on non-existent bucket name")
	}

	// 8. Test account object and account.set_party / account.set_party_by_name
	if snap.Accounts == nil {
		snap.Accounts = make(map[string]*AccountMeta)
	}
	snap.Parties["p-zabka"] = &PartyMeta{ID: "p-zabka", Name: "Żabka", IsUserOwned: false}
	snap.Parties["żabka"] = snap.Parties["p-zabka"]
	snap.Accounts["acct-pos"] = &AccountMeta{ID: "acct-pos", Name: "ZABKA Z4508 K.1", PartyID: "p-temp", Currency: "PLN", IsUserOwned: false}

	txWithExtAcct := tx
	txWithExtAcct.DestinationAccountID = "acct-pos"
	txWithExtAcct.DestAccountName = "ZABKA Z4508 K.1"

	// Read-only in judge
	judgeAcctScript := `account.name == "ZABKA Z4508 K.1"`
	matched, err = EvaluateAutomationJudge(ctx, judgeAcctScript, txWithExtAcct, snap)
	if err != nil || !matched {
		t.Fatalf("expected judge on account.name to match, got matched=%v, err=%v", matched, err)
	}

	// Executor: account.set_party_by_name
	execAcctScript := `account.set_party_by_name("Żabka")`
	outcome, err = EvaluateAutomationExecutor(ctx, execAcctScript, txWithExtAcct, snap)
	if err != nil {
		t.Fatalf("account.set_party_by_name failed: %v", err)
	}
	if outcome.AccountReassign == nil {
		t.Fatalf("expected AccountReassign to be populated")
	}
	if outcome.AccountReassign.AccountID != "acct-pos" || outcome.AccountReassign.TargetPartyID != "p-zabka" {
		t.Fatalf("unexpected AccountReassign: %+v", outcome.AccountReassign)
	}
	if outcome.UpdatedTx.PartyID != "p-zabka" || outcome.UpdatedTx.PartyName != "Żabka" {
		t.Fatalf("unexpected updated tx party: %s (%s)", outcome.UpdatedTx.PartyName, outcome.UpdatedTx.PartyID)
	}

	// Executor: account.set_party with ID
	execAcctIDScript := `account.set_party("p-zabka")`
	outcome, err = EvaluateAutomationExecutor(ctx, execAcctIDScript, txWithExtAcct, snap)
	if err != nil {
		t.Fatalf("account.set_party failed: %v", err)
	}
	if outcome.AccountReassign == nil || outcome.AccountReassign.TargetPartyID != "p-zabka" {
		t.Fatalf("unexpected AccountReassign with ID: %+v", outcome.AccountReassign)
	}

	// Non-existent target party
	badPartyScript := `account.set_party_by_name("UnknownMerchant")`
	_, err = EvaluateAutomationExecutor(ctx, badPartyScript, txWithExtAcct, snap)
	if err == nil {
		t.Fatalf("expected error on non-existent party name in account.set_party_by_name")
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
			"t-car": {
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
			"t-flex": {
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

	// 3. Target reference by ID via target(id)
	script3 := `return target("t-car").anchor_due_date`
	val3, err := EvaluateFormula(ctx, script3, snap)
	if err != nil {
		t.Fatalf("failed reading target anchor_due_date via target(id): %v", err)
	}
	if val3 != dueSec {
		t.Fatalf("expected %d, got %d", dueSec, val3)
	}

	// 4. Target reference by ID via targetById(id)
	script4 := `return targetById("t-car").required`
	val4, err := EvaluateFormula(ctx, script4, snap)
	if err != nil {
		t.Fatalf("failed reading target via targetById(id): %v", err)
	}
	if val4 != 150000 {
		t.Fatalf("expected 150000, got %d", val4)
	}
}

func TestTargetByIdAndExtractDependencies(t *testing.T) {
	ctx := context.Background()
	snap := &InMemoryBudgetSnapshot{
		Targets: map[string]*TargetMeta{
			"01a1b5e8-target-1": {
				ID:       "01a1b5e8-target-1",
				Name:     "",
				Required: 45000,
				Planned:  45000,
			},
			"01a1b5e8-target-2": {
				ID:       "01a1b5e8-target-2",
				Name:     "Electricity",
				Required: 25000,
				Planned:  25000,
			},
		},
	}

	// Target without name accessed by ID via targetById()
	script1 := `return targetById("01a1b5e8-target-1").required`
	val1, err := EvaluateFormula(ctx, script1, snap)
	if err != nil {
		t.Fatalf("failed evaluating targetById for unnamed target: %v", err)
	}
	if val1 != 45000 {
		t.Fatalf("expected 45000, got %d", val1)
	}

	// Target without name accessed by ID via target()
	script2 := `return target("01a1b5e8-target-1").required`
	val2, err := EvaluateFormula(ctx, script2, snap)
	if err != nil {
		t.Fatalf("failed evaluating target for unnamed target by ID: %v", err)
	}
	if val2 != 45000 {
		t.Fatalf("expected 45000, got %d", val2)
	}

	// Check id and name fields on returned target table
	scriptProps := `
		local t = targetById("01a1b5e8-target-2")
		if t.id == "01a1b5e8-target-2" and t.name == "Electricity" then
			return t.required
		end
		return 0
	`
	valProps, err := EvaluateFormula(ctx, scriptProps, snap)
	if err != nil {
		t.Fatalf("failed evaluating target properties: %v", err)
	}
	if valProps != 25000 {
		t.Fatalf("expected 25000, got %d", valProps)
	}

	// ExtractTargetDependencies supports both target() and targetById()
	combinedScript := `
		local a = target("01a1b5e8-target-1").required
		local b = targetById("01a1b5e8-target-2").required
		local c = target("Electricity").planned
		return a + b + c
	`
	deps, err := ExtractTargetDependencies(combinedScript)
	if err != nil {
		t.Fatalf("failed extracting target dependencies: %v", err)
	}
	expectedDeps := []string{"01a1b5e8-target-1", "01a1b5e8-target-2", "Electricity"}
	if len(deps) != len(expectedDeps) {
		t.Fatalf("expected %d deps, got %d: %v", len(expectedDeps), len(deps), deps)
	}
	for i, exp := range expectedDeps {
		if deps[i] != exp {
			t.Errorf("dep %d: expected %q, got %q", i, exp, deps[i])
		}
	}

	// Executor tx.set_target_by_id
	notes := "Invoice payment"
	txCtx := AutomationTxContext{
		Notes:              &notes,
		SourceAmountMinor:  10000,
		DestAmountMinor:    10000,
		IsUserOwnedSource:  true,
		IsUserOwnedDest:    false,
	}
	snapWithBuckets := &InMemoryBudgetSnapshot{
		Buckets: map[string]*BucketMeta{
			"b-utilities": {ID: "b-utilities", Name: "Utilities"},
		},
		Targets: map[string]*TargetMeta{
			"t-power": {ID: "t-power", Name: "Power Bill", BucketID: "b-utilities", Required: 10000},
		},
	}
	execScript := `tx.set_target_by_id("t-power")`
	outcome, err := EvaluateAutomationExecutor(ctx, execScript, txCtx, snapWithBuckets)
	if err != nil {
		t.Fatalf("executor set_target_by_id failed: %v", err)
	}
	if outcome.UpdatedTx.TargetID == nil || *outcome.UpdatedTx.TargetID != "t-power" {
		t.Fatalf("expected TargetID 't-power', got %v", outcome.UpdatedTx.TargetID)
	}
	if outcome.UpdatedTx.BucketID == nil || *outcome.UpdatedTx.BucketID != "b-utilities" {
		t.Fatalf("expected BucketID 'b-utilities', got %v", outcome.UpdatedTx.BucketID)
	}
}
