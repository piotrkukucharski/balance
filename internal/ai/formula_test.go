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
