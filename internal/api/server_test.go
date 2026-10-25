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
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/ai"
	openapi "github.com/balance/balance/internal/api"
	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/service"
	"github.com/balance/balance/internal/web/handlers"
	"github.com/google/uuid"
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
	server.SetFormulaAIProvider(ai.NewMockFormulaAIProvider())

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

	// 9b. Validate Formula with self.anchor_due_date and financial.pmt
	valPMTBody, _ := json.Marshal(map[string]interface{}{
		"lua_script": `
			local nper = date.diff_months(self.anchor_due_date, runtime.today)
			local r = 0.05 / 12
			local pv = 100000
			local pmt = -financial.pmt(r, nper, pv)
			return money(pmt)
		`,
		"due_date": "2036-10-25",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+budgetID+"/targets/validate-formula", bytes.NewReader(valPMTBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on validate-formula PMT, got %d", rec.Code)
	}
	var valPMTResp openapi.ValidateFormulaResponse
	json.Unmarshal(rec.Body.Bytes(), &valPMTResp)
	if !valPMTResp.Valid || valPMTResp.EvaluatedMinor == nil || *valPMTResp.EvaluatedMinor <= 0 {
		t.Fatalf("expected valid PMT formula with positive evaluated minor, got valid=%v, minor=%v, err=%v",
			valPMTResp.Valid, valPMTResp.EvaluatedMinor, valPMTResp.Error)
	}

	// 9c. Validate Formula with missing self.anchor_due_date
	valMissingBody, _ := json.Marshal(map[string]interface{}{
		"lua_script": "return self.anchor_due_date",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+budgetID+"/targets/validate-formula", bytes.NewReader(valMissingBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	var valMissingResp openapi.ValidateFormulaResponse
	json.Unmarshal(rec.Body.Bytes(), &valMissingResp)
	if valMissingResp.Valid {
		t.Fatalf("expected formula accessing missing anchor_due_date to be invalid, got valid=true")
	}
	if valMissingResp.Error == nil || !strings.Contains(*valMissingResp.Error, "anchor_due_date is not configured") {
		t.Fatalf("expected error mentioning anchor_due_date is not configured, got %v", valMissingResp.Error)
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

	// 11. Generate Formula with target context (due_date, target_name) for loan / PMT
	genLoanBody, _ := json.Marshal(map[string]interface{}{
		"current_bucket_name": "Housing",
		"target_name":        "Hipoteka",
		"due_date":           "2036-10-25",
		"target_currency":    "PLN",
		"user_prompt":        "Oblicz ratę kredytu 300000 zł przy oprocentowaniu 6% rocznie",
		"window_days":        30,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+budgetID+"/targets/generate-formula", bytes.NewReader(genLoanBody))
	req.Header.Set("Authorization", "Bearer "+tokResp.Token)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on generate-formula for loan, got %d: %s", rec.Code, rec.Body.String())
	}
	var genLoanResp openapi.GenerateFormulaResponse
	json.Unmarshal(rec.Body.Bytes(), &genLoanResp)
	if !strings.Contains(genLoanResp.LuaScript, "financial.pmt") {
		t.Fatalf("expected generated script to contain financial.pmt, got %q", genLoanResp.LuaScript)
	}
	if !strings.Contains(genLoanResp.LuaScript, "self.anchor_due_date") {
		t.Fatalf("expected generated script to contain self.anchor_due_date, got %q", genLoanResp.LuaScript)
	}
}

func TestAPI_RBAC_UnauthorizedAndForbidden(t *testing.T) {
	e, mainDB, _, appService, _ := setupTestAPIServer(t)

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
	u2ID, _ := appService.CreateUser(context.Background(), service.CreateUserInput{
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

	// Login Owner
	ownerLoginBody, _ := json.Marshal(map[string]interface{}{
		"email":    "owner@example.com",
		"password": "Password123!",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(ownerLoginBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	ownerCookie := rec.Header().Get("Set-Cookie")

	var ownerCSRF string
	mainDB.QueryRowContext(context.Background(), `SELECT csrf_token FROM sessions WHERE user_id = ?`, u1ID).Scan(&ownerCSRF)

	// Test CloneBudget endpoint with OWNER session
	cloneBody := bytes.NewReader([]byte(`{"new_name":"Cloned API Budget","include_transactions":false}`))
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+bID+"/clone", cloneBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", ownerCookie)
	req.Header.Set("X-CSRF-Token", ownerCSRF)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for cloneBudget, got %d: %s", rec.Code, rec.Body.String())
	}
	var cloneResp openapi.BudgetSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &cloneResp); err != nil {
		t.Fatalf("failed unmarshaling clone response: %v", err)
	}
	if cloneResp.Name != "Cloned API Budget" {
		t.Fatalf("expected name 'Cloned API Budget', got %s", cloneResp.Name)
	}

	// Test CloneBudget endpoint with outsider session -> 403 Forbidden
	var outsiderCSRF string
	mainDB.QueryRowContext(context.Background(), `SELECT csrf_token FROM sessions WHERE user_id = ?`, u2ID).Scan(&outsiderCSRF)

	outsiderCloneBody := bytes.NewReader([]byte(`{"new_name":"Outsider Clone"}`))
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+bID+"/clone", outsiderCloneBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", outsiderCSRF)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for outsider cloning budget, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAPI_Automations(t *testing.T) {
	e, mainDB, tenantMgr, _, _ := setupTestAPIServer(t)

	// 1. Register and Login user
	regBody, _ := json.Marshal(map[string]interface{}{
		"email":        "auto_user@example.com",
		"password":     "Password123!",
		"display_name": "Automation Tester",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	loginBody, _ := json.Marshal(map[string]interface{}{
		"email":    "auto_user@example.com",
		"password": "Password123!",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	cookie := rec.Header().Get("Set-Cookie")

	var uID, csrfToken string
	mainDB.QueryRowContext(context.Background(), `SELECT id FROM users WHERE email = ?`, "auto_user@example.com").Scan(&uID)
	mainDB.QueryRowContext(context.Background(), `SELECT csrf_token FROM sessions WHERE user_id = ?`, uID).Scan(&csrfToken)

	// Create budget
	bID := domain.NewUUIDv7()
	bUUID, _ := uuid.Parse(bID)
	tenantDB, err := tenantMgr.GetTenantDB(bID, true)
	if err != nil {
		t.Fatalf("failed to init tenant db: %v", err)
	}
	if _, err := mainDB.ExecContext(context.Background(), `INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at) VALUES (?, 'Auto Budget', 'PLN', 'Europe/Warsaw', ?, 1000, 1000)`, bID, uID); err != nil {
		t.Fatalf("failed to insert budget: %v", err)
	}
	if _, err := mainDB.ExecContext(context.Background(), `INSERT INTO budget_members (budget_id, user_id, role, joined_at) VALUES (?, ?, 'OWNER', 1000)`, bID, uID); err != nil {
		t.Fatalf("failed to insert budget member: %v", err)
	}

	// 2. Create Automation
	createPayload, _ := json.Marshal(map[string]interface{}{
		"name":            "Auto Uber Transport",
		"judge_script":    `text.contains(tx.notes, "Uber")`,
		"executor_script": `tx.set_bucket("Transport")`,
		"comment":         "First rule",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+bID+"/automations", bytes.NewReader(createPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}
	var createdAuto openapi.Automation
	json.Unmarshal(rec.Body.Bytes(), &createdAuto)
	if createdAuto.Name != "Auto Uber Transport" || createdAuto.Version != 1 {
		t.Fatalf("unexpected created automation: %+v", createdAuto)
	}

	// 3. List Automations
	req = httptest.NewRequest(http.MethodGet, "/api/v1/budgets/"+bID+"/automations", nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []openapi.Automation
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 automation, got %d", len(list))
	}

	// 4. Get Automation
	req = httptest.NewRequest(http.MethodGet, "/api/v1/budgets/"+bID+"/automations/"+createdAuto.Id.String(), nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5. Update Automation with squash
	updatePayload, _ := json.Marshal(map[string]interface{}{
		"name":            "Auto Uber Transport Updated",
		"judge_script":    `text.contains(tx.notes, "Uber") or tx.party == "Uber"`,
		"executor_script": `tx.set_bucket("Transport")`,
		"comment":         "Updated condition",
		"squash":          false,
	})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/budgets/"+bID+"/automations/"+createdAuto.Id.String(), bytes.NewReader(updatePayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for update, got %d: %s", rec.Code, rec.Body.String())
	}
	var updatedAuto openapi.Automation
	json.Unmarshal(rec.Body.Bytes(), &updatedAuto)
	if updatedAuto.Version != 2 {
		t.Fatalf("expected version 2, got %d", updatedAuto.Version)
	}

	// 5b. Update again to version 3
	updatePayload3, _ := json.Marshal(map[string]interface{}{
		"name":            "Auto Uber Transport v3",
		"judge_script":    `text.contains(tx.notes, "Uber")`,
		"executor_script": `tx.set_bucket("Transport v3")`,
		"comment":         "v3 update",
	})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/budgets/"+bID+"/automations/"+createdAuto.Id.String(), bytes.NewReader(updatePayload3))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for update v3, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5c. Get Automation Version 1
	req = httptest.NewRequest(http.MethodGet, "/api/v1/budgets/"+bID+"/automations/"+createdAuto.Id.String()+"/versions/1", nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get version 1, got %d: %s", rec.Code, rec.Body.String())
	}
	var v1Auto openapi.Automation
	json.Unmarshal(rec.Body.Bytes(), &v1Auto)
	if v1Auto.Version != 1 || v1Auto.Name != "Auto Uber Transport" {
		t.Fatalf("expected v1 auto, got version %d name %s", v1Auto.Version, v1Auto.Name)
	}

	// 5d. Squash versions 1..3
	squashPayload, _ := json.Marshal(map[string]interface{}{
		"start_version": 1,
		"end_version":   3,
		"comment":       "Squashed v1..v3 via API",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/budgets/"+bID+"/automations/"+createdAuto.Id.String()+"/squash", bytes.NewReader(squashPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for squash, got %d: %s", rec.Code, rec.Body.String())
	}
	var squashedAuto openapi.Automation
	json.Unmarshal(rec.Body.Bytes(), &squashedAuto)
	if squashedAuto.Version != 3 || squashedAuto.Comment == nil || *squashedAuto.Comment != "Squashed v1..v3 via API" {
		t.Fatalf("expected squashed version 3, got %+v", squashedAuto)
	}

	// Verify that version 1 is no longer found (deleted by squash)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/budgets/"+bID+"/automations/"+createdAuto.Id.String()+"/versions/1", nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 NotFound for squashed version 1, got %d", rec.Code)
	}

	// 6. Delete Automation
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/budgets/"+bID+"/automations/"+createdAuto.Id.String(), nil)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", csrfToken)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 NoContent, got %d: %s", rec.Code, rec.Body.String())
	}

	_ = bUUID
	_ = tenantDB
}
