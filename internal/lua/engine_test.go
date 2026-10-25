package lua

import (
	"context"
	"testing"
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
