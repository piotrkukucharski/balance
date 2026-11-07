# Main Database Migrations (`migrations/main`)

## Overview
Main database schema migrations embedded into internal/db.MigrationsFS under migrations/main, managing users, budgets, sessions, memberships, exchange rates, and API tokens.

## Interface
- `internal/db.ApplyMigrations(mainDB, "migrations/main")` (internal/db/db.go:71-102)

## Invariants
- Executed sequentially in alphabetical filename order from the embedded filesystem (internal/db/db.go:85-100)
- Backward-compatibility checks via ensureMainSchemaCompat ensure user table schema additions (e.g. time_format) succeed gracefully (internal/db/db.go:76-78, 597-612)
- Foreign key constraints and WAL pragma configuration are enforced on SQLite database connections prior to migration runs (internal/db/db.go:31-46, 61)

## Side Effects
- Executes DDL statements modifying main.db schema (tables: users, budgets, budget_members, sessions, api_tokens, fx_rates, etc.)

## Verification
- Run: `go test ./...`
