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

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

func TestProfileViewAndUpdate(t *testing.T) {
	logger.BindTestLogger(t)

	tmpDir, err := os.MkdirTemp("", "balance_profile_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mainDB, err := db.OpenDB(filepath.Join(tmpDir, "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mainDB.Close()

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatal(err)
	}

	userID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, err = mainDB.Exec(`
		INSERT INTO users (id, email, password_hash, display_name, locale, date_format, time_format, number_format, created_at, updated_at)
		VALUES (?, 'user@test.com', 'dummyhash', 'Original Name', 'en-US', 'YYYY-MM-DD', '24h', 'auto', ?, ?)`, userID, now, now)
	if err != nil {
		t.Fatalf("failed inserting user: %v", err)
	}

	e := echo.New()
	handler := NewProfileHandler(mainDB)

	// 1. Test GET /settings/profile
	t.Run("GET_ProfileView", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/settings/profile", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(ContextUserIDKey, userID)
		c.Set("csrfToken", "test-csrf")

		if err := handler.ProfileView(c); err != nil {
			t.Fatalf("ProfileView failed: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Original Name") {
			t.Errorf("expected display name in profile view")
		}
		if !strings.Contains(body, `data-date-format="YYYY-MM-DD"`) {
			t.Errorf("expected data-date-format attribute on html")
		}
		if !strings.Contains(body, `data-time-format="24h"`) {
			t.Errorf("expected data-time-format attribute on html")
		}
		if !strings.Contains(body, "updateLivePreview()") {
			t.Errorf("expected live preview script in profile view")
		}
	})

	// 2. Test POST /settings/profile (update date and time preferences)
	t.Run("POST_UpdateProfile_Success", func(t *testing.T) {
		form := url.Values{}
		form.Set("csrf_token", "test-csrf")
		form.Set("display_name", "Updated Name")
		form.Set("locale", "pl-PL")
		form.Set("date_format", "DD.MM.YYYY")
		form.Set("time_format", "12h")
		form.Set("number_format", "1 000,99")

		req := httptest.NewRequest(http.MethodPost, "/settings/profile", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(ContextUserIDKey, userID)
		c.Set("csrfToken", "test-csrf")

		if err := handler.UpdateProfile(c); err != nil {
			t.Fatalf("UpdateProfile failed: %v", err)
		}
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 SeeOther redirect, got %d", rec.Code)
		}
		if rec.Header().Get("Location") != "/settings/profile?saved=true" {
			t.Fatalf("unexpected redirect: %s", rec.Header().Get("Location"))
		}

		// Verify database
		var dbName, dbLocale, dbDateFormat, dbTimeFormat, dbNumberFormat string
		err := mainDB.QueryRowContext(context.Background(), `
			SELECT display_name, locale, date_format, time_format, number_format FROM users WHERE id = ?`, userID).Scan(&dbName, &dbLocale, &dbDateFormat, &dbTimeFormat, &dbNumberFormat)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if dbName != "Updated Name" || dbLocale != "pl-PL" || dbDateFormat != "DD.MM.YYYY" || dbTimeFormat != "12h" || dbNumberFormat != "1 000,99" {
			t.Fatalf("database not updated properly: name=%s, loc=%s, df=%s, tf=%s, nf=%s", dbName, dbLocale, dbDateFormat, dbTimeFormat, dbNumberFormat)
		}
	})

	// 3. Test POST /settings/profile validation
	t.Run("POST_UpdateProfile_Validation", func(t *testing.T) {
		form := url.Values{}
		form.Set("display_name", "Updated Name")
		form.Set("date_format", "INVALID_FORMAT")
		form.Set("time_format", "24h")

		req := httptest.NewRequest(http.MethodPost, "/settings/profile", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(ContextUserIDKey, userID)

		err := handler.UpdateProfile(c)
		if err == nil {
			t.Fatalf("expected validation error on invalid date format")
		}
		he, ok := err.(*echo.HTTPError)
		if !ok || he.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %v", err)
		}
	})
}
