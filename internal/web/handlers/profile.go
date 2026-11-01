package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/i18n"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/web/templates"
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

type profileSelectOption struct {
	Value    string
	Label    string
	Selected bool
}

type profileViewData struct {
	BudgetID                string
	BudgetName              string
	Locale                  string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	NumberFormat            string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	BackLink                string
	FormAction              string
	DisplayName             string
	Email                   string
	Saved                   bool
	LanguageOptions         []profileSelectOption
	DateOptions             []profileSelectOption
	TimeOptions             []profileSelectOption
	NumberOptions           []profileSelectOption
}

// ProfileView renders the user profile settings page.
func (h *ProfileHandler) ProfileView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	userID, ok := c.Get(ContextUserIDKey).(string)
	if !ok || userID == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}

	var email, displayName, locale, dateFormat, timeFormat, numberFormat string
	var createdAt int64
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT email, display_name, locale, date_format, time_format, number_format, created_at
		FROM users WHERE id = ?`, userID).Scan(&email, &displayName, &locale, &dateFormat, &timeFormat, &numberFormat, &createdAt)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "User profile not found")
	}

	if locale == "" {
		locale = i18n.DefaultLocale
	}
	locale = i18n.NormalizeLocale(locale)
	locDefaults := i18n.GetLocaleDefaults(locale)

	if dateFormat == "" {
		dateFormat = "auto"
	}
	if timeFormat == "" {
		timeFormat = "auto"
	}
	if numberFormat == "" {
		numberFormat = "auto"
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	sidebarHTML := ""
	bottomDockHTML := ""
	backLink := "/budgets"
	if budgetID != "" {
		sidebarHTML = RenderSidebarNavigation(budgetID, "profile", locale)
		bottomDockHTML = RenderBottomDock(budgetID, "settings", locale)
		backLink = fmt.Sprintf("/budgets/%s/settings", budgetID)
	}

	formAction := "/settings/profile"
	if budgetID != "" {
		formAction = fmt.Sprintf("/budgets/%s/settings/profile", budgetID)
	}

	allDefaults := i18n.AllLocaleDefaults()
	var languageOptions []profileSelectOption
	for _, tag := range i18n.SupportedLocales() {
		name := tag
		if d, ok := allDefaults[tag]; ok && d.DisplayName != "" {
			name = d.DisplayName
		}
		languageOptions = append(languageOptions, profileSelectOption{
			Value:    tag,
			Label:    name,
			Selected: locale == tag,
		})
	}

	dateOptions := []profileSelectOption{
		{"auto", fmt.Sprintf("%s (%s)", i18n.T(locale, "profile-option-auto-default"), locDefaults.DateFormat), dateFormat == "auto"},
		{"YYYY-MM-DD", "YYYY-MM-DD (e.g. 2026-10-25)", dateFormat == "YYYY-MM-DD"},
		{"DD.MM.YYYY", "DD.MM.YYYY (e.g. 25.10.2026)", dateFormat == "DD.MM.YYYY"},
		{"DD/MM/YYYY", "DD/MM/YYYY (e.g. 25/10/2026)", dateFormat == "DD/MM/YYYY"},
		{"MM/DD/YYYY", "MM/DD/YYYY (e.g. 10/25/2026)", dateFormat == "MM/DD/YYYY"},
	}

	timeOptions := []profileSelectOption{
		{"auto", fmt.Sprintf("%s (%s)", i18n.T(locale, "profile-option-auto-default"), locDefaults.TimeFormat), timeFormat == "auto"},
		{"24h", "24 Hours (e.g. 14:30 / 14:30:00)", timeFormat == "24h"},
		{"12h", "12 Hours AM/PM (e.g. 02:30 PM / 02:30:00 PM)", timeFormat == "12h"},
	}

	numberOptions := []profileSelectOption{
		{"auto", fmt.Sprintf("%s (%s)", i18n.T(locale, "profile-option-auto-default"), i18n.FormatMoneyMinor(100099, "", locale, "auto")), numberFormat == "auto"},
		{"1,000.99", "1,000.99", numberFormat == "1,000.99"},
		{"1.000,99", "1.000,99", numberFormat == "1.000,99"},
		{"1 000,99", "1 000,99", numberFormat == "1 000,99"},
		{"1 000.99", "1 000.99", numberFormat == "1 000.99"},
		{"1_000.99", "1_000.99", numberFormat == "1_000.99"},
		{"1000.99", "1000.99", numberFormat == "1000.99"},
	}

	data := profileViewData{
		BudgetID:       budgetID,
		BudgetName:     "User Profile",
		Locale:         locale,
		CSRFToken:      csrfToken,
		DateFormat:     dateFormat,
		TimeFormat:     timeFormat,
		NumberFormat:   numberFormat,
		SidebarHTML:    template.HTML(sidebarHTML),
		BottomDockHTML: template.HTML(bottomDockHTML),
		BackLink:       backLink,
		FormAction:     formAction,
		DisplayName:    displayName,
		Email:          email,
		Saved:          c.QueryParam("saved") == "true",
		LanguageOptions: languageOptions,
		DateOptions:    dateOptions,
		TimeOptions:    timeOptions,
		NumberOptions:  numberOptions,
	}

	htmlContent, err := templates.RenderString("profile/view", data)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering profile view: "+err.Error())
	}

	return c.HTML(http.StatusOK, htmlContent)
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
	locale := strings.TrimSpace(c.FormValue("locale"))
	dateFormat := strings.TrimSpace(c.FormValue("date_format"))
	timeFormat := strings.TrimSpace(c.FormValue("time_format"))
	numberFormat := strings.TrimSpace(c.FormValue("number_format"))

	if displayName == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Display name cannot be empty")
	}

	localeFound := false
	for _, tag := range i18n.SupportedLocales() {
		if tag == locale {
			localeFound = true
			break
		}
	}
	if !localeFound {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid locale")
	}

	switch dateFormat {
	case "auto", "YYYY-MM-DD", "DD.MM.YYYY", "DD/MM/YYYY", "MM/DD/YYYY":
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid date format")
	}

	switch timeFormat {
	case "auto", "24h", "12h":
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid time format")
	}

	switch numberFormat {
	case "auto", "1,000.99", "1.000,99", "1 000,99", "1 000.99", "1_000.99", "1000.99":
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid number format")
	}

	now := time.Now().UTC().UnixMicro()
	_, err := h.mainDB.ExecContext(ctx, `
		UPDATE users
		SET display_name = ?, locale = ?, date_format = ?, time_format = ?, number_format = ?, updated_at = ?
		WHERE id = ?`, displayName, locale, dateFormat, timeFormat, numberFormat, now, userID)
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
