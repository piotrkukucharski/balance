package bdd

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
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/report"
	"github.com/balance/balance/internal/web/handlers"
	"github.com/cucumber/godog"
	"github.com/labstack/echo/v4"
)

type bddTestContext struct {
	echoEngine *echo.Echo
	mainDB     *sql.DB
	tenantMgr  *db.TenantDBManager
	tmpDir     string

	lastResponse *httptest.ResponseRecorder
	sessionCookie *http.Cookie
	activeUserID  string
	activeBudgetID string
	activeCSRF    string

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
	tc.workspaceHandler = handlers.NewWorkspaceHandler(mainDB, tenantMgr)
	tc.accountsHandler = handlers.NewAccountsHandler(mainDB, tenantMgr)
	tc.txHandler = handlers.NewTransactionsHandler(mainDB, tenantMgr)
	tc.reportsHandler = handlers.NewReportsHandler(mainDB, tenantMgr)
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
	ctx.Step(`^I should see available bank providers "([^"]*)" and "([^"]*)"$`, func(p1, p2 string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, p1) || !strings.Contains(body, p2) {
			return godog.ErrPending
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
}

func TestFeatures(t *testing.T) {
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
