package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/labstack/echo/v4"
)

func TestAuthAndWorkspaceFlow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "balance_web_test_*")
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

	e := echo.New()
	authHandler := NewAuthHandler(mainDB, tenantMgr)
	workspaceHandler := NewWorkspaceHandler(mainDB, tenantMgr)
	txHandler := NewTransactionsHandler(mainDB, tenantMgr)

	// 1. Test Register
	form := url.Values{}
	form.Set("email", "test@example.com")
	form.Set("password", "SecureP@ss123")
	form.Set("display_name", "Test User")

	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = authHandler.HandleRegister(c)
	if err != nil {
		t.Fatalf("HandleRegister failed: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect to /budgets, got %d", rec.Code)
	}

	// Extract session cookie
	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, ck := range cookies {
		if ck.Name == SessionCookieName {
			sessionCookie = ck
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected session cookie")
	}

	// 2. Fetch User ID and CSRF Token from sessions
	tokenHash := domain.NewUUIDv7() // dummy check
	_ = tokenHash
	var userID, csrfToken string
	err = mainDB.QueryRow("SELECT user_id, csrf_token FROM sessions LIMIT 1").Scan(&userID, &csrfToken)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Test Create Budget
	bForm := url.Values{}
	bForm.Set("name", "Home Budget")
	bForm.Set("default_currency", "PLN")
	bForm.Set("timezone", "Europe/Warsaw")

	bReq := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader(bForm.Encode()))
	bReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	bRec := httptest.NewRecorder()
	bCtx := e.NewContext(bReq, bRec)
	bCtx.Set(ContextUserIDKey, userID)

	err = workspaceHandler.CreateBudget(bCtx)
	if err != nil {
		t.Fatalf("CreateBudget failed: %v", err)
	}
	if bRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", bRec.Code)
	}

	loc := bRec.Header().Get("Location")
	parts := strings.Split(loc, "/")
	budgetID := parts[2]

	// 4. Test View Budget
	vReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget", nil)
	vRec := httptest.NewRecorder()
	vCtx := e.NewContext(vReq, vRec)
	vCtx.SetParamNames("budgetId")
	vCtx.SetParamValues(budgetID)
	vCtx.Set(ContextUserIDKey, userID)
	vCtx.Set(ContextRoleKey, "OWNER")

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		t.Fatal(err)
	}
	vCtx.Set(ContextBudgetDBKey, tenantDB)

	err = workspaceHandler.BudgetView(vCtx)
	if err != nil {
		t.Fatalf("BudgetView failed: %v", err)
	}
	if vRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", vRec.Code)
	}
	if !strings.Contains(vRec.Body.String(), "Home Budget") {
		t.Fatalf("expected Home Budget in HTML output")
	}
	if !strings.Contains(vRec.Body.String(), "No accounts connected yet") {
		t.Fatalf("expected 'No accounts connected yet' in Accounts Summary")
	}
	if strings.Contains(vRec.Body.String(), "5,420.50") || strings.Contains(vRec.Body.String(), "12,000.00") {
		t.Fatalf("unexpected hardcoded account balances in Accounts Summary")
	}

	// 5. Test Bank Sync (Imports mock transactions)
	sReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/transactions/sync", nil)
	sRec := httptest.NewRecorder()
	sCtx := e.NewContext(sReq, sRec)
	sCtx.SetParamNames("budgetId")
	sCtx.SetParamValues(budgetID)
	sCtx.Set(ContextUserIDKey, userID)
	sCtx.Set(ContextBudgetDBKey, tenantDB)
	sCtx.Set("correlationID", domain.NewUUIDv7())

	err = txHandler.SyncWithBank(sCtx)
	if err != nil {
		t.Fatalf("SyncWithBank failed: %v", err)
	}
	if sRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", sRec.Code)
	}

	var rawCount int
	tenantDB.QueryRowContext(context.Background(), "SELECT count(*) FROM raw_transactions_current").Scan(&rawCount)
	if rawCount == 0 {
		t.Fatalf("expected imported raw transactions from mock sync, got 0")
	}
}
