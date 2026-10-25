package openapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	openapi "github.com/balance/balance/internal/api"
	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/service"
	"github.com/balance/balance/internal/web/handlers"
	"github.com/labstack/echo/v4"
)

func setupTestAPIServer(t *testing.T) (*echo.Echo, *sql.DB, *db.TenantDBManager, *service.AppService, string) {
	tempDir, err := os.MkdirTemp("", "balance_api_test_*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	mainDBPath := filepath.Join(tempDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		t.Fatalf("failed opening main DB: %v", err)
	}
	t.Cleanup(func() { mainDB.Close() })

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatalf("main migrations failed: %v", err)
	}

	budgetsDir := filepath.Join(tempDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 16)
	if err != nil {
		t.Fatalf("failed creating tenant manager: %v", err)
	}
	t.Cleanup(func() { tenantMgr.CloseAll() })

	sysClock := clock.NewSystemClock(time.UTC)
	appService := service.NewAppService(mainDB, tenantMgr, sysClock)

	server := openapi.NewServer(mainDB, tenantMgr, appService)

	e := echo.New()
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
	openapi.RegisterHandlersWithBaseURL(apiGroup, server, "")

	return e, mainDB, tenantMgr, appService, tempDir
}

func TestAPI_Auth_RegisterAndLoginAndTokenFlow(t *testing.T) {
	e, mainDB, _, _, _ := setupTestAPIServer(t)

	// 1. Register User
	regBody, _ := json.Marshal(map[string]interface{}{
		"email":        "api_user@example.com",
		"password":     "SecurePassword123!",
		"display_name": "API Tester",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on register, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Login User
	loginBody, _ := json.Marshal(map[string]interface{}{
		"email":    "api_user@example.com",
		"password": "SecurePassword123!",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on login, got %d: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatalf("expected Set-Cookie on login response")
	}

	var loginResp openapi.LoginResponse
	json.Unmarshal(rec.Body.Bytes(), &loginResp)

	// Fetch CSRF token for session
	var csrfToken string
	mainDB.QueryRowContext(context.Background(), `SELECT csrf_token FROM sessions WHERE user_id = ?`, loginResp.User.Id.String()).Scan(&csrfToken)

	// 3. Create PAT Token
	tokBody, _ := json.Marshal(map[string]interface{}{
		"name":            "Automation PAT",
		"expires_in_days": 30,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/tokens", bytes.NewReader(tokBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on token create, got %d: %s", rec.Code, rec.Body.String())
	}

	var tokResp openapi.CreateTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tokResp); err != nil {
		t.Fatalf("failed unmarshaling token response: %v", err)
	}
	if tokResp.Token == "" {
		t.Fatalf("expected non-empty PAT token")
	}

	// 4. Test Authenticating via Bearer PAT on /users/me
	req = httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /users/me with Bearer PAT, got %d: %s", rec.Code, rec.Body.String())
	}

	var userResp openapi.UserResponse
	json.Unmarshal(rec.Body.Bytes(), &userResp)
	if string(userResp.Email) != "api_user@example.com" {
		t.Errorf("expected email api_user@example.com, got %s", userResp.Email)
	}

	// 5. Create Budget using Bearer PAT
	bgBody, _ := json.Marshal(map[string]interface{}{
		"name":             "API Test Budget",
		"default_currency": "PLN",
		"timezone":         "UTC",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets", bytes.NewReader(bgBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on budget create, got %d: %s", rec.Code, rec.Body.String())
	}
	var bgResp openapi.BudgetSummary
	json.Unmarshal(rec.Body.Bytes(), &bgResp)
	budgetID := bgResp.Id.String()

	// 6. Create Bucket
	bktBody, _ := json.Marshal(map[string]interface{}{
		"name":       "Groceries",
		"group_name": "Living",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+budgetID+"/buckets", bytes.NewReader(bktBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on bucket create, got %d: %s", rec.Code, rec.Body.String())
	}

	// 7. List Buckets
	req = httptest.NewRequest(http.MethodGet, "/api/v1/budgets/"+budgetID+"/buckets", nil)
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on list buckets, got %d: %s", rec.Code, rec.Body.String())
	}
	var buckets []openapi.Bucket
	json.Unmarshal(rec.Body.Bytes(), &buckets)
	if len(buckets) != 1 || buckets[0].Name != "Groceries" {
		t.Fatalf("expected 1 bucket 'Groceries', got %+v", buckets)
	}

	// 8. Create Party
	partyBody, _ := json.Marshal(map[string]interface{}{
		"name":       "Żabka",
		"party_type": "ORGANIZATION",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+budgetID+"/parties", bytes.NewReader(partyBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on party create, got %d: %s", rec.Code, rec.Body.String())
	}

	// 9. Validate Formula Endpoint
	valBody, _ := json.Marshal(map[string]interface{}{
		"lua_script": "return math.max(100, 200)",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+budgetID+"/targets/validate-formula", bytes.NewReader(valBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on validate-formula, got %d: %s", rec.Code, rec.Body.String())
	}
	var valResp openapi.ValidateFormulaResponse
	json.Unmarshal(rec.Body.Bytes(), &valResp)
	if !valResp.Valid {
		t.Fatalf("expected formula to be valid, got false")
	}
	if valResp.EvaluatedMinor == nil || *valResp.EvaluatedMinor != 200 {
		t.Fatalf("expected EvaluatedMinor 200, got %v", valResp.EvaluatedMinor)
	}

	// 10. Generate Formula Endpoint
	genBody, _ := json.Marshal(map[string]interface{}{
		"current_bucket_name": "Mortgage",
		"target_currency":    "PLN",
		"user_prompt":        "Save 15% of income",
		"window_days":        30,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+budgetID+"/targets/generate-formula", bytes.NewReader(genBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on generate-formula, got %d: %s", rec.Code, rec.Body.String())
	}
	var genResp openapi.GenerateFormulaResponse
	json.Unmarshal(rec.Body.Bytes(), &genResp)
	if genResp.LuaScript == "" || genResp.Explanation == "" {
		t.Fatalf("expected non-empty LuaScript and Explanation, got script=%q exp=%q", genResp.LuaScript, genResp.Explanation)
	}
}

func TestAPI_RBAC_UnauthorizedAndForbidden(t *testing.T) {
	e, _, _, appService, _ := setupTestAPIServer(t)

	// Create user 1 (Owner)
	u1ID, _ := appService.CreateUser(context.Background(), service.CreateUserInput{
		Email:       "owner@example.com",
		Password:    "Password123!",
		DisplayName: "Owner User",
	})
	bID, _ := appService.CreateBudget(context.Background(), service.CreateBudgetInput{
		OwnerUserID:     u1ID,
		Name:            "Owner Budget",
		DefaultCurrency: "PLN",
		Timezone:        "UTC",
	})

	// Create user 2 (Outsider)
	appService.CreateUser(context.Background(), service.CreateUserInput{
		Email:       "outsider@example.com",
		Password:    "Password123!",
		DisplayName: "Outsider User",
	})

	// Login Outsider
	loginBody, _ := json.Marshal(map[string]interface{}{
		"email":    "outsider@example.com",
		"password": "Password123!",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	cookie := rec.Header().Get("Set-Cookie")

	// Outsider tries to list accounts or buckets of Owner Budget -> Should be 403 Forbidden
	req = httptest.NewRequest(http.MethodGet, "/api/v1/budgets/"+bID+"/buckets", nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for outsider user accessing budget, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unauthenticated request -> Should be 401 Unauthorized
	req = httptest.NewRequest(http.MethodGet, "/api/v1/budgets/"+bID+"/buckets", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated request, got %d: %s", rec.Code, rec.Body.String())
	}
}
