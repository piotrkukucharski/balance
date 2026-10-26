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

// AutomationVersionSummary describes a version included in a multi-version squash.
type AutomationVersionSummary struct {
	Version        int    `json:"version"`
	Comment        string `json:"comment"`
	JudgeScript    string `json:"judge_script"`
	ExecutorScript string `json:"executor_script"`
}

// MultiVersionCommentRequest encapsulates context needed to generate an AI summary comment when squashing multiple versions.
type MultiVersionCommentRequest struct {
	AutomationName string                     `json:"automation_name"`
	StartVersion   int                        `json:"start_version"`
	EndVersion     int                        `json:"end_version"`
	Versions       []AutomationVersionSummary `json:"versions"`
}

// TxContextExample represents a transaction example used for few-shot prompting.
type TxContextExample struct {
	ID        string `json:"id,omitempty"`
	Date      string `json:"date,omitempty"`
	Amount    string `json:"amount,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Party     string `json:"party,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	IsOutflow bool   `json:"is_outflow,omitempty"`
}

// GenerateContext carries prompt plus optional few-shot transaction examples and existing code/test context.
type GenerateContext struct {
	Prompt            string             `json:"prompt"`
	ExistingJudge     string             `json:"existing_judge,omitempty"`
	ExistingExecutor  string             `json:"existing_executor,omitempty"`
	TestResults       string             `json:"test_results,omitempty"`
	ExampleTxs        []TxContextExample `json:"example_txs,omitempty"`
}

// AutomationAIProvider generates scripts and version comments.
type AutomationAIProvider interface {
	GenerateJudge(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (string, error)
	GenerateExecutor(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (string, error)
	GeneratePair(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (*AutomationAIPairResult, error)
	GenerateComment(ctx context.Context, req VersionCommentRequest) (string, error)
	GenerateMultiVersionComment(ctx context.Context, req MultiVersionCommentRequest) (string, error)
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

func (m *MockAutomationAIProvider) GenerateJudge(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (string, error) {
	lower := strings.ToLower(genCtx.Prompt)
	for _, ex := range genCtx.ExampleTxs {
		lower += " " + strings.ToLower(ex.Notes) + " " + strings.ToLower(ex.Party)
	}
	if genCtx.ExistingJudge != "" {
		lower += " " + strings.ToLower(genCtx.ExistingJudge)
	}

	if (strings.Contains(lower, "uber") || strings.Contains(lower, "transport")) && strings.Contains(lower, "bolt") {
		return `text.contains(tx.notes, "Uber") or text.contains(tx.notes, "Bolt")`, nil
	}
	if strings.Contains(lower, "uber") || strings.Contains(lower, "transport") || strings.Contains(lower, "bolt") {
		return `text.contains(tx.notes, "Uber") or tx.party == "Uber"`, nil
	}
	if strings.Contains(lower, "biedronka") || strings.Contains(lower, "lidl") || strings.Contains(lower, "groceries") {
		return `text.contains(tx.notes, "Biedronka") or text.contains(tx.notes, "Lidl")`, nil
	}
	if strings.Contains(lower, "netflix") || strings.Contains(lower, "spotify") || strings.Contains(lower, "subscription") {
		return `text.contains(tx.notes, "Netflix") or text.contains(tx.notes, "Spotify")`, nil
	}
	return `text.contains(tx.notes, "` + strings.TrimSpace(genCtx.Prompt) + `")`, nil
}

func (m *MockAutomationAIProvider) GenerateExecutor(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (string, error) {
	lower := strings.ToLower(genCtx.Prompt)
	if genCtx.ExistingExecutor != "" {
		lower += " " + strings.ToLower(genCtx.ExistingExecutor)
	}
	if strings.Contains(lower, "split") {
		return `tx.split({
    { amount = money(50), bucket = "Groceries" },
    { amount = money(50), bucket = "Household" }
})`, nil
	}
	if strings.Contains(lower, "delete") || strings.Contains(lower, "usuń") {
		return `tx.delete()`, nil
	}
	if strings.Contains(lower, "uber") || strings.Contains(lower, "transport") || strings.Contains(lower, "bolt") {
		return `tx.set_bucket("Transport")`, nil
	}
	if strings.Contains(lower, "groceries") || strings.Contains(lower, "jedzenie") {
		return `tx.set_bucket("Groceries")`, nil
	}
	return `tx.set_notes("Processed by automation")`, nil
}

func (m *MockAutomationAIProvider) GeneratePair(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (*AutomationAIPairResult, error) {
	j, _ := m.GenerateJudge(ctx, genCtx, snap)
	e, _ := m.GenerateExecutor(ctx, genCtx, snap)
	return &AutomationAIPairResult{
		JudgeScript:    j,
		ExecutorScript: e,
		Explanation:    fmt.Sprintf("Automatically generated rule for prompt: %q", genCtx.Prompt),
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

func (m *MockAutomationAIProvider) GenerateMultiVersionComment(ctx context.Context, req MultiVersionCommentRequest) (string, error) {
	if len(req.Versions) == 0 {
		return fmt.Sprintf("Squashed versions %d..%d", req.StartVersion, req.EndVersion), nil
	}
	first := req.Versions[0]
	last := req.Versions[len(req.Versions)-1]
	return fmt.Sprintf("Squashed versions %d..%d: consolidated rule logic from %q up to %q",
		req.StartVersion, req.EndVersion, first.Comment, last.Comment), nil
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

func buildUserPromptWithContext(genCtx GenerateContext) string {
	var sb strings.Builder
	sb.WriteString("User Rule Request:\n")
	sb.WriteString(genCtx.Prompt)
	sb.WriteString("\n\n")

	if genCtx.ExistingJudge != "" || genCtx.ExistingExecutor != "" {
		sb.WriteString("Existing Code Context:\n")
		if genCtx.ExistingJudge != "" {
			sb.WriteString("- Existing Judge Script:\n```lua\n")
			sb.WriteString(genCtx.ExistingJudge)
			sb.WriteString("\n```\n")
		}
		if genCtx.ExistingExecutor != "" {
			sb.WriteString("- Existing Executor Script:\n```lua\n")
			sb.WriteString(genCtx.ExistingExecutor)
			sb.WriteString("\n```\n")
		}
		sb.WriteString("\n")
	}

	if genCtx.TestResults != "" {
		sb.WriteString("Recent Test/Dry-Run Results:\n")
		sb.WriteString(genCtx.TestResults)
		sb.WriteString("\n\n")
	}

	if len(genCtx.ExampleTxs) > 0 {
		sb.WriteString("Few-Shot Example Transactions from User Budget:\n")
		for i, tx := range genCtx.ExampleTxs {
			sb.WriteString(fmt.Sprintf("%d. Date: %s | Amount: %s | Notes: %q | Party: %q | Bucket: %q\n",
				i+1, tx.Date, tx.Amount, tx.Notes, tx.Party, tx.Bucket))
		}
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String())
}

func (g *GeminiAutomationProvider) GenerateJudge(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (string, error) {
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
- text.contains(str, substr) -- checks if str contains substr (case-sensitive)
- text.starts_with(str, prefix) -- checks if str starts with prefix
- text.ends_with(str, suffix) -- checks if str ends with suffix
- text.regex_match(pattern, str) -- NOTE SIGNATURE: pattern first, str second! e.g. text.regex_match(".*(bolt|uber|freenow|taxi).*", text.lower(tx.notes))
- text.lower(str), text.upper(str), text.trim(str)
- date.year(tx.date), date.month(tx.date), date.day(tx.date), date.weekday(tx.date)
Strict restrictions: NO loops (while, for), NO function declarations, NO '..' concat operator.
Output ONLY raw Lua code, without markdown formatting or code blocks.`

	userPrompt := buildUserPromptWithContext(genCtx)
	raw, err := g.callGemini(ctx, sysPrompt, userPrompt)
	if err != nil {
		return NewMockAutomationAIProvider().GenerateJudge(ctx, genCtx, snap)
	}
	clean := sanitizeLuaOutput(raw)
	if err := lua.ValidateJudgeScript(clean); err != nil {
		return NewMockAutomationAIProvider().GenerateJudge(ctx, genCtx, snap)
	}
	return clean, nil
}

func (g *GeminiAutomationProvider) GenerateExecutor(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (string, error) {
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

	userPrompt := buildUserPromptWithContext(genCtx)
	raw, err := g.callGemini(ctx, sysPrompt, userPrompt)
	if err != nil {
		return NewMockAutomationAIProvider().GenerateExecutor(ctx, genCtx, snap)
	}
	clean := sanitizeLuaOutput(raw)
	if err := lua.ValidateLuaScript(clean); err != nil {
		return NewMockAutomationAIProvider().GenerateExecutor(ctx, genCtx, snap)
	}
	return clean, nil
}

func (g *GeminiAutomationProvider) GeneratePair(ctx context.Context, genCtx GenerateContext, snap *lua.InMemoryBudgetSnapshot) (*AutomationAIPairResult, error) {
	sysPrompt := `You are a Lua script assistant for Balance financial automation.
From the user's rule description, return a JSON object with:
{
  "judge_script": "valid lua judge code returning boolean",
  "executor_script": "valid lua executor code",
  "explanation": "concise explanation"
}
Available: tx.notes, tx.amount, tx.party, tx.bucket, tx.is_outflow, date.*, tx.set_bucket, tx.set_target, tx.set_notes, tx.split, tx.delete.
String helper functions in text module:
- text.contains(str, substr)
- text.starts_with(str, prefix)
- text.ends_with(str, suffix)
- text.regex_match(pattern, str) -- NOTE SIGNATURE: pattern first, str second! e.g. text.regex_match(".*(bolt|uber|freenow|taxi).*", text.lower(tx.notes))
- text.lower(str), text.upper(str), text.trim(str)
Strict restrictions: NO loops, NO function declarations, NO '..' concat operator.
Return ONLY valid JSON.`

	userPrompt := buildUserPromptWithContext(genCtx)
	raw, err := g.callGemini(ctx, sysPrompt, userPrompt)
	if err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, genCtx, snap)
	}
	clean := sanitizeJSONOutput(raw)
	var res AutomationAIPairResult
	if err := json.Unmarshal([]byte(clean), &res); err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, genCtx, snap)
	}
	if err := lua.ValidateJudgeScript(res.JudgeScript); err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, genCtx, snap)
	}
	if err := lua.ValidateLuaScript(res.ExecutorScript); err != nil {
		return NewMockAutomationAIProvider().GeneratePair(ctx, genCtx, snap)
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

func (g *GeminiAutomationProvider) GenerateMultiVersionComment(ctx context.Context, req MultiVersionCommentRequest) (string, error) {
	sysPrompt := `You are an automated changelog generator for financial automation scripts.
Given a list of consecutive versions of a transaction automation rule that are being squashed together, produce a single concise sentence (max 140 characters) summarizing the cumulative evolution and logic changes.
Return ONLY the comment text.`

	userMsg, _ := json.Marshal(req)
	raw, err := g.callGemini(ctx, sysPrompt, string(userMsg))
	if err != nil {
		return NewMockAutomationAIProvider().GenerateMultiVersionComment(ctx, req)
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
