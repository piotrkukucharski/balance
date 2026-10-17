package budget

import (
	"context"
	"database/sql"
	"testing"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestTenantDB(t *testing.T) *sql.DB {
	t.Helper()
	sqliteDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed opening in-memory db: %v", err)
	}

	if err := db.ApplyPragmas(sqliteDB); err != nil {
		t.Fatalf("failed applying pragmas: %v", err)
	}

	// Apply tenant migration
	migrationContent, err := db.MigrationsFS.ReadFile("migrations/tenant/00001_init_tenant.sql")
	if err != nil {
		t.Fatalf("failed reading tenant migration: %v", err)
	}

	if _, err := sqliteDB.Exec(string(migrationContent)); err != nil {
		t.Fatalf("failed executing migration: %v", err)
	}

	return sqliteDB
}

func TestBucketAndTargetLifecycle(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	correlationID := domain.NewUUIDv7()

	// 1. Create Bucket
	bucketID, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Needs",
		Name:          "Living Expenses",
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating bucket: %v", err)
	}

	// Verify projection & event
	var bName, bGroup string
	var isArchived int
	err = tenantDB.QueryRow(`SELECT name, group_name, is_archived FROM buckets_projection WHERE id = ?`, bucketID).Scan(&bName, &bGroup, &isArchived)
	if err != nil || bName != "Living Expenses" || bGroup != "Needs" || isArchived != 0 {
		t.Fatalf("unexpected bucket projection: %v, %s, %s, %d", err, bName, bGroup, isArchived)
	}

	var eventCount int
	tenantDB.QueryRow(`SELECT count(*) FROM bucket_events WHERE aggregate_id = ?`, bucketID).Scan(&eventCount)
	if eventCount != 1 {
		t.Fatalf("expected 1 bucket event, got %d", eventCount)
	}

	// 2. Create Target
	targetID, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:          bucketID,
		Name:              "Rent",
		TargetType:        "EXPENSE",
		Priority:          3,
		AmountMinor:       200000,
		Currency:          "PLN",
		RecurrenceCadence: CadenceMonthly,
		AgentID:           agentID,
		CorrelationID:     correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating target: %v", err)
	}

	var tName string
	var tPriority int
	var tAmount int64
	err = tenantDB.QueryRow(`SELECT name, priority, amount_minor FROM targets_projection WHERE id = ?`, targetID).Scan(&tName, &tPriority, &tAmount)
	if err != nil || tName != "Rent" || tPriority != 3 || tAmount != 200000 {
		t.Fatalf("unexpected target projection: %v, %s, %d, %d", err, tName, tPriority, tAmount)
	}

	// 3. Attempt to archive bucket with active target -> MUST FAIL
	err = ArchiveBucket(ctx, tenantDB, bucketID, agentID, correlationID)
	if err == nil {
		t.Fatal("expected error archiving bucket with active targets, got nil")
	}

	// 4. Update Target
	err = UpdateTarget(ctx, tenantDB, UpdateTargetInput{
		ID:                  targetID,
		Name:                "Rent Updated",
		TargetType:          "EXPENSE",
		Priority:            3,
		AmountMinor:         220000,
		RecurrenceCadence:   CadenceMonthly,
		NonSpendingStrategy: StrategyRollOver,
		AgentID:             agentID,
		CorrelationID:       correlationID,
	})
	if err != nil {
		t.Fatalf("failed updating target: %v", err)
	}

	// 5. Delete Target
	err = DeleteTarget(ctx, tenantDB, targetID, agentID, correlationID)
	if err != nil {
		t.Fatalf("failed deleting target: %v", err)
	}

	// 6. Archive Bucket -> MUST SUCCEED now
	err = ArchiveBucket(ctx, tenantDB, bucketID, agentID, correlationID)
	if err != nil {
		t.Fatalf("expected archiving bucket to succeed, got %v", err)
	}

	err = tenantDB.QueryRow(`SELECT is_archived FROM buckets_projection WHERE id = ?`, bucketID).Scan(&isArchived)
	if err != nil || isArchived != 1 {
		t.Fatalf("expected bucket to be archived: %v, isArchived=%d", err, isArchived)
	}
}

func TestReadyToAssignAndFunding(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	correlationID := domain.NewUUIDv7()

	// Create user-owned party and account with cash
	partyID := domain.NewUUIDv7()
	if _, err := tenantDB.Exec(`
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Owner', 'INDIVIDUAL', 1, ?, 1000)`, partyID, agentID); err != nil {
		t.Fatalf("failed inserting party: %v", err)
	}

	acctID := domain.NewUUIDv7()
	if _, err := tenantDB.Exec(`
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Checking', 'CHECKING', 'PLN', 0, ?, 1000)`, acctID, partyID, agentID); err != nil {
		t.Fatalf("failed inserting account: %v", err)
	}

	if _, err := tenantDB.Exec(`
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, 'PLN', 500000, 1000)`, acctID); err != nil {
		t.Fatalf("failed inserting balance: %v", err)
	}

	// Initial Ready to Assign should be 5,000 PLN
	rta, err := CalculateReadyToAssign(ctx, tenantDB)
	if err != nil || rta["PLN"] != 500000 {
		t.Fatalf("expected 500000, got %v, err=%v", rta["PLN"], err)
	}

	// Create bucket
	bucketID, _ := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Needs",
		Name:          "Groceries",
		AgentID:       agentID,
		CorrelationID: correlationID,
	})

	// Assign 1,200 PLN
	err = AssignFunds(ctx, tenantDB, AssignFundsInput{
		BucketID:      bucketID,
		Currency:      "PLN",
		AmountMinor:   120000,
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed assigning funds: %v", err)
	}

	// Ready to assign should now be 3,800 PLN
	rta, err = CalculateReadyToAssign(ctx, tenantDB)
	if err != nil || rta["PLN"] != 380000 {
		t.Fatalf("expected 380000, got %v, err=%v", rta["PLN"], err)
	}

	// Archive bucket with balance -> MUST FAIL
	err = ArchiveBucket(ctx, tenantDB, bucketID, agentID, correlationID)
	if err == nil {
		t.Fatal("expected archiving bucket with non-zero balance to fail")
	}

	// Return 1,200 PLN back to ready to assign
	err = AssignFunds(ctx, tenantDB, AssignFundsInput{
		BucketID:      bucketID,
		Currency:      "PLN",
		AmountMinor:   -120000,
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed returning funds: %v", err)
	}

	// Archive should now succeed
	err = ArchiveBucket(ctx, tenantDB, bucketID, agentID, correlationID)
	if err != nil {
		t.Fatalf("archiving bucket failed: %v", err)
	}
}
