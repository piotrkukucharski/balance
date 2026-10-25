package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
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
	UserPrompt        string                    `json:"user_prompt"`
	CurrentBucketName string                    `json:"current_bucket_name"`
	TargetCurrency    string                    `json:"target_currency"`
	WindowDays        int                       `json:"window_days"`
	AvailableBuckets  []BucketContextMeta       `json:"available_buckets"`
	AvailableTargets  []TargetContextMeta       `json:"available_targets"`
	PreviousAttempt   string                    `json:"previous_attempt,omitempty"`
	ValidationError   string                    `json:"validation_error,omitempty"`
	TargetName        string                    `json:"target_name,omitempty"`
	AnchorDueDate     *int64                    `json:"anchor_due_date,omitempty"`
	DueDate           string                    `json:"due_date,omitempty"`
	TargetCtx         *lua.TargetFormulaContext `json:"target_ctx,omitempty"`
}

type FormulaGenerationResult struct {
	LuaScript   string `json:"lua_script"`
	Explanation string `json:"explanation"`
}

type FormulaAIProvider interface {
	ProviderName() string
	GenerateLuaFormula(ctx context.Context, req FormulaGenerationRequest) (*FormulaGenerationResult, error)
}

// NewFormulaAIProvider creates a concrete Gemini or mock FormulaAIProvider based on environment variables.
func NewFormulaAIProvider() FormulaAIProvider {
	apiKey := os.Getenv("GEMINI_API_KEY")
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = os.Getenv("AI_GEMINI_MODEL")
	}
	if os.Getenv("AI_PROVIDER") == "mock" || apiKey == "" {
		return NewMockFormulaAIProvider()
	}
	return NewGeminiDirectHTTPProvider(apiKey, model, ResolveGeminiTimeout())
}

// MockFormulaAIProvider provides deterministic Lua formulas for tests and offline usage.
type MockFormulaAIProvider struct{}

func NewMockFormulaAIProvider() *MockFormulaAIProvider {
	return &MockFormulaAIProvider{}
}

func (m *MockFormulaAIProvider) ProviderName() string {
	return "MOCK"
}

var reAmountOnly = regexp.MustCompile(`(?i)(\d+(?:[\s.,]\d+)*)\s*(?:zł|pln|eur|usd)?`)

func (m *MockFormulaAIProvider) GenerateLuaFormula(ctx context.Context, req FormulaGenerationRequest) (*FormulaGenerationResult, error) {
	promptLower := strings.ToLower(req.UserPrompt)

	if strings.Contains(promptLower, "overpayment") || strings.Contains(promptLower, "nadpłat") {
		return &FormulaGenerationResult{
			LuaScript:   `return math.max(0, money(2000) - target("Required Mortgage Installment").required)`,
			Explanation: "Deduct the required mortgage installment from 2,000 PLN.",
		}, nil
	}

	if strings.Contains(promptLower, "pmt") || strings.Contains(promptLower, "kredyt") || strings.Contains(promptLower, "rata") || strings.Contains(promptLower, "raty") || strings.Contains(promptLower, "ratę") || strings.Contains(promptLower, "loan") || strings.Contains(promptLower, "hipotek") || (strings.Contains(promptLower, "mortgage") && !strings.Contains(promptLower, "overpayment")) {
		pv := int64(300000)
		amtMatch := reAmountOnly.FindStringSubmatch(req.UserPrompt)
		if len(amtMatch) >= 2 {
			amtStr := strings.ReplaceAll(amtMatch[1], " ", "")
			amtStr = strings.ReplaceAll(amtStr, ",", ".")
			if amt, err := strconv.ParseFloat(amtStr, 64); err == nil && amt >= 1000 {
				pv = int64(amt)
			}
		}
		rateAnnual := 0.06
		if strings.Contains(promptLower, "5%") || strings.Contains(promptLower, "0.05") {
			rateAnnual = 0.05
		} else if strings.Contains(promptLower, "7%") || strings.Contains(promptLower, "0.07") {
			rateAnnual = 0.07
		} else if strings.Contains(promptLower, "8%") || strings.Contains(promptLower, "0.08") {
			rateAnnual = 0.08
		} else if strings.Contains(promptLower, "6%") || strings.Contains(promptLower, "0.06") {
			rateAnnual = 0.06
		}

		rateStr := "0.06 / 12"
		if rateAnnual == 0.05 {
			rateStr = "0.05 / 12"
		} else if rateAnnual == 0.07 {
			rateStr = "0.07 / 12"
		} else if rateAnnual == 0.08 {
			rateStr = "0.08 / 12"
		}

		script := fmt.Sprintf("local nper = date.diff_months(self.anchor_due_date, runtime.today)\nlocal r = %s\nlocal pv = %d\nlocal pmt = -financial.pmt(r, nper, pv)\nreturn money(pmt)", rateStr, pv)
		return &FormulaGenerationResult{
			LuaScript:   script,
			Explanation: fmt.Sprintf("Calculate monthly loan payment using financial.pmt for %d principal at %.1f%% annual rate up to target anchor due date.", pv, rateAnnual*100),
		}, nil
	}

	if strings.Contains(promptLower, "income") || strings.Contains(promptLower, "save") || strings.Contains(promptLower, "oszczędz") || strings.Contains(promptLower, "przychód") {
		return &FormulaGenerationResult{
			LuaScript:   `return math.min(money(5000), math.floor(budget.income(30, "PLN") * 0.15))`,
			Explanation: "Save 15% of total income over the last 30 days, capped at 5,000 PLN.",
		}, nil
	}

	// Periodic / recurrence parsing
	if strings.Contains(promptLower, "co") || strings.Contains(promptLower, "kwartal") || strings.Contains(promptLower, "roczn") || strings.Contains(promptLower, "miesięcz") {
		amtMatch := reAmountOnly.FindStringSubmatch(req.UserPrompt)
		if len(amtMatch) >= 2 {
			amtStr := strings.ReplaceAll(amtMatch[1], ",", ".")
			amt, err := strconv.ParseFloat(amtStr, 64)
			if err == nil && amt > 0 {
				cycleDays := 0
				if strings.Contains(promptLower, "co 3") || strings.Contains(promptLower, "co trzy") || strings.Contains(promptLower, "kwartal") {
					cycleDays = 90
				} else if strings.Contains(promptLower, "co 6") || strings.Contains(promptLower, "co sześć") || strings.Contains(promptLower, "pół roku") {
					cycleDays = 180
				} else if strings.Contains(promptLower, "co 2") || strings.Contains(promptLower, "co dwa") {
					cycleDays = 60
				} else if strings.Contains(promptLower, "co miesiąc") || strings.Contains(promptLower, "co 1 miesiąc") || strings.Contains(promptLower, "miesięcz") {
					cycleDays = 30
				} else if strings.Contains(promptLower, "co rok") || strings.Contains(promptLower, "roczn") {
					cycleDays = 365
				} else if strings.Contains(promptLower, "co tydzień") || strings.Contains(promptLower, "co 1 tydzień") {
					cycleDays = 7
				}

				if cycleDays > 0 {
					var amtFormatted string
					if amt == float64(int64(amt)) {
						amtFormatted = fmt.Sprintf("%d", int64(amt))
					} else {
						amtFormatted = fmt.Sprintf("%.2f", amt)
					}
					return &FormulaGenerationResult{
						LuaScript:   fmt.Sprintf("return math.ceil(money(%s) * window.days / %d)", amtFormatted, cycleDays),
						Explanation: fmt.Sprintf("Calculates proportional allocation for %s PLN expense every %d days based on window.", amtFormatted, cycleDays),
					}, nil
				}
			}
		}
	}

	// Fixed amount parsing if user specifies an amount
	amtMatch := reAmountOnly.FindStringSubmatch(req.UserPrompt)
	if len(amtMatch) >= 2 {
		amtStr := strings.ReplaceAll(amtMatch[1], ",", ".")
		amt, err := strconv.ParseFloat(amtStr, 64)
		if err == nil && amt > 0 {
			var amtFormatted string
			if amt == float64(int64(amt)) {
				amtFormatted = fmt.Sprintf("%d", int64(amt))
			} else {
				amtFormatted = fmt.Sprintf("%.2f", amt)
			}
			return &FormulaGenerationResult{
				LuaScript:   fmt.Sprintf("return money(%s)", amtFormatted),
				Explanation: fmt.Sprintf("Fixed allocation of %s.", amtFormatted),
			}, nil
		}
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
	targetCtxOpt ...*lua.TargetFormulaContext,
) (*FormulaGenerationResult, int64, error) {
	if len(req.UserPrompt) > 500 {
		return nil, 0, errors.New("user prompt exceeds 500 characters limit")
	}

	var targetCtx *lua.TargetFormulaContext
	if len(targetCtxOpt) > 0 && targetCtxOpt[0] != nil {
		targetCtx = targetCtxOpt[0]
	} else if req.TargetCtx != nil {
		targetCtx = req.TargetCtx
	} else if req.TargetName != "" || req.AnchorDueDate != nil || req.DueDate != "" {
		var anchorSec *int64 = req.AnchorDueDate
		if anchorSec == nil && req.DueDate != "" {
			if t, err := time.Parse("2006-01-02", req.DueDate); err == nil {
				sec := t.Unix()
				anchorSec = &sec
			}
		}
		targetCtx = &lua.TargetFormulaContext{
			Name:          req.TargetName,
			Currency:      req.TargetCurrency,
			AnchorDueDate: anchorSec,
		}
	}

	// Attempt 1
	res, err := s.provider.GenerateLuaFormula(ctx, req)
	if err != nil {
		return nil, 0, err
	}

	// Validate & dry run Attempt 1
	val, err := lua.EvaluateFormulaWithContext(ctx, res.LuaScript, snapshot, targetCtx)
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

	valRetry, errValRetry := lua.EvaluateFormulaWithContext(ctx, resRetry.LuaScript, snapshot, targetCtx)
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

const formulaSystemInstruction = `You are an expert Lua 5.1 formula generator for the Balance budgeting engine.
Return strictly JSON matching: {"lua_script": "string", "explanation": "string"}.
The script must end with return <int64_minor_units>, returning a single non-negative integer in minor units (e.g. 1 PLN = 100 minor units).

Available helper functions and runtime environment:
- money(val): converts major currency units (number or string) into integer minor units (e.g. money(125) -> 12500, money("19.99") -> 1999).
- target("name").required, .funded, .planned, .spent, .priority, .anchor_due_date: inspects another target in the budget (anchor_due_date is Unix epoch in seconds).
- self.name, .planned, .required, .funded, .spent, .priority, .anchor_due_date: inspects the current target being evaluated (anchor_due_date is Unix epoch in seconds).
- bucket.balance(currency?): returns current bucket balance in minor units.
- budget.income(days, currency?): external inflows over last N days (1 <= days <= 3650).
- budget.spent(days, currency?): external outflows over last N days (1 <= days <= 3650).
- window.days: active horizon window in days (default 30).
- runtime.today: Unix timestamp in seconds representing the start of today (00:00:00) in the budget's timezone.
- financial.pmt(rate, nper, pv, [fv], [type]): computes payment for a loan based on constant payments and interest rate (Excel PMT convention). Use -financial.pmt(...) for positive installment amount.
- date library:
  - date.diff_months(t1, t2): difference in whole months between t1 and t2 (t1 - t2).
  - date.year(t), date.month(t), date.day(t), date.weekday(t)
  - date.format(t, layout?): formats date (default "2006-01-02")
  - date.parse(str, layout?): parses string into Unix microsecond timestamp
- fx library:
  - fx.rate(base, target): returns exchange rate as a float number
  - fx.convert(amountMinor, base, target): converts minor units from base to target currency
- Whitelisted math: math.max, math.min, math.floor, math.ceil, math.abs, math.fmod, math.pow, math.sqrt, tonumber.

Formula rules & best practices:
- Always use money(val) for currency amounts (never raw minor units directly unless using existing target/bucket properties or financial.pmt output).
- For loan / mortgage installments with financial.pmt (e.g. calculating monthly payment to maturity using self.anchor_due_date and runtime.today):
  local nper = date.diff_months(self.anchor_due_date, runtime.today)
  local r = 0.06 / 12
  local pv = 300000
  local pmt = -financial.pmt(r, nper, pv)
  return money(pmt)
- For recurring expenses with a cycle of N days or months (e.g. "125 zł płatne co 3 miesiące / kwartalnie"):
  Calculate the proportional savings allocation for the active window:
  math.ceil(money(125) * window.days / 90)
- For complementary targets (e.g. total mortgage payment of 2000 PLN minus required installment):
  math.max(0, money(2000) - target("Required Mortgage Installment").required)
- For percentage of income savings (e.g. save 15% of past 30 days income capped at 5000 PLN):
  math.min(money(5000), math.floor(budget.income(30, "PLN") * 0.15))
- For multi-currency conversion:
  return fx.convert(money(100), "EUR", "PLN")
- For fixed amounts:
  money(100)

Strictly forbidden constructs (will be rejected by compiler):
- Loops: while, repeat, for
- User-defined functions: function ... end
- Jumps: goto
- String concatenation: ..
- Unsafe libraries: io, os, debug, string, table, loadstring, dofile`

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

	promptBuilder := strings.Builder{}
	fmt.Fprintf(&promptBuilder, "User prompt: %s\nBucket: %s, Currency: %s, Window: %d", req.UserPrompt, req.CurrentBucketName, req.TargetCurrency, req.WindowDays)
	if req.TargetName != "" {
		fmt.Fprintf(&promptBuilder, "\nTarget Name: %s", req.TargetName)
	}
	if req.DueDate != "" {
		fmt.Fprintf(&promptBuilder, "\nTarget Due Date: %s", req.DueDate)
	} else if req.AnchorDueDate != nil {
		fmt.Fprintf(&promptBuilder, "\nTarget Anchor Due Date (epoch seconds): %d", *req.AnchorDueDate)
	}

	if len(req.AvailableTargets) > 0 {
		targetsJSON, _ := json.Marshal(req.AvailableTargets)
		fmt.Fprintf(&promptBuilder, "\n\n<untrusted_budget_metadata_json>\nAvailable Targets in budget: %s\n</untrusted_budget_metadata_json>", string(targetsJSON))
	}

	if req.PreviousAttempt != "" {
		fmt.Fprintf(&promptBuilder, "\n\nPrevious attempt failed with error:\nPrevious script: %s\nValidation Error: %s\nPlease fix the script so that it passes validation.", req.PreviousAttempt, req.ValidationError)
	}

	responseSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"lua_script": map[string]interface{}{
				"type": "string",
			},
			"explanation": map[string]interface{}{
				"type": "string",
			},
		},
		"required": []string{"lua_script", "explanation"},
	}

	bodyData := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": formulaSystemInstruction},
			},
		},
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": promptBuilder.String()},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"responseMimeType": "application/json",
			"responseSchema":   responseSchema,
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
