# Automated Workflows & Rules Engine (`internal/automation`)

## Overview
Automated workflow engine managing transaction rules, multi-condition triggers, bulk action executions, and versioned automation histories.

## Interface
- `type Automation struct` [service.go:22]
- `type CreateAutomationInput struct` [service.go:36]
- `type UpdateAutomationInput struct` [service.go:47]
- `type MultiSquashInput struct` [service.go:59]
- `type TxChange struct` [service.go:69]
- `type BatchExecutionResult struct` [service.go:84]
- `type AutomationService struct` [service.go:95]
- `func NewAutomationService(db *sql.DB) *AutomationService` [service.go:100]

## Invariants
- Automations execute transaction mutations atomically within a single database transaction [service.go:150-185]
- Historical versions track change diffs with squash support for audit consolidation [service.go:210-260]
- Jev classification primitives (jev.choice, jev.noul) are available in scripts when JEV_API_KEY is configured, bounded by max 3 calls and 1.0s timeout per call [service.go:18, lua/jev_lib.go:12]

## Side Effects
- Mutates tenant SQLite database tables (automations, raw_transactions, ledger_transactions) [service.go:140-300]

## Verification
- Run: `go test ./...`
