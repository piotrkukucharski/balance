package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

func TestReportsDataEndpoints(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_reports_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatal(err)
	}

	budgetsDir := filepath.Join(tmpDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantMgr.CloseAll()

	reportsHandler := NewReportsHandler(mainDB, tenantMgr)
	e := echo.New()

	userID := domain.NewUUIDv7()
	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'user@test.com', 'h', 'User', ?, ?)`, userID, now, now)
	_, _ = mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'Test Budget', 'PLN', 'UTC', ?, ?, ?)`, budgetID, userID, now, now)

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	endpoints := []struct {
		name    string
		handler func(echo.Context) error
		path    string
	}{
		{"AgeOfMoney", reportsHandler.GetAgeOfMoneyData, "/budgets/" + budgetID + "/reports/age-of-money/data"},
		{"IncomeExpense", reportsHandler.GetIncomeExpenseData, "/budgets/" + budgetID + "/reports/income-expense/data"},
		{"SpendingTrends", reportsHandler.GetSpendingTrendsData, "/budgets/" + budgetID + "/reports/spending-trends/data"},
		{"SpendingBreakdown", reportsHandler.GetSpendingBreakdownData, "/budgets/" + budgetID + "/reports/spending-breakdown/data"},
		{"NetWorth", reportsHandler.GetNetWorthData, "/budgets/" + budgetID + "/reports/net-worth/data"},
		{"Sankey", reportsHandler.GetSankeyData, "/budgets/" + budgetID + "/reports/sankey/data"},
		{"Forecast", reportsHandler.GetForecastData, "/budgets/" + budgetID + "/reports/forecast/data"},
		{"BudgetAnalysis", reportsHandler.GetBudgetAnalysisData, "/budgets/" + budgetID + "/reports/budget-analysis/data"},
		{"MonteCarlo", reportsHandler.GetMonteCarloData, "/budgets/" + budgetID + "/reports/monte-carlo/data"},
		{"PartyCashflow", reportsHandler.GetPartyCashflowData, "/budgets/" + budgetID + "/reports/party-cashflow/data"},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, ep.path, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("budgetId")
			c.SetParamValues(budgetID)
			c.Set(ContextUserIDKey, userID)
			c.Set(ContextBudgetDBKey, tenantDB)

			if err := ep.handler(c); err != nil {
				t.Fatalf("handler error for %s: %v", ep.name, err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200 for %s, got %d: %s", ep.name, rec.Code, rec.Body.String())
			}
			ct := rec.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				t.Errorf("expected json content-type, got %s", ct)
			}
		})
	}
}

func TestReportDetailView(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_report_detail_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatal(err)
	}

	budgetsDir := filepath.Join(tmpDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantMgr.CloseAll()

	reportsHandler := NewReportsHandler(mainDB, tenantMgr)
	e := echo.New()

	userID := domain.NewUUIDv7()
	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'user@test.com', 'h', 'User', ?, ?)`, userID, now, now)
	_, _ = mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'Test Budget', 'PLN', 'UTC', ?, ?, ?)`, budgetID, userID, now, now)

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	types := []string{
		"age-of-money", "income-expense", "spending-trends", "spending-breakdown",
		"net-worth", "sankey", "forecast", "budget-analysis", "monte-carlo", "party-cashflow",
	}

	for _, rt := range types {
		t.Run("DetailView_"+rt, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/reports/"+rt, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("budgetId", "reportType")
			c.SetParamValues(budgetID, rt)
			c.Set(ContextUserIDKey, userID)
			c.Set(ContextBudgetDBKey, tenantDB)
			c.Set("csrfToken", "test_csrf")

			if err := reportsHandler.ReportDetailView(c); err != nil {
				t.Fatalf("ReportDetailView failed for %s: %v", rt, err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK for %s, got %d", rt, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "detail_chart_") {
				t.Errorf("expected chart container in %s detail view", rt)
			}
			if !strings.Contains(body, "report-filters-form") {
				t.Errorf("expected filters form in %s detail view", rt)
			}
		})
	}
}
