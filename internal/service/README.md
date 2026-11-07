# Application Business Services (`internal/service`)

## Overview
High-level application service layer coordinating multi-package business workflows, fixture provisioning, and transactional operations.

## Interface
- `type FixtureProgressReporter interface` [fixtures.go:15]
- `type LoadFixturesInput struct` [fixtures.go:22]
- `func LoadFixtures(ctx context.Context, input LoadFixturesInput) error` [fixtures.go:32]
- `type AppService struct` [service.go:31]
- `func NewAppService(mainDB *sql.DB, tenantMgr *db.TenantDBManager, clock clock.Clock) *AppService` [service.go:38]
- `func (s *AppService) Clock() clock.Clock` [service.go:50]

## Invariants
- Fixture loading guarantees idempotent teardown and deterministic data population [fixtures.go:32-90]
- Multi-tenant operations verify tenant authorization before accessing tenant databases [service.go:45-80]

## Side Effects
- Coordinates writes to main.db and tenant SQLite database files [fixtures.go:50-120, service.go:60-100]

## Verification
- Run: `go test ./...`
