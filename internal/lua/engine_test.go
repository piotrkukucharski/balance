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
