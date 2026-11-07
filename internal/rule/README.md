# Transaction Rule Matching Engine (`internal/rule`)

## Overview
Deterministic rule matching engine applying prioritized condition sets to transaction streams for automated categorization.

## Interface
- `type MatchingRule struct` [engine.go:30]
- `type MatchingEngine struct` [engine.go:43]
- `func NewMatchingEngine() *MatchingEngine` [engine.go:49]
- `func (e *MatchingEngine) SetRules(rules []MatchingRule)` [engine.go:58]
- `type MatchResult struct` [engine.go:83]
- `func (e *MatchingEngine) Match(tx domain.Transaction) (*MatchResult, bool)` [engine.go:90]

## Invariants
- Rules are evaluated in strict priority order; first matching rule takes precedence [engine.go:90-110]
- Matching supports exact text, regex, and numeric amount ranges on payee, description, and counterparty IBAN [engine.go:65-85]

## Side Effects
- In-memory deterministic matching without external side effects [engine.go:45-120]

## Verification
- Run: `go test ./...`
