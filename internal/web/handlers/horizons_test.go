package handlers

import (
	"context"
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
	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/service"
	"github.com/labstack/echo/v4"
)

func TestHorizonsManagementHandlers(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_horizons_test_*")
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

	sysClock := clock.NewSystemClock(time.UTC)
	appService := service.NewAppService(mainDB, tenantMgr, sysClock)
	handler := NewWorkspaceHandler(mainDB, tenantMgr, nil)
	handler.SetService(appService)

	e := echo.New()

	// 1. Create User and Budget
	pwdHash, _ := auth.HashPassword("ValidPassword123!")
	userID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, err = mainDB.Exec(`
		INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, "owner@example.com", pwdHash, "Budget Owner", now, now,
	)
	if err != nil {
		t.Fatalf("insert user failed: %v", err)
	}

	budgetID, err := appService.CreateBudget(context.Background(), service.CreateBudgetInput{
		Name:            "Horizon Suite Budget",
		OwnerUserID:     userID,
		DefaultCurrency: "PLN",
		Timezone:        "Europe/Warsaw",
	})
	if err != nil {
		t.Fatalf("CreateBudget failed: %v", err)
	}

	tenantDB, err := handler.tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		t.Fatalf("GetTenantDB failed: %v", err)
	}

	// 2. SettingsView renders horizons table
	req := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/settings", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("budgetId")
	c.SetParamValues(budgetID)
	c.Set(ContextUserIDKey, userID)
	c.Set(ContextRoleKey, "OWNER")
	c.Set(ContextBudgetDBKey, tenantDB)
	c.Set("csrfToken", "test-csrf")

	if err := handler.SettingsView(c); err != nil {
		t.Fatalf("SettingsView failed: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Horyzonty Czasowe") && !strings.Contains(body, "Time Horizons") {
		t.Errorf("expected settings view to contain Time Horizons card, got body: %s", body)
	}

	// 3. User updates default horizon preference to 90d
	form := url.Values{}
	form.Set("default_horizon_id", "90d")
	reqPref := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/settings/default-horizon", strings.NewReader(form.Encode()))
	reqPref.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	recPref := httptest.NewRecorder()
	cPref := e.NewContext(reqPref, recPref)
	cPref.SetParamNames("budgetId")
	cPref.SetParamValues(budgetID)
	cPref.Set(ContextUserIDKey, userID)
	cPref.Set(ContextRoleKey, "OWNER")
	cPref.Set(ContextBudgetDBKey, tenantDB)

	if err := handler.SetUserDefaultHorizonHandler(cPref); err != nil {
		t.Fatalf("SetUserDefaultHorizonHandler failed: %v", err)
	}
	if recPref.Code != http.StatusSeeOther {
		t.Errorf("expected 303 See Other, got %d", recPref.Code)
	}
	defH, _ := budget.GetUserDefaultHorizon(context.Background(), tenantDB, userID)
	if defH != "90d" {
		t.Errorf("expected default horizon 90d, got %s", defH)
	}

	// 4. Owner creates custom rolling horizon (5d)
	form5d := url.Values{}
	form5d.Set("horizon_type", "ROLLING_DAYS")
	form5d.Set("days_value", "5")
	req5d := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/settings/horizons", strings.NewReader(form5d.Encode()))
	req5d.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec5d := httptest.NewRecorder()
	c5d := e.NewContext(req5d, rec5d)
	c5d.SetParamNames("budgetId")
	c5d.SetParamValues(budgetID)
	c5d.Set(ContextUserIDKey, userID)
	c5d.Set(ContextRoleKey, "OWNER")
	c5d.Set(ContextBudgetDBKey, tenantDB)

	if err := handler.CreateHorizonHandler(c5d); err != nil {
		t.Fatalf("CreateHorizonHandler failed: %v", err)
	}

	// 5. Check affected users endpoint
	_ = budget.SetUserDefaultHorizon(context.Background(), tenantDB, userID, "5d")
	reqCheck := httptest.NewRequest(http.MethodGet, "/budgets/"+budgetID+"/settings/horizons/affected-users?horizon_id=5d", nil)
	recCheck := httptest.NewRecorder()
	cCheck := e.NewContext(reqCheck, recCheck)
	cCheck.SetParamNames("budgetId")
	cCheck.SetParamValues(budgetID)
	cCheck.Set(ContextBudgetDBKey, tenantDB)

	if err := handler.CheckHorizonAffectedUsers(cCheck); err != nil {
		t.Fatalf("CheckHorizonAffectedUsers failed: %v", err)
	}
	if !strings.Contains(recCheck.Body.String(), userID) {
		t.Errorf("expected affected users to contain user ID, got %s", recCheck.Body.String())
	}

	// 6. Delete horizon with fallback
	formDel := url.Values{}
	formDel.Set("horizon_id", "5d")
	formDel.Set("fallback_horizon_id", "30d")
	reqDel := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/settings/horizons/delete", strings.NewReader(formDel.Encode()))
	reqDel.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	recDel := httptest.NewRecorder()
	cDel := e.NewContext(reqDel, recDel)
	cDel.SetParamNames("budgetId")
	cDel.SetParamValues(budgetID)
	cDel.Set(ContextUserIDKey, userID)
	cDel.Set(ContextRoleKey, "OWNER")
	cDel.Set(ContextBudgetDBKey, tenantDB)

	if err := handler.DeleteHorizonHandler(cDel); err != nil {
		t.Fatalf("DeleteHorizonHandler failed: %v", err)
	}
	defHAfter, _ := budget.GetUserDefaultHorizon(context.Background(), tenantDB, userID)
	if defHAfter != "30d" {
		t.Errorf("expected user default horizon to be reassigned to 30d, got %s", defHAfter)
	}
}
