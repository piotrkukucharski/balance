# Main Database Schema Migrations (`internal/db/migrations/main`)

## Overview
Main database schema definitions initializing core multi-tenant metadata tables (users, sessions, personal_access_tokens, budgets, budget_members, exchange_rates).

## Interface
- `migrations/main/001_init.sql`

## Invariants
- Users table enforces unique email addresses and required argon2id password hashes [001_init.sql:users]
- Sessions enforce SHA-256 token hash indexing and foreign key cascade on user deletion [001_init.sql:sessions]
- Personal access tokens enforce SHA-256 token hash uniqueness and budget/user binding [001_init.sql:personal_access_tokens]
- Budgets enforce UUIDv7 identifiers, owner user binding, and default ISO-4217 currencies [001_init.sql:budgets]
- Budget members map user roles (OWNER, ADMIN, MEMBER, VIEWER) with composite primary key (budget_id, user_id) [001_init.sql:budget_members]
- Exchange rates store ECB and NBP historical currency conversion rates indexed by date and currency pair [001_init.sql:exchange_rates]

## Side Effects
- Executes DDL statements creating users, sessions, personal_access_tokens, budgets, budget_members, and exchange_rates tables and indexes in main.db [001_init.sql:DDL]

## Verification
- Run: `go test ./...`
