package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed all:migrations
var MigrationsFS embed.FS

const (
	DefaultBusyTimeoutMs = 5000
	DefaultCacheSizeKiB  = -16000 // 16 MiB negative value
	DefaultMaxOpenConns  = 4
	DefaultMaxIdleConns  = 2
	MaxCachedTenants     = 64
)

// ApplyPragmas executes mandatory SQLite configuration.
func ApplyPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		fmt.Sprintf("PRAGMA busy_timeout = %d;", DefaultBusyTimeoutMs),
		"PRAGMA synchronous = NORMAL;",
		fmt.Sprintf("PRAGMA cache_size = %d;", DefaultCacheSizeKiB),
		"PRAGMA foreign_keys = true;",
		"PRAGMA temp_store = memory;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("failed executing pragma %q: %w", p, err)
		}
	}
	return nil
}

// OpenDB opens a SQLite database, configures pool limits and applies PRAGMAs.
func OpenDB(dsn string) (*sql.DB, error) {
	ctx := context.Background()
	ctx = logger.WithComponent(ctx, "infra:db")

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		logger.Error(ctx, "Failed opening SQLite database", "dsn", dsn, "error", err.Error())
		return nil, err
	}
	db.SetMaxOpenConns(DefaultMaxOpenConns)
	db.SetMaxIdleConns(DefaultMaxIdleConns)

	if err := ApplyPragmas(db); err != nil {
		logger.Error(ctx, "Failed applying SQLite PRAGMAs", "dsn", dsn, "error", err.Error())
		db.Close()
		return nil, err
	}
	logger.Info(ctx, "SQLite database connection initialized", "dsn", dsn, "max_open", DefaultMaxOpenConns, "max_idle", DefaultMaxIdleConns)
	return db, nil
}

// ApplyMigrations executes all SQL statements in a given directory from MigrationsFS.
func ApplyMigrations(db *sql.DB, dir string) error {
	ctx := context.Background()
	ctx = logger.WithComponent(ctx, "infra:migrations")

	entries, err := MigrationsFS.ReadDir(dir)
	if err != nil {
		logger.Error(ctx, "Failed reading migrations directory", "dir", dir, "error", err.Error())
		return fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		content, err := MigrationsFS.ReadFile(path)
		if err != nil {
			logger.Error(ctx, "Failed reading migration file", "file", path, "error", err.Error())
			return fmt.Errorf("read file %s: %w", path, err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			logger.Error(ctx, "Failed applying migration script", "file", path, "error", err.Error())
			return fmt.Errorf("execute migration %s: %w", path, err)
		}
		logger.Info(ctx, "Database migration executed successfully", "file", path)
	}
	return nil
}

// TenantDBManager handles LRU cached access to tenant databases with filesystem path guards.
type TenantDBManager struct {
	mu         sync.Mutex
	budgetsDir string
	mainDB     *sql.DB
	tenants    map[string]*tenantEntry
	lruHead    *tenantEntry
	lruTail    *tenantEntry
	maxCached  int
}

type tenantEntry struct {
	budgetID string
	db       *sql.DB
	prev     *tenantEntry
	next     *tenantEntry
}

func NewTenantDBManager(budgetsDir string, mainDB *sql.DB, maxCached int) (*TenantDBManager, error) {
	if err := os.MkdirAll(budgetsDir, 0755); err != nil {
		return nil, err
	}
	cleanDir, err := filepath.Abs(budgetsDir)
	if err != nil {
		return nil, err
	}
	if maxCached <= 0 {
		maxCached = MaxCachedTenants
	}
	return &TenantDBManager{
		budgetsDir: cleanDir,
		mainDB:     mainDB,
		tenants:    make(map[string]*tenantEntry),
		maxCached:  maxCached,
	}, nil
}

// VerifyUserMembership checks if a user is a member of the budget and returns their role.
func (m *TenantDBManager) VerifyUserMembership(ctx context.Context, budgetID, userID string) (string, error) {
	if !domain.IsValidUUIDv7(budgetID) {
		return "", errors.New("invalid budget ID format")
	}
	var role string
	err := m.mainDB.QueryRowContext(ctx, "SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?", budgetID, userID).Scan(&role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("unauthorized: user is not a member of this budget")
		}
		return "", err
	}
	return role, nil
}

// GetTenantDB opens or retrieves from cache the tenant DB for budgetID after path validation.
func (m *TenantDBManager) GetTenantDB(budgetID string, allowCreate bool) (*sql.DB, error) {
	ctx := context.Background()
	ctx = logger.WithComponent(ctx, "infra:tenant_manager")

	if !domain.IsValidUUIDv7(budgetID) {
		logger.Warn(ctx, "Invalid budget ID format provided to tenant manager", "budget_id", budgetID)
		return nil, errors.New("invalid budget ID format")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, exists := m.tenants[budgetID]; exists {
		m.moveToHead(entry)
		logger.Debug(ctx, "Tenant DB retrieved from LRU cache", "budget_id", budgetID, "cached_count", len(m.tenants))
		return entry.db, nil
	}

	dbPath := filepath.Join(m.budgetsDir, budgetID+".db")
	if filepath.Dir(dbPath) != m.budgetsDir {
		logger.Error(ctx, "Path traversal attempt detected in tenant manager", "budget_id", budgetID, "path", dbPath)
		return nil, errors.New("path traversal detected")
	}

	if !allowCreate {
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			logger.Warn(ctx, "Tenant database does not exist", "budget_id", budgetID, "path", dbPath)
			return nil, fmt.Errorf("tenant database for budget %s does not exist", budgetID)
		}
	}

	dsn := fmt.Sprintf("file:%s?_journal=WAL", dbPath)
	db, err := OpenDB(dsn)
	if err != nil {
		logger.Error(ctx, "Failed to open tenant database", "budget_id", budgetID, "error", err.Error())
		return nil, err
	}

	// Ensure backward-compatibility schema migrations for older tenant databases
	_ = ensureTenantSchemaCompat(ctx, db)

	// Always ensure schema is applied
	if err := ApplyMigrations(db, "migrations/tenant"); err != nil {
		db.Close()
		logger.Error(ctx, "Tenant migrations failed", "budget_id", budgetID, "error", err.Error())
		return nil, fmt.Errorf("tenant migrations failed: %w", err)
	}

	entry := &tenantEntry{
		budgetID: budgetID,
		db:       db,
	}
	m.tenants[budgetID] = entry
	m.addToHead(entry)
	logger.Info(ctx, "Tenant database loaded into LRU cache", "budget_id", budgetID, "cached_count", len(m.tenants), "max_cached", m.maxCached)

	if len(m.tenants) > m.maxCached {
		m.evictTail()
	}

	return db, nil
}

func (m *TenantDBManager) moveToHead(e *tenantEntry) {
	m.removeNode(e)
	m.addToHead(e)
}

func (m *TenantDBManager) addToHead(e *tenantEntry) {
	e.next = m.lruHead
	e.prev = nil
	if m.lruHead != nil {
		m.lruHead.prev = e
	}
	m.lruHead = e
	if m.lruTail == nil {
		m.lruTail = e
	}
}

func (m *TenantDBManager) removeNode(e *tenantEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		m.lruHead = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		m.lruTail = e.prev
	}
}

func (m *TenantDBManager) evictTail() {
	if m.lruTail == nil {
		return
	}
	tail := m.lruTail
	m.removeNode(tail)
	delete(m.tenants, tail.budgetID)
	tail.db.Close()
	ctx := logger.WithComponent(context.Background(), "infra:tenant_manager")
	logger.Info(ctx, "Tenant database evicted from LRU cache", "budget_id", tail.budgetID, "remaining_cached", len(m.tenants))
}

// CloseTenantDB closes and evicts a specific tenant DB from LRU cache.
func (m *TenantDBManager) CloseTenantDB(budgetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, exists := m.tenants[budgetID]; exists {
		m.removeNode(entry)
		delete(m.tenants, budgetID)
		entry.db.Close()
		ctx := logger.WithComponent(context.Background(), "infra:tenant_manager")
		logger.Info(ctx, "Tenant database closed and evicted from LRU cache", "budget_id", budgetID)
	}
}

// DeleteTenantDB closes the tenant DB and removes its database and WAL/SHM files from disk.
func (m *TenantDBManager) DeleteTenantDB(budgetID string) error {
	if !domain.IsValidUUIDv7(budgetID) {
		return errors.New("invalid budget ID format")
	}

	m.CloseTenantDB(budgetID)

	dbPath := filepath.Join(m.budgetsDir, budgetID+".db")
	if filepath.Dir(dbPath) != m.budgetsDir {
		return errors.New("path traversal detected")
	}

	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove tenant db file: %w", err)
	}

	ctx := logger.WithComponent(context.Background(), "infra:tenant_manager")
	logger.Info(ctx, "Tenant database files deleted from disk", "budget_id", budgetID)
	return nil
}

func (m *TenantDBManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx := logger.WithComponent(context.Background(), "infra:tenant_manager")
	count := len(m.tenants)
	for _, entry := range m.tenants {
		entry.db.Close()
	}
	m.tenants = make(map[string]*tenantEntry)
	m.lruHead = nil
	m.lruTail = nil
	logger.Info(ctx, "All tenant database connections closed", "closed_count", count)
}

func ensureTenantSchemaCompat(ctx context.Context, db *sql.DB) error {
	var tableCount int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='raw_transactions_current'").Scan(&tableCount)
	if tableCount > 0 {
		var hasBookingStatusCurr int
		_ = db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('raw_transactions_current') WHERE name = 'booking_status'").Scan(&hasBookingStatusCurr)
		if hasBookingStatusCurr == 0 {
			if _, err := db.ExecContext(ctx, "ALTER TABLE raw_transactions_current ADD COLUMN booking_status TEXT NOT NULL DEFAULT 'BOOKED'"); err != nil {
				logger.Error(ctx, "Failed adding booking_status to raw_transactions_current", "error", err.Error())
			}
			_, _ = db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_raw_tx_curr_booking_status ON raw_transactions_current(booking_status)")
		}
	}

	var auditTableCount int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='raw_transactions'").Scan(&auditTableCount)
	if auditTableCount > 0 {
		var hasBookingStatusAudit int
		_ = db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('raw_transactions') WHERE name = 'booking_status'").Scan(&hasBookingStatusAudit)
		if hasBookingStatusAudit == 0 {
			if _, err := db.ExecContext(ctx, "ALTER TABLE raw_transactions ADD COLUMN booking_status TEXT NOT NULL DEFAULT 'BOOKED'"); err != nil {
				logger.Error(ctx, "Failed adding booking_status to raw_transactions", "error", err.Error())
			}
		}
	}

	var targetTableCount int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='targets_projection'").Scan(&targetTableCount)
	if targetTableCount > 0 {
		var hasRecurrenceInterval int
		_ = db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('targets_projection') WHERE name = 'recurrence_interval'").Scan(&hasRecurrenceInterval)
		if hasRecurrenceInterval == 0 {
			migrationSQL := `
				PRAGMA foreign_keys = OFF;
				CREATE TABLE targets_projection_compat (
					id TEXT PRIMARY KEY,
					bucket_id TEXT NOT NULL,
					name TEXT NOT NULL,
					target_type TEXT NOT NULL CHECK(target_type IN ('EXPENSE', 'SAVINGS', 'DEBT_PAYMENT')),
					priority INTEGER NOT NULL DEFAULT 2 CHECK(priority >= 1),
					sort_order INTEGER NOT NULL DEFAULT 0,
					amount_mode TEXT NOT NULL DEFAULT 'FIXED' CHECK(amount_mode IN ('FIXED', 'LUA_FORMULA')),
					amount_minor INTEGER NOT NULL DEFAULT 0 CHECK(amount_minor >= 0 AND amount_minor <= 100000000000000),
					lua_script TEXT CHECK(lua_script IS NULL OR length(lua_script) <= 2048),
					last_formula_error TEXT,
					currency TEXT NOT NULL,
					recurrence_cadence TEXT NOT NULL CHECK(recurrence_cadence IN ('ONE_OFF', 'WEEKLY', 'MONTHLY', 'ANNUAL', 'CUSTOM_DAYS', 'CUSTOM_MONTHS', 'CUSTOM_YEARS')),
					recurrence_interval INTEGER CHECK(recurrence_cadence NOT IN ('CUSTOM_DAYS', 'CUSTOM_MONTHS', 'CUSTOM_YEARS') OR (recurrence_interval IS NOT NULL AND recurrence_interval >= 1 AND recurrence_interval <= 36500)),
					due_date INTEGER CHECK(recurrence_cadence NOT IN ('CUSTOM_MONTHS', 'CUSTOM_YEARS') OR due_date IS NOT NULL),
					non_spending_strategy TEXT NOT NULL CHECK(non_spending_strategy IN (
						'ROLL_OVER', 
						'RETURN_TO_POOL', 
						'ARREARS_CUMULATIVE', 
						'CAP_AND_HOLD', 
						'PRORATE_FUTURE'
					)),
					ceiling_amount_minor INTEGER CHECK(ceiling_amount_minor IS NULL OR (ceiling_amount_minor >= 0 AND ceiling_amount_minor <= 100000000000000)),
					current_funded_minor INTEGER NOT NULL DEFAULT 0,
					is_archived INTEGER NOT NULL DEFAULT 0,
					version INTEGER NOT NULL,
					updated_at INTEGER NOT NULL,
					FOREIGN KEY (bucket_id) REFERENCES buckets_projection(id) ON DELETE CASCADE
				);
				INSERT INTO targets_projection_compat (
					id, bucket_id, name, target_type, priority, sort_order, amount_mode, amount_minor,
					lua_script, last_formula_error, currency, recurrence_cadence, recurrence_interval,
					due_date, non_spending_strategy, ceiling_amount_minor, current_funded_minor,
					is_archived, version, updated_at
				)
				SELECT
					id, bucket_id, name, target_type, priority, sort_order, amount_mode, amount_minor,
					lua_script, last_formula_error, currency, recurrence_cadence, recurrence_days,
					due_date, non_spending_strategy, ceiling_amount_minor, current_funded_minor,
					is_archived, version, updated_at
				FROM targets_projection;
				DROP TABLE targets_projection;
				ALTER TABLE targets_projection_compat RENAME TO targets_projection;
				CREATE INDEX IF NOT EXISTS idx_targets_proj_bucket ON targets_projection(bucket_id);
				CREATE INDEX IF NOT EXISTS idx_targets_proj_priority ON targets_projection(priority DESC, due_date ASC, sort_order ASC);
				PRAGMA foreign_keys = ON;
			`
			if _, err := db.ExecContext(ctx, migrationSQL); err != nil {
				logger.Error(ctx, "Failed migrating targets_projection schema to recurrence_interval", "error", err.Error())
			} else {
				logger.Info(ctx, "Successfully migrated targets_projection schema to recurrence_interval")
			}
		}
	}
	return nil
}


