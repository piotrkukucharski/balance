# Envelope Budgeting Engine (`internal/budget`)

## Overview
Envelope budgeting core domain engine handling bucket CRUD, target evaluations, multi-currency allocations, ready-to-assign (RTA) math, and budget horizons.

## Interface
- `type TargetDueAdvanceResult struct` [advance.go:13]
- `func AdvanceOverdueTargetDueDatesForTenant(ctx context.Context, tenantDB *sql.DB, today time.Time) (*TargetDueAdvanceResult, error)` [advance.go:20]
- `type CreateBucketInput struct` [crud.go:32]
- `type UpdateBucketInput struct` [crud.go:40]
- `type CreateTargetInput struct` [crud.go:49]
- `type UpdateTargetInput struct` [crud.go:72]
- `type BudgetEngine struct` [engine.go:30]
- `func NewBudgetEngine(db *sql.DB) *BudgetEngine` [engine.go:35]
- `func CalculateRTA(ctx context.Context, tx *sql.Tx) (int64, error)` [engine.go:120]

## Invariants
- Ready-To-Assign (RTA) = Total Liquid Account Balances - Total Envelopes Assigned [engine.go:120-160]
- Target due date advancement preserves recurrence schedules without skipping intervals [advance.go:25-85]
- Bucket allocations and group assignments maintain referential integrity [groups.go:30-90, crud.go:100-200]

## Side Effects
- Mutates tenant SQLite database tables (buckets_projection, targets_projection, budget_allocations) [crud.go:80-250, engine.go:150-250]

## Verification
- Run: `go test ./...`
