package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/lua"
)

type BucketContextMeta struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

type TargetContextMeta struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	BucketName    string `json:"bucket_name"`
	Priority      int    `json:"priority"`
	Currency      string `json:"currency"`
	RequiredMinor int64  `json:"required_minor"`
	FundedMinor   int64  `json:"funded_minor"`
}

type FormulaGenerationRequest struct {
	UserPrompt        string              `json:"user_prompt"`
	CurrentBucketName string              `json:"current_bucket_name"`
	TargetCurrency    string              `json:"target_currency"`
	WindowDays        int                 `json:"window_days"`
	AvailableBuckets  []BucketContextMeta `json:"available_buckets"`
	AvailableTargets  []TargetContextMeta `json:"available_targets"`
	PreviousAttempt   string              `json:"previous_attempt,omitempty"`
	ValidationError   string              `json:"validation_error,omitempty"`
}

type FormulaGenerationResult struct {
	LuaScript   string `json:"lua_script"`
	Explanation string `json:"explanation"`
}

type FormulaAIProvider interface {
	ProviderName() string
	GenerateLuaFormula(ctx context.Context, req FormulaGenerationRequest) (*FormulaGenerationResult, error)
}

// MockFormulaAIProvider provides deterministic Lua formulas for tests and offline usage.
type MockFormulaAIProvider struct{}

func NewMockFormulaAIProvider() *MockFormulaAIProvider {
	return &MockFormulaAIProvider{}
}

func (m *MockFormulaAIProvider) ProviderName() string {
	return "MOCK"
}

func (m *MockFormulaAIProvider) GenerateLuaFormula(ctx context.Context, req FormulaGenerationRequest) (*FormulaGenerationResult, error) {
	promptLower := strings.ToLower(req.UserPrompt)

	if strings.Contains(promptLower, "mortgage") || strings.Contains(promptLower, "overpayment") {
		return &FormulaGenerationResult{
			LuaScript:   `return math.max(0, money(2000) - target("Required Mortgage Installment").required)`,
			Explanation: "Deduct the required mortgage installment from 2,000 PLN.",
		}, nil
	}

	if strings.Contains(promptLower, "income") || strings.Contains(promptLower, "save") {
		return &FormulaGenerationResult{
			LuaScript:   `return math.min(money(5000), math.floor(budget.income(30, "PLN") * 0.15))`,
			Explanation: "Save 15% of total income over the last 30 days, capped at 5,000 PLN.",
		}, nil
	}

	return &FormulaGenerationResult{
		LuaScript:   fmt.Sprintf("return money(500)"),
		Explanation: "Default fixed allocation of 500.",
	}, nil
}

// FormulaAssistantService orchestrates generation, sandbox validation, and the 1-retry self-correction loop.
type FormulaAssistantService struct {
	provider FormulaAIProvider
}

func NewFormulaAssistantService(provider FormulaAIProvider) *FormulaAssistantService {
	return &FormulaAssistantService{provider: provider}
}

func (s *FormulaAssistantService) GenerateAndValidateFormula(
	ctx context.Context,
	req FormulaGenerationRequest,
	snapshot *lua.InMemoryBudgetSnapshot,
) (*FormulaGenerationResult, int64, error) {
	if len(req.UserPrompt) > 500 {
		return nil, 0, errors.New("user prompt exceeds 500 characters limit")
	}

	// Attempt 1
	res, err := s.provider.GenerateLuaFormula(ctx, req)
	if err != nil {
		return nil, 0, err
	}

	// Validate & dry run Attempt 1
	val, err := lua.EvaluateFormula(ctx, res.LuaScript, snapshot)
	if err == nil {
		return res, val, nil
	}

	// Automatic Self-Correction Retry (Max 1 Retry)
	req.PreviousAttempt = res.LuaScript
	req.ValidationError = err.Error()

	resRetry, errRetry := s.provider.GenerateLuaFormula(ctx, req)
	if errRetry != nil {
		return nil, 0, fmt.Errorf("retry generation failed: %w", errRetry)
	}

	valRetry, errValRetry := lua.EvaluateFormula(ctx, resRetry.LuaScript, snapshot)
	if errValRetry != nil {
		return nil, 0, fmt.Errorf("self-corrected formula failed validation: %w", errValRetry)
	}

	return resRetry, valRetry, nil
}

// GeminiDirectHTTPProvider implements direct HTTP calls to Gemini REST API.
type GeminiDirectHTTPProvider struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewGeminiDirectHTTPProvider(apiKey, model string) *GeminiDirectHTTPProvider {
	if model == "" {
		model = "gemini-3.8-flash"
	}
	return &GeminiDirectHTTPProvider{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (g *GeminiDirectHTTPProvider) ProviderName() string {
	return "GEMINI_DIRECT"
}

func (g *GeminiDirectHTTPProvider) GenerateLuaFormula(ctx context.Context, req FormulaGenerationRequest) (*FormulaGenerationResult, error) {
	if g.apiKey == "" {
		return nil, errors.New("GEMINI_API_KEY is not configured")
	}
	// Real direct HTTP implementation calls Gemini REST API with Structured Outputs
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", g.model)

	systemInstruction := `You are a Lua 5.1 formula generator for the Balance budgeting engine. Return JSON {"lua_script": "...", "explanation": "..."}. You must end with return <int64_minor_units>. Strictly forbidden: while, repeat, for, goto, function, concatenation (..). Only allowed math: abs, ceil, floor, fmod, max, min, pow, sqrt.`

	bodyData := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": systemInstruction},
			},
		},
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": fmt.Sprintf("User prompt: %s\nBucket: %s, Currency: %s, Window: %d", req.UserPrompt, req.CurrentBucketName, req.TargetCurrency, req.WindowDays)},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"responseMimeType": "application/json",
			"temperature":      0.1,
		},
	}

	rawJSON, _ := json.Marshal(bodyData)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(rawJSON)))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API returned status %d", resp.StatusCode)
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
		return nil, err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, errors.New("empty response from Gemini")
	}

	text := geminiResp.Candidates[0].Content.Parts[0].Text
	var out FormulaGenerationResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("failed parsing Gemini JSON output: %w", err)
	}

	return &out, nil
}
