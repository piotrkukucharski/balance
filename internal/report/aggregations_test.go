package report_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/report"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestDatabases(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	tmpDir := t.TempDir()

	mainPath := filepath.Join(tmpDir, "main.db")
	tenantPath := filepath.Join(tmpDir, "tenant.db")

	mainDB, err := db.OpenDB(mainPath)
	if err != nil {
		t.Fatalf("OpenDB main failed: %v", err)
	}
	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatalf("ApplyMigrations main failed: %v", err)
	}

	tenantDB, err := db.OpenDB(tenantPath)
	if err != nil {
		t.Fatalf("OpenDB tenant failed: %v", err)
	}
	if err := db.ApplyMigrations(tenantDB, "migrations/tenant"); err != nil {
		t.Fatalf("ApplyMigrations tenant failed: %v", err)
	}

	return mainDB, tenantDB
}

func seedTestData(t *testing.T, tenantDB *sql.DB) {
	t.Helper()
	now := time.Now().UTC()
	t1 := now.AddDate(0, -2, 0).UnixMicro() // 2 months ago
	t2 := now.AddDate(0, -1, 0).UnixMicro() // 1 month ago
	t3 := now.UnixMicro()                   // current month

	// 1. Parties
	// sp1: Employer (External, sp=0)
	// sp2: User Jan (User owned, sp=1)
	// sp3: Żabka (External, sp=0)
	_, err := tenantDB.Exec(`
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES
		('p-employer', 1, 'Employer LLC', 'ORGANIZATION', 0, 'ag1', 1),
		('p-jan', 1, 'Jan Kowalski', 'INDIVIDUAL', 1, 'ag1', 1),
		('p-zabka', 1, 'Żabka Stores', 'ORGANIZATION', 0, 'ag1', 1);

		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES
		('a-employer', 1, 'p-employer', 'Payroll Account', 'EXTERNAL', 'PLN', 0, 'ag1', 1),
		('a-checking', 1, 'p-jan', 'mBank Checking', 'CHECKING', 'PLN', 0, 'ag1', 1),
		('a-savings', 1, 'p-jan', 'mBank Savings', 'SAVINGS', 'PLN', 0, 'ag1', 1),
		('a-zabka', 1, 'p-zabka', 'Żabka POS', 'EXTERNAL', 'PLN', 0, 'ag1', 1);

		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at) VALUES
		('a-checking', 'PLN', 500000, 1),
		('a-savings', 'PLN', 1000000, 1);

		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at) VALUES
		('b-groceries', 'Food', 'Groceries', 0, 1, 1),
		('b-fun', 'Discretionary', 'Fun & Hobbies', 0, 1, 1);

		INSERT INTO targets_projection (id, bucket_id, name, target_type, priority, sort_order, amount_minor, currency, recurrence_cadence, recurrence_days, non_spending_strategy, is_archived, version, updated_at) VALUES
		('tg-1', 'b-groceries', 'Weekly Groceries', 'EXPENSE', 3, 0, 20000, 'PLN', 'WEEKLY', 0, 'ROLL_OVER', 0, 1, 1),
		('tg-2', 'b-fun', 'Monthly Fun', 'EXPENSE', 2, 0, 50000, 'PLN', 'MONTHLY', 0, 'RETURN_TO_POOL', 0, 1, 1);
	`)
	if err != nil {
		t.Fatalf("failed seeding base metadata: %v", err)
	}

	// 2. Budget Transactions:
	// tx1: Inflow 8,000 PLN (800,000 minor) at t1 (Employer -> Checking)
	// tx2: Outflow 1,500 PLN (150,000 minor) at t1 (Checking -> Żabka, Groceries)
	// tx3: Outflow 500 PLN (50,000 minor) at t2 (Checking -> Żabka, Fun)
	// tx4: Transfer 2,000 PLN (200,000 minor) at t2 (Checking -> Savings, user internal)
	// tx5: Inflow 8,500 PLN (850,000 minor) at t3 (Employer -> Checking)
	// tx6: Outflow 2,200 PLN (220,000 minor) at t3 (Checking -> Żabka, Groceries)
	_, err = tenantDB.Exec(`
		INSERT INTO budget_transactions_current 
		(id, version, source_account_id, destination_account_id, source_amount_minor, source_currency, destination_amount_minor, destination_currency, bucket_id, transaction_date, notes, updated_at)
		VALUES
		('tx-1', 1, 'a-employer', 'a-checking', 800000, 'PLN', 800000, 'PLN', NULL, ?, 'Salary 1', 1),
		('tx-2', 1, 'a-checking', 'a-zabka', 150000, 'PLN', 150000, 'PLN', 'b-groceries', ?, 'Groceries 1', 1),
		('tx-3', 1, 'a-checking', 'a-zabka', 50000, 'PLN', 50000, 'PLN', 'b-fun', ?, 'Board game', 1),
		('tx-4', 1, 'a-checking', 'a-savings', 200000, 'PLN', 200000, 'PLN', NULL, ?, 'Internal save', 1),
		('tx-5', 1, 'a-employer', 'a-checking', 850000, 'PLN', 850000, 'PLN', NULL, ?, 'Salary 2', 1),
		('tx-6', 1, 'a-checking', 'a-zabka', 220000, 'PLN', 220000, 'PLN', 'b-groceries', ?, 'Groceries 2', 1);
	`, t1, t1+1000, t2, t2+1000, t3, t3+1000)
	if err != nil {
		t.Fatalf("failed seeding transactions: %v", err)
	}
}

func TestIncomeVsExpenseAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	res, err := report.CalculateIncomeVsExpense(ctx, tenantDB, mainDB, report.ReportFilter{})
	if err != nil {
		t.Fatalf("CalculateIncomeVsExpense failed: %v", err)
	}

	if len(res.Months) == 0 {
		t.Fatalf("expected monthly results, got 0")
	}

	// Total income = 800000 + 850000 = 1650000
	if res.TotalIncomeMinor != 1650000 {
		t.Errorf("expected total income 1650000, got %d", res.TotalIncomeMinor)
	}
	// Total expense = 150000 + 50000 + 220000 = 420000 (internal transfer tx4 excluded)
	if res.TotalExpenseMinor != 420000 {
		t.Errorf("expected total expense 420000, got %d", res.TotalExpenseMinor)
	}
	if res.TotalNetMinor != 1230000 {
		t.Errorf("expected total net 1230000, got %d", res.TotalNetMinor)
	}
}

func TestSpendingTrendsAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	res, err := report.CalculateSpendingTrends(ctx, tenantDB, mainDB, report.ReportFilter{})
	if err != nil {
		t.Fatalf("CalculateSpendingTrends failed: %v", err)
	}

	if len(res.Points) == 0 {
		t.Fatalf("expected trend points, got 0")
	}
	if len(res.BucketNames) != 2 {
		t.Errorf("expected 2 buckets, got %d", len(res.BucketNames))
	}
}

func TestSpendingBreakdownAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	res, err := report.CalculateSpendingBreakdown(ctx, tenantDB, mainDB, report.ReportFilter{})
	if err != nil {
		t.Fatalf("CalculateSpendingBreakdown failed: %v", err)
	}

	if res.TotalSpentMinor != 420000 {
		t.Errorf("expected total spent 420000, got %d", res.TotalSpentMinor)
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected 2 breakdown items, got %d", len(res.Items))
	}
	// b-groceries: 150000 + 220000 = 370000 (first item due to desc sort)
	if res.Items[0].BucketName != "Groceries" || res.Items[0].TotalSpentMinor != 370000 {
		t.Errorf("expected Groceries 370000, got %s %d", res.Items[0].BucketName, res.Items[0].TotalSpentMinor)
	}
}

func TestNetWorthAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	res, err := report.CalculateNetWorth(ctx, tenantDB, mainDB, report.ReportFilter{})
	if err != nil {
		t.Fatalf("CalculateNetWorth failed: %v", err)
	}

	if len(res.Points) == 0 {
		t.Fatalf("expected net worth points, got 0")
	}
	if res.CurrentNetWorthMinor != 1230000 {
		t.Errorf("expected current net worth 1230000, got %d", res.CurrentNetWorthMinor)
	}
}

func TestSankeyFlowAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	res, err := report.CalculateSankeyFlow(ctx, tenantDB, mainDB, report.ReportFilter{})
	if err != nil {
		t.Fatalf("CalculateSankeyFlow failed: %v", err)
	}

	if len(res.Nodes) == 0 || len(res.Links) == 0 {
		t.Fatalf("expected non-empty sankey nodes and links")
	}
}

func TestBalanceForecastAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	res, err := report.CalculateBalanceForecast(ctx, tenantDB, 30)
	if err != nil {
		t.Fatalf("CalculateBalanceForecast failed: %v", err)
	}

	if res.CurrentBalanceMinor != 1500000 { // 500,000 + 1,000,000
		t.Errorf("expected current balance 1500000, got %d", res.CurrentBalanceMinor)
	}
	if len(res.Points) != 30 {
		t.Errorf("expected 30 forecast points, got %d", len(res.Points))
	}
}

func TestPartyCashflowAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	res, err := report.CalculatePartyCashflow(ctx, tenantDB, mainDB, report.ReportFilter{})
	if err != nil {
		t.Fatalf("CalculatePartyCashflow failed: %v", err)
	}

	if len(res.Parties) != 2 {
		t.Fatalf("expected 2 parties (Employer and Żabka), got %d", len(res.Parties))
	}
	if res.TotalInflowMinor != 1650000 {
		t.Errorf("expected total inflow 1650000, got %d", res.TotalInflowMinor)
	}
	if res.TotalOutflowMinor != 420000 {
		t.Errorf("expected total outflow 420000, got %d", res.TotalOutflowMinor)
	}
}

func TestBudgetAnalysisAggregation(t *testing.T) {
	logger.BindTestLogger(t)
	mainDB, tenantDB := setupTestDatabases(t)
	defer mainDB.Close()
	defer tenantDB.Close()

	seedTestData(t, tenantDB)
	ctx := context.Background()

	// Assign funds to bucket b1
	now := time.Now().UTC().UnixMicro()
	_, err := tenantDB.Exec(`
		INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES ('ev1', 'b1', 1, 'FUNDS_ASSIGNED', '{"amount_minor": 500000, "currency": "PLN"}', 'user1', 'c1', ?)`,
		now)
	if err != nil {
		t.Fatalf("failed to insert bucket event: %v", err)
	}

	res, err := report.CalculateBudgetAnalysis(ctx, tenantDB, mainDB, report.ReportFilter{})
	if err != nil {
		t.Fatalf("CalculateBudgetAnalysis failed: %v", err)
	}

	if len(res.Points) == 0 {
		t.Fatalf("expected at least 1 point in budget analysis, got 0")
	}
}
