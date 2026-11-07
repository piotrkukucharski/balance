# Database Migrations (`migrations`)

## Overview
Intermediate blueprint for database schema migrations embedded into the application binary, managing shared main metadata and isolated tenant databases.

## Subdirectories & Boundaries
- [Main Migrations](./main/README.md): Global schema: users, sessions, budgets, members, PATs, FX rates.
- [Tenant Migrations](./tenant/README.md): Per-budget tenant schema: accounts, envelopes, targets, transactions, automations.
