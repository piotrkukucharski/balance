# Tenant Database Schema Migrations (`internal/db/migrations/tenant`)

## Overview
Tenant budget SQLite schema migrations configuring envelope-budgeting domain tables, audit log triggers, and analytical projections.

## Interface
- `migrations/tenant/001_init.sql`

## Invariants
- Parties table tracks payees and financial counterparties unique per budget [001_init.sql:parties]
- Accounts table defines liquid, asset, liability, and off-budget accounts with default currency and archived status [001_init.sql:accounts]
- Buckets projection manages envelope hierarchy, category groups, color codes, and targets [001_init.sql:buckets_projection]
- Raw transactions and ledger transactions maintain event-sourced immutability and append-only audit histories with triggers [001_init.sql:raw_transactions]
- Targets projection enforces non-spending strategies, recurrence rules, priority tiers, and optional Lua formula scripts [001_init.sql:targets_projection]
- Foreign key constraints enforce referential integrity across accounts, buckets, and transactions [001_init.sql:FK]

## Side Effects
- Executes DDL statements provisioning tenant envelope structures, transaction ledgers, audit logging tables, and triggers [001_init.sql:DDL]

## Verification
- Run: `go test ./...`
