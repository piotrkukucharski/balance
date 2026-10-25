package automation

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/transaction"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestTenantDB(t *testing.T) (*sql.DB, func()) {
	tmpDir, err := os.MkdirTemp("", "automation_test_*")
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

func TestAutomationCRUDAndSquash(t *testing.T) {
	tenantDB, cleanup := setupTestTenantDB(t)
	defer cleanup()
	ctx := context.Background()

	// 1. Create automation version 1
	created, err := CreateAutomation(ctx, tenantDB, CreateAutomationInput{
		Name:           "Auto Uber Transport",
		JudgeScript:    `text.contains(tx.notes, "Uber")`,
		ExecutorScript: `tx.set_bucket("Transport")`,
		Comment:        "Initial version",
		AgentID:        "user-1",
		CorrelationID:  "corr-1",
	})
	if err != nil {
		t.Fatalf("CreateAutomation failed: %v", err)
	}
	if created.Version != 1 || created.SortOrder != 1 || !created.IsActive {
		t.Fatalf("unexpected created automation: %+v", created)
	}

	// 2. Read
	got, err := GetAutomation(ctx, tenantDB, created.ID)
	if err != nil {
		t.Fatalf("GetAutomation failed: %v", err)
	}
	if got.Name != "Auto Uber Transport" {
		t.Fatalf("mismatch name: %s", got.Name)
	}

	// 3. Update to version 2 (normal update without squash)
	updated2, err := UpdateAutomation(ctx, tenantDB, UpdateAutomationInput{
		ID:             created.ID,
		Name:           "Auto Uber Transport v2",
		JudgeScript:    `text.contains(tx.notes, "Uber") or tx.party == "Uber"`,
		ExecutorScript: `tx.set_bucket("Transport")`,
		Comment:        "Add party check",
		Squash:         false,
		AgentID:        "user-1",
		CorrelationID:  "corr-2",
	})
	if err != nil {
		t.Fatalf("UpdateAutomation v2 failed: %v", err)
	}
	if updated2.Version != 2 {
		t.Fatalf("expected version 2, got %d", updated2.Version)
	}

	// Verify append-only has 2 versions
	var count int
	tenantDB.QueryRow("SELECT count(*) FROM automations WHERE id = ?", created.ID).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 versions in automations table, got %d", count)
	}

	// 4. Update with Squash (replaces version 1, keeping total versions at 1, version number 2)
	updatedSquash, err := UpdateAutomation(ctx, tenantDB, UpdateAutomationInput{
		ID:             created.ID,
		Name:           "Auto Uber Transport Squashed",
		JudgeScript:    `text.contains(tx.notes, "Uber")`,
		ExecutorScript: `tx.set_bucket("Transport")`,
		Comment:        "Squashed changes",
		Squash:         true,
		AgentID:        "user-1",
		CorrelationID:  "corr-3",
	})
	if err != nil {
		t.Fatalf("UpdateAutomation squash failed: %v", err)
	}
	if updatedSquash.Version != 2 {
		t.Fatalf("expected version 2 after squash, got %d", updatedSquash.Version)
	}

	// Verify predecessor was removed, leaving only version 2
	tenantDB.QueryRow("SELECT count(*) FROM automations WHERE id = ?", created.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 version after squash, got %d", count)
	}

	// 5. Toggle Active
	active, err := ToggleAutomation(ctx, tenantDB, created.ID)
	if err != nil {
		t.Fatalf("ToggleAutomation failed: %v", err)
	}
	if active {
		t.Fatalf("expected inactive after toggle")
	}

	// 6. Delete
	err = DeleteAutomation(ctx, tenantDB, created.ID, "user-1", "corr-4")
	if err != nil {
		t.Fatalf("DeleteAutomation failed: %v", err)
	}
	_, err = GetAutomation(ctx, tenantDB, created.ID)
	if err != ErrAutomationNotFound {
		t.Fatalf("expected ErrAutomationNotFound, got %v", err)
	}
}

func TestBatchRunAndDryRun(t *testing.T) {
	tenantDB, cleanup := setupTestTenantDB(t)
	defer cleanup()
	ctx := context.Background()

	// Seed bucket
	bucketID := domain.NewUUIDv7()
	tenantDB.Exec(`INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, 'Default', 'Transport', 0, 1, 1000)`, bucketID)

	// Seed user account and counterparty account
	userAcctID := domain.NewUUIDv7()
	partyUserID := domain.NewUUIDv7()
	if _, err := tenantDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Me', 'INDIVIDUAL', 1, 'user', 1000)`, partyUserID); err != nil {
		t.Fatalf("insert user party: %v", err)
	}
	if _, err := tenantDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Checking', 'ASSET', 'PLN', 0, 'user', 1000)`, userAcctID, partyUserID); err != nil {
		t.Fatalf("insert user account: %v", err)
	}

	uberAcctID := domain.NewUUIDv7()
	uberPartyID := domain.NewUUIDv7()
	if _, err := tenantDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Uber', 'ORGANIZATION', 0, 'user', 1000)`, uberPartyID); err != nil {
		t.Fatalf("insert uber party: %v", err)
	}
	if _, err := tenantDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Uber Account', 'EXPENSE', 'PLN', 0, 'user', 1000)`, uberAcctID, uberPartyID); err != nil {
		t.Fatalf("insert uber account: %v", err)
	}

	// Seed transaction
	txID := domain.NewUUIDv7()
	if _, err := tenantDB.Exec(`INSERT INTO budget_transactions (
		id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
		destination_amount_minor, destination_currency, transaction_date, notes, action, agent_id, correlation_id, created_at
	) VALUES (?, 1, ?, ?, 3500, 'PLN', 3500, 'PLN', 1000, 'Uber trip to airport', 'CREATE', 'user', 'corr', 1000)`,
		txID, userAcctID, uberAcctID); err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	if _, err := tenantDB.Exec(`INSERT INTO budget_transactions_current (
		id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
		destination_amount_minor, destination_currency, transaction_date, notes, updated_at
	) VALUES (?, 1, ?, ?, 3500, 'PLN', 3500, 'PLN', 1000, 'Uber trip to airport', 1000)`,
		txID, userAcctID, uberAcctID); err != nil {
		t.Fatalf("insert tx current: %v", err)
	}

	// Create automation
	_, err := CreateAutomation(ctx, tenantDB, CreateAutomationInput{
		Name:           "Categorize Transport",
		JudgeScript:    `text.contains(tx.notes, "Uber")`,
		ExecutorScript: `tx.set_bucket("Transport")`,
		AgentID:        "user-1",
		CorrelationID:  "corr-1",
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}

	// 1. Dry run
	matched, outcome, _, err := DryRunAutomation(ctx, tenantDB, `text.contains(tx.notes, "Uber")`, `tx.set_bucket("Transport")`, txID)
	if err != nil {
		t.Fatalf("dry run error: %v", err)
	}
	if !matched {
		t.Fatalf("expected dry run matched")
	}
	if outcome.UpdatedTx.BucketID == nil || *outcome.UpdatedTx.BucketID != bucketID {
		actualVal := "<nil>"
		if outcome.UpdatedTx.BucketID != nil {
			actualVal = *outcome.UpdatedTx.BucketID
		}
		t.Fatalf("expected bucket %s, got %s", bucketID, actualVal)
	}

	// 2. Batch run (non-dryRun)
	batchRes, err := RunAutomationsBatch(ctx, tenantDB, []string{txID}, false, "user-1", "corr-batch")
	if err != nil {
		t.Fatalf("batch run failed: %v", err)
	}
	if batchRes.ModifiedCount != 1 {
		t.Fatalf("expected 1 modified, got %d", batchRes.ModifiedCount)
	}

	// Verify transaction updated in DB
	var dbBucketID sql.NullString
	tenantDB.QueryRow("SELECT bucket_id FROM budget_transactions_current WHERE id = ?", txID).Scan(&dbBucketID)
	if !dbBucketID.Valid || dbBucketID.String != bucketID {
		t.Fatalf("expected db bucket %s, got %v", bucketID, dbBucketID)
	}
}

func TestInTxPipelineOnExecuteResolution(t *testing.T) {
	tenantDB, cleanup := setupTestTenantDB(t)
	defer cleanup()
	ctx := context.Background()

	// Seed bucket
	bucketID := domain.NewUUIDv7()
	if _, err := tenantDB.Exec(`INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, 'Default', 'Transport', 0, 1, 1000)`, bucketID); err != nil {
		t.Fatalf("insert bucket: %v", err)
	}

	// Seed user account and counterparty account
	userAcctID := domain.NewUUIDv7()
	partyUserID := domain.NewUUIDv7()
	tenantDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Me', 'INDIVIDUAL', 1, 'user', 1000)`, partyUserID)
	tenantDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Checking', 'ASSET', 'PLN', 0, 'user', 1000)`, userAcctID, partyUserID)

	uberAcctID := domain.NewUUIDv7()
	uberPartyID := domain.NewUUIDv7()
	tenantDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Uber', 'ORGANIZATION', 0, 'user', 1000)`, uberPartyID)
	tenantDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Uber Account', 'EXPENSE', 'PLN', 0, 'user', 1000)`, uberAcctID, uberPartyID)

	// Create active automation
	_, err := CreateAutomation(ctx, tenantDB, CreateAutomationInput{
		Name:           "Auto Categorize Uber",
		JudgeScript:    `text.contains(tx.notes, "Uber")`,
		ExecutorScript: `tx.set_bucket("Transport")`,
		AgentID:        "user-1",
		CorrelationID:  "corr-1",
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}

	// Now create a budget transaction via transaction.ExecuteResolution
	note := "Uber ride home"
	opID, err := transaction.ExecuteResolution(ctx, tenantDB, transaction.ResolveResolutionInput{
		OperationType: transaction.OpDirectMove,
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      userAcctID,
				DestinationAccountID: uberAcctID,
				SourceAmountMinor:    4200,
				SourceCurrency:       "PLN",
				DestAmountMinor:      4200,
				DestCurrency:         "PLN",
				Notes:                &note,
			},
		},
		AgentID:       "user-1",
		CorrelationID: "corr-res",
	})
	if err != nil {
		t.Fatalf("ExecuteResolution failed: %v", err)
	}
	if opID == "" {
		t.Fatalf("expected valid opID")
	}

	// Verify the created transaction was automatically categorized to 'Transport' bucket
	var assignedBucketID sql.NullString
	err = tenantDB.QueryRow("SELECT bucket_id FROM budget_transactions_current WHERE notes = ?", note).Scan(&assignedBucketID)
	if err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if !assignedBucketID.Valid || assignedBucketID.String != bucketID {
		t.Fatalf("expected auto-assigned bucket %s, got %v", bucketID, assignedBucketID)
	}

	// Now test fail-closed: create an automation with a runtime error in judge
	_, err = CreateAutomation(ctx, tenantDB, CreateAutomationInput{
		Name:           "Faulty Automation",
		JudgeScript:    `if tx.notes == "FailMe" then error("boom") else return false end`,
		ExecutorScript: `tx.set_notes("boom")`,
		AgentID:        "user-1",
		CorrelationID:  "corr-fail",
	})
	if err != nil {
		t.Fatalf("create faulty automation: %v", err)
	}

	failNote := "FailMe"
	_, err = transaction.ExecuteResolution(ctx, tenantDB, transaction.ResolveResolutionInput{
		OperationType: transaction.OpDirectMove,
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      userAcctID,
				DestinationAccountID: uberAcctID,
				SourceAmountMinor:    1000,
				SourceCurrency:       "PLN",
				DestAmountMinor:      1000,
				DestCurrency:         "PLN",
				Notes:                &failNote,
			},
		},
		AgentID:       "user-1",
		CorrelationID: "corr-res-fail",
	})
	if err == nil {
		t.Fatalf("expected ExecuteResolution to fail closed on automation error")
	}

	// Verify the transaction was NOT created in DB (rollback)
	var count int
	tenantDB.QueryRow("SELECT count(*) FROM budget_transactions_current WHERE notes = ?", failNote).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 transactions created due to rollback, got %d", count)
	}
}
