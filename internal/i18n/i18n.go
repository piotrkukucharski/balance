package i18n

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/lus/fluent.go/fluent"
	"github.com/lus/fluent.go/fluent/parser"
	"github.com/lus/fluent.go/fluent/parser/ast"
	"golang.org/x/text/language"
)

//go:embed locales/*.ftl
var LocalesFS embed.FS

const (
	DefaultLocale = "en-US"
)

// LocaleDefaults holds parsed locale configuration from .ftl files
type LocaleDefaults struct {
	Tag             string
	DisplayName     string
	NumberGrouping  string
	NumberDecimal   string
	DateFormat      string
	TimeFormat      string
	CurrencyPattern string
}

var (
	registryMu       sync.RWMutex
	availableLocales = make(map[string]*fluent.Bundle)
	localeDefaults   = make(map[string]LocaleDefaults)
	supportedTags    = []string{}
)

func init() {
	if err := LoadLocales(); err != nil {
		panic(fmt.Sprintf("failed to load i18n locales: %v", err))
	}
}

// LoadLocales parses all .ftl files embedded in LocalesFS into fluent Bundles and validates keys.
func LoadLocales() error {
	registryMu.Lock()
	defer registryMu.Unlock()

	entries, err := fs.ReadDir(LocalesFS, "locales")
	if err != nil {
		return fmt.Errorf("reading locales directory: %w", err)
	}

	bundles := make(map[string]*fluent.Bundle)
	defaults := make(map[string]LocaleDefaults)
	tags := []string{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ftl") {
			continue
		}

		localeCode := strings.TrimSuffix(entry.Name(), ".ftl")
		data, err := LocalesFS.ReadFile(path.Join("locales", entry.Name()))
		if err != nil {
			return fmt.Errorf("reading ftl file %s: %w", entry.Name(), err)
		}

		res, pErrs := fluent.NewResource(string(data))
		if len(pErrs) > 0 {
			return fmt.Errorf("syntax error in %s: %v", entry.Name(), pErrs[0])
		}

		langTag, err := language.Parse(localeCode)
		if err != nil {
			langTag = language.English
		}

		bundle := fluent.NewBundle(langTag)
		if errs := bundle.AddResource(res); len(errs) > 0 {
			return fmt.Errorf("failed adding resource for %s: %v", localeCode, errs[0])
		}

		bundles[localeCode] = bundle
		tags = append(tags, localeCode)

		// Extract default formatting metadata
		numGrouping, _, _ := bundle.FormatMessage("locale-number-grouping")
		numDecimal, _, _ := bundle.FormatMessage("locale-number-decimal")
		dateFormat, _, _ := bundle.FormatMessage("locale-date-format")
		timeFormat, _, _ := bundle.FormatMessage("locale-time-format")
		currencyPattern, _, _ := bundle.FormatMessage("locale-currency-pattern")

		if numDecimal == "" {
			numDecimal = "."
		}
		if dateFormat == "" {
			dateFormat = "YYYY-MM-DD"
		}
		if timeFormat == "" {
			timeFormat = "24h"
		}

		displayName := localeCode
		switch localeCode {
		case "en-US":
			displayName = "English (US)"
		case "pl-PL":
			displayName = "Polski (PL)"
		}

		defaults[localeCode] = LocaleDefaults{
			Tag:             localeCode,
			DisplayName:     displayName,
			NumberGrouping:  numGrouping,
			NumberDecimal:   numDecimal,
			DateFormat:      dateFormat,
			TimeFormat:      timeFormat,
			CurrencyPattern: currencyPattern,
		}
	}

	availableLocales = bundles
	localeDefaults = defaults
	supportedTags = tags

	return nil
}

// SupportedLocales returns the list of available locale identifiers (e.g. ["en-US", "pl-PL"]).
func SupportedLocales() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	res := make([]string, len(supportedTags))
	copy(res, supportedTags)
	return res
}

// GetLocaleDefaults returns default formatting rules for a given locale.
func GetLocaleDefaults(locale string) LocaleDefaults {
	registryMu.RLock()
	defer registryMu.RUnlock()

	normalized := NormalizeLocale(locale)
	if d, ok := localeDefaults[normalized]; ok {
		return d
	}
	return localeDefaults[DefaultLocale]
}

// NormalizeLocale matches input string against supported locales or falls back to DefaultLocale.
func NormalizeLocale(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return DefaultLocale
	}

	// Direct match
	for _, tag := range supportedTags {
		if strings.EqualFold(tag, input) {
			return tag
		}
	}

	// Language prefix match (e.g., "pl" -> "pl-PL", "en" -> "en-US")
	prefix := strings.ToLower(strings.Split(input, "-")[0])
	for _, tag := range supportedTags {
		if strings.ToLower(strings.Split(tag, "-")[0]) == prefix {
			return tag
		}
	}

	return DefaultLocale
}

type contextKey string

const (
	LocaleContextKey contextKey = "i18n_locale"
)

// WithLocale returns a context enriched with active locale.
func WithLocale(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, LocaleContextKey, NormalizeLocale(locale))
}

// LocaleFromContext extracts active locale from context or returns DefaultLocale.
func LocaleFromContext(ctx context.Context) string {
	if ctx == nil {
		return DefaultLocale
	}
	if loc, ok := ctx.Value(LocaleContextKey).(string); ok && loc != "" {
		return loc
	}
	return DefaultLocale
}

// T translates a message key for the given locale with optional variables.
func T(locale string, key string, args ...any) string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	norm := NormalizeLocale(locale)
	bundle, ok := availableLocales[norm]
	if !ok {
		bundle = availableLocales[DefaultLocale]
	}

	var contexts []*fluent.FormatContext
	if len(args) > 0 {
		vars := make(map[string]any)
		if m, ok := args[0].(map[string]any); ok {
			vars = m
		} else {
			for i := 0; i < len(args)-1; i += 2 {
				if k, ok := args[i].(string); ok {
					vars[k] = args[i+1]
				}
			}
		}
		if len(vars) > 0 {
			contexts = append(contexts, fluent.WithVariables(vars))
		}
	}

	msg, _, err := bundle.FormatMessage(key, contexts...)
	if err != nil || msg == "" {
		// Fallback to default locale
		if bundle != availableLocales[DefaultLocale] && availableLocales[DefaultLocale] != nil {
			fallbackMsg, _, fbErr := availableLocales[DefaultLocale].FormatMessage(key, contexts...)
			if fbErr == nil && fallbackMsg != "" {
				return fallbackMsg
			}
		}
		return key
	}
	return msg
}

// TC translates a message key using the locale found in context.
func TC(ctx context.Context, key string, args ...any) string {
	return T(LocaleFromContext(ctx), key, args...)
}

// FormatMoneyMinor formats minor currency amount (e.g. 100099 -> "1 000,99 PLN" or "1,000.99 USD")
func FormatMoneyMinor(amountMinor int64, currency string, locale string, customNumberFormat string) string {
	defaults := GetLocaleDefaults(locale)
	grouping := defaults.NumberGrouping
	decimal := defaults.NumberDecimal

	if customNumberFormat != "" && customNumberFormat != "auto" {
		switch customNumberFormat {
		case "1,000.99":
			grouping = ","
			decimal = "."
		case "1.000,99":
			grouping = "."
			decimal = ","
		case "1 000,99":
			grouping = " "
			decimal = ","
		case "1 000.99":
			grouping = " "
			decimal = "."
		case "1_000.99":
			grouping = "_"
			decimal = "."
		case "1000.99":
			grouping = ""
			decimal = "."
		}
	}

	isNegative := amountMinor < 0
	absVal := amountMinor
	if isNegative {
		absVal = -amountMinor
	}

	major := absVal / 100
	minor := absVal % 100

	majorStr := strconv.FormatInt(major, 10)
	if grouping != "" {
		var parts []string
		for len(majorStr) > 3 {
			parts = append([]string{majorStr[len(majorStr)-3:]}, parts...)
			majorStr = majorStr[:len(majorStr)-3]
		}
		if len(majorStr) > 0 {
			parts = append([]string{majorStr}, parts...)
		}
		majorStr = strings.Join(parts, grouping)
	}

	formattedNum := fmt.Sprintf("%s%s%02d", majorStr, decimal, minor)
	if isNegative {
		formattedNum = "-" + formattedNum
	}

	if currency == "" {
		return formattedNum
	}

	return fmt.Sprintf("%s %s", formattedNum, currency)
}

// ValidateCatalogKeys ensures every registered locale has the exact same message keys as en-US.
// Any missing or extra key returns a descriptive error with the key and locale.
func ValidateCatalogKeys() error {
	baseData, err := LocalesFS.ReadFile(path.Join("locales", DefaultLocale+".ftl"))
	if err != nil {
		return fmt.Errorf("reading default locale %s: %w", DefaultLocale, err)
	}

	baseKeys, err := extractKeysFromFTL(string(baseData))
	if err != nil {
		return fmt.Errorf("extracting keys from %s: %w", DefaultLocale, err)
	}

	entries, err := fs.ReadDir(LocalesFS, "locales")
	if err != nil {
		return fmt.Errorf("reading locales directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ftl") {
			continue
		}
		locName := strings.TrimSuffix(entry.Name(), ".ftl")
		if locName == DefaultLocale {
			continue
		}

		data, err := LocalesFS.ReadFile(path.Join("locales", entry.Name()))
		if err != nil {
			return fmt.Errorf("reading locale %s: %w", entry.Name(), err)
		}

		locKeys, err := extractKeysFromFTL(string(data))
		if err != nil {
			return fmt.Errorf("extracting keys from %s: %w", entry.Name(), err)
		}

		// Check for missing keys
		for k := range baseKeys {
			if !locKeys[k] {
				return fmt.Errorf("validation error in locale %q: missing required translation key %q (present in %s)", locName, k, DefaultLocale)
			}
		}

		// Check for extra keys
		for k := range locKeys {
			if !baseKeys[k] {
				return fmt.Errorf("validation error in locale %q: unexpected additional translation key %q (not found in %s)", locName, k, DefaultLocale)
			}
		}
	}

	return nil
}

func extractKeysFromFTL(source string) (map[string]bool, error) {
	p := parser.New(source)
	res, pErrs := p.Parse()
	if len(pErrs) > 0 {
		return nil, fmt.Errorf("parse error: %v", pErrs[0])
	}

	keys := make(map[string]bool)
	for _, entry := range res.Body {
		if msg, ok := entry.(*ast.Message); ok && msg.ID != nil {
			keys[msg.ID.Name] = true
		}
		if term, ok := entry.(*ast.Term); ok && term.ID != nil {
			keys["-"+term.ID.Name] = true
		}
	}
	return keys, nil
}
