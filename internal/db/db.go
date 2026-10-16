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
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(DefaultMaxOpenConns)
	db.SetMaxIdleConns(DefaultMaxIdleConns)

	if err := ApplyPragmas(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// ApplyMigrations executes all SQL statements in a given directory from MigrationsFS.
func ApplyMigrations(db *sql.DB, dir string) error {
	entries, err := MigrationsFS.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		content, err := MigrationsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read file %s: %w", path, err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			return fmt.Errorf("execute migration %s: %w", path, err)
		}
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
	if !domain.IsValidUUIDv7(budgetID) {
		return nil, errors.New("invalid budget ID format")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, exists := m.tenants[budgetID]; exists {
		m.moveToHead(entry)
		return entry.db, nil
	}

	dbPath := filepath.Join(m.budgetsDir, budgetID+".db")
	if filepath.Dir(dbPath) != m.budgetsDir {
		return nil, errors.New("path traversal detected")
	}

	if !allowCreate {
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("tenant database for budget %s does not exist", budgetID)
		}
	}

	dsn := fmt.Sprintf("file:%s?_journal=WAL", dbPath)
	db, err := OpenDB(dsn)
	if err != nil {
		return nil, err
	}

	// Always ensure schema is applied
	if err := ApplyMigrations(db, "migrations/tenant"); err != nil {
		db.Close()
		return nil, fmt.Errorf("tenant migrations failed: %w", err)
	}

	entry := &tenantEntry{
		budgetID: budgetID,
		db:       db,
	}
	m.tenants[budgetID] = entry
	m.addToHead(entry)

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
}

func (m *TenantDBManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.tenants {
		entry.db.Close()
	}
	m.tenants = make(map[string]*tenantEntry)
	m.lruHead = nil
	m.lruTail = nil
}
