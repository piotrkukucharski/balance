# Core Domain Primitives (`internal/domain`)

## Overview
Core domain primitives and validation functions, including UUIDv7 generation, minor unit monetary bounds checking, and safe currency math.

## Interface
- `const MaxSafeAmountMinor = 9007199254740991` [primitives.go:15]
- `func NewUUIDv7() string` [primitives.go:22]
- `func IsValidUUIDv7(id string) bool` [primitives.go:32]
- `func ValidateAmountMinor(amount int64) error` [primitives.go:37]
- `func ConvertCurrencySafe(amountMinor int64, rate float64) (int64, error)` [primitives.go:52]

## Invariants
- All monetary amounts are represented as 64-bit signed integers in minor currency units (cents, grosze) [primitives.go:37-45]
- UUIDs strictly adhere to RFC 9562 UUIDv7 format with millisecond time sortability [primitives.go:22-35]
- Safe currency conversion prevents integer overflow beyond MaxSafeAmountMinor [primitives.go:52-70]

## Side Effects
- Reads OS system entropy for random bits during UUIDv7 generation [primitives.go:23]

## Verification
- Run: `go test ./...`
