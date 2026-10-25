package fx

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
)

var (
	// ErrExchangeRateNotFound is returned when no exchange rate or interpolation path exists.
	ErrExchangeRateNotFound = errors.New("exchange rate not found for currency pair and date")
	// ErrInvalidCurrency is returned when currency codes are empty or invalid.
	ErrInvalidCurrency = errors.New("invalid currency code")
)

// RateQuote represents a normalized exchange rate quotation from a central bank feed.
type RateQuote struct {
	BaseCurrency   string // e.g. "EUR" or "USD"
	TargetCurrency string // e.g. "PLN"
	RateDate       int64  // UTC day boundary in microseconds
	RateMicros     int64  // Rate scaled by 1,000,000
	Source         string // "ECB", "NBP", "MANUAL", "MOCK"
}

// SyncOptions configures an exchange rate synchronization run.
type SyncOptions struct {
	Days   int    // Number of days of history to sync (<=0 means auto-detect: 90 if empty, 1 if routine)
	Source string // "ALL", "ECB", "NBP"
	Force  bool   // Force update even if rates exist
}

// DayBoundaryMicros returns the UTC midnight timestamp in microseconds for a given time.Time.
func DayBoundaryMicros(t time.Time) int64 {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).UnixMicro()
}

// ParseDateBoundaryMicros parses a date string "YYYY-MM-DD" into UTC midnight microseconds.
func ParseDateBoundaryMicros(dateStr string) (int64, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(dateStr))
	if err != nil {
		return 0, fmt.Errorf("invalid date format (expected YYYY-MM-DD): %w", err)
	}
	return parsed.UTC().UnixMicro(), nil
}

// ParseDecimalMicros safely parses a floating-point string representation into int64 micros (scaled by 10^6).
// Uses math/big.Rat with round-half-up to avoid IEEE-754 precision issues.
func ParseDecimalMicros(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty rate string")
	}
	rat := new(big.Rat)
	if _, ok := rat.SetString(s); !ok {
		return 0, fmt.Errorf("invalid rate decimal: %q", s)
	}

	mult := big.NewRat(domain.MicroUnitMultiplier, 1)
	prod := new(big.Rat).Mul(rat, mult)

	// Round half-up: (num + denom/2) / denom
	num := prod.Num()
	denom := prod.Denom()
	half := new(big.Int).Rsh(denom, 1)
	sum := new(big.Int).Add(num, half)
	quo := new(big.Int).Quo(sum, denom)

	if !quo.IsInt64() {
		return 0, fmt.Errorf("rate out of bounds for int64: %s", s)
	}
	res := quo.Int64()
	if res <= 0 {
		return 0, fmt.Errorf("rate must be positive: %s", s)
	}
	return res, nil
}
