# Internationalization & Localization (`internal/i18n`)

## Overview
Internationalization and localization manager handling embedded translation dictionaries, number formatting, and locale-specific currency defaults.

## Interface
- `var LocalesFS embed.FS` [i18n.go:21]
- `type LocaleDefaults struct` [i18n.go:28]
- `func LoadLocales() error` [i18n.go:53]
- `func SupportedLocales() []string` [i18n.go:147]
- `func AllLocaleDefaults() map[string]LocaleDefaults` [i18n.go:156]
- `func GetLocaleDefaults(locale string) LocaleDefaults` [i18n.go:167]
- `func T(locale, key string, args ...any) string` [i18n.go:180]

## Invariants
- Fallback locale is 'en' if requested translation key or locale is missing [i18n.go:60-75]
- Locales are embedded in binary via go:embed from internal/i18n/locales [i18n.go:20-25]

## Side Effects
- Reads embedded filesystem resources during initialization [i18n.go:53-70]

## Verification
- Run: `go test ./...`
