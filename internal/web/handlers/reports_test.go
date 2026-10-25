package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/report"
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

func TestParseReportFilter(t *testing.T) {
	e := echo.New()

	t.Run("Range30d", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?range=30d&currency=EUR", nil)
		c := e.NewContext(req, httptest.NewRecorder())
		f := parseReportFilter(c, "PLN")
		if f.TargetCurrency != "EUR" {
			t.Errorf("expected currency EUR, got %s", f.TargetCurrency)
		}
		if f.StartDateMicros == 0 || f.EndDateMicros == 0 {
			t.Errorf("expected non-zero start and end date for 30d range")
		}
		if f.EndDateMicros <= f.StartDateMicros {
			t.Errorf("expected end date > start date")
		}
		if !f.ExcludeInternal {
			t.Errorf("expected exclude internal default true")
		}
	})

	t.Run("RangeYTD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?range=YTD&exclude_internal=false", nil)
		c := e.NewContext(req, httptest.NewRecorder())
		f := parseReportFilter(c, "PLN")
		if f.StartDateMicros == 0 {
			t.Errorf("expected non-zero start date for YTD")
		}
		if f.ExcludeInternal {
			t.Errorf("expected exclude internal false")
		}
	})

	t.Run("RangeAll", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/?range=all", nil)
		c := e.NewContext(req, httptest.NewRecorder())
		f := parseReportFilter(c, "PLN")
		if f.StartDateMicros != 0 || f.EndDateMicros != 0 {
			t.Errorf("expected zero start and end date for all range")
		}
	})
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
			if !strings.Contains(body, "report-kpi-cards") {
				t.Errorf("expected kpi cards container in %s detail view", rt)
			}
			if !strings.Contains(body, "saved-presets-select") {
				t.Errorf("expected presets select in %s detail view", rt)
			}
		})
	}

	t.Run("DetailView_WithSavedPreset", func(t *testing.T) {
		reportID := domain.NewUUIDv7()
		_, err := tenantDB.Exec(`
			INSERT INTO saved_reports_current (id, version, name, report_type, config_json, is_pinned_widget, agent_id, updated_at)
			VALUES (?, 1, 'My Preset Test', 'INCOME_VS_EXPENSE', '{"range":"90d","currency":"EUR","exclude_internal":true}', 1, ?, ?)`,
			reportID, userID, now)
		if err != nil {
			t.Fatalf("insert saved report failed: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/reports/income-expense", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("budgetId", "reportType")
		c.SetParamValues(budgetID, "income-expense")
		c.Set(ContextUserIDKey, userID)
		c.Set(ContextBudgetDBKey, tenantDB)
		c.Set("csrfToken", "test_csrf")

		if err := reportsHandler.ReportDetailView(c); err != nil {
			t.Fatalf("ReportDetailView failed: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "My Preset Test") {
			t.Errorf("expected saved preset name 'My Preset Test' in preset dropdown")
		}
	})
}

func TestSavedReportsManagement(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_saved_reports_test_*")
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

	// 1. Create a saved report
	form := url.Values{}
	form.Set("name", "Q1 Executive Summary")
	form.Set("report_type", "INCOME_VS_EXPENSE")
	form.Set("config_json", `{"range":"90d","currency":"PLN","exclude_internal":true}`)
	form.Set("is_pinned_widget", "true")

	req := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/reports/saved", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("budgetId")
	c.SetParamValues(budgetID)
	c.Set(ContextUserIDKey, userID)
	c.Set(ContextBudgetDBKey, tenantDB)

	if err := reportsHandler.SaveReportConfig(c); err != nil {
		t.Fatalf("SaveReportConfig failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var saveResp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saveResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	savedID := saveResp.ID

	// 2. List saved reports
	listReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/reports/saved/list", nil)
	listRec := httptest.NewRecorder()
	listCtx := e.NewContext(listReq, listRec)
	listCtx.SetParamNames("budgetId")
	listCtx.SetParamValues(budgetID)
	listCtx.Set(ContextUserIDKey, userID)
	listCtx.Set(ContextBudgetDBKey, tenantDB)

	if err := reportsHandler.ListSavedReportsJSON(listCtx); err != nil {
		t.Fatalf("ListSavedReportsJSON failed: %v", err)
	}
	var items []SavedReportItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed decoding saved reports list: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Q1 Executive Summary" || !items[0].IsPinnedWidget {
		t.Fatalf("unexpected saved reports list: %+v", items)
	}

	// 3. Toggle pin status
	pinReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/reports/saved/"+savedID+"/toggle-pin", nil)
	pinReq.Header.Set("Accept", "application/json")
	pinRec := httptest.NewRecorder()
	pinCtx := e.NewContext(pinReq, pinRec)
	pinCtx.SetParamNames("budgetId", "reportId")
	pinCtx.SetParamValues(budgetID, savedID)
	pinCtx.Set(ContextUserIDKey, userID)
	pinCtx.Set(ContextBudgetDBKey, tenantDB)

	if err := reportsHandler.TogglePinReport(pinCtx); err != nil {
		t.Fatalf("TogglePinReport failed: %v", err)
	}
	var pinResp struct {
		ID             string `json:"id"`
		IsPinnedWidget bool   `json:"is_pinned_widget"`
	}
	if err := json.Unmarshal(pinRec.Body.Bytes(), &pinResp); err != nil {
		t.Fatalf("failed decoding pin response: %v", err)
	}
	if pinResp.IsPinnedWidget {
		t.Fatalf("expected report to be unpinned")
	}

	// 4. Delete saved report
	delReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/reports/saved/"+savedID+"/delete", nil)
	delReq.Header.Set("Accept", "application/json")
	delRec := httptest.NewRecorder()
	delCtx := e.NewContext(delReq, delRec)
	delCtx.SetParamNames("budgetId", "reportId")
	delCtx.SetParamValues(budgetID, savedID)
	delCtx.Set(ContextUserIDKey, userID)
	delCtx.Set(ContextBudgetDBKey, tenantDB)

	if err := reportsHandler.DeleteSavedReport(delCtx); err != nil {
		t.Fatalf("DeleteSavedReport failed: %v", err)
	}
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on deletion, got %d", delRec.Code)
	}

	// Verify removal from current projection and presence in append-only table
	var count int
	_ = tenantDB.QueryRow("SELECT COUNT(*) FROM saved_reports_current WHERE id = ?", savedID).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 in saved_reports_current, got %d", count)
	}
	_ = tenantDB.QueryRow("SELECT COUNT(*) FROM saved_reports WHERE id = ? AND action = 'SOFT_DELETE'", savedID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 SOFT_DELETE audit log in saved_reports, got %d", count)
	}
}

func TestCalculateAgeOfMoney_LiquidScopeAndDeployments(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_aom_test_*")
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

	userID := domain.NewUUIDv7()
	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC()
	nowMicros := now.UnixMicro()

	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'user@test.com', 'h', 'User', ?, ?)`, userID, nowMicros, nowMicros)
	_, _ = mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'Test Budget', 'PLN', 'UTC', ?, ?, ?)`, budgetID, userID, nowMicros, nowMicros)

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	userPartyID := domain.NewUUIDv7()
	extPartyID := domain.NewUUIDv7()

	_, err = tenantDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES
		(?, 1, 'My Family', 'USER', 1, ?, ?),
		(?, 1, 'External Merchant/Employer', 'EXTERNAL', 0, ?, ?)`,
		userPartyID, userID, nowMicros,
		extPartyID, userID, nowMicros,
	)
	if err != nil {
		t.Fatal(err)
	}

	checkingAcctID := domain.NewUUIDv7()
	savingsAcctID := domain.NewUUIDv7()
	investAcctID := domain.NewUUIDv7()
	extAcctID := domain.NewUUIDv7()

	_, err = tenantDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES
		(?, 1, ?, 'Checking', 'CHECKING', 'PLN', 0, ?, ?),
		(?, 1, ?, 'Savings', 'SAVINGS', 'PLN', 0, ?, ?),
		(?, 1, ?, 'Brokerage', 'INVESTMENT', 'PLN', 0, ?, ?),
		(?, 1, ?, 'Merchant/Employer', 'EXTERNAL', 'PLN', 0, ?, ?)`,
		checkingAcctID, userPartyID, userID, nowMicros,
		savingsAcctID, userPartyID, userID, nowMicros,
		investAcctID, userPartyID, userID, nowMicros,
		extAcctID, extPartyID, userID, nowMicros,
	)
	if err != nil {
		t.Fatal(err)
	}

	dayMicros := int64(24 * 3600 * 1_000_000)
	t0 := now.AddDate(0, 0, -100).UnixMicro() // Day 0
	t10 := t0 + 10*dayMicros                  // Day 10
	t20 := t0 + 20*dayMicros                  // Day 20
	t30 := t0 + 30*dayMicros                  // Day 30
	t40 := t0 + 40*dayMicros                  // Day 40

	// 1. External income to Checking: 10,000 PLN at Day 0 (Inflow)
	// 2. Liquid-to-liquid transfer: Checking -> Savings: 2,000 PLN at Day 10 (Ignored!)
	// 3. Liquid-to-investment: Checking -> Investment: 3,000 PLN at Day 20 (Capital deployment Outflow!)
	// 4. Investment return: Investment -> Checking: 1,000 PLN at Day 30 (Inflow!)
	// 5. External spending: Checking -> Merchant: 2,000 PLN at Day 40 (Spending Outflow!)
	_, err = tenantDB.Exec(`INSERT INTO budget_transactions_current 
		(id, version, source_account_id, destination_account_id, source_amount_minor, source_currency, destination_amount_minor, destination_currency, transaction_date, updated_at) VALUES
		(?, 1, ?, ?, 1000000, 'PLN', 1000000, 'PLN', ?, ?),
		(?, 1, ?, ?, 200000, 'PLN', 200000, 'PLN', ?, ?),
		(?, 1, ?, ?, 300000, 'PLN', 300000, 'PLN', ?, ?),
		(?, 1, ?, ?, 100000, 'PLN', 100000, 'PLN', ?, ?),
		(?, 1, ?, ?, 200000, 'PLN', 200000, 'PLN', ?, ?)`,
		domain.NewUUIDv7(), extAcctID, checkingAcctID, t0, t0,
		domain.NewUUIDv7(), checkingAcctID, savingsAcctID, t10, t10,
		domain.NewUUIDv7(), checkingAcctID, investAcctID, t20, t20,
		domain.NewUUIDv7(), investAcctID, checkingAcctID, t30, t30,
		domain.NewUUIDv7(), checkingAcctID, extAcctID, t40, t40,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Calculate unfiltered Age of Money
	ctx := context.Background()
	res, err := reportsHandler.CalculateAgeOfMoneyForBudget(ctx, tenantDB)
	if err != nil {
		t.Fatalf("CalculateAgeOfMoneyForBudget failed: %v", err)
	}

	// Should have exactly 2 outflows in history:
	// - Day 20 deployment (age = 20 days)
	// - Day 40 spending (age = 40 days)
	if len(res.History) != 2 {
		t.Fatalf("expected 2 history points, got %d", len(res.History))
	}
	if res.History[0].AgeDays != 20 {
		t.Errorf("expected first outflow age 20 days, got %d", res.History[0].AgeDays)
	}
	if res.History[1].AgeDays != 40 {
		t.Errorf("expected second outflow age 40 days, got %d", res.History[1].AgeDays)
	}
	expectedHeadline := (20 + 40) / 2
	if res.HeadlineDays != expectedHeadline {
		t.Errorf("expected headline %d, got %d", expectedHeadline, res.HeadlineDays)
	}

	// Calculate with date filter: only include Day 35 to Day 50
	filter := report.ReportFilter{
		StartDateMicros: t0 + 35*dayMicros,
		EndDateMicros:   t0 + 50*dayMicros,
	}
	filteredRes, err := reportsHandler.CalculateAgeOfMoneyForBudget(ctx, tenantDB, filter)
	if err != nil {
		t.Fatalf("CalculateAgeOfMoneyForBudget with filter failed: %v", err)
	}
	if len(filteredRes.History) != 1 {
		t.Fatalf("expected 1 history point in filtered range, got %d", len(filteredRes.History))
	}
	if filteredRes.History[0].AgeDays != 40 {
		t.Errorf("expected filtered outflow age 40 days, got %d", filteredRes.History[0].AgeDays)
	}
	if filteredRes.HeadlineDays != 40 {
		t.Errorf("expected headline 40 for filtered range, got %d", filteredRes.HeadlineDays)
	}
}
