package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/balance/balance/internal/logger"
)

// SmartRuleCandidate represents a suggested rule by the AI.
type SmartRuleCandidate struct {
	Name            string  `json:"name"`
	MatchField      string  `json:"match_field"`
	MatchOperator   string  `json:"match_operator"`
	MatchPattern    string  `json:"match_pattern"`
	TargetPartyName string  `json:"target_party_name"`
	SuggestedBucket *string `json:"suggested_bucket,omitempty"`
	Confidence      float64 `json:"confidence"`
	Reasoning       string  `json:"reasoning"`
}

// UnassignedTxInfo holds raw transaction info given to the AI.
type UnassignedTxInfo struct {
	ID        string `json:"id"`
	RawPayee  string `json:"raw_payee"`
	IBAN      string `json:"iban,omitempty"`
	Reference string `json:"reference,omitempty"`
	Currency  string `json:"currency"`
	Amount    string `json:"amount"`
}

// SmartRulePromptRequest passes unassigned transactions and available buckets/parties to the AI.
type SmartRulePromptRequest struct {
	Transactions     []UnassignedTxInfo `json:"transactions"`
	AvailableBuckets []string           `json:"available_buckets"`
	ExistingParties  []string           `json:"existing_parties"`
}

// SmartRuleAIProvider generates auto-matching rules from unassigned transactions.
type SmartRuleAIProvider interface {
	SuggestRules(ctx context.Context, req SmartRulePromptRequest) ([]SmartRuleCandidate, error)
}

// NewSmartRuleAIProvider creates either Gemini or Mock provider.
func NewSmartRuleAIProvider() SmartRuleAIProvider {
	apiKey := os.Getenv("GEMINI_API_KEY")
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = os.Getenv("AI_GEMINI_MODEL")
	}
	if os.Getenv("AI_PROVIDER") == "mock" || apiKey == "" {
		return NewMockSmartRuleAIProvider()
	}
	return NewGeminiSmartRuleProvider(apiKey, model, ResolveGeminiTimeout())
}

// MockSmartRuleAIProvider proposes deterministic rules based on payee patterns.
type MockSmartRuleAIProvider struct{}

func NewMockSmartRuleAIProvider() *MockSmartRuleAIProvider {
	return &MockSmartRuleAIProvider{}
}

func (m *MockSmartRuleAIProvider) SuggestRules(ctx context.Context, req SmartRulePromptRequest) ([]SmartRuleCandidate, error) {
	var candidates []SmartRuleCandidate
	seen := make(map[string]bool)

	for _, tx := range req.Transactions {
		payee := strings.TrimSpace(tx.RawPayee)
		if payee == "" {
			continue
		}
		upper := strings.ToUpper(payee)

		var cand *SmartRuleCandidate
		if strings.Contains(upper, "BIEDRONKA") && !seen["BIEDRONKA"] {
			seen["BIEDRONKA"] = true
			b := "Groceries"
			cand = &SmartRuleCandidate{
				Name:            "Biedronka Groceries",
				MatchField:      "RAW_PAYEE",
				MatchOperator:   "CONTAINS",
				MatchPattern:    "BIEDRONKA",
				TargetPartyName: "Biedronka",
				SuggestedBucket: &b,
				Confidence:      0.95,
				Reasoning:       "Matches popular grocery store chain",
			}
		} else if strings.Contains(upper, "UBER") && !seen["UBER"] {
			seen["UBER"] = true
			b := "Transport"
			cand = &SmartRuleCandidate{
				Name:            "Uber Rides",
				MatchField:      "RAW_PAYEE",
				MatchOperator:   "CONTAINS",
				MatchPattern:    "UBER",
				TargetPartyName: "Uber",
				SuggestedBucket: &b,
				Confidence:      0.90,
				Reasoning:       "Matches transport rides",
			}
		} else if strings.Contains(upper, "NETFLIX") && !seen["NETFLIX"] {
			seen["NETFLIX"] = true
			b := "Subscriptions"
			cand = &SmartRuleCandidate{
				Name:            "Netflix Subscription",
				MatchField:      "RAW_PAYEE",
				MatchOperator:   "CONTAINS",
				MatchPattern:    "NETFLIX",
				TargetPartyName: "Netflix",
				SuggestedBucket: &b,
				Confidence:      0.98,
				Reasoning:       "Streaming subscription service",
			}
		} else if strings.Contains(upper, "ORLEN") && !seen["ORLEN"] {
			seen["ORLEN"] = true
			b := "Fuel"
			cand = &SmartRuleCandidate{
				Name:            "Orlen Gas Station",
				MatchField:      "RAW_PAYEE",
				MatchOperator:   "CONTAINS",
				MatchPattern:    "ORLEN",
				TargetPartyName: "PKN Orlen",
				SuggestedBucket: &b,
				Confidence:      0.92,
				Reasoning:       "Fuel and gas station",
			}
		} else if !seen[payee] && len(candidates) < 5 {
			seen[payee] = true
			parts := strings.Fields(payee)
			keyword := payee
			if len(parts) > 0 {
				keyword = parts[0]
			}
			cand = &SmartRuleCandidate{
				Name:            fmt.Sprintf("Rule for %s", keyword),
				MatchField:      "RAW_PAYEE",
				MatchOperator:   "CONTAINS",
				MatchPattern:    keyword,
				TargetPartyName: keyword,
				Confidence:      0.80,
				Reasoning:       fmt.Sprintf("Auto-extracted keyword %s from payee %s", keyword, payee),
			}
		}

		if cand != nil {
			// Check if suggested bucket matches available buckets
			if cand.SuggestedBucket != nil && len(req.AvailableBuckets) > 0 {
				found := false
				for _, b := range req.AvailableBuckets {
					if strings.EqualFold(b, *cand.SuggestedBucket) {
						cand.SuggestedBucket = &b
						found = true
						break
					}
				}
				if !found && len(req.AvailableBuckets) > 0 {
					// Use first available bucket or leave nil
					cand.SuggestedBucket = &req.AvailableBuckets[0]
				}
			}
			candidates = append(candidates, *cand)
		}
	}

	return candidates, nil
}

// GeminiSmartRuleProvider calls Gemini REST API with structured response schema.
type GeminiSmartRuleProvider struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewGeminiSmartRuleProvider(apiKey, model string, timeout time.Duration) *GeminiSmartRuleProvider {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	return &GeminiSmartRuleProvider{
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (g *GeminiSmartRuleProvider) SuggestRules(ctx context.Context, req SmartRulePromptRequest) ([]SmartRuleCandidate, error) {
	ctx = logger.WithComponent(ctx, "ai:smart_rules")
	if len(req.Transactions) == 0 {
		return []SmartRuleCandidate{}, nil
	}

	txData, err := json.Marshal(req.Transactions)
	if err != nil {
		return nil, fmt.Errorf("failed marshaling txs: %w", err)
	}

	systemInstruction := `You are an expert personal finance classification engine.
Given a list of raw bank transactions (raw_payee, iban, reference) that currently have no classification rule, suggest sensible auto-matching rules.
Each rule must:
- Extract a clean merchant / party name (target_party_name).
- Pick a descriptive rule name (e.g. "Biedronka Groceries", "Uber Rides").
- Specify match_field: "RAW_PAYEE", "COUNTERPARTY_IBAN", "REFERENCE", or "ANY".
- Specify match_operator: "CONTAINS", "PREFIX", "EXACT", or "REGEX".
- Specify match_pattern: a substring or safe regex (max 256 chars) that cleanly matches that merchant.
- If one of the available_buckets is a great match, set suggested_bucket.
- Provide a confidence between 0.0 and 1.0.
Output JSON only adhering strictly to the schema.`

	userPrompt := fmt.Sprintf(`Available buckets: %s
Existing parties: %s
Unclassified transactions:
%s`, strings.Join(req.AvailableBuckets, ", "), strings.Join(req.ExistingParties, ", "), string(txData))

	type geminiResponse struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	requestPayload := map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": systemInstruction}},
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
			"temperature":      0.1,
			"responseMimeType": "application/json",
		},
	}

	bodyBytes, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", g.model, g.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini api returned status %d", resp.StatusCode)
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return []SmartRuleCandidate{}, nil
	}

	var rules []SmartRuleCandidate
	text := geminiResp.Candidates[0].Content.Parts[0].Text
	if err := json.Unmarshal([]byte(text), &rules); err != nil {
		// Try wrapping if object returned
		var wrapped struct {
			Rules []SmartRuleCandidate `json:"rules"`
		}
		if err2 := json.Unmarshal([]byte(text), &wrapped); err2 == nil {
			return wrapped.Rules, nil
		}
		return nil, fmt.Errorf("failed parsing Gemini response into SmartRuleCandidate: %w", err)
	}

	return rules, nil
}
