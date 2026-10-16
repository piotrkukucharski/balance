package transaction

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
)

func TestExecuteResolutionMerge(t *testing.T) {
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
