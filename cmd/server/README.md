# Server CLI (`cmd/server`)

## Overview
Main HTTP server and background worker CLI entry point for Balance.

## Interface
- `server (default HTTP server on PORT or 8080)`
- `server worker:sync-fx [flags] / server sync-fx [flags]`
- `server worker:advance-targets [flags] / server advance-targets [flags]`

## Invariants
- Server startup applies migrations to main.db (migrations/main) and initializes TenantDBManager with a pool of up to 64 cached tenant DB connections (main.go:65-76)
- Multi-layer authentication pipeline: public routes (/login, /register, static assets), session/bearer-authenticated workspace endpoints, and tenant-scoped role-based authorization (VIEWER, EDITOR, ADMIN, OWNER) (main.go:131, 142, 185, 251, 257)
- REST API v1 and OpenAPI 3.1 documentation exposed at /api/v1 and /api/docs (main.go:274-279)
- Scheduler subsystem starts in configured SCHEDULER_MODE and shuts down gracefully upon context termination (main.go:287-301)

## Side Effects
- Listens on TCP network socket (PORT, default 8080)
- Creates or opens DATA_DIR/main.db and tenant SQLite databases in DATA_DIR/budgets
- Runs periodic scheduler routines and background CLI worker operations

## Verification
- Run: `go test ./...`
