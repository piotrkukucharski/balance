package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/web/handlers"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("failed creating data dir: %v", err)
	}

	mainDBPath := filepath.Join(dataDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		log.Fatalf("failed opening main DB: %v", err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		log.Fatalf("main DB migrations failed: %v", err)
	}

	budgetsDir := filepath.Join(dataDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 64)
	if err != nil {
		log.Fatalf("failed initializing tenant manager: %v", err)
	}
	defer tenantMgr.CloseAll()

	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())
	e.Use(handlers.CorrelationIDMiddleware())

	authHandler := handlers.NewAuthHandler(mainDB, tenantMgr)
	workspaceHandler := handlers.NewWorkspaceHandler(mainDB, tenantMgr)
	txHandler := handlers.NewTransactionsHandler(mainDB, tenantMgr)
	reportsHandler := handlers.NewReportsHandler(mainDB, tenantMgr)
	partiesHandler := handlers.NewPartiesHandler(mainDB, tenantMgr)
	membersHandler := handlers.NewMembersHandler(mainDB, tenantMgr)
	accountsHandler := handlers.NewAccountsHandler(mainDB, tenantMgr)
	pwaHandler := handlers.NewPWAHandler()

	// PWA routes
	e.GET("/manifest.webmanifest", pwaHandler.Manifest)
	e.GET("/sw.js", pwaHandler.ServiceWorker)

	// Public Auth routes
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

	// Tenant scoped routes with RBAC
	tenantViewer := authGroup.Group("/budgets/:budgetId", handlers.TenantAuthzMiddleware(tenantMgr, "VIEWER"))
	tenantViewer.GET("/budget", workspaceHandler.BudgetView)
	tenantViewer.GET("", workspaceHandler.BudgetView)
	tenantViewer.GET("/dashboard", workspaceHandler.BudgetView)
	tenantViewer.GET("/accounts", accountsHandler.AccountsView)
	tenantViewer.GET("/transactions", txHandler.TransactionsView)
	tenantViewer.GET("/reports", reportsHandler.ReportsView)
	tenantViewer.GET("/parties", partiesHandler.PartiesView)
	tenantViewer.GET("/members", membersHandler.MembersView)

	tenantEditor := authGroup.Group("/budgets/:budgetId", handlers.TenantAuthzMiddleware(tenantMgr, "EDITOR"))
	tenantEditor.POST("/accounts", accountsHandler.CreateAccount)
	tenantEditor.POST("/accounts/link-bank", accountsHandler.LinkBankAccount)
	tenantEditor.POST("/accounts/:id/update", accountsHandler.UpdateAccount)
	tenantEditor.POST("/accounts/:id/toggle-close", accountsHandler.ToggleCloseAccount)
	tenantEditor.POST("/accounts/:id/delete", accountsHandler.DeleteAccount)
	tenantEditor.POST("/transactions", txHandler.AddManualTransaction)
	tenantEditor.POST("/transactions/sync", txHandler.SyncWithBank)
	tenantEditor.POST("/parties", partiesHandler.CreateParty)

	tenantAdmin := authGroup.Group("/budgets/:budgetId", handlers.TenantAuthzMiddleware(tenantMgr, "ADMIN"))
	tenantAdmin.POST("/members/invite", membersHandler.InviteMember)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Balance server listening on http://localhost:%s\n", port)
	if err := e.Start(":" + port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
