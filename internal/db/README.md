# Database Connection & Migration Management (`internal/db`)

## Overview
Database connection lifecycle and multi-tenant SQLite manager providing WAL PRAGMA configuration, embedded schema migration execution, tenant database pooling with LRU eviction, filesystem path traversal guards, and VACUUM INTO cloning.

## Interface
- `var MigrationsFS embed.FS` [db.go:19-20]
- `const DefaultBusyTimeoutMs = 5000` [db.go:23]
- `const DefaultCacheSizeKiB = -16000` [db.go:24]
- `const DefaultMaxOpenConns = 4` [db.go:25]
- `const DefaultMaxIdleConns = 2` [db.go:26]
- `const MaxCachedTenants = 64` [db.go:119]
- `func ApplyPragmas(db *sql.DB) error` [db.go:31]
- `func OpenDB(dsn string) (*sql.DB, error)` [db.go:50]
- `func ApplyMigrations(db *sql.DB, dir string) error` [db.go:71]
- `type TenantDBManager struct` [db.go:121]
- `func NewTenantDBManager(budgetsDir string, mainDB *sql.DB, maxCached int) (*TenantDBManager, error)` [db.go:138]
- `func (m *TenantDBManager) VerifyUserMembership(ctx context.Context, budgetID string, userID string) (string, error)` [db.go:159]
- `func (m *TenantDBManager) GetTenantDB(budgetID string, allowCreate bool) (*sql.DB, error)` [db.go:179]
- `func (m *TenantDBManager) CloseTenantDB(budgetID string)` [db.go:214]
- `func (m *TenantDBManager) DeleteTenantDB(budgetID string) error` [db.go:243]
- `func (m *TenantDBManager) CloneTenantDB(sourceBudgetID string, targetBudgetID string) (*sql.DB, error)` [db.go:273]
- `func (m *TenantDBManager) CloseAll()` [db.go:343]
- `func (m *TenantDBManager) GetBudgetTimezone(ctx context.Context, budgetID string) (string, error)` [db.go:362]
- [Migrations](./migrations/README.md)

## Invariants
- Every database connection is configured with WAL journal mode, 5000ms busy timeout, NORMAL synchronous, -16000 cache size, foreign keys enabled, and in-memory temp store [db.go:ApplyPragmas]
- Connection pools default to 4 max open connections and 2 max idle connections [db.go:OpenDB]
- Tenant database access enforces strict UUIDv7 format validation and filepath traversal guards against directory climbing [db.go:TenantDBManager.GetTenantDB, db.go:TenantDBManager.DeleteTenantDB, db.go:TenantDBManager.CloneTenantDB]
- LRU cache tracks open tenant SQLite handles up to maxCached (default 64) and safely closes evicted database handles [db.go:TenantDBManager.evictTail]
- Tenant cloning truncates WAL checkpoints on source and uses VACUUM INTO to prevent corruption during online duplication [db.go:TenantDBManager.CloneTenantDB]
- Schema migrations automatically run on tenant database open and verify backward compatibility schema alterations [db.go:TenantDBManager.GetTenantDB, db.go:ensureTenantSchemaCompat, db.go:ensureMainSchemaCompat]

## Side Effects
- Executes PRAGMA statements altering SQLite execution modes and memory usage [db.go:ApplyPragmas]
- Executes DDL statements applying migrations from embedded filesystem [db.go:ApplyMigrations]
- Creates budget storage directory tree on filesystem [db.go:NewTenantDBManager]
- Opens, locks, creates, and evicts SQLite file handles on disk [db.go:TenantDBManager.GetTenantDB]
- Deletes database, -wal, and -shm files from disk on deletion [db.go:TenantDBManager.DeleteTenantDB]
- Writes database backup files using SQLite VACUUM INTO commands [db.go:TenantDBManager.CloneTenantDB]
- Emits structured slog records tagged with infra:db, infra:migrations, and infra:tenant_manager components [db.go:logger]

## Verification
- Run: `go test ./...`
