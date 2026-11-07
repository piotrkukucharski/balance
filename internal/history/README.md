# Audit History & Time-Travel Engine (`internal/history`)

## Overview
Entity audit history and time-travel engine providing unified JSON diff calculation, event inspection, and state restoration across tenant entities.

## Interface
- `type FieldDiff struct` [engine.go:23]
- `type HistoryItem struct` [engine.go:31]
- `type EntityHistoryResult struct` [engine.go:45]
- `type RestoreResult struct` [engine.go:55]
- `func NormalizeEntityType(raw string) (string, error)` [engine.go:63]
- `func FormatEpochMicro(epochMicro int64, loc *time.Location) string` [engine.go:85]
- `type HistoryEngine struct` [engine.go:100]
- `func NewHistoryEngine(db *sql.DB) *HistoryEngine` [engine.go:105]
- `func (e *HistoryEngine) QueryHistory(ctx context.Context, entityType, entityID string) (*EntityHistoryResult, error)` [engine.go:120]
- `func (e *HistoryEngine) RestoreEntity(ctx context.Context, entityType, entityID string, targetMicro int64) (*RestoreResult, error)` [engine.go:180]

## Invariants
- History logs are strictly append-only in tenant audit tables [engine.go:120-150]
- State restoration reconstructs entity snapshots by replaying events up to target microsecond timestamp [engine.go:180-230]

## Side Effects
- Mutates tenant SQLite entity tables during rollback/restore actions [engine.go:220-250]

## Verification
- Run: `go test ./...`
