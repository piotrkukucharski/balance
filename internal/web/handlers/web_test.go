package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/service"
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
	tokensHandler := NewTokensHandler(mainDB)
	profileHandler := NewProfileHandler(mainDB)
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

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
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

	// Link a bank account first so that bank sync has a linked account to pull
	now := time.Now().UTC().UnixMicro()
	userPartyID := domain.NewUUIDv7()
	acctID := domain.NewUUIDv7()
	intID := "mock_integration_" + userID
	tenantDB.ExecContext(context.Background(), `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, ?, ?)`, userPartyID, userID, now)
	tenantDB.ExecContext(context.Background(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, ?, ?)`, acctID, userPartyID, userID, now)
	tenantDB.ExecContext(context.Background(), `
		INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
		VALUES (?, ?, ?, 'mock_acc_1', ?, ?)`, domain.NewUUIDv7(), acctID, intID, now, now)

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
		{"Profile", profileHandler.ProfileView, "/budgets/" + budgetID + "/settings/profile"},
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
			expectedProfileLink := fmt.Sprintf(`/budgets/%s/settings/profile`, budgetID)
			if !strings.Contains(body, expectedProfileLink) {
				t.Fatalf("expected %s view to contain Profile sublink %s", vt.name, expectedProfileLink)
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

	t.Run("Registration disabled via ALLOW_REGISTRATION=false", func(t *testing.T) {
		t.Setenv("ALLOW_REGISTRATION", "false")

		// 1. Register page should return 403 Forbidden
		reqReg := httptest.NewRequest(http.MethodGet, "/register", nil)
		recReg := httptest.NewRecorder()
		cReg := e.NewContext(reqReg, recReg)
		err := authHandler.RegisterPage(cReg)
		if err == nil {
			t.Fatalf("expected error from RegisterPage when registration disabled")
		}
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || httpErr.Code != http.StatusForbidden {
			t.Fatalf("expected 403 HTTPError, got %v", err)
		}

		// 2. HandleRegister should return 403 Forbidden
		form := url.Values{}
		form.Set("email", "forbidden@example.com")
		form.Set("password", "Pass12345678")
		form.Set("display_name", "Forbidden User")
		reqPost := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
		reqPost.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		recPost := httptest.NewRecorder()
		cPost := e.NewContext(reqPost, recPost)
		errPost := authHandler.HandleRegister(cPost)
		if errPost == nil {
			t.Fatalf("expected error from HandleRegister when registration disabled")
		}
		httpErrPost, ok := errPost.(*echo.HTTPError)
		if !ok || httpErrPost.Code != http.StatusForbidden {
			t.Fatalf("expected 403 HTTPError, got %v", errPost)
		}

		// 3. Login page should not show registration link
		reqLogin := httptest.NewRequest(http.MethodGet, "/login", nil)
		recLogin := httptest.NewRecorder()
		cLogin := e.NewContext(reqLogin, recLogin)
		if err := authHandler.LoginPage(cLogin); err != nil {
			t.Fatalf("LoginPage failed: %v", err)
		}
		bodyLogin := recLogin.Body.String()
		if strings.Contains(bodyLogin, `<a href="/register"`) {
			t.Fatalf("login page should not contain registration link when disabled")
		}
		if !strings.Contains(bodyLogin, "Public registration is currently disabled") {
			t.Fatalf("login page should inform that registration is disabled")
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
	appService := service.NewAppService(mainDB, tenantMgr, clock.NewSystemClock(time.UTC))
	workspaceHandler.SetService(appService)
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
		tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
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

	// Test Clone Budget
	t.Run("Clone budget with selective resources", func(t *testing.T) {
		cForm := url.Values{}
		cForm.Set("name", "Web Cloned Budget")
		cForm.Set("include_members", "1")
		cForm.Set("include_accounts", "1")
		cForm.Set("include_buckets", "1")
		cForm.Set("include_targets", "1")
		cForm.Set("include_transactions", "0")
		cReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/clone", strings.NewReader(cForm.Encode()))
		cReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		cRec := httptest.NewRecorder()
		cCtx := e.NewContext(cReq, cRec)
		cCtx.SetParamNames("budgetId")
		cCtx.SetParamValues(budgetID)
		cCtx.Set(ContextUserIDKey, ownerID)
		cCtx.Set(ContextRoleKey, "OWNER")

		if err := workspaceHandler.CloneBudget(cCtx); err != nil {
			t.Fatalf("CloneBudget failed: %v", err)
		}
		if cRec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", cRec.Code)
		}
		loc := cRec.Header().Get("Location")
		if !strings.HasPrefix(loc, "/budgets/") || !strings.HasSuffix(loc, "/budget") {
			t.Fatalf("expected redirect to /budgets/:id/budget, got %s", loc)
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
	appService := service.NewAppService(mainDB, tenantMgr, clock.NewSystemClock(time.UTC))
	workspaceHandler.SetService(appService)
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

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
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
		t.Fatalf("expected target_modal to have recurrence interval input and toggleCustomDays function")
	}
	if !strings.Contains(body, "CUSTOM_DAYS") || !strings.Contains(body, "CUSTOM_MONTHS") || !strings.Contains(body, "CUSTOM_YEARS") {
		t.Fatalf("expected target_modal to contain CUSTOM_DAYS, CUSTOM_MONTHS, and CUSTOM_YEARS cadence options")
	}
	if !strings.Contains(body, "target_due_date") {
		t.Fatalf("expected target_modal to have anchor due date input")
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
	appService := service.NewAppService(mainDB, tenantMgr, clock.NewSystemClock(time.UTC))
	workspaceHandler.SetService(appService)
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

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
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

	// 1. Create target with CUSTOM_DAYS and recurrence_interval = 90
	f1 := url.Values{}
	f1.Set("bucket_id", bucketID)
	f1.Set("name", "Quarterly Card")
	f1.Set("amount", "125.00")
	f1.Set("currency", "PLN")
	f1.Set("recurrence_cadence", "CUSTOM_DAYS")
	f1.Set("recurrence_interval", "90")
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
	var t1Interval int
	var t1Cadence string
	err = tenantDB.QueryRow(`SELECT id, recurrence_cadence, recurrence_interval FROM targets_projection WHERE name = 'Quarterly Card'`).Scan(&t1ID, &t1Cadence, &t1Interval)
	if err != nil {
		t.Fatalf("failed querying Quarterly Card target: %v", err)
	}
	if t1Cadence != "CUSTOM_DAYS" || t1Interval != 90 {
		t.Fatalf("expected CUSTOM_DAYS with 90 interval, got cadence=%s, interval=%d", t1Cadence, t1Interval)
	}

	// 2. Create target with CUSTOM_DAYS but empty recurrence_interval (should gracefully default to 90 without CHECK constraint error)
	f2 := url.Values{}
	f2.Set("bucket_id", bucketID)
	f2.Set("name", "Omitted Days Target")
	f2.Set("amount", "50.00")
	f2.Set("currency", "PLN")
	f2.Set("recurrence_cadence", "CUSTOM_DAYS")
	f2.Set("recurrence_interval", "")
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
		t.Fatalf("CreateTarget with empty recurrence_interval failed: %v", err)
	}

	var t2Interval int
	err = tenantDB.QueryRow(`SELECT recurrence_interval FROM targets_projection WHERE name = 'Omitted Days Target'`).Scan(&t2Interval)
	if err != nil {
		t.Fatalf("failed querying Omitted Days Target: %v", err)
	}
	if t2Interval != 90 {
		t.Fatalf("expected fallback to 90 interval, got %d", t2Interval)
	}

	// 3. Update target to 180 interval
	fUpd := url.Values{}
	fUpd.Set("name", "Semi-Annual Card")
	fUpd.Set("amount", "250.00")
	fUpd.Set("recurrence_cadence", "CUSTOM_DAYS")
	fUpd.Set("recurrence_interval", "180")
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
		t.Fatalf("UpdateTarget to 180 interval failed: %v", err)
	}

	var updInterval int
	var updName string
	err = tenantDB.QueryRow(`SELECT name, recurrence_interval FROM targets_projection WHERE id = ?`, t1ID).Scan(&updName, &updInterval)
	if err != nil {
		t.Fatalf("failed querying updated target: %v", err)
	}
	if updName != "Semi-Annual Card" || updInterval != 180 {
		t.Fatalf("expected name Semi-Annual Card and 180 interval, got name=%s, interval=%d", updName, updInterval)
	}

	// 4. Create target with CUSTOM_MONTHS and anchor due_date
	f3 := url.Values{}
	f3.Set("bucket_id", bucketID)
	f3.Set("name", "Quarterly Tuition")
	f3.Set("amount", "600.00")
	f3.Set("currency", "PLN")
	f3.Set("recurrence_cadence", "CUSTOM_MONTHS")
	f3.Set("recurrence_interval", "3")
	f3.Set("due_date", "2026-06-15")
	f3.Set("non_spending_strategy", "ROLL_OVER")

	req3 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(f3.Encode()))
	req3.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec3 := httptest.NewRecorder()
	ctx3 := e.NewContext(req3, rec3)
	ctx3.SetParamNames("budgetId")
	ctx3.SetParamValues(budgetID)
	ctx3.Set(ContextBudgetDBKey, tenantDB)
	ctx3.Set(ContextUserIDKey, userID)
	ctx3.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(ctx3); err != nil {
		t.Fatalf("CreateTarget (CUSTOM_MONTHS) failed: %v", err)
	}

	var t3Cadence string
	var t3Interval int
	var t3DueDate int64
	err = tenantDB.QueryRow(`SELECT recurrence_cadence, recurrence_interval, due_date FROM targets_projection WHERE name = 'Quarterly Tuition'`).Scan(&t3Cadence, &t3Interval, &t3DueDate)
	if err != nil {
		t.Fatalf("failed querying Quarterly Tuition: %v", err)
	}
	if t3Cadence != "CUSTOM_MONTHS" || t3Interval != 3 || t3DueDate == 0 {
		t.Fatalf("expected CUSTOM_MONTHS, interval 3, non-zero dueDate, got %s, %d, %d", t3Cadence, t3Interval, t3DueDate)
	}

	// 5. Create target with CUSTOM_YEARS and anchor due_date
	f4 := url.Values{}
	f4.Set("bucket_id", bucketID)
	f4.Set("name", "Biennial License")
	f4.Set("amount", "1000.00")
	f4.Set("currency", "PLN")
	f4.Set("recurrence_cadence", "CUSTOM_YEARS")
	f4.Set("recurrence_interval", "2")
	f4.Set("due_date", "2028-01-01")
	f4.Set("non_spending_strategy", "CAP_AND_HOLD")

	req4 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(f4.Encode()))
	req4.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec4 := httptest.NewRecorder()
	ctx4 := e.NewContext(req4, rec4)
	ctx4.SetParamNames("budgetId")
	ctx4.SetParamValues(budgetID)
	ctx4.Set(ContextBudgetDBKey, tenantDB)
	ctx4.Set(ContextUserIDKey, userID)
	ctx4.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(ctx4); err != nil {
		t.Fatalf("CreateTarget (CUSTOM_YEARS) failed: %v", err)
	}

	var t4Cadence string
	var t4Interval int
	err = tenantDB.QueryRow(`SELECT recurrence_cadence, recurrence_interval FROM targets_projection WHERE name = 'Biennial License'`).Scan(&t4Cadence, &t4Interval)
	if err != nil {
		t.Fatalf("failed querying Biennial License: %v", err)
	}
	if t4Cadence != "CUSTOM_YEARS" || t4Interval != 2 {
		t.Fatalf("expected CUSTOM_YEARS with interval 2, got %s / %d", t4Cadence, t4Interval)
	}

	// 6. CUSTOM_MONTHS without due date should fail validation
	f5 := url.Values{}
	f5.Set("bucket_id", bucketID)
	f5.Set("name", "Invalid Months Target")
	f5.Set("amount", "100.00")
	f5.Set("currency", "PLN")
	f5.Set("recurrence_cadence", "CUSTOM_MONTHS")
	f5.Set("recurrence_interval", "3")
	f5.Set("due_date", "")

	req5 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(f5.Encode()))
	req5.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec5 := httptest.NewRecorder()
	ctx5 := e.NewContext(req5, rec5)
	ctx5.SetParamNames("budgetId")
	ctx5.SetParamValues(budgetID)
	ctx5.Set(ContextBudgetDBKey, tenantDB)
	ctx5.Set(ContextUserIDKey, userID)
	ctx5.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(ctx5); err == nil {
		t.Fatalf("expected error creating CUSTOM_MONTHS target without due date, but got nil")
	}

	// 7. Create target with MONTHLY cadence and optional due_date
	f6 := url.Values{}
	f6.Set("bucket_id", bucketID)
	f6.Set("name", "Monthly Subscription with Anchor")
	f6.Set("amount", "49.99")
	f6.Set("currency", "PLN")
	f6.Set("recurrence_cadence", "MONTHLY")
	f6.Set("due_date", "2026-11-15")
	f6.Set("non_spending_strategy", "ROLL_OVER")

	req6 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(f6.Encode()))
	req6.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec6 := httptest.NewRecorder()
	ctx6 := e.NewContext(req6, rec6)
	ctx6.SetParamNames("budgetId")
	ctx6.SetParamValues(budgetID)
	ctx6.Set(ContextBudgetDBKey, tenantDB)
	ctx6.Set(ContextUserIDKey, userID)
	ctx6.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(ctx6); err != nil {
		t.Fatalf("CreateTarget (MONTHLY with due_date) failed: %v", err)
	}

	var t6Cadence string
	var t6DueDate int64
	err = tenantDB.QueryRow(`SELECT recurrence_cadence, due_date FROM targets_projection WHERE name = 'Monthly Subscription with Anchor'`).Scan(&t6Cadence, &t6DueDate)
	if err != nil {
		t.Fatalf("failed querying Monthly Subscription with Anchor: %v", err)
	}
	if t6Cadence != "MONTHLY" || t6DueDate == 0 {
		t.Fatalf("expected MONTHLY with non-zero due_date, got %s, %d", t6Cadence, t6DueDate)
	}

	// Verify rendered in BudgetView
	reqView := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget", nil)
	recView := httptest.NewRecorder()
	ctxView := e.NewContext(reqView, recView)
	ctxView.SetParamNames("budgetId")
	ctxView.SetParamValues(budgetID)
	ctxView.Set(ContextBudgetDBKey, tenantDB)
	ctxView.Set(ContextUserIDKey, userID)
	ctxView.Set(ContextRoleKey, "EDITOR")
	ctxView.Set("csrfToken", "test-token")

	if err := workspaceHandler.BudgetView(ctxView); err != nil {
		t.Fatalf("BudgetView failed: %v", err)
	}
	viewBody := recView.Body.String()
	if !strings.Contains(viewBody, "Due: 2026-11-15") {
		t.Fatalf("expected BudgetView to display due date for monthly target, but it was missing in output")
	}
}

func TestPartyMatchingRulesCRUD(t *testing.T) {
	tempDir := t.TempDir()
	mainPath := filepath.Join(tempDir, "main.db")
	mainDB, err := db.OpenDB(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatal(err)
	}

	budgetsDir := filepath.Join(tempDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantMgr.CloseAll()

	partiesHandler := NewPartiesHandler(mainDB, tenantMgr)
	e := echo.New()

	userID := domain.NewUUIDv7()
	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, err = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'user@example.com', 'hash', 'User', ?, ?)`, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'Rules Budget', 'PLN', 'Europe/Warsaw', ?, ?, ?)`, budgetID, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Create a party
	pForm := url.Values{}
	pForm.Set("name", "Żabka Polska")
	pForm.Set("party_type", "ORGANIZATION")

	pReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/parties", strings.NewReader(pForm.Encode()))
	pReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	pRec := httptest.NewRecorder()
	pCtx := e.NewContext(pReq, pRec)
	pCtx.SetParamNames("budgetId")
	pCtx.SetParamValues(budgetID)
	pCtx.Set(ContextBudgetDBKey, tenantDB)
	pCtx.Set(ContextUserIDKey, userID)
	pCtx.Set("correlationID", domain.NewUUIDv7())

	if err := partiesHandler.CreateParty(pCtx); err != nil {
		t.Fatalf("CreateParty failed: %v", err)
	}

	var partyID string
	err = tenantDB.QueryRow(`SELECT id FROM parties_current WHERE name = 'Żabka Polska'`).Scan(&partyID)
	if err != nil {
		t.Fatalf("failed finding created party: %v", err)
	}

	// Check party in PartiesView
	pvReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/parties", nil)
	pvRec := httptest.NewRecorder()
	pvCtx := e.NewContext(pvReq, pvRec)
	pvCtx.SetParamNames("budgetId")
	pvCtx.SetParamValues(budgetID)
	pvCtx.Set(ContextBudgetDBKey, tenantDB)
	pvCtx.Set(ContextUserIDKey, userID)
	pvCtx.Set("csrfToken", "test-token")

	if err := partiesHandler.PartiesView(pvCtx); err != nil {
		t.Fatalf("PartiesView failed: %v", err)
	}
	pvBody := pvRec.Body.String()
	if !strings.Contains(pvBody, "Żabka Polska") || !strings.Contains(pvBody, "/budgets/"+budgetID+"/parties/"+partyID+"/rules") {
		t.Fatalf("PartiesView body missing party or rules link: %s", pvBody)
	}

	// Create a bucket for testing optional default bucket
	bucketID, err := budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Daily",
		Name:          "Groceries",
		AgentID:       userID,
		CorrelationID: domain.NewUUIDv7(),
	})
	if err != nil {
		t.Fatalf("failed creating bucket: %v", err)
	}

	// 2. View party rules (empty state)
	prvReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/parties/"+partyID+"/rules", nil)
	prvRec := httptest.NewRecorder()
	prvCtx := e.NewContext(prvReq, prvRec)
	prvCtx.SetParamNames("budgetId", "partyId")
	prvCtx.SetParamValues(budgetID, partyID)
	prvCtx.Set(ContextBudgetDBKey, tenantDB)
	prvCtx.Set(ContextUserIDKey, userID)
	prvCtx.Set("csrfToken", "test-token")

	if err := partiesHandler.PartyRulesView(prvCtx); err != nil {
		t.Fatalf("PartyRulesView failed: %v", err)
	}
	if !strings.Contains(prvRec.Body.String(), "No auto-matching rules configured") {
		t.Fatalf("expected empty rules message in PartyRulesView")
	}

	// 3. Create a matching rule
	rForm := url.Values{}
	rForm.Set("name", "Match Zabka Merchant")
	rForm.Set("match_field", "RAW_PAYEE")
	rForm.Set("match_operator", "CONTAINS")
	rForm.Set("match_pattern", "ZABKA")
	rForm.Set("default_bucket_id", bucketID)
	rForm.Set("priority", "50")

	rReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/parties/"+partyID+"/rules", strings.NewReader(rForm.Encode()))
	rReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rRec := httptest.NewRecorder()
	rCtx := e.NewContext(rReq, rRec)
	rCtx.SetParamNames("budgetId", "partyId")
	rCtx.SetParamValues(budgetID, partyID)
	rCtx.Set(ContextBudgetDBKey, tenantDB)
	rCtx.Set(ContextUserIDKey, userID)
	rCtx.Set("correlationID", domain.NewUUIDv7())

	if err := partiesHandler.CreatePartyRule(rCtx); err != nil {
		t.Fatalf("CreatePartyRule failed: %v", err)
	}
	if rRec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect 303, got %d", rRec.Code)
	}

	var ruleID, rName, rField, rOp, rPat, rBucket string
	var rPrio int
	err = tenantDB.QueryRow(`
		SELECT id, name, priority, match_field, match_operator, match_pattern, default_bucket_id
		FROM matching_rules_current
		WHERE name = 'Match Zabka Merchant'`).Scan(&ruleID, &rName, &rPrio, &rField, &rOp, &rPat, &rBucket)
	if err != nil {
		t.Fatalf("failed finding created matching rule in projection: %v", err)
	}
	if rPrio != 50 || rField != "RAW_PAYEE" || rOp != "CONTAINS" || rPat != "ZABKA" || rBucket != bucketID {
		t.Fatalf("unexpected rule fields: %v %d %s %s %s %s", ruleID, rPrio, rField, rOp, rPat, rBucket)
	}

	// Check rule rendered in PartyRulesView
	prvRec2 := httptest.NewRecorder()
	prvCtx2 := e.NewContext(prvReq, prvRec2)
	prvCtx2.SetParamNames("budgetId", "partyId")
	prvCtx2.SetParamValues(budgetID, partyID)
	prvCtx2.Set(ContextBudgetDBKey, tenantDB)
	prvCtx2.Set(ContextUserIDKey, userID)
	prvCtx2.Set("csrfToken", "test-token")

	if err := partiesHandler.PartyRulesView(prvCtx2); err != nil {
		t.Fatalf("PartyRulesView 2 failed: %v", err)
	}
	prvBody2 := prvRec2.Body.String()
	if !strings.Contains(prvBody2, "Match Zabka Merchant") || !strings.Contains(prvBody2, "Groceries") {
		t.Fatalf("PartyRulesView missing rule details: %s", prvBody2)
	}

	// 4. Test invalid REGEX fails validation
	badRegexForm := url.Values{}
	badRegexForm.Set("name", "Bad Regex Rule")
	badRegexForm.Set("match_field", "RAW_PAYEE")
	badRegexForm.Set("match_operator", "REGEX")
	badRegexForm.Set("match_pattern", "[invalid(")

	badReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/parties/"+partyID+"/rules", strings.NewReader(badRegexForm.Encode()))
	badReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	badRec := httptest.NewRecorder()
	badCtx := e.NewContext(badReq, badRec)
	badCtx.SetParamNames("budgetId", "partyId")
	badCtx.SetParamValues(budgetID, partyID)
	badCtx.Set(ContextBudgetDBKey, tenantDB)
	badCtx.Set(ContextUserIDKey, userID)
	badCtx.Set("correlationID", domain.NewUUIDv7())

	if err := partiesHandler.CreatePartyRule(badCtx); err == nil {
		t.Fatalf("expected error creating rule with invalid regex, got nil")
	}

	// 5. Delete matching rule
	dReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/parties/"+partyID+"/rules/"+ruleID+"/delete", nil)
	dRec := httptest.NewRecorder()
	dCtx := e.NewContext(dReq, dRec)
	dCtx.SetParamNames("budgetId", "partyId", "ruleId")
	dCtx.SetParamValues(budgetID, partyID, ruleID)
	dCtx.Set(ContextBudgetDBKey, tenantDB)
	dCtx.Set(ContextUserIDKey, userID)

	if err := partiesHandler.DeletePartyRule(dCtx); err != nil {
		t.Fatalf("DeletePartyRule failed: %v", err)
	}

	var countCurr, countHist int
	_ = tenantDB.QueryRow(`SELECT count(*) FROM matching_rules_current WHERE id = ?`, ruleID).Scan(&countCurr)
	_ = tenantDB.QueryRow(`SELECT count(*) FROM matching_rules WHERE id = ?`, ruleID).Scan(&countHist)
	if countCurr != 0 || countHist != 0 {
		t.Fatalf("expected rule to be deleted from both tables, got curr=%d, hist=%d", countCurr, countHist)
	}
}

func TestTargetCurrencySelectionAndImmutability(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_target_currency_test_*")
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

	userID := domain.NewUUIDv7()
	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	// 1. Setup user and budget
	_, err = mainDB.Exec(`
		INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		VALUES (?, 'traveler@example.com', 'hash', 'Traveler', ?, ?)`, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mainDB.Exec(`
		INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, 'Vacation Budget', 'PLN', 'UTC', ?, ?, ?)`, budgetID, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mainDB.Exec(`
		INSERT INTO budget_members (budget_id, user_id, role, joined_at)
		VALUES (?, ?, 'OWNER', ?)`, budgetID, userID, now)
	if err != nil {
		t.Fatal(err)
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	// Create bucket
	bucketID, err := budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Travel",
		Name:          "Trips",
		AgentID:       userID,
		CorrelationID: domain.NewUUIDv7(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 2. Initial BudgetView: only default currency (PLN)
	viewReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget", nil)
	viewRec := httptest.NewRecorder()
	viewCtx := e.NewContext(viewReq, viewRec)
	viewCtx.SetParamNames("budgetId")
	viewCtx.SetParamValues(budgetID)
	viewCtx.Set(ContextBudgetDBKey, tenantDB)
	viewCtx.Set(ContextUserIDKey, userID)
	viewCtx.Set(ContextRoleKey, "OWNER")
	viewCtx.Set("csrfToken", "test-csrf")

	if err := workspaceHandler.BudgetView(viewCtx); err != nil {
		t.Fatalf("BudgetView failed: %v", err)
	}
	html := viewRec.Body.String()
	if !strings.Contains(html, `<select id="target_currency" name="currency"`) {
		t.Fatalf("expected select target_currency in HTML, got:\n%s", html)
	}
	if !strings.Contains(html, `<option value="PLN" selected>PLN</option>`) {
		t.Fatalf("expected PLN option selected in target_currency select")
	}
	if !strings.Contains(html, `(Currency cannot be changed after creation)`) {
		t.Fatalf("expected currency immutability helper text in modal")
	}

	// 3. Add account with EUR and account_currency_balances with USD
	userPartyID := domain.NewUUIDv7()
	acctID := domain.NewUUIDv7()
	_, err = tenantDB.Exec(`
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Self', 'INDIVIDUAL', 1, ?, ?)`, userPartyID, userID, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tenantDB.Exec(`
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, 'Euro Account', 'CHECKING', 'EUR', 0, ?, ?)`, acctID, userPartyID, userID, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tenantDB.Exec(`
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, 'USD', 50000, ?)`, acctID, now)
	if err != nil {
		t.Fatal(err)
	}

	// BudgetView again: should now include EUR, PLN, USD
	viewRec2 := httptest.NewRecorder()
	viewCtx2 := e.NewContext(viewReq, viewRec2)
	viewCtx2.SetParamNames("budgetId")
	viewCtx2.SetParamValues(budgetID)
	viewCtx2.Set(ContextBudgetDBKey, tenantDB)
	viewCtx2.Set(ContextUserIDKey, userID)
	viewCtx2.Set(ContextRoleKey, "OWNER")
	viewCtx2.Set("csrfToken", "test-csrf")

	if err := workspaceHandler.BudgetView(viewCtx2); err != nil {
		t.Fatalf("BudgetView failed: %v", err)
	}
	html2 := viewRec2.Body.String()
	if !strings.Contains(html2, `<option value="EUR">EUR</option>`) ||
		!strings.Contains(html2, `<option value="PLN" selected>PLN</option>`) ||
		!strings.Contains(html2, `<option value="USD">USD</option>`) {
		t.Fatalf("expected EUR, PLN, USD in select target_currency, got:\n%s", html2)
	}

	// 4. Create target in EUR (available currency)
	createForm := url.Values{}
	createForm.Set("bucket_id", bucketID)
	createForm.Set("name", "Hotel Rome")
	createForm.Set("target_type", "EXPENSE")
	createForm.Set("priority", "2")
	createForm.Set("currency", "EUR")
	createForm.Set("recurrence_cadence", "MONTHLY")
	createForm.Set("amount", "250.00")

	createReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(createForm.Encode()))
	createReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	createCtx.SetParamNames("budgetId")
	createCtx.SetParamValues(budgetID)
	createCtx.Set(ContextBudgetDBKey, tenantDB)
	createCtx.Set(ContextUserIDKey, userID)
	createCtx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateTarget(createCtx); err != nil {
		t.Fatalf("CreateTarget failed: %v", err)
	}
	if createRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", createRec.Code)
	}

	var targetID, targetCurr, targetName string
	var amountMinor int64
	err = tenantDB.QueryRow(`
		SELECT id, name, currency, amount_minor
		FROM targets_projection
		WHERE name = 'Hotel Rome' AND is_archived = 0`).Scan(&targetID, &targetName, &targetCurr, &amountMinor)
	if err != nil {
		t.Fatalf("failed finding created target: %v", err)
	}
	if targetCurr != "EUR" {
		t.Fatalf("expected target currency EUR, got %s", targetCurr)
	}
	if amountMinor != 25000 {
		t.Fatalf("expected amount minor 25000, got %d", amountMinor)
	}

	// 5. Reject creating target in unsupported currency (JPY)
	badForm := url.Values{}
	badForm.Set("bucket_id", bucketID)
	badForm.Set("name", "Tokyo Tickets")
	badForm.Set("currency", "JPY")
	badForm.Set("amount", "10000.00")

	badReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets", strings.NewReader(badForm.Encode()))
	badReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	badRec := httptest.NewRecorder()
	badCtx := e.NewContext(badReq, badRec)
	badCtx.SetParamNames("budgetId")
	badCtx.SetParamValues(budgetID)
	badCtx.Set(ContextBudgetDBKey, tenantDB)
	badCtx.Set(ContextUserIDKey, userID)
	badCtx.Set("correlationID", domain.NewUUIDv7())

	err = workspaceHandler.CreateTarget(badCtx)
	if err == nil {
		t.Fatalf("expected error creating target with unsupported currency JPY, got nil")
	}
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request HTTPError, got %v", err)
	}

	// 6. Update target: attempt to change currency to USD
	updateForm := url.Values{}
	updateForm.Set("name", "Hotel Rome Luxury")
	updateForm.Set("target_type", "EXPENSE")
	updateForm.Set("priority", "3")
	updateForm.Set("amount", "350.00")
	updateForm.Set("recurrence_cadence", "MONTHLY")
	updateForm.Set("currency", "USD") // Attempted tamper

	updateReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/targets/"+targetID+"/update", strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	updateRec := httptest.NewRecorder()
	updateCtx := e.NewContext(updateReq, updateRec)
	updateCtx.SetParamNames("budgetId", "id")
	updateCtx.SetParamValues(budgetID, targetID)
	updateCtx.Set(ContextBudgetDBKey, tenantDB)
	updateCtx.Set(ContextUserIDKey, userID)
	updateCtx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.UpdateTarget(updateCtx); err != nil {
		t.Fatalf("UpdateTarget failed: %v", err)
	}

	var updatedCurr, updatedName string
	var updatedAmt int64
	err = tenantDB.QueryRow(`
		SELECT name, currency, amount_minor
		FROM targets_projection
		WHERE id = ?`, targetID).Scan(&updatedName, &updatedCurr, &updatedAmt)
	if err != nil {
		t.Fatalf("failed querying updated target: %v", err)
	}
	if updatedName != "Hotel Rome Luxury" {
		t.Fatalf("expected updated name 'Hotel Rome Luxury', got '%s'", updatedName)
	}
	if updatedCurr != "EUR" {
		t.Fatalf("expected target currency to remain immutable as 'EUR', got '%s'", updatedCurr)
	}
	if updatedAmt != 35000 {
		t.Fatalf("expected amount minor 35000, got %d", updatedAmt)
	}
}

func TestBucketStructureAssistantFlow(t *testing.T) {
	logger.BindTestLogger(t)
	tmpDir, err := os.MkdirTemp("", "balance_structure_test_*")
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
	appService := service.NewAppService(mainDB, tenantMgr, clock.NewSystemClock(time.UTC))
	workspaceHandler.SetService(appService)
	e := echo.New()

	userID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, _ = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'structure@example.com', 'h', 'User', ?, ?)`, userID, now, now)

	budgetID := domain.NewUUIDv7()
	_, err = mainDB.Exec(`
		INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, 'Assistant Budget', 'PLN', 'Europe/Warsaw', ?, ?, ?)`, budgetID, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mainDB.Exec(`
		INSERT INTO budget_members (budget_id, user_id, role, joined_at)
		VALUES (?, ?, 'OWNER', ?)`, budgetID, userID, now)
	if err != nil {
		t.Fatal(err)
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatalf("failed getting tenant db: %v", err)
	}

	// 1. Pre-create a bucket in an existing group to test conflict detection and group prioritization
	_, err = budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Koszty stałe",
		Name:          "Czynsz",
		AgentID:       userID,
		CorrelationID: domain.NewUUIDv7(),
	})
	if err != nil {
		t.Fatalf("failed pre-creating bucket: %v", err)
	}

	// 2. Test GenerateStructurePreview endpoint
	genBody := `{"prompt":"Czynsz: 2500 PLN\nJedzenie: 1200 PLN\nOszczędności: 500 PLN","currency":"PLN"}`
	genReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/structure/generate-ai-preview", strings.NewReader(genBody))
	genReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	genRec := httptest.NewRecorder()
	genCtx := e.NewContext(genReq, genRec)
	genCtx.SetParamNames("budgetId")
	genCtx.SetParamValues(budgetID)
	genCtx.Set(ContextBudgetDBKey, tenantDB)
	genCtx.Set(ContextUserIDKey, userID)
	genCtx.Set(ContextRoleKey, "OWNER")
	genCtx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.GenerateStructurePreview(genCtx); err != nil {
		t.Fatalf("GenerateStructurePreview failed: %v", err)
	}
	if genRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from generate preview, got %d: %s", genRec.Code, genRec.Body.String())
	}

	var previewData ai.BudgetStructure
	if err := json.Unmarshal(genRec.Body.Bytes(), &previewData); err != nil {
		t.Fatalf("failed unmarshaling preview response: %v", err)
	}

	// Verify existing groups were enriched
	if len(previewData.ExistingGroups) == 0 || previewData.ExistingGroups[0] != "Koszty stałe" {
		t.Errorf("expected ExistingGroups to contain 'Koszty stałe', got: %v", previewData.ExistingGroups)
	}

	// Verify conflict detection marked Czynsz as existing
	var foundCzynsz bool
	for _, g := range previewData.Groups {
		for _, b := range g.Buckets {
			if strings.EqualFold(b.Name, "Czynsz") {
				foundCzynsz = true
				if !b.IsExisting {
					t.Errorf("expected Czynsz bucket to have IsExisting=true")
				}
				if b.ExistingGroup != "Koszty stałe" {
					t.Errorf("expected Czynsz ExistingGroup='Koszty stałe', got %q", b.ExistingGroup)
				}
			}
		}
	}
	if !foundCzynsz {
		t.Errorf("expected Czynsz in preview buckets")
	}

	// 3. Test ApplyStructure with customized priority, group, and conflict resolution
	applyPayload := `{
		"groups": [
			{
				"group_name": "Opłaty i Rachunki",
				"buckets": [
					{
						"name": "Czynsz",
						"conflict_resolution": "MERGE",
						"targets": [
							{
								"name": "Opłata czynszowa",
								"target_type": "EXPENSE",
								"priority": 3,
								"amount_minor": 250000,
								"currency": "PLN",
								"recurrence_cadence": "MONTHLY",
								"non_spending_strategy": "RETURN_TO_POOL"
							}
						]
					},
					{
						"name": "Jedzenie",
						"conflict_resolution": "MERGE",
						"targets": [
							{
								"name": "Supermarket",
								"target_type": "EXPENSE",
								"priority": 2,
								"amount_minor": 120000,
								"currency": "PLN",
								"recurrence_cadence": "MONTHLY",
								"non_spending_strategy": "RETURN_TO_POOL"
							}
						]
					}
				]
			}
		]
	}`

	applyReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/structure/apply", strings.NewReader(applyPayload))
	applyReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	applyRec := httptest.NewRecorder()
	applyCtx := e.NewContext(applyReq, applyRec)
	applyCtx.SetParamNames("budgetId")
	applyCtx.SetParamValues(budgetID)
	applyCtx.Set(ContextBudgetDBKey, tenantDB)
	applyCtx.Set(ContextUserIDKey, userID)
	applyCtx.Set(ContextRoleKey, "OWNER")
	applyCtx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.ApplyStructure(applyCtx); err != nil {
		t.Fatalf("ApplyStructure failed: %v", err)
	}
	if applyRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from apply structure, got %d: %s", applyRec.Code, applyRec.Body.String())
	}

	// Verify Czynsz bucket group was updated to "Opłaty i Rachunki"
	var czynszGroup string
	err = tenantDB.QueryRow(`SELECT group_name FROM buckets_projection WHERE name = 'Czynsz'`).Scan(&czynszGroup)
	if err != nil || czynszGroup != "Opłaty i Rachunki" {
		t.Errorf("expected Czynsz group to be updated to 'Opłaty i Rachunki', got %q (err: %v)", czynszGroup, err)
	}

	// Verify target was created with priority 3
	var prio3 int
	err = tenantDB.QueryRow(`SELECT priority FROM targets_projection WHERE name = 'Opłata czynszowa'`).Scan(&prio3)
	if err != nil || prio3 != 3 {
		t.Errorf("expected priority 3 for 'Opłata czynszowa', got %d (err: %v)", prio3, err)
	}
}

func TestWorkspaceTimeHorizonsAndDynamicCalculation(t *testing.T) {
	tempDir := t.TempDir()
	mainPath := filepath.Join(tempDir, "main.db")
	mainDB, err := db.OpenDB(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatal(err)
	}

	budgetsDir := filepath.Join(tempDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantMgr.CloseAll()

	reportsHandler := NewReportsHandler(mainDB, tenantMgr)
	workspaceHandler := NewWorkspaceHandler(mainDB, tenantMgr, reportsHandler)

	appService := service.NewAppService(mainDB, tenantMgr, clock.NewSystemClock(time.UTC))
	workspaceHandler.SetService(appService)

	e := echo.New()
	userID := domain.NewUUIDv7()
	_, err = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, ?, 'hash', 'Test User', ?, ?)`,
		userID, "user@example.com", time.Now().Unix(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}

	// Create budget
	form := url.Values{}
	form.Set("name", "Horizon Test Budget")
	form.Set("default_currency", "PLN")
	form.Set("timezone", "UTC")

	bReq := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader(form.Encode()))
	bReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	bRec := httptest.NewRecorder()
	bCtx := e.NewContext(bReq, bRec)
	bCtx.Set(ContextUserIDKey, userID)
	bCtx.Set("correlationID", domain.NewUUIDv7())

	if err := workspaceHandler.CreateBudget(bCtx); err != nil {
		t.Fatalf("CreateBudget failed: %v", err)
	}

	// Get budget ID from redirect
	var budgetID string
	loc := bRec.Header().Get("Location")
	parts := strings.Split(loc, "/")
	if len(parts) >= 3 {
		budgetID = parts[2]
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	// Create bucket
	bucketID := domain.NewUUIDv7()
	nowEpoch := time.Now().Unix()
	_, err = tenantDB.Exec(`INSERT INTO buckets_projection (id, group_name, name, version, updated_at) VALUES (?, 'Goals', 'Vacation', 1, ?)`, bucketID, nowEpoch)
	if err != nil {
		t.Fatal(err)
	}

	// Create one-off target due in 360 days: amount 120 zł (12000 minor)
	targetID := domain.NewUUIDv7()
	now := time.Now().UTC()
	dueDate := now.AddDate(0, 0, 360)
	dueDateMicro := dueDate.UnixMicro()

	_, err = tenantDB.Exec(`INSERT INTO targets_projection (
		id, bucket_id, name, target_type, priority, sort_order, amount_minor, currency,
		recurrence_cadence, due_date, non_spending_strategy, current_funded_minor, version, updated_at
	) VALUES (?, ?, 'Holiday Trip', 'SAVINGS', 2, 0, 12000, 'PLN', 'ONE_OFF', ?, 'ROLL_OVER', 0, 1, ?)`,
		targetID, bucketID, dueDateMicro, nowEpoch)
	if err != nil {
		t.Fatal(err)
	}

	testHorizons := []struct {
		horizonParam   string
		expectedTitle  string
		expectedActive string
	}{
		{"monthly", "Monthly", "horizon=monthly&filter=all\" class=\"btn btn-xs join-item btn-primary\""},
		{"30d", "30d", "horizon=30d&filter=all\" class=\"btn btn-xs join-item btn-primary\""},
		{"90d", "90d", "horizon=90d&filter=all\" class=\"btn btn-xs join-item btn-primary\""},
		{"annual", "Annual", "horizon=annual&filter=all\" class=\"btn btn-xs join-item btn-primary\""},
		{"365d", "365d", "horizon=365d&filter=all\" class=\"btn btn-xs join-item btn-primary\""},
	}

	for _, th := range testHorizons {
		t.Run("Horizon_"+th.horizonParam, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget?horizon="+th.horizonParam, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("budgetId")
			c.SetParamValues(budgetID)
			c.Set(ContextBudgetDBKey, tenantDB)
			c.Set(ContextUserIDKey, userID)
			c.Set(ContextRoleKey, "OWNER")
			c.Set("csrfToken", "test-token")

			if err := workspaceHandler.BudgetView(c); err != nil {
				t.Fatalf("BudgetView failed for %s: %v", th.horizonParam, err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK for %s, got %d", th.horizonParam, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "Needed ("+th.expectedTitle+")") {
				t.Errorf("expected Needed (%s) in stats banner, but body did not contain it", th.expectedTitle)
			}
			if !strings.Contains(body, th.expectedActive) {
				t.Errorf("expected active button for %s, but pattern %q not found in body", th.horizonParam, th.expectedActive)
			}
		})
	}
}

func TestReadyToAssignMultiCurrencyLayout(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_rta_test_*")
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

	// 1. Register User
	form := url.Values{}
	form.Set("email", "multi@example.com")
	form.Set("password", "SecureP@ss123")
	form.Set("display_name", "Jan Multi")

	reqReg := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	reqReg.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	recReg := httptest.NewRecorder()
	cReg := e.NewContext(reqReg, recReg)

	if err := authHandler.HandleRegister(cReg); err != nil {
		t.Fatalf("HandleRegister failed: %v", err)
	}

	var userID, csrfToken string
	if err := mainDB.QueryRow("SELECT user_id, csrf_token FROM sessions LIMIT 1").Scan(&userID, &csrfToken); err != nil {
		t.Fatal(err)
	}

	// 2. Create Budget
	bForm := url.Values{}
	bForm.Set("name", "Multi Currency Budget")
	bForm.Set("default_currency", "PLN")
	bForm.Set("timezone", "UTC")
	bForm.Set("csrf_token", csrfToken)

	reqB := httptest.NewRequest(http.MethodPost, "/budgets", strings.NewReader(bForm.Encode()))
	reqB.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	recB := httptest.NewRecorder()
	cB := e.NewContext(reqB, recB)
	cB.Set(ContextUserIDKey, userID)
	cB.Set("csrfToken", csrfToken)

	if err := workspaceHandler.CreateBudget(cB); err != nil {
		t.Fatalf("CreateBudget failed: %v", err)
	}

	var budgetID string
	if err := mainDB.QueryRow("SELECT id FROM budgets LIMIT 1").Scan(&budgetID); err != nil {
		t.Fatal(err)
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Single-currency budget verification
	req1 := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget", nil)
	rec1 := httptest.NewRecorder()
	c1 := e.NewContext(req1, rec1)
	c1.SetParamNames("budgetId")
	c1.SetParamValues(budgetID)
	c1.Set(ContextBudgetDBKey, tenantDB)
	c1.Set(ContextUserIDKey, userID)
	c1.Set(ContextRoleKey, "OWNER")
	c1.Set("csrfToken", csrfToken)

	if err := workspaceHandler.BudgetView(c1); err != nil {
		t.Fatalf("BudgetView failed for 1 currency: %v", err)
	}
	body1 := rec1.Body.String()
	if !strings.Contains(body1, "Ready to Assign") {
		t.Errorf("Ready to Assign card missing")
	}
	if strings.Contains(body1, "+") && strings.Contains(body1, "walut") {
		t.Errorf("Unexpected multi-currency dropdown in single currency budget")
	}

	// 4. Add multiple accounts with distinct currencies: EUR, USD, GBP, CHF, CAD (5 non-default currencies)
	now := time.Now().UTC().UnixMicro()
	userPartyID := domain.NewUUIDv7()
	_, err = tenantDB.Exec(`
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, notes, agent_id, updated_at)
		VALUES (?, 1, 'Jan Multi Personal', 'PERSON', 1, NULL, ?, ?)`,
		userPartyID, userID, now,
	)
	if err != nil {
		t.Fatalf("failed inserting party: %v", err)
	}

	currencies := []struct {
		name string
		curr string
		bal  int64
	}{
		{"Konto Walutowe EUR", "EUR", 150000},
		{"Konto Walutowe USD", "USD", 250000},
		{"Konto Walutowe GBP", "GBP", 80000},
		{"Konto Walutowe CHF", "CHF", 95000},
		{"Konto Walutowe CAD", "CAD", 60000},
	}

	for _, cItem := range currencies {
		accID := domain.NewUUIDv7()
		_, err = tenantDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', ?, 0, ?, ?)`,
			accID, userPartyID, cItem.name, cItem.curr, userID, now,
		)
		if err != nil {
			t.Fatalf("failed inserting account %s: %v", cItem.name, err)
		}
		_, err = tenantDB.Exec(`
			INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
			VALUES (?, ?, ?, ?)`,
			accID, cItem.curr, cItem.bal, now,
		)
		if err != nil {
			t.Fatalf("failed inserting balance %s: %v", cItem.curr, err)
		}
	}

	// 5. Multi-currency verification (should show first 3 sorted and +2 walut in dropdown)
	req2 := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	c2.SetParamNames("budgetId")
	c2.SetParamValues(budgetID)
	c2.Set(ContextBudgetDBKey, tenantDB)
	c2.Set(ContextUserIDKey, userID)
	c2.Set(ContextRoleKey, "OWNER")
	c2.Set("csrfToken", csrfToken)

	if err := workspaceHandler.BudgetView(c2); err != nil {
		t.Fatalf("BudgetView failed for multi-currency: %v", err)
	}
	body2 := rec2.Body.String()

	// Sorted: CAD, CHF, EUR (visible directly), GBP, USD (in dropdown)
	if !strings.Contains(body2, "CAD") || !strings.Contains(body2, "CHF") || !strings.Contains(body2, "EUR") {
		t.Errorf("Expected CAD, CHF, EUR to be present in body")
	}
	if !strings.Contains(body2, "+2 walut") {
		t.Errorf("Expected dropdown badge '+2 walut' for 5 additional currencies, body had:\n%s", body2)
	}
	if !strings.Contains(body2, "Pozostałe waluty") {
		t.Errorf("Expected dropdown header 'Pozostałe waluty'")
	}
	if !strings.Contains(body2, "GBP") || !strings.Contains(body2, "USD") {
		t.Errorf("Expected GBP and USD inside dropdown")
	}
}

func TestGroupRenamingAndMovingEndpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "balance_groups_test_*")
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

	userID := domain.NewUUIDv7()
	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, err = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'guser@example.com', 'hash', 'Group User', ?, ?)`, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'Group Budget', 'PLN', 'UTC', ?, ?, ?)`, budgetID, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainDB.Exec(`INSERT INTO budget_members (budget_id, user_id, role, joined_at) VALUES (?, ?, 'OWNER', ?)`, budgetID, userID, now)
	if err != nil {
		t.Fatal(err)
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatalf("failed getting tenant db: %v", err)
	}

	// Create two buckets in different groups
	_, err = budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Needs",
		Name:          "Rent",
		AgentID:       userID,
		CorrelationID: "c1",
	})
	if err != nil {
		t.Fatalf("failed creating bucket in Needs: %v", err)
	}

	_, err = budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Wants",
		Name:          "Dining Out",
		AgentID:       userID,
		CorrelationID: "c2",
	})
	if err != nil {
		t.Fatalf("failed creating bucket in Wants: %v", err)
	}

	// 1. Move "Wants" up
	formMove := make(url.Values)
	formMove.Set("group_name", "Wants")
	formMove.Set("csrf_token", "token123")
	moveReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/groups/move?dir=up", strings.NewReader(formMove.Encode()))
	moveReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	moveRec := httptest.NewRecorder()
	moveCtx := e.NewContext(moveReq, moveRec)
	moveCtx.SetParamNames("budgetId")
	moveCtx.SetParamValues(budgetID)
	moveCtx.Set(ContextBudgetDBKey, tenantDB)
	moveCtx.Set(ContextUserIDKey, userID)
	moveCtx.Set(ContextRoleKey, "EDITOR")
	moveCtx.Set("correlationID", "corr-move")

	if err := workspaceHandler.MoveGroup(moveCtx); err != nil {
		t.Fatalf("MoveGroup failed: %v", err)
	}
	if moveRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", moveRec.Code)
	}

	// Verify order in ListBucketGroups
	groups, err := budget.ListBucketGroups(context.Background(), tenantDB)
	if err != nil || len(groups) != 2 {
		t.Fatalf("unexpected groups: %v (err: %v)", groups, err)
	}
	if groups[0].Name != "Wants" || groups[1].Name != "Needs" {
		t.Errorf("expected Wants to be first after move up, got: %s, %s", groups[0].Name, groups[1].Name)
	}

	// 2. Rename "Wants" to "Lifestyle & Fun"
	formRename := make(url.Values)
	formRename.Set("old_name", "Wants")
	formRename.Set("new_name", "Lifestyle & Fun")
	formRename.Set("csrf_token", "token123")
	renReq := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/groups/rename", strings.NewReader(formRename.Encode()))
	renReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	renRec := httptest.NewRecorder()
	renCtx := e.NewContext(renReq, renRec)
	renCtx.SetParamNames("budgetId")
	renCtx.SetParamValues(budgetID)
	renCtx.Set(ContextBudgetDBKey, tenantDB)
	renCtx.Set(ContextUserIDKey, userID)
	renCtx.Set(ContextRoleKey, "EDITOR")
	renCtx.Set("correlationID", "corr-rename")

	if err := workspaceHandler.RenameGroup(renCtx); err != nil {
		t.Fatalf("RenameGroup failed: %v", err)
	}
	if renRec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other from rename, got %d", renRec.Code)
	}

	// Verify bucket updated
	var bucketGroup string
	err = tenantDB.QueryRow(`SELECT group_name FROM buckets_projection WHERE name = 'Dining Out'`).Scan(&bucketGroup)
	if err != nil || bucketGroup != "Lifestyle & Fun" {
		t.Errorf("expected Dining Out bucket group to be 'Lifestyle & Fun', got %q (err: %v)", bucketGroup, err)
	}

	// 3. Verify BudgetView renders the renamed group in first position
	bvReq := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget", nil)
	bvRec := httptest.NewRecorder()
	bvCtx := e.NewContext(bvReq, bvRec)
	bvCtx.SetParamNames("budgetId")
	bvCtx.SetParamValues(budgetID)
	bvCtx.Set(ContextBudgetDBKey, tenantDB)
	bvCtx.Set(ContextUserIDKey, userID)
	bvCtx.Set(ContextRoleKey, "OWNER")
	bvCtx.Set("csrfToken", "token123")

	if err := workspaceHandler.BudgetView(bvCtx); err != nil {
		t.Fatalf("BudgetView failed: %v", err)
	}
	body := bvRec.Body.String()
	if !strings.Contains(body, "Lifestyle & Fun") {
		t.Errorf("expected BudgetView to contain 'Lifestyle & Fun'")
	}
	// Check group-header position specifically
	lifestyleHeader := `toggleGroup('Lifestyle___Fun')`
	needsHeader := `toggleGroup('Needs')`
	lifestyleIdx := strings.Index(body, lifestyleHeader)
	needsIdx := strings.Index(body, needsHeader)
	if lifestyleIdx == -1 || needsIdx == -1 || lifestyleIdx > needsIdx {
		t.Errorf("expected 'Lifestyle & Fun' group header to appear before 'Needs' in BudgetView html, got indices: lifestyle=%d, needs=%d", lifestyleIdx, needsIdx)
	}
}

func TestMobileResponsiveBudgetViewAndErgonomics(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "balance_mobile_ux_test_*")
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

	userID := domain.NewUUIDv7()
	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, err = mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'mob@example.com', 'hash', 'Mobile User', ?, ?)`, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'Mobile Family Budget', 'PLN', 'UTC', ?, ?, ?)`, budgetID, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainDB.Exec(`INSERT INTO budget_members (budget_id, user_id, role, joined_at) VALUES (?, ?, 'OWNER', ?)`, budgetID, userID, now)
	if err != nil {
		t.Fatal(err)
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatalf("failed getting tenant db: %v", err)
	}

	bID, err := budget.CreateBucket(context.Background(), tenantDB, budget.CreateBucketInput{
		GroupName:     "Housing & Bills",
		Name:          "Electricity",
		AgentID:       userID,
		CorrelationID: "corr-mob-1",
	})
	if err != nil {
		t.Fatalf("failed creating bucket: %v", err)
	}

	dueMicro := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC).UnixMicro()
	ceilMinor := int64(120000)
	_, err = budget.CreateTarget(context.Background(), tenantDB, budget.CreateTargetInput{
		BucketID:            bID,
		Name:                "Monthly Power Bill",
		TargetType:          "EXPENSE",
		Priority:            3,
		AmountMinor:         35000,
		Currency:            "PLN",
		RecurrenceCadence:   "MONTHLY",
		DueDate:             &dueMicro,
		CeilingAmountMinor:  &ceilMinor,
		NonSpendingStrategy: "ROLL_OVER",
		AgentID:             userID,
		CorrelationID:       "corr-mob-target-1",
	})
	if err != nil {
		t.Fatalf("failed creating target: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/budget?horizon=90d&filter=all", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("budgetId")
	c.SetParamValues(budgetID)
	c.Set(ContextBudgetDBKey, tenantDB)
	c.Set(ContextUserIDKey, userID)
	c.Set(ContextRoleKey, "OWNER")
	c.Set("csrfToken", "csrf-mob")

	if err := workspaceHandler.BudgetView(c); err != nil {
		t.Fatalf("BudgetView failed: %v", err)
	}
	body := rec.Body.String()

	// 1. Mobile Horizon Select & Desktop Join
	if !strings.Contains(body, `id="mobile_horizon_select"`) {
		t.Errorf("expected mobile_horizon_select dropdown in renderBudgetControlsToolbar")
	}
	if !strings.Contains(body, `value="/budgets/`+budgetID+`/budget?horizon=90d&filter=all" selected`) {
		t.Errorf("expected 90d option to be selected in mobile_horizon_select")
	}

	// 2. Responsive Filter Toolbar & Full-Width Search Input
	if !strings.Contains(body, `grid grid-cols-2 sm:inline-flex sm:join`) {
		t.Errorf("expected 2-column mobile grid for filter buttons")
	}
	if !strings.Contains(body, `id="envelope_search_input"`) || !strings.Contains(body, `w-full sm:w-48`) {
		t.Errorf("expected full-width mobile search wrapper w-full sm:w-48")
	}

	// 3. Mobile Bucket Cards with Category Header, Direct Assign Button & Enriched Target Sublist
	if !strings.Contains(body, `id="mobile-group-Housing___Bills"`) || !strings.Contains(body, `toggleMobileGroup('Housing___Bills')`) {
		t.Errorf("expected collapsible mobile category group header for Housing & Bills")
	}
	if !strings.Contains(body, `openAssignModal('`+bID+`', 'Electricity', 'PLN')`) {
		t.Errorf("expected direct Assign quick action button on mobile bucket card")
	}
	if !strings.Contains(body, `Due: 2026-11-15`) || !strings.Contains(body, `Goal: 1200.00 PLN`) {
		t.Errorf("expected target due date and goal in mobile target sublist")
	}

	// 4. Bottom Dock Safe Area & Thumb-Friendly Classes
	if !strings.Contains(body, `dock dock-md md:hidden`) || !strings.Contains(body, `pb-[env(safe-area-inset-bottom)]`) {
		t.Errorf("expected RenderBottomDock to include dock-md and safe-area-inset-bottom")
	}
	if !strings.Contains(body, `pb-24 md:pb-8`) {
		t.Errorf("expected <main> to include pb-24 md:pb-8 so bottom dock does not obscure content")
	}

	// 5. Bottom-Sheet Modals & Responsive Bucket Drawer Stats
	if !strings.Contains(body, `max-h-[85dvh]`) || !strings.Contains(body, `pb-[calc(1.5rem+env(safe-area-inset-bottom))]`) {
		t.Errorf("expected bottom-sheet modals to include max-h-[85dvh] and safe-area bottom padding")
	}
	if !strings.Contains(body, `stats stats-vertical sm:stats-horizontal`) {
		t.Errorf("expected bucket_drawer stats to stack vertically on mobile (stats-vertical sm:stats-horizontal)")
	}
	if !strings.Contains(body, `inputmode="decimal"`) {
		t.Errorf("expected decimal inputmode on amount fields for mobile on-screen keyboards")
	}
}







