package handlers

import (
	"database/sql"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

// ProfileHandler handles user profile and display preference views and updates.
type ProfileHandler struct {
	mainDB *sql.DB
}

// NewProfileHandler creates a new ProfileHandler instance.
func NewProfileHandler(mainDB *sql.DB) *ProfileHandler {
	return &ProfileHandler{mainDB: mainDB}
}

// ProfileView renders the user profile settings page.
func (h *ProfileHandler) ProfileView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	userID, ok := c.Get(ContextUserIDKey).(string)
	if !ok || userID == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}

	var email, displayName, locale, dateFormat, timeFormat string
	var createdAt int64
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT email, display_name, locale, date_format, time_format, created_at
		FROM users WHERE id = ?`, userID).Scan(&email, &displayName, &locale, &dateFormat, &timeFormat, &createdAt)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "User profile not found")
	}

	if dateFormat == "" {
		dateFormat = "YYYY-MM-DD"
	}
	if timeFormat == "" {
		timeFormat = "24h"
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	sidebarHTML := ""
	bottomDockHTML := ""
	backLink := "/budgets"
	if budgetID != "" {
		sidebarHTML = RenderSidebarNavigation(budgetID, "profile")
		bottomDockHTML = RenderBottomDock(budgetID, "settings")
		backLink = fmt.Sprintf("/budgets/%s/settings", budgetID)
	}

	formAction := "/settings/profile"
	if budgetID != "" {
		formAction = fmt.Sprintf("/budgets/%s/settings/profile", budgetID)
	}

	dateOptions := []struct {
		val   string
		label string
	}{
		{"YYYY-MM-DD", "YYYY-MM-DD (e.g. 2026-10-25)"},
		{"DD.MM.YYYY", "DD.MM.YYYY (e.g. 25.10.2026)"},
		{"DD/MM/YYYY", "DD/MM/YYYY (e.g. 25/10/2026)"},
		{"MM/DD/YYYY", "MM/DD/YYYY (e.g. 10/25/2026)"},
	}

	var dateOptionsHTML strings.Builder
	for _, opt := range dateOptions {
		selected := ""
		if opt.val == dateFormat {
			selected = " selected"
		}
		dateOptionsHTML.WriteString(fmt.Sprintf(`<option value="%s"%s>%s</option>`, opt.val, selected, html.EscapeString(opt.label)))
	}

	timeOptions := []struct {
		val   string
		label string
	}{
		{"24h", "24 Hours (e.g. 14:30 / 14:30:00)"},
		{"12h", "12 Hours AM/PM (e.g. 02:30 PM / 02:30:00 PM)"},
	}

	var timeOptionsHTML strings.Builder
	for _, opt := range timeOptions {
		selected := ""
		if opt.val == timeFormat {
			selected = " selected"
		}
		timeOptionsHTML.WriteString(fmt.Sprintf(`<option value="%s"%s>%s</option>`, opt.val, selected, html.EscapeString(opt.label)))
	}

	successMsg := ""
	if c.QueryParam("saved") == "true" {
		successMsg = `
			<div class="alert alert-success alert-soft text-xs shadow-xs mb-4">
				<span>✓ Preferences updated successfully.</span>
			</div>`
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark" data-date-format="%s" data-time-format="%s">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <meta name="user-date-format" content="%s">
    <meta name="user-time-format" content="%s">
    <title>Balance — User Profile & Preferences</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col">
    <!-- Navbar -->
    <header class="navbar bg-base-100 border-b border-base-300 px-6 sticky top-0 z-30 shadow-xs">
        <div class="navbar-start gap-4 items-center">
            <a href="/budgets" class="text-2xl font-black tracking-tight text-primary flex items-center gap-2">
                <span>⚖️</span>
                <span>Balance</span>
            </a>
            <div class="badge badge-neutral font-semibold gap-1.5 py-3">
                <span class="w-2 h-2 rounded-full bg-success"></span>
                <span>User Profile</span>
            </div>
        </div>
        <div class="navbar-end gap-3">
            <a href="%s" class="btn btn-ghost btn-sm font-medium">← Back</a>
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm font-medium">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        %s

        <!-- Main Content -->
        <main class="flex-1 p-6 md:p-8 overflow-y-auto max-w-4xl">
            <div class="mb-6">
                <h1 class="text-2xl font-black tracking-tight">Profile & Preferences</h1>
                <p class="text-xs text-base-content/60 mt-1">Manage personal account details and regional date/time formatting preferences.</p>
            </div>

            %s

            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-body p-6">
                    <h2 class="card-title text-lg font-bold mb-4">Date & Time Personalization</h2>
                    <form method="POST" action="%s" class="flex flex-col gap-5">
                        <input type="hidden" name="csrf_token" value="%s" />

                        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
                            <div class="form-control">
                                <label class="label pb-1"><span class="label-text font-medium text-xs">Display Name</span></label>
                                <input type="text" name="display_name" value="%s" required class="input input-bordered w-full input-md focus:input-primary font-semibold" />
                            </div>
                            <div class="form-control">
                                <label class="label pb-1"><span class="label-text font-medium text-xs">Email Address</span></label>
                                <input type="email" value="%s" disabled class="input input-bordered w-full input-md bg-base-200 cursor-not-allowed font-mono text-xs" />
                            </div>
                        </div>

                        <div class="divider my-1"></div>

                        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
                            <div class="form-control">
                                <label class="label pb-1"><span class="label-text font-medium text-xs">Preferred Date Format</span></label>
                                <select name="date_format" id="pref_date_format" class="select select-bordered w-full select-md focus:select-primary" onchange="updateLivePreview()">
                                    %s
                                </select>
                            </div>
                            <div class="form-control">
                                <label class="label pb-1"><span class="label-text font-medium text-xs">Preferred Time Format</span></label>
                                <select name="time_format" id="pref_time_format" class="select select-bordered w-full select-md focus:select-primary" onchange="updateLivePreview()">
                                    %s
                                </select>
                            </div>
                        </div>

                        <!-- Live Preview Card -->
                        <div class="bg-base-200 rounded-xl p-4 border border-base-300 flex flex-col gap-2 mt-2">
                            <div class="text-xs font-bold text-base-content/70 uppercase tracking-wider">Live Preview</div>
                            <div class="flex flex-wrap items-center gap-6">
                                <div>
                                    <div class="text-xs text-base-content/50">Date Preview:</div>
                                    <div id="preview_date" class="font-mono text-sm font-semibold text-primary">...</div>
                                </div>
                                <div>
                                    <div class="text-xs text-base-content/50">Timestamp Preview:</div>
                                    <div id="preview_time" class="font-mono text-sm font-semibold text-secondary">...</div>
                                </div>
                            </div>
                        </div>

                        <div class="card-actions justify-end mt-4">
                            <button type="submit" class="btn btn-primary btn-sm shadow font-semibold">Save Preferences</button>
                        </div>
                    </form>
                </div>
            </div>
        </main>
    </div>

    %s

    <script>
    function updateLivePreview() {
        const dFormat = document.getElementById('pref_date_format').value;
        const tFormat = document.getElementById('pref_time_format').value;
        const now = new Date();

        const year = now.getFullYear();
        const month = String(now.getMonth() + 1).padStart(2, '0');
        const day = String(now.getDate()).padStart(2, '0');
        let hours = now.getHours();
        const minutes = String(now.getMinutes()).padStart(2, '0');
        const seconds = String(now.getSeconds()).padStart(2, '0');

        let dStr = '';
        switch(dFormat) {
            case 'DD.MM.YYYY': dStr = day + '.' + month + '.' + year; break;
            case 'DD/MM/YYYY': dStr = day + '/' + month + '/' + year; break;
            case 'MM/DD/YYYY': dStr = month + '/' + day + '/' + year; break;
            default: dStr = year + '-' + month + '-' + day;
        }

        let tStr = '';
        if (tFormat === '12h') {
            const ampm = hours >= 12 ? 'PM' : 'AM';
            const h12 = hours %% 12 || 12;
            const hStr = String(h12).padStart(2, '0');
            tStr = hStr + ':' + minutes + ':' + seconds + ' ' + ampm;
        } else {
            tStr = String(hours).padStart(2, '0') + ':' + minutes + ':' + seconds;
        }

        document.getElementById('preview_date').textContent = dStr;
        document.getElementById('preview_time').textContent = dStr + ' ' + tStr;
    }
    updateLivePreview();
    </script>
</body>
</html>`,
		html.EscapeString(dateFormat), html.EscapeString(timeFormat),
		html.EscapeString(dateFormat), html.EscapeString(timeFormat),
		html.EscapeString(backLink),
		html.EscapeString(csrfToken),
		formatSidebar(sidebarHTML),
		successMsg,
		html.EscapeString(formAction),
		html.EscapeString(csrfToken),
		html.EscapeString(displayName),
		html.EscapeString(email),
		dateOptionsHTML.String(),
		timeOptionsHTML.String(),
		bottomDockHTML,
	)

	return c.HTML(http.StatusOK, htmlContent)
}

func formatSidebar(sidebarHTML string) string {
	if sidebarHTML == "" {
		return ""
	}
	return fmt.Sprintf(`
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
            %s
        </aside>`, sidebarHTML)
}

// UpdateProfile processes user profile updates and format preferences.
func (h *ProfileHandler) UpdateProfile(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:profile")
	userID, ok := c.Get(ContextUserIDKey).(string)
	if !ok || userID == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}

	budgetID := c.Param("budgetId")
	displayName := strings.TrimSpace(c.FormValue("display_name"))
	dateFormat := strings.TrimSpace(c.FormValue("date_format"))
	timeFormat := strings.TrimSpace(c.FormValue("time_format"))

	if displayName == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Display name cannot be empty")
	}

	switch dateFormat {
	case "YYYY-MM-DD", "DD.MM.YYYY", "DD/MM/YYYY", "MM/DD/YYYY":
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid date format")
	}

	switch timeFormat {
	case "24h", "12h":
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid time format")
	}

	now := time.Now().UTC().UnixMicro()
	_, err := h.mainDB.ExecContext(ctx, `
		UPDATE users
		SET display_name = ?, date_format = ?, time_format = ?, updated_at = ?
		WHERE id = ?`, displayName, dateFormat, timeFormat, now, userID)
	if err != nil {
		logger.Error(ctx, "Failed updating user preferences", "user_id", userID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update profile preferences")
	}

	redirectURL := "/settings/profile?saved=true"
	if budgetID != "" {
		redirectURL = fmt.Sprintf("/budgets/%s/settings/profile?saved=true", budgetID)
	}

	return c.Redirect(http.StatusSeeOther, redirectURL)
}
