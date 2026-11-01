package templates

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestStaticPathHasContentHash(t *testing.T) {
	txJSPath := StaticPath("transactions/transactions.js")
	if !strings.HasPrefix(txJSPath, "/static/transactions/transactions.") || !strings.HasSuffix(txJSPath, ".js") {
		t.Fatalf("expected hashed static path for transactions.js, got %q", txJSPath)
	}
	// Example: /static/transactions/transactions.<12-hex>.js
	parts := strings.Split(strings.TrimPrefix(txJSPath, "/static/transactions/"), ".")
	if len(parts) != 3 || len(parts[1]) != 12 {
		t.Fatalf("expected 12-char hex hash in static path, got %q", txJSPath)
	}
}

func TestServeStaticAssetImmutableCacheAndETag(t *testing.T) {
	e := echo.New()
	RegisterStaticRoutes(e)

	hashedURL := StaticPath("transactions/transactions.js")
	req := httptest.NewRequest(http.MethodGet, hashedURL, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 for %s, got %d", hashedURL, rec.Code)
	}

	cc := rec.Header().Get("Cache-Control")
	if cc != "public, max-age=31536000, immutable" {
		t.Errorf("expected immutable Cache-Control header, got %q", cc)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/javascript") {
		t.Errorf("expected application/javascript Content-Type, got %q", ct)
	}

	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("expected non-empty ETag header")
	}

	// Verify 304 Not Modified on If-None-Match
	req304 := httptest.NewRequest(http.MethodGet, hashedURL, nil)
	req304.Header.Set("If-None-Match", etag)
	rec304 := httptest.NewRecorder()
	e.ServeHTTP(rec304, req304)

	if rec304.Code != http.StatusNotModified {
		t.Errorf("expected 304 Not Modified when If-None-Match matches ETag, got %d", rec304.Code)
	}
}

func TestBaseTemplateInheritanceIsolation(t *testing.T) {
	// Render transactions/view and transactions/csv_import to verify that
	// Twig-like {{block}} / {{define}} overrides ("title", "theme", "content", "navbar_end")
	// do not collide between different pages.
	txHTML, err := RenderString("transactions/view", map[string]any{
		"BudgetID":   "b-1",
		"BudgetName": "My Budget",
		"CSRFToken":  "csrf-1",
	})
	if err != nil {
		t.Fatalf("failed rendering transactions/view: %v", err)
	}
	if !strings.Contains(txHTML, "<title>Balance — Transactions") {
		t.Errorf("expected Transactions title in transactions/view, got:\n%s", txHTML)
	}
	if !strings.Contains(txHTML, `data-theme="dark"`) {
		t.Errorf("expected dark theme in transactions/view")
	}

	csvHTML, err := RenderString("transactions/csv_import", map[string]any{
		"BudgetID":   "b-1",
		"BudgetName": "My Budget",
		"CSRFToken":  "csrf-1",
	})
	if err != nil {
		t.Fatalf("failed rendering transactions/csv_import: %v", err)
	}
	if !strings.Contains(csvHTML, "<title>Import Bank Statement CSV - Balance</title>") {
		t.Errorf("expected CSV Import title in transactions/csv_import, got:\n%s", csvHTML)
	}
	if !strings.Contains(csvHTML, `data-theme="emerald"`) {
		t.Errorf("expected emerald theme override in transactions/csv_import")
	}
	if !strings.Contains(csvHTML, "← Back to Ledger") {
		t.Errorf("expected navbar_end block override in transactions/csv_import")
	}
}
