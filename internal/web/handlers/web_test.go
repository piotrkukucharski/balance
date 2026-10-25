package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/budget"
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
	reportsHandler := NewReportsHandler(mainDB, tenantMgr)
	workspaceHandler := NewWorkspaceHandler(mainDB, tenantMgr, reportsHandler)
	txHandler := NewTransactionsHandler(mainDB, tenantMgr)
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

	// 6. Test that all budget views render "💳 2. Accounts" and "⚙️ 6. Settings" in navigation menu
	tokensHandler := NewTokensHandler(mainDB)

	viewsToTest := []struct {
		name    string
		handler func(echo.Context) error
		path    string
	}{
		{"Transactions", txHandler.TransactionsView, "/budgets/" + budgetID + "/transactions"},
		{"Reports", reportsHandler.ReportsView, "/budgets/" + budgetID + "/reports"},
		{"Parties", partiesHandler.PartiesView, "/budgets/" + budgetID + "/parties"},
		{"Settings", workspaceHandler.SettingsView, "/budgets/" + budgetID + "/settings"},
		{"Members", membersHandler.MembersView, "/budgets/" + budgetID + "/settings/members"},
		{"Tokens", tokensHandler.TokensView, "/budgets/" + budgetID + "/settings/tokens"},
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
			expectedSettingsLink := fmt.Sprintf(`/budgets/%s/settings`, budgetID)
			if !strings.Contains(body, expectedSettingsLink) {
				t.Fatalf("expected %s view to contain Settings link %s", vt.name, expectedSettingsLink)
			}
			expectedMembersLink := fmt.Sprintf(`/budgets/%s/settings/members`, budgetID)
			if !strings.Contains(body, expectedMembersLink) {
				t.Fatalf("expected %s view to contain Members sublink %s", vt.name, expectedMembersLink)
			}
			expectedTokensLink := fmt.Sprintf(`/budgets/%s/settings/tokens`, budgetID)
			if !strings.Contains(body, expectedTokensLink) {
				t.Fatalf("expected %s view to contain Tokens sublink %s", vt.name, expectedTokensLink)
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

	t.Run("Valid Bearer token authorizes and sets userID in context asynchronously updating last_used_at", func(t *testing.T) {
		userID := domain.NewUUIDv7()
		now := time.Now().UTC().UnixMicro()
		_, err = mainDB.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES (?, 'pat_user@example.com', 'hash', 'PAT User', ?, ?)`, userID, now, now)
		if err != nil {
			t.Fatal(err)
		}

		fullPAT, hash, err := auth.GeneratePAT()
		if err != nil {
			t.Fatal(err)
		}
		_, err = mainDB.Exec(`
			INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, created_at)
			VALUES (?, ?, 'Test Key', ?, ?, ?)`, domain.NewUUIDv7(), userID, hash, now+100000000, now)
		if err != nil {
			t.Fatal(err)
		}

		var capturedUserID string
		authedHandler := mw(func(c echo.Context) error {
			capturedUserID, _ = c.Get(ContextUserIDKey).(string)
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/budgets", nil)
		req.Header.Set("Authorization", "Bearer "+fullPAT)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := authedHandler(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		if capturedUserID != userID {
			t.Fatalf("expected userID %s, got %s", userID, capturedUserID)
		}

		// Wait briefly for async last_used_at update
		deadline := time.Now().Add(500 * time.Millisecond)
		var updated bool
		for time.Now().Before(deadline) {
			var lastUsed sql.NullInt64
			_ = mainDB.QueryRow("SELECT last_used_at FROM api_tokens WHERE token_hash = ?", hash).Scan(&lastUsed)
			if lastUsed.Valid && lastUsed.Int64 > 0 {
				updated = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !updated {
			t.Fatalf("expected last_used_at to be updated asynchronously")
		}
	})

	t.Run("Revoked Bearer token returns 401 Unauthorized", func(t *testing.T) {
		userID := domain.NewUUIDv7()
		now := time.Now().UTC().UnixMicro()
		_, err = mainDB.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES (?, 'revoked_user@example.com', 'hash', 'Revoked User', ?, ?)`, userID, now, now)
		if err != nil {
			t.Fatal(err)
		}

		fullPAT, hash, err := auth.GeneratePAT()
		if err != nil {
			t.Fatal(err)
		}
		_, err = mainDB.Exec(`
			INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, revoked_at, created_at)
			VALUES (?, ?, 'Revoked Key', ?, ?, ?, ?)`, domain.NewUUIDv7(), userID, hash, now+100000000, now, now)
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodGet, "/budgets", nil)
		req.Header.Set("Authorization", "Bearer "+fullPAT)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err = handler(c)
		if err == nil {
			t.Fatalf("expected 401 Unauthorized error, got nil")
		}
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || httpErr.Code != http.StatusUnauthorized {
			t.Fatalf("expected echo.HTTPError 401, got %v", err)
		}
	})

	t.Run("Tokens management view, creation and revocation", func(t *testing.T) {
		tokensHandler := NewTokensHandler(mainDB)
		userID := domain.NewUUIDv7()
		now := time.Now().UTC().UnixMicro()
		_, err = mainDB.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES (?, 'tokens_mgmt@example.com', 'hash', 'Tokens Mgmt User', ?, ?)`, userID, now, now)
		if err != nil {
			t.Fatal(err)
		}

		budgetID := domain.NewUUIDv7()
		_, err = mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_by_agent_id, created_at, updated_at)
			VALUES (?, 'Tokens Test Budget', 'PLN', ?, ?, ?)`, budgetID, userID, now, now)
		if err != nil {
			t.Fatal(err)
		}

		// 1. View Tokens page
		req := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/settings/tokens", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(budgetID)
		c.Set(ContextUserIDKey, userID)
		c.Set(ContextRoleKey, "OWNER")
		c.Set("csrfToken", "dummy_csrf")

		if err := tokensHandler.TokensView(c); err != nil {
			t.Fatalf("TokensView failed: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Personal Access Tokens (PAT)") {
			t.Fatalf("expected title in TokensView")
		}

		// 2. Create Token via POST
		form := url.Values{}
		form.Set("name", "Automation CLI")
		form.Set("expiration_days", "30")
		cReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/settings/tokens", strings.NewReader(form.Encode()))
		cReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		cRec := httptest.NewRecorder()
		cCtx := e.NewContext(cReq, cRec)
		cCtx.SetParamNames("budgetId")
		cCtx.SetParamValues(budgetID)
		cCtx.Set(ContextUserIDKey, userID)

		if err := tokensHandler.CreateToken(cCtx); err != nil {
			t.Fatalf("CreateToken failed: %v", err)
		}
		if cRec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 See Other redirect, got %d", cRec.Code)
		}
		loc := cRec.Header().Get("Location")
		if !strings.Contains(loc, "new_token=bal_pat_") {
			t.Fatalf("expected redirect URL to include newly generated token, got %s", loc)
		}

		// Verify persisted token in DB
		var tokenID string
		err = mainDB.QueryRow("SELECT id FROM api_tokens WHERE user_id = ? AND name = 'Automation CLI'", userID).Scan(&tokenID)
		if err != nil {
			t.Fatalf("expected token in DB: %v", err)
		}

		// 3. Revoke Token via POST
		rReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/settings/tokens/"+tokenID+"/revoke", nil)
		rRec := httptest.NewRecorder()
		rCtx := e.NewContext(rReq, rRec)
		rCtx.SetParamNames("budgetId", "tokenId")
		rCtx.SetParamValues(budgetID, tokenID)
		rCtx.Set(ContextUserIDKey, userID)

		if err := tokensHandler.RevokeToken(rCtx); err != nil {
			t.Fatalf("RevokeToken failed: %v", err)
		}
		if rRec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 See Other redirect, got %d", rRec.Code)
		}

		var revokedAt sql.NullInt64
		_ = mainDB.QueryRow("SELECT revoked_at FROM api_tokens WHERE id = ?", tokenID).Scan(&revokedAt)
		if !revokedAt.Valid || revokedAt.Int64 == 0 {
			t.Fatalf("expected token to be marked as revoked")
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

	t.Run("HandleLogin renders login page with error alert when user does not exist", func(t *testing.T) {
		form := url.Values{}
		form.Set("email", "missing@example.com")
		form.Set("password", "WrongPass123!")

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := authHandler.HandleLogin(c); err != nil {
			t.Fatalf("expected HTML response instead of unhandled error: %v", err)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401 Unauthorized, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "Invalid email or password. Please try again.") {
			t.Fatalf("expected error alert on login page, got: %s", body)
		}
		if !strings.Contains(body, `value="missing@example.com"`) {
			t.Fatalf("expected email input to preserve submitted email")
		}
	})
}

func TestBudgetRenameAndDeleteFlow(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_budget_crud_*")
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
	workspaceHandler := NewWorkspaceHandler(mainDB, tenantMgr, reportsHandler)
	e := echo.New()

	ownerID := domain.NewUUIDv7()
	editorID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'owner@example.com', 'h', 'Owner', ?, ?)`, ownerID, now, now)
	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'editor@example.com', 'h', 'Editor', ?, ?)`, editorID, now, now)

	// Create budget via workspaceHandler
	bForm := url.Values{}
	bForm.Set("name", "Initial Name")
	bForm.Set("default_currency", "PLN")
	bForm.Set("timezone", "Europe/Warsaw")

	bReq := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader(bForm.Encode()))
	bReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	bRec := httptest.NewRecorder()
	bCtx := e.NewContext(bReq, bRec)
	bCtx.Set(ContextUserIDKey, ownerID)

	if err := workspaceHandler.CreateBudget(bCtx); err != nil {
		t.Fatalf("CreateBudget failed: %v", err)
	}
	loc := bRec.Header().Get("Location")
	parts := strings.Split(loc, "/")
	budgetID := parts[2]

	// Add editor membership
	_, _ = mainDB.Exec(`INSERT INTO budget_members (budget_id, user_id, role, joined_at) VALUES (?, ?, 'EDITOR', ?)`, budgetID, editorID, now)

	// Verify UI visibility: OWNER sees Rename and Delete, EDITOR does not
	t.Run("UI controls visibility per role", func(t *testing.T) {
		// Owner view
		oReq := httptest.NewRequest(http.MethodGet, "/budgets", nil)
		oRec := httptest.NewRecorder()
		oCtx := e.NewContext(oReq, oRec)
		oCtx.Set(ContextUserIDKey, ownerID)
		oCtx.Set("csrfToken", "test_csrf")
		if err := workspaceHandler.BudgetsList(oCtx); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(oRec.Body.String(), "Rename") || !strings.Contains(oRec.Body.String(), "Delete Budget") {
			t.Fatalf("expected OWNER to see Rename and Delete Budget controls")
		}

		// Editor view
		eReq := httptest.NewRequest(http.MethodGet, "/budgets", nil)
		eRec := httptest.NewRecorder()
		eCtx := e.NewContext(eReq, eRec)
		eCtx.Set(ContextUserIDKey, editorID)
		eCtx.Set("csrfToken", "test_csrf")
		if err := workspaceHandler.BudgetsList(eCtx); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(eRec.Body.String(), "Rename") || strings.Contains(eRec.Body.String(), "Delete Budget") {
			t.Fatalf("expected non-OWNER not to see Rename or Delete Budget controls")
		}
	})

	// Test Rename Budget
	t.Run("Rename budget", func(t *testing.T) {
		rForm := url.Values{}
		rForm.Set("name", "Renamed Budget")
		rReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/update", strings.NewReader(rForm.Encode()))
		rReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rRec := httptest.NewRecorder()
		rCtx := e.NewContext(rReq, rRec)
		rCtx.SetParamNames("budgetId")
		rCtx.SetParamValues(budgetID)

		if err := workspaceHandler.UpdateBudget(rCtx); err != nil {
			t.Fatalf("UpdateBudget failed: %v", err)
		}
		if rRec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", rRec.Code)
		}

		var updatedName string
		_ = mainDB.QueryRow("SELECT name FROM budgets WHERE id = ?", budgetID).Scan(&updatedName)
		if updatedName != "Renamed Budget" {
			t.Fatalf("expected Renamed Budget, got %s", updatedName)
		}
	})

	// Test Budget Settings View and Update via /budgets/:id/settings
	t.Run("Budget settings view and update", func(t *testing.T) {
		tenantDB, err := tenantMgr.GetTenantDB(budgetID, false)
		if err != nil {
			t.Fatal(err)
		}

		// View Settings
		sReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/settings", nil)
		sRec := httptest.NewRecorder()
		sCtx := e.NewContext(sReq, sRec)
		sCtx.SetParamNames("budgetId")
		sCtx.SetParamValues(budgetID)
		sCtx.Set(ContextUserIDKey, ownerID)
		sCtx.Set(ContextRoleKey, "OWNER")
		sCtx.Set(ContextBudgetDBKey, tenantDB)
		sCtx.Set("csrfToken", "dummy_csrf")

		if err := workspaceHandler.SettingsView(sCtx); err != nil {
			t.Fatalf("SettingsView failed: %v", err)
		}
		if sRec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", sRec.Code)
		}
		body := sRec.Body.String()
		if !strings.Contains(body, "Renamed Budget") {
			t.Fatalf("expected budget name in settings view")
		}
		if !strings.Contains(body, "Danger Zone") {
			t.Fatalf("expected Danger Zone in settings view for OWNER")
		}

		// Update Settings via /budgets/:id/settings
		uForm := url.Values{}
		uForm.Set("name", "Settings Renamed Budget")
		uForm.Set("timezone", "UTC")
		uReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/settings", strings.NewReader(uForm.Encode()))
		uReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		uRec := httptest.NewRecorder()
		uCtx := e.NewContext(uReq, uRec)
		uCtx.SetParamNames("budgetId")
		uCtx.SetParamValues(budgetID)

		if err := workspaceHandler.UpdateBudget(uCtx); err != nil {
			t.Fatalf("UpdateBudget failed: %v", err)
		}
		if uRec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", uRec.Code)
		}
		if uRec.Header().Get("Location") != "/budgets/"+budgetID+"/settings" {
			t.Fatalf("expected redirect to /budgets/%s/settings, got %s", budgetID, uRec.Header().Get("Location"))
		}

		var checkName string
		_ = mainDB.QueryRow("SELECT name FROM budgets WHERE id = ?", budgetID).Scan(&checkName)
		if checkName != "Settings Renamed Budget" {
			t.Fatalf("expected Settings Renamed Budget, got %s", checkName)
		}
	})

	// Test Delete Budget
	t.Run("Delete budget removes record and files", func(t *testing.T) {
		dReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/delete", nil)
		dRec := httptest.NewRecorder()
		dCtx := e.NewContext(dReq, dRec)
		dCtx.SetParamNames("budgetId")
		dCtx.SetParamValues(budgetID)

		if err := workspaceHandler.DeleteBudget(dCtx); err != nil {
			t.Fatalf("DeleteBudget failed: %v", err)
		}
		if dRec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", dRec.Code)
		}

		var count int
		_ = mainDB.QueryRow("SELECT count(*) FROM budgets WHERE id = ?", budgetID).Scan(&count)
		if count != 0 {
			t.Fatalf("expected 0 budgets in main.db, got %d", count)
		}

		tenantFilePath := filepath.Join(budgetsDir, budgetID+".db")
		if _, err := os.Stat(tenantFilePath); !os.IsNotExist(err) {
			t.Fatalf("expected tenant database file to be removed from disk")
		}
	})
}

func TestTargetModalAndLuaFormulaAssistant(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "balance_target_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainDBPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
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
	workspaceHandler := NewWorkspaceHandler(mainDB, tenantMgr, reportsHandler)
	e := echo.New()

	userID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'user@example.com', 'h', 'User', ?, ?)`, userID, now, now)

	// Create budget
	bForm := url.Values{}
	bForm.Set("name", "Target Test Budget")
	bForm.Set("default_currency", "PLN")
	bForm.Set("timezone", "Europe/Warsaw")

	bReq := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader(bForm.Encode()))
	bReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	bRec := httptest.NewRecorder()
	bCtx := e.NewContext(bReq, bRec)
	bCtx.Set(ContextUserIDKey, userID)

	if err := workspaceHandler.CreateBudget(bCtx); err != nil {
		t.Fatalf("CreateBudget failed: %v", err)
	}
	loc := bRec.Header().Get("Location")
	budgetID := strings.Split(loc, "/")[2]

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		t.Fatalf("GetTenantDB failed: %v", err)
	}

	// Create a bucket
	bucketID, err := budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Living",
		Name:          "Mortgage",
		AgentID:       userID,
		CorrelationID: domain.NewUUIDv7(),
	})
	if err != nil {
		t.Fatalf("CreateBucket failed: %v", err)
	}

	// 1. Create target with FIXED amount
	t1Form := url.Values{}
	t1Form.Set("bucket_id", bucketID)
	t1Form.Set("name", "Required Mortgage Installment")
	t1Form.Set("target_type", "EXPENSE")
	t1Form.Set("priority", "3")
	t1Form.Set("amount_mode", "FIXED")
	t1Form.Set("amount", "900.00")
	t1Form.Set("currency", "PLN")
	t1Form.Set("recurrence_cadence", "MONTHLY")
	t1Form.Set("non_spending_strategy", "ARREARS_CUMULATIVE")

	t1Req := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(t1Form.Encode()))
	t1Req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	t1Rec := httptest.NewRecorder()
	t1Ctx := e.NewContext(t1Req, t1Rec)
	t1Ctx.SetParamNames("budgetId")
	t1Ctx.SetParamValues(budgetID)
	t1Ctx.Set(ContextBudgetDBKey, tenantDB)
	t1Ctx.Set(ContextUserIDKey, userID)
	t1Ctx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(t1Ctx); err != nil {
		t.Fatalf("CreateTarget (FIXED) failed: %v", err)
	}

	// 2. Create target with LUA_FORMULA mode
	t2Form := url.Values{}
	t2Form.Set("bucket_id", bucketID)
	t2Form.Set("name", "Mortgage Overpayment")
	t2Form.Set("target_type", "EXPENSE")
	t2Form.Set("priority", "1")
	t2Form.Set("amount_mode", "LUA_FORMULA")
	t2Form.Set("lua_script", `return math.max(0, money(2000) - target("Required Mortgage Installment").required)`)
	t2Form.Set("currency", "PLN")
	t2Form.Set("recurrence_cadence", "MONTHLY")
	t2Form.Set("non_spending_strategy", "RETURN_TO_POOL")

	t2Req := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(t2Form.Encode()))
	t2Req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	t2Rec := httptest.NewRecorder()
	t2Ctx := e.NewContext(t2Req, t2Rec)
	t2Ctx.SetParamNames("budgetId")
	t2Ctx.SetParamValues(budgetID)
	t2Ctx.Set(ContextBudgetDBKey, tenantDB)
	t2Ctx.Set(ContextUserIDKey, userID)
	t2Ctx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(t2Ctx); err != nil {
		t.Fatalf("CreateTarget (LUA_FORMULA) failed: %v", err)
	}

	// Verify target was created with amount_mode = LUA_FORMULA in tenantDB
	var t2ID, t2Mode, t2Script string
	var t2Amt int64
	err = tenantDB.QueryRow(`SELECT id, amount_mode, lua_script, amount_minor FROM targets_projection WHERE name = 'Mortgage Overpayment'`).Scan(&t2ID, &t2Mode, &t2Script, &t2Amt)
	if err != nil {
		t.Fatalf("failed querying targets_projection: %v", err)
	}
	if t2Mode != "LUA_FORMULA" {
		t.Fatalf("expected amount_mode LUA_FORMULA, got %s", t2Mode)
	}
	if !strings.Contains(t2Script, "target(\"Required Mortgage Installment\").required") {
		t.Fatalf("expected lua script to contain target reference, got: %s", t2Script)
	}

	// 3. Update the target
	updForm := url.Values{}
	updForm.Set("name", "Mortgage Overpayment Updated")
	updForm.Set("target_type", "EXPENSE")
	updForm.Set("priority", "2")
	updForm.Set("amount_mode", "LUA_FORMULA")
	updForm.Set("lua_script", `return math.max(0, money(2500) - target("Required Mortgage Installment").required)`)
	updForm.Set("recurrence_cadence", "MONTHLY")
	updForm.Set("non_spending_strategy", "RETURN_TO_POOL")

	updReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets/"+t2ID+"/update", strings.NewReader(updForm.Encode()))
	updReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	updRec := httptest.NewRecorder()
	updCtx := e.NewContext(updReq, updRec)
	updCtx.SetParamNames("budgetId", "id")
	updCtx.SetParamValues(budgetID, t2ID)
	updCtx.Set(ContextBudgetDBKey, tenantDB)
	updCtx.Set(ContextUserIDKey, userID)
	updCtx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.UpdateTarget(updCtx); err != nil {
		t.Fatalf("UpdateTarget failed: %v", err)
	}

	var updName, updScript string
	var updPrio int
	err = tenantDB.QueryRow(`SELECT name, priority, lua_script FROM targets_projection WHERE id = ?`, t2ID).Scan(&updName, &updPrio, &updScript)
	if err != nil {
		t.Fatalf("failed querying updated target: %v", err)
	}
	if updName != "Mortgage Overpayment Updated" || updPrio != 2 || !strings.Contains(updScript, "money(2500)") {
		t.Fatalf("unexpected updated target state: name=%s, prio=%d, script=%s", updName, updPrio, updScript)
	}

	// 4. Verify BudgetView renders Edit button, calculation mode switch, and script
	bvReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget", nil)
	bvRec := httptest.NewRecorder()
	bvCtx := e.NewContext(bvReq, bvRec)
	bvCtx.SetParamNames("budgetId")
	bvCtx.SetParamValues(budgetID)
	bvCtx.Set(ContextRoleKey, "OWNER")
	bvCtx.Set(ContextBudgetDBKey, tenantDB)
	bvCtx.Set(ContextUserIDKey, userID)
	bvCtx.Set("csrfToken", "test_csrf")

	if err := workspaceHandler.BudgetView(bvCtx); err != nil {
		t.Fatalf("BudgetView failed: %v", err)
	}
	body := bvRec.Body.String()
	if !strings.Contains(body, "target-edit-btn") {
		t.Fatalf("expected BudgetView to contain target-edit-btn")
	}
	if !strings.Contains(body, "openEditTargetModal") {
		t.Fatalf("expected BudgetView to contain openEditTargetModal function")
	}
	if !strings.Contains(body, "switchAmountMode") {
		t.Fatalf("expected BudgetView to contain switchAmountMode function")
	}
	if !strings.Contains(body, "generateFormulaAI") {
		t.Fatalf("expected BudgetView to contain generateFormulaAI function")
	}
	if !strings.Contains(body, "validateFormulaScript") {
		t.Fatalf("expected BudgetView to contain validateFormulaScript function")
	}
	if !strings.Contains(body, "mode_lua") || !strings.Contains(body, "mode_fixed") {
		t.Fatalf("expected target_modal to have calculation mode radio inputs")
	}
	if !strings.Contains(body, "target_recurrence_days") || !strings.Contains(body, "toggleCustomDays") {
		t.Fatalf("expected target_modal to have recurrence_days input and toggleCustomDays function")
	}
}

func TestCreateAndEditTarget_CustomDays(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "balance_custom_days_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainDBPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
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
	workspaceHandler := NewWorkspaceHandler(mainDB, tenantMgr, reportsHandler)
	e := echo.New()

	userID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'user@example.com', 'h', 'User', ?, ?)`, userID, now, now)

	// Create budget
	bForm := url.Values{}
	bForm.Set("name", "Custom Days Budget")
	bForm.Set("default_currency", "PLN")
	bForm.Set("timezone", "Europe/Warsaw")

	bReq := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader(bForm.Encode()))
	bReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	bRec := httptest.NewRecorder()
	bCtx := e.NewContext(bReq, bRec)
	bCtx.Set(ContextUserIDKey, userID)

	if err := workspaceHandler.CreateBudget(bCtx); err != nil {
		t.Fatalf("CreateBudget failed: %v", err)
	}
	loc := bRec.Header().Get("Location")
	budgetID := strings.Split(loc, "/")[2]

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		t.Fatalf("GetTenantDB failed: %v", err)
	}

	// Create bucket
	bucketID, err := budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Fixed",
		Name:          "Subscriptions",
		AgentID:       userID,
		CorrelationID: domain.NewUUIDv7(),
	})
	if err != nil {
		t.Fatalf("failed creating bucket: %v", err)
	}

	// 1. Create target with CUSTOM_DAYS and recurrence_days = 90
	f1 := url.Values{}
	f1.Set("bucket_id", bucketID)
	f1.Set("name", "Quarterly Card")
	f1.Set("amount", "125.00")
	f1.Set("currency", "PLN")
	f1.Set("recurrence_cadence", "CUSTOM_DAYS")
	f1.Set("recurrence_days", "90")
	f1.Set("non_spending_strategy", "ROLL_OVER")

	req1 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(f1.Encode()))
	req1.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec1 := httptest.NewRecorder()
	ctx1 := e.NewContext(req1, rec1)
	ctx1.SetParamNames("budgetId")
	ctx1.SetParamValues(budgetID)
	ctx1.Set(ContextBudgetDBKey, tenantDB)
	ctx1.Set(ContextUserIDKey, userID)
	ctx1.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(ctx1); err != nil {
		t.Fatalf("CreateTarget (CUSTOM_DAYS 90) failed: %v", err)
	}

	var t1ID string
	var t1Days int
	var t1Cadence string
	err = tenantDB.QueryRow(`SELECT id, recurrence_cadence, recurrence_days FROM targets_projection WHERE name = 'Quarterly Card'`).Scan(&t1ID, &t1Cadence, &t1Days)
	if err != nil {
		t.Fatalf("failed querying Quarterly Card target: %v", err)
	}
	if t1Cadence != "CUSTOM_DAYS" || t1Days != 90 {
		t.Fatalf("expected CUSTOM_DAYS with 90 days, got cadence=%s, days=%d", t1Cadence, t1Days)
	}

	// 2. Create target with CUSTOM_DAYS but empty recurrence_days (should gracefully default to 90 without CHECK constraint error)
	f2 := url.Values{}
	f2.Set("bucket_id", bucketID)
	f2.Set("name", "Omitted Days Target")
	f2.Set("amount", "50.00")
	f2.Set("currency", "PLN")
	f2.Set("recurrence_cadence", "CUSTOM_DAYS")
	f2.Set("recurrence_days", "")
	f2.Set("non_spending_strategy", "ROLL_OVER")

	req2 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(f2.Encode()))
	req2.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec2 := httptest.NewRecorder()
	ctx2 := e.NewContext(req2, rec2)
	ctx2.SetParamNames("budgetId")
	ctx2.SetParamValues(budgetID)
	ctx2.Set(ContextBudgetDBKey, tenantDB)
	ctx2.Set(ContextUserIDKey, userID)
	ctx2.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(ctx2); err != nil {
		t.Fatalf("CreateTarget with empty recurrence_days failed: %v", err)
	}

	var t2Days int
	err = tenantDB.QueryRow(`SELECT recurrence_days FROM targets_projection WHERE name = 'Omitted Days Target'`).Scan(&t2Days)
	if err != nil {
		t.Fatalf("failed querying Omitted Days Target: %v", err)
	}
	if t2Days != 90 {
		t.Fatalf("expected fallback to 90 days, got %d", t2Days)
	}

	// 3. Update target to 180 days
	fUpd := url.Values{}
	fUpd.Set("name", "Semi-Annual Card")
	fUpd.Set("amount", "250.00")
	fUpd.Set("recurrence_cadence", "CUSTOM_DAYS")
	fUpd.Set("recurrence_days", "180")
	fUpd.Set("non_spending_strategy", "ROLL_OVER")

	reqUpd := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets/"+t1ID+"/update", strings.NewReader(fUpd.Encode()))
	reqUpd.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	recUpd := httptest.NewRecorder()
	ctxUpd := e.NewContext(reqUpd, recUpd)
	ctxUpd.SetParamNames("budgetId", "id")
	ctxUpd.SetParamValues(budgetID, t1ID)
	ctxUpd.Set(ContextBudgetDBKey, tenantDB)
	ctxUpd.Set(ContextUserIDKey, userID)
	ctxUpd.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.UpdateTarget(ctxUpd); err != nil {
		t.Fatalf("UpdateTarget to 180 days failed: %v", err)
	}

	var updDays int
	var updName string
	err = tenantDB.QueryRow(`SELECT name, recurrence_days FROM targets_projection WHERE id = ?`, t1ID).Scan(&updName, &updDays)
	if err != nil {
		t.Fatalf("failed querying updated target: %v", err)
	}
	if updName != "Semi-Annual Card" || updDays != 180 {
		t.Fatalf("expected name Semi-Annual Card and 180 days, got name=%s, days=%d", updName, updDays)
	}
}




