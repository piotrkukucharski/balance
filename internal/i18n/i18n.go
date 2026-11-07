package i18n

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
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
	FirstDayOfWeek  string
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

	// 1. Strict validation of catalog keys, terms, attributes, and variables
	if err := validateCatalogKeysInternal(); err != nil {
		return fmt.Errorf("i18n catalog validation failed: %w", err)
	}

	entries, err := fs.ReadDir(LocalesFS, "locales")
	if err != nil {
		return fmt.Errorf("reading locales directory: %w", err)
	}

	bundles := make(map[string]*fluent.Bundle)
	defaults := make(map[string]LocaleDefaults)
	var tags []string

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

		// Extract default formatting metadata and dynamic display name from .ftl
		numGrouping, _, _ := bundle.FormatMessage("locale-number-grouping")
		numDecimal, _, _ := bundle.FormatMessage("locale-number-decimal")
		dateFormat, _, _ := bundle.FormatMessage("locale-date-format")
		timeFormat, _, _ := bundle.FormatMessage("locale-time-format")
		currencyPattern, _, _ := bundle.FormatMessage("locale-currency-pattern")
		displayName, _, _ := bundle.FormatMessage("locale-name")
		firstDayOfWeek, _, _ := bundle.FormatMessage("locale-first-day-of-week")

		if displayName == "" {
			displayName = localeCode
		}
		if numDecimal == "" {
			numDecimal = "."
		}
		if dateFormat == "" {
			dateFormat = "YYYY-MM-DD"
		}
		if timeFormat == "" {
			timeFormat = "24h"
		}
		firstDayOfWeek = strings.ToUpper(strings.TrimSpace(firstDayOfWeek))
		if firstDayOfWeek == "" {
			firstDayOfWeek = "MONDAY"
		}

		defaults[localeCode] = LocaleDefaults{
			Tag:             localeCode,
			DisplayName:     displayName,
			NumberGrouping:  numGrouping,
			NumberDecimal:   numDecimal,
			DateFormat:      dateFormat,
			TimeFormat:      timeFormat,
			CurrencyPattern: currencyPattern,
			FirstDayOfWeek:  firstDayOfWeek,
		}
	}

	sort.Strings(tags)
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

// AllLocaleDefaults returns default formatting rules for all supported locales.
func AllLocaleDefaults() map[string]LocaleDefaults {
	registryMu.RLock()
	defer registryMu.RUnlock()
	res := make(map[string]LocaleDefaults, len(localeDefaults))
	for k, v := range localeDefaults {
		res[k] = v
	}
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

	registryMu.RLock()
	tags := supportedTags
	registryMu.RUnlock()

	// Direct match
	for _, tag := range tags {
		if strings.EqualFold(tag, input) {
			return tag
		}
	}

	// Language prefix match (e.g., "pl" -> "pl-PL", "en" -> "en-US")
	prefix := strings.ToLower(strings.Split(input, "-")[0])
	for _, tag := range tags {
		if strings.ToLower(strings.Split(tag, "-")[0]) == prefix {
			return tag
		}
	}

	return DefaultLocale
}

// DetectLocaleFromHeader parses an HTTP Accept-Language header and returns best matching supported locale.
func DetectLocaleFromHeader(acceptLangHeader string) string {
	acceptLangHeader = strings.TrimSpace(acceptLangHeader)
	if acceptLangHeader == "" {
		return DefaultLocale
	}

	tags, _, err := language.ParseAcceptLanguage(acceptLangHeader)
	if err != nil || len(tags) == 0 {
		return DefaultLocale
	}

	registryMu.RLock()
	supported := supportedTags
	registryMu.RUnlock()

	var serverTags []language.Tag
	for _, s := range supported {
		if t, err := language.Parse(s); err == nil {
			serverTags = append(serverTags, t)
		}
	}

	matcher := language.NewMatcher(serverTags)
	matchedTag, _, _ := matcher.Match(tags...)
	for _, s := range supported {
		if strings.EqualFold(s, matchedTag.String()) {
			return s
		}
		if strings.HasPrefix(strings.ToLower(s), strings.ToLower(matchedTag.String())) {
			return s
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

// FormatNumber formats a raw integer or float string into custom grouping and decimal notation.
func FormatNumber(majorPart string, minorPart string, isNegative bool, grouping string, decimal string) string {
	if grouping != "" {
		var parts []string
		for len(majorPart) > 3 {
			parts = append([]string{majorPart[len(majorPart)-3:]}, parts...)
			majorPart = majorPart[:len(majorPart)-3]
		}
		if len(majorPart) > 0 {
			parts = append([]string{majorPart}, parts...)
		}
		majorPart = strings.Join(parts, grouping)
	}

	formattedNum := majorPart
	if minorPart != "" {
		formattedNum += decimal + minorPart
	}
	if isNegative {
		formattedNum = "-" + formattedNum
	}
	return formattedNum
}

// FormatMoneyMinor formats minor currency amount (e.g. 100099 -> "1 000,99 PLN" or "1,000.99 USD")
// honoring both custom number formats and locale currency pattern from .ftl files.
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

	formattedNum := FormatNumber(strconv.FormatInt(major, 10), fmt.Sprintf("%02d", minor), isNegative, grouping, decimal)

	if currency == "" {
		return formattedNum
	}

	// Use locale-currency-pattern if defined, e.g. "{ $amount } { $currency }"
	pattern := defaults.CurrencyPattern
	if pattern != "" && strings.Contains(pattern, "amount") && strings.Contains(pattern, "currency") {
		res := strings.ReplaceAll(pattern, "{ $amount }", formattedNum)
		res = strings.ReplaceAll(res, "{$amount}", formattedNum)
		res = strings.ReplaceAll(res, "{ $currency }", currency)
		res = strings.ReplaceAll(res, "{$currency}", currency)
		return strings.TrimSpace(res)
	}

	return fmt.Sprintf("%s %s", formattedNum, currency)
}

// MessageSignature captures message identity and required variables.
type MessageSignature struct {
	ID        string
	Variables []string
}

// ValidateCatalogKeys ensures every registered locale has the exact same message keys,
// attributes, and variables as en-US. Any missing or extra key returns a descriptive error.
func ValidateCatalogKeys() error {
	return validateCatalogKeysInternal()
}

func validateCatalogKeysInternal() error {
	baseData, err := LocalesFS.ReadFile(path.Join("locales", DefaultLocale+".ftl"))
	if err != nil {
		return fmt.Errorf("reading default locale %s: %w", DefaultLocale, err)
	}

	baseSigs, err := extractSignaturesFromFTL(string(baseData))
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

		locSigs, err := extractSignaturesFromFTL(string(data))
		if err != nil {
			return fmt.Errorf("extracting keys from %s: %w", entry.Name(), err)
		}

		// Check for missing keys
		for k, baseSig := range baseSigs {
			locSig, exists := locSigs[k]
			if !exists {
				return fmt.Errorf("validation error in locale %q: missing required translation key %q (present in %s)", locName, k, DefaultLocale)
			}
			// Verify variables match
			for _, v := range baseSig.Variables {
				if !containsString(locSig.Variables, v) {
					return fmt.Errorf("validation error in locale %q (key %q): missing variable {$%s} (required by %s)", locName, k, v, DefaultLocale)
				}
			}
		}

		// Check for extra keys
		for k := range locSigs {
			if _, exists := baseSigs[k]; !exists {
				return fmt.Errorf("validation error in locale %q: unexpected additional translation key %q (not found in %s)", locName, k, DefaultLocale)
			}
		}
	}

	return nil
}

func containsString(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func extractSignaturesFromFTL(source string) (map[string]MessageSignature, error) {
	p := parser.New(source)
	res, pErrs := p.Parse()
	if len(pErrs) > 0 {
		return nil, fmt.Errorf("parse error: %v", pErrs[0])
	}

	sigs := make(map[string]MessageSignature)
	for _, entry := range res.Body {
		if msg, ok := entry.(*ast.Message); ok && msg.ID != nil {
			var vars []string
			if msg.Value != nil {
				for _, elem := range msg.Value.Elements {
					vars = append(vars, extractVariables(elem)...)
				}
			}
			sigs[msg.ID.Name] = MessageSignature{
				ID:        msg.ID.Name,
				Variables: deduplicateStrings(vars),
			}
			for _, attr := range msg.Attributes {
				var aVars []string
				if attr.Value != nil {
					for _, elem := range attr.Value.Elements {
						aVars = append(aVars, extractVariables(elem)...)
					}
				}
				attrKey := msg.ID.Name + "." + attr.ID.Name
				sigs[attrKey] = MessageSignature{
					ID:        attrKey,
					Variables: deduplicateStrings(aVars),
				}
			}
		}
		if term, ok := entry.(*ast.Term); ok && term.ID != nil {
			sigs["-"+term.ID.Name] = MessageSignature{
				ID: "-"+term.ID.Name,
			}
		}
	}
	return sigs, nil
}

func extractVariables(node ast.Node) []string {
	var vars []string
	if node == nil {
		return vars
	}
	switch n := node.(type) {
	case *ast.Placeable:
		vars = append(vars, extractVariables(n.Expression)...)
	case *ast.VariableReference:
		if n.ID != nil {
			vars = append(vars, n.ID.Name)
		}
	case *ast.SelectExpression:
		vars = append(vars, extractVariables(n.Selector)...)
		for _, v := range n.Variants {
			if v.Value != nil {
				for _, elem := range v.Value.Elements {
					vars = append(vars, extractVariables(elem)...)
				}
			}
		}
	}
	return vars
}

func deduplicateStrings(slice []string) []string {
	seen := make(map[string]bool)
	var res []string
	for _, s := range slice {
		if !seen[s] {
			seen[s] = true
			res = append(res, s)
		}
	}
	return res
}
