package domain

import (
	"errors"
	"math/big"
	"regexp"

	"github.com/google/uuid"
)

// Constants as defined in specification
const (
	MaxSafeAmountMinor int64 = 100_000_000_000_00 // 10^14 minor units (100 billion major units)
	MicroUnitMultiplier int64 = 1_000_000
)

var (
	uuidv7Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// NewUUIDv7 generates a canonical RFC 9562 UUIDv7 string.
func NewUUIDv7() string {
	val, err := uuid.NewV7()
	if err != nil {
		// Fallback to random V4 if V7 clock fails, but normally V7 succeeds
		return uuid.NewString()
	}
	return val.String()
}

// IsValidUUIDv7 checks if a string is a valid UUIDv7 format.
func IsValidUUIDv7(s string) bool {
	return uuidv7Regex.MatchString(s)
}

// ValidateAmountMinor ensures amount is strictly within safe bounds [0, 10^14].
func ValidateAmountMinor(amount int64, allowZero bool) error {
	if allowZero && amount == 0 {
		return nil
	}
	if amount <= 0 {
		return errors.New("amount must be strictly positive")
	}
	if amount > MaxSafeAmountMinor {
		return errors.New("amount exceeds maximum safe minor amount (10^14)")
	}
	return nil
}

// ConvertCurrencySafe converts an amount in minor units using a rate scaled by 1,000,000 (micros).
// Uses 128-bit safe arithmetic (math/big) to eliminate 64-bit intermediate multiplication overflow.
func ConvertCurrencySafe(amountMinor int64, rateMicros int64) (int64, error) {
	if rateMicros <= 0 {
		return 0, errors.New("exchange rate must be strictly positive")
	}
	if err := ValidateAmountMinor(amountMinor, true); err != nil {
		return 0, err
	}

	a := big.NewInt(amountMinor)
	r := big.NewInt(rateMicros)
	m := big.NewInt(MicroUnitMultiplier)

	// product = a * r
	product := new(big.Int).Mul(a, r)
	// result = product / m
	result := new(big.Int).Quo(product, m)

	if !result.IsInt64() {
		return 0, errors.New("currency conversion result exceeds int64")
	}

	res := result.Int64()
	if res > MaxSafeAmountMinor {
		return 0, errors.New("converted amount exceeds maximum safe minor amount")
	}
	return res, nil
}
