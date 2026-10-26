package budget

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

	// 5. Create Target with empty name (optional name)
	optTargetID, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:          bucketID,
		Name:              "",
		TargetType:        "EXPENSE",
		Priority:          1,
		AmountMinor:       50000,
		Currency:          "PLN",
		RecurrenceCadence: CadenceMonthly,
		AgentID:           agentID,
		CorrelationID:     correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating target with empty name: %v", err)
	}

	var optNameNull sql.NullString
	var optPriority int
	err = tenantDB.QueryRow(`SELECT name, priority FROM targets_projection WHERE id = ?`, optTargetID).Scan(&optNameNull, &optPriority)
	if err != nil {
		t.Fatalf("failed querying target projection: %v", err)
	}
	if optNameNull.Valid && optNameNull.String != "" {
		t.Fatalf("expected empty/null name, got %s", optNameNull.String)
	}
	if optPriority != 1 {
		t.Fatalf("expected priority 1, got %d", optPriority)
	}

	// 6. Update Target with empty name
	err = UpdateTarget(ctx, tenantDB, UpdateTargetInput{
		ID:                  targetID,
		Name:                "",
		TargetType:          "EXPENSE",
		Priority:            2,
		AmountMinor:         150000,
		RecurrenceCadence:   CadenceMonthly,
		NonSpendingStrategy: StrategyRollOver,
		AgentID:             agentID,
		CorrelationID:       correlationID,
	})
	if err != nil {
		t.Fatalf("failed updating target to empty name: %v", err)
	}

	err = tenantDB.QueryRow(`SELECT name, priority FROM targets_projection WHERE id = ?`, targetID).Scan(&optNameNull, &optPriority)
	if err != nil {
		t.Fatalf("failed querying target projection after update: %v", err)
	}
	if optNameNull.Valid && optNameNull.String != "" {
		t.Fatalf("expected empty/null name after update, got %s", optNameNull.String)
	}
	if optPriority != 2 {
		t.Fatalf("expected priority 2, got %d", optPriority)
	}

	// 5. Delete Targets
	err = DeleteTarget(ctx, tenantDB, targetID, agentID, correlationID)
	if err != nil {
		t.Fatalf("failed deleting target: %v", err)
	}
	err = DeleteTarget(ctx, tenantDB, optTargetID, agentID, correlationID)
	if err != nil {
		t.Fatalf("failed deleting optional target: %v", err)
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

func TestAssignFundsReverseWaterfallAndMoveFunds(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	correlationID := domain.NewUUIDv7()

	b1, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Needs",
		Name:          "Insurance & Bills",
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating bucket 1: %v", err)
	}

	tMandatory, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:          b1,
		Name:              "Home Insurance",
		Priority:          3,
		AmountMinor:       60000, // 600.00 PLN
		Currency:          "PLN",
		RecurrenceCadence: CadenceAnnual,
		AgentID:           agentID,
		CorrelationID:     correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating mandatory target: %v", err)
	}

	tOptional, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:          b1,
		Name:              "Gadget Insurance",
		Priority:          1,
		AmountMinor:       20000, // 200.00 PLN
		Currency:          "PLN",
		RecurrenceCadence: CadenceMonthly,
		AgentID:           agentID,
		CorrelationID:     correlationID,
	})
	if err != nil {
		t.Fatalf("failed creating optional target: %v", err)
	}

	// 1. Assign 800 PLN to bucket
	err = AssignFunds(ctx, tenantDB, AssignFundsInput{
		BucketID:      b1,
		Currency:      "PLN",
		AmountMinor:   80000,
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed assigning 800 PLN: %v", err)
	}

	var mFunded, oFunded int64
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tMandatory).Scan(&mFunded)
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tOptional).Scan(&oFunded)
	if mFunded != 60000 || oFunded != 20000 {
		t.Fatalf("expected 60000 & 20000, got %d & %d", mFunded, oFunded)
	}

	// 2. Unassign 200 PLN (negative AssignFunds) -> lower priority target should lose funding first
	err = AssignFunds(ctx, tenantDB, AssignFundsInput{
		BucketID:      b1,
		Currency:      "PLN",
		AmountMinor:   -20000,
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed unassigning 200 PLN: %v", err)
	}

	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tMandatory).Scan(&mFunded)
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tOptional).Scan(&oFunded)
	if mFunded != 60000 || oFunded != 0 {
		t.Fatalf("expected mandatory=60000 & optional=0 after 200 PLN unassign, got mandatory=%d & optional=%d", mFunded, oFunded)
	}

	// 3. Unassign remaining 600 PLN -> mandatory target should become 0
	err = AssignFunds(ctx, tenantDB, AssignFundsInput{
		BucketID:      b1,
		Currency:      "PLN",
		AmountMinor:   -60000,
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("failed unassigning 600 PLN: %v", err)
	}

	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tMandatory).Scan(&mFunded)
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tOptional).Scan(&oFunded)
	if mFunded != 0 || oFunded != 0 {
		t.Fatalf("expected both 0 after full unassign, got mandatory=%d & optional=%d", mFunded, oFunded)
	}

	// 4. Test MoveFunds: assign 500 PLN to b1, then move to b2
	_ = AssignFunds(ctx, tenantDB, AssignFundsInput{
		BucketID:      b1,
		Currency:      "PLN",
		AmountMinor:   50000,
		AgentID:       agentID,
		CorrelationID: correlationID,
	})

	b2, _ := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Needs",
		Name:          "Emergency",
		AgentID:       agentID,
		CorrelationID: correlationID,
	})
	tDest, _ := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:          b2,
		Name:              "Buffer",
		Priority:          3,
		AmountMinor:       50000,
		Currency:          "PLN",
		RecurrenceCadence: CadenceMonthly,
		AgentID:           agentID,
		CorrelationID:     correlationID,
	})

	err = MoveFunds(ctx, tenantDB, MoveFundsInput{
		SourceBucketID: b1,
		DestBucketID:   b2,
		Currency:       "PLN",
		AmountMinor:    50000,
		AgentID:        agentID,
		CorrelationID:  correlationID,
	})
	if err != nil {
		t.Fatalf("failed moving funds: %v", err)
	}

	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tMandatory).Scan(&mFunded)
	var dFunded int64
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tDest).Scan(&dFunded)
	if mFunded != 0 || dFunded != 50000 {
		t.Fatalf("expected source target=0, dest target=50000, got src=%d dst=%d", mFunded, dFunded)
	}

	// 5. Test MoveTargetFunds within bucket b2 (Target to Target and Target to Buffer)
	tDest2, _ := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:          b2,
		Name:              "Secondary Buffer",
		Priority:          2,
		AmountMinor:       20000,
		Currency:          "PLN",
		RecurrenceCadence: CadenceMonthly,
		AgentID:           agentID,
		CorrelationID:     correlationID,
	})

	// Move 150.00 PLN from tDest to tDest2
	err = MoveTargetFunds(ctx, tenantDB, MoveTargetFundsInput{
		BucketID:       b2,
		SourceTargetID: &tDest,
		DestTargetID:   &tDest2,
		Currency:       "PLN",
		AmountMinor:    15000,
		AgentID:        agentID,
		CorrelationID:  correlationID,
	})
	if err != nil {
		t.Fatalf("failed moving target funds: %v", err)
	}

	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tDest).Scan(&dFunded)
	var d2Funded int64
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tDest2).Scan(&d2Funded)
	if dFunded != 35000 || d2Funded != 15000 {
		t.Fatalf("expected tDest=35000, tDest2=15000, got tDest=%d tDest2=%d", dFunded, d2Funded)
	}

	// Move 50.00 PLN from tDest2 to bucket buffer (nil DestTargetID)
	err = MoveTargetFunds(ctx, tenantDB, MoveTargetFundsInput{
		BucketID:       b2,
		SourceTargetID: &tDest2,
		DestTargetID:   nil,
		Currency:       "PLN",
		AmountMinor:    5000,
		AgentID:        agentID,
		CorrelationID:  correlationID,
	})
	if err != nil {
		t.Fatalf("failed moving target funds to buffer: %v", err)
	}
	_ = tenantDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE id = ?`, tDest2).Scan(&d2Funded)
	if d2Funded != 10000 {
		t.Fatalf("expected tDest2=10000 after moving to buffer, got %d", d2Funded)
	}
}

func TestGetAvailableCurrencies(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	// Initial check: empty DB, only default currency
	currencies, err := GetAvailableCurrencies(ctx, tenantDB, "PLN")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(currencies) != 1 || currencies[0] != "PLN" {
		t.Fatalf("expected [PLN], got %v", currencies)
	}

	// Add an account with primary_currency EUR
	userPartyID := domain.NewUUIDv7()
	acctID1 := domain.NewUUIDv7()
	acctID2 := domain.NewUUIDv7()
	agentID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Self', 'INDIVIDUAL', 1, ?, ?)`, userPartyID, agentID, now)
	if err != nil {
		t.Fatalf("failed inserting party: %v", err)
	}

	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'EUR Account', 'CHECKING', 'EUR', 0, ?, ?)`, acctID1, userPartyID, agentID, now)
	if err != nil {
		t.Fatalf("failed inserting account: %v", err)
	}

	// Add another account balance with USD
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Multi Account', 'CHECKING', 'PLN', 0, ?, ?)`, acctID2, userPartyID, agentID, now)
	if err != nil {
		t.Fatalf("failed inserting account: %v", err)
	}

	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, 'USD', 10000, ?)`, acctID2, now)
	if err != nil {
		t.Fatalf("failed inserting account_currency_balances: %v", err)
	}

	currencies, err = GetAvailableCurrencies(ctx, tenantDB, "PLN")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expected sorted: EUR, PLN, USD
	if len(currencies) != 3 || currencies[0] != "EUR" || currencies[1] != "PLN" || currencies[2] != "USD" {
		t.Fatalf("expected [EUR, PLN, USD], got %v", currencies)
	}
}
