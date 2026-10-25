package history

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestTenantDB(t *testing.T) (*sql.DB, func()) {
	tmpDir, err := os.MkdirTemp("", "history_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "tenant.db")
	tenantDB, err := db.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := db.ApplyMigrations(tenantDB, "migrations/tenant"); err != nil {
		t.Fatalf("failed to apply migrations: %v", err)
	}

	cleanup := func() {
		tenantDB.Close()
		os.RemoveAll(tmpDir)
	}

	return tenantDB, cleanup
}

func TestNormalizeEntityType(t *testing.T) {
	cases := []struct {
		input    string
		expected string
		err      bool
	}{
		{"parties", "parties", false},
		{"Party", "parties", false},
		{"account", "accounts", false},
		{"ACCOUNTS", "accounts", false},
		{"matching_rules", "matching_rules", false},
		{"matching-rule", "matching_rules", false},
		{"raw_transactions", "raw_transactions", false},
		{"budget_transactions", "budget_transactions", false},
		{"transactions", "budget_transactions", false},
		{"saved_reports", "saved_reports", false},
		{"invalid_type", "", true},
	}

	for _, tc := range cases {
		got, err := NormalizeEntityType(tc.input)
		if tc.err && err == nil {
			t.Errorf("expected error for %q, got nil", tc.input)
		} else if !tc.err && err != nil {
			t.Errorf("unexpected error for %q: %v", tc.input, err)
		} else if got != tc.expected {
			t.Errorf("expected %q for %q, got %q", tc.expected, tc.input, got)
		}
	}
}

func TestPartyHistoryAndRestore(t *testing.T) {
	ctx := context.Background()
	tenantDB, cleanup := setupTestTenantDB(t)
	defer cleanup()

	partyID := domain.NewUUIDv7()
	agent1 := domain.NewUUIDv7()
	agent2 := domain.NewUUIDv7()
	corr1 := domain.NewUUIDv7()
	corr2 := domain.NewUUIDv7()
	t1 := time.Now().Add(-10 * time.Minute).UnixMicro()
	t2 := time.Now().Add(-5 * time.Minute).UnixMicro()

	// 1. Initial version
	_, err := tenantDB.ExecContext(ctx, `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, 'Original Merchant', 'ORGANIZATION', 0, 'CREATE', ?, ?, ?)`,
		partyID, agent1, corr1, t1)
	if err != nil {
		t.Fatalf("failed inserting party v1: %v", err)
	}
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Original Merchant', 'ORGANIZATION', 0, ?, ?)`,
		partyID, agent1, t1)
	if err != nil {
		t.Fatalf("failed inserting party_current v1: %v", err)
	}

	// 2. Updated version
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
		VALUES (?, 2, 'Renamed Merchant', 'ORGANIZATION', 0, 'UPDATE', ?, ?, ?)`,
		partyID, agent2, corr2, t2)
	if err != nil {
		t.Fatalf("failed inserting party v2: %v", err)
	}
	_, err = tenantDB.ExecContext(ctx, `
		UPDATE parties_current SET version = 2, name = 'Renamed Merchant', agent_id = ?, updated_at = ? WHERE id = ?`,
		agent2, t2, partyID)
	if err != nil {
		t.Fatalf("failed updating party_current v2: %v", err)
	}

	// 3. Query history
	hist, err := GetEntityHistory(ctx, tenantDB, "parties", partyID)
	if err != nil {
		t.Fatalf("GetEntityHistory failed: %v", err)
	}
	if len(hist.Items) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(hist.Items))
	}
	// Check DESC order: v2 first, then v1
	if hist.Items[0].Version != 2 || hist.Items[1].Version != 1 {
		t.Fatalf("expected versions [2, 1], got [%d, %d]", hist.Items[0].Version, hist.Items[1].Version)
	}
	if !hist.Items[0].IsCurrent {
		t.Errorf("expected version 2 to be current")
	}
	if len(hist.Items[0].FieldDiffs) == 0 {
		t.Errorf("expected field diffs in version 2")
	}

	// 4. Restore version 1
	restorerAgent := domain.NewUUIDv7()
	restoreRes, err := RestoreEntityVersion(ctx, tenantDB, "parties", partyID, 1, restorerAgent, domain.NewUUIDv7(), time.Now().UnixMicro())
	if err != nil {
		t.Fatalf("RestoreEntityVersion failed: %v", err)
	}
	if restoreRes.NewVersion != 3 {
		t.Errorf("expected new version 3, got %d", restoreRes.NewVersion)
	}
	if restoreRes.RestoredFromVersion != 1 {
		t.Errorf("expected restored from 1, got %d", restoreRes.RestoredFromVersion)
	}

	// 5. Verify parties_current has name restored to 'Original Merchant' and version 3
	var curVer int
	var curName string
	err = tenantDB.QueryRowContext(ctx, `SELECT version, name FROM parties_current WHERE id = ?`, partyID).Scan(&curVer, &curName)
	if err != nil {
		t.Fatalf("failed querying parties_current: %v", err)
	}
	if curVer != 3 {
		t.Errorf("expected parties_current version 3, got %d", curVer)
	}
	if curName != "Original Merchant" {
		t.Errorf("expected restored name 'Original Merchant', got %q", curName)
	}

	// 6. Verify immutable history: versions 1 and 2 still exist intact
	var v1Count, v2Count int
	tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM parties WHERE id = ? AND version = 1 AND name = 'Original Merchant'`, partyID).Scan(&v1Count)
	tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM parties WHERE id = ? AND version = 2 AND name = 'Renamed Merchant'`, partyID).Scan(&v2Count)
	if v1Count != 1 || v2Count != 1 {
		t.Errorf("historical versions mutated: v1Count=%d, v2Count=%d", v1Count, v2Count)
	}
}

func TestBudgetTransactionHistoryAndRollbackBalances(t *testing.T) {
	ctx := context.Background()
	tenantDB, cleanup := setupTestTenantDB(t)
	defer cleanup()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UnixMicro()

	// Create user party & checking account
	userPartyID := domain.NewUUIDv7()
	tenantDB.ExecContext(ctx, `INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Me', 'INDIVIDUAL', 1, ?, ?)`, userPartyID, agentID, now)
	userAcctID := domain.NewUUIDv7()
	tenantDB.ExecContext(ctx, `INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, 'Checking', 'CHECKING', 'PLN', 0, ?, ?)`, userAcctID, userPartyID, agentID, now)
	tenantDB.ExecContext(ctx, `INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at) VALUES (?, 'PLN', 100000, ?)`, userAcctID, now) // 1000.00 PLN

	// Create external party & account
	extPartyID := domain.NewUUIDv7()
	tenantDB.ExecContext(ctx, `INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Supermarket', 'ORGANIZATION', 0, ?, ?)`, extPartyID, agentID, now)
	extAcctID := domain.NewUUIDv7()
	tenantDB.ExecContext(ctx, `INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, 'POS', 'EXTERNAL', 'PLN', 0, ?, ?)`, extAcctID, extPartyID, agentID, now)

	// Create bucket
	bucketID := domain.NewUUIDv7()
	tenantDB.ExecContext(ctx, `INSERT INTO buckets_projection (id, name, group_name, version, updated_at) VALUES (?, 'Groceries', 'Default', 1, ?)`, bucketID, now)
	tenantDB.ExecContext(ctx, `INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor) VALUES (?, 'PLN', 50000)`, bucketID) // 500.00 PLN

	txID := domain.NewUUIDv7()

	// Initial version 1: Expense of 100.00 PLN (10000 minor)
	// Deducts 100.00 from user account (1000 - 100 = 900 PLN) and 100.00 from bucket (500 - 100 = 400 PLN)
	tenantDB.ExecContext(ctx, `
		INSERT INTO budget_transactions (
			id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
			destination_amount_minor, destination_currency, bucket_id, transaction_date, notes,
			action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, 10000, 'PLN', 10000, 'PLN', ?, ?, 'Weekly Groceries', 'CREATE', ?, ?, ?)`,
		txID, userAcctID, extAcctID, bucketID, now, agentID, corrID, now)
	tenantDB.ExecContext(ctx, `
		INSERT INTO budget_transactions_current (
			id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
			destination_amount_minor, destination_currency, bucket_id, transaction_date, notes, updated_at
		) VALUES (?, 1, ?, ?, 10000, 'PLN', 10000, 'PLN', ?, ?, 'Weekly Groceries', ?)`,
		txID, userAcctID, extAcctID, bucketID, now, now)
	tenantDB.ExecContext(ctx, `UPDATE account_currency_balances SET balance_minor = 90000 WHERE account_id = ?`, userAcctID)
	tenantDB.ExecContext(ctx, `UPDATE bucket_currency_balances SET balance_minor = 40000 WHERE bucket_id = ?`, bucketID)

	// Version 2: Modified to 300.00 PLN (30000 minor)
	// Deducts additional 200.00 PLN -> account: 700 PLN, bucket: 200 PLN
	tenantDB.ExecContext(ctx, `
		INSERT INTO budget_transactions (
			id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
			destination_amount_minor, destination_currency, bucket_id, transaction_date, notes,
			action, agent_id, correlation_id, created_at
		) VALUES (?, 2, ?, ?, 30000, 'PLN', 30000, 'PLN', ?, ?, 'Weekly Groceries (Updated)', 'UPDATE', ?, ?, ?)`,
		txID, userAcctID, extAcctID, bucketID, now, agentID, corrID, now+1000)
	tenantDB.ExecContext(ctx, `
		UPDATE budget_transactions_current SET version = 2, source_amount_minor = 30000, destination_amount_minor = 30000, notes = 'Weekly Groceries (Updated)' WHERE id = ?`, txID)
	tenantDB.ExecContext(ctx, `UPDATE account_currency_balances SET balance_minor = 70000 WHERE account_id = ?`, userAcctID)
	tenantDB.ExecContext(ctx, `UPDATE bucket_currency_balances SET balance_minor = 20000 WHERE bucket_id = ?`, bucketID)

	// Rollback / Restore to version 1
	restoreRes, err := RestoreEntityVersion(ctx, tenantDB, "budget_transactions", txID, 1, agentID, corrID, now+2000)
	if err != nil {
		t.Fatalf("RestoreEntityVersion failed: %v", err)
	}
	if restoreRes.NewVersion != 3 {
		t.Errorf("expected new version 3, got %d", restoreRes.NewVersion)
	}

	// Verify budget_transactions_current amount is back to 10000 minor
	var curAmt int64
	tenantDB.QueryRowContext(ctx, `SELECT source_amount_minor FROM budget_transactions_current WHERE id = ?`, txID).Scan(&curAmt)
	if curAmt != 10000 {
		t.Errorf("expected restored amount 10000, got %d", curAmt)
	}

	// Verify account balance is restored to 900.00 PLN (90000 minor)
	var acctBal int64
	tenantDB.QueryRowContext(ctx, `SELECT balance_minor FROM account_currency_balances WHERE account_id = ?`, userAcctID).Scan(&acctBal)
	if acctBal != 90000 {
		t.Errorf("expected restored account balance 90000 minor, got %d", acctBal)
	}

	// Verify bucket balance is restored to 400.00 PLN (40000 minor)
	var bucketBal int64
	tenantDB.QueryRowContext(ctx, `SELECT balance_minor FROM bucket_currency_balances WHERE bucket_id = ?`, bucketID).Scan(&bucketBal)
	if bucketBal != 40000 {
		t.Errorf("expected restored bucket balance 40000 minor, got %d", bucketBal)
	}
}

func TestAccountHistoryAndRestore(t *testing.T) {
	ctx := context.Background()
	tenantDB, cleanup := setupTestTenantDB(t)
	defer cleanup()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UnixMicro()

	partyID := domain.NewUUIDv7()
	tenantDB.ExecContext(ctx, `INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Me', 'INDIVIDUAL', 1, ?, ?)`, partyID, agentID, now)

	acctID := domain.NewUUIDv7()

	// Version 1
	tenantDB.ExecContext(ctx, `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, 'Main Checking', 'CHECKING', 'PLN', 'PL123', 'EMOJI', '💳', 0, 'CREATE', ?, ?, ?)`,
		acctID, partyID, agentID, corrID, now)
	tenantDB.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Main Checking', 'CHECKING', 'PLN', 'PL123', 'EMOJI', '💳', 0, ?, ?)`,
		acctID, partyID, agentID, now)

	// Version 2: Closed
	tenantDB.ExecContext(ctx, `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, 2, ?, 'Main Checking (Closed)', 'CHECKING', 'PLN', 'PL123', 'EMOJI', '💳', 1, 'UPDATE', ?, ?, ?)`,
		acctID, partyID, agentID, corrID, now+1000)
	tenantDB.ExecContext(ctx, `
		UPDATE accounts_current SET version = 2, name = 'Main Checking (Closed)', is_closed = 1, updated_at = ? WHERE id = ?`, now+1000, acctID)

	// Version 3: Soft Delete
	tenantDB.ExecContext(ctx, `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, 3, ?, 'Main Checking (Closed)', 'CHECKING', 'PLN', 'PL123', 'EMOJI', '💳', 1, 'SOFT_DELETE', ?, ?, ?)`,
		acctID, partyID, agentID, corrID, now+2000)
	tenantDB.ExecContext(ctx, `DELETE FROM accounts_current WHERE id = ?`, acctID)

	// Verify history shows 3 items
	hist, err := GetEntityHistory(ctx, tenantDB, "accounts", acctID)
	if err != nil {
		t.Fatalf("GetEntityHistory failed: %v", err)
	}
	if len(hist.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(hist.Items))
	}
	if !hist.IsDeleted {
		t.Errorf("expected IsDeleted to be true")
	}

	// Restoring soft-delete version (v3) should fail with ErrSoftDelete
	_, err = RestoreEntityVersion(ctx, tenantDB, "accounts", acctID, 3, agentID, corrID, now+3000)
	if err != ErrSoftDelete {
		t.Errorf("expected ErrSoftDelete, got %v", err)
	}

	// Restoring v1 should resurrect the account in accounts_current as v4
	res, err := RestoreEntityVersion(ctx, tenantDB, "accounts", acctID, 1, agentID, corrID, now+4000)
	if err != nil {
		t.Fatalf("resurrection failed: %v", err)
	}
	if res.NewVersion != 4 {
		t.Errorf("expected new version 4, got %d", res.NewVersion)
	}

	var curVer, isClosed int
	var curName string
	err = tenantDB.QueryRowContext(ctx, `SELECT version, name, is_closed FROM accounts_current WHERE id = ?`, acctID).Scan(&curVer, &curName, &isClosed)
	if err != nil {
		t.Fatalf("account was not resurrected in accounts_current: %v", err)
	}
	if curVer != 4 || curName != "Main Checking" || isClosed != 0 {
		t.Errorf("expected restored v4 'Main Checking' open (0), got v%d %q closed=%d", curVer, curName, isClosed)
	}
}
