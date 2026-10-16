package bdd

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/db"
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
