package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

func TestAuthAndWorkspaceFlow(t *testing.T) {
	logger.BindTestLogger(t)

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
	reportsHandler := NewReportsHandler(mainDB, tenantMgr)
	partiesHandler := NewPartiesHandler(mainDB, tenantMgr)
	membersHandler := NewMembersHandler(mainDB, tenantMgr)
	accountsHandler := NewAccountsHandler(mainDB, tenantMgr)

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

	// 6. Test that all budget views render "💳 2. Accounts" in navigation menu
	viewsToTest := []struct {
		name    string
		handler func(echo.Context) error
		path    string
	}{
		{"Transactions", txHandler.TransactionsView, "/budgets/" + budgetID + "/transactions"},
		{"Reports", reportsHandler.ReportsView, "/budgets/" + budgetID + "/reports"},
		{"Parties", partiesHandler.PartiesView, "/budgets/" + budgetID + "/parties"},
		{"Members", membersHandler.MembersView, "/budgets/" + budgetID + "/members"},
		{"Accounts", accountsHandler.AccountsView, "/budgets/" + budgetID + "/accounts"},
		{"Budget", workspaceHandler.BudgetView, "/budgets/" + budgetID + "/budget"},
	}

	for _, vt := range viewsToTest {
		t.Run("NavigationMenu_"+vt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, vt.path, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("budgetId")
			c.SetParamValues(budgetID)
			c.Set(ContextUserIDKey, userID)
			c.Set(ContextRoleKey, "OWNER")
			c.Set(ContextBudgetDBKey, tenantDB)
			c.Set("csrfToken", "dummy_csrf")

			if err := vt.handler(c); err != nil {
				t.Fatalf("%s view failed: %v", vt.name, err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK for %s, got %d", vt.name, rec.Code)
			}
			body := rec.Body.String()
			expectedMenuItem := fmt.Sprintf(`<li><a href="/budgets/%s/accounts"`, budgetID)
			if !strings.Contains(body, expectedMenuItem) {
				t.Fatalf("expected %s view to contain Accounts menu item", vt.name)
			}
			if !strings.Contains(body, "💳 2. Accounts") {
				t.Fatalf("expected %s view to display '💳 2. Accounts'", vt.name)
			}
		})
	}
}

func TestSessionOrBearerAuthMiddleware(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_auth_mw_test_*")
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

	e := echo.New()
	mw := SessionOrBearerAuthMiddleware(mainDB)
	handler := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "protected content")
	})

	t.Run("Expired or invalid session with HTML accept redirects to /login and clears cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/budgets", nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.AddCookie(&http.Cookie{
			Name:  SessionCookieName,
			Value: "non_existent_token_12345",
		})
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := handler(c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected status 303 See Other, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/login" {
			t.Fatalf("expected redirect to /login, got %s", loc)
		}

		// Verify cookie was cleared
		var cleared bool
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == SessionCookieName && ck.MaxAge < 0 {
				cleared = true
				break
			}
		}
		if !cleared {
			t.Fatalf("expected session cookie to be cleared (MaxAge < 0)")
		}
	})

	t.Run("Expired or invalid session with API JSON accept returns 401 Unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/budgets", nil)
		req.Header.Set("Accept", "application/json")
		req.AddCookie(&http.Cookie{
			Name:  SessionCookieName,
			Value: "non_existent_token_12345",
		})
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := handler(c)
		if err == nil {
			t.Fatalf("expected 401 Unauthorized error, got nil")
		}
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || httpErr.Code != http.StatusUnauthorized {
			t.Fatalf("expected echo.HTTPError 401, got %v", err)
		}
	})

	t.Run("Missing cookie with HTML accept redirects to /login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/budgets", nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := handler(c)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected status 303 See Other, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/login" {
			t.Fatalf("expected redirect to /login, got %s", loc)
		}
	})
}

func TestFaviconServingAndViews(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_fav_test_*")
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

	authHandler := NewAuthHandler(mainDB, tenantMgr)
	e := echo.New()

	t.Run("LoginPage contains favicon link", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := authHandler.LoginPage(c); err != nil {
			t.Fatalf("LoginPage failed: %v", err)
		}
		if !strings.Contains(rec.Body.String(), `<link rel="icon" type="image/x-icon" href="/favicon.ico">`) {
			t.Fatalf("LoginPage missing favicon link")
		}
	})

	t.Run("RegisterPage contains favicon link", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/register", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := authHandler.RegisterPage(c); err != nil {
			t.Fatalf("RegisterPage failed: %v", err)
		}
		if !strings.Contains(rec.Body.String(), `<link rel="icon" type="image/x-icon" href="/favicon.ico">`) {
			t.Fatalf("RegisterPage missing favicon link")
		}
	})
}


