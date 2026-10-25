package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/fx"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/service"
	openapi "github.com/balance/balance/internal/api"
	webassets "github.com/balance/balance/internal/web/assets"
	"github.com/balance/balance/internal/web/handlers"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "worker:sync-fx" || os.Args[1] == "sync-fx") {
		ctx := logger.WithComponent(context.Background(), "worker:sync-fx")
		if err := fx.RunWorkerCLI(ctx, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Worker error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctx := context.Background()
	ctx = logger.WithComponent(ctx, "infra:server")

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Error(ctx, "Failed creating data dir", "data_dir", dataDir, "error", err.Error())
		os.Exit(1)
	}

	mainDBPath := filepath.Join(dataDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		logger.Error(ctx, "Failed opening main DB", "path", mainDBPath, "error", err.Error())
		os.Exit(1)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		logger.Error(ctx, "Main DB migrations failed", "error", err.Error())
		os.Exit(1)
	}

	budgetsDir := filepath.Join(dataDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 64)
	if err != nil {
		logger.Error(ctx, "Failed initializing tenant manager", "error", err.Error())
		os.Exit(1)
	}
	defer tenantMgr.CloseAll()

	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(handlers.CorrelationIDMiddleware())
	e.Use(handlers.StructuredRequestLoggerMiddleware())

	systemClock := clock.NewSystemClock(time.UTC)
	appService := service.NewAppService(mainDB, tenantMgr, systemClock)

	authHandler := handlers.NewAuthHandler(mainDB, tenantMgr)
	authHandler.SetService(appService)

	reportsHandler := handlers.NewReportsHandler(mainDB, tenantMgr)
	workspaceHandler := handlers.NewWorkspaceHandler(mainDB, tenantMgr, reportsHandler)
	workspaceHandler.SetService(appService)

	txHandler := handlers.NewTransactionsHandler(mainDB, tenantMgr)
	txHandler.SetService(appService)

	partiesHandler := handlers.NewPartiesHandler(mainDB, tenantMgr)
	membersHandler := handlers.NewMembersHandler(mainDB, tenantMgr)
	tokensHandler := handlers.NewTokensHandler(mainDB)
	accountsHandler := handlers.NewAccountsHandler(mainDB, tenantMgr)
	accountsHandler.SetService(appService)

	pwaHandler := handlers.NewPWAHandler()

	// PWA routes
	e.GET("/manifest.webmanifest", pwaHandler.Manifest)
	e.GET("/sw.js", pwaHandler.ServiceWorker)

	// Public Auth routes
	e.GET("/favicon.ico", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "image/x-icon", webassets.FaviconICO)
	})
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(303, "/budgets")
	})
	e.GET("/login", authHandler.LoginPage)
	e.POST("/login", authHandler.HandleLogin)
	e.GET("/register", authHandler.RegisterPage)
	e.POST("/register", authHandler.HandleRegister)
	e.POST("/logout", authHandler.HandleLogout)

	// Protected routes (Session or Bearer)
	authGroup := e.Group("", handlers.SessionOrBearerAuthMiddleware(mainDB))
	authGroup.GET("/budgets", workspaceHandler.BudgetsList)
	authGroup.POST("/budgets", workspaceHandler.CreateBudget)

	// Bank Integration Callbacks (Must precede /budgets/:budgetId pattern)
	e.GET("/budgets/integrations/gocardless/callback", accountsHandler.GoCardlessCallback)
	authGroup.GET("/budgets/integrations/gocardless/callback", accountsHandler.GoCardlessCallback)

	// Tenant scoped routes with RBAC
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
	tenantEditor.POST("/transactions/sync", txHandler.SyncWithBank)
	tenantEditor.POST("/transactions/preview-csv", txHandler.PreviewCSV)
	tenantEditor.POST("/transactions/import-csv", txHandler.ImportCSV)
	tenantEditor.POST("/transactions/raw/:rawId/resolve", txHandler.ResolveRawTransaction)
	tenantEditor.POST("/reports/saved", reportsHandler.SaveReportConfig)
	tenantEditor.POST("/reports/saved/:reportId/toggle-pin", reportsHandler.TogglePinReport)
	tenantEditor.POST("/reports/saved/:reportId/delete", reportsHandler.DeleteSavedReport)
	tenantEditor.POST("/transactions/raw/:rawId/ignore", txHandler.IgnoreRawTransaction)
	tenantEditor.POST("/transactions/raw/batch-ignore", txHandler.BatchIgnoreRawTransactions)
	tenantEditor.POST("/transactions/resolve/batch", txHandler.BatchResolveRawTransactions)
	tenantEditor.POST("/transactions/reconcile/auto-match", txHandler.AutoMatchRawTransactions)
	tenantEditor.POST("/parties", partiesHandler.CreateParty)
	tenantEditor.POST("/buckets", workspaceHandler.CreateBucket)
	tenantEditor.POST("/buckets/:id/update", workspaceHandler.UpdateBucket)
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
	tenantOwner.POST("/delete", workspaceHandler.DeleteBudget)

	// REST API v1 (OpenAPI 3.1)
	apiServer := openapi.NewServer(mainDB, tenantMgr, appService)
	apiGroup := e.Group("/api/v1", func(next echo.HandlerFunc) echo.HandlerFunc {
		authMiddleware := handlers.SessionOrBearerAuthMiddleware(mainDB)
		return func(c echo.Context) error {
			path := c.Request().URL.Path
			// Skip authentication for public auth endpoints
			if path == "/api/v1/auth/login" || path == "/api/v1/auth/register" {
				return next(c)
			}
			return authMiddleware(next)(c)
		}
	})
	openapi.RegisterHandlersWithBaseURL(apiGroup, apiServer, "")



	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	logger.Info(ctx, "Balance server starting", "port", port, "url", fmt.Sprintf("http://localhost:%s", port))
	if err := e.Start(":" + port); err != nil {
		logger.Error(ctx, "Server stopped unexpectedly", "error", err.Error())
		os.Exit(1)
	}
}
