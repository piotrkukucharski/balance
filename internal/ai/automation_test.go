package ai

import (
	"context"
	"testing"

	"github.com/balance/balance/internal/lua"
)

func TestMockAutomationAIProvider(t *testing.T) {
	ctx := context.Background()
	p := NewMockAutomationAIProvider()

	// 1. Judge generation
	j, err := p.GenerateJudge(ctx, "Categorize Uber trips", nil)
	if err != nil {
		t.Fatalf("GenerateJudge failed: %v", err)
	}
	if err := lua.ValidateJudgeScript(j); err != nil {
		t.Fatalf("generated invalid judge script %q: %v", j, err)
	}

	// 2. Executor generation
	e, err := p.GenerateExecutor(ctx, "Set bucket to Transport", nil)
	if err != nil {
		t.Fatalf("GenerateExecutor failed: %v", err)
	}
	if err := lua.ValidateLuaScript(e); err != nil {
		t.Fatalf("generated invalid executor script %q: %v", e, err)
	}

	// 3. Pair generation
	pair, err := p.GeneratePair(ctx, "When grocery shopping at Biedronka set bucket to Groceries", nil)
	if err != nil {
		t.Fatalf("GeneratePair failed: %v", err)
	}
	if err := lua.ValidateJudgeScript(pair.JudgeScript); err != nil {
		t.Fatalf("pair judge invalid: %v", err)
	}
	if err := lua.ValidateLuaScript(pair.ExecutorScript); err != nil {
		t.Fatalf("pair executor invalid: %v", err)
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
}
