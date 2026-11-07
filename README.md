# Balance Repository Blueprint

## System Overview
Balance is a multi-tenant personal finance and envelope-budgeting platform built with Go, Echo, SQLite, and server-side rendered HTML views. The system provides zero-based envelope budgeting, multi-currency accounting with historical exchange rates (ECB, NBP), bank statement parsing, Open Banking PSD2 synchronization via GoCardless, transaction deduplication and audit trails, automation rules powered by sandboxed Lua, and financial forecasting and analytics.

The repository follows a clean domain-driven architecture organized around command-line entry points, embedded migrations, and modularized internal subsystems.

## Subdirectories & System Boundaries
- [Command Entry Points](./cmd/README.md): Application CLI binaries (`cmd/server` for HTTP web/REST server and worker processes, `cmd/fixtures` for development data provisioning).
- [Internal Packages](./internal/README.md): Core business logic, domain models, multi-tenant SQLite managers, presentation handlers, background schedulers, reporting engines, and integrations.
- [Database Migrations](./migrations/README.md): Embedded SQL schema migrations managing the global central catalog (`migrations/main`) and isolated per-budget tenant databases (`migrations/tenant`).
- [Automation Scripts](./scripts/README.md): Operational tooling including worktree management, database backup routines, and environment orchestration.

## Global Invariants
- **Multi-Tenant SQLite Isolation**: Central authentication, user accounts, and budget memberships reside in `main.db`, while each budget's envelopes, transactions, accounts, and projections are kept in isolated tenant databases under `DATA_DIR/budgets/<budget-id>.db`.
- **Minor Unit Monetary Math**: All monetary amounts are represented as 64-bit signed integers in minor units (e.g. cents, grosze) bounded by `MaxSafeAmountMinor` (9,007,199,254,740,991) to eliminate floating-point imprecision.
- **Microsecond Epoch Timestamps**: Timestamps in event logs, ledger entries, and audit projections are stored as 64-bit integers representing UTC microseconds.
- **CQRS & Audit Trails**: Tenant transactions maintain append-only audit histories in `raw_transactions` alongside current projection tables (`raw_transactions_current`). Deduplication hashes deterministically prevent duplicate imports.
- **Sandboxed Execution**: Custom mathematical formulas and user-defined automation rules execute in a sandboxed GopherLua runtime without network or filesystem access.

## Global Side Effects
- **Filesystem & Database Storage**: Manages SQLite database files with WAL journal mode, creating directories under `DATA_DIR` (`main.db` and `budgets/*.db`).
- **Network Services**: Binds HTTP server on configurable TCP ports (`PORT`, default 8080), serving web UI, REST API v1 (`/api/v1`), and OpenAPI documentation (`/api/docs`).
- **External Network Invocations**: Communicates with external financial services via HTTPS (European Central Bank, National Bank of Poland, and GoCardless PSD2 APIs) and configured LLM providers for AI assistance.

## System Verification
- Run: `go test ./...`
