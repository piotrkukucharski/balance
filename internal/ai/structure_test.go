package ai

import (
	"context"
	"testing"
)

func TestMockBudgetStructureAIProvider_Presets(t *testing.T) {
	provider := NewMockBudgetStructureAIProvider()

	t.Run("Polish family preset", func(t *testing.T) {
		res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "Rodzina 2+1, mieszkanie i kredyt hipoteczny, rachunki, zakupy i wakacje",
			DefaultCurrency: "PLN",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Groups) == 0 {
			t.Fatalf("expected groups, got 0")
		}
		foundBucket := false
		for _, g := range res.Groups {
			for _, b := range g.Buckets {
				if b.Name != "" {
					foundBucket = true
				}
				for _, tgt := range b.Targets {
					if tgt.Currency != "PLN" {
						t.Errorf("expected PLN, got %s", tgt.Currency)
					}
					if tgt.AmountMinor <= 0 {
						t.Errorf("expected positive amount, got %d", tgt.AmountMinor)
					}
				}
			}
		}
		if !foundBucket {
			t.Errorf("expected at least one bucket")
		}
	})

	t.Run("Custom line items", func(t *testing.T) {
		prompt := "Czynsz: 2500 PLN\nJedzenie: 1200 PLN\nOszczędności: 500 PLN"
		res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          prompt,
			DefaultCurrency: "PLN",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Groups) != 1 || len(res.Groups[0].Buckets) != 3 {
			t.Fatalf("expected 1 group and 3 buckets, got %d groups and %d buckets", len(res.Groups), len(res.Groups[0].Buckets))
		}
		b0 := res.Groups[0].Buckets[0]
		if b0.Name != "Czynsz" || len(b0.Targets) != 1 || b0.Targets[0].AmountMinor != 250000 {
			t.Errorf("unexpected bucket 0: %+v", b0)
		}
	})

	t.Run("Student preset with EUR", func(t *testing.T) {
		res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "I am a student living in dormitory",
			DefaultCurrency: "EUR",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Groups) == 0 {
			t.Fatalf("expected groups, got 0")
		}
		for _, g := range res.Groups {
			for _, b := range g.Buckets {
				for _, tgt := range b.Targets {
					if tgt.Currency != "EUR" {
						t.Errorf("expected EUR, got %s", tgt.Currency)
					}
				}
			}
		}
	})

	t.Run("Empty prompt error", func(t *testing.T) {
		_, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "   ",
			DefaultCurrency: "PLN",
		})
		if err == nil {
			t.Fatalf("expected error for empty prompt")
		}
	})
}
