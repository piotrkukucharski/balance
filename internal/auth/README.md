# Authentication & Token Security (`internal/auth`)

## Overview
Authentication security primitives, password hashing with Argon2id, CSPRNG token generation, and Personal Access Token (PAT) cryptographic verification.

## Interface
- `func IsRegistrationAllowed(userCount int) bool` [auth.go:18]
- `func HashPassword(password string) (string, error)` [auth.go:37]
- `func VerifyPassword(password, hash string) (bool, error)` [auth.go:55]
- `func GenerateCSPRNGToken(bytesLen int) (string, error)` [auth.go:98]
- `func HashToken(token string) string` [auth.go:107]
- `func GeneratePAT() (rawToken string, tokenHash string, err error)` [auth.go:113]

## Invariants
- Password hashing enforces Argon2id parameters (64MB memory, 3 iterations, 2 parallelism, 32-byte salt/key) [auth.go:25-35]
- Session and PAT tokens are generated using crypto/rand CSPRNG and stored strictly as SHA-256 hashes [auth.go:98-115]

## Side Effects
- Computes cryptographic hashes utilizing CPU and memory resources [auth.go:37-60, 107-115]

## Verification
- Run: `go test ./...`
