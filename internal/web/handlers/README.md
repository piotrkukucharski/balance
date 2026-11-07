# Web Handlers & Middlewares (`internal/web/handlers`)

## Overview
HTTP web controllers, session/bearer authentication middlewares, CSRF validators, and server-side rendered endpoints for the Balance platform.

## Interface
- `func CorrelationIDMiddleware() echo.MiddlewareFunc` (middleware.go:33)
- `func StructuredRequestLoggerMiddleware() echo.MiddlewareFunc` (middleware.go:51)
- `func SessionOrBearerAuthMiddleware(mainDB *sql.DB) echo.MiddlewareFunc` (middleware.go:108)
- `func TenantAuthzMiddleware(tenantMgr *db.TenantDBManager, minRole string) echo.MiddlewareFunc` (middleware.go:250)
- `type AuthHandler struct` (auth.go:25)
- `type WorkspaceHandler struct` (workspace.go:94)
- `type AccountsHandler struct` (accounts.go:24)
- `type TransactionsHandler struct` (transactions.go:31)
- `type ReportsHandler struct` (reports.go:22)
- `type PartiesHandler struct` (parties.go:22)
- `type MembersHandler struct` (members.go:18)
- `type ProfileHandler struct` (profile.go:18)
- `type HistoryHandler struct` (history.go:20)
- `type AutomationsHandler struct` (automations.go:23)
- `type DebugHandler struct` (debug.go:29)
- `type PWAHandler struct` (pwa.go:9)
- `type TokensHandler struct` (tokens.go:34)

## Invariants
- Session authentication checks SQLite sessions table and enforces CSRF tokens on state-mutating HTTP methods POST, PUT, PATCH, and DELETE (middleware.go:183-213)
- Tenant authorization middleware enforces hierarchical role ordering VIEWER < EDITOR < ADMIN < OWNER before opening tenant SQLite database (middleware.go:251-282)

## Side Effects
- Sets and deletes HTTP session cookies (SessionCookieName = balance_session) (middleware.go:21, 188-195)
- Mutates central and tenant SQLite databases during budget allocations, entity creations, updates, and rollbacks
- Emits structured log messages with correlation IDs across all inbound HTTP requests (middleware.go:50-104)

## Verification
- Run: `go test ./...`
