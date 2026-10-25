package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestFormatCustomDateAndDateTime(t *testing.T) {
	tm := time.Date(2026, time.October, 25, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		dFormat  string
		tFormat  string
		wantDate string
		wantTime string
	}{
		{"YYYY-MM-DD", "24h", "2026-10-25", "2026-10-25 14:30"},
		{"DD.MM.YYYY", "24h", "25.10.2026", "25.10.2026 14:30"},
		{"DD/MM/YYYY", "12h", "25/10/2026", "25/10/2026 02:30 PM"},
		{"MM/DD/YYYY", "12h", "10/25/2026", "10/25/2026 02:30 PM"},
	}

	for _, tc := range tests {
		gotDate := FormatCustomDate(tm, tc.dFormat)
		if gotDate != tc.wantDate {
			t.Errorf("FormatCustomDate(%s) = %q, want %q", tc.dFormat, gotDate, tc.wantDate)
		}
		gotTime := FormatCustomDateTime(tm, tc.dFormat, tc.tFormat)
		if gotTime != tc.wantTime {
			t.Errorf("FormatCustomDateTime(%s, %s) = %q, want %q", tc.dFormat, tc.tFormat, gotTime, tc.wantTime)
		}
	}
}

func TestRenderTimeTag(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	c.Set(ContextDateFormatKey, "DD.MM.YYYY")
	c.Set(ContextTimeFormatKey, "12h")

	tm := time.Date(2026, 10, 25, 15, 45, 0, 0, time.UTC)
	tag := RenderTimeTag(tm, true, c)

	if !strings.Contains(tag, `datetime="2026-10-25T15:45:00Z"`) {
		t.Errorf("expected ISO timestamp in datetime attr, got: %s", tag)
	}
	if !strings.Contains(tag, `data-format-date="DD.MM.YYYY"`) {
		t.Errorf("expected data-format-date attr, got: %s", tag)
	}
	if !strings.Contains(tag, `data-format-time="12h"`) {
		t.Errorf("expected data-format-time attr, got: %s", tag)
	}
	if !strings.Contains(tag, `25.10.2026 03:45 PM`) {
		t.Errorf("expected pre-formatted text in time tag, got: %s", tag)
	}
}
