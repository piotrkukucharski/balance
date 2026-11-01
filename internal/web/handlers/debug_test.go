package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/gocardless"
	"github.com/labstack/echo/v4"
)

func setupTestDebugEnvironment(t *testing.T) (*DebugHandler, string, string, func()) {
	tmpDir, err := os.MkdirTemp("", "balance_debug_test_*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}

	mainDBPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		t.Fatalf("failed opening main db: %v", err)
	}

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatalf("failed applying main migrations: %v", err)
	}

	budgetsDir := filepath.Join(tmpDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		t.Fatalf("failed creating tenant manager: %v", err)
	}

	budgetID := domain.NewUUIDv7()
	userID := domain.NewUUIDv7()

	if _, err := mainDB.Exec(`INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		VALUES (?, 'owner@example.com', 'hash', 'Owner', 1000, 1000)`, userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	if _, err := mainDB.Exec(`INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, 'Debug Budget', 'PLN', 'Europe/Warsaw', ?, 1000, 1000)`, budgetID, userID); err != nil {
		t.Fatalf("insert budget: %v", err)
	}

	if _, err := mainDB.Exec(`INSERT INTO budget_members (budget_id, user_id, role, joined_at)
		VALUES (?, ?, 'OWNER', 1000)`, budgetID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}

	h := NewDebugHandler(mainDB, tenantMgr)

	cleanup := func() {
		tenantMgr.CloseAll()
		mainDB.Close()
		os.RemoveAll(tmpDir)
	}

	return h, budgetID, userID, cleanup
}

func TestDebugIntegrationAudit_AI(t *testing.T) {
	h, budgetID, userID, cleanup := setupTestDebugEnvironment(t)
	defer cleanup()

	e := echo.New()

	// 1. Missing GEMINI_API_KEY
	t.Setenv("GEMINI_API_KEY", "")
	req := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/debug/integrations/ai/audit", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("budgetId")
	c.SetParamValues(budgetID)
	c.Set(ContextUserIDKey, userID)
	c.Set(ContextRoleKey, "OWNER")

	if err := h.AuditAIAgentAction(c); err != nil {
		t.Fatalf("AuditAIAgentAction failed: %v", err)
	}

	var res IntegrationAuditResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res.Success {
		t.Fatalf("expected success=false when GEMINI_API_KEY is not set")
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", res.StatusCode)
	}

	// 2. Mock Gemini Server returning 200 OK
	mockGeminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [
				{
					"content": {
						"parts": [
							{"text": "4"}
						]
					}
				}
			]
		}`))
	}))
	defer mockGeminiServer.Close()

	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GEMINI_BASE_URL", mockGeminiServer.URL)

	req2 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/debug/integrations/ai/audit", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	c2.SetParamNames("budgetId")
	c2.SetParamValues(budgetID)
	c2.Set(ContextUserIDKey, userID)
	c2.Set(ContextRoleKey, "OWNER")

	if err := h.AuditAIAgentAction(c2); err != nil {
		t.Fatalf("AuditAIAgentAction failed: %v", err)
	}

	var res2 IntegrationAuditResult
	if err := json.Unmarshal(rec2.Body.Bytes(), &res2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !res2.Success {
		t.Fatalf("expected success=true, got false: %s", res2.Message)
	}
	if !strings.Contains(res2.Message, "4") {
		t.Fatalf("expected message to mention '4', got %s", res2.Message)
	}

	// 3. Mock Gemini Server returning 429 Quota Exceeded error
	mockErrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"Resource exhausted: quota exceeded","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer mockErrServer.Close()

	t.Setenv("GEMINI_BASE_URL", mockErrServer.URL)

	req3 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/debug/integrations/ai/audit", nil)
	rec3 := httptest.NewRecorder()
	c3 := e.NewContext(req3, rec3)
	c3.SetParamNames("budgetId")
	c3.SetParamValues(budgetID)
	c3.Set(ContextUserIDKey, userID)
	c3.Set(ContextRoleKey, "OWNER")

	if err := h.AuditAIAgentAction(c3); err != nil {
		t.Fatalf("AuditAIAgentAction failed: %v", err)
	}

	var res3 IntegrationAuditResult
	if err := json.Unmarshal(rec3.Body.Bytes(), &res3); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res3.Success {
		t.Fatalf("expected success=false for 429 response")
	}
	if res3.StatusCode != 429 {
		t.Fatalf("expected status 429, got %d", res3.StatusCode)
	}
	if !strings.Contains(res3.RawResponse, "RESOURCE_EXHAUSTED") {
		t.Fatalf("expected raw response preserved, got: %s", res3.RawResponse)
	}
}

func TestDebugIntegrationAudit_GoCardless(t *testing.T) {
	h, budgetID, userID, cleanup := setupTestDebugEnvironment(t)
	defer cleanup()

	e := echo.New()

	// 1. Missing credentials
	t.Setenv("GOCARDLESS_SECRET_ID", "")
	t.Setenv("GOCARDLESS_SECRET_KEY", "")

	req := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/debug/integrations/gocardless/audit", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("budgetId")
	c.SetParamValues(budgetID)
	c.Set(ContextUserIDKey, userID)
	c.Set(ContextRoleKey, "OWNER")

	if err := h.AuditGoCardlessAction(c); err != nil {
		t.Fatalf("AuditGoCardlessAction failed: %v", err)
	}

	var res IntegrationAuditResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res.Success {
		t.Fatalf("expected success=false without credentials")
	}

	// 2. Mock GoCardless Server
	mockGCServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/token/new") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"access":"mock_token","access_expires":86400}`))
			return
		}
		if strings.Contains(r.URL.Path, "/institutions") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"SANDBOXFINANCE_SFIN0000","name":"Sandbox Finance","bic":"SANDPLWA"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGCServer.Close()

	t.Setenv("GOCARDLESS_SECRET_ID", "sec_id")
	t.Setenv("GOCARDLESS_SECRET_KEY", "sec_key")
	t.Setenv("GOCARDLESS_BASE_URL", mockGCServer.URL)
	gcClient := gocardless.NewClient("sec_id", "sec_key", "http://localhost:8080/cb")
	h.SetGoCardlessClient(gcClient)

	req2 := httptest.NewRequest(http.MethodPost, "/budgets/"+budgetID+"/debug/integrations/gocardless/audit", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	c2.SetParamNames("budgetId")
	c2.SetParamValues(budgetID)
	c2.Set(ContextUserIDKey, userID)
	c2.Set(ContextRoleKey, "OWNER")

	if err := h.AuditGoCardlessAction(c2); err != nil {
		t.Fatalf("AuditGoCardlessAction failed: %v", err)
	}

	var res2 IntegrationAuditResult
	if err := json.Unmarshal(rec2.Body.Bytes(), &res2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !res2.Success {
		t.Fatalf("expected success=true, got false: %s", res2.Message)
	}
	if !strings.Contains(res2.Message, "1") {
		t.Fatalf("expected 1 institution, got message: %s", res2.Message)
	}
}
