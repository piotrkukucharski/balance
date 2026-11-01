package i18n_test

import (
	"context"
	"testing"

	"github.com/balance/balance/internal/i18n"
)

func TestValidateCatalogKeys(t *testing.T) {
	err := i18n.ValidateCatalogKeys()
	if err != nil {
		t.Fatalf("Translation catalog validation failed: %v", err)
	}
}

func TestTranslationsAndPlurals(t *testing.T) {
	// English lookup
	enAppName := i18n.T("en-US", "app-name")
	if enAppName != "Balance" {
		t.Errorf("expected 'Balance', got %q", enAppName)
	}

	// Polish lookup
	plNavBudget := i18n.T("pl-PL", "nav-budget")
	if plNavBudget != "📂 1. Budżet (Koperty)" {
		t.Errorf("expected '📂 1. Budżet (Koperty)', got %q", plNavBudget)
	}

	// Polish plurals test
	plPluralOne := i18n.T("pl-PL", "test-items-count", "count", 1)
	if plPluralOne != "Masz 1 nieprzeczytaną wiadomość" {
		t.Errorf("expected 'Masz 1 nieprzeczytaną wiadomość', got %q", plPluralOne)
	}

	plPluralFew := i18n.T("pl-PL", "test-items-count", "count", 3)
	if plPluralFew != "Masz 3 nieprzeczytane wiadomości" {
		t.Errorf("expected 'Masz 3 nieprzeczytane wiadomości', got %q", plPluralFew)
	}

	plPluralMany := i18n.T("pl-PL", "test-items-count", "count", 5)
	if plPluralMany != "Masz 5 nieprzeczytanych wiadomości" {
		t.Errorf("expected 'Masz 5 nieprzeczytanych wiadomości', got %q", plPluralMany)
	}

	// Context lookup
	ctx := i18n.WithLocale(context.Background(), "pl-PL")
	if loc := i18n.LocaleFromContext(ctx); loc != "pl-PL" {
		t.Errorf("expected locale pl-PL from context, got %s", loc)
	}

	plLogout := i18n.TC(ctx, "nav-logout")
	if plLogout != "Wyloguj się" {
		t.Errorf("expected 'Wyloguj się', got %q", plLogout)
	}
}

func TestDetectLocaleFromHeader(t *testing.T) {
	tests := []struct {
		header   string
		expected string
	}{
		{"pl-PL,pl;q=0.9,en-US;q=0.8,en;q=0.7", "pl-PL"},
		{"en-US,en;q=0.9", "en-US"},
		{"pl", "pl-PL"},
		{"fr-FR,fr;q=0.9", "en-US"},
		{"", "en-US"},
	}

	for _, tc := range tests {
		got := i18n.DetectLocaleFromHeader(tc.header)
		if got != tc.expected {
			t.Errorf("DetectLocaleFromHeader(%q) = %q, expected %q", tc.header, got, tc.expected)
		}
	}
}

func TestFormatMoneyMinor(t *testing.T) {
	tests := []struct {
		amount   int64
		currency string
		locale   string
		custom   string
		expected string
	}{
		{100099, "USD", "en-US", "auto", "1,000.99 USD"},
		{100099, "PLN", "pl-PL", "auto", "1 000,99 PLN"},
		{100099, "EUR", "en-US", "1.000,99", "1.000,99 EUR"},
		{100099, "EUR", "en-US", "1_000.99", "1_000.99 EUR"},
		{100099, "EUR", "en-US", "1000.99", "1000.99 EUR"},
		{100099, "EUR", "en-US", "1 000.99", "1 000.99 EUR"},
		{-5025, "PLN", "pl-PL", "auto", "-50,25 PLN"},
	}

	for _, tc := range tests {
		got := i18n.FormatMoneyMinor(tc.amount, tc.currency, tc.locale, tc.custom)
		if got != tc.expected {
			t.Errorf("FormatMoneyMinor(%d, %q, %q, %q) = %q; expected %q", tc.amount, tc.currency, tc.locale, tc.custom, got, tc.expected)
		}
	}
}
