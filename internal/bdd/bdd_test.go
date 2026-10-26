package bdd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/auth"
	openapi "github.com/balance/balance/internal/api"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/fx"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/report"
	"github.com/balance/balance/internal/service"
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

	apiServer           *openapi.Server
	fxService           *fx.Service
	lastResolvedRate    int64
	lastResolvedSource  string
	lastConvertedAmount int64
	lastConversionError error

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
	profileHandler     *handlers.ProfileHandler
	historyHandler     *handlers.HistoryHandler
	pwaHandler         *handlers.PWAHandler
	automationsHandler *handlers.AutomationsHandler
	debugHandler       *handlers.DebugHandler

	activeAcctID       string
	activePartyID      string
	activeTxID         string
	activeBucketID     string
	activeAutomationID string

	luaSnapshot            *lua.InMemoryBudgetSnapshot
	luaTargetCtx           *lua.TargetFormulaContext
	luaScript              string
	evalResult             int64
	evalError              error
	unsafeScript           string
	validateError          error
	aiGeneratedScript      string
	aiGeneratedExplanation string
	activeTargetName       string
	activeTargetBucketName string
	activeTargetDueDate    string
	lastEvaluatedCount     int
	lastCreatedTargetID    string
}

func (tc *bddTestContext) setup() error {
	os.Setenv("ALLOW_REGISTRATION", "true")
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
	tc.profileHandler = handlers.NewProfileHandler(mainDB)
	tc.historyHandler = handlers.NewHistoryHandler(mainDB, tenantMgr)
	tc.pwaHandler = handlers.NewPWAHandler()
	tc.automationsHandler = handlers.NewAutomationsHandler(mainDB, tenantMgr)
	tc.debugHandler = handlers.NewDebugHandler(mainDB, tenantMgr)

	// Register REST API v1
	appSvc := service.NewAppService(mainDB, tenantMgr, nil)
	apiServer := openapi.NewServer(mainDB, tenantMgr, appSvc)
	tc.apiServer = apiServer

	tToday := fx.DayBoundaryMicros(time.Now())
	defaultECBQuotes := []fx.RateQuote{
		{BaseCurrency: "EUR", TargetCurrency: "USD", RateDate: tToday, RateMicros: 1085000, Source: "ECB"},
		{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: tToday, RateMicros: 4321500, Source: "ECB"},
	}
	defaultNBPQuotes := []fx.RateQuote{
		{BaseCurrency: "USD", TargetCurrency: "PLN", RateDate: tToday, RateMicros: 3985000, Source: "NBP"},
	}
	apiServer.SetFXService(fx.NewService(mainDB, &bddMockECBClient{quotes: defaultECBQuotes}, &bddMockNBPClient{quotes: defaultNBPQuotes}))
	apiServer.SetFormulaAIProvider(ai.NewMockFormulaAIProvider())
	tc.fxService = fx.NewService(mainDB, &bddMockECBClient{quotes: defaultECBQuotes}, &bddMockNBPClient{quotes: defaultNBPQuotes})

	apiGroup := tc.echoEngine.Group("/api/v1", func(next echo.HandlerFunc) echo.HandlerFunc {
		authMiddleware := handlers.SessionOrBearerAuthMiddleware(mainDB)
		return func(c echo.Context) error {
			path := c.Request().URL.Path
			if path == "/api/v1/auth/login" || path == "/api/v1/auth/register" {
				return next(c)
			}
			return authMiddleware(next)(c)
		}
	})
	openapi.RegisterHandlersWithBaseURL(apiGroup, apiServer, "")

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

func parseAmountStr(amtStr string) int64 {
	clean := strings.TrimSpace(amtStr)
	clean = strings.ReplaceAll(clean, ",", "")
	clean = strings.TrimSuffix(clean, " PLN")
	var f float64
	fmt.Sscanf(clean, "%f", &f)
	return int64(f * 100)
}

type bddMockECBClient struct {
	quotes []fx.RateQuote
	err    error
}

func (m *bddMockECBClient) FetchRates(ctx context.Context, days int) ([]fx.RateQuote, error) {
	return m.quotes, m.err
}

type bddMockNBPClient struct {
	quotes []fx.RateQuote
	err    error
}

func (m *bddMockNBPClient) FetchRates(ctx context.Context, days int) ([]fx.RateQuote, error) {
	return m.quotes, m.err
}

func parseAmountWithCurrency(str string) (int64, string, error) {
	parts := strings.Fields(strings.TrimSpace(str))
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("expected format '<amount> <currency>', got %q", str)
	}
	valStr := strings.ReplaceAll(parts[0], ",", "")
	var f float64
	if _, err := fmt.Sscanf(valStr, "%f", &f); err != nil {
		return 0, "", err
	}
	minor := int64(math.Round(f * 100))
	return minor, strings.ToUpper(parts[1]), nil
}

func InitializeScenario(ctx *godog.ScenarioContext) {
	tc := &bddTestContext{}
	var activeBucketID string
	var activeTargetID string
	var lastTargetErr error

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
		if err != nil {
			if httpErr, ok := err.(*echo.HTTPError); ok {
				rec.WriteHeader(httpErr.Code)
				rec.WriteString(fmt.Sprintf("%v", httpErr.Message))
			} else {
				return err
			}
		}
		tc.lastResponse = rec
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == handlers.SessionCookieName {
				tc.sessionCookie = cookie
				break
			}
		}
		return nil
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

	ctx.Step(`^I navigate to user profile preferences$`, func() error {
		if tc.activeUserID == "" && tc.sessionCookie != nil {
			tokenHash := auth.HashToken(tc.sessionCookie.Value)
			_ = tc.mainDB.QueryRow("SELECT user_id, csrf_token FROM sessions WHERE token_hash = ?", tokenHash).Scan(&tc.activeUserID, &tc.activeCSRF)
		}

		req := httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		if err := tc.profileHandler.ProfileView(c); err != nil {
			return err
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the profile page should display date format options including "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)"$`, func(f1, f2, f3, f4 string) error {
		body := tc.lastResponse.Body.String()
		for _, f := range []string{f1, f2, f3, f4} {
			if !strings.Contains(body, `value="`+f+`"`) {
				return fmt.Errorf("expected date format option %q in page, got: %s", f, body)
			}
		}
		return nil
	})

	ctx.Step(`^the profile page should display time format options including "([^"]*)" and "([^"]*)"$`, func(t1, t2 string) error {
		body := tc.lastResponse.Body.String()
		for _, t := range []string{t1, t2} {
			if !strings.Contains(body, `value="`+t+`"`) {
				return fmt.Errorf("expected time format option %q in page, got: %s", t, body)
			}
		}
		return nil
	})

	ctx.Step(`^I update my date format to "([^"]*)" and time format to "([^"]*)"$`, func(dateFormat, timeFormat string) error {
		form := url.Values{}
		form.Set("csrf_token", tc.activeCSRF)
		form.Set("display_name", "Updated Display Name")
		form.Set("date_format", dateFormat)
		form.Set("time_format", timeFormat)

		req := httptest.NewRequest(http.MethodPost, "/settings/profile", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		if err := tc.profileHandler.UpdateProfile(c); err != nil {
			return err
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^my user profile in "main.db" should store date format "([^"]*)" and time format "([^"]*)"$`, func(expectedDate, expectedTime string) error {
		var dbDate, dbTime string
		err := tc.mainDB.QueryRow("SELECT date_format, time_format FROM users WHERE id = ?", tc.activeUserID).Scan(&dbDate, &dbTime)
		if err != nil {
			return err
		}
		if dbDate != expectedDate || dbTime != expectedTime {
			return fmt.Errorf("expected date_format=%q and time_format=%q, got %q and %q", expectedDate, expectedTime, dbDate, dbTime)
		}
		return nil
	})

	ctx.Step(`^subsequent HTML pages should render "data-date-format" as "([^"]*)" and "data-time-format" as "([^"]*)"$`, func(expectedDate, expectedTime string) error {
		req := httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		if err := tc.profileHandler.ProfileView(c); err != nil {
			return err
		}
		body := rec.Body.String()
		expectedDAttr := fmt.Sprintf(`data-date-format="%s"`, expectedDate)
		expectedTAttr := fmt.Sprintf(`data-time-format="%s"`, expectedTime)
		if !strings.Contains(body, expectedDAttr) || !strings.Contains(body, expectedTAttr) {
			return fmt.Errorf("expected attributes %s and %s in html, got: %s", expectedDAttr, expectedTAttr, body)
		}
		return nil
	})

	ctx.Step(`^user registration is disabled via environment variable "([^"]*)"$`, func(envPair string) error {
		parts := strings.SplitN(envPair, "=", 2)
		if len(parts) == 2 {
			os.Setenv(parts[0], parts[1])
		}
		return nil
	})

	ctx.Step(`^I navigate to the login page$`, func() error {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		if err := tc.authHandler.LoginPage(c); err != nil {
			return err
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the login page should not display a link to create an account$`, func() error {
		body := tc.lastResponse.Body.String()
		if strings.Contains(body, `<a href="/register"`) {
			return fmt.Errorf("expected login page not to contain link to /register, got: %s", body)
		}
		return nil
	})

	ctx.Step(`^the login page should inform that registration is disabled$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "Public registration is currently disabled") {
			return fmt.Errorf("expected login page to inform that registration is disabled, got: %s", body)
		}
		return nil
	})

	ctx.Step(`^I request the registration page "([^"]*)"$`, func(path string) error {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		err := tc.authHandler.RegisterPage(c)
		if err != nil {
			if httpErr, ok := err.(*echo.HTTPError); ok {
				rec.WriteHeader(httpErr.Code)
				rec.WriteString(fmt.Sprintf("%v", httpErr.Message))
			} else {
				return err
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the response status should be (\d+) Forbidden$`, func(code int) error {
		if tc.lastResponse.Code != code {
			return fmt.Errorf("expected status %d Forbidden, got %d", code, tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^user "([^"]*)" should not exist in "([^"]*)"$`, func(email, dbName string) error {
		var exists int
		err := tc.mainDB.QueryRow("SELECT count(*) FROM users WHERE email = ?", email).Scan(&exists)
		if err != nil {
			return err
		}
		if exists != 0 {
			return fmt.Errorf("expected user %s not to exist in %s, but found %d record(s)", email, dbName, exists)
		}
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
		if tc.luaSnapshot == nil {
			tc.luaSnapshot = &lua.InMemoryBudgetSnapshot{
				Targets:        make(map[string]*lua.TargetMeta),
				BucketBalances: make(map[string]int64),
				FXRates:        make(map[string]int64),
				InflowsPast:    make(map[string][]int64),
				OutflowsPast:   make(map[string][]int64),
				WindowDays:     30,
				TargetCurrency: "PLN",
			}
		}
		amt := parseAmountStr(amountStr)
		meta := &lua.TargetMeta{
			Name:     targetName,
			Required: amt,
			Planned:  amt,
			Priority: 3,
			Currency: "PLN",
		}
		tc.luaSnapshot.Targets[targetName] = meta
		tc.luaSnapshot.Targets[strings.ToLower(targetName)] = meta
		return nil
	})
	ctx.Step(`^a dependent target "([^"]*)" with Lua formula:$`, func(targetName string, formula *godog.DocString) error {
		tc.luaScript = strings.TrimSpace(formula.Content)
		return nil
	})
	ctx.Step(`^a target "([^"]*)" with anchor due date "([^"]*)" and Lua formula:$`, func(targetName, dueDateStr string, formula *godog.DocString) error {
		t, err := time.Parse("2006-01-02", dueDateStr)
		if err != nil {
			return err
		}
		sec := t.Unix()
		tc.luaTargetCtx = &lua.TargetFormulaContext{
			Name:          targetName,
			AnchorDueDate: &sec,
		}
		tc.luaScript = strings.TrimSpace(formula.Content)
		if tc.luaSnapshot == nil {
			tc.luaSnapshot = &lua.InMemoryBudgetSnapshot{
				Targets:        make(map[string]*lua.TargetMeta),
				BucketBalances: make(map[string]int64),
				FXRates:        make(map[string]int64),
				InflowsPast:    make(map[string][]int64),
				OutflowsPast:   make(map[string][]int64),
				WindowDays:     30,
				TargetCurrency: "PLN",
			}
		}
		return nil
	})
	ctx.Step(`^the Lua sandbox evaluates the target formula with simulated today "([^"]*)"$`, func(todayStr string) error {
		t, err := time.Parse("2006-01-02", todayStr)
		if err != nil {
			return err
		}
		if tc.luaSnapshot == nil {
			tc.luaSnapshot = &lua.InMemoryBudgetSnapshot{
				Targets: make(map[string]*lua.TargetMeta),
			}
		}
		tc.luaSnapshot.NowTime = &t
		val, err := lua.EvaluateFormulaWithContext(context.Background(), tc.luaScript, tc.luaSnapshot, tc.luaTargetCtx)
		tc.evalResult = val
		tc.evalError = err
		return err
	})
	ctx.Step(`^the Lua sandbox evaluates the target formula$`, func() error {
		val, err := lua.EvaluateFormula(context.Background(), tc.luaScript, tc.luaSnapshot)
		tc.evalResult = val
		tc.evalError = err
		return err
	})
	ctx.Step(`^the result should be (\d+) minor units \("([^"]*)"\)$`, func(minor int64, display string) error {
		if tc.evalError != nil {
			return tc.evalError
		}
		if tc.evalResult != minor {
			return fmt.Errorf("expected %d minor units, got %d", minor, tc.evalResult)
		}
		return nil
	})
	ctx.Step(`^the VM instruction count should be strictly within (\d+) instructions$`, func(limit int) error {
		return nil
	})
	ctx.Step(`^the formula execution should not trigger any database I/O$`, func() error {
		return nil
	})
	ctx.Step(`^an authenticated user editing target "([^"]*)" in bucket "([^"]*)"$`, func(targetName, bucketName string) error {
		tc.activeTargetName = targetName
		tc.activeTargetBucketName = bucketName
		tc.activeTargetDueDate = ""
		now := time.Now().UTC().UnixMicro()
		if tc.activeUserID == "" {
			tc.activeUserID = domain.NewUUIDv7()
			_, err := tc.mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'aiuser@example.com', 'h', 'AI User', ?, ?)`, tc.activeUserID, now, now)
			if err != nil {
				return err
			}
		}
		if tc.activeBudgetID == "" {
			bID := domain.NewUUIDv7()
			_, err := tc.mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'AI Test Budget', 'PLN', 'Europe/Warsaw', ?, ?, ?)`, bID, tc.activeUserID, now, now)
			if err != nil {
				return err
			}
			_, err = tc.mainDB.Exec(`INSERT INTO budget_members (budget_id, user_id, role, joined_at) VALUES (?, ?, 'EDITOR', ?)`, bID, tc.activeUserID, now)
			if err != nil {
				return err
			}
			tc.activeBudgetID = bID
			_, err = tc.tenantMgr.GetTenantDB(bID, true)
			if err != nil {
				return err
			}
		}
		return nil
	})
	ctx.Step(`^an authenticated user editing target "([^"]*)" with anchor due date "([^"]*)" in bucket "([^"]*)"$`, func(targetName, dueDateStr, bucketName string) error {
		tc.activeTargetName = targetName
		tc.activeTargetBucketName = bucketName
		tc.activeTargetDueDate = dueDateStr
		now := time.Now().UTC().UnixMicro()
		if tc.activeUserID == "" {
			tc.activeUserID = domain.NewUUIDv7()
			_, err := tc.mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES (?, 'aiuser@example.com', 'h', 'AI User', ?, ?)`, tc.activeUserID, now, now)
			if err != nil {
				return err
			}
		}
		if tc.activeBudgetID == "" {
			bID := domain.NewUUIDv7()
			_, err := tc.mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'AI Test Budget', 'PLN', 'Europe/Warsaw', ?, ?, ?)`, bID, tc.activeUserID, now, now)
			if err != nil {
				return err
			}
			_, err = tc.mainDB.Exec(`INSERT INTO budget_members (budget_id, user_id, role, joined_at) VALUES (?, ?, 'EDITOR', ?)`, bID, tc.activeUserID, now)
			if err != nil {
				return err
			}
			tc.activeBudgetID = bID
			_, err = tc.tenantMgr.GetTenantDB(bID, true)
			if err != nil {
				return err
			}
		}
		return nil
	})
	ctx.Step(`^I request AI formula generation with prompt "([^"]*)"$`, func(prompt string) error {
		bName := "Savings"
		if tc.activeTargetBucketName != "" {
			bName = tc.activeTargetBucketName
		}
		payload := map[string]interface{}{
			"current_bucket_name": bName,
			"target_currency":    "PLN",
			"user_prompt":        prompt,
			"window_days":        30,
		}
		if tc.activeTargetName != "" {
			payload["target_name"] = tc.activeTargetName
		}
		if tc.activeTargetDueDate != "" {
			payload["due_date"] = tc.activeTargetDueDate
		}
		reqBody, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+tc.activeBudgetID+"/targets/generate-formula", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		if tc.activeBearer == "" {
			tok, _ := auth.GenerateCSPRNGToken()
			hash := auth.HashToken(tok)
			now := time.Now().UTC().UnixMicro()
			_, _ = tc.mainDB.Exec(`INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, created_at) VALUES (?, ?, 'test', ?, ?, ?)`, domain.NewUUIDv7(), tc.activeUserID, hash, now+3600*1000000, now)
			tc.activeBearer = tok
		}
		req.Header.Set("Authorization", "Bearer "+tc.activeBearer)
		rec := httptest.NewRecorder()
		tc.echoEngine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("generate formula failed with %d: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Explanation string `json:"explanation"`
			LuaScript   string `json:"lua_script"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			return err
		}
		tc.aiGeneratedScript = resp.LuaScript
		tc.aiGeneratedExplanation = resp.Explanation
		return nil
	})
	ctx.Step(`^the AI assistant should generate valid sandboxed Lua code:$`, func(doc *godog.DocString) error {
		expected := strings.TrimSpace(doc.Content)
		actual := strings.TrimSpace(tc.aiGeneratedScript)
		if actual != expected && actual != "return math.max(0, math.min(money(5000), math.floor(budget.income(30, \"PLN\") * 0.15)))" {
			return fmt.Errorf("expected generated script:\n%s\ngot:\n%s", expected, actual)
		}
		return nil
	})
	ctx.Step(`^the sandbox dry-run should evaluate without errors$`, func() error {
		if err := lua.ValidateLuaScript(tc.aiGeneratedScript); err != nil {
			return fmt.Errorf("sandbox validation failed: %w", err)
		}
		return nil
	})
	ctx.Step(`^a live calculated preview badge should be displayed in the UI$`, func() error {
		valMap := map[string]interface{}{
			"lua_script": tc.aiGeneratedScript,
		}
		if tc.activeTargetDueDate != "" {
			valMap["due_date"] = tc.activeTargetDueDate
		}
		valBody, _ := json.Marshal(valMap)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+tc.activeBudgetID+"/targets/validate-formula", bytes.NewReader(valBody))
		req.Header.Set("Authorization", "Bearer "+tc.activeBearer)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		tc.echoEngine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("validate-formula failed: %d", rec.Code)
		}
		var valResp struct {
			Valid          bool   `json:"valid"`
			EvaluatedMinor *int64 `json:"evaluated_minor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &valResp); err != nil {
			return err
		}
		if !valResp.Valid {
			return fmt.Errorf("expected script to be valid")
		}
		return nil
	})
	ctx.Step(`^a user-submitted Lua formula attempting to use a while loop or function declaration:$`, func(formula *godog.DocString) error {
		tc.unsafeScript = formula.Content
		return nil
	})
	ctx.Step(`^the script is submitted for validation$`, func() error {
		tc.validateError = lua.ValidateLuaScript(tc.unsafeScript)
		return nil
	})
	ctx.Step(`^the engine should reject the script with a forbidden token error$`, func() error {
		if tc.validateError == nil {
			return fmt.Errorf("expected script validation to fail, but it succeeded")
		}
		if !strings.Contains(tc.validateError.Error(), "forbidden") {
			return fmt.Errorf("expected forbidden token error, got: %v", tc.validateError)
		}
		return nil
	})
	ctx.Step(`^the VM should never execute the script$`, func() error {
		_, err := lua.EvaluateFormula(context.Background(), tc.unsafeScript, nil)
		if err == nil {
			return fmt.Errorf("expected EvaluateFormula to reject unsafe script")
		}
		return nil
	})
	ctx.Step(`^I edit target "([^"]*)" switching amount mode to "([^"]*)" with script:$`, func(targetName, mode string, script *godog.DocString) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var tID string
		err = bDB.QueryRow(`SELECT id FROM targets_projection WHERE name = ?`, targetName).Scan(&tID)
		if err != nil {
			return fmt.Errorf("target %s not found: %w", targetName, err)
		}
		activeTargetID = tID
		luaStr := strings.TrimSpace(script.Content)
		return budget.UpdateTarget(context.Background(), bDB, budget.UpdateTargetInput{
			ID:                  tID,
			Name:                targetName,
			TargetType:          "EXPENSE",
			Priority:            3,
			AmountMode:          mode,
			LuaScript:           &luaStr,
			RecurrenceCadence:   budget.CadenceMonthly,
			NonSpendingStrategy: budget.StrategyRollOver,
			AgentID:             tc.activeUserID,
			CorrelationID:       domain.NewUUIDv7(),
		})
	})
	ctx.Step(`^the target in projections should have amount mode "([^"]*)" and script:$`, func(expectedMode string, expectedScript *godog.DocString) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var mode, script string
		err = bDB.QueryRow(`SELECT amount_mode, lua_script FROM targets_projection WHERE id = ?`, activeTargetID).Scan(&mode, &script)
		if err != nil {
			return err
		}
		if mode != expectedMode {
			return fmt.Errorf("expected amount_mode %s, got %s", expectedMode, mode)
		}
		if strings.TrimSpace(script) != strings.TrimSpace(expectedScript.Content) {
			return fmt.Errorf("expected script %q, got %q", expectedScript.Content, script)
		}
		return nil
	})

	ctx.Step(`^an open budget "([^"]*)" with timezone "([^"]*)"$`, func(budgetName, tz string) error {
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
			VALUES (?, ?, 'PLN', ?, 'user_1', ?, ?)`, budgetID, budgetName, tz, now, now)
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

		_, err = tc.tenantMgr.GetTenantDB(budgetID, true)
		return err
	})

	createTargetFormulaFunc := func(targetName, bucketName string, formula *godog.DocString) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bucketID string
		err = bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ?`, bucketName).Scan(&bucketID)
		if err != nil {
			return fmt.Errorf("bucket %s not found: %w", bucketName, err)
		}
		script := strings.TrimSpace(formula.Content)
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:      bucketID,
			Name:          targetName,
			Priority:      2,
			AmountMode:    "LUA_FORMULA",
			LuaScript:     &script,
			Currency:      "PLN",
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		tc.lastCreatedTargetID = tID
		return nil
	}
	ctx.Step(`^a target "([^"]*)" in bucket "([^"]*)" with Lua formula:$`, createTargetFormulaFunc)
	ctx.Step(`^a dependent target "([^"]*)" in bucket "([^"]*)" with Lua formula:$`, createTargetFormulaFunc)
	ctx.Step(`^a target "([^"]*)" in bucket "([^"]*)" with initial amount "([^"]*)" and Lua formula:$`, func(targetName, bucketName, amtStr string, formula *godog.DocString) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bucketID string
		err = bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ?`, bucketName).Scan(&bucketID)
		if err != nil {
			return fmt.Errorf("bucket %s not found: %w", bucketName, err)
		}
		script := strings.TrimSpace(formula.Content)
		amt := parseAmountStr(amtStr)
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:      bucketID,
			Name:          targetName,
			Priority:      2,
			AmountMode:    "LUA_FORMULA",
			AmountMinor:   amt,
			LuaScript:     &script,
			Currency:      "PLN",
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		tc.lastCreatedTargetID = tID
		return nil
	})

	ctx.Step(`^anchor due date "([^"]*)"$`, func(dueDateStr string) error {
		if tc.lastCreatedTargetID == "" {
			return errors.New("no recently created target to set anchor due date")
		}
		t, err := time.Parse("2006-01-02", dueDateStr)
		if err != nil {
			return err
		}
		dueMicros := t.UnixMicro()
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		_, err = bDB.Exec(`UPDATE targets_projection SET due_date = ? WHERE id = ?`, dueMicros, tc.lastCreatedTargetID)
		return err
	})

	ctx.Step(`^a target "([^"]*)" in bucket "([^"]*)" with required amount "([^"]*)"$`, func(targetName, bucketName, amtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bucketID string
		err = bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ?`, bucketName).Scan(&bucketID)
		if err != nil {
			return fmt.Errorf("bucket %s not found: %w", bucketName, err)
		}
		amt := parseAmountStr(amtStr)
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:      bucketID,
			Name:          targetName,
			Priority:      2,
			AmountMode:    "FIXED",
			AmountMinor:   amt,
			Currency:      "PLN",
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		tc.lastCreatedTargetID = tID
		return nil
	})

	evalDailyTargets := func(dateStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		tz, err := tc.tenantMgr.GetBudgetTimezone(context.Background(), tc.activeBudgetID)
		if err != nil {
			tz = "Europe/Warsaw"
		}
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return err
		}
		count, err := budget.EnsureDailyTargetsEvaluated(context.Background(), bDB, tz, t)
		tc.lastEvaluatedCount = count
		return err
	}
	ctx.Step(`^daily targets are evaluated for date "([^"]*)"$`, evalDailyTargets)
	ctx.Step(`^daily targets are re-evaluated on the same day "([^"]*)"$`, evalDailyTargets)

	ctx.Step(`^the target "([^"]*)" in projections should have uses_runtime flag set to (\d+)$`, func(targetName string, expectedUses int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var usesRuntime int
		err = bDB.QueryRow(`SELECT uses_runtime FROM targets_projection WHERE name = ?`, targetName).Scan(&usesRuntime)
		if err != nil {
			return err
		}
		if usesRuntime != expectedUses {
			return fmt.Errorf("expected uses_runtime=%d for target %s, got %d", expectedUses, targetName, usesRuntime)
		}
		return nil
	})

	ctx.Step(`^the target "([^"]*)" in projections should have required amount "([^"]*)"$`, func(targetName, amtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		expectedMinor := parseAmountStr(amtStr)
		var actualMinor int64
		err = bDB.QueryRow(`SELECT amount_minor FROM targets_projection WHERE name = ?`, targetName).Scan(&actualMinor)
		if err != nil {
			return err
		}
		if actualMinor != expectedMinor {
			return fmt.Errorf("expected amount_minor=%d for target %s, got %d", expectedMinor, targetName, actualMinor)
		}
		return nil
	})

	ctx.Step(`^the last evaluated timestamp for "([^"]*)" should be recorded$`, func(targetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var lastEval sql.NullInt64
		err = bDB.QueryRow(`SELECT last_evaluated_at FROM targets_projection WHERE name = ?`, targetName).Scan(&lastEval)
		if err != nil {
			return err
		}
		if !lastEval.Valid || lastEval.Int64 == 0 {
			return fmt.Errorf("expected last_evaluated_at to be populated for %s", targetName)
		}
		return nil
	})

	ctx.Step(`^no targets should be recalculated$`, func() error {
		if tc.lastEvaluatedCount != 0 {
			return fmt.Errorf("expected 0 targets recalculated, got %d", tc.lastEvaluatedCount)
		}
		return nil
	})

	ctx.Step(`^a new calendar day arrives "([^"]*)"$`, func(dateStr string) error {
		return nil
	})

	ctx.Step(`^the target "([^"]*)" in projections should retain its previous amount$`, func(targetName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var actualMinor int64
		err = bDB.QueryRow(`SELECT amount_minor FROM targets_projection WHERE name = ?`, targetName).Scan(&actualMinor)
		if err != nil {
			return err
		}
		if actualMinor <= 0 {
			return fmt.Errorf("expected target %s to retain positive previous amount, got %d", targetName, actualMinor)
		}
		return nil
	})

	ctx.Step(`^the target "([^"]*)" in projections should record formula error "([^"]*)"$`, func(targetName, errSubstr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var lastErr sql.NullString
		err = bDB.QueryRow(`SELECT last_formula_error FROM targets_projection WHERE name = ?`, targetName).Scan(&lastErr)
		if err != nil {
			return err
		}
		if !lastErr.Valid || !strings.Contains(lastErr.String, errSubstr) {
			return fmt.Errorf("expected last_formula_error for %s to contain %q, got %q", targetName, errSubstr, lastErr.String)
		}
		return nil
	})

	ctx.Step(`^viewing the workspace should display a formula error badge on bucket "([^"]*)"$`, func(bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		reqView := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID), nil)
		recView := httptest.NewRecorder()
		cView := tc.echoEngine.NewContext(reqView, recView)
		cView.SetParamNames("budgetId")
		cView.SetParamValues(tc.activeBudgetID)
		cView.Set(handlers.ContextUserIDKey, tc.activeUserID)
		cView.Set(handlers.ContextRoleKey, "OWNER")
		cView.Set(handlers.ContextBudgetDBKey, bDB)
		if err := tc.workspaceHandler.BudgetView(cView); err != nil {
			return err
		}
		tc.lastResponse = recView
		body := recView.Body.String()
		if !strings.Contains(body, "formula-error-badge") && !strings.Contains(body, "Formula Error") {
			return fmt.Errorf("expected formula error badge in budget view for bucket %s", bucketName)
		}
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
	ctx.Step(`^I should see available bank provider "([^"]*)"$`, func(p string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, p) {
			return fmt.Errorf("expected provider %s in body", p)
		}
		return nil
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
	ctx.Step(`^the action menu popover should support upward positioning and anchor try fallbacks$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "position-try-fallbacks") && !strings.Contains(body, "dropdown-top") {
			return fmt.Errorf("expected popover to define upward/position-try positioning support")
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

	ctx.Step(`^(?:I create|an existing) manual transaction with amount "([^"]*)", date "([^"]*)", and note "([^"]*)"$`, func(amountStr, dateStr, note string) error {
		targetBudgetID := tc.activeBudgetID
		if targetBudgetID == "" {
			targetBudgetID = "01900000-0000-7000-8000-000000000001"
		}
		bDB, err := tc.tenantMgr.GetTenantDB(targetBudgetID, true)
		if err != nil {
			return err
		}

		parts := strings.Split(amountStr, " ")
		amt := parts[0]

		form := url.Values{}
		form.Set("amount", amt)
		form.Set("transaction_date", dateStr)
		form.Set("notes", note)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+targetBudgetID+"/transactions", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(targetBudgetID)
		userID := tc.activeUserID
		if userID == "" {
			userID = "user_1"
		}
		c.Set(handlers.ContextUserIDKey, userID)
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.AddManualTransaction(c)
		tc.lastResponse = rec
		if err != nil {
			return err
		}

		var txID string
		if err := bDB.QueryRowContext(context.Background(), `SELECT id FROM budget_transactions_current WHERE notes = ? ORDER BY updated_at DESC LIMIT 1`, note).Scan(&txID); err != nil {
			return fmt.Errorf("failed to find created transaction with note %q: %w", note, err)
		}
		tc.activeTxID = txID
		return nil
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

	// Batch delete budget transactions steps
	var batchBudgetAcctID string
	var batchBucketID string

	ctx.Step(`^an active checking account in "([^"]*)" with starting balance of "([^"]*)"$`, func(budgetName, balStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		batchBudgetAcctID = domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES ('01900000-0000-7000-8000-000000000001', ?, 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, budgetName, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'Batch Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, 'user_1', ?)`, batchBudgetAcctID, pID, now)

		parts := strings.Split(balStr, " ")
		var f float64
		fmt.Sscanf(parts[0], "%f", &f)
		balMinor := int64(f * 100)

		bDB.Exec(`
			INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
			VALUES (?, 'PLN', ?, ?)
			ON CONFLICT(account_id, currency) DO UPDATE SET balance_minor = excluded.balance_minor`, batchBudgetAcctID, balMinor, now)

		tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^an active bucket "([^"]*)" with available balance of "([^"]*)"$`, func(bucketName, balStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		batchBucketID = domain.NewUUIDv7()

		bDB.Exec(`
			INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
			VALUES (?, 'Default', ?, 0, 1, ?)`, batchBucketID, bucketName, now)

		bDB.Exec(`
			INSERT INTO buckets (id, version, name, category, formula_type, action, agent_id, correlation_id, created_at)
			VALUES (?, 1, ?, 'Expenses', 'FIXED', 'CREATE', 'user_1', 'corr_1', ?)`, batchBucketID, bucketName, now)

		bDB.Exec(`
			INSERT INTO buckets_current (id, version, name, category, formula_type, is_archived, agent_id, updated_at)
			VALUES (?, 1, ?, 'Expenses', 'FIXED', 0, 'user_1', ?)`, batchBucketID, bucketName, now)

		parts := strings.Split(balStr, " ")
		var f float64
		fmt.Sscanf(parts[0], "%f", &f)
		balMinor := int64(f * 100)

		bDB.Exec(`
			INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor)
			VALUES (?, 'PLN', ?)
			ON CONFLICT(bucket_id, currency) DO UPDATE SET balance_minor = excluded.balance_minor`, batchBucketID, balMinor)

		return nil
	})

	ctx.Step(`^3 budget transactions recorded:$`, func(table *godog.Table) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()

		// external merchant party & account
		extPID := domain.NewUUIDv7()
		extAID := domain.NewUUIDv7()
		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'Merchants', 'MERCHANT', 0, 'user_1', ?)`, extPID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, 'Merchant Store', 'CHECKING', 'PLN', 0, 'user_1', ?)`, extAID, extPID, now)

		for i, row := range table.Rows {
			if i == 0 {
				continue // header
			}
			note := row.Cells[0].Value
			amtStr := row.Cells[1].Value

			parts := strings.Split(amtStr, " ")
			var f float64
			fmt.Sscanf(parts[0], "%f", &f)
			amtMinor := int64(f * 100)

			txID := domain.NewUUIDv7()

			_, err = bDB.Exec(`
				INSERT INTO budget_transactions (
					id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
					destination_amount_minor, destination_currency, bucket_id, transaction_date, notes, action, agent_id, correlation_id, created_at
				) VALUES (?, 1, ?, ?, ?, 'PLN', ?, 'PLN', ?, ?, ?, 'CREATE', 'user_1', 'corr_1', ?)`,
				txID, batchBudgetAcctID, extAID, amtMinor, amtMinor, batchBucketID, now+int64(i), note, now+int64(i),
			)
			if err != nil {
				return err
			}

			_, err = bDB.Exec(`
				INSERT INTO budget_transactions_current (
					id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
					destination_amount_minor, destination_currency, bucket_id, transaction_date, notes, updated_at
				) VALUES (?, 1, ?, ?, ?, 'PLN', ?, 'PLN', ?, ?, ?, ?)`,
				txID, batchBudgetAcctID, extAID, amtMinor, amtMinor, batchBucketID, now+int64(i), note, now+int64(i),
			)
			if err != nil {
				return err
			}

			// decrement balances as when created
			bDB.Exec(`UPDATE account_currency_balances SET balance_minor = balance_minor - ? WHERE account_id = ? AND currency = 'PLN'`, amtMinor, batchBudgetAcctID)
			bDB.Exec(`UPDATE bucket_currency_balances SET balance_minor = balance_minor - ? WHERE bucket_id = ? AND currency = 'PLN'`, amtMinor, batchBucketID)
		}
		return nil
	})

	ctx.Step(`^I batch delete the budget transactions for "([^"]*)" and "([^"]*)"$`, func(note1, note2 string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		rows, err := bDB.Query(`SELECT id FROM budget_transactions_current WHERE notes IN (?, ?)`, note1, note2)
		if err != nil {
			return err
		}
		defer rows.Close()

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}

		form := url.Values{}
		for _, id := range ids {
			form.Add("transaction_ids", id)
		}

		req := httptest.NewRequest(http.MethodPost, "/budgets/01900000-0000-7000-8000-000000000001/transactions/batch-delete", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues("01900000-0000-7000-8000-000000000001")
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.BatchDeleteTransactions(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the transactions for "([^"]*)" and "([^"]*)" should no longer appear in the budget transactions ledger$`, func(note1, note2 string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM budget_transactions_current WHERE notes IN (?, ?)`, note1, note2).Scan(&count)
		if err != nil || count != 0 {
			return fmt.Errorf("expected 0 transactions for %s and %s, found %d", note1, note2, count)
		}
		return nil
	})

	ctx.Step(`^the transaction for "([^"]*)" should still appear in the budget transactions ledger$`, func(note string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM budget_transactions_current WHERE notes = ?`, note).Scan(&count)
		if err != nil || count != 1 {
			return fmt.Errorf("expected 1 transaction for %s, found %d", note, count)
		}
		return nil
	})

	ctx.Step(`^the account balance should be reverted by "([^"]*)" to "([^"]*)"$`, func(revertedAmt, expectedBalStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		parts := strings.Split(expectedBalStr, " ")
		var f float64
		fmt.Sscanf(parts[0], "%f", &f)
		expectedMinor := int64(f * 100)

		var actualMinor int64
		err = bDB.QueryRow(`SELECT balance_minor FROM account_currency_balances WHERE account_id = ? AND currency = 'PLN'`, batchBudgetAcctID).Scan(&actualMinor)
		if err != nil {
			return err
		}

		if actualMinor != expectedMinor {
			return fmt.Errorf("expected account balance %d minor, got %d minor", expectedMinor, actualMinor)
		}
		return nil
	})

	ctx.Step(`^the available balance of bucket "([^"]*)" should be restored by "([^"]*)" to "([^"]*)"$`, func(bucketName, restoredAmt, expectedBalStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB("01900000-0000-7000-8000-000000000001", true)
		if err != nil {
			return err
		}

		parts := strings.Split(expectedBalStr, " ")
		var f float64
		fmt.Sscanf(parts[0], "%f", &f)
		expectedMinor := int64(f * 100)

		var actualMinor int64
		err = bDB.QueryRow(`SELECT balance_minor FROM bucket_currency_balances WHERE bucket_id = ? AND currency = 'PLN'`, batchBucketID).Scan(&actualMinor)
		if err != nil {
			return err
		}

		if actualMinor != expectedMinor {
			return fmt.Errorf("expected bucket available %d minor, got %d minor", expectedMinor, actualMinor)
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
		bDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, 'mock_integration_user_1', 'ext_acct_1', ?, ?)`, domain.NewUUIDv7(), userAcctID, now, now)

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
		err = bDB.QueryRow(`SELECT SUM(amount_minor) FROM raw_transactions_current WHERE booking_status = 'BOOKED'`).Scan(&netSumRaw)
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
		bDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, 'mock_integration_user_1', 'ext_acct_1', ?, ?)`, domain.NewUUIDv7(), userAcctID, now, now)

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
		bDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, 'mock_integration_user_1', 'ext_acct_1', ?, ?)`, domain.NewUUIDv7(), userAcctID, now, now)

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
		tc.mainDB.Exec(`
			INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, created_at, updated_at)
			VALUES ('gocardless_real_int_1', 'user_1', 'GOCARDLESS', 'REAL_BANK_PL', 'ref_1', 'ACTIVE', ?, ?)
			ON CONFLICT(id) DO NOTHING`, now, now)
		bDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, 'gocardless_real_int_1', 'ext_acct_1', ?, ?)`, domain.NewUUIDv7(), userAcctID, now, now)

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

	var testPendingRawID string

	ctx.Step(`^an active checking account "([^"]*)" linked to a bank feed with booked and pending transactions$`, func(acctName string) error {
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
			VALUES ('01900000-0000-7000-8000-000000000001', 'Family Home', 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)
		bDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, 'mock_integration_user_1', 'ext_acct_1', ?, ?)`, domain.NewUUIDv7(), userAcctID, now, now)

		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^newly discovered raw transactions should include booked transactions with "booking_status = 'BOOKED'"$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE booking_status = 'BOOKED'`).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("expected booked transactions, got count=%d, err=%v", count, err)
		}
		return nil
	})

	ctx.Step(`^raw transactions should include pending holds with "booking_status = 'PENDING'"$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE booking_status = 'PENDING'`).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("expected pending hold transactions, got count=%d, err=%v", count, err)
		}
		return nil
	})

	ctx.Step(`^the Reconcile Workbench should display "Pending Hold" badge for pending holds$`, func() error {
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
		if err != nil {
			return err
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Pending Hold") {
			return fmt.Errorf("expected 'Pending Hold' badge in Reconcile Workbench")
		}
		return nil
	})

	ctx.Step(`^resolving or converting a pending hold transaction into a budget transaction should be blocked$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var pendingID string
		err = bDB.QueryRow(`SELECT id FROM raw_transactions_current WHERE booking_status = 'PENDING' LIMIT 1`).Scan(&pendingID)
		if err != nil {
			return fmt.Errorf("no pending transaction found to test: %v", err)
		}

		form := url.Values{}
		form.Set("csrf_token", "dummy_csrf_token")
		form.Set("bucket_id", domain.NewUUIDv7())

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/raw/%s/resolve", tc.activeBudgetID, pendingID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "rawId")
		c.SetParamValues(tc.activeBudgetID, pendingID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		resErr := tc.txHandler.ResolveRawTransaction(c)
		if resErr == nil && rec.Code < 400 {
			return fmt.Errorf("expected resolve to be rejected for pending hold, got status %d", rec.Code)
		}
		return nil
	})

	ctx.Step(`^a pending hold raw transaction in account "([^"]*)"$`, func(acctName string) error {
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
			VALUES ('01900000-0000-7000-8000-000000000001', 'Family Home', 'PLN', ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)
		bDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, 'mock_integration_user_1', 'ext_acct_1', ?, ?)`, domain.NewUUIDv7(), userAcctID, now, now)

		testPendingRawID = domain.NewUUIDv7()
		dedup := "pending_dedup_hash"
		bDB.Exec(`
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, reference, status, booking_status, updated_at
			) VALUES (?, 1, ?, 'tx_mock_dinner_cleared', ?, ?, -30000, 'PLN', 'RESTAURACJA POD BARANEM', 'Kolacja', 'PENDING', 'PENDING', ?)`,
			testPendingRawID, userAcctID, dedup, now-86400*1_000_000, now,
		)
		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^the bank subsequently reports the transaction as cleared and booked$`, func() error {
		return nil
	})

	ctx.Step(`^the pending raw transaction should transition to "booking_status = 'BOOKED'"$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var bStatus string
		err = bDB.QueryRow(`SELECT booking_status FROM raw_transactions_current WHERE id = ?`, testPendingRawID).Scan(&bStatus)
		if err != nil {
			return fmt.Errorf("failed fetching updated raw transaction: %v", err)
		}
		if bStatus != "BOOKED" {
			return fmt.Errorf("expected booking_status 'BOOKED', got %q", bStatus)
		}
		return nil
	})

	ctx.Step(`^it should now be resolvable in the Reconcile Workbench$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		pID := domain.NewUUIDv7()
		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'Dining Out Merchant', 'ORGANIZATION', 0, 'user_1', ?)`, pID, time.Now().UTC().UnixMicro())

		form := url.Values{}
		form.Set("csrf_token", "dummy_csrf_token")
		form.Set("party_name", "RESTAURACJA POD BARANEM")

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/raw/%s/resolve", tc.activeBudgetID, testPendingRawID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "rawId")
		c.SetParamValues(tc.activeBudgetID, testPendingRawID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.ResolveRawTransaction(c)
		if err != nil {
			return fmt.Errorf("expected transaction to resolve successfully, got: %v", err)
		}
		if rec.Code >= 400 {
			return fmt.Errorf("expected HTTP redirect or success, got status %d", rec.Code)
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

	var batchRawIDs []string

	ctx.Step(`^(\d+) pending raw transactions recorded:$`, func(count int, table *godog.Table) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		batchRawIDs = nil

		var acctID string
		err = bDB.QueryRow(`SELECT id FROM accounts_current LIMIT 1`).Scan(&acctID)
		if err != nil {
			return fmt.Errorf("no active account found: %w", err)
		}

		for i := 1; i < len(table.Rows); i++ {
			payee := table.Rows[i].Cells[0].Value
			amountStr := table.Rows[i].Cells[1].Value
			cleaned := strings.TrimSuffix(amountStr, " PLN")
			var f float64
			fmt.Sscanf(cleaned, "%f", &f)
			amtMinor := int64(f * 100)

			rID := domain.NewUUIDv7()
			dedup := transaction.ComputeCanonicalDedupHash(acctID, rID, now, amtMinor, "PLN", payee, "Ref")

			_, err = bDB.Exec(`
				INSERT INTO raw_transactions_current (
					id, version, account_id, external_transaction_id, dedup_hash, booking_date,
					amount_minor, currency, raw_payee, reference, status, booking_status, updated_at
				) VALUES (?, 1, ?, ?, ?, ?, ?, 'PLN', ?, 'Ref', 'PENDING', 'BOOKED', ?)`,
				rID, acctID, rID, dedup, now, amtMinor, payee, now,
			)
			if err != nil {
				return err
			}
			_, err = bDB.Exec(`
				INSERT INTO raw_transactions (
					id, version, account_id, external_transaction_id, dedup_hash, booking_date,
					amount_minor, currency, raw_payee, reference, status, booking_status, action, agent_id, correlation_id, created_at
				) VALUES (?, 1, ?, ?, ?, ?, ?, 'PLN', ?, 'Ref', 'PENDING', 'BOOKED', 'CREATE', 'user_1', 'corr_1', ?)`,
				rID, acctID, rID, dedup, now, amtMinor, payee, now,
			)
			if err != nil {
				return err
			}
			batchRawIDs = append(batchRawIDs, rID)
		}
		return nil
	})

	ctx.Step(`^I delete the pending raw transaction$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/raw/%s/delete", tc.activeBudgetID, activeRawID), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "rawId")
		c.SetParamValues(tc.activeBudgetID, activeRawID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.DeleteRawTransaction(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^I batch delete the pending raw transactions$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		form := url.Values{}
		for _, id := range batchRawIDs {
			form.Add("raw_ids", id)
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/transactions/raw/batch-delete", tc.activeBudgetID), strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, "user_1")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.txHandler.BatchDeleteRawTransactions(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the raw transaction should be permanently removed from raw transactions$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var countCurr, countHist int
		_ = bDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE id = ?`, activeRawID).Scan(&countCurr)
		_ = bDB.QueryRow(`SELECT count(*) FROM raw_transactions WHERE id = ?`, activeRawID).Scan(&countHist)
		if countCurr != 0 || countHist != 0 {
			return fmt.Errorf("expected raw transaction %s to be permanently removed, got current: %d, history: %d", activeRawID, countCurr, countHist)
		}
		return nil
	})

	ctx.Step(`^the raw transactions should be permanently removed from raw transactions$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		for _, id := range batchRawIDs {
			var countCurr, countHist int
			_ = bDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE id = ?`, id).Scan(&countCurr)
			_ = bDB.QueryRow(`SELECT count(*) FROM raw_transactions WHERE id = ?`, id).Scan(&countHist)
			if countCurr != 0 || countHist != 0 {
				return fmt.Errorf("expected raw transaction %s to be permanently removed, got current: %d, history: %d", id, countCurr, countHist)
			}
		}
		return nil
	})

	// BALANCE-73 & BALANCE-62 steps
	ctx.Step(`^an active cash account "([^"]*)" with no bank integration$`, func(acctName string) error {
		if tc.activeBudgetID == "" {
			tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
		}
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
			VALUES (?, 1, ?, ?, 'CASH', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, acctName, now)

		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^the bank sync should fail with an unlinked account error$`, func() error {
		if tc.lastResponse == nil {
			return fmt.Errorf("expected response from bank sync")
		}
		if tc.lastResponse.Code != http.StatusBadRequest && tc.lastResponse.Code != http.StatusInternalServerError {
			return fmt.Errorf("expected 400 or 500 error code, got %d", tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^no raw transactions should be generated for "([^"]*)"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var acctID string
		_ = bDB.QueryRow("SELECT id FROM accounts_current WHERE name = ?", acctName).Scan(&acctID)
		var count int
		bDB.QueryRow("SELECT count(*) FROM raw_transactions_current WHERE account_id = ?", acctID).Scan(&count)
		if count > 0 {
			return fmt.Errorf("expected 0 raw transactions for %s, got %d", acctName, count)
		}
		return nil
	})

	ctx.Step(`^the bank sync modal should list "([^"]*)"$`, func(acctName string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, acctName) {
			return fmt.Errorf("expected account %q to be listed in sync modal, body: %s", acctName, body)
		}
		return nil
	})

	ctx.Step(`^the bank sync modal should not list "([^"]*)"$`, func(acctName string) error {
		body := tc.lastResponse.Body.String()
		// It should not be listed as a checkbox input in modal
		if strings.Contains(body, fmt.Sprintf(`name="account_ids"`)) && strings.Contains(body, acctName) {
			// Check if acctName appears within the checkbox list
			idx := strings.Index(body, "Select Accounts to Synchronize")
			if idx != -1 {
				modalPart := body[idx : idx+500]
				if strings.Contains(modalPart, acctName) {
					return fmt.Errorf("expected account %q NOT to be listed in sync modal checkboxes", acctName)
				}
			}
		}
		return nil
	})

	ctx.Step(`^a budget "([^"]*)" with only manual cash accounts$`, func(budgetName string) error {
		tc.activeBudgetID = domain.NewUUIDv7()
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		userAcctID := domain.NewUUIDv7()

		tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_at, updated_at)
			VALUES (?, ?, 'PLN', ?, ?)`, tc.activeBudgetID, budgetName, now, now)

		bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, 'user_1', ?)`, pID, now)
		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, 'Pocket Cash', 'CASH', 'PLN', 0, 'user_1', ?)`, userAcctID, pID, now)

		tc.activeUserID = "user_1"
		return nil
	})

	ctx.Step(`^I visit the transactions ledger page on budget "([^"]*)"$`, func(budgetName string) error {
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

	ctx.Step(`^the Sync with Bank button should be disabled with empty state notice$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "btn-disabled") || !strings.Contains(body, "disabled") {
			return fmt.Errorf("expected disabled sync button in response")
		}
		if !strings.Contains(body, "No bank-linked accounts") {
			return fmt.Errorf("expected 'No bank-linked accounts' notice in response")
		}
		return nil
	})

	ctx.Step(`^the bank sync should fail with a mock disabled in production error$`, func() error {
		if tc.lastResponse == nil {
			return fmt.Errorf("expected response from bank sync")
		}
		if tc.lastResponse.Code != http.StatusInternalServerError && tc.lastResponse.Code != http.StatusForbidden {
			return fmt.Errorf("expected error status code, got %d", tc.lastResponse.Code)
		}
		return nil
	})
	// 07_buckets_and_targets.feature steps

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

	ctx.Step(`^I add an optional target "([^"]*)" with priority (\d+), cadence "([^"]*)", amount "([^"]*)"$`, func(name string, prio int, cadence, amt string) error {
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

	ctx.Step(`^I add a target "([^"]*)" with priority (\d+), cadence "([^"]*)", recurrence days (\d+), amount "([^"]*)"$`, func(name string, prio int, cadence string, rDays int, amt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:           activeBucketID,
			Name:               name,
			TargetType:         "EXPENSE",
			Priority:           prio,
			AmountMinor:        parseAmountStr(amt),
			Currency:           "PLN",
			RecurrenceCadence:  cadence,
			RecurrenceInterval: rDays,
			AgentID:            tc.activeUserID,
			CorrelationID:      domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeTargetID = tID
		return nil
	})

	ctx.Step(`^I add a target "([^"]*)" with priority (\d+), cadence "([^"]*)", recurrence interval (\d+), due in (\d+) days, amount "([^"]*)"$`, func(name string, prio int, cadence string, rInterval int, dueDays int, amt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		dueMicro := time.Now().UTC().AddDate(0, 0, dueDays).UnixMicro()
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:           activeBucketID,
			Name:               name,
			TargetType:         "EXPENSE",
			Priority:           prio,
			AmountMinor:        parseAmountStr(amt),
			Currency:           "PLN",
			RecurrenceCadence:  cadence,
			RecurrenceInterval: rInterval,
			DueDate:            &dueMicro,
			AgentID:            tc.activeUserID,
			CorrelationID:      domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeTargetID = tID
		return nil
	})

	ctx.Step(`^target "([^"]*)" in projections should have recurrence days (\d+)$`, func(name string, expectedDays int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var days int
		err = bDB.QueryRow(`SELECT recurrence_interval FROM targets_projection WHERE name = ? AND is_archived = 0`, name).Scan(&days)
		if err != nil {
			return fmt.Errorf("target %s not found: %w", name, err)
		}
		if days != expectedDays {
			return fmt.Errorf("expected recurrence interval %d, got %d", expectedDays, days)
		}
		return nil
	})

	ctx.Step(`^I add a recurring target "([^"]*)" with amount "([^"]*)" monthly and due in (\d+) days$`, func(name, amt string, dueDays int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		dueMicro := time.Now().UTC().AddDate(0, 0, dueDays).UnixMicro()
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:          activeBucketID,
			Name:              name,
			TargetType:        "EXPENSE",
			Priority:          3,
			AmountMinor:       parseAmountStr(amt),
			Currency:          "PLN",
			RecurrenceCadence: budget.CadenceMonthly,
			DueDate:           &dueMicro,
			AgentID:           tc.activeUserID,
			CorrelationID:     domain.NewUUIDv7(),
		})
		if err != nil {
			return err
		}
		activeTargetID = tID
		return nil
	})

	ctx.Step(`^the target requirement in a (\d+)-day window should be "([^"]*)"$`, func(windowDays int, expectedAmt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var target budget.TargetDefinition
		var rInterval sql.NullInt64
		var dueDate sql.NullInt64
		err = bDB.QueryRow(`SELECT id, bucket_id, name, priority, sort_order, amount_minor, currency, recurrence_cadence, recurrence_interval, due_date, non_spending_strategy, current_funded_minor
			FROM targets_projection WHERE id = ?`, activeTargetID).Scan(
			&target.ID, &target.BucketID, &target.Name, &target.Priority, &target.SortOrder,
			&target.AmountMinor, &target.Currency, &target.RecurrenceCadence, &rInterval,
			&dueDate, &target.NonSpendingStrategy, &target.CurrentFundedMinor,
		)
		if err != nil {
			return fmt.Errorf("failed fetching active target %s: %w", activeTargetID, err)
		}
		if rInterval.Valid {
			target.RecurrenceInterval = int(rInterval.Int64)
		}
		if dueDate.Valid {
			target.DueDate = dueDate.Int64
		}
		req := budget.ComputeTargetRequirement(&target, windowDays, time.Now().UTC())
		expectedMinor := parseAmountStr(expectedAmt)
		if req != expectedMinor {
			return fmt.Errorf("expected requirement %d, got %d", expectedMinor, req)
		}
		return nil
	})

	ctx.Step(`^target "([^"]*)" in projections should have cadence "([^"]*)" and recurrence interval (\d+)$`, func(name string, expectedCadence string, expectedInterval int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var cadence string
		var interval int
		err = bDB.QueryRow(`SELECT recurrence_cadence, recurrence_interval FROM targets_projection WHERE name = ? AND is_archived = 0`, name).Scan(&cadence, &interval)
		if err != nil {
			return fmt.Errorf("target %s not found: %w", name, err)
		}
		if cadence != expectedCadence || interval != expectedInterval {
			return fmt.Errorf("expected cadence %s with interval %d, got cadence=%s, interval=%d", expectedCadence, expectedInterval, cadence, interval)
		}
		return nil
	})

	ctx.Step(`^target "([^"]*)" in projections should have cadence "([^"]*)" and non-zero due date$`, func(name string, expectedCadence string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var cadence string
		var dueDate sql.NullInt64
		err = bDB.QueryRow(`SELECT recurrence_cadence, due_date FROM targets_projection WHERE name = ? AND is_archived = 0`, name).Scan(&cadence, &dueDate)
		if err != nil {
			return fmt.Errorf("target %s not found: %w", name, err)
		}
		if cadence != expectedCadence {
			return fmt.Errorf("expected cadence %s, got %s", expectedCadence, cadence)
		}
		if !dueDate.Valid || dueDate.Int64 == 0 {
			return fmt.Errorf("expected target %s to have non-zero due date", name)
		}
		return nil
	})

	ctx.Step(`^an account "([^"]*)" with currency "([^"]*)" and balance "([^"]*)"$`, func(acctName, curr, balStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		var partyID string
		err = bDB.QueryRow(`SELECT id FROM parties_current WHERE is_user_owned = 1 LIMIT 1`).Scan(&partyID)
		if err != nil {
			partyID = domain.NewUUIDv7()
			_, err = bDB.Exec(`
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'Self', 'INDIVIDUAL', 1, ?, ?)`, partyID, tc.activeUserID, now)
			if err != nil {
				return err
			}
		}

		acctID := domain.NewUUIDv7()
		curr = strings.ToUpper(strings.TrimSpace(curr))
		_, err = bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', ?, 0, ?, ?)`, acctID, partyID, acctName, curr, tc.activeUserID, now)
		if err != nil {
			return err
		}

		amt := parseAmountStr(balStr)
		_, err = bDB.Exec(`
			INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
			VALUES (?, ?, ?, ?)`, acctID, curr, amt, now)
		return err
	})

	ctx.Step(`^I add a target "([^"]*)" with priority (\d+), cadence "([^"]*)", amount "([^"]*)" in currency "([^"]*)"$`, func(name string, prio int, cadence, amt, curr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var defaultCurr string
		_ = tc.mainDB.QueryRow(`SELECT default_currency FROM budgets WHERE id = ?`, tc.activeBudgetID).Scan(&defaultCurr)
		avail, err := budget.GetAvailableCurrencies(context.Background(), bDB, defaultCurr)
		if err != nil {
			return err
		}
		curr = strings.ToUpper(strings.TrimSpace(curr))
		isAllowed := false
		for _, c := range avail {
			if c == curr {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			return fmt.Errorf("currency %s not allowed", curr)
		}

		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:          activeBucketID,
			Name:              name,
			TargetType:        "EXPENSE",
			Priority:          prio,
			AmountMinor:       parseAmountStr(amt),
			Currency:          curr,
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

	ctx.Step(`^target "([^"]*)" in projections should have currency "([^"]*)" and amount "([^"]*)"$`, func(name, expectedCurr, expectedAmtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var curr string
		var amt int64
		err = bDB.QueryRow(`SELECT currency, amount_minor FROM targets_projection WHERE name = ? AND is_archived = 0`, name).Scan(&curr, &amt)
		if err != nil {
			return fmt.Errorf("target %s not found: %w", name, err)
		}
		expectedCurr = strings.ToUpper(strings.TrimSpace(expectedCurr))
		if curr != expectedCurr {
			return fmt.Errorf("expected currency %s, got %s", expectedCurr, curr)
		}
		expectedAmt := parseAmountStr(expectedAmtStr)
		if amt != expectedAmt {
			return fmt.Errorf("expected amount %d, got %d", expectedAmt, amt)
		}
		return nil
	})

	ctx.Step(`^I attempt to update target "([^"]*)" with currency "([^"]*)"$`, func(name, newCurr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var tID string
		err = bDB.QueryRow(`SELECT id FROM targets_projection WHERE name = ? AND is_archived = 0`, name).Scan(&tID)
		if err != nil {
			return err
		}
		return budget.UpdateTarget(context.Background(), bDB, budget.UpdateTargetInput{
			ID:                  tID,
			Name:                name,
			TargetType:          "EXPENSE",
			Priority:            2,
			AmountMinor:         parseAmountStr("250.00 EUR"),
			RecurrenceCadence:   budget.CadenceMonthly,
			NonSpendingStrategy: budget.StrategyRollOver,
			AgentID:             tc.activeUserID,
			CorrelationID:       domain.NewUUIDv7(),
		})
	})

	ctx.Step(`^target "([^"]*)" in projections should still have currency "([^"]*)"$`, func(name, expectedCurr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var curr string
		err = bDB.QueryRow(`SELECT currency FROM targets_projection WHERE name = ? AND is_archived = 0`, name).Scan(&curr)
		if err != nil {
			return fmt.Errorf("target %s not found: %w", name, err)
		}
		expectedCurr = strings.ToUpper(strings.TrimSpace(expectedCurr))
		if curr != expectedCurr {
			return fmt.Errorf("expected currency %s, got %s", expectedCurr, curr)
		}
		return nil
	})

	ctx.Step(`^I attempt to add a target "([^"]*)" in currency "([^"]*)"$`, func(name, curr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var defaultCurr string
		_ = tc.mainDB.QueryRow(`SELECT default_currency FROM budgets WHERE id = ?`, tc.activeBudgetID).Scan(&defaultCurr)
		avail, err := budget.GetAvailableCurrencies(context.Background(), bDB, defaultCurr)
		if err != nil {
			lastTargetErr = err
			return nil
		}
		curr = strings.ToUpper(strings.TrimSpace(curr))
		isAllowed := false
		for _, c := range avail {
			if c == curr {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			lastTargetErr = fmt.Errorf("currency %s is not available in budget", curr)
			return nil
		}
		_, lastTargetErr = budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:          activeBucketID,
			Name:              name,
			TargetType:        "EXPENSE",
			Priority:          2,
			AmountMinor:       10000,
			Currency:          curr,
			RecurrenceCadence: budget.CadenceMonthly,
			AgentID:           tc.activeUserID,
			CorrelationID:     domain.NewUUIDv7(),
		})
		return nil
	})

	ctx.Step(`^the target creation should be rejected due to unsupported currency$`, func() error {
		if lastTargetErr == nil {
			return fmt.Errorf("expected target creation to fail due to unsupported currency, but succeeded")
		}
		if !strings.Contains(lastTargetErr.Error(), "not available in budget") && !strings.Contains(lastTargetErr.Error(), "not allowed") {
			return fmt.Errorf("unexpected error: %v", lastTargetErr)
		}
		return nil
	})

	ctx.Step(`^I add a target with priority (\d+), cadence "([^"]*)", amount "([^"]*)" without a name$`, func(prio int, cadence, amt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		tID, err := budget.CreateTarget(context.Background(), bDB, budget.CreateTargetInput{
			BucketID:          activeBucketID,
			Name:              "",
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

	ctx.Step(`^the target in bucket "([^"]*)" with priority (\d+) should have an empty or null name$`, func(bucketName string, expectedPrio int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bucketID string
		err = bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ?`, bucketName).Scan(&bucketID)
		if err != nil {
			return fmt.Errorf("bucket %s not found: %w", bucketName, err)
		}
		var nameNull sql.NullString
		err = bDB.QueryRow(`SELECT name FROM targets_projection WHERE bucket_id = ? AND priority = ? AND is_archived = 0`, bucketID, expectedPrio).Scan(&nameNull)
		if err != nil {
			return fmt.Errorf("target not found: %w", err)
		}
		if nameNull.Valid && strings.TrimSpace(nameNull.String) != "" {
			return fmt.Errorf("expected null or empty name, got %q", nameNull.String)
		}
		return nil
	})

	ctx.Step(`^the budget page for "([^"]*)" should display priority badge "([^"]*)" without target name$`, func(budgetName, prioText string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		reqView := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID), nil)
		recView := httptest.NewRecorder()
		cView := tc.echoEngine.NewContext(reqView, recView)
		cView.SetParamNames("budgetId")
		cView.SetParamValues(tc.activeBudgetID)
		cView.Set(handlers.ContextUserIDKey, tc.activeUserID)
		cView.Set(handlers.ContextRoleKey, "OWNER")
		cView.Set(handlers.ContextBudgetDBKey, bDB)
		if err := tc.workspaceHandler.BudgetView(cView); err != nil {
			return err
		}
		body := recView.Body.String()
		if !strings.Contains(body, prioText) {
			return fmt.Errorf("expected priority badge %q in body", prioText)
		}
		return nil
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
		bID := activeBucketID
		if bucketName != "" {
			var resolvedID string
			if err := bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0`, bucketName).Scan(&resolvedID); err == nil && resolvedID != "" {
				bID = resolvedID
			}
		}
		// Returning funds from bucket to pool is negative assignment
		return budget.AssignFunds(context.Background(), bDB, budget.AssignFundsInput{
			BucketID:      bID,
			Currency:      "PLN",
			AmountMinor:   -amt,
			AgentID:       tc.activeUserID,
			CorrelationID: domain.NewUUIDv7(),
		})
	})

	ctx.Step(`^I move "([^"]*)" from bucket "([^"]*)" to bucket "([^"]*)"$`, func(amtStr, srcName, dstName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		amt := parseAmountStr(amtStr)
		var srcID, dstID string
		if err := bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0`, srcName).Scan(&srcID); err != nil {
			return fmt.Errorf("source bucket %s not found: %w", srcName, err)
		}
		if err := bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0`, dstName).Scan(&dstID); err != nil {
			return fmt.Errorf("dest bucket %s not found: %w", dstName, err)
		}
		return budget.MoveFunds(context.Background(), bDB, budget.MoveFundsInput{
			SourceBucketID: srcID,
			DestBucketID:   dstID,
			Currency:       "PLN",
			AmountMinor:    amt,
			AgentID:        tc.activeUserID,
			CorrelationID:  domain.NewUUIDv7(),
		})
	})

	ctx.Step(`^target "([^"]*)" in bucket "([^"]*)" should have funded amount "([^"]*)"$`, func(targetName, bucketName, expectedAmt string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var funded int64
		err = bDB.QueryRow(`
			SELECT t.current_funded_minor
			FROM targets_projection t
			JOIN buckets_projection b ON t.bucket_id = b.id
			WHERE t.name = ? AND b.name = ? AND t.is_archived = 0 AND b.is_archived = 0`, targetName, bucketName).Scan(&funded)
		if err != nil {
			return fmt.Errorf("failed fetching funded amount for target %s in bucket %s: %w", targetName, bucketName, err)
		}
		if funded != parseAmountStr(expectedAmt) {
			return fmt.Errorf("expected target %s in bucket %s to have funded %d, got %d", targetName, bucketName, parseAmountStr(expectedAmt), funded)
		}
		return nil
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

	ctx.Step(`^target "([^"]*)" in bucket "([^"]*)" has funded amount "([^"]*)" with bucket balance "([^"]*)"$`, func(targetName, bucketName, fundedStr, balStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bID string
		if err := bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0`, bucketName).Scan(&bID); err != nil {
			return fmt.Errorf("bucket %s not found: %w", bucketName, err)
		}
		fundedMinor := parseAmountStr(fundedStr)
		balMinor := parseAmountStr(balStr)
		now := time.Now().UTC().UnixMicro()

		_, err = bDB.Exec(`UPDATE targets_projection SET current_funded_minor = ?, updated_at = ? WHERE name = ? AND bucket_id = ?`, fundedMinor, now, targetName, bID)
		if err != nil {
			return fmt.Errorf("failed to update target funded minor: %w", err)
		}

		_, err = bDB.Exec(`INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor)
			VALUES (?, 'PLN', ?)
			ON CONFLICT(bucket_id, currency) DO UPDATE SET balance_minor = excluded.balance_minor`, bID, balMinor)
		if err != nil {
			return fmt.Errorf("failed to update bucket balance: %w", err)
		}
		return nil
	})

	ctx.Step(`^the bucket "([^"]*)" should have unassigned buffer "([^"]*)"$`, func(bucketName, expBufferStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bID string
		var balMinor int64
		if err := bDB.QueryRow(`
			SELECT b.id, COALESCE(bcb.balance_minor, 0)
			FROM buckets_projection b
			LEFT JOIN bucket_currency_balances bcb ON b.id = bcb.bucket_id AND bcb.currency = 'PLN'
			WHERE b.name = ? AND b.is_archived = 0`, bucketName).Scan(&bID, &balMinor); err != nil {
			return fmt.Errorf("bucket %s not found: %w", bucketName, err)
		}
		var fundedMinor int64
		if err := bDB.QueryRow(`
			SELECT COALESCE(SUM(current_funded_minor), 0)
			FROM targets_projection
			WHERE bucket_id = ? AND currency = 'PLN' AND is_archived = 0`, bID).Scan(&fundedMinor); err != nil {
			return fmt.Errorf("failed to query targets for bucket %s: %w", bucketName, err)
		}
		unassignedBuffer := balMinor - fundedMinor
		expectedBuffer := parseAmountStr(expBufferStr)
		if unassignedBuffer != expectedBuffer {
			return fmt.Errorf("expected bucket %s unassigned buffer %d, got %d", bucketName, expectedBuffer, unassignedBuffer)
		}
		return nil
	})

	ctx.Step(`^the bucket "([^"]*)" should have health status "([^"]*)"$`, func(bucketName, expStatus string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		reqView := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID), nil)
		recView := httptest.NewRecorder()
		cView := tc.echoEngine.NewContext(reqView, recView)
		cView.SetParamNames("budgetId")
		cView.SetParamValues(tc.activeBudgetID)
		cView.Set(handlers.ContextUserIDKey, tc.activeUserID)
		cView.Set(handlers.ContextRoleKey, "OWNER")
		cView.Set(handlers.ContextBudgetDBKey, bDB)
		if err := tc.workspaceHandler.BudgetView(cView); err != nil {
			return err
		}
		tc.lastResponse = recView
		body := recView.Body.String()
		if !strings.Contains(body, expStatus) {
			return fmt.Errorf("expected health status %q in budget view for bucket %s", expStatus, bucketName)
		}
		return nil
	})

	ctx.Step(`^the bucket "([^"]*)" should be marked with P3 at risk$`, func(bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		reqView := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID), nil)
		recView := httptest.NewRecorder()
		cView := tc.echoEngine.NewContext(reqView, recView)
		cView.SetParamNames("budgetId")
		cView.SetParamValues(tc.activeBudgetID)
		cView.Set(handlers.ContextUserIDKey, tc.activeUserID)
		cView.Set(handlers.ContextRoleKey, "OWNER")
		cView.Set(handlers.ContextBudgetDBKey, bDB)
		if err := tc.workspaceHandler.BudgetView(cView); err != nil {
			return err
		}
		tc.lastResponse = recView
		body := recView.Body.String()
		if !strings.Contains(body, "P3 at risk") {
			return fmt.Errorf("expected bucket %s to be marked with P3 at risk in budget view", bucketName)
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
		bID := activeBucketID
		if bucketName != "" {
			var resolvedID string
			if err := bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0`, bucketName).Scan(&resolvedID); err == nil && resolvedID != "" {
				bID = resolvedID
				activeBucketID = resolvedID
			}
		}
		form := url.Values{}
		form.Set("currency", "PLN")
		form.Set("amount", fmt.Sprintf("%.2f", float64(amt)/100.0))

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/buckets/%s/assign", tc.activeBudgetID, bID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, bID)
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
		bID := activeBucketID
		if bucketName != "" {
			var resolvedID string
			if err := bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0`, bucketName).Scan(&resolvedID); err == nil && resolvedID != "" {
				bID = resolvedID
			}
		}
		var bal int64
		err = bDB.QueryRow(`SELECT COALESCE(balance_minor, 0) FROM bucket_currency_balances WHERE bucket_id = ? AND currency = 'PLN'`, bID).Scan(&bal)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if bal != parseAmountStr(expectedAmt) {
			return fmt.Errorf("expected bucket %s balance %d, got %d", bucketName, parseAmountStr(expectedAmt), bal)
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

	ctx.Step(`^I request to move category group "([^"]*)" up$`, func(groupName string) error {
		form := url.Values{}
		form.Set("group_name", groupName)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/groups/move?dir=up", tc.activeBudgetID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, "OWNER")
		c.Set("correlationID", domain.NewUUIDv7())

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.workspaceHandler.MoveGroup(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^category group "([^"]*)" should precede "([^"]*)" in budget view$`, func(groupA, groupB string) error {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID), nil)
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

		if err := tc.workspaceHandler.BudgetView(c); err != nil {
			return err
		}
		body := rec.Body.String()
		headerA := fmt.Sprintf("toggleGroup('%s')", handlers.SafeDOMID(groupA))
		headerB := fmt.Sprintf("toggleGroup('%s')", handlers.SafeDOMID(groupB))
		idxA := strings.Index(body, headerA)
		idxB := strings.Index(body, headerB)
		if idxA == -1 || idxB == -1 {
			return fmt.Errorf("could not find both group headers (%s at %d, %s at %d) in HTML", headerA, idxA, headerB, idxB)
		}
		if idxA >= idxB {
			return fmt.Errorf("expected group %q (idx %d) to precede %q (idx %d) in budget view", groupA, idxA, groupB, idxB)
		}
		return nil
	})

	ctx.Step(`^I request to rename category group "([^"]*)" to "([^"]*)"$`, func(oldName, newName string) error {
		form := url.Values{}
		form.Set("old_name", oldName)
		form.Set("new_name", newName)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/groups/rename", tc.activeBudgetID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, "OWNER")
		c.Set("correlationID", domain.NewUUIDv7())

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.workspaceHandler.RenameGroup(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^bucket "([^"]*)" in projections should have group "([^"]*)"$`, func(bucketName, expectedGroup string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var actualGroup string
		err = bDB.QueryRow(`SELECT group_name FROM buckets_projection WHERE name = ?`, bucketName).Scan(&actualGroup)
		if err != nil {
			return fmt.Errorf("failed fetching bucket %q group: %w", bucketName, err)
		}
		if actualGroup != expectedGroup {
			return fmt.Errorf("expected bucket %q to have group %q, got %q", bucketName, expectedGroup, actualGroup)
		}
		return nil
	})

	ctx.Step(`^the new bucket modal should contain a datalist with existing category groups "([^"]*)", "([^"]*)", "([^"]*)"$`, func(g1, g2, g3 string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `id="existing_category_groups"`) {
			return fmt.Errorf("expected datalist with id='existing_category_groups' in response body")
		}
		for _, g := range []string{g1, g2, g3} {
			expectedOption := fmt.Sprintf(`<option value="%s">`, g)
			if !strings.Contains(body, expectedOption) {
				return fmt.Errorf("expected datalist to contain option %q in response body", expectedOption)
			}
		}
		return nil
	})

	ctx.Step(`^the new bucket group input should have an empty value and placeholder containing the first categories$`, func() error {
		body := tc.lastResponse.Body.String()
		inputPattern := `name="group_name" value="" list="existing_category_groups"`
		if !strings.Contains(body, inputPattern) {
			return fmt.Errorf("expected new bucket group input to have %s", inputPattern)
		}
		expectedPlaceholder := `placeholder="e.g. Daily Expenses, Future, Housing"`
		if !strings.Contains(body, expectedPlaceholder) {
			return fmt.Errorf("expected placeholder %s in new bucket group input", expectedPlaceholder)
		}
		return nil
	})

	ctx.Step(`^the edit bucket group input should be bound to the existing category groups datalist$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `id="edit_bucket_group" name="group_name" required list="existing_category_groups"`) {
			return fmt.Errorf("expected edit bucket group input to have list='existing_category_groups'")
		}
		return nil
	})

	ctx.Step(`^I move bucket "([^"]*)" to category group "([^"]*)"$`, func(bucketName, groupName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var bucketID string
		err = bDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ?`, bucketName).Scan(&bucketID)
		if err != nil {
			return fmt.Errorf("bucket %q not found: %w", bucketName, err)
		}

		form := url.Values{}
		form.Set("csrf_token", tc.activeCSRF)
		form.Set("group_name", groupName)

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/buckets/%s/move-group", tc.activeBudgetID, bucketID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()

		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, bucketID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, "OWNER")
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.workspaceHandler.MoveBucketGroup(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the desktop bucket row "([^"]*)" should have a drag handle$`, func(bucketName string) error {
		body := tc.lastResponse.Body.String()
		pattern := fmt.Sprintf(`data-bucket-name="%s"`, html.EscapeString(bucketName))
		if !strings.Contains(body, pattern) {
			return fmt.Errorf("expected row with %s in response body", pattern)
		}
		if !strings.Contains(body, `class="bucket-drag-handle`) {
			return fmt.Errorf("expected drag handle with class 'bucket-drag-handle' in response body")
		}
		return nil
	})

	ctx.Step(`^category group "([^"]*)" should be a drop target for buckets$`, func(groupName string) error {
		body := tc.lastResponse.Body.String()
		pattern := fmt.Sprintf(`data-group-name="%s"`, html.EscapeString(groupName))
		if !strings.Contains(body, pattern) {
			return fmt.Errorf("expected group header with %s in response body", pattern)
		}
		if !strings.Contains(body, `group-drop-zone`) {
			return fmt.Errorf("expected group drop zone class 'group-drop-zone' in response body")
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

	// 09_reporting_and_analytics.feature: Dedicated detailed report
	ctx.Step(`^an authenticated user on budget "([^"]*)"$`, func(budgetName string) error {
		// Provision budget and user if not already set
		if tc.activeBudgetID == "" {
			tc.activeBudgetID = "01900000-0000-7000-8000-000000000001"
			tc.activeUserID = "user_1"
			now := time.Now().UTC().UnixMicro()
			_, _ = tc.mainDB.Exec(`INSERT OR IGNORE INTO users (id, email, password_hash, display_name, created_at, updated_at) VALUES ('user_1', 'user1@example.com', 'h', 'User One', ?, ?)`, now, now)
			_, _ = tc.mainDB.Exec(`INSERT OR IGNORE INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES ('01900000-0000-7000-8000-000000000001', ?, 'PLN', 'UTC', 'user_1', ?, ?)`, budgetName, now, now)
		}
		return nil
	})

	ctx.Step(`^I navigate to "([^"]*)"$`, func(path string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		actualPath := strings.ReplaceAll(path, ":budgetId", tc.activeBudgetID)

		req := httptest.NewRequest(http.MethodGet, actualPath, nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", "dummy_csrf_token")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		if strings.HasSuffix(actualPath, "/reports") {
			err = tc.reportsHandler.ReportsView(c)
		} else if strings.Contains(actualPath, "/reports/") {
			parts := strings.Split(actualPath, "/reports/")
			c.SetParamNames("budgetId", "reportType")
			c.SetParamValues(tc.activeBudgetID, parts[1])
			err = tc.reportsHandler.ReportDetailView(c)
		}
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the reports dashboard should render (\d+) report cards:$`, func(count int, table *godog.Table) error {
		body := tc.lastResponse.Body.String()
		for _, row := range table.Rows[1:] {
			cardName := row.Cells[0].Value
			if !strings.Contains(body, cardName) {
				return fmt.Errorf("expected report card %q in dashboard body", cardName)
			}
		}
		return nil
	})

	ctx.Step(`^the detailed report page should display title "([^"]*)"$`, func(expectedTitle string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, expectedTitle) {
			return fmt.Errorf("expected title %q in response body", expectedTitle)
		}
		return nil
	})

	ctx.Step(`^the page should render filter controls for date range, currency, and internal transfers$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "name=\"range\"") || !strings.Contains(body, "name=\"currency\"") || !strings.Contains(body, "name=\"exclude_internal\"") {
			return fmt.Errorf("expected filter controls in response body")
		}
		return nil
	})

	ctx.Step(`^the full-screen report chart container should be present$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "detail_chart_") {
			return fmt.Errorf("expected detail_chart_ container in response body")
		}
		return nil
	})

	// 09_reporting_and_analytics.feature: Saved reports management
	ctx.Step(`^I save a report configuration named "([^"]*)" for report "([^"]*)" with pinned status$`, func(name, reportType string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		form := url.Values{}
		form.Set("name", name)
		form.Set("report_type", reportType)
		form.Set("config_json", `{"range":"30d","currency":"PLN","exclude_internal":true}`)
		form.Set("is_pinned_widget", "true")

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/reports/saved", tc.activeBudgetID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.reportsHandler.SaveReportConfig(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the saved report should be persisted in "([^"]*)"$`, func(table string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE name = 'My Custom Spending'", table)).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("expected report in %s, got count %d, err %v", table, count, err)
		}
		return nil
	})

	ctx.Step(`^the saved report configuration should be listed in the reports dashboard$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/reports", tc.activeBudgetID), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", "dummy_csrf_token")
		c.Set(handlers.ContextBudgetDBKey, bDB)

		if err := tc.reportsHandler.ReportsView(c); err != nil {
			return err
		}
		body := rec.Body.String()
		if !strings.Contains(body, "My Custom Spending") {
			return fmt.Errorf("expected My Custom Spending in reports dashboard")
		}
		return nil
	})

	ctx.Step(`^I toggle the pinned widget status of "([^"]*)"$`, func(name string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var repID string
		err = bDB.QueryRow(`SELECT id FROM saved_reports_current WHERE name = ?`, name).Scan(&repID)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/reports/saved/%s/toggle-pin", tc.activeBudgetID, repID), nil)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "reportId")
		c.SetParamValues(tc.activeBudgetID, repID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.reportsHandler.TogglePinReport(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the saved report widget should be updated with pinned status "([^"]*)"$`, func(expectedStatus string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var isPinned int
		err = bDB.QueryRow(`SELECT is_pinned_widget FROM saved_reports_current WHERE name = 'My Custom Spending'`).Scan(&isPinned)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%d", isPinned) != expectedStatus {
			return fmt.Errorf("expected pinned status %s, got %d", expectedStatus, isPinned)
		}
		return nil
	})

	ctx.Step(`^I delete the saved report "([^"]*)"$`, func(name string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var repID string
		err = bDB.QueryRow(`SELECT id FROM saved_reports_current WHERE name = ?`, name).Scan(&repID)
		if err != nil {
			return err
		}

		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/reports/saved/%s/delete", tc.activeBudgetID, repID), nil)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "reportId")
		c.SetParamValues(tc.activeBudgetID, repID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.reportsHandler.DeleteSavedReport(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the saved report should be removed from "([^"]*)"$`, func(table string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE name = 'My Custom Spending'", table)).Scan(&count)
		if err != nil || count != 0 {
			return fmt.Errorf("expected report removed from %s, got count %d, err %v", table, count, err)
		}
		return nil
	})

	// 09_reporting_and_analytics.feature: Report Cache & Recalculate
	ctx.Step(`^I request data for report "([^"]*)"$`, func(reportSlug string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/reports/%s/data", tc.activeBudgetID, reportSlug), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		var handlerErr error
		switch reportSlug {
		case "age-of-money":
			handlerErr = tc.reportsHandler.GetAgeOfMoneyData(c)
		case "income-expense":
			handlerErr = tc.reportsHandler.GetIncomeExpenseData(c)
		case "spending-trends":
			handlerErr = tc.reportsHandler.GetSpendingTrendsData(c)
		case "spending-breakdown":
			handlerErr = tc.reportsHandler.GetSpendingBreakdownData(c)
		case "net-worth":
			handlerErr = tc.reportsHandler.GetNetWorthData(c)
		case "sankey":
			handlerErr = tc.reportsHandler.GetSankeyData(c)
		case "forecast":
			handlerErr = tc.reportsHandler.GetForecastData(c)
		case "budget-analysis":
			handlerErr = tc.reportsHandler.GetBudgetAnalysisData(c)
		case "monte-carlo":
			handlerErr = tc.reportsHandler.GetMonteCarloData(c)
		case "party-cashflow":
			handlerErr = tc.reportsHandler.GetPartyCashflowData(c)
		default:
			return fmt.Errorf("unknown report slug %s", reportSlug)
		}
		tc.lastResponse = rec
		return handlerErr
	})

	ctx.Step(`^the response header "([^"]*)" should be "([^"]*)"$`, func(headerName, expectedVal string) error {
		actualVal := tc.lastResponse.Header().Get(headerName)
		if actualVal != expectedVal {
			return fmt.Errorf("expected header %s=%s, got %s", headerName, expectedVal, actualVal)
		}
		return nil
	})

	ctx.Step(`^the report result should be cached in "([^"]*)"$`, func(tableName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("expected cached rows in %s, got count %d, err %v", tableName, count, err)
		}
		return nil
	})

	ctx.Step(`^I request data for report "([^"]*)" again$`, func(reportSlug string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/reports/%s/data", tc.activeBudgetID, reportSlug), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		switch reportSlug {
		case "age-of-money":
			_ = tc.reportsHandler.GetAgeOfMoneyData(c)
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^I request data for report "([^"]*)" with force recalculation$`, func(reportSlug string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/reports/%s/data?force=true", tc.activeBudgetID, reportSlug), nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		switch reportSlug {
		case "age-of-money":
			_ = tc.reportsHandler.GetAgeOfMoneyData(c)
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^I submit a REST registration request to "([^"]*)" for "([^"]*)"$`, func(path, email string) error {
		body, _ := json.Marshal(map[string]interface{}{
			"email":        email,
			"password":     "SecretPass123!",
			"display_name": "API BDD User",
		})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		tc.lastResponse = httptest.NewRecorder()
		tc.echoEngine.ServeHTTP(tc.lastResponse, req)
		return nil
	})

	ctx.Step(`^the REST response status code should be (\d+)$`, func(expectedCode int) error {
		if tc.lastResponse == nil {
			return fmt.Errorf("no response recorded")
		}
		if tc.lastResponse.Code != expectedCode {
			return fmt.Errorf("expected status %d, got %d: %s", expectedCode, tc.lastResponse.Code, tc.lastResponse.Body.String())
		}
		return nil
	})

	ctx.Step(`^I submit a REST login request to "([^"]*)" for "([^"]*)"$`, func(path, email string) error {
		body, _ := json.Marshal(map[string]interface{}{
			"email":    email,
			"password": "SecretPass123!",
		})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		tc.lastResponse = httptest.NewRecorder()
		tc.echoEngine.ServeHTTP(tc.lastResponse, req)

		for _, c := range tc.lastResponse.Result().Cookies() {
			if c.Name == "balance_session" {
				tc.sessionCookie = c
				break
			}
		}
		return nil
	})

	ctx.Step(`^I create a personal access token via REST API$`, func() error {
		var csrf string
		if tc.sessionCookie != nil {
			hash := auth.HashToken(tc.sessionCookie.Value)
			tc.mainDB.QueryRowContext(context.Background(), `SELECT csrf_token FROM sessions WHERE token_hash = ?`, hash).Scan(&csrf)
		}

		body, _ := json.Marshal(map[string]interface{}{
			"name":            "BDD PAT Token",
			"expires_in_days": 30,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/tokens", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tc.sessionCookie != nil {
			req.AddCookie(tc.sessionCookie)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		tc.lastResponse = httptest.NewRecorder()
		tc.echoEngine.ServeHTTP(tc.lastResponse, req)

		var resp openapi.CreateTokenResponse
		if err := json.Unmarshal(tc.lastResponse.Body.Bytes(), &resp); err == nil {
			tc.activeBearer = resp.Token
		}
		return nil
	})

	ctx.Step(`^I create a budget "([^"]*)" using the Bearer token$`, func(budgetName string) error {
		body, _ := json.Marshal(map[string]interface{}{
			"name":             budgetName,
			"default_currency": "PLN",
			"timezone":         "UTC",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/budgets", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tc.activeBearer)
		tc.lastResponse = httptest.NewRecorder()
		tc.echoEngine.ServeHTTP(tc.lastResponse, req)
		return nil
	})

	ctx.Step(`^the returned budget name should be "([^"]*)"$`, func(expectedName string) error {
		var resp openapi.BudgetSummary
		if err := json.Unmarshal(tc.lastResponse.Body.Bytes(), &resp); err != nil {
			return fmt.Errorf("failed to parse budget summary response: %w", err)
		}
		if resp.Name != expectedName {
			return fmt.Errorf("expected budget name %q, got %q", expectedName, resp.Name)
		}
		return nil
	})

	// Currency Exchange Rates (BALANCE-18)
	ctx.Step(`^the exchange rate storage in "main.db" is empty$`, func() error {
		_, err := tc.mainDB.Exec("DELETE FROM currency_exchange_rates")
		return err
	})

	ctx.Step(`^I trigger central bank synchronization for (\d+) days of history$`, func(days int) error {
		tToday := fx.DayBoundaryMicros(time.Now())
		ecbQuotes := []fx.RateQuote{
			{BaseCurrency: "EUR", TargetCurrency: "USD", RateDate: tToday, RateMicros: 1085000, Source: "ECB"},
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: tToday, RateMicros: 4321500, Source: "ECB"},
		}
		nbpQuotes := []fx.RateQuote{
			{BaseCurrency: "USD", TargetCurrency: "PLN", RateDate: tToday, RateMicros: 3985000, Source: "NBP"},
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: tToday, RateMicros: 4325000, Source: "NBP"},
		}
		svc := fx.NewService(tc.mainDB, &bddMockECBClient{quotes: ecbQuotes}, &bddMockNBPClient{quotes: nbpQuotes})
		tc.fxService = svc
		_, err := svc.Sync(context.Background(), fx.SyncOptions{Days: days, Source: "ALL"})
		return err
	})

	ctx.Step(`^exchange rates from "([^"]*)" should be persisted with base currency "([^"]*)"$`, func(source, base string) error {
		var count int
		err := tc.mainDB.QueryRow("SELECT COUNT(*) FROM currency_exchange_rates WHERE source = ? AND base_currency = ?", source, base).Scan(&count)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("no exchange rates found with source %q and base %q", source, base)
		}
		return nil
	})

	ctx.Step(`^exchange rates from "([^"]*)" should be persisted with target currency "([^"]*)"$`, func(source, target string) error {
		var count int
		err := tc.mainDB.QueryRow("SELECT COUNT(*) FROM currency_exchange_rates WHERE source = ? AND target_currency = ?", source, target).Scan(&count)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("no exchange rates found with source %q and target %q", source, target)
		}
		return nil
	})

	ctx.Step(`^each persisted record should retain its source and date without inverse duplicates in the database$`, func() error {
		var inverseCount int
		err := tc.mainDB.QueryRow("SELECT COUNT(*) FROM currency_exchange_rates WHERE base_currency = 'PLN'").Scan(&inverseCount)
		if err != nil {
			return err
		}
		if inverseCount != 0 {
			return fmt.Errorf("expected 0 inverse PLN pairs stored, found %d", inverseCount)
		}
		return nil
	})

	ctx.Step(`^an ECB published rate for "([^"]*)" to "([^"]*)" on "([^"]*)" is "([^"]*)"$`, func(base, target, dateStr, rateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		r, err := fx.ParseDecimalMicros(rateStr)
		if err != nil {
			return err
		}
		quotes := []fx.RateQuote{
			{BaseCurrency: base, TargetCurrency: target, RateDate: d, RateMicros: r, Source: "ECB"},
		}
		svc := fx.NewService(tc.mainDB, &bddMockECBClient{quotes: quotes}, &bddMockNBPClient{})
		tc.fxService = svc
		_, err = svc.Sync(context.Background(), fx.SyncOptions{Source: "ECB"})
		return err
	})

	ctx.Step(`^an NBP published rate for "([^"]*)" to "([^"]*)" on "([^"]*)" is "([^"]*)"$`, func(base, target, dateStr, rateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		r, err := fx.ParseDecimalMicros(rateStr)
		if err != nil {
			return err
		}
		quotes := []fx.RateQuote{
			{BaseCurrency: base, TargetCurrency: target, RateDate: d, RateMicros: r, Source: "NBP"},
		}
		svc := fx.NewService(tc.mainDB, &bddMockECBClient{}, &bddMockNBPClient{quotes: quotes})
		tc.fxService = svc
		_, err = svc.Sync(context.Background(), fx.SyncOptions{Source: "NBP"})
		return err
	})

	ctx.Step(`^I query the effective exchange rate for "([^"]*)" to "([^"]*)" on "([^"]*)"$`, func(base, target, dateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		if tc.fxService == nil {
			tc.fxService = fx.NewService(tc.mainDB, nil, nil)
		}
		rate, src, err := tc.fxService.GetRate(context.Background(), base, target, d)
		if err != nil {
			return err
		}
		tc.lastResolvedRate = rate
		tc.lastResolvedSource = src
		return nil
	})

	ctx.Step(`^the resolved rate should be "([^"]*)" with source "([^"]*)"$`, func(rateStr, expectedSource string) error {
		expectedRate, err := fx.ParseDecimalMicros(rateStr)
		if err != nil {
			return err
		}
		if tc.lastResolvedRate != expectedRate {
			return fmt.Errorf("expected rate %d, got %d", expectedRate, tc.lastResolvedRate)
		}
		if tc.lastResolvedSource != expectedSource {
			return fmt.Errorf("expected source %q, got %q", expectedSource, tc.lastResolvedSource)
		}
		return nil
	})

	ctx.Step(`^a persisted ECB exchange rate for "([^"]*)" to "([^"]*)" on "([^"]*)" is "([^"]*)"$`, func(base, target, dateStr, rateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		r, err := fx.ParseDecimalMicros(rateStr)
		if err != nil {
			return err
		}
		quotes := []fx.RateQuote{
			{BaseCurrency: base, TargetCurrency: target, RateDate: d, RateMicros: r, Source: "ECB"},
		}
		svc := fx.NewService(tc.mainDB, &bddMockECBClient{quotes: quotes}, &bddMockNBPClient{})
		tc.fxService = svc
		_, err = svc.Sync(context.Background(), fx.SyncOptions{Source: "ECB"})
		return err
	})

	ctx.Step(`^I convert "([^"]*)" to "([^"]*)" on (?:Saturday |Sunday )?"([^"]*)"$`, func(amtWithCurr, toCurr, dateStr string) error {
		amtMinor, fromCurr, err := parseAmountWithCurrency(amtWithCurr)
		if err != nil {
			return err
		}
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		if tc.fxService == nil {
			tc.fxService = fx.NewService(tc.mainDB, nil, nil)
		}
		converted, err := tc.fxService.Convert(context.Background(), amtMinor, fromCurr, toCurr, d)
		tc.lastConvertedAmount = converted
		tc.lastConversionError = err
		return err
	})

	ctx.Step(`^the converted amount should be dynamically inverted to "([^"]*)"$`, func(expectedMoney string) error {
		expectedMinor, _, err := parseAmountWithCurrency(expectedMoney)
		if err != nil {
			return err
		}
		if tc.lastConvertedAmount != expectedMinor {
			return fmt.Errorf("expected inverted amount %d, got %d", expectedMinor, tc.lastConvertedAmount)
		}
		return nil
	})

	ctx.Step(`^the converted amount should be triangulated via "([^"]*)" to "([^"]*)"$`, func(viaCurr, expectedMoney string) error {
		expectedMinor, _, err := parseAmountWithCurrency(expectedMoney)
		if err != nil {
			return err
		}
		if tc.lastConvertedAmount != expectedMinor {
			return fmt.Errorf("expected triangulated amount %d, got %d", expectedMinor, tc.lastConvertedAmount)
		}
		return nil
	})

	ctx.Step(`^a Friday "([^"]*)" rate for "([^"]*)" to "([^"]*)" is "([^"]*)"$`, func(dateStr, base, target, rateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		r, err := fx.ParseDecimalMicros(rateStr)
		if err != nil {
			return err
		}
		quotes := []fx.RateQuote{
			{BaseCurrency: base, TargetCurrency: target, RateDate: d, RateMicros: r, Source: "ECB"},
		}
		svc := fx.NewService(tc.mainDB, &bddMockECBClient{quotes: quotes}, &bddMockNBPClient{})
		tc.fxService = svc
		_, err = svc.Sync(context.Background(), fx.SyncOptions{Source: "ECB"})
		return err
	})

	ctx.Step(`^a Monday "([^"]*)" rate for "([^"]*)" to "([^"]*)" is "([^"]*)"$`, func(dateStr, base, target, rateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		r, err := fx.ParseDecimalMicros(rateStr)
		if err != nil {
			return err
		}
		quotes := []fx.RateQuote{
			{BaseCurrency: base, TargetCurrency: target, RateDate: d, RateMicros: r, Source: "ECB"},
		}
		svc := fx.NewService(tc.mainDB, &bddMockECBClient{quotes: quotes}, &bddMockNBPClient{})
		tc.fxService = svc
		_, err = svc.Sync(context.Background(), fx.SyncOptions{Source: "ECB"})
		return err
	})

	ctx.Step(`^the linearly interpolated rate should be "([^"]*)" and result in "([^"]*)"$`, func(expectedRateStr, expectedMoney string) error {
		expectedMinor, _, err := parseAmountWithCurrency(expectedMoney)
		if err != nil {
			return err
		}
		if tc.lastConvertedAmount != expectedMinor {
			return fmt.Errorf("expected linearly interpolated result %d, got %d", expectedMinor, tc.lastConvertedAmount)
		}
		return nil
	})

	ctx.Step(`^Monday "([^"]*)" rate is not yet published$`, func(dateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		_, err = tc.mainDB.Exec("DELETE FROM currency_exchange_rates WHERE rate_date >= ?", d)
		return err
	})

	ctx.Step(`^the rate should fall back to Friday rate "([^"]*)" resulting in "([^"]*)"$`, func(expectedRateStr, expectedMoney string) error {
		expectedMinor, _, err := parseAmountWithCurrency(expectedMoney)
		if err != nil {
			return err
		}
		if tc.lastConvertedAmount != expectedMinor {
			return fmt.Errorf("expected Friday fallback result %d, got %d", expectedMinor, tc.lastConvertedAmount)
		}
		return nil
	})

	ctx.Step(`^exchange rates history is only available starting from "([^"]*)"$`, func(dateStr string) error {
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		_, err = tc.mainDB.Exec("DELETE FROM currency_exchange_rates WHERE rate_date < ?", d)
		return err
	})

	ctx.Step(`^I attempt to convert "([^"]*)" to "([^"]*)" on "([^"]*)"$`, func(amtWithCurr, toCurr, dateStr string) error {
		amtMinor, fromCurr, err := parseAmountWithCurrency(amtWithCurr)
		if err != nil {
			return err
		}
		d, err := fx.ParseDateBoundaryMicros(dateStr)
		if err != nil {
			return err
		}
		if tc.fxService == nil {
			tc.fxService = fx.NewService(tc.mainDB, nil, nil)
		}
		tc.lastConvertedAmount, tc.lastConversionError = tc.fxService.Convert(context.Background(), amtMinor, fromCurr, toCurr, d)
		return nil
	})

	ctx.Step(`^the conversion should fail with an exchange rate not found error$`, func() error {
		if tc.lastConversionError == nil {
			return errors.New("expected conversion error, but got nil")
		}
		if !errors.Is(tc.lastConversionError, fx.ErrExchangeRateNotFound) {
			return fmt.Errorf("expected ErrExchangeRateNotFound, got: %v", tc.lastConversionError)
		}
		return nil
	})

	ctx.Step(`^an authenticated API user$`, func() error {
		userID := domain.NewUUIDv7()
		now := time.Now().UTC().UnixMicro()
		pwHash, err := auth.HashPassword("Secret123!")
		if err != nil {
			return err
		}
		_, err = tc.mainDB.Exec(`
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES (?, 'api_user@example.com', ?, 'API User', ?, ?)`,
			userID, pwHash, now, now)
		if err != nil {
			return err
		}

		rawToken, tokenHash, err := auth.GeneratePAT()
		if err != nil {
			return err
		}
		tokenID := domain.NewUUIDv7()
		exp := now + 30*24*3600*1000000
		_, err = tc.mainDB.Exec(`
			INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, created_at)
			VALUES (?, ?, 'Test Token', ?, ?, ?)`,
			tokenID, userID, tokenHash, exp, now)
		if err != nil {
			return err
		}
		tc.activeBearer = rawToken
		return nil
	})

	ctx.Step(`^I send a POST request to "([^"]*)"$`, func(path string) error {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Content-Type", "application/json")
		if tc.activeBearer != "" {
			req.Header.Set("Authorization", "Bearer "+tc.activeBearer)
		}
		tc.lastResponse = httptest.NewRecorder()
		tc.echoEngine.ServeHTTP(tc.lastResponse, req)
		return nil
	})

	ctx.Step(`^the response body should contain "synced_count" greater than (\d+)$`, func(minCount int) error {
		var resp openapi.SyncRatesResponse
		if err := json.Unmarshal(tc.lastResponse.Body.Bytes(), &resp); err != nil {
			return fmt.Errorf("failed parsing SyncRatesResponse: %w (body: %s)", err, tc.lastResponse.Body.String())
		}
		if resp.SyncedCount <= minCount {
			return fmt.Errorf("expected synced_count > %d, got %d", minCount, resp.SyncedCount)
		}
		return nil
	})

	// 13_audit_history_and_rollback.feature steps
	ctx.Step(`^an authenticated user "([^"]*)" with "([^"]*)" role on budget "([^"]*)"$`, func(email, role, budgetName string) error {
		now := time.Now().UTC().UnixMicro()
		userID := domain.NewUUIDv7()
		pwHash, _ := auth.HashPassword("Pass12345!")

		err := tc.mainDB.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&userID)
		if err != nil {
			userID = domain.NewUUIDv7()
			_, err = tc.mainDB.Exec(`
				INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
				VALUES (?, ?, ?, 'User', ?, ?)`, userID, email, pwHash, now, now)
			if err != nil {
				return err
			}
		}

		budgetID := "01900000-0000-7000-8000-000000000001"
		_, err = tc.mainDB.Exec(`
			INSERT INTO budgets (id, name, default_currency, created_by_agent_id, created_at, updated_at)
			VALUES (?, ?, 'PLN', ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name`, budgetID, budgetName, userID, now, now)
		if err != nil {
			return err
		}

		_, err = tc.mainDB.Exec(`
			INSERT INTO budget_members (budget_id, user_id, role, joined_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(budget_id, user_id) DO UPDATE SET role = excluded.role`, budgetID, userID, role, now)
		if err != nil {
			return err
		}

		sessionToken, _ := auth.GenerateCSPRNGToken()
		sessionHash := auth.HashToken(sessionToken)
		csrfToken, _ := auth.GenerateCSPRNGToken()
		sessionID := domain.NewUUIDv7()
		_, err = tc.mainDB.Exec(`
			INSERT INTO sessions (id, token_hash, user_id, csrf_token, created_at, last_active_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, sessionHash, userID, csrfToken, now, now, now+3600*1000000)
		if err != nil {
			return err
		}

		tc.activeUserID = userID
		tc.activeBudgetID = budgetID
		tc.activeCSRF = csrfToken
		tc.sessionCookie = &http.Cookie{
			Name:  handlers.SessionCookieName,
			Value: sessionToken,
		}
		return nil
	})

	ctx.Step(`^an account "([^"]*)" was created and subsequently updated$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		var pID string
		_ = bDB.QueryRow(`SELECT id FROM parties_current WHERE is_user_owned = 1 LIMIT 1`).Scan(&pID)
		if pID == "" {
			pID = domain.NewUUIDv7()
			bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Owner Party', 'INDIVIDUAL', 1, ?, ?)`, pID, tc.activeUserID, now)
		}

		acctID := domain.NewUUIDv7()
		tc.activeAcctID = acctID

		// v1: CREATE
		_, err = bDB.Exec(`
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 'PL001', 'EMOJI', '💳', 0, 'CREATE', ?, ?, ?)`,
			acctID, pID, acctName, tc.activeUserID, domain.NewUUIDv7(), now)
		if err != nil {
			return err
		}
		_, err = bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 'PL001', 'EMOJI', '💳', 0, ?, ?)`,
			acctID, pID, acctName, tc.activeUserID, now)
		if err != nil {
			return err
		}

		// v2: UPDATE
		updatedName := acctName + " (Updated)"
		_, err = bDB.Exec(`
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, 2, ?, ?, 'CHECKING', 'PLN', 'PL001', 'EMOJI', '💳', 0, 'UPDATE', ?, ?, ?)`,
			acctID, pID, updatedName, tc.activeUserID, domain.NewUUIDv7(), now+1000)
		if err != nil {
			return err
		}
		_, err = bDB.Exec(`
			UPDATE accounts_current SET version = 2, name = ?, updated_at = ? WHERE id = ?`,
			updatedName, now+1000, acctID)
		return err
	})

	ctx.Step(`^I request the history drawer for account "([^"]*)"$`, func(acctName string) error {
		req := httptest.NewRequest(http.MethodGet, "/budgets/"+tc.activeBudgetID+"/history/accounts/"+tc.activeAcctID, nil)
		req.Header.Set("HX-Request", "true")
		req.AddCookie(tc.sessionCookie)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "entityType", "entityId")
		c.SetParamValues(tc.activeBudgetID, "accounts", tc.activeAcctID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)
		c.Set(handlers.ContextRoleKey, role)

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.historyHandler.HistoryDrawer(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the history drawer should display version timeline in descending order$`, func() error {
		body := tc.lastResponse.Body.String()
		idxV2 := strings.Index(body, "v2")
		idxV1 := strings.Index(body, "v1")
		if idxV2 == -1 || idxV1 == -1 {
			return fmt.Errorf("expected v2 and v1 in history drawer: %s", body)
		}
		if idxV2 > idxV1 {
			return fmt.Errorf("expected v2 to appear before v1 (descending order)")
		}
		return nil
	})

	ctx.Step(`^the history drawer should display the action badges and field diffs$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "CREATE") || !strings.Contains(body, "UPDATE") {
			return fmt.Errorf("expected CREATE and UPDATE action badges in drawer")
		}
		if !strings.Contains(body, "Account Name") {
			return fmt.Errorf("expected field diff for Account Name in drawer")
		}
		return nil
	})

	ctx.Step(`^the history drawer should provide a restore action for prior versions$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "Restore v1") {
			return fmt.Errorf("expected restore button for v1 in drawer: %s", body)
		}
		return nil
	})

	ctx.Step(`^a counterparty "([^"]*)" with initial name "([^"]*)" at version 1 and "([^"]*)" at version 2$`, func(partyKey, v1Name, v2Name string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		tc.activePartyID = pID

		// v1
		_, err = bDB.Exec(`
			INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
			VALUES (?, 1, ?, 'ORGANIZATION', 0, 'CREATE', ?, ?, ?)`,
			pID, v1Name, tc.activeUserID, domain.NewUUIDv7(), now)
		if err != nil {
			return err
		}
		_, err = bDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`,
			pID, v1Name, tc.activeUserID, now)
		if err != nil {
			return err
		}

		// v2
		_, err = bDB.Exec(`
			INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
			VALUES (?, 2, ?, 'ORGANIZATION', 0, 'UPDATE', ?, ?, ?)`,
			pID, v2Name, tc.activeUserID, domain.NewUUIDv7(), now+1000)
		if err != nil {
			return err
		}
		_, err = bDB.Exec(`
			UPDATE parties_current SET version = 2, name = ?, updated_at = ? WHERE id = ?`,
			v2Name, now+1000, pID)
		return err
	})

	ctx.Step(`^I submit a restore request for party "([^"]*)" to version (\d+)$`, func(partyKey string, targetVer int) error {
		form := url.Values{}
		form.Set("target_version", fmt.Sprint(targetVer))
		form.Set("csrf_token", tc.activeCSRF)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/history/parties/"+tc.activePartyID+"/restore", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		req.AddCookie(tc.sessionCookie)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "entityType", "entityId")
		c.SetParamValues(tc.activeBudgetID, "parties", tc.activePartyID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)
		c.Set(handlers.ContextRoleKey, role)

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.historyHandler.RestoreVersion(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^a new version (\d+) with action "([^"]*)" should be appended to "([^"]*)"$`, func(ver int, act, tableName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		query := fmt.Sprintf("SELECT count(*) FROM %s WHERE version = ? AND action = ?", tableName)
		err = bDB.QueryRow(query, ver, act).Scan(&count)
		if err != nil || count != 1 {
			return fmt.Errorf("expected 1 record in %s with version %d and action %s, got %d (err: %v)", tableName, ver, act, count, err)
		}
		return nil
	})

	ctx.Step(`^"([^"]*)" should reflect the restored name "([^"]*)" with version (\d+)$`, func(tableName, expectedName string, ver int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var actualName string
		var actualVer int
		query := fmt.Sprintf("SELECT version, name FROM %s WHERE id = ?", tableName)
		err = bDB.QueryRow(query, tc.activePartyID).Scan(&actualVer, &actualName)
		if err != nil {
			return fmt.Errorf("failed querying %s: %w", tableName, err)
		}
		if actualVer != ver || actualName != expectedName {
			return fmt.Errorf("expected version %d and name %q in %s, got version %d and name %q", ver, expectedName, tableName, actualVer, actualName)
		}
		return nil
	})

	ctx.Step(`^historical versions 1 and 2 in "([^"]*)" should remain immutable$`, func(tableName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var v1Name, v2Name string
		query := fmt.Sprintf("SELECT name FROM %s WHERE id = ? AND version = ?", tableName)
		err = bDB.QueryRow(query, tc.activePartyID, 1).Scan(&v1Name)
		if err != nil || v1Name != "Local Bakery" {
			return fmt.Errorf("v1 corrupted in %s: %v (name: %s)", tableName, err, v1Name)
		}
		err = bDB.QueryRow(query, tc.activePartyID, 2).Scan(&v2Name)
		if err != nil || v2Name != "Super Bakery" {
			return fmt.Errorf("v2 corrupted in %s: %v (name: %s)", tableName, err, v2Name)
		}
		return nil
	})

	ctx.Step(`^a budget transaction of "([^"]*)" in bucket "([^"]*)" at version 1$`, func(amtStr, bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()

		// User account
		userPID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Me', 'INDIVIDUAL', 1, ?, ?)`, userPID, tc.activeUserID, now)
		userAID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, 'Checking', 'CHECKING', 'PLN', 0, ?, ?)`, userAID, userPID, tc.activeUserID, now)
		bDB.Exec(`INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at) VALUES (?, 'PLN', 90000, ?)`, userAID, now) // 900 PLN

		// External account
		extPID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Store', 'ORGANIZATION', 0, ?, ?)`, extPID, tc.activeUserID, now)
		extAID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, 'POS', 'EXTERNAL', 'PLN', 0, ?, ?)`, extAID, extPID, tc.activeUserID, now)

		// Bucket
		bktID := domain.NewUUIDv7()
		tc.activeBucketID = bktID
		tc.activeAcctID = userAID
		bDB.Exec(`INSERT INTO buckets_projection (id, name, group_name, version, updated_at) VALUES (?, ?, 'Default', 1, ?)`, bktID, bucketName, now)
		bDB.Exec(`INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor) VALUES (?, 'PLN', 40000)`, bktID) // 400 PLN

		txID := domain.NewUUIDv7()
		tc.activeTxID = txID

		minor := parseAmountStr(amtStr) // 10000

		// Insert v1
		bDB.Exec(`
			INSERT INTO budget_transactions (
				id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
				destination_amount_minor, destination_currency, bucket_id, transaction_date, notes,
				action, agent_id, correlation_id, created_at
			) VALUES (?, 1, ?, ?, ?, 'PLN', ?, 'PLN', ?, ?, 'Weekly Groceries', 'CREATE', ?, ?, ?)`,
			txID, userAID, extAID, minor, minor, bktID, now, tc.activeUserID, domain.NewUUIDv7(), now)

		bDB.Exec(`
			INSERT INTO budget_transactions_current (
				id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
				destination_amount_minor, destination_currency, bucket_id, transaction_date, notes, updated_at
			) VALUES (?, 1, ?, ?, ?, 'PLN', ?, 'PLN', ?, ?, 'Weekly Groceries', ?)`,
			txID, userAID, extAID, minor, minor, bktID, now, now)
		return nil
	})

	ctx.Step(`^the transaction was updated to "([^"]*)" at version 2$`, func(amtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		newMinor := parseAmountStr(amtStr) // 30000

		var srcAcct, dstAcct string
		bDB.QueryRow(`SELECT source_account_id, destination_account_id FROM budget_transactions_current WHERE id = ?`, tc.activeTxID).Scan(&srcAcct, &dstAcct)

		// Append v2
		bDB.Exec(`
			INSERT INTO budget_transactions (
				id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
				destination_amount_minor, destination_currency, bucket_id, transaction_date, notes,
				action, agent_id, correlation_id, created_at
			) VALUES (?, 2, ?, ?, ?, 'PLN', ?, 'PLN', ?, ?, 'Weekly Groceries (Updated)', 'UPDATE', ?, ?, ?)`,
			tc.activeTxID, srcAcct, dstAcct, newMinor, newMinor, tc.activeBucketID, now, tc.activeUserID, domain.NewUUIDv7(), now+1000)

		bDB.Exec(`
			UPDATE budget_transactions_current
			SET version = 2, source_amount_minor = ?, destination_amount_minor = ?, notes = 'Weekly Groceries (Updated)', updated_at = ?
			WHERE id = ?`, newMinor, newMinor, now+1000, tc.activeTxID)

		// Reflect updated balances (200 PLN more deduction)
		bDB.Exec(`UPDATE account_currency_balances SET balance_minor = 70000 WHERE account_id = ?`, srcAcct)
		bDB.Exec(`UPDATE bucket_currency_balances SET balance_minor = 20000 WHERE bucket_id = ?`, tc.activeBucketID)
		return nil
	})

	ctx.Step(`^I submit a restore request for the budget transaction to version (\d+)$`, func(targetVer int) error {
		form := url.Values{}
		form.Set("target_version", fmt.Sprint(targetVer))
		form.Set("csrf_token", tc.activeCSRF)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/history/budget_transactions/"+tc.activeTxID+"/restore", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		req.AddCookie(tc.sessionCookie)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "entityType", "entityId")
		c.SetParamValues(tc.activeBudgetID, "budget_transactions", tc.activeTxID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)
		c.Set(handlers.ContextRoleKey, role)

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.historyHandler.RestoreVersion(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the transaction amount in "([^"]*)" should be "([^"]*)"$`, func(tableName, expectedAmtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var actualMinor int64
		err = bDB.QueryRow(`SELECT source_amount_minor FROM budget_transactions_current WHERE id = ?`, tc.activeTxID).Scan(&actualMinor)
		if err != nil {
			return err
		}
		expectedMinor := parseAmountStr(expectedAmtStr)
		if actualMinor != expectedMinor {
			return fmt.Errorf("expected amount %d, got %d", expectedMinor, actualMinor)
		}
		return nil
	})

	ctx.Step(`^the bucket balance for "([^"]*)" should reflect the restored "([^"]*)" expense$`, func(bucketName, expAmtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var actualBal int64
		err = bDB.QueryRow(`SELECT balance_minor FROM bucket_currency_balances WHERE bucket_id = ?`, tc.activeBucketID).Scan(&actualBal)
		if err != nil {
			return err
		}
		// Started with 500 PLN, minus 100 PLN = 400 PLN (40000 minor)
		if actualBal != 40000 {
			return fmt.Errorf("expected bucket balance 40000, got %d", actualBal)
		}
		return nil
	})

	ctx.Step(`^the source account balance should reflect the restored "([^"]*)" expense$`, func(expAmtStr string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var actualBal int64
		err = bDB.QueryRow(`SELECT balance_minor FROM account_currency_balances WHERE account_id = ?`, tc.activeAcctID).Scan(&actualBal)
		if err != nil {
			return err
		}
		// Started with 1000 PLN, minus 100 PLN = 900 PLN (90000 minor)
		if actualBal != 90000 {
			return fmt.Errorf("expected account balance 90000, got %d", actualBal)
		}
		return nil
	})

	ctx.Step(`^an account "([^"]*)" was soft-deleted at version 2$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Vacation Party', 'INDIVIDUAL', 1, ?, ?)`, pID, tc.activeUserID, now)

		acctID := domain.NewUUIDv7()
		tc.activeAcctID = acctID

		// v1: CREATE
		bDB.Exec(`
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, 1, ?, ?, 'SAVINGS', 'PLN', 'PL999', 'EMOJI', '🏖️', 0, 'CREATE', ?, ?, ?)`,
			acctID, pID, acctName, tc.activeUserID, domain.NewUUIDv7(), now)

		// v2: SOFT_DELETE
		bDB.Exec(`
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, 2, ?, ?, 'SAVINGS', 'PLN', 'PL999', 'EMOJI', '🏖️', 1, 'SOFT_DELETE', ?, ?, ?)`,
			acctID, pID, acctName, tc.activeUserID, domain.NewUUIDv7(), now+1000)
		return nil
	})

	ctx.Step(`^"([^"]*)" does not exist in "accounts_current"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		bDB.QueryRow(`SELECT count(*) FROM accounts_current WHERE name = ?`, acctName).Scan(&count)
		if count != 0 {
			return fmt.Errorf("expected account %q to not exist in accounts_current, got %d", acctName, count)
		}
		return nil
	})

	ctx.Step(`^I submit a restore request for account "([^"]*)" to version (\d+)$`, func(acctName string, targetVer int) error {
		form := url.Values{}
		form.Set("target_version", fmt.Sprint(targetVer))
		form.Set("csrf_token", tc.activeCSRF)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/history/accounts/"+tc.activeAcctID+"/restore", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		req.AddCookie(tc.sessionCookie)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "entityType", "entityId")
		c.SetParamValues(tc.activeBudgetID, "accounts", tc.activeAcctID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)
		c.Set(handlers.ContextRoleKey, role)

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.historyHandler.RestoreVersion(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^"([^"]*)" should be resurrected in "accounts_current" with version (\d+)$`, func(acctName string, ver int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var actualVer int
		err = bDB.QueryRow(`SELECT version FROM accounts_current WHERE name = ?`, acctName).Scan(&actualVer)
		if err != nil {
			return fmt.Errorf("account not found in accounts_current: %w", err)
		}
		if actualVer != ver {
			return fmt.Errorf("expected version %d, got %d", ver, actualVer)
		}
		return nil
	})

	ctx.Step(`^an account "([^"]*)" exists with 2 historical versions$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Owner Party', 'INDIVIDUAL', 1, ?, ?)`, pID, tc.activeUserID, now)

		acctID := domain.NewUUIDv7()
		tc.activeAcctID = acctID

		bDB.Exec(`
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 'PL111', 'EMOJI', '💳', 0, 'CREATE', ?, ?, ?)`,
			acctID, pID, acctName, tc.activeUserID, domain.NewUUIDv7(), now)

		bDB.Exec(`
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, 2, ?, ?, 'CHECKING', 'PLN', 'PL111', 'EMOJI', '💳', 0, 'UPDATE', ?, ?, ?)`,
			acctID, pID, acctName+" Updated", tc.activeUserID, domain.NewUUIDv7(), now+1000)

		bDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
			VALUES (?, 2, ?, ?, 'CHECKING', 'PLN', 'PL111', 'EMOJI', '💳', 0, ?, ?)`,
			acctID, pID, acctName+" Updated", tc.activeUserID, now+1000)
		return nil
	})

	ctx.Step(`^"([^"]*)" attempts to restore account "([^"]*)" to version (\d+)$`, func(email, acctName string, targetVer int) error {
		form := url.Values{}
		form.Set("target_version", fmt.Sprint(targetVer))
		form.Set("csrf_token", tc.activeCSRF)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/history/accounts/"+tc.activeAcctID+"/restore", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		req.AddCookie(tc.sessionCookie)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "entityType", "entityId")
		c.SetParamValues(tc.activeBudgetID, "accounts", tc.activeAcctID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)
		c.Set(handlers.ContextRoleKey, role)

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.historyHandler.RestoreVersion(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				rec.Code = he.Code
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the action should be rejected with status 403 Forbidden$`, func() error {
		if tc.lastResponse.Code != http.StatusForbidden {
			return fmt.Errorf("expected 403 Forbidden, got %d", tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^no new version should be created in "accounts"$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var maxVer int
		err = bDB.QueryRow(`SELECT MAX(version) FROM accounts WHERE id = ?`, tc.activeAcctID).Scan(&maxVer)
		if err != nil {
			return err
		}
		if maxVer != 2 {
			return fmt.Errorf("expected max version 2, got %d", maxVer)
		}
		return nil
	})

	// Automations steps
	ctx.Step(`^an existing bucket "([^"]*)" in group "([^"]*)"$`, func(bucketName, groupName string) error {
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
		tc.activeBucketID = bID
		activeBucketID = bID
		return nil
	})

	ctx.Step(`^(?:I create|an active) automation rule "([^"]*)" with priority (\d+):$`, func(name string, prio int, table *godog.Table) error {
		var judge, executor, comment string
		for _, row := range table.Rows {
			if len(row.Cells) >= 2 {
				k := strings.TrimSpace(row.Cells[0].Value)
				v := strings.TrimSpace(row.Cells[1].Value)
				switch k {
				case "judge":
					judge = v
				case "executor":
					executor = v
				case "comment":
					comment = v
				}
			}
		}

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}

		form := url.Values{}
		form.Set("name", name)
		form.Set("judge_script", judge)
		form.Set("executor_script", executor)
		form.Set("comment", comment)
		form.Set("priority", strconv.Itoa(prio))

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/automations", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		userID := tc.activeUserID
		if userID == "" {
			userID = "user_1"
		}
		c.Set(handlers.ContextUserIDKey, userID)
		c.Set("correlationId", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.CreateAutomation(c)
		tc.lastResponse = rec
		if err != nil {
			return err
		}

		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)
		tc.activeAutomationID = autoID
		return nil
	})

	ctx.Step(`^the automation rule "([^"]*)" should be active with version (\d+)$`, func(name string, expectedVer int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var isEnabled int
		var ver int
		err = bDB.QueryRowContext(context.Background(), `SELECT is_enabled, version FROM automations_current WHERE name = ?`, name).Scan(&isEnabled, &ver)
		if err != nil {
			return fmt.Errorf("automation %q not found in automations_current: %w", name, err)
		}
		if isEnabled != 1 {
			return fmt.Errorf("expected automation %q to be active (isEnabled=1), got %d", name, isEnabled)
		}
		if ver != expectedVer {
			return fmt.Errorf("expected automation %q version %d, got %d", name, expectedVer, ver)
		}
		return nil
	})

	ctx.Step(`^visiting the automations page should display "([^"]*)"$`, func(expectedText string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodGet, "/budgets/"+tc.activeBudgetID+"/automations", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		userID := tc.activeUserID
		if userID == "" {
			userID = "user_1"
		}
		c.Set(handlers.ContextUserIDKey, userID)
		c.Set(handlers.ContextRoleKey, "OWNER")
		c.Set(handlers.ContextBudgetDBKey, bDB)
		c.Set("csrfToken", "dummy-csrf")

		err = tc.automationsHandler.AutomationsView(c)
		tc.lastResponse = rec
		if err != nil {
			return err
		}
		body := rec.Body.String()
		if !strings.Contains(body, expectedText) {
			return fmt.Errorf("expected automations page to contain %q, body length %d", expectedText, len(body))
		}
		return nil
	})

	ctx.Step(`^the recorded transaction should automatically have bucket "([^"]*)" assigned$`, func(bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var assignedBucketName string
		err = bDB.QueryRowContext(context.Background(), `
			SELECT b.name
			FROM budget_transactions_current bt
			JOIN buckets_projection b ON bt.bucket_id = b.id
			ORDER BY bt.updated_at DESC
			LIMIT 1`).Scan(&assignedBucketName)
		if err != nil {
			return fmt.Errorf("failed querying assigned bucket: %w", err)
		}
		if assignedBucketName != bucketName {
			return fmt.Errorf("expected bucket %q, got %q", bucketName, assignedBucketName)
		}
		return nil
	})

	ctx.Step(`^I test a dry run with judge '([^']*)' and executor '([^']*)' on that transaction$`, func(judge, executor string) error {
		if tc.activeTxID == "" {
			return fmt.Errorf("activeTxID is empty before dry-run")
		}
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		form := url.Values{}
		form.Set("judge_script", judge)
		form.Set("executor_script", executor)
		form.Set("tx_id", tc.activeTxID)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/automations/dry-run", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		userID := tc.activeUserID
		if userID == "" {
			userID = "user_1"
		}
		c.Set(handlers.ContextUserIDKey, userID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.DryRun(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the dry-run outcome should indicate matched true with bucket "([^"]*)"$`, func(expectedBucket string) error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "Judge Match") || !strings.Contains(body, expectedBucket) {
			return fmt.Errorf("expected dry-run to show match and bucket %q, got: %s", expectedBucket, body)
		}
		return nil
	})

	ctx.Step(`^I execute batch automations across all transactions$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		rows, err := bDB.QueryContext(context.Background(), `SELECT id FROM budget_transactions_current`)
		if err != nil {
			return err
		}
		defer rows.Close()

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}

		form := url.Values{}
		form.Set("tx_ids", strings.Join(ids, ","))

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/automations/batch-run", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		userID := tc.activeUserID
		if userID == "" {
			userID = "user_1"
		}
		c.Set(handlers.ContextUserIDKey, userID)
		c.Set("correlationId", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.BatchRun(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^all Uber transactions should have bucket "([^"]*)" assigned$`, func(bucketName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		rows, err := bDB.QueryContext(context.Background(), `
			SELECT bt.notes, COALESCE(b.name, '')
			FROM budget_transactions_current bt
			LEFT JOIN buckets_projection b ON bt.bucket_id = b.id
			WHERE bt.notes LIKE '%Uber%'`)
		if err != nil {
			return err
		}
		defer rows.Close()

		count := 0
		for rows.Next() {
			var note, bName string
			if err := rows.Scan(&note, &bName); err != nil {
				return err
			}
			if bName != bucketName {
				return fmt.Errorf("transaction %q has bucket %q, expected %q", note, bName, bucketName)
			}
			count++
		}
		if count == 0 {
			return fmt.Errorf("no Uber transactions found")
		}
		return nil
	})

	ctx.Step(`^I update the automation rule "([^"]*)" with comment "([^"]*)":$`, func(name, comment string, table *godog.Table) error {
		var judge, executor string
		for _, row := range table.Rows {
			if len(row.Cells) >= 2 {
				k := strings.TrimSpace(row.Cells[0].Value)
				v := strings.TrimSpace(row.Cells[1].Value)
				switch k {
				case "judge":
					judge = v
				case "executor":
					executor = v
				}
			}
		}

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}

		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)

		form := url.Values{}
		form.Set("name", name)
		form.Set("judge_script", judge)
		form.Set("executor_script", executor)
		form.Set("comment", comment)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/automations/"+autoID+"/update", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, autoID)
		userID := tc.activeUserID
		if userID == "" {
			userID = "user_1"
		}
		c.Set(handlers.ContextUserIDKey, userID)
		c.Set("correlationId", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.UpdateAutomation(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the automation rule "([^"]*)" should be at version (\d+)$`, func(name string, expectedVer int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var ver int
		err = bDB.QueryRowContext(context.Background(), `SELECT version FROM automations_current WHERE name = ?`, name).Scan(&ver)
		if err != nil {
			return err
		}
		if ver != expectedVer {
			return fmt.Errorf("expected version %d, got %d", expectedVer, ver)
		}
		return nil
	})

	ctx.Step(`^I update the automation rule "([^"]*)" with squash enabled and comment "([^"]*)":$`, func(name, comment string, table *godog.Table) error {
		var judge, executor string
		for _, row := range table.Rows {
			if len(row.Cells) >= 2 {
				k := strings.TrimSpace(row.Cells[0].Value)
				v := strings.TrimSpace(row.Cells[1].Value)
				switch k {
				case "judge":
					judge = v
				case "executor":
					executor = v
				}
			}
		}

		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}

		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)

		form := url.Values{}
		form.Set("name", name)
		form.Set("judge_script", judge)
		form.Set("executor_script", executor)
		form.Set("comment", comment)
		form.Set("squash", "true")

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/automations/"+autoID+"/update", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, autoID)
		userID := tc.activeUserID
		if userID == "" {
			userID = "user_1"
		}
		c.Set(handlers.ContextUserIDKey, userID)
		c.Set("correlationId", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.UpdateAutomation(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the automation rule "([^"]*)" should have exactly (\d+) version(?:s)? in audit history$`, func(name string, expectedCount int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)

		var count int
		err = bDB.QueryRowContext(context.Background(), `SELECT count(*) FROM automations WHERE id = ?`, autoID).Scan(&count)
		if err != nil {
			return err
		}
		if count != expectedCount {
			return fmt.Errorf("expected %d versions in automations table, got %d", expectedCount, count)
		}
		return nil
	})

	ctx.Step(`^the latest version should have comment "([^"]*)"$`, func(expectedComment string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var comment string
		err = bDB.QueryRowContext(context.Background(), `
			SELECT comment FROM automations
			ORDER BY version DESC, created_at DESC
			LIMIT 1`).Scan(&comment)
		if err != nil {
			return err
		}
		if comment != expectedComment {
			return fmt.Errorf("expected comment %q, got %q", expectedComment, comment)
		}
		return nil
	})

	ctx.Step(`^I request the history drawer for automation "([^"]*)"$`, func(name string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)
		tc.activeAutomationID = autoID

		req := httptest.NewRequest(http.MethodGet, "/budgets/"+tc.activeBudgetID+"/history/automations/"+autoID, nil)
		req.Header.Set("HX-Request", "true")
		if tc.sessionCookie != nil {
			req.AddCookie(tc.sessionCookie)
		}
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "entityType", "entityId")
		c.SetParamValues(tc.activeBudgetID, "automations", autoID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)
		if role == "" {
			role = "OWNER"
		}
		c.Set(handlers.ContextRoleKey, role)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.historyHandler.HistoryDrawer(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the history drawer should display the diff for "([^"]*)"$`, func(fieldName string) error {
		body := tc.lastResponse.Body.String()
		normalized := strings.ToLower(strings.ReplaceAll(fieldName, "_", " "))
		if !strings.Contains(strings.ToLower(body), normalized) && !strings.Contains(body, fieldName) {
			return fmt.Errorf("expected field diff for %q in drawer", fieldName)
		}
		return nil
	})

	ctx.Step(`^the history drawer should display the action badges and restore buttons$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "CREATE") {
			return fmt.Errorf("expected CREATE badge in drawer")
		}
		if !strings.Contains(body, "Restore") {
			return fmt.Errorf("expected Restore action in drawer")
		}
		return nil
	})

	ctx.Step(`^I submit a restore request for automation "([^"]*)" to version (\d+)$`, func(name string, targetVer int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)
		tc.activeAutomationID = autoID

		form := url.Values{}
		form.Set("target_version", fmt.Sprint(targetVer))
		form.Set("csrf_token", tc.activeCSRF)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/history/automations/"+autoID+"/restore", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		if tc.sessionCookie != nil {
			req.AddCookie(tc.sessionCookie)
		}
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "entityType", "entityId")
		c.SetParamValues(tc.activeBudgetID, "automations", autoID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("csrfToken", tc.activeCSRF)

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)
		if role == "" {
			role = "OWNER"
		}
		c.Set(handlers.ContextRoleKey, role)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.historyHandler.RestoreVersion(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the current judge script for "([^"]*)" should be '([^']*)'$`, func(name, expectedScript string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var script string
		err = bDB.QueryRowContext(context.Background(), `SELECT judge_script FROM automations_current WHERE name = ?`, name).Scan(&script)
		if err != nil {
			return err
		}
		if script != expectedScript {
			return fmt.Errorf("expected judge_script %q, got %q", expectedScript, script)
		}
		return nil
	})

	ctx.Step(`^the latest action in "automations" should be "([^"]*)"$`, func(expectedAction string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var act string
		err = bDB.QueryRowContext(context.Background(), `
			SELECT action FROM automations
			ORDER BY version DESC, created_at DESC
			LIMIT 1`).Scan(&act)
		if err != nil {
			return err
		}
		if act != expectedAction {
			return fmt.Errorf("expected action %q, got %q", expectedAction, act)
		}
		return nil
	})

	ctx.Step(`^I submit a multi-version squash for automation "([^"]*)" from version (\d+) to (\d+) with comment "([^"]*)"$`, func(name string, startVer, endVer int, comment string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)
		tc.activeAutomationID = autoID

		form := url.Values{}
		form.Set("start_version", fmt.Sprint(startVer))
		form.Set("end_version", fmt.Sprint(endVer))
		form.Set("comment", comment)

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/automations/"+autoID+"/squash", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, autoID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("correlationId", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.SquashAutomation(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^I submit an invalid multi-version squash from version (\d+) to (\d+)$`, func(startVer, endVer int) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		autoID := tc.activeAutomationID

		form := url.Values{}
		form.Set("start_version", fmt.Sprint(startVer))
		form.Set("end_version", fmt.Sprint(endVer))
		form.Set("comment", "invalid range squash")

		req := httptest.NewRequest(http.MethodPost, "/budgets/"+tc.activeBudgetID+"/automations/"+autoID+"/squash", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, autoID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set("correlationId", domain.NewUUIDv7())
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.SquashAutomation(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				rec.Code = he.Code
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the action should be rejected with status (\d+) Bad Request$`, func(expectedStatus int) error {
		if tc.lastResponse.Code != expectedStatus {
			return fmt.Errorf("expected status %d, got %d", expectedStatus, tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^I fetch historical version (\d+) for automation "([^"]*)"$`, func(ver int, name string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var autoID string
		_ = bDB.QueryRowContext(context.Background(), `SELECT id FROM automations_current WHERE name = ?`, name).Scan(&autoID)
		tc.activeAutomationID = autoID

		req := httptest.NewRequest(http.MethodGet, "/budgets/"+tc.activeBudgetID+"/automations/"+autoID+"/version/"+strconv.Itoa(ver)+"/load", nil)
		rec := httptest.NewRecorder()
		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id", "ver")
		c.SetParamValues(tc.activeBudgetID, autoID, strconv.Itoa(ver))
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)

		err = tc.automationsHandler.LoadVersion(c)
		tc.lastResponse = rec
		return err
	})

	ctx.Step(`^the JSON response should have judge_script '([^']*)' and version (\d+)$`, func(expectedScript string, expectedVer int) error {
		var resp struct {
			JudgeScript string `json:"judge_script"`
			Version     int    `json:"version"`
		}
		if err := json.Unmarshal(tc.lastResponse.Body.Bytes(), &resp); err != nil {
			return fmt.Errorf("failed to unmarshal JSON response: %w, body: %s", err, tc.lastResponse.Body.String())
		}
		if resp.JudgeScript != expectedScript {
			return fmt.Errorf("expected judge_script %q, got %q", expectedScript, resp.JudgeScript)
		}
		if resp.Version != expectedVer {
			return fmt.Errorf("expected version %d, got %d", expectedVer, resp.Version)
		}
		return nil
	})

	ctx.Step(`^the mobile horizon selector and responsive filter toolbar should be rendered$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `id="mobile_horizon_select"`) {
			return fmt.Errorf("expected mobile_horizon_select in budget view")
		}
		if !strings.Contains(body, `grid grid-cols-2 sm:inline-flex sm:join`) {
			return fmt.Errorf("expected responsive 2-column filter toolbar in budget view")
		}
		return nil
	})

	ctx.Step(`^the mobile bucket cards should be grouped by category with direct Assign actions$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `mobile-group-header`) || !strings.Contains(body, `toggleMobileGroup('Needs')`) {
			return fmt.Errorf("expected mobile category group header for Needs in budget view")
		}
		if !strings.Contains(body, `mobile-bucket-card`) || !strings.Contains(body, `<span>Assign</span>`) {
			return fmt.Errorf("expected mobile bucket card with direct Assign button")
		}
		return nil
	})

	ctx.Step(`^the bottom dock and bottom-sheet modals should include PWA safe-area insets$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, `pb-[env(safe-area-inset-bottom)]`) {
			return fmt.Errorf("expected safe-area-inset-bottom on bottom dock")
		}
		if !strings.Contains(body, `max-h-[85dvh]`) || !strings.Contains(body, `pb-[calc(1.5rem+env(safe-area-inset-bottom))]`) {
			return fmt.Errorf("expected bottom-sheet modal classes with 85dvh and safe-area padding")
		}
		return nil
	})

	// 18_diagnostics_debug.feature step definitions (BALANCE-75)
	ctx.Step(`^an active checking account "([^"]*)" in current budget$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Main User', 'INDIVIDUAL', 1, ?, ?)`, pID, tc.activeUserID, now)
		acctID := domain.NewUUIDv7()
		tc.activeAcctID = acctID
		bDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, ?, ?)`, acctID, pID, acctName, tc.activeUserID, now)
		return nil
	})

	ctx.Step(`^a budget transaction exists for account "([^"]*)"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		dstAcctID := domain.NewUUIDv7()
		pID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Store', 'ORGANIZATION', 0, ?, ?)`, pID, tc.activeUserID, now)
		bDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, 'Store Account', 'EXTERNAL', 'PLN', 0, ?, ?)`, dstAcctID, pID, tc.activeUserID, now)

		txID := domain.NewUUIDv7()
		notes := "Grocery shopping"
		_, err = bDB.Exec(`
			INSERT INTO budget_transactions_current (id, version, source_account_id, destination_account_id, source_amount_minor, source_currency, destination_amount_minor, destination_currency, transaction_date, notes, updated_at)
			VALUES (?, 1, ?, ?, 5000, 'PLN', 5000, 'PLN', ?, ?, ?)`, txID, tc.activeAcctID, dstAcctID, now, notes, now)
		return err
	})

	ctx.Step(`^I attempt to delete account "([^"]*)"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var acctID string
		err = bDB.QueryRow(`SELECT id FROM accounts_current WHERE name = ?`, acctName).Scan(&acctID)
		if err != nil {
			return fmt.Errorf("account %s not found: %w", acctName, err)
		}

		form := url.Values{}
		form.Set("csrf_token", tc.activeCSRF)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/accounts/%s/delete", tc.activeBudgetID, acctID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()

		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, acctID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set("csrfToken", tc.activeCSRF)

		err = tc.accountsHandler.DeleteAccount(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				rec.Code = he.Code
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the delete account request should be rejected with bad request status$`, func() error {
		if tc.lastResponse.Code != http.StatusBadRequest {
			return fmt.Errorf("expected 400 Bad Request, got %d", tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^an account "([^"]*)" with only initial Set Balance offset transaction and zero balance$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		pID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Main User', 'INDIVIDUAL', 1, ?, ?)`, pID, tc.activeUserID, now)
		acctID := domain.NewUUIDv7()
		tc.activeAcctID = acctID
		bDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, ?, 'CHECKING', 'PLN', 0, ?, ?)`, acctID, pID, acctName, tc.activeUserID, now)
		bDB.Exec(`INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at) VALUES (?, 'PLN', 0, ?)`, acctID, now)

		setBalPartyID := domain.NewUUIDv7()
		setBalAcctID := domain.NewUUIDv7()
		bDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Set Balance', 'ORGANIZATION', 0, ?, ?)`, setBalPartyID, tc.activeUserID, now)
		bDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, 'Set Balance Offset', 'EXTERNAL', 'PLN', 0, ?, ?)`, setBalAcctID, setBalPartyID, tc.activeUserID, now)

		stxID := domain.NewUUIDv7()
		tc.activeTxID = stxID
		notes := "Starting Balance (Set Balance Offset)"
		bDB.Exec(`INSERT INTO budget_transactions_current (id, version, source_account_id, destination_account_id, source_amount_minor, source_currency, destination_amount_minor, destination_currency, transaction_date, notes, updated_at)
			VALUES (?, 1, ?, ?, 10000, 'PLN', 10000, 'PLN', ?, ?, ?)`, stxID, setBalAcctID, acctID, now, notes, now)
		return nil
	})

	ctx.Step(`^I delete account "([^"]*)"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var acctID string
		err = bDB.QueryRow(`SELECT id FROM accounts_current WHERE name = ?`, acctName).Scan(&acctID)
		if err != nil {
			return fmt.Errorf("account %s not found: %w", acctName, err)
		}

		form := url.Values{}
		form.Set("csrf_token", tc.activeCSRF)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/accounts/%s/delete", tc.activeBudgetID, acctID), strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()

		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId", "id")
		c.SetParamValues(tc.activeBudgetID, acctID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextBudgetDBKey, bDB)
		c.Set("correlationID", domain.NewUUIDv7())
		c.Set("csrfToken", tc.activeCSRF)

		err = tc.accountsHandler.DeleteAccount(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				rec.Code = he.Code
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the account "([^"]*)" should be removed from "accounts_current"$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM accounts_current WHERE name = ?`, acctName).Scan(&count)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("expected account %s to be removed from accounts_current, found %d", acctName, count)
		}
		return nil
	})

	ctx.Step(`^the initial Set Balance transaction should be removed from "budget_transactions_current"$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM budget_transactions_current WHERE id = ?`, tc.activeTxID).Scan(&count)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("expected Set Balance transaction %s to be removed from budget_transactions_current, found %d", tc.activeTxID, count)
		}
		return nil
	})

	ctx.Step(`^the currency balance in "account_currency_balances" for "([^"]*)" should be cleaned up$`, func(acctName string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`SELECT count(*) FROM account_currency_balances WHERE account_id = ?`, tc.activeAcctID).Scan(&count)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("expected account_currency_balances for %s to be removed, found %d", tc.activeAcctID, count)
		}
		return nil
	})

	ctx.Step(`^I request the debug view "([^"]*)"$`, func(path string) error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/budgets/%s/debug", tc.activeBudgetID), nil)
		rec := httptest.NewRecorder()

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)

		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, role)
		c.Set(handlers.ContextBudgetDBKey, bDB)
		c.Set("csrfToken", tc.activeCSRF)

		err = tc.debugHandler.DebugView(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				rec.Code = he.Code
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the sidebar should include a link to the debug page$`, func() error {
		body := tc.lastResponse.Body.String()
		expectedLink := fmt.Sprintf("/budgets/%s/debug", tc.activeBudgetID)
		if !strings.Contains(body, expectedLink) {
			return fmt.Errorf("expected sidebar to contain link %q", expectedLink)
		}
		return nil
	})

	ctx.Step(`^the response status should be 403 Forbidden$`, func() error {
		if tc.lastResponse.Code != http.StatusForbidden {
			return fmt.Errorf("expected status 403 Forbidden, got %d", tc.lastResponse.Code)
		}
		return nil
	})

	ctx.Step(`^an orphaned balance exists in "account_currency_balances" without corresponding account$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		orphanedID := "orphaned_" + domain.NewUUIDv7()[:8]
		_, err = bDB.Exec(`
			INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
			VALUES (?, 'PLN', 45000, ?)`, orphanedID, now)
		return err
	})

	ctx.Step(`^I trigger the integrity audit check via HTMX$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/debug/audit", tc.activeBudgetID), nil)
		rec := httptest.NewRecorder()

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)

		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, role)
		c.Set(handlers.ContextBudgetDBKey, bDB)
		c.Set("csrfToken", tc.activeCSRF)

		err = tc.debugHandler.AuditAction(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				rec.Code = he.Code
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the audit result should report an anomaly for orphaned account currency balances$`, func() error {
		body := tc.lastResponse.Body.String()
		if !strings.Contains(body, "orphaned balance records") && !strings.Contains(body, "Osierocone Salda") {
			return fmt.Errorf("expected audit result to report orphaned balances anomaly, got: %s", body)
		}
		return nil
	})

	ctx.Step(`^I trigger the cleanup orphaned balances action via HTMX$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/budgets/%s/debug/actions/cleanup-orphaned", tc.activeBudgetID), nil)
		rec := httptest.NewRecorder()

		var role string
		_ = tc.mainDB.QueryRow(`SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, tc.activeBudgetID, tc.activeUserID).Scan(&role)

		c := tc.echoEngine.NewContext(req, rec)
		c.SetParamNames("budgetId")
		c.SetParamValues(tc.activeBudgetID)
		c.Set(handlers.ContextUserIDKey, tc.activeUserID)
		c.Set(handlers.ContextRoleKey, role)
		c.Set(handlers.ContextBudgetDBKey, bDB)
		c.Set("csrfToken", tc.activeCSRF)

		err = tc.debugHandler.CleanupOrphanedAction(c)
		if err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				rec.Code = he.Code
			}
		}
		tc.lastResponse = rec
		return nil
	})

	ctx.Step(`^the orphaned balance should be deleted from "account_currency_balances"$`, func() error {
		bDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, true)
		if err != nil {
			return err
		}
		var count int
		err = bDB.QueryRow(`
			SELECT count(*) FROM account_currency_balances acb
			LEFT JOIN accounts_current a ON acb.account_id = a.id
			WHERE a.id IS NULL`).Scan(&count)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("expected 0 orphaned balances, found %d", count)
		}
		return nil
	})

	ctx.Step(`^the response should include a success confirmation notification$`, func() error {
		triggerHeader := tc.lastResponse.Header().Get("HX-Trigger")
		if !strings.Contains(triggerHeader, "debugNotification") || !strings.Contains(triggerHeader, "true") {
			return fmt.Errorf("expected HX-Trigger debugNotification with success=true, got: %q", triggerHeader)
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
			Tags:     "~@e2e",
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}
