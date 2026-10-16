package rule

import (
	"testing"
)

func TestMatchingEngine(t *testing.T) {
	bucketGroceries := "018d0000-0000-7000-8000-000000000010"
	zabkaAccount := "018d0000-0000-7000-8000-000000000020"

	rules := []*MatchingRule{
		{
			ID:                       "r1",
			Name:                     "Zabka Stores",
			Priority:                 10,
			MatchField:               FieldRawPayee,
			MatchOperator:            OpContains,
			MatchPattern:             "ZABKA",
			TargetCounterpartyAcctID: zabkaAccount,
			DefaultBucketID:          &bucketGroceries,
		},
		{
			ID:                       "r2",
			Name:                     "IBAN Match",
			Priority:                 20,
			MatchField:               FieldCounterpartyIBAN,
			MatchOperator:            OpExact,
			MatchPattern:             "PL123456789",
			TargetCounterpartyAcctID: "018d0000-0000-7000-8000-000000000030",
		},
	}

	engine, err := NewMatchingEngine(rules)
	if err != nil {
		t.Fatalf("NewMatchingEngine failed: %v", err)
	}

	// 1. Zabka test
	res := engine.Match("ZABKA Z1234 WARSZAWA", "", "Zakupy spozywcze")
	if res == nil {
		t.Fatalf("expected match for Zabka")
	}
	if res.TargetCounterpartyAcctID != zabkaAccount || *res.DefaultBucketID != bucketGroceries {
		t.Fatalf("unexpected match result: %+v", res)
	}

	// 2. IBAN test
	res2 := engine.Match("Random Payee", "PL123456789", "")
	if res2 == nil {
		t.Fatalf("expected match for IBAN")
	}
	if res2.TargetCounterpartyAcctID != "018d0000-0000-7000-8000-000000000030" {
		t.Fatalf("unexpected IBAN match result: %+v", res2)
	}

	// 3. Unmatched test
	res3 := engine.Match("Unknown Merchant", "", "")
	if res3 != nil {
		t.Fatalf("expected no match, got %+v", res3)
	}
}
