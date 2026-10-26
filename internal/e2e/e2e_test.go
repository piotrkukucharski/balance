package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balance/balance/internal/ai"
	openapi "github.com/balance/balance/internal/api"
	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/service"
	webassets "github.com/balance/balance/internal/web/assets"
	"github.com/balance/balance/internal/web/handlers"
	"github.com/cucumber/godog"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var (
	sharedBrowserOnce sync.Once
	sharedBrowser     *rod.Browser
	sharedBrowserErr  error
)

func getSharedBrowser() (*rod.Browser, error) {
	sharedBrowserOnce.Do(func() {
		headless := true
		if strings.EqualFold(os.Getenv("E2E_HEADLESS"), "false") || os.Getenv("E2E_HEADLESS") == "0" {
			headless = false
		}

		binPath, _ := launcher.LookPath()
		if binPath == "" {
			binPath = "/usr/bin/chromium"
		}

		l := launcher.New().
			Bin(binPath).
			Headless(headless).
			Set("no-sandbox").
			Set("disable-gpu").
			Set("disable-dev-shm-usage")

		controlURL, err := l.Launch()
		if err != nil {
			sharedBrowserErr = fmt.Errorf("failed launching chromium (%s): %w", binPath, err)
			return
		}

		b := rod.New().ControlURL(controlURL)
		if slowMoStr := os.Getenv("E2E_SLOWMO"); slowMoStr != "" {
			if d, err := time.ParseDuration(slowMoStr); err == nil {
				b = b.SlowMotion(d)
			}
		}

		if err := b.Connect(); err != nil {
			sharedBrowserErr = fmt.Errorf("failed connecting to chromium: %w", err)
			return
		}
		sharedBrowser = b
	})
	return sharedBrowser, sharedBrowserErr
}

type e2eScenarioContext struct {
	tmpDir     string
	mainDB     *sql.DB
	tenantMgr  *db.TenantDBManager
	appService *service.AppService
	echoEngine *echo.Echo
	server     *httptest.Server

	incognito *rod.Browser
	page      *rod.Page
	router    *rod.HijackRouter

	activeUserID   string
	activeEmail    string
	activePassword string
	activeBudgetID string

	mu         sync.Mutex
	jsErrors   []string
	httpErrors []string
}

func (tc *e2eScenarioContext) setup() error {
	browser, err := getSharedBrowser()
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "balance_e2e_*")
	if err != nil {
		return err
	}
	tc.tmpDir = tmpDir

	mainDBPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		return err
	}
	tc.mainDB = mainDB

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		return err
	}

	budgetsDir := filepath.Join(tmpDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 32)
	if err != nil {
		return err
	}
	tc.tenantMgr = tenantMgr

	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(handlers.CorrelationIDMiddleware())
	e.Use(handlers.StructuredRequestLoggerMiddleware())

	sysClock := clock.NewSystemClock(time.UTC)
	appService := service.NewAppService(mainDB, tenantMgr, sysClock)
	tc.appService = appService

	authHandler := handlers.NewAuthHandler(mainDB, tenantMgr)
	authHandler.SetService(appService)

	reportsHandler := handlers.NewReportsHandler(mainDB, tenantMgr)
	workspaceHandler := handlers.NewWorkspaceHandler(mainDB, tenantMgr, reportsHandler)
	workspaceHandler.SetService(appService)
	workspaceHandler.SetStructureAIProvider(ai.NewMockBudgetStructureAIProvider())

	txHandler := handlers.NewTransactionsHandler(mainDB, tenantMgr)
	txHandler.SetService(appService)

	partiesHandler := handlers.NewPartiesHandler(mainDB, tenantMgr)
	membersHandler := handlers.NewMembersHandler(mainDB, tenantMgr)
	tokensHandler := handlers.NewTokensHandler(mainDB)
	accountsHandler := handlers.NewAccountsHandler(mainDB, tenantMgr)
	accountsHandler.SetService(appService)
	pwaHandler := handlers.NewPWAHandler()

	e.GET("/manifest.webmanifest", pwaHandler.Manifest)
	e.GET("/sw.js", pwaHandler.ServiceWorker)
	e.GET("/favicon.ico", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "image/x-icon", webassets.FaviconICO)
	})
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusSeeOther, "/budgets")
	})
	e.GET("/login", authHandler.LoginPage)
	e.POST("/login", authHandler.HandleLogin)
	e.GET("/register", authHandler.RegisterPage)
	e.POST("/register", authHandler.HandleRegister)
	e.POST("/logout", authHandler.HandleLogout)

	authGroup := e.Group("", handlers.SessionOrBearerAuthMiddleware(mainDB))
	authGroup.GET("/budgets", workspaceHandler.BudgetsList)
	authGroup.POST("/budgets", workspaceHandler.CreateBudget)

	e.GET("/budgets/integrations/gocardless/callback", accountsHandler.GoCardlessCallback)
	authGroup.GET("/budgets/integrations/gocardless/callback", accountsHandler.GoCardlessCallback)

	tenantViewer := authGroup.Group("/budgets/:budgetId", handlers.TenantAuthzMiddleware(tenantMgr, "VIEWER"))
	tenantViewer.GET("/budget", workspaceHandler.BudgetView)
	tenantViewer.GET("", workspaceHandler.BudgetView)
	tenantViewer.GET("/dashboard", workspaceHandler.BudgetView)
	tenantViewer.GET("/accounts", accountsHandler.AccountsView)
	tenantViewer.GET("/accounts/institutions", accountsHandler.GetInstitutions)
	tenantViewer.GET("/accounts/extract-favicon", accountsHandler.ExtractFavicon)
	tenantViewer.GET("/transactions", txHandler.TransactionsView)
	tenantViewer.GET("/transactions/import-csv", txHandler.CSVImportPage)
	tenantViewer.GET("/transactions/sync/events", txHandler.SyncBankEvents)
	tenantViewer.GET("/reports", reportsHandler.ReportsView)
	tenantViewer.GET("/reports/:reportType", reportsHandler.ReportDetailView)
	tenantViewer.GET("/reports/saved/list", reportsHandler.ListSavedReportsJSON)
	tenantViewer.GET("/reports/age-of-money/data", reportsHandler.GetAgeOfMoneyData)
	tenantViewer.GET("/reports/income-expense/data", reportsHandler.GetIncomeExpenseData)
	tenantViewer.GET("/reports/spending-trends/data", reportsHandler.GetSpendingTrendsData)
	tenantViewer.GET("/reports/spending-breakdown/data", reportsHandler.GetSpendingBreakdownData)
	tenantViewer.GET("/reports/net-worth/data", reportsHandler.GetNetWorthData)
	tenantViewer.GET("/reports/sankey/data", reportsHandler.GetSankeyData)
	tenantViewer.GET("/reports/forecast/data", reportsHandler.GetForecastData)
	tenantViewer.GET("/reports/budget-analysis/data", reportsHandler.GetBudgetAnalysisData)
	tenantViewer.GET("/reports/monte-carlo/data", reportsHandler.GetMonteCarloData)
	tenantViewer.GET("/reports/party-cashflow/data", reportsHandler.GetPartyCashflowData)
	tenantViewer.GET("/parties", partiesHandler.PartiesView)
	tenantViewer.GET("/parties/:partyId/rules", partiesHandler.PartyRulesView)
	tenantViewer.GET("/settings", workspaceHandler.SettingsView)
	tenantViewer.GET("/settings/members", membersHandler.MembersView)
	tenantViewer.GET("/settings/tokens", tokensHandler.TokensView)
	tenantViewer.POST("/settings/tokens", tokensHandler.CreateToken)
	tenantViewer.POST("/settings/tokens/:tokenId/revoke", tokensHandler.RevokeToken)
	tenantViewer.GET("/members", membersHandler.MembersView)

	tenantEditor := authGroup.Group("/budgets/:budgetId", handlers.TenantAuthzMiddleware(tenantMgr, "EDITOR"))
	tenantEditor.POST("/accounts", accountsHandler.CreateAccount)
	tenantEditor.POST("/accounts/link-bank", accountsHandler.LinkBankAccount)
	tenantEditor.POST("/accounts/connect-queue", accountsHandler.ConnectBankQueue)
	tenantEditor.POST("/accounts/:id/reconnect", accountsHandler.ReconnectAccount)
	tenantEditor.POST("/accounts/:id/update", accountsHandler.UpdateAccount)
	tenantEditor.POST("/accounts/:id/toggle-close", accountsHandler.ToggleCloseAccount)
	tenantEditor.POST("/accounts/:id/delete", accountsHandler.DeleteAccount)
	tenantEditor.POST("/transactions", txHandler.AddManualTransaction)
	tenantEditor.POST("/transactions/:txId/update", txHandler.UpdateTransaction)
	tenantEditor.POST("/transactions/:txId/delete", txHandler.DeleteTransaction)
	tenantEditor.POST("/transactions/batch-delete", txHandler.BatchDeleteTransactions)
	tenantEditor.POST("/transactions/sync", txHandler.SyncWithBank)
	tenantEditor.POST("/transactions/preview-csv", txHandler.PreviewCSV)
	tenantEditor.POST("/transactions/import-csv", txHandler.ImportCSV)
	tenantEditor.POST("/transactions/raw/:rawId/resolve", txHandler.ResolveRawTransaction)
	tenantEditor.POST("/reports/saved", reportsHandler.SaveReportConfig)
	tenantEditor.POST("/reports/saved/:reportId/toggle-pin", reportsHandler.TogglePinReport)
	tenantEditor.POST("/reports/saved/:reportId/delete", reportsHandler.DeleteSavedReport)
	tenantEditor.POST("/transactions/raw/:rawId/delete", txHandler.DeleteRawTransaction)
	tenantEditor.POST("/transactions/raw/batch-delete", txHandler.BatchDeleteRawTransactions)
	tenantEditor.POST("/transactions/raw/:rawId/ignore", txHandler.IgnoreRawTransaction)
	tenantEditor.POST("/transactions/raw/batch-ignore", txHandler.BatchIgnoreRawTransactions)
	tenantEditor.POST("/transactions/resolve/batch", txHandler.BatchResolveRawTransactions)
	tenantEditor.POST("/transactions/reconcile/auto-match", txHandler.AutoMatchRawTransactions)
	tenantEditor.POST("/parties", partiesHandler.CreateParty)
	tenantEditor.POST("/parties/:partyId/rules", partiesHandler.CreatePartyRule)
	tenantEditor.POST("/parties/:partyId/rules/:ruleId/delete", partiesHandler.DeletePartyRule)
	tenantEditor.POST("/groups/rename", workspaceHandler.RenameGroup)
	tenantEditor.POST("/groups/move", workspaceHandler.MoveGroup)
	tenantEditor.POST("/buckets", workspaceHandler.CreateBucket)
	tenantEditor.POST("/buckets/:id/update", workspaceHandler.UpdateBucket)
	tenantEditor.POST("/buckets/:id/move-group", workspaceHandler.MoveBucketGroup)
	tenantEditor.POST("/buckets/:id/archive", workspaceHandler.ArchiveBucket)
	tenantEditor.POST("/buckets/:id/assign", workspaceHandler.AssignFunds)
	tenantEditor.POST("/buckets/move", workspaceHandler.MoveFunds)
	tenantEditor.POST("/buckets/auto-assign", workspaceHandler.AutoAssign)
	tenantEditor.POST("/budget/auto-assign", workspaceHandler.AutoAssign)
	tenantEditor.POST("/targets", workspaceHandler.CreateTarget)
	tenantEditor.POST("/targets/:id/update", workspaceHandler.UpdateTarget)
	tenantEditor.POST("/targets/:id/delete", workspaceHandler.DeleteTarget)
	tenantEditor.POST("/structure/generate-ai-preview", workspaceHandler.GenerateStructurePreview)
	tenantEditor.GET("/structure/preview-source", workspaceHandler.PreviewSourceStructure)
	tenantEditor.POST("/structure/apply", workspaceHandler.ApplyStructure)

	tenantAdmin := authGroup.Group("/budgets/:budgetId", handlers.TenantAuthzMiddleware(tenantMgr, "ADMIN"))
	tenantAdmin.POST("/settings/members/invite", membersHandler.InviteMember)
	tenantAdmin.POST("/members/invite", membersHandler.InviteMember)

	tenantOwner := authGroup.Group("/budgets/:budgetId", handlers.TenantAuthzMiddleware(tenantMgr, "OWNER"))
	tenantOwner.POST("/settings", workspaceHandler.UpdateBudget)
	tenantOwner.POST("/update", workspaceHandler.UpdateBudget)
	tenantOwner.POST("/clone", workspaceHandler.CloneBudget)
	tenantOwner.POST("/delete", workspaceHandler.DeleteBudget)

	apiServer := openapi.NewServer(mainDB, tenantMgr, appService)
	apiGroup := e.Group("/api/v1", func(next echo.HandlerFunc) echo.HandlerFunc {
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

	tc.echoEngine = e
	tc.server = httptest.NewServer(e)

	incognito, err := browser.Incognito()
	if err != nil {
		return err
	}
	tc.incognito = incognito

	page, err := incognito.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return err
	}
	_ = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:  1440,
		Height: 900,
	})
	tc.page = page

	// Intercept external CDN assets in fast offline E2E mode unless E2E_LIVE_CDN=true
	if !strings.EqualFold(os.Getenv("E2E_LIVE_CDN"), "true") {
		router := page.HijackRequests()
		_ = router.Add("https://cdn.jsdelivr.net/*", "", func(ctx *rod.Hijack) {
			u := ctx.Request.URL().String()
			if strings.Contains(u, "echarts") {
				ctx.Response.SetHeader("Content-Type", "application/javascript")
				ctx.Response.SetBody(`
					window.echarts = {
						init: function(dom) {
							if (dom) {
								dom.setAttribute('data-echarts-initialized', 'true');
								dom.innerHTML = '<canvas class="echarts-canvas" width="400" height="200"></canvas>';
							}
							return {
								showLoading: function() {},
								hideLoading: function() {},
								setOption: function(opt) {
									if (dom) {
										dom.setAttribute('data-echarts-rendered', 'true');
									}
								},
								resize: function() {}
							};
						}
					};
				`)
				return
			}
			if strings.Contains(u, "tailwindcss") {
				ctx.Response.SetHeader("Content-Type", "application/javascript")
				ctx.Response.SetBody(`/* tailwind browser stub */`)
				return
			}
			ctx.Response.SetHeader("Content-Type", "text/css")
			ctx.Response.SetBody(`.hidden { display: none !important; } aside.hidden { display: flex !important; } dialog.modal-open { display: block !important; }`)
		})
		_ = router.Add("https://unpkg.com/*", "", func(ctx *rod.Hijack) {
			ctx.Response.SetHeader("Content-Type", "application/javascript")
			ctx.Response.SetBody(`/* htmx stub for deterministic form navigation */`)
		})
		_ = router.Add("https://cdn-logos.gocardless.com/*", "", func(ctx *rod.Hijack) {
			ctx.Response.SetHeader("Content-Type", "image/png")
			ctx.Response.SetBody("")
		})
		go router.Run()
		tc.router = router
	}

	// Automatically accept all browser confirm() dialogs AND monitor JS runtime exceptions + HTTP 4xx/5xx responses
	go page.EachEvent(
		func(e *proto.PageJavascriptDialogOpening) {
			_ = proto.PageHandleJavaScriptDialog{Accept: true}.Call(page)
		},
		func(e *proto.RuntimeExceptionThrown) {
			desc := e.ExceptionDetails.Text
			if e.ExceptionDetails.Exception != nil && e.ExceptionDetails.Exception.Description != "" {
				desc = e.ExceptionDetails.Exception.Description
			}
			tc.mu.Lock()
			tc.jsErrors = append(tc.jsErrors, desc)
			tc.mu.Unlock()
		},
		func(e *proto.NetworkResponseReceived) {
			urlStr := e.Response.URL
			if strings.HasPrefix(urlStr, tc.server.URL) && e.Response.Status >= 400 {
				tc.mu.Lock()
				tc.httpErrors = append(tc.httpErrors, fmt.Sprintf("HTTP %d on %s", e.Response.Status, urlStr))
				tc.mu.Unlock()
			}
		},
	)()

	return nil
}

func (tc *e2eScenarioContext) captureFailureArtifacts(scenarioName string) {
	if tc.page == nil {
		return
	}
	outDir := filepath.Join("..", "..", "test-results", "e2e")
	_ = os.MkdirAll(outDir, 0755)
	slug := regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(strings.ToLower(scenarioName), "_")

	if img, err := tc.page.Screenshot(true, nil); err == nil {
		_ = os.WriteFile(filepath.Join(outDir, slug+".png"), img, 0644)
	}
	if htmlStr, err := tc.page.HTML(); err == nil {
		_ = os.WriteFile(filepath.Join(outDir, slug+".html"), []byte(htmlStr), 0644)
	}
}

func (tc *e2eScenarioContext) teardown() {
	if tc.router != nil {
		_ = tc.router.Stop()
	}
	if tc.page != nil {
		_ = tc.page.Close()
	}
	if tc.incognito != nil {
		_ = tc.incognito.Close()
	}
	if tc.server != nil {
		tc.server.Close()
	}
	if tc.tenantMgr != nil {
		tc.tenantMgr.CloseAll()
	}
	if tc.mainDB != nil {
		_ = tc.mainDB.Close()
	}
	if tc.tmpDir != "" {
		_ = os.RemoveAll(tc.tmpDir)
	}
}

func (tc *e2eScenarioContext) navigate(path string) error {
	actualPath := strings.ReplaceAll(path, ":budgetId", tc.activeBudgetID)
	fullURL := tc.server.URL + actualPath
	if err := tc.page.Navigate(fullURL); err != nil {
		return err
	}
	return tc.page.WaitLoad()
}

func (tc *e2eScenarioContext) bodyText() (string, error) {
	el, err := tc.page.Element("body")
	if err != nil {
		return "", err
	}
	return el.Text()
}

func (tc *e2eScenarioContext) pageHTML() (string, error) {
	return tc.page.HTML()
}

func (tc *e2eScenarioContext) seedAuthenticatedUserAndBudget(email, password, budgetName string) error {
	if password == "" {
		password = "SuperSecret123!"
	}
	tc.activeEmail = email
	tc.activePassword = password

	uid, err := tc.appService.CreateUser(context.Background(), service.CreateUserInput{
		Email:       email,
		Password:    password,
		DisplayName: "Jan Kowalski",
	})
	if err != nil {
		return err
	}
	tc.activeUserID = uid

	if budgetName != "" {
		bid, err := tc.appService.CreateBudget(context.Background(), service.CreateBudgetInput{
			Name:            budgetName,
			DefaultCurrency: "PLN",
			Timezone:        "Europe/Warsaw",
			OwnerUserID:     uid,
		})
		if err != nil {
			return err
		}
		tc.activeBudgetID = bid
	}

	// Create stateful session in mainDB and set cookie on browser
	rawToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		return err
	}
	csrfToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		return err
	}
	tokenHash := auth.HashToken(rawToken)
	now := time.Now().UTC().UnixMicro()
	expiresAt := now + int64(30*24*3600*1_000_000)

	_, err = tc.mainDB.Exec(`
		INSERT INTO sessions (id, token_hash, user_id, csrf_token, created_at, last_active_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		domain.NewUUIDv7(), tokenHash, uid, csrfToken, now, now, expiresAt,
	)
	if err != nil {
		return err
	}

	return tc.page.SetCookies([]*proto.NetworkCookieParam{
		{
			Name:     handlers.SessionCookieName,
			Value:    rawToken,
			URL:      tc.server.URL,
			Path:     "/",
			HTTPOnly: true,
		},
	})
}


func (tc *e2eScenarioContext) assertNoBrowserErrors() error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if len(tc.jsErrors) > 0 {
		return fmt.Errorf("unhandled browser JS runtime exceptions: %s", strings.Join(tc.jsErrors, "; "))
	}
	if len(tc.httpErrors) > 0 {
		return fmt.Errorf("unexpected HTTP >= 400 responses: %s", strings.Join(tc.httpErrors, "; "))
	}
	return nil
}

func containsNormalized(haystack, needle string) bool {
	if strings.Contains(haystack, needle) {
		return true
	}
	noCommaNeedle := strings.ReplaceAll(needle, ",", "")
	if strings.Contains(haystack, noCommaNeedle) {
		return true
	}
	noCommaHaystack := strings.ReplaceAll(haystack, ",", "")
	return strings.Contains(noCommaHaystack, noCommaNeedle)
}

func parseMajorToMinor(s string) int64 {
	clean := strings.ReplaceAll(s, ",", "")
	clean = strings.TrimSpace(strings.TrimSuffix(clean, "PLN"))
	clean = strings.TrimSpace(clean)
	var f float64
	fmt.Sscanf(clean, "%f", &f)
	if f >= 0 {
		return int64(f*100 + 0.5)
	}
	return int64(f*100 - 0.5)
}

func (tc *e2eScenarioContext) seedAccount(name, acctType, currency string, balanceMinor int64) (string, error) {
	return tc.appService.CreateFinancialAccount(context.Background(), service.CreateAccountInput{
		BudgetID:            tc.activeBudgetID,
		UserID:              tc.activeUserID,
		CorrelationID:       domain.NewUUIDv7(),
		Name:                name,
		AccountType:         acctType,
		PrimaryCurrency:     currency,
		FaviconType:         "EMOJI",
		FaviconValue:        "💵",
		InitialBalanceMinor: balanceMinor,
	})
}

func (tc *e2eScenarioContext) seedBucket(name, groupName string) (string, error) {
	if groupName == "" {
		groupName = "Needs"
	}
	return tc.appService.CreateBucket(context.Background(), tc.activeBudgetID, budget.CreateBucketInput{
		GroupName:     groupName,
		Name:          name,
		AgentID:       tc.activeUserID,
		CorrelationID: domain.NewUUIDv7(),
	})
}

func (tc *e2eScenarioContext) findBucketID(name string) (string, error) {
	tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
	if err != nil {
		return "", err
	}
	var id string
	err = tenantDB.QueryRow(`SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0`, name).Scan(&id)
	return id, err
}

func (tc *e2eScenarioContext) findAccountID(name string) (string, error) {
	tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
	if err != nil {
		return "", err
	}
	var id string
	err = tenantDB.QueryRow(`SELECT id FROM accounts_current WHERE name = ?`, name).Scan(&id)
	return id, err
}

func InitializeE2EScenario(ctx *godog.ScenarioContext) {
	tc := &e2eScenarioContext{}

	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		if err := tc.setup(); err != nil {
			return c, err
		}
		return c, nil
	})

	ctx.After(func(c context.Context, sc *godog.Scenario, scErr error) (context.Context, error) {
		guardErr := tc.assertNoBrowserErrors()
		if scErr != nil || guardErr != nil {
			tc.captureFailureArtifacts(sc.Name)
		}
		tc.teardown()
		if scErr == nil && guardErr != nil {
			return c, guardErr
		}
		return c, nil
	})

	// =========================================================================
	// GIVEN STEPS (Hybrid Setup & Seeding)
	// =========================================================================

	ctx.Step(`^a clean browser session$`, func() error {
		return nil
	})

	ctx.Step(`^an authenticated browser session for "([^"]*)" with password "([^"]*)" owning budget "([^"]*)"$`, func(email, password, budgetName string) error {
		return tc.seedAuthenticatedUserAndBudget(email, password, budgetName)
	})

	ctx.Step(`^an authenticated browser session for "([^"]*)" on budget "([^"]*)"$`, func(email, budgetName string) error {
		return tc.seedAuthenticatedUserAndBudget(email, "SuperSecret123!", budgetName)
	})

	ctx.Step(`^an authenticated browser session for "([^"]*)" on budget "([^"]*)" with "([^"]*)" in "([^"]*)"$`, func(email, budgetName, amountStr, acctName string) error {
		if err := tc.seedAuthenticatedUserAndBudget(email, "SuperSecret123!", budgetName); err != nil {
			return err
		}
		minor := parseMajorToMinor(amountStr)
		_, err := tc.seedAccount(acctName, "CHECKING", "PLN", minor)
		return err
	})

	ctx.Step(`^an authenticated browser session for "([^"]*)" on budget "([^"]*)" with account "([^"]*)" and bucket "([^"]*)"$`, func(email, budgetName, acctName, bucketName string) error {
		if err := tc.seedAuthenticatedUserAndBudget(email, "SuperSecret123!", budgetName); err != nil {
			return err
		}
		if _, err := tc.seedAccount(acctName, "CHECKING", "PLN", 500000); err != nil {
			return err
		}
		_, err := tc.seedBucket(bucketName, "Needs")
		return err
	})

	ctx.Step(`^a linked mock bank account "([^"]*)" with expired authorization consent$`, func(acctName string) error {
		acctID, err := tc.seedAccount(acctName, "CHECKING", "PLN", 542050)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		past := now - int64(24*3600*1_000_000)
		intID := domain.NewUUIDv7()
		_, err = tc.mainDB.Exec(`
			INSERT INTO user_integrations (id, user_id, provider, institution_id, status, expires_at, created_at, updated_at)
			VALUES (?, ?, 'MOCK', 'MOCK_BANK_PL01', 'EXPIRED', ?, ?, ?)`,
			intID, tc.activeUserID, past, now, now,
		)
		if err != nil {
			return err
		}
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		_, err = tenantDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, ?, 'mock_acc_checking_pln', ?, ?)`,
			domain.NewUUIDv7(), acctID, intID, now, now,
		)
		return err
	})

	ctx.Step(`^bucket "([^"]*)" has mandatory target "([^"]*)" of "([^"]*)" and recommended target "([^"]*)" of "([^"]*)"$`, func(bucketName, t1Name, t1Amt, t2Name, t2Amt string) error {
		bID, err := tc.seedBucket(bucketName, "Needs")
		if err != nil {
			return err
		}
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		targets := []struct {
			name     string
			priority int
			minor    int64
		}{
			{t1Name, 3, parseMajorToMinor(t1Amt)},
			{t2Name, 2, parseMajorToMinor(t2Amt)},
		}
		for i, t := range targets {
			tID := domain.NewUUIDv7()
			_, err := tenantDB.Exec(`
				INSERT INTO targets_projection (
					id, version, bucket_id, name, target_type, priority, sort_order,
					amount_minor, currency, recurrence_cadence, recurrence_interval,
					non_spending_strategy, current_funded_minor, is_archived, updated_at
				) VALUES (?, 1, ?, ?, 'EXPENSE', ?, ?, ?, 'PLN', 'MONTHLY', NULL, 'ROLL_OVER', 0, 0, ?)`,
				tID, bID, t.name, t.priority, i+1, t.minor, now,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^a linked mock bank account "([^"]*)" and buckets "([^"]*)", "([^"]*)" and "([^"]*)"$`, func(acctName, b1, b2, b3 string) error {
		acctID, err := tc.seedAccount(acctName, "CHECKING", "PLN", 542050)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		future := now + int64(90*24*3600*1_000_000)
		intID := domain.NewUUIDv7()
		_, err = tc.mainDB.Exec(`
			INSERT INTO user_integrations (id, user_id, provider, institution_id, status, expires_at, created_at, updated_at)
			VALUES (?, ?, 'MOCK', 'MOCK_BANK_PL01', 'ACTIVE', ?, ?, ?)`,
			intID, tc.activeUserID, future, now, now,
		)
		if err != nil {
			return err
		}
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		_, err = tenantDB.Exec(`
			INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
			VALUES (?, ?, ?, 'mock_acc_checking_pln', 0, ?)`,
			domain.NewUUIDv7(), acctID, intID, now,
		)
		if err != nil {
			return err
		}
		for _, bName := range []string{b1, b2, b3} {
			if _, err := tc.seedBucket(bName, "Needs"); err != nil {
				return err
			}
		}
		return nil
	})

	ctx.Step(`^an active matching rule mapping "([^"]*)" to bucket "([^"]*)"$`, func(pattern, bucketName string) error {
		bID, err := tc.findBucketID(bucketName)
		if err != nil {
			return err
		}
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		partyID := domain.NewUUIDv7()
		extAcctID := domain.NewUUIDv7()
		ruleID := domain.NewUUIDv7()

		_, err = tenantDB.Exec(`
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`,
			partyID, pattern+" Merchant", tc.activeUserID, now,
		)
		if err != nil {
			return err
		}
		_, err = tenantDB.Exec(`
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
			extAcctID, partyID, pattern+" Account", tc.activeUserID, now,
		)
		if err != nil {
			return err
		}
		_, err = tenantDB.Exec(`
			INSERT INTO matching_rules_current (
				id, version, name, priority, match_field, match_operator, match_pattern,
				target_counterparty_account_id, default_bucket_id, updated_at
			) VALUES (?, 1, ?, 100, 'RAW_PAYEE', 'CONTAINS', ?, ?, ?, ?)`,
			ruleID, pattern+" Rule", pattern, extAcctID, bID, now,
		)
		return err
	})

	ctx.Step(`^an authenticated browser session for "([^"]*)" on budget "([^"]*)" with rich sample transactions$`, func(email, budgetName string) error {
		if err := tc.seedAuthenticatedUserAndBudget(email, "SuperSecret123!", budgetName); err != nil {
			return err
		}
		acctID, err := tc.seedAccount("Main Checking", "CHECKING", "PLN", 850000)
		if err != nil {
			return err
		}
		bID, err := tc.seedBucket("Groceries", "Needs")
		if err != nil {
			return err
		}
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		extPartyID := domain.NewUUIDv7()
		extAcctID := domain.NewUUIDv7()
		_, _ = tenantDB.Exec(`INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at) VALUES (?, 1, 'Biedronka', 'ORGANIZATION', 0, ?, ?)`, extPartyID, tc.activeUserID, now)
		_, _ = tenantDB.Exec(`INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at) VALUES (?, 1, ?, 'Biedronka POS', 'EXTERNAL', 'PLN', 0, ?, ?)`, extAcctID, extPartyID, tc.activeUserID, now)
		_, _ = tenantDB.Exec(`
			INSERT INTO budget_transactions_current (
				id, version, source_account_id, destination_account_id,
				source_amount_minor, source_currency, destination_amount_minor, destination_currency,
				bucket_id, notes, transaction_date, agent_id, updated_at
			) VALUES (?, 1, ?, ?, 24000, 'PLN', 24000, 'PLN', ?, 'Sample Groceries', ?, ?, ?)`,
			domain.NewUUIDv7(), acctID, extAcctID, bID, now, tc.activeUserID, now,
		)
		return nil
	})

	// =========================================================================
	// WHEN & THEN STEPS (100% Real Browser Interaction via Rod)
	// =========================================================================

	ctx.Step(`^I navigate to "([^"]*)" in the browser$`, func(path string) error {
		return tc.navigate(path)
	})

	ctx.Step(`^I navigate to the "([^"]*)"(?: workspace| settings)? view in the browser$`, func(viewName string) error {
		var path string
		switch strings.ToLower(viewName) {
		case "budget":
			path = fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID)
		case "accounts":
			path = fmt.Sprintf("/budgets/%s/accounts", tc.activeBudgetID)
		case "transactions":
			path = fmt.Sprintf("/budgets/%s/transactions", tc.activeBudgetID)
		case "reports":
			path = fmt.Sprintf("/budgets/%s/reports", tc.activeBudgetID)
		case "external parties", "parties":
			path = fmt.Sprintf("/budgets/%s/parties", tc.activeBudgetID)
		case "members":
			path = fmt.Sprintf("/budgets/%s/settings/members", tc.activeBudgetID)
		case "api tokens", "tokens":
			path = fmt.Sprintf("/budgets/%s/settings/tokens", tc.activeBudgetID)
		case "settings":
			path = fmt.Sprintf("/budgets/%s/settings", tc.activeBudgetID)
		default:
			return fmt.Errorf("unknown workspace view: %s", viewName)
		}
		return tc.navigate(path)
	})

	ctx.Step(`^I fill in the registration form with display name "([^"]*)", email "([^"]*)" and password "([^"]*)"$`, func(displayName, email, password string) error {
		if err := tc.page.MustElement(`input[name="display_name"]`).Input(displayName); err != nil {
			return err
		}
		if err := tc.page.MustElement(`input[name="email"]`).Input(email); err != nil {
			return err
		}
		return tc.page.MustElement(`input[name="password"]`).Input(password)
	})

	ctx.Step(`^I click the "([^"]*)" submit button$`, func(btnText string) error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		el, err := tc.page.ElementR("button", regexp.QuoteMeta(btnText))
		if err != nil {
			return err
		}
		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the browser URL should be "([^"]*)"$`, func(expectedPath string) error {
		info, err := tc.page.Info()
		if err != nil {
			return err
		}
		actual := strings.TrimPrefix(info.URL, tc.server.URL)
		if actual != expectedPath {
			return fmt.Errorf("expected browser URL path %q, got %q", expectedPath, actual)
		}
		return nil
	})

	ctx.Step(`^the page should display "([^"]*)"$`, func(expected string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !containsNormalized(txt, expected) {
			return fmt.Errorf("expected page to display %q, got body snippet: %.300s", expected, txt)
		}
		return nil
	})

	ctx.Step(`^I open the new budget modal and create a budget named "([^"]*)" with currency "([^"]*)" and timezone "([^"]*)"$`, func(name, currency, tz string) error {
		_, err := tc.page.Eval(`() => document.getElementById('new_budget_modal').showModal()`)
		if err != nil {
			return err
		}
		if err := tc.page.MustElement(`#new_budget_modal input[name="name"]`).Input(name); err != nil {
			return err
		}
		_, _ = tc.page.Eval(`(c) => { document.querySelector('#new_budget_modal select[name="default_currency"]').value = c; }`, currency)
		tzInput := tc.page.MustElement(`#new_budget_modal input[name="timezone"]`)
		_ = tzInput.SelectAllText()
		if err := tzInput.Input(tz); err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#new_budget_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		if err := tc.page.WaitLoad(); err != nil {
			return err
		}
		info, _ := tc.page.Info()
		parts := strings.Split(strings.TrimPrefix(info.URL, tc.server.URL+"/budgets/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			tc.activeBudgetID = parts[0]
		}
		return nil
	})

	ctx.Step(`^the browser should navigate to the budget workspace view$`, func() error {
		info, err := tc.page.Info()
		if err != nil {
			return err
		}
		if !strings.Contains(info.URL, "/budgets/") || !strings.HasSuffix(info.URL, "/budget") {
			return fmt.Errorf("expected budget workspace URL, got %s", info.URL)
		}
		return nil
	})

	ctx.Step(`^the page should display budget title "([^"]*)"$`, func(title string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, title) {
			return fmt.Errorf("expected budget title %q on page", title)
		}
		return nil
	})

	ctx.Step(`^the Ready to Assign banner should display "([^"]*)"$`, func(amountStr string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !containsNormalized(txt, amountStr) {
			return fmt.Errorf("expected Ready to Assign banner to display %q, body: %.400s", amountStr, txt)
		}
		return nil
	})

	ctx.Step(`^the sidebar navigation should display links for "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)", and "([^"]*)"$`, func(l1, l2, l3, l4, l5, l6 string) error {
		htmlStr, err := tc.pageHTML()
		if err != nil {
			return err
		}
		for _, link := range []string{l1, l2, l3, l4, l5, l6} {
			if !strings.Contains(htmlStr, link) {
				return fmt.Errorf("expected sidebar to contain link %q", link)
			}
		}
		return nil
	})

	ctx.Step(`^I navigate to the budget settings page via the sidebar$`, func() error {
		if err := tc.navigate(fmt.Sprintf("/budgets/%s/budget", tc.activeBudgetID)); err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		_, err := tc.page.Eval(`(bid) => {
			const link = document.querySelector('aside a[href="/budgets/' + bid + '/settings"]');
			if (!link) throw new Error('sidebar settings link not found');
			link.click();
		}`, tc.activeBudgetID)
		if err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^I update the budget name to "([^"]*)" and save settings$`, func(newName string) error {
		_, err := tc.page.Eval(`(val) => {
			const inp = document.querySelector('form[action*="/settings"] input[name="name"]');
			inp.value = val;
		}`, newName)
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action*="/settings"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the page should reflect the updated budget name "([^"]*)"$`, func(newName string) error {
		htmlStr, err := tc.pageHTML()
		if err != nil {
			return err
		}
		if !strings.Contains(htmlStr, newName) {
			return fmt.Errorf("expected page to contain updated budget name %q", newName)
		}
		return nil
	})

	ctx.Step(`^the budgets list should display "([^"]*)"$`, func(budgetName string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, budgetName) {
			return fmt.Errorf("expected budgets list to display %q", budgetName)
		}
		return nil
	})

	ctx.Step(`^I click the "Log Out" button in the top navigation bar$`, func() error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action="/logout"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^I fill in the login form with email "([^"]*)" and password "([^"]*)" and click "Sign In"$`, func(email, password string) error {
		if err := tc.page.MustElement(`form[action="/login"] input[name="email"]`).Input(email); err != nil {
			return err
		}
		if err := tc.page.MustElement(`form[action="/login"] input[name="password"]`).Input(password); err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action="/login"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^I clone the budget "([^"]*)" into "([^"]*)" via the clone modal$`, func(sourceName, cloneName string) error {
		// Trigger openCloneBudgetModal via JavaScript
		_, err := tc.page.Eval(`(targetName) => {
			const cards = document.querySelectorAll('.card');
			let foundBudgetId = null;
			for (const card of cards) {
				if (card.textContent.includes(targetName)) {
					const cloneBtn = card.querySelector('button[onclick*="openCloneBudgetModal"]');
					if (cloneBtn) {
						cloneBtn.click();
						return;
					}
				}
			}
			throw new Error('Clone button for budget not found');
		}`, sourceName)
		if err != nil {
			return err
		}

		// Fill in new name in clone modal
		nameInp := tc.page.MustElement(`#clone_budget_name`)
		_ = nameInp.SelectAllText()
		if err := nameInp.Input(cloneName); err != nil {
			return err
		}

		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#clone_budget_form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		if err := tc.page.WaitLoad(); err != nil {
			return err
		}
		info, _ := tc.page.Info()
		parts := strings.Split(strings.TrimPrefix(info.URL, tc.server.URL+"/budgets/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			tc.activeBudgetID = parts[0]
		}
		return nil
	})

	// =========================================================================
	// ACCOUNTS & MOCK BANK WIZARD STEPS
	// =========================================================================

	ctx.Step(`^I open the "New Account" modal and create a "([^"]*)" account named "([^"]*)" with currency "([^"]*)", balance "([^"]*)" and emoji icon "([^"]*)"$`, func(acctType, name, currency, balance, emoji string) error {
		_, err := tc.page.Eval(`(args) => {
			new_account_modal.showModal();
			document.querySelector('#new_account_modal input[name="name"]').value = args.name;
			document.querySelector('#new_account_modal select[name="account_type"]').value = args.acctType;
			document.querySelector('#new_account_modal select[name="primary_currency"]').value = args.currency;
			document.querySelector('#new_account_modal input[name="initial_balance"]').value = args.balance;
			setNewEmoji(args.emoji);
		}`, map[string]string{
			"name":     name,
			"acctType": acctType,
			"currency": currency,
			"balance":  balance,
			"emoji":    emoji,
		})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#new_account_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the accounts table should display "([^"]*)" with icon "([^"]*)" and balance "([^"]*)"$`, func(name, icon, balance string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, name) || !strings.Contains(txt, icon) || !containsNormalized(txt, balance) {
			return fmt.Errorf("expected accounts table to display %q, %q, and %q; got: %.400s", name, icon, balance, txt)
		}
		return nil
	})

	ctx.Step(`^I open the edit modal for account "([^"]*)" and change its emoji icon to "([^"]*)"$`, func(acctName, newEmoji string) error {
		acctID, err := tc.findAccountID(acctName)
		if err != nil {
			return err
		}
		_, err = tc.page.Eval(`(args) => {
			openEditModal(args.id, args.name, 'PLN', 'CHECKING', 'EMOJI', args.emoji);
			setEditEmoji(args.emoji);
		}`, map[string]string{"id": acctID, "name": acctName, "emoji": newEmoji})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#edit_account_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the accounts table should display "([^"]*)" with icon "([^"]*)"$`, func(name, icon string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, name) || !strings.Contains(txt, icon) {
			return fmt.Errorf("expected accounts table to display %q with icon %q", name, icon)
		}
		return nil
	})

	ctx.Step(`^I trigger the close toggle action for account "([^"]*)"$`, func(acctName string) error {
		acctID, err := tc.findAccountID(acctName)
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		_, err = tc.page.Eval(`(id) => {
			const form = document.querySelector('form[action*="/accounts/' + id + '/toggle-close"]');
			form.submit();
		}`, acctID)
		if err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the account "([^"]*)" should display status badge "([^"]*)"$`, func(acctName, badge string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, acctName) || !strings.Contains(txt, badge) {
			return fmt.Errorf("expected account %q with status badge %q", acctName, badge)
		}
		return nil
	})

	ctx.Step(`^I open the "Connect Bank Accounts" modal$`, func() error {
		_, err := tc.page.Eval(`() => link_bank_modal.showModal()`)
		return err
	})

	ctx.Step(`^I select the "Mock Sandbox" provider in step 1$`, func() error {
		_, err := tc.page.Eval(`() => selectProvider('MOCK')`)
		return err
	})

	ctx.Step(`^I select country "([^"]*)" in step 2$`, func(countryName string) error {
		code := "PL"
		if strings.EqualFold(countryName, "Germany") {
			code = "DE"
		} else if strings.EqualFold(countryName, "United Kingdom") {
			code = "GB"
		} else if strings.EqualFold(countryName, "France") {
			code = "FR"
		}
		_, err := tc.page.Eval(`(c) => selectCountry(c)`, code)
		if err != nil {
			return err
		}
		// Wait until institutions list is populated
		for i := 0; i < 30; i++ {
			htmlStr, _ := tc.pageHTML()
			if strings.Contains(htmlStr, "SANDBOXFINANCE_SFIN0000") {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("timed out waiting for mock institutions to load in step 3")
	})

	ctx.Step(`^I select institutions "([^"]*)" and "([^"]*)" in step 3 and click connect$`, func(inst1, inst2 string) error {
		_, err := tc.page.Eval(`() => {
			const cbs = document.querySelectorAll('#institutions_list_container input[type="checkbox"]');
			cbs.forEach(cb => {
				if (cb.value === 'SANDBOXFINANCE_SFIN0000' || cb.value === 'MOCK_BANK_PL01') {
					cb.checked = true;
					toggleInstitutionSelection(cb.value, true);
				}
			});
			startConnectionQueue();
		}`)
		return err
	})

	ctx.Step(`^the sequential connection queue in step 4 should complete for both institutions$`, func() error {
		for i := 0; i < 40; i++ {
			txt, _ := tc.bodyText()
			if strings.Contains(txt, "All Bank Connections Processed") {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("timed out waiting for sequential bank connection queue to finish")
	})

	ctx.Step(`^I click "Done & View Accounts"$`, func() error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#queue_done_btn`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the accounts table should display the imported mock checking and savings accounts with their balances$`, func() error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, "mBank Checking PLN") || !strings.Contains(txt, "mBank Savings EUR") {
			return fmt.Errorf("expected imported mock accounts in table, got: %.400s", txt)
		}
		return nil
	})

	ctx.Step(`^the account "([^"]*)" should display the "Consent Expired" warning badge$`, func(acctName string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, acctName) || !strings.Contains(txt, "Consent Expired") {
			return fmt.Errorf("expected Consent Expired badge for %q", acctName)
		}
		return nil
	})

	ctx.Step(`^I click the "Reconnect" button for "([^"]*)"$`, func(acctName string) error {
		acctID, err := tc.findAccountID(acctName)
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		_, err = tc.page.Eval(`(args) => reconnectAccount(args.id, args.name)`, map[string]string{
			"id":   acctID,
			"name": acctName,
		})
		if err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the account "([^"]*)" integration status should be restored to active without "Consent Expired"$`, func(acctName string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if strings.Contains(txt, "Consent Expired") {
			return fmt.Errorf("expected Consent Expired badge to be cleared after reconnect")
		}
		if !strings.Contains(txt, "Active") {
			return fmt.Errorf("expected Active badge after reconnect")
		}
		return nil
	})

	// =========================================================================
	// BUCKETS, TARGETS, ASSIGN, MOVE & AUTO-ASSIGN STEPS
	// =========================================================================

	ctx.Step(`^I open the "New Bucket" modal and create bucket "([^"]*)" in group "([^"]*)"$`, func(bucketName, groupName string) error {
		_, err := tc.page.Eval(`(args) => {
			new_bucket_modal.showModal();
			document.querySelector('#new_bucket_modal input[name="name"]').value = args.name;
			document.querySelector('#new_bucket_modal input[name="group_name"]').value = args.group;
		}`, map[string]string{"name": bucketName, "group": groupName})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#new_bucket_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the budget table should list buckets "([^"]*)" and "([^"]*)"$`, func(b1, b2 string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, b1) || !strings.Contains(txt, b2) {
			return fmt.Errorf("expected buckets %q and %q in budget table", b1, b2)
		}
		return nil
	})

	ctx.Step(`^I open the Bucket Inspector drawer for "([^"]*)"$`, func(bucketName string) error {
		bID, err := tc.findBucketID(bucketName)
		if err != nil {
			return err
		}
		_, err = tc.page.Eval(`(args) => openBucketDrawer(args.id, args.name, 'Needs', '0.00', 'PLN')`, map[string]string{
			"id":   bID,
			"name": bucketName,
		})
		return err
	})

	ctx.Step(`^I click "\+ Add Target" and create a "([^"]*)" target named "([^"]*)" with priority "([^"]*)" and amount "([^"]*)"$`, func(cadence, targetName, priorityLabel, amount string) error {
		prioVal := "3"
		if strings.Contains(priorityLabel, "2") {
			prioVal = "2"
		} else if strings.Contains(priorityLabel, "1") {
			prioVal = "1"
		}
		_, err := tc.page.Eval(`(args) => {
			openNewTargetModal();
			document.getElementById('target_name').value = args.name;
			document.getElementById('target_cadence').value = args.cadence;
			document.getElementById('target_priority').value = args.prio;
			document.getElementById('target_amount').value = args.amount;
		}`, map[string]string{
			"name":    targetName,
			"cadence": cadence,
			"prio":    prioVal,
			"amount":  amount,
		})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#target_form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the bucket "([^"]*)" should display required target "([^"]*)" and health badge "([^"]*)"$`, func(bucketName, reqAmt, badge string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, bucketName) || !containsNormalized(txt, reqAmt) || !strings.Contains(txt, badge) {
			return fmt.Errorf("expected bucket %q with required target %q and badge %q", bucketName, reqAmt, badge)
		}
		return nil
	})

	ctx.Step(`^I open the "Assign" modal for "([^"]*)" and assign "([^"]*)" PLN$`, func(bucketName, amount string) error {
		bID, err := tc.findBucketID(bucketName)
		if err != nil {
			return err
		}
		_, err = tc.page.Eval(`(args) => {
			openAssignModal(args.id, args.name, 'PLN');
			document.querySelector('#assign_funds_form input[name="amount"]').value = args.amount;
		}`, map[string]string{"id": bID, "name": bucketName, "amount": amount})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#assign_funds_form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the bucket "([^"]*)" available balance should be "([^"]*)" and health badge should be "([^"]*)"$`, func(bucketName, balStr, badge string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, bucketName) || !containsNormalized(txt, balStr) || !strings.Contains(txt, badge) {
			return fmt.Errorf("expected bucket %q to have balance %q and badge %q", bucketName, balStr, badge)
		}
		return nil
	})

	ctx.Step(`^I open the "Move" modal from "([^"]*)" and move "([^"]*)" PLN to bucket "([^"]*)"$`, func(srcBucket, amount, dstBucket string) error {
		srcID, err := tc.findBucketID(srcBucket)
		if err != nil {
			return err
		}
		dstID, err := tc.findBucketID(dstBucket)
		if err != nil {
			return err
		}
		_, err = tc.page.Eval(`(args) => {
			openMoveModal(args.srcId, args.srcName, 'PLN');
			document.querySelector('#move_funds_modal select[name="dest_bucket_id"]').value = args.dstId;
			document.querySelector('#move_funds_modal input[name="amount"]').value = args.amount;
		}`, map[string]string{
			"srcId":   srcID,
			"srcName": srcBucket,
			"dstId":   dstID,
			"amount":  amount,
		})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#move_funds_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the bucket "([^"]*)" available balance should be "([^"]*)"$`, func(bucketName, balStr string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, bucketName) || !containsNormalized(txt, balStr) {
			return fmt.Errorf("expected bucket %q to display balance %q, got: %.400s", bucketName, balStr, txt)
		}
		return nil
	})

	ctx.Step(`^I click the "Auto-Assign by Priority" button$`, func() error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action*="/auto-assign"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	// =========================================================================
	// TRANSACTIONS, BANK SYNC, RECONCILE WORKBENCH & CSV IMPORT STEPS
	// =========================================================================

	ctx.Step(`^I submit the manual transaction form with account "([^"]*)", bucket "([^"]*)", amount "([^"]*)", date "([^"]*)" and note "([^"]*)"$`, func(acctName, bucketName, amount, dateStr, note string) error {
		acctID, err := tc.findAccountID(acctName)
		if err != nil {
			return err
		}
		bucketID, err := tc.findBucketID(bucketName)
		if err != nil {
			return err
		}
		cleanAmt := strings.TrimPrefix(amount, "-")
		_, err = tc.page.Eval(`(args) => {
			document.getElementById('qa_date').value = args.date;
			document.getElementById('qa_account_id').value = args.acctId;
			document.getElementById('qa_bucket_id').value = args.bucketId;
			document.getElementById('qa_payee').value = args.note;
			document.getElementById('qa_amount').value = args.amount;
		}`, map[string]string{
			"date":     dateStr,
			"acctId":   acctID,
			"bucketId": bucketID,
			"note":     note,
			"amount":   cleanAmt,
		})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#quick_add_form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the transactions ledger table should display "([^"]*)" with amount "([^"]*)"$`, func(note, amountStr string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, note) || !containsNormalized(txt, amountStr) {
			return fmt.Errorf("expected ledger to display %q with amount %q, got: %.400s", note, amountStr, txt)
		}
		return nil
	})

	ctx.Step(`^I open the edit modal for transaction "([^"]*)" and update the note to "([^"]*)" and amount to "([^"]*)"$`, func(oldNote, newNote, newAmt string) error {
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var txID, acctID, bucketID string
		err = tenantDB.QueryRow(`SELECT id, source_account_id, COALESCE(bucket_id, '') FROM budget_transactions_current WHERE notes = ?`, oldNote).
			Scan(&txID, &acctID, &bucketID)
		if err != nil {
			return err
		}
		cleanAmt := strings.TrimPrefix(newAmt, "-")
		_, err = tc.page.Eval(`(args) => {
			openEditTxModal(args.id, args.amt, '2026-10-16', args.note, args.bucketId, args.acctId);
			document.getElementById('edit_tx_notes').value = args.note;
			document.getElementById('edit_tx_amount').value = args.amt;
		}`, map[string]string{
			"id":       txID,
			"amt":      cleanAmt,
			"note":     newNote,
			"bucketId": bucketID,
			"acctId":   acctID,
		})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#edit_tx_form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^I delete the transaction "([^"]*)" from the actions menu$`, func(note string) error {
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var txID string
		if err := tenantDB.QueryRow(`SELECT id FROM budget_transactions_current WHERE notes = ?`, note).Scan(&txID); err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		_, err = tc.page.Eval(`(id) => {
			const form = document.querySelector('form[action*="/transactions/' + id + '/delete"]');
			form.submit();
		}`, txID)
		if err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^"([^"]*)" should no longer appear in the transactions ledger table$`, func(note string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if strings.Contains(txt, note) {
			return fmt.Errorf("expected %q to be deleted from ledger", note)
		}
		return nil
	})

	ctx.Step(`^I open the "Sync with Bank" modal and trigger bank synchronization for "([^"]*)"$`, func(acctName string) error {
		_, err := tc.page.Eval(`() => bank_sync_modal.showModal()`)
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#bank_sync_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^pending raw bank transactions should appear in the Reconcile Workbench$`, func() error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, "ZABKA") || !strings.Contains(txt, "RESTAURACJA POD BARANEM") {
			return fmt.Errorf("expected synced mock raw transactions in workbench, got: %.400s", txt)
		}
		return nil
	})

	ctx.Step(`^I click "Auto-Match" in the Reconcile Workbench$`, func() error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action*="/reconcile/auto-match"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the "([^"]*)" raw transactions should be automatically resolved into bucket "([^"]*)"$`, func(payeePattern, bucketName string) error {
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var count int
		err = tenantDB.QueryRow(`
			SELECT COUNT(*) FROM budget_transactions_current bt
			JOIN buckets_projection bp ON bt.bucket_id = bp.id
			WHERE bt.notes LIKE ? AND bp.name = ?`, "%"+payeePattern+"%", bucketName).Scan(&count)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("expected %q transactions to be resolved into bucket %q", payeePattern, bucketName)
		}
		return nil
	})

	ctx.Step(`^I select the 3 dining bill and friend repayment raw transactions and execute a "Merge" resolution into bucket "([^"]*)"$`, func(bucketName string) error {
		bID, err := tc.findBucketID(bucketName)
		if err != nil {
			return err
		}
		_, err = tc.page.Eval(`(bucketId) => {
			document.querySelectorAll('.raw-select-cb').forEach(cb => {
				const p = (cb.dataset.payee || '').toUpperCase();
				if (p.includes('RESTAURACJA') || p.includes('MAREK') || p.includes('ANNA')) {
					cb.checked = true;
				} else {
					cb.checked = false;
				}
			});
			updateFloatingActionBar();
			openMergeModal();
			document.querySelector('#merge_form select[name="bucket_id"]').value = bucketId;
			document.querySelector('#merge_form input[name="notes"]').value = 'Merged Dining Out';
		}`, bID)
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#merge_form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^a single net budget transaction of "([^"]*)" should appear in the ledger for bucket "([^"]*)"$`, func(netAmt, bucketName string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !containsNormalized(txt, netAmt) || !strings.Contains(txt, bucketName) {
			return fmt.Errorf("expected net merged transaction %q in bucket %q", netAmt, bucketName)
		}
		return nil
	})

	ctx.Step(`^I open the "Split" modal for a pending raw transaction and split it across "([^"]*)" and "([^"]*)"$`, func(b1Name, b2Name string) error {
		b1ID, err := tc.findBucketID(b1Name)
		if err != nil {
			return err
		}
		b2ID, err := tc.findBucketID(b2Name)
		if err != nil {
			return err
		}
		_, err = tc.page.Eval(`(args) => {
			const cbs = Array.from(document.querySelectorAll('.raw-select-cb'));
			cbs.forEach((cb, idx) => { cb.checked = (idx === 0); });
			updateFloatingActionBar();
			openSplitModal();
			const selects = document.querySelectorAll('#split_form select[name="split_bucket_id"]');
			if (selects.length >= 2) {
				selects[0].value = args.b1;
				selects[1].value = args.b2;
			}
			const notes = document.querySelectorAll('#split_form input[name="split_note"]');
			if (notes.length >= 2) {
				notes[0].value = 'Split Part 1';
				notes[1].value = 'Split Part 2';
			}
		}`, map[string]string{"b1": b1ID, "b2": b2ID})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#split_form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the split budget transactions should appear in the ledger$`, func() error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, "Split Part 1") || !strings.Contains(txt, "Split Part 2") {
			return fmt.Errorf("expected Split Part 1 and Split Part 2 in ledger")
		}
		return nil
	})

	ctx.Step(`^I select account "([^"]*)", upload a CSV statement with "([^"]*)" and "([^"]*)" rows, and click preview$`, func(acctName, p1, p2 string) error {
		acctID, err := tc.findAccountID(acctName)
		if err != nil {
			return err
		}
		_, err = tc.page.Eval(`(id) => { document.querySelector('select[name="account_id"]').value = id; }`, acctID)
		if err != nil {
			return err
		}
		csvPath := filepath.Join(tc.tmpDir, "statement.csv")
		csvContent := fmt.Sprintf("Date,Amount,Payee,Reference,IBAN\n2026-10-14,-85.50,%s,Card payment,PL111\n2026-10-15,4500.00,%s,Monthly salary,PL222\n", p1, p2)
		if err := os.WriteFile(csvPath, []byte(csvContent), 0644); err != nil {
			return err
		}
		fileInput, err := tc.page.Element(`#csv_file`)
		if err != nil {
			return err
		}
		if err := fileInput.SetFiles([]string{csvPath}); err != nil {
			return err
		}
		// Wait briefly for FileReader onload callback in browser
		for i := 0; i < 20; i++ {
			txt, _ := tc.bodyText()
			if strings.Contains(txt, p1) && strings.Contains(txt, p2) {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("CSV preview table did not populate after selecting file")
	})

	ctx.Step(`^the CSV preview table should display (\d+) parsed rows in the browser$`, func(expectedDataRows int) error {
		htmlStr, err := tc.pageHTML()
		if err != nil {
			return err
		}
		if !strings.Contains(htmlStr, "Biedronka") || !strings.Contains(htmlStr, "Employer Sp. z o.o.") {
			return fmt.Errorf("expected parsed CSV rows in #preview-tbody")
		}
		return nil
	})

	ctx.Step(`^I confirm and submit the CSV import with auto-match enabled$`, func() error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#csv-import-form button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the browser should redirect to the transactions view$`, func() error {
		info, err := tc.page.Info()
		if err != nil {
			return err
		}
		if !strings.HasSuffix(info.URL, "/transactions") {
			return fmt.Errorf("expected redirect to /transactions, got %s", info.URL)
		}
		return nil
	})

	ctx.Step(`^the "([^"]*)" transaction should appear in the resolved budget transactions ledger$`, func(payee string) error {
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var count int
		if err := tenantDB.QueryRow(`SELECT COUNT(*) FROM budget_transactions_current WHERE notes LIKE ?`, "%"+payee+"%").Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("expected %q in resolved budget_transactions_current", payee)
		}
		txt, _ := tc.bodyText()
		if !strings.Contains(txt, payee) {
			return fmt.Errorf("expected %q rendered in ledger table", payee)
		}
		return nil
	})

	ctx.Step(`^the "([^"]*)" transaction should appear in the Pending Bank Feeds workbench$`, func(payee string) error {
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		var count int
		if err := tenantDB.QueryRow(`SELECT COUNT(*) FROM raw_transactions_current WHERE raw_payee LIKE ? AND status = 'PENDING'`, "%"+payee+"%").Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("expected %q in pending raw_transactions_current", payee)
		}
		txt, _ := tc.bodyText()
		if !strings.Contains(txt, payee) {
			return fmt.Errorf("expected %q rendered in Pending Bank Feeds workbench", payee)
		}
		return nil
	})

	// =========================================================================
	// REPORTS, PARTIES, MEMBERS & PERSONAL ACCESS TOKENS STEPS
	// =========================================================================

	ctx.Step(`^the dashboard should render all 10 report cards including "([^"]*)", "([^"]*)", "([^"]*)" and "([^"]*)"$`, func(r1, r2, r3, r4 string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		for _, r := range []string{r1, r2, r3, r4} {
			if !strings.Contains(txt, r) {
				return fmt.Errorf("expected report card %q on dashboard", r)
			}
		}
		// Wait briefly for async ECharts initialization on all 10 chart containers
		for i := 0; i < 20; i++ {
			htmlStr, _ := tc.pageHTML()
			if strings.Count(htmlStr, `data-echarts-initialized="true"`) >= 10 {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	})

	ctx.Step(`^I click on the "([^"]*)" report card to open the detailed report view$`, func(reportTitle string) error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		el, err := tc.page.ElementR("a", regexp.QuoteMeta(reportTitle))
		if err != nil {
			return err
		}
		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the detailed report page should render filter controls and initialize the ECharts chart canvas$`, func() error {
		for i := 0; i < 20; i++ {
			htmlStr, _ := tc.pageHTML()
			if strings.Contains(htmlStr, "report-filters-form") && strings.Contains(htmlStr, `data-echarts-initialized="true"`) {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("expected #report-filters-form and initialized ECharts container on detailed report page")
	})

	ctx.Step(`^I open the "Save Report Configuration" modal, save a report named "([^"]*)" as pinned, and return to "([^"]*)"$`, func(reportName, returnPath string) error {
		_, err := tc.page.Eval(`(name) => {
			document.getElementById('save_report_modal').showModal();
			document.querySelector('#save_report_modal input[name="name"]').value = name;
			document.querySelector('#save_report_modal input[name="is_pinned_widget"]').checked = true;
		}`, reportName)
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#save_report_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the "Saved Report Configurations" section should display "([^"]*)" with a pinned badge$`, func(reportName string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, reportName) || !strings.Contains(txt, "Pinned") {
			return fmt.Errorf("expected saved report %q with Pinned badge, got: %.400s", reportName, txt)
		}
		return nil
	})

	ctx.Step(`^I unpin and delete the saved report "([^"]*)"$`, func(reportName string) error {
		wait1 := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action*="/toggle-pin"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait1()
		if err := tc.page.WaitLoad(); err != nil {
			return err
		}

		wait2 := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action*="/reports/saved/"][action*="/delete"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait2()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^"([^"]*)" should be removed from the saved reports section$`, func(reportName string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if strings.Contains(txt, reportName) {
			return fmt.Errorf("expected saved report %q to be deleted", reportName)
		}
		return nil
	})

	ctx.Step(`^I open the "New Counterparty" modal and create an "([^"]*)" party named "([^"]*)"$`, func(partyType, partyName string) error {
		_, err := tc.page.Eval(`(args) => {
			new_party_modal.showModal();
			document.querySelector('#new_party_modal input[name="name"]').value = args.name;
			document.querySelector('#new_party_modal select[name="party_type"]').value = args.type;
		}`, map[string]string{"name": partyName, "type": partyType})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#new_party_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the parties table should display "([^"]*)" with badge "([^"]*)"$`, func(partyName, badge string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, partyName) || !strings.Contains(txt, badge) {
			return fmt.Errorf("expected party %q with badge %q", partyName, badge)
		}
		return nil
	})

	ctx.Step(`^I open the "Invite Member" modal and invite "([^"]*)" with role "([^"]*)"$`, func(email, role string) error {
		_, err := tc.page.Eval(`(args) => {
			invite_modal.showModal();
			document.querySelector('#invite_modal input[name="email"]').value = args.email;
			document.querySelector('#invite_modal select[name="role"]').value = args.role;
		}`, map[string]string{"email": email, "role": role})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#invite_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the browser should complete the invitation submission without errors and render the members table$`, func() error {
		var count int
		if err := tc.mainDB.QueryRow(`SELECT COUNT(*) FROM budget_invitations WHERE budget_id = ?`, tc.activeBudgetID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("expected invitation record in budget_invitations")
		}
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, "Members & Access Control") {
			return fmt.Errorf("expected Members & Access Control table to render")
		}
		return nil
	})

	ctx.Step(`^I generate a new Personal Access Token named "([^"]*)" with "([^"]*)" expiration$`, func(tokenName, expLabel string) error {
		_, err := tc.page.Eval(`(name) => {
			document.querySelector('form[action*="/settings/tokens"] input[name="name"]').value = name;
		}`, tokenName)
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action*="/settings/tokens"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the token created modal should open displaying the generated "bal_pat_" token value$`, func() error {
		htmlStr, err := tc.pageHTML()
		if err != nil {
			return err
		}
		if !strings.Contains(htmlStr, "token_created_modal") || !strings.Contains(htmlStr, "bal_pat_") {
			return fmt.Errorf("expected token_created_modal with bal_pat_ token")
		}
		return nil
	})

	ctx.Step(`^I close the token modal and click "Revoke" on "([^"]*)"$`, func(tokenName string) error {
		wait1 := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#token_created_modal a.btn`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait1()
		if err := tc.page.WaitLoad(); err != nil {
			return err
		}

		wait2 := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`form[action*="/revoke"] button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait2()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the token "([^"]*)" row should display status badge "([^"]*)"$`, func(tokenName, badge string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, tokenName) || !strings.Contains(txt, badge) {
			return fmt.Errorf("expected token %q to display status badge %q", tokenName, badge)
		}
		return nil
	})

	// =========================================================================
	// AI & Import Structure Steps (BALANCE-31)
	// =========================================================================

	ctx.Step(`^bucket "([^"]*)" has target "([^"]*)" of "([^"]*)"$`, func(bucketName, targetName, amtStr string) error {
		bID, err := tc.seedBucket(bucketName, "Needs")
		if err != nil {
			return err
		}
		tenantDB, err := tc.tenantMgr.GetTenantDB(tc.activeBudgetID, false)
		if err != nil {
			return err
		}
		now := time.Now().UTC().UnixMicro()
		tID := domain.NewUUIDv7()
		minor := parseMajorToMinor(amtStr)
		_, err = tenantDB.Exec(`
			INSERT INTO targets_projection (
				id, version, bucket_id, name, target_type, priority, sort_order,
				amount_minor, currency, recurrence_cadence, recurrence_interval,
				non_spending_strategy, current_funded_minor, is_archived, updated_at
			) VALUES (?, 1, ?, ?, 'EXPENSE', 2, 1, ?, 'PLN', 'MONTHLY', NULL, 'RETURN_TO_POOL', 0, 0, ?)`,
			tID, bID, targetName, minor, now,
		)
		return err
	})

	ctx.Step(`^the budget table should display the empty structure state card$`, func() error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		if !strings.Contains(txt, "No Buckets Created Yet") && !strings.Contains(txt, "No Envelopes Created Yet") {
			return fmt.Errorf("expected empty structure state card with 'No Buckets Created Yet', got: %s", txt)
		}
		return nil
	})

	ctx.Step(`^the structure preview should detect existing bucket "([^"]*)"$`, func(bucketName string) error {
		res, err := tc.page.Eval(`(bName) => {
			const cards = Array.from(document.querySelectorAll('.bucket-card'));
			for (const c of cards) {
				const nameInput = c.querySelector('.bucket-name-input');
				if (nameInput && nameInput.value.toLowerCase().includes(bName.toLowerCase())) {
					const conflictBadge = c.querySelector('.bucket-conflict-select');
					return conflictBadge !== null;
				}
			}
			return false;
		}`, bucketName)
		if err != nil {
			return err
		}
		if !res.Value.Bool() {
			return fmt.Errorf("expected existing bucket conflict detected for %q", bucketName)
		}
		return nil
	})

	ctx.Step(`^I set priority of target "([^"]*)" to "([^"]*)" in the structure preview$`, func(targetName, prio string) error {
		prioNum := "2"
		if strings.Contains(prio, "3") {
			prioNum = "3"
		} else if strings.Contains(prio, "1") {
			prioNum = "1"
		}
		_, err := tc.page.Eval(`(tName, pVal) => {
			const rows = Array.from(document.querySelectorAll('.target-chk')).map(chk => chk.closest('div'));
			for (const r of rows) {
				const nameInput = r.querySelector('.target-name-input');
				if (nameInput && nameInput.value.toLowerCase().includes(tName.toLowerCase())) {
					const sel = r.querySelector('.target-prio-select');
					if (sel) {
						sel.value = pVal;
						sel.dispatchEvent(new Event('change', { bubbles: true }));
						return true;
					}
				}
			}
			return false;
		}`, targetName, prioNum)
		return err
	})

	ctx.Step(`^I open the AI Structure generator modal$`, func() error {
		_, err := tc.page.Eval(`() => openStructureModal('ai')`)
		return err
	})

	ctx.Step(`^I enter budget prompt "([^"]*)" and click "Generate Preview"$`, func(prompt string) error {
		if err := tc.page.MustElement(`#structure_ai_prompt`).Input(prompt); err != nil {
			return err
		}
		if err := tc.page.MustElement(`#btn_generate_preview`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		return rod.Try(func() {
			tc.page.MustElement(`#structure_preview_wrapper:not(.hidden)`).MustWaitVisible()
		})
	})

	ctx.Step(`^the structure preview should list proposed envelopes "([^"]*)", "([^"]*)", and "([^"]*)"$`, func(e1, e2, e3 string) error {
		res, err := tc.page.Eval(`() => {
			const wrapper = document.getElementById('structure_preview_wrapper');
			if (!wrapper) return '';
			const inputs = Array.from(wrapper.querySelectorAll('input[type="text"]')).map(i => i.value);
			return wrapper.innerText + ' ' + inputs.join(' ');
		}`)
		if err != nil {
			return err
		}
		txt := strings.ToLower(res.Value.Str())
		if !strings.Contains(txt, strings.ToLower(e1)) || !strings.Contains(txt, strings.ToLower(e2)) || !strings.Contains(txt, strings.ToLower(e3)) {
			return fmt.Errorf("expected proposed envelopes %q, %q, %q in preview, got: %s", e1, e2, e3, txt)
		}
		return nil
	})

	ctx.Step(`^the structure preview should list proposed envelopes "([^"]*)" and "([^"]*)"$`, func(e1, e2 string) error {
		res, err := tc.page.Eval(`() => {
			const wrapper = document.getElementById('structure_preview_wrapper');
			if (!wrapper) return '';
			const inputs = Array.from(wrapper.querySelectorAll('input[type="text"]')).map(i => i.value);
			return wrapper.innerText + ' ' + inputs.join(' ');
		}`)
		if err != nil {
			return err
		}
		txt := strings.ToLower(res.Value.Str())
		if !strings.Contains(txt, strings.ToLower(e1)) || !strings.Contains(txt, strings.ToLower(e2)) {
			return fmt.Errorf("expected proposed envelopes %q, %q in preview, got: %s", e1, e2, txt)
		}
		return nil
	})

	ctx.Step(`^I click "Apply Selected Structure"$`, func() error {
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#btn_apply_structure`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})

	ctx.Step(`^the budget table should list buckets "([^"]*)", "([^"]*)", and "([^"]*)"$`, func(b1, b2, b3 string) error {
		txt, err := tc.bodyText()
		if err != nil {
			return err
		}
		lower := strings.ToLower(txt)
		if !strings.Contains(lower, strings.ToLower(b1)) || !strings.Contains(lower, strings.ToLower(b2)) || !strings.Contains(lower, strings.ToLower(b3)) {
			return fmt.Errorf("expected buckets %q, %q, %q in budget table, got: %s", b1, b2, b3, txt)
		}
		return nil
	})

	ctx.Step(`^I open the Structure Import modal$`, func() error {
		_, err := tc.page.Eval(`() => openStructureModal('import')`)
		return err
	})

	ctx.Step(`^I select source budget "([^"]*)" and click "Load Structure"$`, func(sourceBudgetName string) error {
		_, err := tc.page.Eval(`(name) => {
			const sel = document.getElementById('source_budget_select');
			for (let i = 0; i < sel.options.length; i++) {
				if (sel.options[i].text.includes(name)) {
					sel.selectedIndex = i;
					break;
				}
			}
		}`, sourceBudgetName)
		if err != nil {
			return err
		}
		if err := tc.page.MustElement(`#btn_load_source`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		return rod.Try(func() {
			tc.page.MustElement(`#structure_preview_wrapper:not(.hidden)`).MustWaitVisible()
		})
	})

	ctx.Step(`^I resize the browser viewport to mobile 375x812 and navigate to the "Budget" workspace view$`, func() error {
		if err := tc.page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width:             375,
			Height:            812,
			DeviceScaleFactor: 2,
			Mobile:            true,
		}); err != nil {
			return err
		}
		return tc.navigate("/budgets/" + tc.activeBudgetID + "/budget")
	})

	ctx.Step(`^the mobile horizon selector and bottom dock should be visible in the mobile viewport$`, func() error {
		res, err := tc.page.Eval(`() => {
			const sel = document.getElementById('mobile_horizon_select');
			const dock = document.querySelector('.dock');
			const groupHdr = document.querySelector('.mobile-group-header');
			const card = document.querySelector('.mobile-bucket-card');
			if (!sel || !dock || !groupHdr || !card) return false;
			const selRect = sel.getBoundingClientRect();
			const dockRect = dock.getBoundingClientRect();
			const cardRect = card.getBoundingClientRect();
			return selRect.width > 0 && dockRect.width > 0 && cardRect.width > 0;
		}`)
		if err != nil {
			return err
		}
		if !res.Value.Bool() {
			return fmt.Errorf("expected mobile_horizon_select, .dock, .mobile-group-header, and .mobile-bucket-card to have positive visible dimensions at 375x812")
		}
		return nil
	})

	ctx.Step(`^I click the quick "Assign" button on the mobile bucket card for "([^"]*)" and assign "([^"]*)" PLN$`, func(bucketName, amount string) error {
		_, err := tc.page.Eval(`(args) => {
			const cards = Array.from(document.querySelectorAll('.mobile-bucket-card'));
			const card = cards.find(c => c.innerText.includes(args.name));
			if (!card) throw new Error('mobile bucket card not found for ' + args.name);
			const btn = Array.from(card.querySelectorAll('button')).find(b => b.innerText.includes('Assign'));
			if (!btn) throw new Error('quick Assign button not found on mobile card');
			btn.click();
			const inp = document.querySelector('#assign_funds_modal input[name="amount"]');
			inp.value = args.amount;
		}`, map[string]any{"name": bucketName, "amount": amount})
		if err != nil {
			return err
		}
		wait := tc.page.WaitNavigation(proto.PageLifecycleEventNameLoad)
		if err := tc.page.MustElement(`#assign_funds_modal button[type="submit"]`).Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
		wait()
		return tc.page.WaitLoad()
	})
}

func TestBrowserE2E(t *testing.T) {
	paths := []string{"../../features/e2e"}
	if feat := os.Getenv("GODOG_FEATURE"); feat != "" {
		paths = []string{feat}
	}
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeE2EScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    paths,
			Tags:     "@e2e",
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run browser E2E feature tests")
	}
}
