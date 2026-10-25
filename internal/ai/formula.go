package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/balance/balance/internal/logger"
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

func NewGeminiDirectHTTPProvider(apiKey, model string, timeout ...time.Duration) *GeminiDirectHTTPProvider {
	if model == "" {
		model = "gemini-3.8-flash"
	}
	t := ResolveGeminiTimeout()
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}
	return &GeminiDirectHTTPProvider{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: t},
	}
}

func (g *GeminiDirectHTTPProvider) ProviderName() string {
	return "GEMINI_DIRECT"
}

func (g *GeminiDirectHTTPProvider) GenerateLuaFormula(ctx context.Context, req FormulaGenerationRequest) (*FormulaGenerationResult, error) {
	ctx = logger.WithComponent(ctx, "ai:formula")
	if g.apiKey == "" {
		logger.Error(ctx, "GEMINI_API_KEY is not configured")
		return nil, errors.New("GEMINI_API_KEY is not configured")
	}

	baseURL := os.Getenv("GEMINI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", strings.TrimRight(baseURL, "/"), g.model)

	start := time.Now()
	timeoutSec := g.httpClient.Timeout.Seconds()
	logger.Info(ctx, "Starting Gemini Lua formula generation",
		"model", g.model,
		"user_prompt", req.UserPrompt,
		"bucket", req.CurrentBucketName,
		"currency", req.TargetCurrency,
		"timeout_seconds", timeoutSec,
	)

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

	rawJSON, err := json.Marshal(bodyData)
	if err != nil {
		logger.Error(ctx, "Failed marshaling Gemini formula request", "model", g.model, "error", err.Error())
		return nil, fmt.Errorf("failed marshaling Gemini formula request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(rawJSON)))
	if err != nil {
		logger.Error(ctx, "Failed creating Gemini formula HTTP request", "model", g.model, "error", err.Error())
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.httpClient.Do(httpReq)
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		logger.Error(ctx, "Gemini Lua formula HTTP request failed",
			"model", g.model,
			"error", err.Error(),
			"duration_ms", durationMs,
		)
		return nil, fmt.Errorf("gemini API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Error(ctx, "Gemini Lua formula API returned error status",
			"model", g.model,
			"status", resp.StatusCode,
			"body", string(respBody),
			"duration_ms", durationMs,
		)
		return nil, fmt.Errorf("gemini API returned status %d: %s", resp.StatusCode, string(respBody))
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
		logger.Error(ctx, "Failed decoding Gemini formula response", "model", g.model, "error", err.Error(), "duration_ms", durationMs)
		return nil, err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		logger.Error(ctx, "Gemini returned empty candidates for formula", "model", g.model, "duration_ms", durationMs)
		return nil, errors.New("empty response from Gemini")
	}

	text := geminiResp.Candidates[0].Content.Parts[0].Text
	var out FormulaGenerationResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		logger.Error(ctx, "Failed parsing Gemini formula JSON output", "model", g.model, "error", err.Error(), "duration_ms", durationMs)
		return nil, fmt.Errorf("failed parsing Gemini JSON output: %w", err)
	}

	logger.Info(ctx, "Gemini Lua formula generated successfully",
		"model", g.model,
		"duration_ms", durationMs,
	)
	return &out, nil
}
