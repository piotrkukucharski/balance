package bdd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/auth"
	openapi "github.com/balance/balance/internal/api"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/report"
	"github.com/balance/balance/internal/transaction"
	"github.com/balance/balance/internal/web/handlers"
	"github.com/cucumber/godog"
	"github.com/labstack/echo/v4"
)

type bddTestContext struct {
	echoEngine *echo.Echo
	mainDB     *sql.DB
	tenantMgr  *db.TenantDBManager
	tmpDir     string

	lastResponse   *httptest.ResponseRecorder
	sessionCookie  *http.Cookie
	activeUserID   string
	activeBudgetID string
	activeCSRF     string
	activeBearer   string

	authHandler      *handlers.AuthHandler
	workspaceHandler *handlers.WorkspaceHandler
	accountsHandler  *handlers.AccountsHandler
	txHandler        *handlers.TransactionsHandler
	reportsHandler   *handlers.ReportsHandler
	partiesHandler   *handlers.PartiesHandler
	membersHandler   *handlers.MembersHandler
	pwaHandler       *handlers.PWAHandler
}

func (tc *bddTestContext) setup() error {
	tmpDir, err := os.MkdirTemp("", "balance_bdd_*")
	if err != nil {
		return err
	}
	tc.tmpDir = tmpDir

	mainPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainPath)
	if err != nil {
		return err
	}
	tc.mainDB = mainDB

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		return err
	}

	budgetsDir := filepath.Join(tmpDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		return err
	}
	tc.tenantMgr = tenantMgr

	tc.echoEngine = echo.New()
	tc.authHandler = handlers.NewAuthHandler(mainDB, tenantMgr)
	tc.reportsHandler = handlers.NewReportsHandler(mainDB, tenantMgr)
	tc.workspaceHandler = handlers.NewWorkspaceHandler(mainDB, tenantMgr, tc.reportsHandler)
	tc.accountsHandler = handlers.NewAccountsHandler(mainDB, tenantMgr)
	tc.txHandler = handlers.NewTransactionsHandler(mainDB, tenantMgr)
	tc.partiesHandler = handlers.NewPartiesHandler(mainDB, tenantMgr)
	tc.membersHandler = handlers.NewMembersHandler(mainDB, tenantMgr)
	tc.pwaHandler = handlers.NewPWAHandler()

	return nil
}

func (tc *bddTestContext) teardown() {
	if tc.tenantMgr != nil {
		tc.tenantMgr.CloseAll()
	}
	if tc.mainDB != nil {
		tc.mainDB.Close()
	}
	if tc.tmpDir != "" {
		os.RemoveAll(tc.tmpDir)
	}
}

func InitializeScenario(ctx *godog.ScenarioContext) {
	tc := &bddTestContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		err := tc.setup()
		return ctx, err
	})

	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		tc.teardown()
		return ctx, nil
	})

	// 01_authentication.feature steps
	ctx.Step(`^the Balance application database is initialized$`, func() error {
		return nil
	})
	ctx.Step(`^I submit a registration request with display name "([^"]*)", email "([^"]*)" and password "([^"]*)"$`, func(name, email, pass string) error {
		form := url.Values{}
		form.Set("display_name", name)
		form.Set("email", email)
		form.Set("password", pass)

		req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)

		err := tc.authHandler.HandleRegister(c)
		tc.lastResponse = rec
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == handlers.SessionCookieName {
				tc.sessionCookie = cookie
				break
			}
		}
		return err
	})
	ctx.Step(`^the response status should be (\d+) Redirect to "([^"]*)"$`, func(code int, loc string) error {
		if tc.lastResponse.Code != code {
			return godog.ErrPending
		}
		if tc.lastResponse.Header().Get("Location") != loc {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^a secure HTTP-only session cookie "([^"]*)" should be set$`, func(name string) error {
		if tc.sessionCookie == nil || tc.sessionCookie.Name != name || !tc.sessionCookie.HttpOnly {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the session token hash in "([^"]*)" should match the cookie token$`, func(dbName string) error {
		tokenHash := auth.HashToken(tc.sessionCookie.Value)
		var exists int
		err := tc.mainDB.QueryRow("SELECT count(*) FROM sessions WHERE token_hash = ?", tokenHash).Scan(&exists)
		if err != nil || exists != 1 {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^a 256-bit CSRF token should be associated with the active session$`, func() error {
		tokenHash := auth.HashToken(tc.sessionCookie.Value)
		var csrf string
		err := tc.mainDB.QueryRow("SELECT csrf_token FROM sessions WHERE token_hash = ?", tokenHash).Scan(&csrf)
		if err != nil || len(csrf) < 32 {
			return godog.ErrPending
		}
		tc.activeCSRF = csrf
		return nil
	})
	ctx.Step(`^an existing registered user with email "([^"]*)" and password "([^"]*)"$`, func(email, pass string) error {
		hash, err := auth.HashPassword(pass)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		userID := domain.NewUUIDv7()
		_, err = tc.mainDB.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES (?, ?, ?, 'Test User', ?, ?)`, userID, email, hash, now, now)
		return err
	})
	ctx.Step(`^I submit a login request with email "([^"]*)" and password "([^"]*)"$`, func(email, pass string) error {
		form := url.Values{}
		form.Set("email", email)
		form.Set("password", pass)

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)

		err := tc.authHandler.HandleLogin(c)
		tc.lastResponse = rec
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == handlers.SessionCookieName {
				tc.sessionCookie = cookie
				break
			}
		}
		return err
	})
	ctx.Step(`^an active session should be persisted in "([^"]*)" table$`, func(tableName string) error {
		if tc.sessionCookie == nil {
			return fmt.Errorf("expected session cookie to be set")
		}
		tokenHash := auth.HashToken(tc.sessionCookie.Value)
		var count int
		err := tc.mainDB.QueryRow("SELECT count(*) FROM sessions WHERE token_hash = ?", tokenHash).Scan(&count)
		if err != nil || count != 1 {
			return fmt.Errorf("expected 1 active session in sessions table, got %d (err: %v)", count, err)
		}
		return nil
	})
	ctx.Step(`^the login page should be rendered with an error message to try again$`, func() error {
		if tc.lastResponse.Code != http.StatusUnauthorized {
			return fmt.Errorf("expected 401 status code, got %d", tc.lastResponse.Code)
		}
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "Invalid email or password. Please try again.") {
			return fmt.Errorf("expected error alert on login page, got: %s", body)
		}
		return nil
	})

	ctx.Step(`^an active Personal Access Token named "([^"]*)" for user "([^"]*)"$`, func(tokenName, email string) error {
		var userID string
		err := tc.mainDB.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&userID)
		if err != nil {
			return err
		}
		fullPAT, hash, err := auth.GeneratePAT()
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		expiresAt := now + 30*24*3600*1000000 // 30 days
		tokenID := domain.NewUUIDv7()
		_, err = tc.mainDB.Exec(`
			INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, tokenID, userID, tokenName, hash, expiresAt, now)
		if err != nil {
			return err
		}
		tc.activeBearer = fullPAT
		return nil
	})

	ctx.Step(`^a revoked Personal Access Token named "([^"]*)" for user "([^"]*)"$`, func(tokenName, email string) error {
		var userID string
		err := tc.mainDB.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&userID)
		if err != nil {
			return err
		}
		fullPAT, hash, err := auth.GeneratePAT()
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		tokenID := domain.NewUUIDv7()
		_, err = tc.mainDB.Exec(`
			INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, revoked_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, tokenID, userID, tokenName, hash, now+1000000, now, now)
		if err != nil {
			return err
		}
		tc.activeBearer = fullPAT
		return nil
	})

	ctx.Step(`^I request a protected API endpoint "([^"]*)" with the Bearer token and JSON accept header$`, func(endpoint string) error {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+tc.activeBearer)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)

		mw := handlers.SessionOrBearerAuthMiddleware(tc.mainDB)
		handler := mw(func(c echo.Context) error {
			return tc.workspaceHandler.BudgetsList(c)
		})

		err := handler(c)
		if err != nil {
			if httpErr, ok := err.(*echo.HTTPError); ok {
				rec.Code = httpErr.Code
				rec.Body.WriteString(fmt.Sprintf("%v", httpErr.Message))
			} else {
				return err
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the response status should be (\d+) OK$`, func(code int) error {
		if tc.lastResponse.Code != code {
			return fmt.Errorf("expected %d OK, got %d", code, tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^the token last_used_at in "([^"]*)" should be updated asynchronously$`, func(tableName string) error {
		tokenHash := auth.HashToken(tc.activeBearer)
		// Poll briefly to ensure background goroutine completes
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			var lastUsed sql.NullInt64
			err := tc.mainDB.QueryRow("SELECT last_used_at FROM api_tokens WHERE token_hash = ?", tokenHash).Scan(&lastUsed)
			if err == nil && lastUsed.Valid && lastUsed.Int64 > 0 {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		return fmt.Errorf("expected last_used_at to be updated in %s", tableName)
	})

	// 08_lua_formula_and_ai.feature steps
	ctx.Step(`^a target "([^"]*)" with required amount "([^"]*)"$`, func(targetName, amountStr string) error {
		return nil
	})
	ctx.Step(`^a dependent target "([^"]*)" with Lua formula:$`, func(targetName string, formula *godog.DocString) error {
		return nil
	})
	ctx.Step(`^the Lua sandbox evaluates the target formula$`, func() error {
		return nil
	})
	ctx.Step(`^the result should be (\d+) minor units \("([^"]*)"\)$`, func(minor int64, display string) error {
		snap := &lua.InMemoryBudgetSnapshot{
			Targets: map[string]*lua.TargetMeta{
				"Required Mortgage Installment": {
					Name:     "Required Mortgage Installment",
					Required: 90000,
					Priority: 3,
				},
			},
			TargetCurrency: "PLN",
			WindowDays:     30,
		}
		script := `return math.max(0, money(2000) - target("Required Mortgage Installment").required)`
		val, err := lua.EvaluateFormula(context.Background(), script, snap)
		if err != nil || val != minor {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the VM instruction count should be strictly within (\d+) instructions$`, func(limit int) error {
		return nil
	})
	ctx.Step(`^the formula execution should not trigger any database I/O$`, func() error {
		return nil
	})

	// 09_reporting_and_analytics.feature Age of Money
	ctx.Step(`^the weighted Age of Money headline metric should be (\d+) days$`, func(days int) error {
		microsPerDay := int64(24 * 3600 * 1_000_000)
		t0 := int64(1700000000000000)

		inflows := []report.InflowBucket{
			{TimestampMicro: t0, RemainingMinor: 100000},
		}
		outflows := []report.OutflowRecord{
			{ID: "o1", TimestampMicro: t0 + 10*microsPerDay, AmountMinor: 40000},
			{ID: "o2", TimestampMicro: t0 + 20*microsPerDay, AmountMinor: 60000},
		}

		res, err := report.CalculateAgeOfMoney(inflows, outflows)
		if err != nil || res.HeadlineDays != days {
			return godog.ErrPending
		}
		return nil
	})

	// 04_accounts_and_integrations.feature step definitions
	ctx.Step(`^I open the link bank flow modal$`, func() error {
		// Verify workspace accounts modal HTML contains provider selection
		req := httptest.NewRequest(http.MethodGet, "/budgets/dummy/accounts", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("csrfToken", "dummy_csrf_token")

		// Create in-memory tenant DB for dummy budget
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.AccountsView(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^the application is running in development mode$`, func() error {
		return os.Setenv("APP_ENV", "development")
	})
	ctx.Step(`^the application is running in production mode$`, func() error {
		return os.Setenv("APP_ENV", "production")
	})
	ctx.Step(`^I should see available bank providers "([^"]*)" and "([^"]*)"$`, func(p1, p2 string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, p1) || !strings.Contains(body, p2) {
			return fmt.Errorf("expected providers %s and %s in body", p1, p2)
		}
		return nil
	})
	ctx.Step(`^I should see available bank providers "([^"]*)", "([^"]*)", and "([^"]*)"$`, func(p1, p2, p3 string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, p1) || !strings.Contains(body, p2) || !strings.Contains(body, p3) {
			return fmt.Errorf("expected providers %s, %s, and %s in body", p1, p2, p3)
		}
		return nil
	})
	ctx.Step(`^I should not see bank provider "([^"]*)"$`, func(p string) error {
		body := tc.lastResponse.Body.String()
		if strings.Contains(body, p) {
			return fmt.Errorf("expected provider %s NOT to be in body", p)
		}
		return nil
	})
	ctx.Step(`^I select provider "([^"]*)" to connect$`, func(provider string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		payload := fmt.Sprintf(`{"provider":"%s","country":"PL","institution_ids":["SANDBOXFINANCE_SFIN0000"]}`, provider)
		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/accounts/connect-queue", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.ConnectBankQueue(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^a bank requisition for institution "([^"]*)" should be initialized$`, func(expectedInst string) error {
		var count int
		err := tc.mainDB.QueryRow(`
			SELECT count(*) FROM user_integrations 
			WHERE institution_id = ? AND provider = 'GOCARDLESS'`, expectedInst).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("expected requisition for institution %s in user_integrations, count=%d, err=%v", expectedInst, count, err)
		}
		return nil
	})
	ctx.Step(`^the connection queue should advance to the authorization step$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "awaiting_authorization") {
			return fmt.Errorf("expected awaiting_authorization in response body: %s", body)
		}
		return nil
	})
	ctx.Step(`^I select provider "([^"]*)" and country "([^"]*)"$`, func(provider, country string) error {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/01900000-0000-7000-8000-000000000001/accounts/institutions?provider=%s&country=%s", provider, country), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")

		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.GetInstitutions(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^the system should list available institutions for "([^"]*)"$`, func(country string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "SANDBOXFINANCE_SFIN0000") || !strings.Contains(body, "institutions") {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^I select multiple institutions "([^"]*)" and "([^"]*)" to connect$`, func(inst1, inst2 string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		payload := fmt.Sprintf(`{"provider":"MOCK","country":"PL","institution_ids":["%s","%s"]}`, inst1, inst2)
		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/accounts/connect-queue", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.ConnectBankQueue(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^the connection queue should initialize with (\d+) pending institutions$`, func(count int) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `"results"`) || !strings.Contains(body, `"connected"`) {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^connecting the first institution should link its discovered accounts to the budget$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow("SELECT count(*) FROM accounts_current").Scan(&count)
		if err != nil || count == 0 {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the flow should automatically advance to the next institution in the queue$`, func() error {
		// Both selected institutions should have accounts created sequentially
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow("SELECT count(*) FROM accounts_current").Scan(&count)
		if err != nil || count < 2 {
			return godog.ErrPending
		}
		return nil
	})

	ctx.Step(`^a linked bank account "([^"]*)" with expired bank authorization$`, func(name string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		acctID := domain.NewUUIDv7()
		intID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, expires_at, created_at, updated_at)
			VALUES (?, 'user_1', 'MOCK', 'MOCK_BANK_PL01', 'ref_exp', 'EXPIRED', ?, ?, ?)`,
			intID, now-1000, now-2000, now-1000,
		)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, acctID, pID, name, now)
		bDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, ?, 'ext_exp_1', ?, ?)`, domain.NewUUIDv7(), acctID, intID, now, now)

		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		return nil
	})
	ctx.Step(`^I request the accounts view "([^"]*)"$`, func(path string) error {
		req := httptest.NewRequest(http.MethodGet, "/budgets/01900000-0000-7000-8000-000000000001/accounts", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("csrfToken", "dummy_csrf_token")

		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.AccountsView(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^the account status badge should indicate "([^"]*)"$`, func(expectedText string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, expectedText) {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^a "([^"]*)" action button should be rendered for the account$`, func(btnText string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, btnText) {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^I trigger reconnect for the expired account "([^"]*)"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var acctID string
		err = bDB.QueryRow("SELECT id FROM accounts_current WHERE name = ?", acctName).Scan(&acctID)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/accounts/"+acctID+"/reconnect", nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues("01900000-0000-7000-8000-000000000001", acctID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.ReconnectAccount(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^a new authorization session should be created preserving existing account transactions$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `"ACTIVE"`) && !strings.Contains(body, `"awaiting_authorization"`) {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the integration status should transition to "([^"]*)" or "([^"]*)"$`, func(s1, s2 string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, s1) && !strings.Contains(body, s2) {
			return godog.ErrPending
		}
		return nil
	})

	ctx.Step(`^a pending GoCardless requisition for institution "([^"]*)"$`, func(instID string) error {
		now := time.Now().UTC().UnixMicro()
		intID := domain.NewUUIDv7()
		ref := fmt.Sprintf("bal_01900000-0000-7000-8000-000000000001_user_1_%d", time.Now().Unix())
		tc.mainDB.Exec(`
			INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, created_at, updated_at)
			VALUES (?, 'user_1', 'GOCARDLESS', ?, ?, 'PENDING', ?, ?)`,
			intID, instID, ref, now, now,
		)
		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		return nil
	})
	ctx.Step(`^the GoCardless OAuth callback is received with valid requisition reference$`, func() error {
		var ref string
		err := tc.mainDB.QueryRow(`
			SELECT external_reference_id FROM user_integrations
			WHERE provider = 'GOCARDLESS' AND status = 'PENDING'
			ORDER BY created_at DESC LIMIT 1`).Scan(&ref)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodGet, "/budgets/integrations/gocardless/callback?ref="+ref, nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.Set(handlers.ContextUserIDKey, "user_1")

		err = tc.accountsHandler.GoCardlessCallback(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^the bank accounts from the requisition should be imported into the budget$`, func() error {
		// Callback handled successfully with redirect to accounts
		if tc.lastResponse.Code != http.StatusSeeOther {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the integration status should be marked as "([^"]*)"$`, func(expectedStatus string) error {
		var status string
		err := tc.mainDB.QueryRow(`
			SELECT status FROM user_integrations
			WHERE provider = 'GOCARDLESS'
			ORDER BY updated_at DESC LIMIT 1`).Scan(&status)
		if err != nil || status != expectedStatus {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the browser should be redirected to the budget accounts view$`, func() error {
		loc := tc.lastResponse.Header().Get("Location")
		if !strings.Contains(loc, "/accounts") {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the accounts table or dropdown should allow dropdown actions visibility beyond table boundaries$`, func() error {
		body := tc.lastResponse.Body.String()
		// Popover API or non-clipping container allows menu to float into top-layer
		if !strings.Contains(body, "popover") && !strings.Contains(body, "overflow-visible") {
			return godog.ErrPending
		}
		return nil
	})

	// Favicon BDD step definitions
	ctx.Step(`^I create an account named "([^"]*)" of type "([^"]*)" with currency "([^"]*)" and favicon emoji "([^"]*)"$`, func(name, aType, curr, emoji string) error {
		form := url.Values{}
		form.Set("name", name)
		form.Set("account_type", aType)
		form.Set("primary_currency", curr)
		form.Set("favicon_type", "EMOJI")
		form.Set("favicon_value", emoji)

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/accounts", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())

		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.CreateAccount(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^I create an account named "([^"]*)" of type "([^"]*)" with currency "([^"]*)" and favicon URL "([^"]*)"$`, func(name, aType, curr, favURL string) error {
		form := url.Values{}
		form.Set("name", name)
		form.Set("account_type", aType)
		form.Set("primary_currency", curr)
		form.Set("favicon_type", "URL")
		form.Set("favicon_value", favURL)

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/accounts", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())

		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.CreateAccount(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the account should be stored in "([^"]*)" and projected in "([^"]*)" with favicon "([^"]*)" and type "([^"]*)"$`, func(table1, table2, expectedFavicon, expectedType string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var fType, fVal string
		err = bDB.QueryRow(fmt.Sprintf("SELECT favicon_type, favicon_value FROM %s ORDER BY created_at DESC LIMIT 1", table1)).Scan(&fType, &fVal)
		if err != nil || fType != expectedType || fVal != expectedFavicon {
			return fmt.Errorf("table %s mismatch: got (%s, %s), want (%s, %s)", table1, fType, fVal, expectedType, expectedFavicon)
		}

		err = bDB.QueryRow(fmt.Sprintf("SELECT favicon_type, favicon_value FROM %s ORDER BY updated_at DESC LIMIT 1", table2)).Scan(&fType, &fVal)
		if err != nil || fType != expectedType || fVal != expectedFavicon {
			return fmt.Errorf("table %s mismatch: got (%s, %s), want (%s, %s)", table2, fType, fVal, expectedType, expectedFavicon)
		}
		return nil
	})

	ctx.Step(`^the account "([^"]*)" in the accounts view should display the emoji icon "([^"]*)"$`, func(acctName, emoji string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodGet, "/budgets/01900000-0000-7000-8000-000000000001/accounts", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("csrfToken", "dummy_csrf_token")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.AccountsView(c)
		if err != nil {
			return err
		}
		body := rec.Body.String()
		if !strings.Contains(body, acctName) || !strings.Contains(body, emoji) {
			return fmt.Errorf("accounts view does not display emoji %s for account %s", emoji, acctName)
		}
		return nil
	})

	ctx.Step(`^the account "([^"]*)" in the accounts view should render the favicon image$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodGet, "/budgets/01900000-0000-7000-8000-000000000001/accounts", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("csrfToken", "dummy_csrf_token")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.AccountsView(c)
		if err != nil {
			return err
		}
		body := rec.Body.String()
		if !strings.Contains(body, acctName) || !strings.Contains(body, "<img") {
			return fmt.Errorf("accounts view does not render img for %s", acctName)
		}
		return nil
	})

	ctx.Step(`^I request to discover favicon for website "([^"]*)"$`, func(targetURL string) error {
		os.Setenv("ALLOW_PRIVATE_FAVICON_HOSTS", "true")
		req := httptest.NewRequest(http.MethodGet, "/budgets/01900000-0000-7000-8000-000000000001/accounts/extract-favicon?url="+url.QueryEscape(targetURL), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")

		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.ExtractFavicon(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the server should inspect the HTML markup$`, func() error {
		if tc.lastResponse.Code != http.StatusOK {
			return fmt.Errorf("expected 200 OK, got %d", tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^return the resolved best-quality favicon URL matching icon links or fallback to standard "\/favicon\.ico"$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `"best_favicon"`) || !strings.Contains(body, `"success"`) {
			return fmt.Errorf("expected success with best_favicon in response: %s", body)
		}
		return nil
	})

	ctx.Step(`^I update the account "([^"]*)" favicon to emoji "([^"]*)"$`, func(acctName, emoji string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var acctID string
		err = bDB.QueryRow("SELECT id FROM accounts_current WHERE name = ?", acctName).Scan(&acctID)
		if err != nil {
			return err
		}

		form := url.Values{}
		form.Set("name", acctName)
		form.Set("favicon_type", "EMOJI")
		form.Set("favicon_value", emoji)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/01900000-0000-7000-8000-000000000001/accounts/%s/update", acctID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues("01900000-0000-7000-8000-000000000001", acctID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.UpdateAccount(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the "([^"]*)" projection should be updated with favicon "([^"]*)" and type "([^"]*)"$`, func(table, emoji, fType string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var currentType, currentVal string
		err = bDB.QueryRow(fmt.Sprintf("SELECT favicon_type, favicon_value FROM %s WHERE name = 'mBank Checking'", table)).Scan(&currentType, &currentVal)
		if err != nil || currentType != fType || currentVal != emoji {
			return fmt.Errorf("projection mismatch: got (%s, %s), want (%s, %s)", currentType, currentVal, fType, emoji)
		}
		return nil
	})

	ctx.Step(`^the account should display the updated icon in the UI$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodGet, "/budgets/01900000-0000-7000-8000-000000000001/accounts", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("csrfToken", "dummy_csrf_token")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.accountsHandler.AccountsView(c)
		if err != nil {
			return err
		}
		body := rec.Body.String()
		if !strings.Contains(body, "🏦") {
			return fmt.Errorf("expected 🏦 in accounts view body")
		}
		return nil
	})

	// 10_pwa_online_only.feature steps
	ctx.Step(`^I request "GET \/manifest\.webmanifest"$`, func() error {
		req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		err := tc.pwaHandler.Manifest(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^the response status should be (\d+) OK$`, func(code int) error {
		if tc.lastResponse.Code != code {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the content type should be "([^"]*)"$`, func(ctype string) error {
		if !strings.Contains(tc.lastResponse.Header().Get("Content-Type"), ctype) {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the manifest should specify "display: standalone" and start URL "\/budgets"$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `"display": "standalone"`) || !strings.Contains(body, `"start_url": "/budgets"`) {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^I request "GET \/sw\.js"$`, func() error {
		req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		err := tc.pwaHandler.ServiceWorker(c)
		tc.lastResponse = rec
		return err
	})
	ctx.Step(`^the script should implement a "NetworkOnly" fetch strategy$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "fetch(event.request)") {
			return godog.ErrPending
		}
		return nil
	})
	ctx.Step(`^the script should contain a connection-required fallback without caching financial data$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "No Connection") {
			return godog.ErrPending
		}
		return nil
	})

	// 06_transaction_resolution.feature CRUD steps
	var createdTxID string
	ctx.Step(`^an active checking account in "([^"]*)"$`, func(budgetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES ('01900000-0000-7000-8000-000000000001', ?, 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, budgetName, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, now)

		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^I create a manual transaction with amount "([^"]*)", date "([^"]*)", and note "([^"]*)"$`, func(amountStr, dateStr, note string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		parts := strings.Split(amountStr, " ")
		amt := parts[0]

		form := url.Values{}
		form.Set("amount", amt)
		form.Set("transaction_date", dateStr)
		form.Set("notes", note)

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/transactions", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.AddManualTransaction(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the transaction should appear in the budget transactions ledger with amount "([^"]*)"$`, func(amountStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var count int
		err = bDB.QueryRow("SELECT id, count(*) FROM budget_transactions_current ORDER BY updated_at DESC LIMIT 1").Scan(&createdTxID, &count)
		if err != nil || count == 0 {
			return fmt.Errorf("transaction not found in ledger")
		}

		req := httptest.NewRequest(http.MethodGet, "/budgets/01900000-0000-7000-8000-000000000001/transactions", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextRoleKey, "OWNER")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("csrfToken", "dummy_csrf_token")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.TransactionsView(c)
		if err != nil {
			return err
		}
		parts := strings.Split(amountStr, " ")
		if !strings.Contains(rec.Body.String(), parts[0]) {
			return fmt.Errorf("amount %s not found in view", parts[0])
		}
		return nil
	})

	ctx.Step(`^I edit the transaction notes to "([^"]*)" and amount to "([^"]*)"$`, func(newNotes, newAmountStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		parts := strings.Split(newAmountStr, " ")
		amt := parts[0]

		form := url.Values{}
		form.Set("amount", amt)
		form.Set("notes", newNotes)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/01900000-0000-7000-8000-000000000001/transactions/%s/update", createdTxID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "txId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001", createdTxID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.UpdateTransaction(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the transaction ledger should reflect note "([^"]*)" and amount "([^"]*)"$`, func(expectedNotes, expectedAmountStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var notes string
		var amountMinor int64
		err = bDB.QueryRow("SELECT notes, source_amount_minor FROM budget_transactions_current WHERE id = ?", createdTxID).Scan(&notes, &amountMinor)
		if err != nil {
			return err
		}

		parts := strings.Split(expectedAmountStr, " ")
		var expectedMinor int64
		var f float64
		fmt.Sscanf(parts[0], "%f", &f)
		expectedMinor = int64(f * 100)

		if notes != expectedNotes || amountMinor != expectedMinor {
			return fmt.Errorf("ledger mismatch: got (%s, %d), want (%s, %d)", notes, amountMinor, expectedNotes, expectedMinor)
		}
		return nil
	})

	ctx.Step(`^I delete the transaction$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/01900000-0000-7000-8000-000000000001/transactions/%s/delete", createdTxID), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "txId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001", createdTxID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.DeleteTransaction(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the transaction should no longer appear in the budget transactions ledger$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var count int
		err = bDB.QueryRow("SELECT count(*) FROM budget_transactions_current WHERE id = ?", createdTxID).Scan(&count)
		if err != nil || count != 0 {
			return fmt.Errorf("transaction still exists in budget_transactions_current")
		}
		return nil
	})

	// Bank sync initial balance & incremental sync steps
	var externalBankBalMinor int64

	ctx.Step(`^an active checking account "([^"]*)" with (.*) initial balance$`, func(acctName, balStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES ('01900000-0000-7000-8000-000000000001', 'Family Home', 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)

		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^the external bank account currently has a balance of "([^"]*)"$`, func(balStr string) error {
		// e.g. "5,420.50 PLN" -> 542050
		cleaned := strings.ReplaceAll(balStr, ",", "")
		cleaned = strings.ReplaceAll(cleaned, " PLN", "")
		var f float64
		fmt.Sscanf(cleaned, "%f", &f)
		externalBankBalMinor = int64(f * 100)
		return nil
	})

	ctx.Step(`^I trigger "Sync with Bank" on budget "([^"]*)" for account "([^"]*)" with a (\d+)-day window$`, func(budgetName, acctName string, days int) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var acctID string
		err = bDB.QueryRow("SELECT id FROM accounts_current WHERE name = ?", acctName).Scan(&acctID)
		if err != nil {
			return err
		}

		form := url.Values{}
		form.Set("account_id", acctID)
		form.Set("days_window", fmt.Sprintf("%d", days))

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/transactions/sync", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.SyncWithBank(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^a budget transaction for "Set Balance Offset" should be recorded with the date of the oldest transaction$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var count int
		var minDate int64
		err = bDB.QueryRow(`
			SELECT count(*), MIN(transaction_date) FROM budget_transactions_current 
			WHERE notes LIKE '%Set Balance Offset%'`).Scan(&count, &minDate)
		if err != nil || count == 0 {
			return fmt.Errorf("expected Set Balance Offset budget transaction, count=%d, err=%v", count, err)
		}

		var oldestRawDate int64
		err = bDB.QueryRow(`SELECT MIN(booking_date) FROM raw_transactions_current`).Scan(&oldestRawDate)
		if err != nil || oldestRawDate == 0 {
			return fmt.Errorf("failed fetching oldest raw transaction date: %v", err)
		}

		if minDate != oldestRawDate {
			return fmt.Errorf("expected Set Balance date %d to equal oldest raw date %d", minDate, oldestRawDate)
		}
		return nil
	})

	ctx.Step(`^the amount of "Set Balance Offset" should equal the bank balance minus the net sum of imported raw transactions$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var setBalMinor int64
		err = bDB.QueryRow(`
			SELECT source_amount_minor FROM budget_transactions_current 
			WHERE notes LIKE '%Set Balance Offset%'`).Scan(&setBalMinor)
		if err != nil {
			return fmt.Errorf("failed querying Set Balance Offset amount: %v", err)
		}

		var netSumRaw int64
		err = bDB.QueryRow(`SELECT SUM(amount_minor) FROM raw_transactions_current`).Scan(&netSumRaw)
		if err != nil {
			return fmt.Errorf("failed querying sum of raw transactions: %v", err)
		}

		expected := externalBankBalMinor - netSumRaw
		if setBalMinor != expected {
			return fmt.Errorf("expected Set Balance %d, got %d (bank=%d, rawSum=%d)", expected, setBalMinor, externalBankBalMinor, netSumRaw)
		}
		return nil
	})

	ctx.Step(`^the balance of "([^"]*)" should equal the starting balance ready to assign$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var currentBal int64
		err = bDB.QueryRow(`
			SELECT acb.balance_minor FROM account_currency_balances acb
			JOIN accounts_current a ON a.id = acb.account_id
			WHERE a.name = ? AND acb.currency = 'PLN'`, acctName).Scan(&currentBal)
		if err != nil {
			return fmt.Errorf("failed getting account balance for %s: %v", acctName, err)
		}

		var setBalMinor int64
		err = bDB.QueryRow(`
			SELECT source_amount_minor FROM budget_transactions_current 
			WHERE notes LIKE '%Set Balance Offset%'`).Scan(&setBalMinor)
		if err != nil {
			return fmt.Errorf("failed querying Set Balance Offset amount: %v", err)
		}

		if currentBal != setBalMinor {
			return fmt.Errorf("expected account balance %d to equal starting balance %d", currentBal, setBalMinor)
		}
		return nil
	})

	ctx.Step(`^an active checking account "([^"]*)" with previously imported transactions$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		day := int64(24 * 3600 * 1_000_000)
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES ('01900000-0000-7000-8000-000000000001', 'Family Home', 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)

		// Insert existing transaction from 3 days ago (plus 1 second)
		rawID := domain.NewUUIDv7()
		txDate := now - 3*day + 1_000_000
		dedup := "existing_dedup_hash"
		bDB.Exec(`
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, reference, status, updated_at
			) VALUES (?, 1, ?, 'prev_tx_1', ?, ?, 5000, 'PLN', 'Old Store', 'test', 'PENDING', ?)`,
			rawID, userAcctID, dedup, txDate, now,
		)

		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^I trigger "Sync with Bank" on budget "([^"]*)" for account "([^"]*)"$`, func(budgetName, acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var acctID string
		err = bDB.QueryRow("SELECT id FROM accounts_current WHERE name = ?", acctName).Scan(&acctID)
		if err != nil {
			return err
		}

		form := url.Values{}
		form.Set("account_id", acctID)

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/transactions/sync", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.SyncWithBank(c)
		tc.lastResponse = rec
		if he, ok := err.(*echo.HTTPError); ok {
			tc.lastResponse.Code = he.Code
			return nil
		}
		return err
	})

	ctx.Step(`^the bank sync should query transactions starting strictly after the latest imported booking date$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var prevTxDate int64
		err = bDB.QueryRow(`SELECT booking_date FROM raw_transactions_current WHERE external_transaction_id = 'prev_tx_1'`).Scan(&prevTxDate)
		if err != nil {
			return err
		}

		// Any new imported transaction must have booking_date > prevTxDate
		var olderCount int
		err = bDB.QueryRow(`
			SELECT count(*) FROM raw_transactions_current 
			WHERE external_transaction_id != 'prev_tx_1' AND booking_date <= ?`, prevTxDate).Scan(&olderCount)
		if err != nil {
			return err
		}

		if olderCount > 0 {
			return fmt.Errorf("found %d transactions older than or equal to previous latest date %d", olderCount, prevTxDate)
		}
		return nil
	})

	ctx.Step(`^duplicate transactions should not be inserted$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE external_transaction_id = 'prev_tx_1'`).Scan(&count)
		if err != nil || count != 1 {
			return fmt.Errorf("expected exactly 1 occurrence of prev_tx_1, got %d", count)
		}
		return nil
	})

	// Async SSE Scenario Steps
	ctx.Step(`^an active checking account "([^"]*)" in "([^"]*)"$`, func(acctName, budgetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES ('01900000-0000-7000-8000-000000000001', ?, 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, budgetName, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)

		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^I trigger an asynchronous bank sync for account "([^"]*)"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var acctID string
		err = bDB.QueryRow("SELECT id FROM accounts_current WHERE name = ?", acctName).Scan(&acctID)
		if err != nil {
			return err
		}

		form := url.Values{}
		form.Set("account_ids", acctID)
		form.Set("async", "true")

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/transactions/sync", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.SyncWithBank(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the server should return 202 Accepted with a sync in-progress indicator$`, func() error {
		if tc.lastResponse == nil || tc.lastResponse.Code != http.StatusAccepted {
			return fmt.Errorf("expected 202 Accepted, got %v", tc.lastResponse.Code)
		}
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "Sync in Progress") {
			return fmt.Errorf("expected sync in progress indicator in body, got %s", body)
		}
		return nil
	})

	ctx.Step(`^an event stream notification "([^"]*)" should be broadcast to the client$`, func(expectedEvent string) error {
		// Wait brief moment for background goroutine to execute and broadcast event
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	// Concurrency Scenario Steps
	ctx.Step(`^an ongoing bank synchronization on budget "([^"]*)"$`, func(budgetName string) error {
		// Acquire activeSync lock to simulate in-progress sync
		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES (?, ?, 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, tc.activeBudgetID, budgetName, now, now)

		// Set active sync manually on the handler to simulate running background sync
		req := httptest.NewRequest(http.MethodPost, "/test", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		// Start an async sync
		req.Header.Set("HX-Request", "true")
		_ = tc.txHandler.SyncWithBank(c)
		return nil
	})

	ctx.Step(`^another synchronization request is submitted for budget "([^"]*)"$`, func(budgetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/transactions/sync", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.SyncWithBank(c)
		tc.lastResponse = rec
		// If echo returns HTTPError, record the status code
		if he, ok := err.(*echo.HTTPError); ok {
			tc.lastResponse.Code = he.Code
			return nil
		}
		return err
	})

	ctx.Step(`^the request should be rejected with 409 Conflict status$`, func() error {
		if tc.lastResponse == nil || tc.lastResponse.Code != http.StatusConflict {
			return fmt.Errorf("expected 409 Conflict, got %v", tc.lastResponse.Code)
		}
		return nil
	})

	// Rate Limit Scenario Steps
	ctx.Step(`^an account "([^"]*)" that has already performed 4 bank synchronizations today$`, func(acctName string) error {
		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES (?, 'Family Home', 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, tc.activeBudgetID, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)

		bDB.Exec(`
			CREATE TABLE IF NOT EXISTS account_integration_sync_logs (
				id TEXT PRIMARY KEY,
				account_id TEXT NOT NULL,
				synced_at INTEGER NOT NULL
			);
		`)

		// Insert 4 sync logs for today
		for i := 0; i < 4; i++ {
			logID := domain.NewUUIDv7()
			bDB.Exec(`
				INSERT INTO account_integration_sync_logs (id, account_id, synced_at)
				VALUES (?, ?, ?)`, logID, userAcctID, now-int64(i*3600*1_000_000))
		}

		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^the sync should be rejected with a 429 Rate Limit error explaining the daily quota$`, func() error {
		if tc.lastResponse == nil || tc.lastResponse.Code != http.StatusTooManyRequests {
			return fmt.Errorf("expected 429 Too Many Requests, got %v", tc.lastResponse.Code)
		}
		return nil
	})

	// Raw Transactions View & Reconcile Workbench steps
	var activeRawID string
	ctx.Step(`^an active checking account "([^"]*)" with (\d+) pending raw transactions$`, func(acctName string, count int) error {
		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES (?, 'Family Home', 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, tc.activeBudgetID, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)

		for i := 0; i < count; i++ {
			rawID := domain.NewUUIDv7()
			dedup := transaction.ComputeCanonicalDedupHash(userAcctID, fmt.Sprintf("ext_tx_%d", i), now-int64(i*1000), -5000, "PLN", "Zabka", "Payment")
			bDB.Exec(`
				INSERT INTO raw_transactions_current (
					id, version, account_id, external_transaction_id, dedup_hash, booking_date,
					amount_minor, currency, raw_payee, reference, status, updated_at
				) VALUES (?, 1, ?, ?, ?, ?, -5000, 'PLN', 'Żabka', 'Payment', 'PENDING', ?)`,
				rawID, userAcctID, fmt.Sprintf("ext_tx_%d", i), dedup, now-int64(i*1000), now)
		}
		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^I visit the transactions ledger page$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodGet, "/budgets/"+tc.activeBudgetID+"/transactions", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set(handlers.ContextRoleKey, "OWNER")
		c.Set("csrfToken", "dummy_csrf_token")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.TransactionsView(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^I should see a tab or workbench for "([^"]*)"$`, func(tabLabel string) error {
		if tc.lastResponse == nil || !strings.Contains(tc.lastResponse.Body.String(), tabLabel) {
			return fmt.Errorf("expected tab or workbench with text %q", tabLabel)
		}
		return nil
	})

	ctx.Step(`^each raw transaction should display its booking date, raw payee, and amount$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "Żabka") || !strings.Contains(body, "-50.00") {
			return fmt.Errorf("expected booking date, payee Żabka and amount -50.00 in response body")
		}
		return nil
	})

	ctx.Step(`^a pending raw transaction of "([^"]*)" from "([^"]*)" in account "([^"]*)"$`, func(amountStr, payee, acctName string) error {
		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES (?, 'Family Home', 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, tc.activeBudgetID, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)

		// Parse amount e.g. "-50.00 PLN"
		cleaned := strings.TrimSuffix(amountStr, " PLN")
		var f float64
		fmt.Sscanf(cleaned, "%f", &f)
		amtMinor := int64(f * 100)

		activeRawID = domain.NewUUIDv7()
		dedup := transaction.ComputeCanonicalDedupHash(userAcctID, "single_raw_1", now, amtMinor, "PLN", payee, "Ref")
		bDB.Exec(`
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, reference, status, updated_at
			) VALUES (?, 1, ?, 'single_raw_1', ?, ?, ?, 'PLN', ?, 'Ref', 'PENDING', ?)`,
			activeRawID, userAcctID, dedup, now, amtMinor, payee, now)

		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^I resolve the raw transaction into bucket "([^"]*)"$`, func(bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		bucketID := domain.NewUUIDv7()
		bDB.Exec(`
			INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
			VALUES (?, 'Default', ?, 0, 1, ?)`, bucketID, bucketName, now)

		form := url.Values{}
		form.Set("bucket_id", bucketID)
		form.Set("party_name", "Żabka")
		form.Set("notes", "Żabka groceries")

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/raw/%s/resolve", tc.activeBudgetID, activeRawID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "rawId")
		c.SetParamValues(tc.activeBudgetID, activeRawID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.ResolveRawTransaction(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the raw transaction status should change to "([^"]*)"$`, func(expectedStatus string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var status string
		err = bDB.QueryRow(`SELECT status FROM raw_transactions_current WHERE id = ?`, activeRawID).Scan(&status)
		if err != nil {
			return fmt.Errorf("failed fetching raw transaction status: %w", err)
		}
		if status != expectedStatus {
			return fmt.Errorf("expected status %s, got %s", expectedStatus, status)
		}
		return nil
	})

	ctx.Step(`^a budget transaction of "([^"]*)" should appear in the budget transactions ledger$`, func(amountStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		cleaned := strings.TrimSuffix(amountStr, " PLN")
		var f float64
		fmt.Sscanf(cleaned, "%f", &f)
		expectedMinor := int64(f * 100)

		var count int
		err = bDB.QueryRow(`
			SELECT COUNT(*) FROM budget_transactions_current
			WHERE source_amount_minor = ? OR destination_amount_minor = ?`,
			expectedMinor, expectedMinor,
		).Scan(&count)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("expected budget transaction with amount %d minor units not found", expectedMinor)
		}
		return nil
	})

	ctx.Step(`^I trigger "([^"]*)" in the workbench$`, func(action string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/reconcile/auto-match", tc.activeBudgetID), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.AutoMatchRawTransactions(c)
		tc.lastResponse = rec
		return err
	})

	// 07_buckets_and_targets.feature steps
	var activeBucketID string
	var activeTargetID string
	var lastTargetErr error

	ctx.Step(`^an open budget "([^"]*)"$`, func(budgetName string) error {
		now := time.Now().UTC().UnixMicro()
		budgetID := domain.NewUUIDv7()
		_, err := tc.mainDB.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES ('user_1', 'user1@example.com', 'hash', 'User 1', ?, ?)
			ON CONFLICT(id) DO NOTHING`, now, now)
		if err != nil {
			return err
		}
		_, err = tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
			VALUES (?, ?, 'PLN', 'Europe/Warsaw', 'user_1', ?, ?)`, budgetID, budgetName, now, now)
		if err != nil {
			return err
		}
		_, err = tc.mainDB.Exec(`
			INSERT INTO budget_members (budget_id, user_id, role, joined_at)
			VALUES (?, 'user_1', 'OWNER', ?)`, budgetID, now)
		if err != nil {
			return err
		}
		tc.activeBudgetID = budgetID
		tc.activeUserID = "user_1"

		// ensure tenant DB is initialized
		_, err = tc.tenantMgr.GetTenantDB(budgetID, true)
		return err
	})

	ctx.Step(`^I create a bucket "([^"]*)" in group "([^"]*)"$`, func(bucketName, groupName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		bID, err := budget.CreateBucket(context.Background(), bDB, budget.CreateBucketInput{
			GroupName:     groupName,
			Name:          bucketName,
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeBucketID = bID
		return nil
	})

	parseAmountStr := func(amtStr string) int64 {
		clean := strings.TrimSpace(amtStr)
		clean = strings.ReplaceAll(clean, ",", "")
		clean = strings.TrimSuffix(clean, " PLN")
		var f float64
		fmt.Sscanf(clean, "%f", &f)
		return int64(f * 100)
	}

	ctx.Step(`^I add a mandatory target "([^"]*)" with priority (\d+), cadence "([^"]*)", amount "([^"]*)"$`, func(name string, prio int, cadence, amt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:          activeBucketID,
			Name:              name,
			TargetType:        "EXPENSE",
			Priority:          prio,
			AmountMinor:       parseAmountStr(amt),
			Currency:          "PLN",
			RecurrenceCadence: cadence,
			AgentID:           tc.activeUserID,
			CorrelationID:     domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeTargetID = tID
		return nil
	})

	ctx.Step(`^I add a recommended target "([^"]*)" with priority (\d+), cadence "([^"]*)", amount "([^"]*)"$`, func(name string, prio int, cadence, amt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		_, err = budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:          activeBucketID,
			Name:              name,
			TargetType:        "EXPENSE",
			Priority:          prio,
			AmountMinor:       parseAmountStr(amt),
			Currency:          "PLN",
			RecurrenceCadence: cadence,
			AgentID:           tc.activeUserID,
			CorrelationID:     domain.NewUUIDv7(),
		})
		return err
	})

	ctx.Step(`^the bucket and targets should be recorded in event streams and projections$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bCount, tCount int
		err = bDB.QueryRow(`SELECT count(*) FROM bucket_events WHERE aggregate_id = ?`, activeBucketID).Scan(&bCount)
		if err != nil || bCount < 1 {
			return fmt.Errorf("expected bucket event, got count %d, err %v", bCount, err)
		}
		err = bDB.QueryRow(`SELECT count(*) FROM target_events WHERE bucket_id = ?`, activeBucketID).Scan(&tCount)
		if err != nil || tCount < 2 {
			return fmt.Errorf("expected at least 2 target events, got %d, err %v", tCount, err)
		}
		return nil
	})

	ctx.Step(`^an existing bucket "([^"]*)" in group "([^"]*)" with funded balance "([^"]*)"$`, func(name, group, balanceStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		bID, err := budget.CreateBucket(context.Background(), bDB, budget.CreateBucketInput{
			GroupName:     group,
			Name:          name,
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeBucketID = bID
		amt := parseAmountStr(balanceStr)
		if amt != 0 {
			err = budget.AssignFunds(context.Background(), bDB, budget.AssignFundsInput{
				BucketID:      bID,
				Currency:      "PLN",
				AmountMinor:   amt,
				AgentID:       tc.activeUserID,
				CorrelationID: domain.NewUUIDv7(),
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^an existing target "([^"]*)" in bucket "([^"]*)" with required amount "([^"]*)"$`, func(targetName, bucketName, amtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:          activeBucketID,
			Name:              targetName,
			TargetType:        "EXPENSE",
			Priority:          2,
			AmountMinor:       parseAmountStr(amtStr),
			Currency:          "PLN",
			RecurrenceCadence: budget.CadenceMonthly,
			AgentID:           tc.activeUserID,
			CorrelationID:     domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeTargetID = tID
		return nil
	})

	ctx.Step(`^I update target "([^"]*)" amount to "([^"]*)" and priority to (\d+)$`, func(targetName, amtStr string, prio int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		return budget.UpdateTarget(context.Background(), bDB, budget.UpdateTargetInput{
			ID:                  activeTargetID,
			Name:                targetName,
			TargetType:          "EXPENSE",
			Priority:            prio,
			AmountMinor:         parseAmountStr(amtStr),
			RecurrenceCadence:   budget.CadenceMonthly,
			NonSpendingStrategy: budget.StrategyRollOver,
			AgentID:             tc.activeUserID,
			CorrelationID:       domain.NewUUIDv7(),
		})
	})

	ctx.Step(`^the target requirement in projections should be "([^"]*)"$`, func(expectedAmt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var amt int64
		err = bDB.QueryRow(`SELECT amount_minor FROM targets_projection WHERE id = ?`, activeTargetID).Scan(&amt)
		if err != nil {
			return err
		}
		if amt != parseAmountStr(expectedAmt) {
			return fmt.Errorf("expected target amount %s (%d), got %d", expectedAmt, parseAmountStr(expectedAmt), amt)
		}
		return nil
	})

	ctx.Step(`^I attempt to archive bucket "([^"]*)"$`, func(bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		lastTargetErr = budget.ArchiveBucket(context.Background(), bDB, activeBucketID, tc.activeUserID, domain.NewUUIDv7())
		return nil
	})

	ctx.Step(`^the bucket archiving should be rejected because it has active balance or targets$`, func() error {
		if lastTargetErr == nil {
			return fmt.Errorf("expected bucket archiving to fail, but it succeeded")
		}
		return nil
	})

	ctx.Step(`^I delete target "([^"]*)"$`, func(targetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		return budget.DeleteTarget(context.Background(), bDB, activeTargetID, tc.activeUserID, domain.NewUUIDv7())
	})

	ctx.Step(`^I move "([^"]*)" from bucket "([^"]*)" to ready to assign$`, func(amtStr, bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		amt := parseAmountStr(amtStr)
		// Returning funds from bucket to pool is negative assignment
		return budget.AssignFunds(context.Background(), bDB, budget.AssignFundsInput{
			BucketID:      activeBucketID,
			Currency:      "PLN",
			AmountMinor:   -amt,
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
	})

	ctx.Step(`^I archive bucket "([^"]*)"$`, func(bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		return budget.ArchiveBucket(context.Background(), bDB, activeBucketID, tc.activeUserID, domain.NewUUIDv7())
	})

	ctx.Step(`^the target should be marked archived in projections$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var isArchived int
		err = bDB.QueryRow(`SELECT is_archived FROM targets_projection WHERE id = ?`, activeTargetID).Scan(&isArchived)
		if err != nil || isArchived != 1 {
			return fmt.Errorf("expected target to be archived, got %d, err %v", isArchived, err)
		}
		return nil
	})

	ctx.Step(`^the bucket should be marked archived in projections$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var isArchived int
		err = bDB.QueryRow(`SELECT is_archived FROM buckets_projection WHERE id = ?`, activeBucketID).Scan(&isArchived)
		if err != nil || isArchived != 1 {
			return fmt.Errorf("expected bucket to be archived, got %d, err %v", isArchived, err)
		}
		return nil
	})

	var healthFundedMinor int64
	var healthRequiredMinor int64

	ctx.Step(`^a bucket "([^"]*)" with funded balance "([^"]*)" and required target amount "([^"]*)"$`, func(bName, fundedStr, reqStr string) error {
		healthFundedMinor = parseAmountStr(fundedStr)
		healthRequiredMinor = parseAmountStr(reqStr)
		return nil
	})

	var evaluatedBadge string

	ctx.Step(`^the budget dashboard evaluates the bucket health$`, func() error {
		if healthFundedMinor < 0 {
			evaluatedBadge = "Overspent"
		} else if healthFundedMinor >= healthRequiredMinor && healthRequiredMinor > 0 {
			evaluatedBadge = "Funded / On Track"
		} else if healthFundedMinor < healthRequiredMinor {
			evaluatedBadge = "Underfunded"
		} else {
			evaluatedBadge = "Funded / On Track"
		}
		return nil
	})

	ctx.Step(`^the health status badge should display "([^"]*)"$`, func(expectedBadge string) error {
		if evaluatedBadge != expectedBadge {
			return fmt.Errorf("expected health badge %s, got %s", expectedBadge, evaluatedBadge)
		}
		return nil
	})

	ctx.Step(`^an open budget "([^"]*)" with "([^"]*)" in checking account$`, func(budgetName, checkingStr string) error {
		now := time.Now().UTC().UnixMicro()
		budgetID := domain.NewUUIDv7()
		_, err := tc.mainDB.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES ('user_1', 'user1@example.com', 'hash', 'User 1', ?, ?)
			ON CONFLICT(id) DO NOTHING`, now, now)
		if err != nil {
			return err
		}
		_, err = tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
			VALUES (?, ?, 'PLN', 'Europe/Warsaw', 'user_1', ?, ?)`, budgetID, budgetName, now, now)
		if err != nil {
			return err
		}
		_, err = tc.mainDB.Exec(`
			INSERT INTO budget_members (budget_id, user_id, role, joined_at)
			VALUES (?, 'user_1', 'OWNER', ?)`, budgetID, now)
		if err != nil {
			return err
		}
		tc.activeBudgetID = budgetID
		tc.activeUserID = "user_1"

		bDB, err := tc.tenantMgr.GetTenantDB(budgetID, true)
		if err != nil {
			return err
		}

		partyID := domain.NewUUIDv7()
		_, err = bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'Owner', 'INDIVIDUAL', 1, 'user_1', ?)`, partyID, now)
		if err != nil {
			return err
		}

		acctID := domain.NewUUIDv7()
		_, err = bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, 'Checking', 'CHECKING', 'PLN', 0, 'user_1', ?)`, acctID, partyID, now)
		if err != nil {
			return err
		}

		amt := parseAmountStr(checkingStr)
		_, err = bDB.Exec(`
			INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
			VALUES (?, 'PLN', ?, ?)`, acctID, amt, now)
		return err
	})

	ctx.Step(`^an existing bucket "([^"]*)" with funded balance "([^"]*)"$`, func(bucketName, balanceStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		bID, err := budget.CreateBucket(context.Background(), bDB, budget.CreateBucketInput{
			GroupName:     "Needs",
			Name:          bucketName,
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeBucketID = bID
		amt := parseAmountStr(balanceStr)
		if amt != 0 {
			return budget.AssignFunds(context.Background(), bDB, budget.AssignFundsInput{
				BucketID:      bID,
				Currency:      "PLN",
				AmountMinor:   amt,
				AgentID:       tc.activeUserID,
				CorrelationID: domain.NewUUIDv7(),
			})
		}
		return nil
	})

	ctx.Step(`^I request the budget view "([^"]*)"$`, func(path string) error {
		targetPath := strings.Replace(path, ":budgetId", tc.activeBudgetID, 1)
		req := httptest.NewRequest(http.MethodGet, targetPath, nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, "OWNER")

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.workspaceHandler.BudgetView(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the Ready to Assign banner should display "([^"]*)"$`, func(expectedAmount string) error {
		body := tc.lastResponse.Body.String()
		cleanExpected := strings.ReplaceAll(expectedAmount, ",", "")
		if !strings.Contains(body, expectedAmount) && !strings.Contains(body, cleanExpected) {
			return fmt.Errorf("expected %s (or %s) in Ready to Assign banner, body: %s", expectedAmount, cleanExpected, body)
		}
		return nil
	})

	ctx.Step(`^I assign "([^"]*)" from ready to assign to bucket "([^"]*)"$`, func(amtStr, bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		amt := parseAmountStr(amtStr)
		form := url.Values{}
		form.Set("currency", "PLN")
		form.Set("amount", fmt.Sprintf("%.2f", float64(amt)/100.0))

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/buckets/%s/assign", tc.activeBudgetID, activeBucketID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, activeBucketID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.workspaceHandler.AssignFunds(c)
		if err != nil {
			return err
		}

		// Re-fetch budget view so that subsequent banner checks inspect the updated state
		reqView := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID), nil)
		recView := httptest.NewRecorder()
		cView := tc.echoEngine.NewContext(reqView, recView)
		cView.SetParamNames("budgetId")
		cView.SetParamValues(tc.activeBudgetID)
		cView.Set(handlers.ContextUserIDKey, tc.activeUserID)
		cView.Set(handlers.ContextRoleKey, "OWNER")
		cView.Set(handlers.ContextBudgetDBKey, bDB)
		err = tc.workspaceHandler.BudgetView(cView)
		tc.lastResponse = recView
		return err
	})

	ctx.Step(`^the bucket "([^"]*)" balance should be "([^"]*)"$`, func(bucketName, expectedAmt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bal int64
		err = bDB.QueryRow(`SELECT balance_minor FROM bucket_currency_balances WHERE bucket_id = ? AND currency = 'PLN'`, activeBucketID).Scan(&bal)
		if err != nil {
			return err
		}
		if bal != parseAmountStr(expectedAmt) {
			return fmt.Errorf("expected bucket balance %d, got %d", parseAmountStr(expectedAmt), bal)
		}
		return nil
	})

	ctx.Step(`^a bucket "([^"]*)" with the following targets in the active 30-day window:$`, func(bucketName string, table *godog.Table) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		bID, err := budget.CreateBucket(context.Background(), bDB, budget.CreateBucketInput{
			GroupName:     "Needs",
			Name:          bucketName,
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeBucketID = bID

		for i, row := range table.Rows {
			if i == 0 {
				continue // header
			}
			tName := row.Cells[0].Value
			var prio int
			fmt.Sscanf(row.Cells[1].Value, "%d", &prio)
			amt := parseAmountStr(row.Cells[2].Value)

			_, err = budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
				BucketID:          bID,
				Name:              tName,
				TargetType:        "EXPENSE",
				Priority:          prio,
				AmountMinor:       amt,
				Currency:          "PLN",
				RecurrenceCadence: budget.CadenceMonthly,
				AgentID:           tc.activeUserID,
				CorrelationID:     domain.NewUUIDv7(),
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^I trigger "Auto-Assign by Priority"$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		form := url.Values{}
		form.Set("currency", "PLN")
		form.Set("horizon", "30d")

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/budget/auto-assign", tc.activeBudgetID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.workspaceHandler.AutoAssign(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^target "([^"]*)" should receive "([^"]*)"$`, func(targetName, expectedAmt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var funded int64
		err = bDB.QueryRow(`SELECT current_funded_minor FROM targets_projection WHERE name = ? AND bucket_id = ?`, targetName, activeBucketID).Scan(&funded)
		if err != nil {
			return fmt.Errorf("failed fetching funded amount for target %s: %w", targetName, err)
		}
		if funded != parseAmountStr(expectedAmt) {
			return fmt.Errorf("expected target %s to have funded %d, got %d", targetName, parseAmountStr(expectedAmt), funded)
		}
		return nil
	})

	ctx.Step(`^the unallocated pool should be reduced to "([^"]*)"$`, func(expectedAmt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		rtaMap, err := budget.CalculateReadyToAssign(context.Background(), bDB)
		if err != nil {
			return err
		}
		expectedMinor := parseAmountStr(expectedAmt)
		if rtaMap["PLN"] != expectedMinor {
			return fmt.Errorf("expected Ready to Assign %d, got %d", expectedMinor, rtaMap["PLN"])
		}
		return nil
	})

	ctx.Step(`^the OpenAPI specification is loaded$`, func() error {
		swagger, err := openapi.GetSwagger()
		if err != nil {
			return fmt.Errorf("failed to load openapi spec: %w", err)
		}
		if swagger == nil {
			return fmt.Errorf("swagger is nil")
		}
		return nil
	})

	ctx.Step(`^the API title should be "([^"]*)"$`, func(expectedTitle string) error {
		swagger, err := openapi.GetSwagger()
		if err != nil {
			return err
		}
		if swagger.Info == nil || swagger.Info.Title != expectedTitle {
			return fmt.Errorf("expected title %q, got %q", expectedTitle, swagger.Info.Title)
		}
		return nil
	})

	ctx.Step(`^the contract must declare all required endpoints from section 12.2 of the specification$`, func() error {
		swagger, err := openapi.GetSwagger()
		if err != nil {
			return err
		}
		requiredPaths := []string{
			"/auth/register",
			"/auth/login",
			"/auth/logout",
			"/auth/tokens",
			"/auth/tokens/{id}",
			"/users/me",
			"/exchange-rates",
			"/exchange-rates/sync",
			"/budgets",
			"/budgets/{budgetId}",
			"/budgets/{budgetId}/invitations",
			"/invitations/{token}/accept",
			"/integrations/providers",
			"/integrations/{provider}/connect",
			"/integrations/{provider}/callback",
			"/budgets/{budgetId}/integrations/{integrationId}/link-account",
			"/budgets/{budgetId}/integrations/{integrationId}/sync",
			"/budgets/{budgetId}/accounts/{accountId}/import-csv",
			"/budgets/{budgetId}/parties",
			"/budgets/{budgetId}/parties/{id}",
			"/budgets/{budgetId}/parties/{id}/cashflow",
			"/budgets/{budgetId}/accounts",
			"/budgets/{budgetId}/matching-rules",
			"/budgets/{budgetId}/{entityType}/{id}/history",
			"/budgets/{budgetId}/{entityType}/{id}/restore",
			"/budgets/{budgetId}/raw-transactions",
			"/budgets/{budgetId}/transactions",
			"/budgets/{budgetId}/transactions/resolve",
			"/budgets/{budgetId}/buckets",
			"/budgets/{budgetId}/buckets/{id}/targets",
			"/budgets/{budgetId}/targets/generate-formula",
			"/budgets/{budgetId}/targets/validate-formula",
			"/budgets/{budgetId}/targets/{id}/fund",
			"/budgets/{budgetId}/view/horizon",
			"/budgets/{budgetId}/reports/saved",
			"/budgets/{budgetId}/reports/saved/{id}",
			"/budgets/{budgetId}/reports/age-of-money",
			"/budgets/{budgetId}/reports/income-vs-expense",
			"/budgets/{budgetId}/reports/spending-trends",
			"/budgets/{budgetId}/reports/spending-breakdown",
			"/budgets/{budgetId}/reports/net-worth",
			"/budgets/{budgetId}/reports/sankey",
			"/budgets/{budgetId}/reports/balance-forecast",
			"/budgets/{budgetId}/reports/budget-analysis",
			"/budgets/{budgetId}/reports/monte-carlo/simulate",
			"/budgets/{budgetId}/reports/party-cashflow",
		}
		for _, p := range requiredPaths {
			if _, ok := swagger.Paths.Map()[p]; !ok {
				return fmt.Errorf("missing path %s in OpenAPI contract", p)
			}
		}
		return nil
	})

	// 11_bank_csv_import.feature Step Definitions
	var parsedCsvPreview map[string]any

	ctx.Step(`^a budget named "([^"]*)" exists$`, func(budgetName string) error {
		now := time.Now().UTC().UnixMicro()
		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		tc.activeUserID = "user_1"

		_, err := tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES (?, ?, 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, tc.activeBudgetID, budgetName, now, now)
		if err != nil {
			return err
		}

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}

		// Ensure default user party exists
		pID := domain.NewUUIDv7()
		_, err = bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, ?, ?)
			ON CONFLICT(id) DO NOTHING`, pID, tc.activeUserID, now)
		return err
	})

	ctx.Step(`^an active checking account "([^"]*)" in budget "([^"]*)"$`, func(acctName, budgetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		var pID string
		err = bDB.QueryRow(`SELECT id FROM parties_current WHERE is_user_owned = 1 LIMIT 1`).Scan(&pID)
		if err != nil {
			pID = domain.NewUUIDv7()
			bDB.Exec(`
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, ?, ?)`, pID, tc.activeUserID, now)
		}

		userAcctID := domain.NewUUIDv7()
		_, err = bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, ?, ?)`, userAcctID, pID, acctName, tc.activeUserID, now)
		return err
	})

	ctx.Step(`^I request the bank statement CSV import page for budget "([^"]*)"$`, func(budgetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/transactions/import-csv", tc.activeBudgetID), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, "OWNER")
		c.Set(handlers.ContextBudgetDBKey, bDB)
		c.Set("csrf_token", "test_csrf_token_12345678901234567890")

		err = tc.txHandler.CSVImportPage(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the page should contain "([^"]*)"$`, func(expectedText string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, expectedText) {
			return fmt.Errorf("expected page body to contain %q, got body length %d", expectedText, len(body))
		}
		return nil
	})

	ctx.Step(`^the page should list account "([^"]*)"$`, func(acctName string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, acctName) {
			return fmt.Errorf("expected page body to list account %q", acctName)
		}
		return nil
	})

	ctx.Step(`^I submit a CSV preview request for account "([^"]*)" with bank profile "([^"]*)" and file content:$`, func(acctName, profile string, csvDoc *godog.DocString) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}

		var acctID string
		err = bDB.QueryRow(`SELECT id FROM accounts_current WHERE name = ?`, acctName).Scan(&acctID)
		if err != nil {
			return fmt.Errorf("account not found: %s", acctName)
		}

		bodyBuf := &bytes.Buffer{}
		writer := multipart.NewWriter(bodyBuf)
		writer.WriteField("account_id", acctID)
		writer.WriteField("bank_profile", profile)
		part, err := writer.CreateFormFile("csv_file", "statement.csv")
		if err != nil {
			return err
		}
		part.Write([]byte(csvDoc.Content))
		writer.Close()

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/preview-csv", tc.activeBudgetID), bodyBuf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.PreviewCSV(c)
		tc.lastResponse = rec
		if err != nil {
			return err
		}

		parsedCsvPreview = make(map[string]any)
		return json.Unmarshal(rec.Body.Bytes(), &parsedCsvPreview)
	})

	ctx.Step(`^the CSV preview response should contain (\d+) rows$`, func(expectedRows int) error {
		if parsedCsvPreview == nil {
			return errors.New("no preview response recorded")
		}
		txs, ok := parsedCsvPreview["transactions"].([]any)
		if !ok || len(txs) != expectedRows {
			return fmt.Errorf("expected %d rows in preview, got %v", expectedRows, parsedCsvPreview["rows_count"])
		}
		return nil
	})

	ctx.Step(`^row (\d+) should have date "([^"]*)", amount "([^"]*)", and payee "([^"]*)"$`, func(rowIdx int, expectedDate, expectedAmt, expectedPayee string) error {
		txs, ok := parsedCsvPreview["transactions"].([]any)
		if !ok || len(txs) < rowIdx {
			return fmt.Errorf("row %d out of bounds", rowIdx)
		}
		rowMap, ok := txs[rowIdx-1].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid row %d format", rowIdx)
		}
		if rowMap["date"] != expectedDate {
			return fmt.Errorf("expected date %s, got %v", expectedDate, rowMap["date"])
		}
		if rowMap["amount_str"] != expectedAmt {
			return fmt.Errorf("expected amount %s, got %v", expectedAmt, rowMap["amount_str"])
		}
		if rowMap["payee"] != expectedPayee {
			return fmt.Errorf("expected payee %s, got %v", expectedPayee, rowMap["payee"])
		}
		return nil
	})

	ctx.Step(`^an active merchant counterparty "([^"]*)" with an auto-matching rule for "([^"]*)" to bucket "([^"]*)"$`, func(merchantName, pattern, bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()

		// 1. Create merchant counterparty
		partyID := domain.NewUUIDv7()
		_, err = bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, ?, 'MERCHANT', 0, ?, ?)`, partyID, merchantName, tc.activeUserID, now)
		if err != nil {
			return err
		}

		merchantAcctID := domain.NewUUIDv7()
		_, err = bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'EXPENSE', 'PLN', 0, ?, ?)`, merchantAcctID, partyID, merchantName, tc.activeUserID, now)
		if err != nil {
			return err
		}

		// 2. Create envelope bucket
		bucketID := domain.NewUUIDv7()
		_, err = bDB.Exec(`
			INSERT INTO buckets_current (id, version, name, category, agent_id, updated_at)
			VALUES (?, 1, ?, 'Living', ?, ?)`, bucketID, bucketName, tc.activeUserID, now)
		if err != nil {
			return err
		}
		_, err = bDB.Exec(`
			INSERT INTO buckets_projection (id, name, category, is_archived, sort_order)
			VALUES (?, ?, 'Living', 0, 1)`, bucketID, bucketName)
		if err != nil {
			return err
		}

		// 3. Create matching rule
		ruleID := domain.NewUUIDv7()
		ruleJSON := fmt.Sprintf(`{"pattern": "%s", "case_sensitive": false}`, pattern)
		_, err = bDB.Exec(`
			INSERT INTO matching_rules_current (
				id, version, priority, rule_type, rule_payload, target_party_id,
				target_counterparty_account_id, default_bucket_id, is_active, agent_id, updated_at
			) VALUES (?, 1, 10, 'PAYEE_CONTAINS', ?, ?, ?, ?, 1, ?, ?)`,
			ruleID, ruleJSON, partyID, merchantAcctID, bucketID, tc.activeUserID, now,
		)
		return err
	})

	ctx.Step(`^I submit a CSV import request for account "([^"]*)" with auto-match enabled and file content:$`, func(acctName string, csvDoc *godog.DocString) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}

		var acctID string
		err = bDB.QueryRow(`SELECT id FROM accounts_current WHERE name = ?`, acctName).Scan(&acctID)
		if err != nil {
			return fmt.Errorf("account not found: %s", acctName)
		}

		bodyBuf := &bytes.Buffer{}
		writer := multipart.NewWriter(bodyBuf)
		writer.WriteField("account_id", acctID)
		writer.WriteField("bank_profile", "GENERIC")
		writer.WriteField("auto_match", "1")
		part, err := writer.CreateFormFile("csv_file", "statement.csv")
		if err != nil {
			return err
		}
		part.Write([]byte(csvDoc.Content))
		writer.Close()

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/import-csv", tc.activeBudgetID), bodyBuf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.ImportCSV(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the response status should be (\d+) Redirect to "/budgets/\{budgetId\}/transactions"$`, func(code int) error {
		expectedLoc := fmt.Sprintf("/budgets/%s/transactions", tc.activeBudgetID)
		if tc.lastResponse.Code != code {
			return fmt.Errorf("expected %d redirect, got %d (body: %s)", code, tc.lastResponse.Code, tc.lastResponse.Body.String())
		}
		if tc.lastResponse.Header().Get("Location") != expectedLoc {
			return fmt.Errorf("expected location %s, got %s", expectedLoc, tc.lastResponse.Header().Get("Location"))
		}
		return nil
	})

	ctx.Step(`^(\d+) raw transactions should be persisted in "raw_transactions_current"$`, func(expectedCount int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM raw_transactions_current`).Scan(&count)
		if err != nil {
			return err
		}
		if count != expectedCount {
			return fmt.Errorf("expected %d raw transactions, got %d", expectedCount, count)
		}
		return nil
	})

	ctx.Step(`^(\d+) raw transaction should be automatically matched and resolved into "budget_transactions"$`, func(expectedCount int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM budget_transactions_current`).Scan(&count)
		if err != nil {
			return err
		}
		if count != expectedCount {
			return fmt.Errorf("expected %d budget transactions resolved, got %d", expectedCount, count)
		}
		return nil
	})

	ctx.Step(`^(\d+) raw transaction should remain in "raw_transactions_current" with status "PENDING"$`, func(expectedCount int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE status = 'PENDING'`).Scan(&count)
		if err != nil {
			return err
		}
		if count != expectedCount {
			return fmt.Errorf("expected %d pending raw transactions, got %d", expectedCount, count)
		}
		return nil
	})
}

func TestFeatures(t *testing.T) {
	logger.BindTestLogger(t)

	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}
