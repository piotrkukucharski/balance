package transaction

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
)

func TestExecuteResolutionMerge(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_tx_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "tenant.db")
	tenantDB, err := db.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantDB.Close()

	if err := db.ApplyMigrations(tenantDB, "migrations/tenant"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().UnixMicro()
	agentID := domain.NewUUIDv7()
	correlationID := domain.NewUUIDv7()

	// Setup Parties: User party (is_user_owned = 1) and Restaurant party (is_user_owned = 0)
	userPartyID := domain.NewUUIDv7()
	restPartyID := domain.NewUUIDv7()

	_, err = tenantDB.Exec(`
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Jan Kowalski', 'INDIVIDUAL', 1, ?, ?),
		       (?, 1, 'Restaurant', 'ORGANIZATION', 0, ?, ?)`,
		userPartyID, agentID, now, restPartyID, agentID, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Setup Accounts: User Checking and Restaurant External
	userAcctID := domain.NewUUIDv7()
	restAcctID := domain.NewUUIDv7()

	_, err = tenantDB.Exec(`
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, ?, ?),
		       (?, 1, ?, 'Restaurant POS', 'EXTERNAL', 'PLN', 0, ?, ?)`,
		userAcctID, userPartyID, agentID, now, restAcctID, restPartyID, agentID, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Setup Bucket
	bucketID := domain.NewUUIDv7()
	_, err = tenantDB.Exec(`
		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, 'Living', 'Dining Out', 0, 1, ?)`,
		bucketID, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Insert 3 raw transactions (dinner with friends: -300 PLN, +100 PLN, +100 PLN)
	raw1 := domain.NewUUIDv7()
	raw2 := domain.NewUUIDv7()
	raw3 := domain.NewUUIDv7()

	insertRaw := func(id string, amount int64, payee string) {
		h := ComputeCanonicalDedupHash(userAcctID, id, now, amount, "PLN", payee, "ref")
		_, err := tenantDB.Exec(`
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, status, updated_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, 'PLN', ?, 'PENDING', ?)`,
			id, userAcctID, id, h, now, amount, payee, now,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	insertRaw(raw1, -30000, "RESTAURANT")
	insertRaw(raw2, 10000, "FRIEND A")
	insertRaw(raw3, 10000, "FRIEND B")

	// Resolve: Merge raw 1, 2, 3 into 1 BudgetTransaction of 100.00 PLN (10000 minor)
	opID, err := ExecuteResolution(ctx, tenantDB, ResolveResolutionInput{
		OperationType:     OpMerge,
		RawTransactionIDs: []string{raw1, raw2, raw3},
		BudgetTransactions: []BudgetTxInput{
			{
				SourceAccountID:      userAcctID,
				DestinationAccountID: restAcctID,
				SourceAmountMinor:    10000, // net 100 PLN
				SourceCurrency:       "PLN",
				DestAmountMinor:      10000,
				DestCurrency:         "PLN",
				BucketID:             &bucketID,
				TransactionDate:      now,
			},
		},
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("ExecuteResolution failed: %v", err)
	}

	if !domain.IsValidUUIDv7(opID) {
		t.Fatalf("expected valid operation UUIDv7, got %s", opID)
	}

	// Verify raw transactions transitioned to PROCESSED
	var countProcessed int
	err = tenantDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE status = 'PROCESSED'`).Scan(&countProcessed)
	if err != nil {
		t.Fatal(err)
	}
	if countProcessed != 3 {
		t.Fatalf("expected 3 PROCESSED raw transactions, got %d", countProcessed)
	}

	// Verify transaction links
	var linksCount int
	err = tenantDB.QueryRow(`SELECT count(*) FROM transaction_links WHERE operation_id = ?`, opID).Scan(&linksCount)
	if err != nil {
		t.Fatal(err)
	}
	if linksCount != 3 {
		t.Fatalf("expected 3 transaction_links rows, got %d", linksCount)
	}

	// Verify account balances updated
	var userBal int64
	err = tenantDB.QueryRow(`SELECT balance_minor FROM account_currency_balances WHERE account_id = ? AND currency = 'PLN'`, userAcctID).Scan(&userBal)
	if err != nil {
		t.Fatal(err)
	}
	if userBal != -10000 {
		t.Fatalf("expected user balance -10000, got %d", userBal)
	}

	var bucketBal int64
	err = tenantDB.QueryRow(`SELECT balance_minor FROM bucket_currency_balances WHERE bucket_id = ? AND currency = 'PLN'`, bucketID).Scan(&bucketBal)
	if err != nil {
		t.Fatal(err)
	}
	if bucketBal != -10000 {
		t.Fatalf("expected bucket balance -10000, got %d", bucketBal)
	}
}

func TestTransactionTargetFundedLifecycle(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_tx_target_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "tenant.db")
	tenantDB, err := db.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantDB.Close()

	if err := db.ApplyMigrations(tenantDB, "migrations/tenant"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC().UnixMicro()
	agentID := domain.NewUUIDv7()
	correlationID := domain.NewUUIDv7()

	userPartyID := domain.NewUUIDv7()
	restPartyID := domain.NewUUIDv7()
	_, _ = tenantDB.Exec(`
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Me', 'INDIVIDUAL', 1, ?, ?),
		       (?, 1, 'Scooter Co', 'ORGANIZATION', 0, ?, ?)`,
		userPartyID, agentID, now, restPartyID, agentID, now,
	)

	userAcctID := domain.NewUUIDv7()
	restAcctID := domain.NewUUIDv7()
	_, _ = tenantDB.Exec(`
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, ?, ?),
		       (?, 1, ?, 'Scooter Vendor', 'EXTERNAL', 'PLN', 0, ?, ?)`,
		userAcctID, userPartyID, agentID, now, restAcctID, restPartyID, agentID, now,
	)

	bucketID := domain.NewUUIDv7()
	_, _ = tenantDB.Exec(`
		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, 'Living', 'Transport', 0, 1, ?)`,
		bucketID, now,
	)
	_, _ = tenantDB.Exec(`
		INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor)
		VALUES (?, 'PLN', 10000)`, bucketID,
	)

	targetID := domain.NewUUIDv7()
	_, _ = tenantDB.Exec(`
		INSERT INTO targets_projection (
			id, bucket_id, name, target_type, priority, sort_order, amount_minor, currency,
			recurrence_cadence, non_spending_strategy, current_funded_minor, is_archived, version, updated_at
		) VALUES (?, ?, 'Hulajnogi', 'EXPENSE', 1, 0, 10000, 'PLN', 'MONTHLY', 'ROLL_OVER', 10000, 0, 1, ?)`,
		targetID, bucketID, now,
	)

	notes := "Dott scooter ride"
	opID, err := ExecuteResolution(ctx, tenantDB, ResolveResolutionInput{
		OperationType: OpDirectMove,
		BudgetTransactions: []BudgetTxInput{
			{
				SourceAccountID:      userAcctID,
				DestinationAccountID: restAcctID,
				SourceAmountMinor:    2500, // 25.00 PLN
				SourceCurrency:       "PLN",
				DestAmountMinor:      2500,
				DestCurrency:         "PLN",
				BucketID:             &bucketID,
				TargetID:             &targetID,
				Notes:                &notes,
			},
		},
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("execute resolution failed: %v", err)
	}
	_ = opID

	var funded int64
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, targetID).Scan(&funded)
	if funded != 7500 {
		t.Fatalf("expected target funded 7500 (100 - 25), got %d", funded)
	}

	// Fetch created budget transaction ID
	var createdTxID string
	_ = tenantDB.QueryRow(`SELECT id FROM budget_transactions_current WHERE target_id = ?`, targetID).Scan(&createdTxID)

	// Update transaction to 10.00 PLN
	updateNotes := "Dott ride (corrected)"
	err = UpdateBudgetTransaction(ctx, tenantDB, UpdateBudgetTxInput{
		ID:                   createdTxID,
		SourceAccountID:      userAcctID,
		DestinationAccountID: restAcctID,
		SourceAmountMinor:    1000, // 10.00 PLN
		SourceCurrency:       "PLN",
		DestAmountMinor:      1000,
		DestCurrency:         "PLN",
		BucketID:             &bucketID,
		TargetID:             &targetID,
		Notes:                &updateNotes,
		AgentID:              agentID,
		CorrelationID:        correlationID,
	})
	if err != nil {
		t.Fatalf("update budget tx failed: %v", err)
	}

	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, targetID).Scan(&funded)
	if funded != 9000 {
		t.Fatalf("expected target funded 9000 (100 - 10), got %d", funded)
	}

	// Delete transaction
	err = DeleteBudgetTransaction(ctx, tenantDB, createdTxID, agentID, correlationID)
	if err != nil {
		t.Fatalf("delete budget tx failed: %v", err)
	}

	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, targetID).Scan(&funded)
	if funded != 10000 {
		t.Fatalf("expected target funded restored to 10000 after deletion, got %d", funded)
	}
}

func TestDeleteRawTransactions(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_tx_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "tenant.db")
	tenantDB, err := db.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantDB.Close()

	if err := db.ApplyMigrations(tenantDB, "migrations/tenant"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	userAcctID := "01900000-0000-7000-8000-000000000010"
	rawID := "01900000-0000-7000-8000-000000000099"

	_, err = tenantDB.Exec(`
		INSERT INTO raw_transactions_current (id, version, account_id, dedup_hash, booking_date, raw_payee, amount_minor, currency, status, booking_status, updated_at)
		VALUES (?, 1, ?, 'hash99', 1700000000, 'Test Payee', -5000, 'PLN', 'PENDING', 'BOOKED', 1000)`, rawID, userAcctID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tenantDB.Exec(`
		INSERT INTO raw_transactions (id, version, account_id, dedup_hash, booking_date, raw_payee, amount_minor, currency, status, booking_status, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, 'hash99', 1700000000, 'Test Payee', -5000, 'PLN', 'PENDING', 'BOOKED', 'CREATE', 'agent1', 'corr1', 1000)`, rawID, userAcctID)
	if err != nil {
		t.Fatal(err)
	}

	// Verify exists
	var count int
	if err := tenantDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE id = ?`, rawID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected 1 raw transaction before delete, got %d", count)
	}

	// Hard delete
	if err := DeleteRawTransaction(ctx, tenantDB, rawID); err != nil {
		t.Fatalf("DeleteRawTransaction failed: %v", err)
	}

	// Verify completely gone
	if err := tenantDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE id = ?`, rawID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expected 0 in raw_transactions_current, got %d", count)
	}
	if err := tenantDB.QueryRow(`SELECT count(*) FROM raw_transactions WHERE id = ?`, rawID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expected 0 in raw_transactions history, got %d", count)
	}
}

