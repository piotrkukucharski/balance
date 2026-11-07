# Migrations Package (`internal/db/migrations`)

## Overview
Intermediate blueprint embedding SQL schema migration files for the global main SQLite database and isolated per-tenant budget databases.

## Subdirectories & Boundaries
- [Main Migrations](./main/README.md): Main database schema definitions initializing core multi-tenant metadata tables (users, sessions, personal_access_tokens, budgets, budget_members, exchange_rates).
- [Tenant Migrations](./tenant/README.md): Tenant budget SQLite schema migrations configuring envelope-budgeting domain tables, audit log triggers, and analytical projections.
