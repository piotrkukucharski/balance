# Transaction Engine & Ledger (`internal/transaction`)

## Overview
Transaction lifecycle and double-entry ledger management engine handling deduplication hashing, resolution workflows, and CQRS projection updates.

## Interface
- `type InTxAutomationHook func(ctx context.Context, tx *sql.Tx, txID string) error` [engine.go:24]
- `var GlobalAutomationHook InTxAutomationHook` [engine.go:27]
- `func ComputeCanonicalDedupHash(accountID string, bookingDateMicro int64, amountMinor int64, payee, reference string, seq int) string` [engine.go:31]
- `type BudgetTxInput struct` [engine.go:53]
- `type ResolveResolutionInput struct` [engine.go:67]
- `func ExecuteResolution(ctx context.Context, tx *sql.Tx, input ResolveResolutionInput) error` [engine.go:87]

## Invariants
- Deduplication hash uniquely identifies transaction occurrences using SHA-256 over canonical fields [engine.go:31-48]
- Ledger updates maintain double-entry balance invariants between raw transactions and bucket allocations [engine.go:87-150]

## Side Effects
- Writes to raw_transactions, raw_transactions_current, and bucket_transactions in tenant database [engine.go:87-160]

## Verification
- Run: `go test ./...`
