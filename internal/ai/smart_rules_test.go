package ai

import (
	"context"
	"testing"
)

func TestMockSmartRuleAIProvider(t *testing.T) {
	provider := NewMockSmartRuleAIProvider()
	req := SmartRulePromptRequest{
		Transactions: []UnassignedTxInfo{
			{
				ID:       "01a17853-95ba-7253-a1c5-e41f05f1a769",
				RawPayee: "BIEDRONKA 1234 WROCLAW",
				Currency: "PLN",
				Amount:   "-45.20",
			},
			{
				ID:       "01a17853-95ba-7253-a1c5-e41f05f1a770",
				RawPayee: "UBER BV TRIP HELP.UBER.COM",
				Currency: "PLN",
				Amount:   "-19.99",
			},
		},
		AvailableBuckets: []string{"Groceries", "Transport"},
		ExistingParties:  []string{},
	}

	rules, err := provider.SuggestRules(context.Background(), req)
	if err != nil {
		t.Fatalf("SuggestRules failed: %v", err)
	}

	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	if rules[0].TargetPartyName != "Biedronka" || *rules[0].SuggestedBucket != "Groceries" {
		t.Errorf("unexpected first rule: %+v", rules[0])
	}
	if rules[1].TargetPartyName != "Uber" || *rules[1].SuggestedBucket != "Transport" {
		t.Errorf("unexpected second rule: %+v", rules[1])
	}
}
