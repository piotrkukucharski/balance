package ai

import (
	"context"
	"testing"

	"github.com/balance/balance/internal/lua"
)

func TestFormulaAssistantServiceWithMock(t *testing.T) {
	ctx := context.Background()
	mockProvider := NewMockFormulaAIProvider()
	svc := NewFormulaAssistantService(mockProvider)

	snap := &lua.InMemoryBudgetSnapshot{
		Targets: map[string]*lua.TargetMeta{
			"Required Mortgage Installment": {
				ID:       "018d0000-0000-7000-8000-000000000001",
				Name:     "Required Mortgage Installment",
				Required: 90000,
				Priority: 3,
			},
		},
		TargetCurrency: "PLN",
		WindowDays:     30,
	}

	req := FormulaGenerationRequest{
		UserPrompt:        "Calculate mortgage overpayment so total spending is 2000 PLN",
		CurrentBucketName: "Mortgage",
		TargetCurrency:    "PLN",
		WindowDays:        30,
	}

	res, val, err := svc.GenerateAndValidateFormula(ctx, req, snap)
	if err != nil {
		t.Fatalf("GenerateAndValidateFormula failed: %v", err)
	}

	if val != 110000 {
		t.Fatalf("expected evaluated minor amount 110000, got %d", val)
	}
	if len(res.LuaScript) == 0 {
		t.Fatalf("empty lua script returned")
	}
}

func TestMockFormulaAIProvider_PeriodicPrompt(t *testing.T) {
	ctx := context.Background()
	mockProvider := NewMockFormulaAIProvider()
	svc := NewFormulaAssistantService(mockProvider)

	snap30 := &lua.InMemoryBudgetSnapshot{
		TargetCurrency: "PLN",
		WindowDays:     30,
	}

	req := FormulaGenerationRequest{
		UserPrompt:        "125 zł płatne co trzy miesiące",
		CurrentBucketName: "Transport",
		TargetCurrency:    "PLN",
		WindowDays:        30,
	}

	res, val, err := svc.GenerateAndValidateFormula(ctx, req, snap30)
	if err != nil {
		t.Fatalf("GenerateAndValidateFormula failed: %v", err)
	}

	expectedScript := "return math.ceil(money(125) * window.days / 90)"
	if res.LuaScript != expectedScript {
		t.Fatalf("expected lua script %q, got %q", expectedScript, res.LuaScript)
	}

	// 125 * 100 * 30 / 90 = 4166.666 -> math.ceil = 4167 minor units
	if val != 4167 {
		t.Fatalf("expected evaluated value 4167 for 30-day window, got %d", val)
	}

	// Check 90-day window
	snap90 := &lua.InMemoryBudgetSnapshot{
		TargetCurrency: "PLN",
		WindowDays:     90,
	}
	val90, err := lua.EvaluateFormula(ctx, res.LuaScript, snap90)
	if err != nil {
		t.Fatalf("EvaluateFormula failed on 90-day window: %v", err)
	}
	if val90 != 12500 {
		t.Fatalf("expected evaluated value 12500 for 90-day window, got %d", val90)
	}
}

func TestNewFormulaAIProvider(t *testing.T) {
	// 1. Without GEMINI_API_KEY -> Mock
	p1 := NewFormulaAIProvider()
	if p1.ProviderName() != "MOCK" && p1.ProviderName() != "GEMINI_DIRECT" {
		t.Fatalf("unexpected provider name: %s", p1.ProviderName())
	}
}
