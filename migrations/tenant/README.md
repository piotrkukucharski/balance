# Tenant Database Migrations (`migrations/tenant`)

## Overview
Tenant budget database schema migrations embedded into internal/db.MigrationsFS under migrations/tenant, managing accounts, transactions, buckets, targets, automations, and projections.

## Interface
- `internal/db.ApplyMigrations(tenantDB, "migrations/tenant")` (internal/db/db.go:71-102, 200)

## Invariants
- Applied automatically whenever a tenant database connection is opened or instantiated via TenantDBManager.GetTenantDB (internal/db/db.go:200-204)
- Backward-compatibility checks via ensureTenantSchemaCompat migrate booking_status, targets_projection recurrence schedules, bucket_groups, and report_cache (internal/db/db.go:197, 380-595)
- Transactions maintain deduplication hashes and append-only audit trail invariants with current projection tables (raw_transactions, raw_transactions_current)

## Side Effects
- Executes DDL/DML statements altering tenant SQLite databases located at DATA_DIR/budgets/<budget-id>.db

## Verification
- Run: `go test ./...`
