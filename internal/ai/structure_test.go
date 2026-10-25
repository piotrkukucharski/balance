package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMockBudgetStructureAIProvider_Presets(t *testing.T) {
	provider := NewMockBudgetStructureAIProvider()

	t.Run("Polish family preset", func(t *testing.T) {
		res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "Rodzina 2+1, mieszkanie i kredyt hipoteczny, rachunki, zakupy i wakacje",
			DefaultCurrency: "PLN",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Groups) == 0 {
			t.Fatalf("expected groups, got 0")
		}
		foundBucket := false
		for _, g := range res.Groups {
			for _, b := range g.Buckets {
				if b.Name != "" {
					foundBucket = true
				}
				for _, tgt := range b.Targets {
					if tgt.Currency != "PLN" {
						t.Errorf("expected PLN, got %s", tgt.Currency)
					}
					if tgt.AmountMinor <= 0 {
						t.Errorf("expected positive amount, got %d", tgt.AmountMinor)
					}
				}
			}
		}
		if !foundBucket {
			t.Errorf("expected at least one bucket")
		}
	})

	t.Run("Custom line items", func(t *testing.T) {
		prompt := "Czynsz: 2500 PLN\nJedzenie: 1200 PLN\nOszczędności: 500 PLN"
		res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          prompt,
			DefaultCurrency: "PLN",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Groups) != 1 || len(res.Groups[0].Buckets) != 3 {
			t.Fatalf("expected 1 group and 3 buckets, got %d groups and %d buckets", len(res.Groups), len(res.Groups[0].Buckets))
		}
		b0 := res.Groups[0].Buckets[0]
		if b0.Name != "Czynsz" || len(b0.Targets) != 1 || b0.Targets[0].AmountMinor != 250000 {
			t.Errorf("unexpected bucket 0: %+v", b0)
		}
	})

	t.Run("Student preset with EUR", func(t *testing.T) {
		res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "I am a student living in dormitory",
			DefaultCurrency: "EUR",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Groups) == 0 {
			t.Fatalf("expected groups, got 0")
		}
		for _, g := range res.Groups {
			for _, b := range g.Buckets {
				for _, tgt := range b.Targets {
					if tgt.Currency != "EUR" {
						t.Errorf("expected EUR, got %s", tgt.Currency)
					}
				}
			}
		}
	})

	t.Run("Empty prompt error", func(t *testing.T) {
		_, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "   ",
			DefaultCurrency: "PLN",
		})
		if err == nil {
			t.Fatalf("expected error for empty prompt")
		}
	})
}

func TestResolveGeminiTimeout(t *testing.T) {
	// Default
	t.Setenv("GEMINI_TIMEOUT_SECONDS", "")
	t.Setenv("AI_TIMEOUT_SECONDS", "")
	t.Setenv("AI_GEMINI_TIMEOUT", "")
	if got := ResolveGeminiTimeout(); got != DefaultGeminiTimeout {
		t.Errorf("expected default timeout %v, got %v", DefaultGeminiTimeout, got)
	}

	// GEMINI_TIMEOUT_SECONDS
	t.Setenv("GEMINI_TIMEOUT_SECONDS", "120")
	if got := ResolveGeminiTimeout(); got != 120*time.Second {
		t.Errorf("expected 120s, got %v", got)
	}

	// AI_TIMEOUT_SECONDS fallback
	t.Setenv("GEMINI_TIMEOUT_SECONDS", "")
	t.Setenv("AI_TIMEOUT_SECONDS", "45")
	if got := ResolveGeminiTimeout(); got != 45*time.Second {
		t.Errorf("expected 45s, got %v", got)
	}

	// Invalid value falls back
	t.Setenv("AI_TIMEOUT_SECONDS", "invalid")
	if got := ResolveGeminiTimeout(); got != DefaultGeminiTimeout {
		t.Errorf("expected fallback to default timeout, got %v", got)
	}
}

func TestGeminiBudgetStructureProvider_Timeout(t *testing.T) {
	p := NewGeminiBudgetStructureProvider("fake-key", "gemini-3.8-flash", 75*time.Second)
	if p.httpClient.Timeout != 75*time.Second {
		t.Errorf("expected client timeout 75s, got %v", p.httpClient.Timeout)
	}
}

func TestGeminiBudgetStructureProvider_HTTPErrorsAndTimeout(t *testing.T) {
	t.Run("API returns 429 rate limit error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted"}}`))
		}))
		defer ts.Close()

		t.Setenv("GEMINI_BASE_URL", ts.URL)
		provider := NewGeminiBudgetStructureProvider("test-key", "gemini-3.8-flash", 5*time.Second)

		_, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "Rent 2000 PLN",
			DefaultCurrency: "PLN",
		})
		if err == nil {
			t.Fatal("expected error on 429 response, got nil")
		}
		if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "Resource has been exhausted") {
			t.Errorf("expected error to contain 429 and response body, got: %v", err)
		}
	})

	t.Run("API times out exceeding client timeout", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}))
		defer ts.Close()

		t.Setenv("GEMINI_BASE_URL", ts.URL)
		// Set very short timeout
		provider := NewGeminiBudgetStructureProvider("test-key", "gemini-3.8-flash", 20*time.Millisecond)

		_, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "Rent 2000 PLN",
			DefaultCurrency: "PLN",
		})
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
		if !strings.Contains(err.Error(), "context deadline exceeded") && !strings.Contains(err.Error(), "Client.Timeout") {
			t.Errorf("expected timeout/deadline exceeded error, got: %v", err)
		}
	})

	t.Run("API returns successful structured JSON", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-goog-api-key") != "test-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{
				"candidates": [{
					"content": {
						"parts": [{
							"text": "{\"groups\":[{\"group_name\":\"Housing\",\"buckets\":[{\"name\":\"Rent\",\"targets\":[{\"name\":\"Rent\",\"target_type\":\"EXPENSE\",\"priority\":3,\"amount_minor\":250000,\"currency\":\"PLN\",\"recurrence_cadence\":\"MONTHLY\",\"non_spending_strategy\":\"RETURN_TO_POOL\"}]}]}]}"
						}]
					}
				}]
			}`)
		}))
		defer ts.Close()

		t.Setenv("GEMINI_BASE_URL", ts.URL)
		provider := NewGeminiBudgetStructureProvider("test-key", "gemini-3.8-flash", 5*time.Second)

		res, err := provider.GenerateBudgetStructure(context.Background(), BudgetStructurePromptRequest{
			Prompt:          "Rent 2500 PLN",
			DefaultCurrency: "PLN",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Groups) != 1 || res.Groups[0].GroupName != "Housing" {
			t.Fatalf("unexpected result: %+v", res)
		}
		if len(res.Groups[0].Buckets) != 1 || res.Groups[0].Buckets[0].Targets[0].AmountMinor != 250000 {
			t.Fatalf("unexpected bucket/target: %+v", res.Groups[0].Buckets[0])
		}
	})
}
