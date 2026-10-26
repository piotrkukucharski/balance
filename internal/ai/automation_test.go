package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/balance/balance/internal/lua"
)

func TestMockAutomationAIProvider(t *testing.T) {
	ctx := context.Background()
	p := NewMockAutomationAIProvider()

	// 1. Judge generation
	j, err := p.GenerateJudge(ctx, GenerateContext{Prompt: "Categorize Uber trips"}, nil)
	if err != nil {
		t.Fatalf("GenerateJudge failed: %v", err)
	}
	if err := lua.ValidateJudgeScript(j); err != nil {
		t.Fatalf("generated invalid judge script %q: %v", j, err)
	}

	// 2. Executor generation
	e, err := p.GenerateExecutor(ctx, GenerateContext{Prompt: "Set bucket to Transport"}, nil)
	if err != nil {
		t.Fatalf("GenerateExecutor failed: %v", err)
	}
	if err := lua.ValidateLuaScript(e); err != nil {
		t.Fatalf("generated invalid executor script %q: %v", e, err)
	}

	// 3. Pair generation
	pair, err := p.GeneratePair(ctx, GenerateContext{Prompt: "When grocery shopping at Biedronka set bucket to Groceries"}, nil)
	if err != nil {
		t.Fatalf("GeneratePair failed: %v", err)
	}
	if err := lua.ValidateJudgeScript(pair.JudgeScript); err != nil {
		t.Fatalf("pair judge invalid: %v", err)
	}
	if err := lua.ValidateLuaScript(pair.ExecutorScript); err != nil {
		t.Fatalf("pair executor invalid: %v", err)
	}

	// 3b. Pair generation with few-shot context and existing judge
	pairWithContext, err := p.GeneratePair(ctx, GenerateContext{
		Prompt:        "Support Bolt rides too",
		ExistingJudge: `text.contains(tx.notes, "Uber")`,
		ExampleTxs: []TxContextExample{
			{Notes: "Bolt ride to town", Party: "Bolt", Bucket: "Transport"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("GeneratePair with context failed: %v", err)
	}
	if !strings.Contains(pairWithContext.JudgeScript, "Bolt") {
		t.Fatalf("expected judge script to contain Bolt, got: %s", pairWithContext.JudgeScript)
	}

	// 4. Comment generation
	comment, err := p.GenerateComment(ctx, VersionCommentRequest{
		AutomationName:   "Uber rule",
		OldJudgeScript:   `text.contains(tx.notes, "Uber")`,
		NewJudgeScript:   `text.contains(tx.notes, "Uber") or tx.party == "Uber"`,
		PreviousComments: []string{"initial"},
	})
	if err != nil {
		t.Fatalf("GenerateComment failed: %v", err)
	}
	if comment == "" {
		t.Fatalf("expected non-empty comment")
	}

	// 5. Multi-version squash comment generation
	multiComment, err := p.GenerateMultiVersionComment(ctx, MultiVersionCommentRequest{
		AutomationName: "Uber rule",
		StartVersion:   2,
		EndVersion:     4,
		Versions: []AutomationVersionSummary{
			{Version: 2, Comment: "Add Bolt support", JudgeScript: `text.contains(tx.notes, "Bolt")`},
			{Version: 3, Comment: "Add FreeNow support", JudgeScript: `text.contains(tx.notes, "FreeNow")`},
			{Version: 4, Comment: "Refine Transport bucket", ExecutorScript: `tx.set_bucket("Transport")`},
		},
	})
	if err != nil {
		t.Fatalf("GenerateMultiVersionComment failed: %v", err)
	}
	if multiComment == "" {
		t.Fatalf("expected non-empty multiComment")
	}
}
