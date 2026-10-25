package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ProposedTarget represents an individual target generated from AI prompt or budget export.
type ProposedTarget struct {
	Name                string `json:"name"`
	TargetType          string `json:"target_type"`          // EXPENSE, SAVINGS, DEBT_PAYMENT
	Priority            int    `json:"priority"`             // 1=Optional, 2=Recommended, 3=Mandatory
	AmountMinor         int64  `json:"amount_minor"`         // minor units (cents/grosze)
	Currency            string `json:"currency"`
	RecurrenceCadence   string `json:"recurrence_cadence"`   // ONE_OFF, WEEKLY, MONTHLY, ANNUAL, CUSTOM_DAYS
	NonSpendingStrategy string `json:"non_spending_strategy"` // ROLL_OVER, RETURN_TO_POOL, ARREARS_CUMULATIVE, CAP_AND_HOLD, PRORATE_FUTURE
	DueDateDay          *int   `json:"due_date_day,omitempty"`
}

// ProposedBucket represents an envelope/bucket containing proposed targets.
type ProposedBucket struct {
	Name    string           `json:"name"`
	Targets []ProposedTarget `json:"targets"`
}

// ProposedGroup represents a logical group of buckets (e.g., Housing, Living, Savings).
type ProposedGroup struct {
	GroupName string           `json:"group_name"`
	Buckets   []ProposedBucket `json:"buckets"`
}

// BudgetStructure holds the complete hierarchy of groups, buckets, and targets.
type BudgetStructure struct {
	Groups []ProposedGroup `json:"groups"`
}

// BudgetStructurePromptRequest contains the user's natural language description and budget context.
type BudgetStructurePromptRequest struct {
	Prompt          string `json:"prompt"`
	DefaultCurrency string `json:"default_currency"`
}

// BudgetStructureAIProvider is the interface for AI-based budget structure generators.
type BudgetStructureAIProvider interface {
	ProviderName() string
	GenerateBudgetStructure(ctx context.Context, req BudgetStructurePromptRequest) (*BudgetStructure, error)
}

// NewBudgetStructureProvider creates a concrete or mock AI provider based on environment variables.
func NewBudgetStructureProvider() BudgetStructureAIProvider {
	apiKey := os.Getenv("GEMINI_API_KEY")
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = os.Getenv("AI_GEMINI_MODEL")
	}
	if os.Getenv("AI_PROVIDER") == "mock" || apiKey == "" {
		return NewMockBudgetStructureAIProvider()
	}
	return NewGeminiBudgetStructureProvider(apiKey, model)
}

// MockBudgetStructureAIProvider provides deterministic budget structure generation for tests & offline mode.
type MockBudgetStructureAIProvider struct{}

func NewMockBudgetStructureAIProvider() *MockBudgetStructureAIProvider {
	return &MockBudgetStructureAIProvider{}
}

func (m *MockBudgetStructureAIProvider) ProviderName() string {
	return "MOCK"
}

func (m *MockBudgetStructureAIProvider) GenerateBudgetStructure(ctx context.Context, req BudgetStructurePromptRequest) (*BudgetStructure, error) {
	curr := strings.ToUpper(strings.TrimSpace(req.DefaultCurrency))
	if curr == "" {
		curr = "PLN"
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("empty budget description prompt")
	}

	promptLower := strings.ToLower(prompt)

	// Custom parsing if user enters structured lines or comma-separated items like "czynsz 2500, jedzenie 1200", etc.
	reSplit := regexp.MustCompile(`[\n,;]+`)
	lines := reSplit.Split(prompt, -1)
	var customBuckets []ProposedBucket
	reLine := regexp.MustCompile(`(?i)(?:^|[-*•]\s*)([a-zA-ZąćęłńóśźżĄĆĘŁŃÓŚŹŻ\s/]+?)[:\s]+(?:(\d+(?:[.,]\d+)?)\s*([a-zA-Z]{3})?)`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		matches := reLine.FindStringSubmatch(line)
		if len(matches) >= 3 {
			bName := strings.TrimSpace(matches[1])
			runes := []rune(bName)
			if len(runes) > 0 {
				runes[0] = unicode.ToUpper(runes[0])
				bName = string(runes)
			}
			amtStr := strings.ReplaceAll(matches[2], ",", ".")
			amtFloat, err := strconv.ParseFloat(amtStr, 64)
			if err == nil && amtFloat > 0 && bName != "" {
				targetCurr := curr
				if len(matches) >= 4 && matches[3] != "" {
					targetCurr = strings.ToUpper(matches[3])
				}
				amountMinor := int64(amtFloat * 100)

				prio := 2
				strategy := "RETURN_TO_POOL"
				tType := "EXPENSE"
				lowerName := strings.ToLower(bName)
				if strings.Contains(lowerName, "kredyt") || strings.Contains(lowerName, "czynsz") || strings.Contains(lowerName, "rachunk") || strings.Contains(lowerName, "rent") {
					prio = 3
					strategy = "RETURN_TO_POOL"
					if strings.Contains(lowerName, "kredyt") {
						tType = "DEBT_PAYMENT"
					}
				} else if strings.Contains(lowerName, "oszczędn") || strings.Contains(lowerName, "saving") || strings.Contains(lowerName, "inwestyc") {
					prio = 2
					strategy = "ROLL_OVER"
					tType = "SAVINGS"
				}

				customBuckets = append(customBuckets, ProposedBucket{
					Name: bName,
					Targets: []ProposedTarget{
						{
							Name:                bName,
							TargetType:          tType,
							Priority:            prio,
							AmountMinor:         amountMinor,
							Currency:            targetCurr,
							RecurrenceCadence:   "MONTHLY",
							NonSpendingStrategy: strategy,
						},
					},
				})
			}
		}
	}

	if len(customBuckets) >= 2 {
		return &BudgetStructure{
			Groups: []ProposedGroup{
				{
					GroupName: "Generated Categories",
					Buckets:   customBuckets,
				},
			},
		}, nil
	}

	// Deterministic Mock presets for common scenarios
	if strings.Contains(promptLower, "student") {
		return &BudgetStructure{
			Groups: []ProposedGroup{
				{
					GroupName: "Housing & Living",
					Buckets: []ProposedBucket{
						{
							Name: "Dormitory / Rent",
							Targets: []ProposedTarget{
								{Name: "Room Rent", TargetType: "EXPENSE", Priority: 3, AmountMinor: 90000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
							},
						},
						{
							Name: "Groceries",
							Targets: []ProposedTarget{
								{Name: "Food & Meals", TargetType: "EXPENSE", Priority: 2, AmountMinor: 70000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
							},
						},
					},
				},
				{
					GroupName: "Study & Life",
					Buckets: []ProposedBucket{
						{
							Name: "Transport",
							Targets: []ProposedTarget{
								{Name: "Student Semester Pass", TargetType: "EXPENSE", Priority: 2, AmountMinor: 6000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
							},
						},
						{
							Name: "Emergency Fund",
							Targets: []ProposedTarget{
								{Name: "Safety Cushion", TargetType: "SAVINGS", Priority: 1, AmountMinor: 10000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "ROLL_OVER"},
							},
						},
					},
				},
			},
		}, nil
	}

	// Standard Polish / Universal Family & Personal Budget preset
	isPolish := strings.Contains(promptLower, "mieszkan") || strings.Contains(promptLower, "rodzin") ||
		strings.Contains(promptLower, "kredyt") || strings.Contains(promptLower, "rachunk") ||
		strings.Contains(promptLower, "auto") || strings.Contains(promptLower, "jedzen") ||
		curr == "PLN"

	if isPolish {
		return &BudgetStructure{
			Groups: []ProposedGroup{
				{
					GroupName: "Koszty stałe",
					Buckets: []ProposedBucket{
						{
							Name: "Mieszkanie i czynsz",
							Targets: []ProposedTarget{
								{Name: "Czynsz i media", TargetType: "EXPENSE", Priority: 3, AmountMinor: 250000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
							},
						},
						{
							Name: "Rachunki i subskrypcje",
							Targets: []ProposedTarget{
								{Name: "Internet, prąd, telefon", TargetType: "EXPENSE", Priority: 3, AmountMinor: 35000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
							},
						},
					},
				},
				{
					GroupName: "Życie codzienne",
					Buckets: []ProposedBucket{
						{
							Name: "Zakupy spożywcze",
							Targets: []ProposedTarget{
								{Name: "Żywność i chemia domowa", TargetType: "EXPENSE", Priority: 2, AmountMinor: 160000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
							},
						},
						{
							Name: "Transport i paliwo",
							Targets: []ProposedTarget{
								{Name: "Paliwo / bilety", TargetType: "EXPENSE", Priority: 2, AmountMinor: 45000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
							},
						},
					},
				},
				{
					GroupName: "Oszczędności i cele",
					Buckets: []ProposedBucket{
						{
							Name: "Poduszka finansowa",
							Targets: []ProposedTarget{
								{Name: "Fundusz awaryjny", TargetType: "SAVINGS", Priority: 2, AmountMinor: 50000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "ROLL_OVER"},
							},
						},
						{
							Name: "Wakacje",
							Targets: []ProposedTarget{
								{Name: "Urlop letni", TargetType: "SAVINGS", Priority: 1, AmountMinor: 30000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "ROLL_OVER"},
							},
						},
					},
				},
			},
		}, nil
	}

	// English universal preset
	return &BudgetStructure{
		Groups: []ProposedGroup{
			{
				GroupName: "Fixed Obligations",
				Buckets: []ProposedBucket{
					{
						Name: "Housing",
						Targets: []ProposedTarget{
							{Name: "Rent / Mortgage", TargetType: "EXPENSE", Priority: 3, AmountMinor: 120000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
						},
					},
					{
						Name: "Utilities",
						Targets: []ProposedTarget{
							{Name: "Electricity, Water, Internet", TargetType: "EXPENSE", Priority: 3, AmountMinor: 25000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
						},
					},
				},
			},
			{
				GroupName: "Daily Living",
				Buckets: []ProposedBucket{
					{
						Name: "Groceries",
						Targets: []ProposedTarget{
							{Name: "Food & Household Goods", TargetType: "EXPENSE", Priority: 2, AmountMinor: 60000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
						},
					},
					{
						Name: "Transportation",
						Targets: []ProposedTarget{
							{Name: "Fuel / Transit Pass", TargetType: "EXPENSE", Priority: 2, AmountMinor: 20000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "RETURN_TO_POOL"},
						},
					},
				},
			},
			{
				GroupName: "Savings & Goals",
				Buckets: []ProposedBucket{
					{
						Name: "Emergency Fund",
						Targets: []ProposedTarget{
							{Name: "Emergency Reserve", TargetType: "SAVINGS", Priority: 2, AmountMinor: 30000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "ROLL_OVER"},
						},
					},
					{
						Name: "Vacation",
						Targets: []ProposedTarget{
							{Name: "Holiday Trip", TargetType: "SAVINGS", Priority: 1, AmountMinor: 20000, Currency: curr, RecurrenceCadence: "MONTHLY", NonSpendingStrategy: "ROLL_OVER"},
						},
					},
				},
			},
		},
	}, nil
}

// GeminiBudgetStructureProvider implements direct HTTP calls to Gemini REST API with Structured Outputs.
type GeminiBudgetStructureProvider struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewGeminiBudgetStructureProvider(apiKey, model string) *GeminiBudgetStructureProvider {
	if model == "" {
		model = "gemini-3.8-flash"
	}
	return &GeminiBudgetStructureProvider{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (g *GeminiBudgetStructureProvider) ProviderName() string {
	return "GEMINI_DIRECT"
}

func (g *GeminiBudgetStructureProvider) GenerateBudgetStructure(ctx context.Context, req BudgetStructurePromptRequest) (*BudgetStructure, error) {
	if g.apiKey == "" {
		return nil, errors.New("GEMINI_API_KEY is not configured")
	}

	curr := strings.ToUpper(strings.TrimSpace(req.DefaultCurrency))
	if curr == "" {
		curr = "PLN"
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", g.model)

	systemInstruction := `You are an expert personal finance and envelope budgeting assistant for the Balance app.
Analyze the user's natural language budget description and create a realistic, well-organized budget envelope structure.
Break the budget down into logical groups (e.g. Housing, Living Expenses, Savings, Utilities), buckets (envelopes), and targets.
For each target:
- name: clear descriptive title
- target_type: EXPENSE, SAVINGS, or DEBT_PAYMENT
- priority: 3 (mandatory/essential: rent, debt, utilities), 2 (recommended: groceries, transit, savings), or 1 (optional/lifestyle)
- amount_minor: target amount in minor currency units (e.g. 2500 PLN = 250000)
- currency: default to budget currency unless explicitly stated otherwise
- recurrence_cadence: MONTHLY, WEEKLY, ANNUAL, or ONE_OFF (default to MONTHLY)
- non_spending_strategy: ROLL_OVER for savings/goals, RETURN_TO_POOL for variable monthly living expenses

Return JSON strictly matching the BudgetStructure schema.`

	responseSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"groups": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"group_name": map[string]interface{}{"type": "string"},
						"buckets": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"name": map[string]interface{}{"type": "string"},
									"targets": map[string]interface{}{
										"type": "array",
										"items": map[string]interface{}{
											"type": "object",
											"properties": map[string]interface{}{
												"name":                  map[string]interface{}{"type": "string"},
												"target_type":           map[string]interface{}{"type": "string", "enum": []string{"EXPENSE", "SAVINGS", "DEBT_PAYMENT"}},
												"priority":              map[string]interface{}{"type": "integer"},
												"amount_minor":          map[string]interface{}{"type": "integer"},
												"currency":              map[string]interface{}{"type": "string"},
												"recurrence_cadence":    map[string]interface{}{"type": "string", "enum": []string{"ONE_OFF", "WEEKLY", "MONTHLY", "ANNUAL", "CUSTOM_DAYS"}},
												"non_spending_strategy": map[string]interface{}{"type": "string", "enum": []string{"ROLL_OVER", "RETURN_TO_POOL", "ARREARS_CUMULATIVE", "CAP_AND_HOLD", "PRORATE_FUTURE"}},
											},
											"required": []string{"name", "target_type", "priority", "amount_minor", "currency", "recurrence_cadence", "non_spending_strategy"},
										},
									},
								},
								"required": []string{"name", "targets"},
							},
						},
					},
					"required": []string{"group_name", "buckets"},
				},
			},
		},
		"required": []string{"groups"},
	}

	bodyData := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": systemInstruction},
			},
		},
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": fmt.Sprintf("Default Currency: %s\nUser budget description: %s", curr, req.Prompt)},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"responseMimeType": "application/json",
			"responseSchema":   responseSchema,
			"temperature":      0.2,
		},
	}

	rawJSON, err := json.Marshal(bodyData)
	if err != nil {
		return nil, fmt.Errorf("failed marshaling Gemini request: %w", err)
	}

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
	var out BudgetStructure
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("failed parsing Gemini JSON output: %w", err)
	}

	return &out, nil
}
