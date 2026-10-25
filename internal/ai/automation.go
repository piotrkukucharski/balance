package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/balance/balance/internal/lua"
)

// AutomationAIPairResult contains both judge and executor scripts generated from a user prompt.
type AutomationAIPairResult struct {
	JudgeScript    string `json:"judge_script"`
	ExecutorScript string `json:"executor_script"`
	Explanation    string `json:"explanation"`
}

// VersionCommentRequest encapsulates context needed to generate a smart version change comment.
type VersionCommentRequest struct {
	AutomationName    string   `json:"automation_name"`
	OldJudgeScript    string   `json:"old_judge_script"`
	NewJudgeScript    string   `json:"new_judge_script"`
	OldExecutorScript string   `json:"old_executor_script"`
	NewExecutorScript string   `json:"new_executor_script"`
	PreviousComments  []string `json:"previous_comments"`
}

// AutomationAIProvider generates scripts and version comments.
type AutomationAIProvider interface {
	GenerateJudge(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (string, error)
	GenerateExecutor(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (string, error)
	GeneratePair(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (*AutomationAIPairResult, error)
	GenerateComment(ctx context.Context, req VersionCommentRequest) (string, error)
}

// NewAutomationAIProvider constructs the active AutomationAIProvider based on configuration.
func NewAutomationAIProvider() AutomationAIProvider {
	apiKey := os.Getenv("GEMINI_API_KEY")
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = os.Getenv("AI_GEMINI_MODEL")
	}
	if os.Getenv("AI_PROVIDER") == "mock" || apiKey == "" {
		return NewMockAutomationAIProvider()
	}
	return NewGeminiAutomationProvider(apiKey, model, ResolveGeminiTimeout())
}

// MockAutomationAIProvider provides deterministic Lua scripts for tests and offline development.
type MockAutomationAIProvider struct{}

func NewMockAutomationAIProvider() *MockAutomationAIProvider {
	return &MockAutomationAIProvider{}
}

func (m *MockAutomationAIProvider) GenerateJudge(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (string, error) {
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "uber") || strings.Contains(lower, "transport") || strings.Contains(lower, "bolt") {
		return `text.contains(tx.notes, "Uber") or tx.party == "Uber"`, nil
	}
	if strings.Contains(lower, "biedronka") || strings.Contains(lower, "lidl") || strings.Contains(lower, "groceries") {
		return `text.contains(tx.notes, "Biedronka") or text.contains(tx.notes, "Lidl")`, nil
	}
	if strings.Contains(lower, "netflix") || strings.Contains(lower, "spotify") || strings.Contains(lower, "subscription") {
		return `text.contains(tx.notes, "Netflix") or text.contains(tx.notes, "Spotify")`, nil
	}
	return `text.contains(tx.notes, "` + strings.TrimSpace(prompt) + `")`, nil
}

func (m *MockAutomationAIProvider) GenerateExecutor(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (string, error) {
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "split") {
		return `tx.split({
    { amount = money(50), bucket = "Groceries" },
    { amount = money(50), bucket = "Household" }
})`, nil
	}
	if strings.Contains(lower, "delete") || strings.Contains(lower, "usuń") {
		return `tx.delete()`, nil
	}
	if strings.Contains(lower, "uber") || strings.Contains(lower, "transport") {
		return `tx.set_bucket("Transport")`, nil
	}
	if strings.Contains(lower, "groceries") || strings.Contains(lower, "jedzenie") {
		return `tx.set_bucket("Groceries")`, nil
	}
	return `tx.set_notes("Processed by automation")`, nil
}

func (m *MockAutomationAIProvider) GeneratePair(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (*AutomationAIPairResult, error) {
	j, _ := m.GenerateJudge(ctx, prompt, snap)
	e, _ := m.GenerateExecutor(ctx, prompt, snap)
	return &AutomationAIPairResult{
		JudgeScript:    j,
		ExecutorScript: e,
		Explanation:    fmt.Sprintf("Automatically generated rule for prompt: %q", prompt),
	}, nil
}

func (m *MockAutomationAIProvider) GenerateComment(ctx context.Context, req VersionCommentRequest) (string, error) {
	if req.NewJudgeScript != req.OldJudgeScript && req.NewExecutorScript != req.OldExecutorScript {
		return "Refined judge condition and updated execution actions", nil
	}
	if req.NewJudgeScript != req.OldJudgeScript {
		return "Adjusted trigger condition in judge script", nil
	}
	if req.NewExecutorScript != req.OldExecutorScript {
		return "Updated transaction modification rules in executor", nil
	}
	return "Minor optimization to automation script", nil
}

// GeminiAutomationProvider calls Google Gemini API with validation and fallback.
type GeminiAutomationProvider struct {
	apiKey     string
	model      string
	httpClient *http.Client
	timeout    time.Duration
}

func NewGeminiAutomationProvider(apiKey, model string, timeout time.Duration) *GeminiAutomationProvider {
	if model == "" {
		model = "gemini-3.8-flash"
	}
	return &GeminiAutomationProvider{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: timeout},
		timeout:    timeout,
	}
}

func (g *GeminiAutomationProvider) GenerateJudge(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (string, error) {
	sysPrompt := `You are a Lua script assistant for Balance financial automation.
Your task is to write ONLY a Lua conditional expression or snippet (Judge script).
The script MUST evaluate to a boolean (return true or return false, or a boolean expression).
Available variables:
- tx.notes (string)
- tx.amount (minor units, e.g. 15000 = 150.00 PLN)
- tx.currency (string)
- tx.date (YYYY-MM-DD string)
- tx.party (string payee/party name)
- tx.source_account (string)
- tx.dest_account (string)
- tx.bucket (string)
- tx.is_outflow, tx.is_inflow, tx.is_transfer (booleans)
- text.contains(str, substr), text.starts_with(str, prefix), text.regex_match(pattern, str)
- date.year(tx.date), date.month(tx.date), date.day(tx.date), date.weekday(tx.date)
Strict restrictions: NO loops (while, for), NO function declarations, NO '..' concat operator.
Output ONLY raw Lua code, without markdown formatting or code blocks.`

	raw, err := g.callGemini(ctx, sysPrompt, prompt)
	if err != nil {
		return NewMockAutomationAIProvider().GenerateJudge(ctx, prompt, snap)
	}
	clean := sanitizeLuaOutput(raw)
	if err := lua.ValidateJudgeScript(clean); err != nil {
		return NewMockAutomationAIProvider().GenerateJudge(ctx, prompt, snap)
	}
	return clean, nil
}

func (g *GeminiAutomationProvider) GenerateExecutor(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (string, error) {
	sysPrompt := `You are a Lua script assistant for Balance financial automation.
Your task is to write ONLY a Lua script (Executor script) executed when the Judge returns true.
Available mutators:
- tx.set_bucket("Bucket Name")
- tx.set_target("Target Name") (also assigns parent bucket automatically)
- tx.set_party("Party Name")
- tx.set_notes("Notes text")
- tx.set_amount(money(50.00)) or tx.set_amount(5000)
- tx.split({ { amount = money(20), bucket = "A" }, { amount = money(30), bucket = "B" } })
- tx.delete()
Strict restrictions: NO loops (while, for), NO function declarations, NO '..' concat operator.
Output ONLY raw Lua code, without markdown formatting or code blocks.`

	raw, err := g.callGemini(ctx, sysPrompt, prompt)
	if err != nil {
		return NewMockAutomationAIProvider().GenerateExecutor(ctx, prompt, snap)
	}
	clean := sanitizeLuaOutput(raw)
	if err := lua.ValidateLuaScript(clean); err != nil {
		return NewMockAutomationAIProvider().GenerateExecutor(ctx, prompt, snap)
	}
	return clean, nil
}

func (g *GeminiAutomationProvider) GeneratePair(ctx context.Context, prompt string, snap *lua.InMemoryBudgetSnapshot) (*AutomationAIPairResult, error) {
	sysPrompt := `You are a Lua script assistant for Balance financial automation.
From the user's rule description, return a JSON object with:
{
  "judge_script": "valid lua judge code returning boolean",
  "executor_script": "valid lua executor code",
  "explanation": "concise explanation"
}
Available: tx.notes, tx.amount, tx.party, tx.bucket, tx.is_outflow, text.contains, text.regex_match, date.*, tx.set_bucket, tx.set_target, tx.set_notes, tx.split, tx.delete.
Strict restrictions: NO loops, NO function declarations, NO '..' concat operator.
Return ONLY valid JSON.`

	raw, err := g.callGemini(ctx, sysPrompt, prompt)
	if err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, prompt, snap)
	}
	clean := sanitizeJSONOutput(raw)
	var res AutomationAIPairResult
	if err := json.Unmarshal([]byte(clean), &res); err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, prompt, snap)
	}
	if err := lua.ValidateJudgeScript(res.JudgeScript); err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, prompt, snap)
	}
	if err := lua.ValidateLuaScript(res.ExecutorScript); err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, prompt, snap)
	}
	return &res, nil
}

func (g *GeminiAutomationProvider) GenerateComment(ctx context.Context, req VersionCommentRequest) (string, error) {
	sysPrompt := `You are an automated changelog generator for financial automation scripts.
Given the previous code, updated code, and past version comments, produce a single concise sentence (max 100 characters) describing what changed in this version.
Return ONLY the comment text.`

	userMsg, _ := json.Marshal(req)
	raw, err := g.callGemini(ctx, sysPrompt, string(userMsg))
	if err != nil {
		return NewMockAutomationAIProvider().GenerateComment(ctx, req)
	}
	clean := strings.TrimSpace(raw)
	clean = strings.Trim(clean, `"'`)
	if len(clean) > 200 {
		clean = clean[:200]
	}
	return clean, nil
}

func (g *GeminiAutomationProvider) callGemini(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", g.model, g.apiKey)

	reqPayload := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{
				{"text": systemPrompt},
			},
		},
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]any{
					{"text": userPrompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"temperature": 0.1,
		},
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gemini api returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response candidates from gemini")
	}

	return strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text), nil
}

func sanitizeLuaOutput(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```lua") {
		s = strings.TrimPrefix(s, "```lua")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func sanitizeJSONOutput(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
