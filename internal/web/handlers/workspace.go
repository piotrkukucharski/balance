package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/i18n"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/service"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

type budgetCardViewData struct {
	ID          string
	Name        string
	EscapedName string
	Currency    string
	Timezone    string
	Role        string
	IsOwner     bool
}

type horizonOptionViewData struct {
	ID           string
	Name         string
	Days         int
	IsSelected   bool
	TypeBadge    string
	IsDefault    bool
	IsSystem     bool
	IsHidden     bool
	CanDelete    bool
	OptionLabel  string
	ToolbarLabel string
}

type horizonMemberAffectedViewData struct {
	UserID      string
	DisplayName string
	Email       string
}

type budgetsListViewData struct {
	Locale                  string
	NumberFormat            string
	DateFormat              string
	TimeFormat              string
	BudgetName              string
	CSRFToken               string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	Budgets                 []budgetCardViewData
	HasOwnerBudget          bool
}

type settingsPageViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	DefaultCurrency         string
	Timezone                string
	Role                    string
	IsOwner                 bool
	DateFormat              string
	TimeFormat              string
	CSRFToken               string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	UserDefaultHorizonID    string
	UserFirstDayOfWeek      string
	Horizons                []horizonOptionViewData
	VisibleHorizons         []horizonOptionViewData
}

type WorkspaceHandler struct {
	mainDB         *sql.DB
	tenantMgr      *db.TenantDBManager
	reportsHandler *ReportsHandler
	appService     *service.AppService
	structureAI    ai.BudgetStructureAIProvider
}

func NewWorkspaceHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager, reportsHandler *ReportsHandler) *WorkspaceHandler {
	return &WorkspaceHandler{
		mainDB:         mainDB,
		tenantMgr:      tenantMgr,
		reportsHandler: reportsHandler,
		structureAI:    ai.NewBudgetStructureProvider(),
	}
}

func (h *WorkspaceHandler) SetService(svc *service.AppService) {
	h.appService = svc
}

func (h *WorkspaceHandler) SetStructureAIProvider(p ai.BudgetStructureAIProvider) {
	h.structureAI = p
}

func (h *WorkspaceHandler) now(loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	if h.appService != nil && h.appService.Clock() != nil {
		return h.appService.Clock().Now().In(loc)
	}
	return time.Now().In(loc)
}

// GetAvailableCurrencies returns a unique sorted list of currencies available for a budget.
func (h *WorkspaceHandler) GetAvailableCurrencies(ctx context.Context, tenantDB *sql.DB, defaultCurrency string) ([]string, error) {
	return budget.GetAvailableCurrencies(ctx, tenantDB, defaultCurrency)
}

// BudgetsList renders the post-login budget selection dashboard with DaisyUI 5.
func (h *WorkspaceHandler) BudgetsList(c echo.Context) error {
	userID := c.Get(ContextUserIDKey).(string)

	rows, err := h.mainDB.QueryContext(c.Request().Context(), `
		SELECT b.id, b.name, b.default_currency, b.timezone, bm.role
		FROM budgets b
		JOIN budget_members bm ON b.id = bm.budget_id
		WHERE bm.user_id = ?
		ORDER BY b.created_at DESC`, userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load budgets")
	}
	defer rows.Close()

	var budgets []budgetCardViewData
	hasOwnerBudget := false
	for rows.Next() {
		var id, name, curr, tz, role string
		rows.Scan(&id, &name, &curr, &tz, &role)
		isOwner := role == "OWNER"
		if isOwner {
			hasOwnerBudget = true
		}
		budgets = append(budgets, budgetCardViewData{
			ID:          id,
			Name:        name,
			EscapedName: strings.ReplaceAll(name, "'", "\\'"),
			Currency:    curr,
			Timezone:    tz,
			Role:        role,
			IsOwner:     isOwner,
		})
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	htmlContent, err := templates.RenderString("workspace/budgets_list", budgetsListViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		CSRFToken:               csrfToken,
		Budgets:                 budgets,
		HasOwnerBudget:          hasOwnerBudget,
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering budgets list")
	}

	return c.HTML(http.StatusOK, htmlContent)
}

func formatHorizonLabels(h budget.BudgetHorizon, locale string, now time.Time, loc *time.Location) (name, toolbarLabel, optionLabel string) {
	switch h.HorizonType {
	case budget.HorizonTypeMonthly:
		name = i18n.T(locale, "horizon-name-monthly")
		toolbarLabel = name
		optionLabel = fmt.Sprintf("%s (%s)", name, i18n.T(locale, "settings-horizons-type-monthly"))
	case budget.HorizonTypeAnnual:
		name = i18n.T(locale, "horizon-name-annual")
		toolbarLabel = name
		optionLabel = fmt.Sprintf("%s (%s)", name, i18n.T(locale, "settings-horizons-type-annual"))
	case budget.HorizonTypeRollingDays:
		days := 30
		if h.DaysValue != nil {
			days = *h.DaysValue
		}
		name = fmt.Sprintf("%dd", days)
		toolbarLabel = i18n.T(locale, "horizon-name-rolling", "days", days)
		optionLabel = fmt.Sprintf("%s (%s)", toolbarLabel, i18n.T(locale, "settings-horizons-type-rolling"))
	case budget.HorizonTypeCalendarDayOfMonth:
		day := 10
		if h.DaysValue != nil {
			day = *h.DaysValue
		}
		name = fmt.Sprintf("day-%d", day)
		toolbarLabel = i18n.T(locale, "horizon-name-calendar-toolbar", "day", day)
		optionLabel = i18n.T(locale, "horizon-name-calendar-full", "day", day)
	default:
		name = h.ID
		toolbarLabel = h.ID
		optionLabel = h.ID
	}
	return
}
func (h *WorkspaceHandler) SettingsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)

	var name, defaultCurrency, timezone string
	var createdAt, updatedAt int64
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT name, default_currency, timezone, created_at, updated_at
		FROM budgets WHERE id = ?`, budgetID).Scan(&name, &defaultCurrency, &timezone, &createdAt, &updatedAt)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	userID, _ := c.Get(ContextUserIDKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	sidebarHTML := RenderSidebarNavigation(budgetID, "settings", userLocale)
	bottomDockHTML := RenderBottomDock(budgetID, "settings", userLocale)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	loc := time.UTC
	if timezone != "" {
		if l, err := time.LoadLocation(timezone); err == nil {
			loc = l
		}
	}
	nowInTz := h.now(loc)

	// Fetch user default horizon and all horizons
	userDefaultHorizon, _ := budget.GetUserDefaultHorizon(c.Request().Context(), tenantDB, userID)
	locDefaults := i18n.GetLocaleDefaults(userLocale)
	userFirstDayOfWeek, _ := budget.GetUserFirstDayOfWeek(c.Request().Context(), tenantDB, userID, locDefaults.FirstDayOfWeek)
	allHorizons, _ := budget.ListBudgetHorizons(c.Request().Context(), tenantDB, false)

	var horizonsViewData []horizonOptionViewData
	var visibleHorizonsViewData []horizonOptionViewData

	for _, bh := range allHorizons {
		hName, tLabel, optLabel := formatHorizonLabels(bh, userLocale, nowInTz, loc)
		_, wDays, _ := budget.ParseHorizonIdentifier(bh.ID, nowInTz, loc)
		typeBadge := "badge-info"
		if bh.HorizonType == budget.HorizonTypeMonthly || bh.HorizonType == budget.HorizonTypeAnnual {
			typeBadge = "badge-neutral"
		} else if bh.HorizonType == budget.HorizonTypeCalendarDayOfMonth {
			typeBadge = "badge-accent"
		}
		item := horizonOptionViewData{
			ID:           bh.ID,
			Name:         hName,
			Days:         wDays,
			IsSelected:   bh.ID == userDefaultHorizon,
			TypeBadge:    typeBadge,
			IsDefault:    bh.ID == userDefaultHorizon,
			IsSystem:     bh.IsSystem,
			IsHidden:     bh.IsHidden,
			CanDelete:    !bh.IsSystem,
			OptionLabel:  optLabel,
			ToolbarLabel: tLabel,
		}
		horizonsViewData = append(horizonsViewData, item)
		if !bh.IsHidden {
			visibleHorizonsViewData = append(visibleHorizonsViewData, item)
		}
	}

	htmlContent, err := templates.RenderString("workspace/settings", settingsPageViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		BudgetName:              name,
		DefaultCurrency:         defaultCurrency,
		Timezone:                timezone,
		Role:                    role,
		IsOwner:                 role == "OWNER",
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		CSRFToken:               csrfToken,
		SidebarHTML:             template.HTML(sidebarHTML),
		BottomDockHTML:          template.HTML(bottomDockHTML),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
		UserDefaultHorizonID:    userDefaultHorizon,
		UserFirstDayOfWeek:      userFirstDayOfWeek,
		Horizons:                horizonsViewData,
		VisibleHorizons:         visibleHorizonsViewData,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering budget settings")
	}

	return c.HTML(http.StatusOK, htmlContent)
}

// SetUserFirstDayOfWeekHandler updates current user's preferred first day of week preference.
func (h *WorkspaceHandler) SetUserFirstDayOfWeekHandler(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)

	day := strings.TrimSpace(c.FormValue("first_day_of_week"))
	if day == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "first_day_of_week is required")
	}

	if err := budget.SetUserFirstDayOfWeek(c.Request().Context(), tenantDB, userID, day); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings", budgetID))
}

// SetUserDefaultHorizonHandler updates current user's default horizon preference.
func (h *WorkspaceHandler) SetUserDefaultHorizonHandler(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)

	horizonID := strings.TrimSpace(c.FormValue("default_horizon_id"))
	if horizonID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "default_horizon_id is required")
	}

	if err := budget.SetUserDefaultHorizon(c.Request().Context(), tenantDB, userID, horizonID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings", budgetID))
}

// CreateHorizonHandler adds a new rolling or calendar horizon (OWNER required).
func (h *WorkspaceHandler) CreateHorizonHandler(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Only budget OWNER can create time horizons")
	}
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	hType := budget.HorizonType(strings.TrimSpace(c.FormValue("horizon_type")))
	daysVal, _ := strconv.Atoi(strings.TrimSpace(c.FormValue("days_value")))

	_, err := budget.CreateCustomHorizon(c.Request().Context(), tenantDB, budget.CreateCustomHorizonInput{
		HorizonType: hType,
		DaysValue:   daysVal,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings", budgetID))
}

// ToggleHorizonHiddenHandler hides or shows a time horizon (OWNER required).
func (h *WorkspaceHandler) ToggleHorizonHiddenHandler(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Only budget OWNER can modify time horizons")
	}
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	horizonID := strings.TrimSpace(c.FormValue("horizon_id"))
	action := strings.TrimSpace(c.FormValue("action")) // "hide" or "show"
	fallbackID := strings.TrimSpace(c.FormValue("fallback_horizon_id"))

	hide := action == "hide"
	err := budget.SetHorizonHidden(c.Request().Context(), tenantDB, budget.UpdateHorizonVisibilityInput{
		HorizonID:        horizonID,
		Hide:             hide,
		FallbackTargetID: fallbackID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings", budgetID))
}

// DeleteHorizonHandler deletes a custom time horizon (OWNER required).
func (h *WorkspaceHandler) DeleteHorizonHandler(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Only budget OWNER can delete time horizons")
	}
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	horizonID := strings.TrimSpace(c.FormValue("horizon_id"))
	fallbackID := strings.TrimSpace(c.FormValue("fallback_horizon_id"))

	err := budget.DeleteCustomHorizon(c.Request().Context(), tenantDB, horizonID, fallbackID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings", budgetID))
}

// CheckHorizonAffectedUsers returns the list of users who have horizonID set as default.
func (h *WorkspaceHandler) CheckHorizonAffectedUsers(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	horizonID := strings.TrimSpace(c.QueryParam("horizon_id"))

	users, err := budget.FindUsersWithDefaultHorizon(c.Request().Context(), tenantDB, h.mainDB, budgetID, horizonID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, map[string]any{
		"horizon_id": horizonID,
		"count":      len(users),
		"users":      users,
	})
}

// UpdateBudget updates the budget name and settings in main.db (requires OWNER).
func (h *WorkspaceHandler) UpdateBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")
	budgetID := c.Param("budgetId")
	name := strings.TrimSpace(c.FormValue("name"))
	tz := strings.TrimSpace(c.FormValue("timezone"))

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Budget name cannot be empty")
	}

	now := time.Now().UTC().UnixMicro()
	var res sql.Result
	var err error
	if tz != "" {
		res, err = h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE budgets SET name = ?, timezone = ?, updated_at = ?
			WHERE id = ?`, name, tz, now, budgetID)
	} else {
		res, err = h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE budgets SET name = ?, updated_at = ?
			WHERE id = ?`, name, now, budgetID)
	}
	if err != nil {
		logger.Error(ctx, "Failed to update budget settings", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Database error")
	}

	rowsAff, err := res.RowsAffected()
	if err != nil || rowsAff == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
	}

	logger.Info(ctx, "Budget updated successfully", "budget_id", budgetID, "new_name", name)
	if strings.Contains(c.Request().URL.Path, "/settings") {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings", budgetID))
	}
	return c.Redirect(http.StatusSeeOther, "/budgets")
}

// CloneBudget clones an existing budget tenant and its resources based on checklist (requires OWNER).
func (h *WorkspaceHandler) CloneBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")
	budgetID := c.Param("budgetId")
	name := strings.TrimSpace(c.FormValue("name"))

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Budget name cannot be empty")
	}

	userID := c.Get(ContextUserIDKey).(string)

	includeMembers := c.FormValue("include_members") == "1" || c.FormValue("include_members") == "true" || c.FormValue("include_members") == "on"
	includeAccounts := c.FormValue("include_accounts") == "1" || c.FormValue("include_accounts") == "true" || c.FormValue("include_accounts") == "on"
	includeBuckets := c.FormValue("include_buckets") == "1" || c.FormValue("include_buckets") == "true" || c.FormValue("include_buckets") == "on"
	includeTargets := c.FormValue("include_targets") == "1" || c.FormValue("include_targets") == "true" || c.FormValue("include_targets") == "on"
	includeTransactions := c.FormValue("include_transactions") == "1" || c.FormValue("include_transactions") == "true" || c.FormValue("include_transactions") == "on"

	correlationID := c.Response().Header().Get("X-Correlation-ID")

	if h.appService != nil {
		newBudgetID, err := h.appService.CloneBudget(c.Request().Context(), service.CloneBudgetInput{
			SourceBudgetID:      budgetID,
			NewName:             name,
			RequestingUserID:    userID,
			IncludeMembers:      includeMembers,
			IncludeAccounts:     includeAccounts,
			IncludeBuckets:      includeBuckets,
			IncludeTargets:      includeTargets,
			IncludeTransactions: includeTransactions,
			CorrelationID:       correlationID,
		})
		if err != nil {
			if errors.Is(err, service.ErrInvalidInput) {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			if errors.Is(err, service.ErrUnauthorized) {
				return echo.NewHTTPError(http.StatusForbidden, err.Error())
			}
			if errors.Is(err, service.ErrBudgetNotFound) {
				return echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
			logger.Error(ctx, "Failed cloning budget via service", "source_budget_id", budgetID, "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to clone budget")
		}

		logger.Info(ctx, "Budget cloned successfully", "source_budget_id", budgetID, "new_budget_id", newBudgetID, "name", name)
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", newBudgetID))
	}

	return echo.NewHTTPError(http.StatusInternalServerError, "Service unavailable")
}

// DeleteBudget deletes the budget and its memberships from main.db, and deletes its tenant sqlite database (requires OWNER).
func (h *WorkspaceHandler) DeleteBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")
	budgetID := c.Param("budgetId")

	if h.appService != nil {
		if err := h.appService.RemoveBudget(c.Request().Context(), budgetID); err != nil {
			if errors.Is(err, service.ErrBudgetNotFound) {
				return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
			}
			logger.Error(ctx, "Failed deleting budget via service", "budget_id", budgetID, "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete budget")
		}
		logger.Info(ctx, "Budget deleted successfully", "budget_id", budgetID)
		return c.Redirect(http.StatusSeeOther, "/budgets")
	}

	tx, err := h.mainDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Failed to begin transaction for budget deletion", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Database error")
	}
	defer tx.Rollback()

	// Clean up related records in main.db
	_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM budget_invitations WHERE budget_id = ?`, budgetID)
	_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM budget_members WHERE budget_id = ?`, budgetID)
	res, err := tx.ExecContext(c.Request().Context(), `DELETE FROM budgets WHERE id = ?`, budgetID)
	if err != nil {
		logger.Error(ctx, "Failed to delete budget record", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete budget")
	}

	rowsAff, err := res.RowsAffected()
	if err != nil || rowsAff == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Failed committing budget deletion", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	// Delete tenant SQLite database file and evict from LRU cache
	if err := h.tenantMgr.DeleteTenantDB(budgetID); err != nil {
		logger.Warn(ctx, "Failed deleting tenant db file from disk", "budget_id", budgetID, "error", err.Error())
	}

	logger.Info(ctx, "Budget deleted successfully", "budget_id", budgetID)
	return c.Redirect(http.StatusSeeOther, "/budgets")
}


// CreateBudget creates a new budget in main.db and provisions the tenant SQLite database.
func (h *WorkspaceHandler) CreateBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")

	userID := c.Get(ContextUserIDKey).(string)
	name := c.FormValue("name")
	curr := c.FormValue("default_currency")
	tz := c.FormValue("timezone")
	defHorizon := c.FormValue("default_horizon")

	if name == "" || curr == "" {
		logger.Warn(ctx, "Budget creation failed validation: name and currency required", "user_id", userID)
		return echo.NewHTTPError(http.StatusBadRequest, "Budget name and currency are required")
	}
	if tz == "" {
		tz = "UTC"
	}
	if defHorizon == "" {
		defHorizon = "monthly"
	}

	budgetID := domain.NewUUIDv7()

	userLocale := GetUserLocale(c)
	locDefaults := i18n.GetLocaleDefaults(userLocale)

	if h.appService != nil {
		id, err := h.appService.CreateBudget(c.Request().Context(), service.CreateBudgetInput{
			ID:              budgetID,
			Name:            name,
			OwnerUserID:     userID,
			DefaultCurrency: curr,
			Timezone:        tz,
			DefaultHorizon:  defHorizon,
			FirstDayOfWeek:  locDefaults.FirstDayOfWeek,
		})
		if err != nil {
			logger.Error(ctx, "Failed creating budget via service", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create budget")
		}

		creationMode := c.FormValue("creation_mode")
		aiPrompt := strings.TrimSpace(c.FormValue("ai_prompt"))
		sourceBudgetID := strings.TrimSpace(c.FormValue("source_budget_id"))
		correlationID := c.Response().Header().Get("X-Correlation-ID")

		if creationMode == "ai" && aiPrompt != "" {
			if h.structureAI == nil {
				h.structureAI = ai.NewBudgetStructureProvider()
			}
			structRes, err := h.structureAI.GenerateBudgetStructure(c.Request().Context(), ai.BudgetStructurePromptRequest{
				Prompt:          aiPrompt,
				DefaultCurrency: curr,
			})
			if err != nil {
				logger.Error(ctx, "Failed generating budget structure during budget creation", "budget_id", id, "error", err.Error())
			} else if structRes != nil {
				bCount, tCount, errApply := h.appService.ApplyBudgetStructure(c.Request().Context(), id, structRes, userID, correlationID)
				if errApply != nil {
					logger.Error(ctx, "Failed applying generated budget structure during budget creation", "budget_id", id, "error", errApply.Error())
				} else {
					logger.Info(ctx, "Applied generated budget structure to new budget", "budget_id", id, "buckets", bCount, "targets", tCount)
				}
			}
		} else if creationMode == "copy" && sourceBudgetID != "" {
			exported, err := h.appService.ExportBudgetStructure(c.Request().Context(), sourceBudgetID)
			if err != nil {
				logger.Error(ctx, "Failed exporting structure from source budget during budget creation", "source_budget_id", sourceBudgetID, "error", err.Error())
			} else if exported != nil {
				bCount, tCount, errApply := h.appService.ApplyBudgetStructure(c.Request().Context(), id, exported, userID, correlationID)
				if errApply != nil {
					logger.Error(ctx, "Failed applying copied budget structure during budget creation", "budget_id", id, "error", errApply.Error())
				} else {
					logger.Info(ctx, "Applied copied budget structure to new budget", "budget_id", id, "buckets", bCount, "targets", tCount)
				}
			}
		}

		logger.Info(ctx, "New budget provisioned successfully", "budget_id", id, "name", name, "currency", curr, "timezone", tz, "owner_id", userID)
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", id))
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := h.mainDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Failed starting transaction for budget creation", "user_id", userID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Database error")
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		budgetID, name, curr, tz, userID, now, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed to insert budget into main.db", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create budget")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO budget_members (budget_id, user_id, role, joined_at)
		VALUES (?, ?, 'OWNER', ?)`,
		budgetID, userID, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed to assign budget owner", "budget_id", budgetID, "user_id", userID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to assign budget owner")
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Failed committing budget creation transaction", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	// Provision tenant DB
	tDB, err := h.tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		logger.Error(ctx, "Failed initializing tenant database for budget", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to initialize tenant database")
	}
	_ = budget.SetUserDefaultHorizon(c.Request().Context(), tDB, userID, defHorizon)
	if locDefaults.FirstDayOfWeek != "" && budget.IsValidFirstDayOfWeek(locDefaults.FirstDayOfWeek) {
		_ = budget.SetUserFirstDayOfWeek(c.Request().Context(), tDB, userID, locDefaults.FirstDayOfWeek)
	}

	logger.Info(ctx, "New budget provisioned successfully", "budget_id", budgetID, "name", name, "currency", curr, "timezone", tz, "owner_id", userID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

type bucketCurrencyItemViewData struct {
	Currency        string `json:"Currency"`
	BalanceMinor    int64  `json:"BalanceMinor"`
	BalanceFormatted string `json:"BalanceFormatted"`
	CoverDeficitMinor int64 `json:"CoverDeficitMinor"` // positive minor amount to cover
	CoverDeficitFormatted string `json:"CoverDeficitFormatted"`
	CanCover        bool   `json:"CanCover"`
	RTASurplusFormatted string `json:"RTASurplusFormatted"`
}

type bucketViewData struct {
	ID                        string           `json:"ID"`
	GroupName                 string           `json:"GroupName"`
	GroupID                   string           `json:"-"`
	EscapedName               string           `json:"-"`
	EscapedGroupName          string           `json:"-"`
	Name                      string           `json:"Name"`
	BalanceMinor              int64            `json:"BalanceMinor"`
	BalanceFormatted          string           `json:"-"`
	Currency                  string           `json:"Currency"`
	TotalRequiredMinor        int64            `json:"TotalRequiredMinor"`
	TotalRequiredFormatted    string           `json:"-"`
	TotalFundedMinor          int64            `json:"TotalFundedMinor"`
	TotalFundedFormatted      string           `json:"-"`
	TotalNeededMinor          int64            `json:"TotalNeededMinor"`
	TotalNeededFormatted      string           `json:"-"`
	UnassignedBufferMinor     int64            `json:"UnassignedBufferMinor"`
	UnassignedBufferFormatted string           `json:"-"`
	BufferMobileStr           string           `json:"-"`
	HealthStatus              string           `json:"HealthStatus"`
	HealthBadge               string           `json:"HealthBadge"`
	HealthBadgeClass          string           `json:"HealthBadgeClass"`
	StatusDot                 string           `json:"-"`
	ProgressClass             string           `json:"-"`
	AvailColor                string           `json:"-"`
	NeededColor               string           `json:"-"`
	SearchTokens              string           `json:"-"`
	ActionMenuDesktop         template.HTML    `json:"-"`
	ActionMenuMobile          template.HTML    `json:"-"`
	HasP3AtRisk               bool             `json:"HasP3AtRisk"`
	HasFormulaError           bool             `json:"HasFormulaError"`
	FormulaErrorMessage       string           `json:"FormulaErrorMessage"`
	DefaultExpanded           bool             `json:"DefaultExpanded"`
	ProgressPercent           int              `json:"ProgressPercent"`
	Targets                   []targetViewData `json:"Targets"`
	HasOverspentDeficit       bool             `json:"HasOverspentDeficit"`
	OverspentCurrencies       []bucketCurrencyItemViewData `json:"OverspentCurrencies"`
}

type targetViewData struct {
	ID                  string  `json:"ID"`
	BucketID            string  `json:"BucketID"`
	Name                string  `json:"Name"`
	HasLabel            bool    `json:"-"`
	MobileNameDisplay   string  `json:"-"`
	TargetType          string  `json:"TargetType"`
	Priority            int     `json:"Priority"`
	PriorityText        string  `json:"-"`
	PrioBadgeClass      string  `json:"-"`
	SortOrder           int     `json:"SortOrder"`
	AmountMode          string  `json:"AmountMode"`
	AmountMinor         int64   `json:"AmountMinor"`
	LuaScript           *string `json:"LuaScript"`
	LastFormulaError    *string `json:"LastFormulaError"`
	UsesRuntime         int     `json:"UsesRuntime"`
	Currency            string  `json:"Currency"`
	RecurrenceCadence     string  `json:"RecurrenceCadence"`
	RecurrenceInterval    int     `json:"RecurrenceInterval"`
	RecurrenceDays        int     `json:"RecurrenceDays"`
	RecurrenceDayOfWeek   *int    `json:"RecurrenceDayOfWeek"`
	RecurrenceDayOfMonth  *int    `json:"RecurrenceDayOfMonth"`
	RecurrenceMonthOfYear *int    `json:"RecurrenceMonthOfYear"`
	CadenceDisplay        string  `json:"-"`
	DueDate             int64   `json:"DueDate"`
	DueText             string  `json:"-"`
	GoalText            string  `json:"-"`
	NonSpendingStrategy string  `json:"NonSpendingStrategy"`
	CeilingAmountMinor  *int64  `json:"CeilingAmountMinor"`
	CurrentFundedMinor  int64   `json:"CurrentFundedMinor"`
	FundedFormatted     string  `json:"-"`
	RequiredMinor       int64   `json:"RequiredMinor"`
	RequiredFormatted   string  `json:"-"`
	NeededMinor         int64   `json:"NeededMinor"`
	NeededFormatted     string  `json:"-"`
	ProgressPercent     int     `json:"ProgressPercent"`
	ProgressClass       string  `json:"-"`
	HealthBadge         string  `json:"HealthBadge"`
	HealthBadgeClass    string  `json:"HealthBadgeClass"`
}

type bucketGroupViewData struct {
	GroupName               string
	EscapedGroupName        string
	GroupID                 string
	Buckets                 []bucketViewData
	TotalBucketsCount       int
	TotalAvailableMinor     int64
	TotalAvailableFormatted string
	TotalRequiredMinor      int64
	TotalRequiredFormatted  string
	TotalFundedMinor        int64
	TotalFundedFormatted    string
	TotalNeededMinor        int64
	TotalNeededFormatted    string
	ProgressPercent         int
	ProgressClass           string
}

type accountSummaryItem struct {
	Name             string
	Currency         string
	BalanceFormatted string
	IsURLIcon        bool
	IconValue        string
}

type rtaCurrencyBadgeViewData struct {
	Currency        string
	AmountFormatted string
	BadgeColor      string
	IsWarning       bool
	WarningTooltip  string
}

type overspentSummaryItemViewData struct {
	Currency        string
	DeficitFormatted string
}

type otherBudgetOptionViewData struct {
	ID       string
	Name     string
	Currency string
}

type budgetPageViewData struct {
	Locale                       string
	NumberFormat                 string
	BudgetID                     string
	BudgetName                   string
	Role                         string
	DefaultCurr                  string
	HorizonParam                 string
	HorizonTitle                 string
	HorizonDisplay               string
	IsHorizonMonthly             bool
	WindowDays                   int
	AvailableHorizons            []horizonOptionViewData
	FilterParam                  string
	IsFilterAll                  bool
	FlashMsg                     string
	AOMHeadline                  int
	DateFormat                   string
	TimeFormat                   string
	CSRFToken                    string
	SidebarHTML                  template.HTML
	BottomDockHTML               template.HTML
	DateTimeFormatterScript      template.HTML
	HasAccounts                  bool
	AccountSummaries             []accountSummaryItem
	RTAColor                     string
	RTADescClass                 string
	RTADesc                      string
	DefaultRTAFormatted          string
	DefaultRTAHasDeficitWarning  bool
	DefaultRTAWarningTooltip     string
	HasNonDefaultRTACurrencies   bool
	TopRTACurrencies             []rtaCurrencyBadgeViewData
	ExtraRTACurrencies           []rtaCurrencyBadgeViewData
	HasOverspentAlert            bool
	OverspentAlertCurrencies     []overspentSummaryItemViewData
	OverspentFilterCurrencies    []string
	TotalNeededFormatted         string
	HasMandatoryP3Needed         bool
	MandatoryP3NeededFormatted   string
	CountAll                     int
	CountAttention               int
	CountOverspent               int
	CountBuffer                  int
	AllBucketsCount              int
	DisplayBucketsCount          int
	BucketGroups                 []bucketGroupViewData
	AllBuckets                   []bucketViewData
	DistinctGroups               []string
	NewBucketGroupPlaceholder    string
	HasOtherBudgets              bool
	OtherBudgets                 []otherBudgetOptionViewData
	AvailableCurrencies          []string
	AccountCurrencies            []string
	UserFirstDayOfWeek           string
	BucketsJSON                  template.JS
}

func (h *WorkspaceHandler) budgetRedirectURL(budgetID string, c echo.Context, flashCode string, anchorBucketID string) string {
	horizon := c.QueryParam("horizon")
	if horizon == "" {
		horizon = c.FormValue("horizon")
	}
	filter := c.QueryParam("filter")
	if filter == "" {
		filter = c.FormValue("filter")
	}

	userID, _ := c.Get(ContextUserIDKey).(string)
	tenantDB, _ := c.Get(ContextBudgetDBKey).(*sql.DB)
	userDefaultHorizon := "monthly"
	if tenantDB != nil && userID != "" {
		if defH, err := budget.GetUserDefaultHorizon(c.Request().Context(), tenantDB, userID); err == nil && defH != "" {
			userDefaultHorizon = defH
		}
	}

	q := url.Values{}
	if horizon != "" && horizon != userDefaultHorizon {
		q.Set("horizon", horizon)
	}
	if filter != "" && filter != "all" {
		q.Set("filter", filter)
	}
	if flashCode != "" {
		q.Set("flash", flashCode)
	}

	res := fmt.Sprintf("/budgets/%s/budget", budgetID)
	if encoded := q.Encode(); encoded != "" {
		res += "?" + encoded
	}
	if anchorBucketID != "" {
		res += "#bucket-" + anchorBucketID
	}
	return res
}

func budgetRedirectURL(budgetID string, c echo.Context, flashCode string, anchorBucketID string) string {
	horizon := c.QueryParam("horizon")
	if horizon == "" {
		horizon = c.FormValue("horizon")
	}
	filter := c.QueryParam("filter")
	if filter == "" {
		filter = c.FormValue("filter")
	}

	userID, _ := c.Get(ContextUserIDKey).(string)
	tenantDB, _ := c.Get(ContextBudgetDBKey).(*sql.DB)
	userDefaultHorizon := "monthly"
	if tenantDB != nil && userID != "" {
		if defH, err := budget.GetUserDefaultHorizon(c.Request().Context(), tenantDB, userID); err == nil && defH != "" {
			userDefaultHorizon = defH
		}
	}

	q := url.Values{}
	if horizon != "" && horizon != userDefaultHorizon {
		q.Set("horizon", horizon)
	}
	if filter != "" && filter != "all" {
		q.Set("filter", filter)
	}
	if flashCode != "" {
		q.Set("flash", flashCode)
	}

	res := fmt.Sprintf("/budgets/%s/budget", budgetID)
	if encoded := q.Encode(); encoded != "" {
		res += "?" + encoded
	}
	if anchorBucketID != "" {
		res += "#bucket-" + anchorBucketID
	}
	return res
}

func getFlashMessage(code string) string {
	switch code {
	case "created":
		return "Envelope created successfully."
	case "updated":
		return "Envelope updated successfully."
	case "archived":
		return "Envelope archived successfully."
	case "assigned":
		return "Funds assigned successfully."
	case "moved":
		return "Funds moved successfully."
	case "auto_assigned":
		return "Funds auto-assigned by priority waterfall."
	case "target_created":
		return "Target created successfully."
	case "target_updated":
		return "Target updated successfully."
	case "target_deleted":
		return "Target deleted successfully."
	case "target_funded":
		return "Target funded successfully."
	default:
		return ""
	}
}

// SafeDOMID converts any string into a valid, safe DOM element identifier or token.
func SafeDOMID(s string) string {
	return safeDOMID(s)
}

func safeDOMID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	res := b.String()
	if res == "" {
		return "default"
	}
	return res
}

// BudgetView renders the YNAB-style Budget/Envelopes view with DaisyUI 5 stats, tables, and badges.
func (h *WorkspaceHandler) BudgetView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	csrfToken, _ := c.Get("csrfToken").(string)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)

	var budgetName, defaultCurr, tz string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency, timezone FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr, &tz)

	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	nowInTz := h.now(loc)

	// Fetch visible horizons for toolbar
	visibleHorizons, _ := budget.ListBudgetHorizons(c.Request().Context(), tenantDB, true)
	userDefaultHorizon, _ := budget.GetUserDefaultHorizon(c.Request().Context(), tenantDB, userID)

	horizonParam := c.QueryParam("horizon")
	if horizonParam == "" {
		horizonParam = userDefaultHorizon
		if horizonParam == "" {
			horizonParam = "monthly"
		}
	}

	var windowDays int
	horizonParam, windowDays = budget.CalculateHorizonDays(horizonParam, nowInTz, loc)

	var availableHorizonsViewData []horizonOptionViewData
	for _, bh := range visibleHorizons {
		hName, tLabel, optLabel := formatHorizonLabels(bh, userLocale, nowInTz, loc)
		_, wDays, _ := budget.ParseHorizonIdentifier(bh.ID, nowInTz, loc)
		availableHorizonsViewData = append(availableHorizonsViewData, horizonOptionViewData{
			ID:           bh.ID,
			Name:         hName,
			Days:         wDays,
			IsSelected:   bh.ID == horizonParam,
			IsDefault:    bh.ID == userDefaultHorizon,
			IsSystem:     bh.IsSystem,
			IsHidden:     bh.IsHidden,
			OptionLabel:  optLabel,
			ToolbarLabel: tLabel,
		})
	}

	filterParam := c.QueryParam("filter")
	if filterParam == "" {
		filterParam = "all"
	}

	flashCode := c.QueryParam("flash")
	flashMsg := getFlashMessage(flashCode)

	// 1. Calculate Age of Money dynamically
	aomHeadline := 0
	if h.reportsHandler != nil {
		if aomResult, _ := h.reportsHandler.CalculateAgeOfMoneyForBudget(c.Request().Context(), tenantDB); aomResult != nil {
			aomHeadline = aomResult.HeadlineDays
		}
	}

	// 2. Calculate Ready to Assign dynamically
	rtaMap, err := budget.CalculateReadyToAssign(c.Request().Context(), tenantDB)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed calculating ready to assign", "error", err.Error())
		rtaMap = make(map[string]int64)
	}
	defaultRTA := rtaMap[defaultCurr]

	type rtaCurrencyItem struct {
		curr string
		amt  int64
	}
	var nonDefaultCurrencies []rtaCurrencyItem
	for curr, amt := range rtaMap {
		if curr != defaultCurr && amt != 0 {
			nonDefaultCurrencies = append(nonDefaultCurrencies, rtaCurrencyItem{curr: curr, amt: amt})
		}
	}
	sort.Slice(nonDefaultCurrencies, func(i, j int) bool {
		return nonDefaultCurrencies[i].curr < nonDefaultCurrencies[j].curr
	})

	var rtaOtherBadges strings.Builder
	if len(nonDefaultCurrencies) > 0 {
		rtaOtherBadges.WriteString(`<div class="flex flex-wrap items-center gap-1.5 mt-1.5">`)
		limit := 3
		displayCount := len(nonDefaultCurrencies)
		if displayCount > limit {
			displayCount = limit
		}

		for i := 0; i < displayCount; i++ {
			item := nonDefaultCurrencies[i]
			badgeColor := "badge-info"
			if item.amt < 0 {
				badgeColor = "badge-error"
			}
			rtaOtherBadges.WriteString(fmt.Sprintf(`<span class="badge %s badge-sm font-bold font-mono">%.2f %s</span>`, badgeColor, float64(item.amt)/100.0, item.curr))
		}

		if len(nonDefaultCurrencies) > limit {
			extraCount := len(nonDefaultCurrencies) - limit
			rtaOtherBadges.WriteString(fmt.Sprintf(`
				<div class="dropdown dropdown-end dropdown-top">
					<label tabindex="0" class="badge badge-neutral badge-sm font-bold cursor-pointer hover:badge-primary transition-colors">+%d walut</label>
					<div tabindex="0" class="dropdown-content z-20 menu p-2.5 shadow-xl bg-base-100 rounded-box border border-base-300 w-52 gap-1.5 text-xs">
						<div class="font-bold text-[11px] uppercase tracking-wider text-base-content/60 px-1 pb-1 border-b border-base-200">Pozostałe waluty</div>
			`, extraCount))
			for i := limit; i < len(nonDefaultCurrencies); i++ {
				item := nonDefaultCurrencies[i]
				badgeColor := "badge-info"
				if item.amt < 0 {
					badgeColor = "badge-error"
				}
				rtaOtherBadges.WriteString(fmt.Sprintf(`
						<div class="flex items-center justify-between px-1 py-0.5">
							<span class="font-bold">%s:</span>
							<span class="badge %s badge-xs font-mono font-bold">%.2f %s</span>
						</div>
				`, item.curr, badgeColor, float64(item.amt)/100.0, item.curr))
			}
			rtaOtherBadges.WriteString(`
					</div>
				</div>
			`)
		}
		rtaOtherBadges.WriteString(`</div>`)
	}

	// 3. Fetch all active targets
	userLocDefaults := i18n.GetLocaleDefaults(userLocale)
	userFirstDayOfWeek, _ := budget.GetUserFirstDayOfWeek(c.Request().Context(), tenantDB, userID, userLocDefaults.FirstDayOfWeek)
	snap, _ := budget.BuildBudgetSnapshot(c.Request().Context(), tenantDB, defaultCurr, windowDays, tz)

	targetRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, bucket_id, name, target_type, priority, sort_order, amount_minor, currency,
		       recurrence_cadence, recurrence_interval,
		       recurrence_day_of_week, recurrence_day_of_month, recurrence_month_of_year,
		       due_date, non_spending_strategy,
		       ceiling_amount_minor, current_funded_minor, amount_mode, lua_script, last_formula_error, uses_runtime
		FROM targets_projection
		WHERE is_archived = 0
		ORDER BY priority DESC, due_date ASC, sort_order ASC`)
	targetsByBucket := make(map[string][]targetViewData)
	if err == nil {
		defer targetRows.Close()
		for targetRows.Next() {
			var t targetViewData
			var nameNull sql.NullString
			var dueDate sql.NullInt64
			var rInterval sql.NullInt64
			var rDow sql.NullInt64
			var rDom sql.NullInt64
			var rMoy sql.NullInt64
			var ceiling sql.NullInt64
			var luaScriptNull sql.NullString
			var lastErrNull sql.NullString
			targetRows.Scan(&t.ID, &t.BucketID, &nameNull, &t.TargetType, &t.Priority, &t.SortOrder,
				&t.AmountMinor, &t.Currency, &t.RecurrenceCadence, &rInterval,
				&rDow, &rDom, &rMoy,
				&dueDate, &t.NonSpendingStrategy, &ceiling, &t.CurrentFundedMinor,
				&t.AmountMode, &luaScriptNull, &lastErrNull, &t.UsesRuntime)
			if nameNull.Valid {
				t.Name = nameNull.String
			}
			if rInterval.Valid {
				t.RecurrenceInterval = int(rInterval.Int64)
				t.RecurrenceDays = t.RecurrenceInterval
			}
			if rDow.Valid {
				v := int(rDow.Int64)
				t.RecurrenceDayOfWeek = &v
			}
			if rDom.Valid {
				v := int(rDom.Int64)
				t.RecurrenceDayOfMonth = &v
			}
			if rMoy.Valid {
				v := int(rMoy.Int64)
				t.RecurrenceMonthOfYear = &v
			}
			if dueDate.Valid {
				t.DueDate = dueDate.Int64
			}
			if ceiling.Valid {
				cVal := ceiling.Int64
				t.CeilingAmountMinor = &cVal
			}
			if luaScriptNull.Valid {
				sVal := luaScriptNull.String
				t.LuaScript = &sVal
			}
			if lastErrNull.Valid {
				eVal := lastErrNull.String
				t.LastFormulaError = &eVal
			}

			// Dynamic Lua formula evaluation (only for formulas not using runtime, as runtime formulas are evaluated and persisted daily via EnsureDailyTargetsEvaluated)
			if snap != nil && t.AmountMode == "LUA_FORMULA" && t.UsesRuntime == 0 && t.LuaScript != nil && *t.LuaScript != "" {
				var anchorDueDateSec *int64
				if dueDate.Valid && dueDate.Int64 > 0 {
					sec := dueDate.Int64 / 1_000_000
					anchorDueDateSec = &sec
				}
				targetCtx := &lua.TargetFormulaContext{
					TargetID:      t.ID,
					Name:          t.Name,
					Planned:       t.AmountMinor,
					Required:      t.AmountMinor,
					Funded:        t.CurrentFundedMinor,
					Priority:      t.Priority,
					Currency:      t.Currency,
					AnchorDueDate: anchorDueDateSec,
				}
				evalVal, evalErr := lua.EvaluateFormulaWithContext(c.Request().Context(), *t.LuaScript, snap, targetCtx)
				if evalErr == nil {
					t.AmountMinor = evalVal
					t.LastFormulaError = nil
				} else {
					errStr := evalErr.Error()
					t.LastFormulaError = &errStr
				}
			}

			tDef := &budget.TargetDefinition{
				ID:                    t.ID,
				BucketID:              t.BucketID,
				Name:                  t.Name,
				Priority:              t.Priority,
				SortOrder:             t.SortOrder,
				AmountMinor:           t.AmountMinor,
				Currency:              t.Currency,
				RecurrenceCadence:     t.RecurrenceCadence,
				RecurrenceInterval:    t.RecurrenceInterval,
				RecurrenceDayOfWeek:   t.RecurrenceDayOfWeek,
				RecurrenceDayOfMonth:  t.RecurrenceDayOfMonth,
				RecurrenceMonthOfYear: t.RecurrenceMonthOfYear,
				DueDate:               t.DueDate,
				NonSpendingStrategy:   t.NonSpendingStrategy,
				CeilingAmountMinor:    t.CeilingAmountMinor,
				CurrentFundedMinor:    t.CurrentFundedMinor,
			}
			t.RequiredMinor = budget.ComputeTargetRequirementInLoc(tDef, windowDays, nowInTz, loc)
			if t.RequiredMinor > 0 {
				needed := t.RequiredMinor - t.CurrentFundedMinor
				if needed < 0 {
					needed = 0
				}
				t.NeededMinor = needed
				pct := int((t.CurrentFundedMinor * 100) / t.RequiredMinor)
				if pct < 0 {
					pct = 0
				} else if pct > 100 {
					pct = 100
				}
				t.ProgressPercent = pct
			} else {
				t.NeededMinor = 0
				t.ProgressPercent = 100
			}

			// Target health evaluation
			isOverdue := t.RecurrenceCadence == budget.CadenceOneOff && t.DueDate > 0 && budget.CalendarDaysBetween(nowInTz, time.UnixMicro(t.DueDate).In(loc), loc) <= 0
			if isOverdue && t.CurrentFundedMinor < t.RequiredMinor {
				t.HealthBadge = "Overdue"
				t.HealthBadgeClass = "badge-error text-error-content"
			} else if t.CurrentFundedMinor < 0 {
				t.HealthBadge = "Overspent"
				t.HealthBadgeClass = "badge-error text-error-content"
			} else if t.CurrentFundedMinor >= t.RequiredMinor && t.RequiredMinor > 0 {
				t.HealthBadge = "Funded / On Track"
				t.HealthBadgeClass = "badge-success text-success-content"
			} else if t.CurrentFundedMinor < t.RequiredMinor {
				t.HealthBadge = "Underfunded"
				t.HealthBadgeClass = "badge-warning text-warning-content"
			} else {
				t.HealthBadge = "Funded / On Track"
				t.HealthBadgeClass = "badge-success text-success-content"
			}

			// Enrich display fields for targets
			t.HasLabel = strings.TrimSpace(t.Name) != ""
			switch t.Priority {
			case 3:
				t.PriorityText = "Mandatory"
				t.PrioBadgeClass = "badge-error"
			case 2:
				t.PriorityText = "Recommended"
				t.PrioBadgeClass = "badge-primary"
			default:
				t.PriorityText = "Optional"
				t.PrioBadgeClass = "badge-ghost"
			}
			if t.HasLabel {
				t.MobileNameDisplay = t.Name
			} else {
				t.MobileNameDisplay = t.PriorityText
			}

			rInt := t.RecurrenceInterval
			if rInt == 0 {
				rInt = t.RecurrenceDays
			}
			switch t.RecurrenceCadence {
			case "CUSTOM_DAYS":
				if rInt == 0 {
					rInt = 90
				}
				t.CadenceDisplay = fmt.Sprintf("%dd", rInt)
			case "CUSTOM_MONTHS":
				if rInt == 0 {
					rInt = 3
				}
				t.CadenceDisplay = fmt.Sprintf("%dm", rInt)
			case "CUSTOM_YEARS":
				if rInt == 0 {
					rInt = 1
				}
				t.CadenceDisplay = fmt.Sprintf("%dy", rInt)
			default:
				t.CadenceDisplay = t.RecurrenceCadence
			}

			if t.DueDate > 0 {
				t.DueText = fmt.Sprintf(" • Due: %s", time.UnixMicro(t.DueDate).In(loc).Format("2006-01-02"))
			}
			if t.CeilingAmountMinor != nil {
				t.GoalText = fmt.Sprintf(" • Goal: %.2f %s", float64(*t.CeilingAmountMinor)/100.0, t.Currency)
			}
			if t.CurrentFundedMinor < 0 {
				t.ProgressClass = "progress-error"
			} else if t.ProgressPercent >= 100 {
				t.ProgressClass = "progress-success"
			} else {
				t.ProgressClass = "progress-warning"
			}
			t.FundedFormatted = i18n.FormatMoneyMinor(t.CurrentFundedMinor, "", userLocale, userNumberFormat)
			t.RequiredFormatted = i18n.FormatMoneyMinor(t.RequiredMinor, "", userLocale, userNumberFormat)
			t.NeededFormatted = i18n.FormatMoneyMinor(t.NeededMinor, "", userLocale, userNumberFormat)

			targetsByBucket[t.BucketID] = append(targetsByBucket[t.BucketID], t)
		}
	}

	// 4. Fetch all bucket currency balances across all currencies to detect any overspending
	bucketBalancesMap := make(map[string]map[string]int64)
	bbRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT bucket_id, currency, balance_minor
		FROM bucket_currency_balances`)
	if err == nil {
		defer bbRows.Close()
		for bbRows.Next() {
			var bID, curr string
			var bMinor int64
			if err := bbRows.Scan(&bID, &curr, &bMinor); err == nil {
				if bucketBalancesMap[bID] == nil {
					bucketBalancesMap[bID] = make(map[string]int64)
				}
				bucketBalancesMap[bID][curr] = bMinor
			}
		}
	}

	// 5. Fetch buckets and balances
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT b.id, b.group_name, b.name, COALESCE(bcb.balance_minor, 0)
		FROM buckets_projection b
		LEFT JOIN bucket_groups bg ON b.group_name = bg.name
		LEFT JOIN bucket_currency_balances bcb ON b.id = bcb.bucket_id AND bcb.currency = ?
		WHERE b.is_archived = 0
		ORDER BY COALESCE(bg.sort_order, 999999) ASC, b.group_name ASC, b.name ASC`, defaultCurr)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading buckets")
	}
	defer rows.Close()

	var allBuckets []bucketViewData
	for rows.Next() {
		var b bucketViewData
		rows.Scan(&b.ID, &b.GroupName, &b.Name, &b.BalanceMinor)
		b.Currency = defaultCurr
		b.Targets = targetsByBucket[b.ID]

		var totalReq, totalFunded, totalNeeded int64
		hasUnderfundedP3 := false
		hasUnderfundedP2 := false
		hasUnderfundedP1 := false
		hasP3Targets := false

		hasFormulaError := false
		formulaErrMsg := ""
		for _, t := range b.Targets {
			if t.LastFormulaError != nil && *t.LastFormulaError != "" {
				hasFormulaError = true
				if formulaErrMsg == "" {
					formulaErrMsg = *t.LastFormulaError
				}
			}
			if t.Currency == defaultCurr {
				totalReq += t.RequiredMinor
				totalFunded += t.CurrentFundedMinor
				totalNeeded += t.NeededMinor
				if t.Priority == 3 {
					hasP3Targets = true
					if t.NeededMinor > 0 {
						hasUnderfundedP3 = true
					}
				} else if t.Priority == 2 {
					if t.NeededMinor > 0 {
						hasUnderfundedP2 = true
					}
				} else if t.Priority == 1 {
					if t.NeededMinor > 0 {
						hasUnderfundedP1 = true
					}
				}
			}
		}

		b.HasFormulaError = hasFormulaError
		b.FormulaErrorMessage = formulaErrMsg

		b.TotalRequiredMinor = totalReq
		b.TotalFundedMinor = totalFunded
		b.TotalNeededMinor = totalNeeded
		b.UnassignedBufferMinor = b.BalanceMinor - totalFunded

		if totalReq > 0 {
			pct := int((totalFunded * 100) / totalReq)
			if pct < 0 {
				pct = 0
			} else if pct > 100 {
				pct = 100
			}
			b.ProgressPercent = pct
		} else {
			b.ProgressPercent = 100
		}

		// Health status evaluation strictly in order:
		// 1. BalanceMinor < 0 -> Overspent
		// 2. UnassignedBufferMinor < 0 -> Over-allocated
		// 3. hasUnderfundedP3 || hasUnderfundedP2 -> Underfunded
		// 4. hasUnderfundedP1 -> Optional remaining
		// 5. Else -> Funded / On Track
		if b.BalanceMinor < 0 {
			b.HealthStatus = "Overspent"
			b.HealthBadge = "Overspent"
			b.HealthBadgeClass = "badge-error text-error-content"
		} else if b.UnassignedBufferMinor < 0 {
			b.HealthStatus = "Over-allocated"
			b.HealthBadge = "Over-allocated"
			b.HealthBadgeClass = "badge-warning text-warning-content"
		} else if hasUnderfundedP3 || hasUnderfundedP2 {
			b.HealthStatus = "Underfunded"
			b.HealthBadge = "Underfunded"
			b.HealthBadgeClass = "badge-warning text-warning-content"
		} else if hasUnderfundedP1 {
			b.HealthStatus = "Optional remaining"
			b.HealthBadge = "Optional remaining"
			b.HealthBadgeClass = "badge-ghost text-base-content/60"
		} else {
			b.HealthStatus = "Funded / On Track"
			b.HealthBadge = "Funded / On Track"
			b.HealthBadgeClass = "badge-success text-success-content"
		}

		b.HasP3AtRisk = (hasP3Targets && b.UnassignedBufferMinor < 0) || hasUnderfundedP3
		b.DefaultExpanded = (b.HealthStatus == "Overspent" || b.HealthStatus == "Over-allocated" || b.HealthStatus == "Underfunded")

		// Enrich bucket display fields
		gName := b.GroupName
		if gName == "" {
			gName = "Needs"
		}
		b.GroupID = safeDOMID(gName)
		b.EscapedName = strings.ReplaceAll(b.Name, "'", "\\'")
		b.EscapedGroupName = strings.ReplaceAll(b.GroupName, "'", "\\'")
		b.BalanceFormatted = i18n.FormatMoneyMinor(b.BalanceMinor, "", userLocale, userNumberFormat)
		b.TotalRequiredFormatted = i18n.FormatMoneyMinor(b.TotalRequiredMinor, "", userLocale, userNumberFormat)
		b.TotalFundedFormatted = i18n.FormatMoneyMinor(b.TotalFundedMinor, "", userLocale, userNumberFormat)
		b.TotalNeededFormatted = i18n.FormatMoneyMinor(b.TotalNeededMinor, "", userLocale, userNumberFormat)
		b.UnassignedBufferFormatted = i18n.FormatMoneyMinor(b.UnassignedBufferMinor, "", userLocale, userNumberFormat)

		// Populate overspent currencies across all balances for this bucket
		if balances, ok := bucketBalancesMap[b.ID]; ok {
			var overspentItems []bucketCurrencyItemViewData
			for cCode, balMinor := range balances {
				if balMinor < 0 {
					deficitMinor := -balMinor
					rtaAmt := rtaMap[cCode]
					canCover := rtaAmt > 0
					overspentItems = append(overspentItems, bucketCurrencyItemViewData{
						Currency:              cCode,
						BalanceMinor:          balMinor,
						BalanceFormatted:      i18n.FormatMoneyMinor(balMinor, "", userLocale, userNumberFormat),
						CoverDeficitMinor:     deficitMinor,
						CoverDeficitFormatted: fmt.Sprintf("%.2f", float64(deficitMinor)/100.0),
						CanCover:              canCover,
						RTASurplusFormatted:   i18n.FormatMoneyMinor(rtaAmt, "", userLocale, userNumberFormat),
					})
				}
			}
			sort.Slice(overspentItems, func(i, j int) bool {
				return overspentItems[i].Currency < overspentItems[j].Currency
			})
			if len(overspentItems) > 0 {
				b.HasOverspentDeficit = true
				b.OverspentCurrencies = overspentItems
			}
		}

		if b.UnassignedBufferMinor > 0 {
			b.BufferMobileStr = fmt.Sprintf("Buffer: +%s %s", i18n.FormatMoneyMinor(b.UnassignedBufferMinor, "", userLocale, userNumberFormat), b.Currency)
		} else if b.UnassignedBufferMinor < 0 {
			b.BufferMobileStr = fmt.Sprintf("⚠ Over-allocated: %s %s", i18n.FormatMoneyMinor(b.UnassignedBufferMinor, "", userLocale, userNumberFormat), b.Currency)
		} else {
			b.BufferMobileStr = fmt.Sprintf("Buffer: %s %s", i18n.FormatMoneyMinor(0, "", userLocale, userNumberFormat), b.Currency)
		}

		switch b.HealthStatus {
		case "Overspent":
			b.StatusDot = "bg-error"
		case "Over-allocated", "Underfunded":
			b.StatusDot = "bg-warning"
		case "Optional remaining":
			b.StatusDot = "bg-base-300"
		default:
			b.StatusDot = "bg-success"
		}

		if b.HealthStatus == "Overspent" || b.HealthStatus == "Over-allocated" {
			b.ProgressClass = "progress-error"
		} else if b.ProgressPercent >= 100 {
			b.ProgressClass = "progress-success"
		} else {
			b.ProgressClass = "progress-warning"
		}

		if b.BalanceMinor < 0 {
			b.AvailColor = "text-error"
		} else {
			b.AvailColor = "text-base-content"
		}

		if b.TotalNeededMinor > 0 {
			b.NeededColor = "text-warning"
		} else {
			b.NeededColor = "text-base-content/60"
		}

		searchTokens := []string{b.Name, b.GroupName}
		for _, t := range b.Targets {
			if strings.TrimSpace(t.Name) != "" {
				searchTokens = append(searchTokens, t.Name)
			}
		}
		b.SearchTokens = strings.ToLower(strings.Join(searchTokens, " "))

		actionMenuDesktop := RenderPopoverActionMenu("bucket", b.ID, []TableActionItem{
			{
				Label:   "💵 Assign Funds",
				OnClick: fmt.Sprintf("openAssignModal('%s', '%s', '%s')", b.ID, b.EscapedName, b.Currency),
			},
			{
				Label:   "⇄ Move Funds",
				OnClick: fmt.Sprintf("openMoveModal('%s', '%s', '%s')", b.ID, b.EscapedName, b.Currency),
			},
			{
				Label:   "✏️ Edit Bucket",
				OnClick: fmt.Sprintf("openEditBucketModal('%s', '%s', '%s')", b.ID, b.EscapedName, b.EscapedGroupName),
			},
			{
				Label:      "🗑️ Archive Bucket",
				FormAction: fmt.Sprintf("/budgets/%s/buckets/%s/archive", budgetID, b.ID),
				CSRFToken:  csrfToken,
				ConfirmMsg: "Are you sure you want to archive this bucket? Existing transactions and balances will be preserved.",
				IsDanger:   true,
			},
		})
		b.ActionMenuDesktop = template.HTML(actionMenuDesktop)

		actionMenuMobile := RenderPopoverActionMenu("bucket_mob", b.ID, []TableActionItem{
			{
				Label:   "⇄ Move Funds",
				OnClick: fmt.Sprintf("openMoveModal('%s', '%s', '%s')", b.ID, b.EscapedName, b.Currency),
			},
			{
				Label:   "✏️ Edit Bucket",
				OnClick: fmt.Sprintf("openEditBucketModal('%s', '%s', '%s')", b.ID, b.EscapedName, b.EscapedGroupName),
			},
			{
				Label:      "🗑️ Archive Bucket",
				FormAction: fmt.Sprintf("/budgets/%s/buckets/%s/archive", budgetID, b.ID),
				CSRFToken:  csrfToken,
				ConfirmMsg: "Are you sure you want to archive this bucket? Existing transactions and balances will be preserved.",
				IsDanger:   true,
			},
		})
		b.ActionMenuMobile = template.HTML(actionMenuMobile)

		allBuckets = append(allBuckets, b)
	}

	// Filter counts & stats
	var countAll, countAttention, countOverspent, countBuffer int
	var totalNeededAcrossAllMinor int64
	var totalMandatoryP3NeededAcrossAllMinor int64

	// Track overspending across all currencies
	totalDeficitsPerCurrency := make(map[string]int64)
	nonDefaultOverspentCurrenciesMap := make(map[string]bool)

	for _, b := range allBuckets {
		countAll++
		isOverspent := b.HealthStatus == "Overspent" || b.HasOverspentDeficit
		if isOverspent || b.HealthStatus == "Over-allocated" || b.HealthStatus == "Underfunded" {
			countAttention++
		}
		if isOverspent {
			countOverspent++
		}
		if b.UnassignedBufferMinor > 0 {
			countBuffer++
		}

		if b.HasOverspentDeficit {
			for _, oc := range b.OverspentCurrencies {
				totalDeficitsPerCurrency[oc.Currency] += oc.CoverDeficitMinor
				if oc.Currency != defaultCurr {
					nonDefaultOverspentCurrenciesMap[oc.Currency] = true
				}
			}
		}

		totalNeededAcrossAllMinor += b.TotalNeededMinor
		for _, t := range b.Targets {
			if t.Currency == defaultCurr && t.Priority == 3 {
				totalMandatoryP3NeededAcrossAllMinor += t.NeededMinor
			}
		}
	}

	var nonDefaultOverspentCurrencies []string
	for cCode := range nonDefaultOverspentCurrenciesMap {
		nonDefaultOverspentCurrencies = append(nonDefaultOverspentCurrencies, cCode)
	}
	sort.Strings(nonDefaultOverspentCurrencies)

	var overspentAlertCurrencies []overspentSummaryItemViewData
	var allDeficitCurrenciesSorted []string
	for cCode := range totalDeficitsPerCurrency {
		allDeficitCurrenciesSorted = append(allDeficitCurrenciesSorted, cCode)
	}
	sort.Strings(allDeficitCurrenciesSorted)
	for _, cCode := range allDeficitCurrenciesSorted {
		overspentAlertCurrencies = append(overspentAlertCurrencies, overspentSummaryItemViewData{
			Currency:         cCode,
			DeficitFormatted: i18n.FormatMoneyMinor(-totalDeficitsPerCurrency[cCode], "", userLocale, userNumberFormat),
		})
	}
	hasOverspentAlert := len(overspentAlertCurrencies) > 0 && filterParam != "overspent"

	// Filter display buckets
	var displayBuckets []bucketViewData
	for _, b := range allBuckets {
		match := true
		switch filterParam {
		case "attention":
			match = (b.HealthStatus == "Overspent" || b.HasOverspentDeficit || b.HealthStatus == "Over-allocated" || b.HealthStatus == "Underfunded")
		case "overspent":
			match = (b.HealthStatus == "Overspent" || b.HasOverspentDeficit)
		case "buffer":
			match = (b.UnassignedBufferMinor > 0)
		default:
			match = true
		}
		if match {
			displayBuckets = append(displayBuckets, b)
		}
	}

	// Group display buckets by GroupName preserving configured order
	type groupEntry struct {
		GroupName string
		Buckets   []bucketViewData
	}
	var groupEntries []groupEntry
	groupIndex := make(map[string]int)

	// Fetch all configured groups from bucket_groups to guarantee order and allow empty or filtered groups
	configuredGroups, _ := budget.ListBucketGroups(c.Request().Context(), tenantDB)
	for _, cg := range configuredGroups {
		if _, exists := groupIndex[cg.Name]; !exists {
			idx := len(groupEntries)
			groupIndex[cg.Name] = idx
			groupEntries = append(groupEntries, groupEntry{
				GroupName: cg.Name,
			})
		}
	}

	for _, b := range displayBuckets {
		gName := b.GroupName
		if gName == "" {
			gName = "Needs"
		}
		idx, exists := groupIndex[gName]
		if !exists {
			idx = len(groupEntries)
			groupIndex[gName] = idx
			groupEntries = append(groupEntries, groupEntry{
				GroupName: gName,
			})
		}
		groupEntries[idx].Buckets = append(groupEntries[idx].Buckets, b)
	}

	// Count active buckets per group across all buckets (unfiltered)
	groupActiveBucketsCount := make(map[string]int)
	for _, b := range allBuckets {
		gName := b.GroupName
		if gName == "" {
			gName = "Needs"
		}
		groupActiveBucketsCount[gName]++
	}

	var bucketGroups []bucketGroupViewData
	for _, ge := range groupEntries {
		// When filtering (e.g. attention, overspent, buffer), skip groups with 0 matching buckets
		if filterParam != "" && filterParam != "all" && len(ge.Buckets) == 0 {
			continue
		}
		bg := bucketGroupViewData{
			GroupName:         ge.GroupName,
			EscapedGroupName:  strings.ReplaceAll(ge.GroupName, "'", "\\'"),
			GroupID:           safeDOMID(ge.GroupName),
			Buckets:           ge.Buckets,
			TotalBucketsCount: groupActiveBucketsCount[ge.GroupName],
		}
		for _, b := range ge.Buckets {
			bg.TotalAvailableMinor += b.BalanceMinor
			bg.TotalRequiredMinor += b.TotalRequiredMinor
			bg.TotalFundedMinor += b.TotalFundedMinor
			bg.TotalNeededMinor += b.TotalNeededMinor
		}
		bg.TotalAvailableFormatted = i18n.FormatMoneyMinor(bg.TotalAvailableMinor, "", userLocale, userNumberFormat)
		bg.TotalRequiredFormatted = i18n.FormatMoneyMinor(bg.TotalRequiredMinor, "", userLocale, userNumberFormat)
		bg.TotalFundedFormatted = i18n.FormatMoneyMinor(bg.TotalFundedMinor, "", userLocale, userNumberFormat)
		bg.TotalNeededFormatted = i18n.FormatMoneyMinor(bg.TotalNeededMinor, "", userLocale, userNumberFormat)
		if bg.TotalRequiredMinor > 0 {
			pct := int((bg.TotalFundedMinor * 100) / bg.TotalRequiredMinor)
			if pct < 0 {
				pct = 0
			} else if pct > 100 {
				pct = 100
			}
			bg.ProgressPercent = pct
		} else {
			bg.ProgressPercent = 100
		}
		if bg.ProgressPercent >= 100 {
			bg.ProgressClass = "progress-success"
		} else {
			bg.ProgressClass = "progress-warning"
		}
		bucketGroups = append(bucketGroups, bg)
	}

	// Fetch user-owned accounts and balances for sidebar summary
	accRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.name, COALESCE(acb.balance_minor, 0), a.primary_currency,
		       COALESCE(a.favicon_type, 'EMOJI'), COALESCE(a.favicon_value, '')
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN account_currency_balances acb ON a.id = acb.account_id AND a.primary_currency = acb.currency
		WHERE p.is_user_owned = 1 AND a.is_closed = 0
		ORDER BY a.name ASC`)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed querying accounts for workspace summary", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading accounts")
	}
	defer accRows.Close()

	var accountSummaries []accountSummaryItem
	hasAccounts := false
	for accRows.Next() {
		hasAccounts = true
		var aName, aCurr, fType, fVal string
		var aBalance int64
		accRows.Scan(&aName, &aBalance, &aCurr, &fType, &fVal)

		isURL := false
		iconVal := fVal
		if fType == "URL" && fVal != "" {
			isURL = true
		} else if iconVal == "" {
			iconVal = "💳"
		}

		accountSummaries = append(accountSummaries, accountSummaryItem{
			Name:             aName,
			Currency:         aCurr,
			BalanceFormatted: i18n.FormatMoneyMinor(aBalance, "", userLocale, userNumberFormat),
			IsURLIcon:        isURL,
			IconValue:        iconVal,
		})
	}

	// Distinct category groups for datalist in bucket modal
	var distinctGroups []string
	seenGroups := make(map[string]bool)
	for _, ge := range groupEntries {
		gName := strings.TrimSpace(ge.GroupName)
		if gName != "" && !seenGroups[gName] {
			seenGroups[gName] = true
			distinctGroups = append(distinctGroups, gName)
		}
	}
	newBucketGroupPlaceholder := "e.g. Needs, Wants, Savings"
	if len(distinctGroups) > 0 {
		count := 3
		if len(distinctGroups) < count {
			count = len(distinctGroups)
		}
		newBucketGroupPlaceholder = "e.g. " + strings.Join(distinctGroups[:count], ", ")
	}

	// Query user's other accessible budgets for Structure Import dropdown
	otherBudgetRows, err := h.mainDB.QueryContext(c.Request().Context(), `
		SELECT b.id, b.name, b.default_currency
		FROM budgets b
		JOIN budget_members bm ON b.id = bm.budget_id
		WHERE bm.user_id = ? AND b.id != ?
		ORDER BY b.name ASC`, userID, budgetID)
	var otherBudgets []otherBudgetOptionViewData
	hasOtherBudgets := false
	if err == nil {
		defer otherBudgetRows.Close()
		for otherBudgetRows.Next() {
			var oID, oName, oCurr string
			if err := otherBudgetRows.Scan(&oID, &oName, &oCurr); err == nil {
				hasOtherBudgets = true
				otherBudgets = append(otherBudgets, otherBudgetOptionViewData{
					ID:       oID,
					Name:     oName,
					Currency: oCurr,
				})
			}
		}
	}

	availCurrencies, err := budget.GetAvailableCurrencies(c.Request().Context(), tenantDB, defaultCurr)
	if err != nil {
		availCurrencies = []string{defaultCurr}
	}

	accountCurrencies, err := budget.GetAccountCurrencies(c.Request().Context(), tenantDB, defaultCurr)
	if err != nil {
		accountCurrencies = []string{defaultCurr}
	}

	horizonTitle := horizonParam
	horizonDisplay := horizonParam
	for _, avh := range availableHorizonsViewData {
		if avh.ID == horizonParam {
			horizonTitle = avh.ToolbarLabel
			horizonDisplay = avh.ToolbarLabel
			break
		}
	}
	if horizonDisplay == "" {
		horizonTitle = horizonParam
		horizonDisplay = horizonParam
	}

	rtaColor := "text-success"
	rtaDescClass := "text-success"
	rtaDesc := "All money assigned to envelopes"
	if defaultRTA < 0 {
		rtaColor = "text-error"
		rtaDescClass = "text-error font-semibold"
		rtaDesc = "You assigned more than you have!"
	} else if defaultRTA > 0 {
		rtaColor = "text-primary"
		rtaDescClass = "text-base-content/60"
		rtaDesc = "Assign this to envelopes below"
	}

	// Determine warning status for default RTA if it is inflated by negative bucket balances
	defaultRTADeficitMinor := totalDeficitsPerCurrency[defaultCurr]
	defaultRTAHasDeficitWarning := defaultRTADeficitMinor > 0 && defaultRTA >= 0
	defaultRTAWarningTooltip := ""
	if defaultRTAHasDeficitWarning {
		formattedDeficit := i18n.FormatMoneyMinor(defaultRTADeficitMinor, "", userLocale, userNumberFormat) + " " + defaultCurr
		defaultRTAWarningTooltip = i18n.T(userLocale, "budget-overspent-rta-warning-tooltip", "deficit", formattedDeficit)
	}

	var topCurrencies, extraCurrencies []rtaCurrencyBadgeViewData
	limit := 3
	displayCount := len(nonDefaultCurrencies)
	if displayCount > limit {
		displayCount = limit
	}
	for i := 0; i < displayCount; i++ {
		item := nonDefaultCurrencies[i]
		color := "badge-info"
		isWarning := false
		warningTooltip := ""
		cDeficit := totalDeficitsPerCurrency[item.curr]
		if item.amt < 0 {
			color = "badge-error"
		} else if cDeficit > 0 {
			color = "badge-warning"
			isWarning = true
			formattedDeficit := i18n.FormatMoneyMinor(cDeficit, "", userLocale, userNumberFormat) + " " + item.curr
			warningTooltip = i18n.T(userLocale, "budget-overspent-rta-warning-tooltip", "deficit", formattedDeficit)
		}
		topCurrencies = append(topCurrencies, rtaCurrencyBadgeViewData{
			Currency:        item.curr,
			AmountFormatted: i18n.FormatMoneyMinor(item.amt, "", userLocale, userNumberFormat),
			BadgeColor:      color,
			IsWarning:       isWarning,
			WarningTooltip:  warningTooltip,
		})
	}
	for i := limit; i < len(nonDefaultCurrencies); i++ {
		item := nonDefaultCurrencies[i]
		color := "badge-info"
		isWarning := false
		warningTooltip := ""
		cDeficit := totalDeficitsPerCurrency[item.curr]
		if item.amt < 0 {
			color = "badge-error"
		} else if cDeficit > 0 {
			color = "badge-warning"
			isWarning = true
			formattedDeficit := i18n.FormatMoneyMinor(cDeficit, "", userLocale, userNumberFormat) + " " + item.curr
			warningTooltip = i18n.T(userLocale, "budget-overspent-rta-warning-tooltip", "deficit", formattedDeficit)
		}
		extraCurrencies = append(extraCurrencies, rtaCurrencyBadgeViewData{
			Currency:        item.curr,
			AmountFormatted: i18n.FormatMoneyMinor(item.amt, "", userLocale, userNumberFormat),
			BadgeColor:      color,
			IsWarning:       isWarning,
			WarningTooltip:  warningTooltip,
		})
	}

	allBucketsJSON, _ := json.Marshal(allBuckets)
	// Preserve unescaped unicode entities like & in JSON so scripts match raw strings
	rawBucketsJSON := strings.ReplaceAll(string(allBucketsJSON), "\\u0026", "&")

	dateFormat, timeFormat := GetUserDateTimePreferences(c)

	pageData := budgetPageViewData{
		Locale:                     userLocale,
		NumberFormat:               userNumberFormat,
		BudgetID:                   budgetID,
		BudgetName:                 budgetName,
		Role:                       role,
		DefaultCurr:                defaultCurr,
		HorizonParam:               horizonParam,
		HorizonTitle:               horizonTitle,
		HorizonDisplay:             horizonDisplay,
		IsHorizonMonthly:           horizonParam == "monthly",
		WindowDays:                 windowDays,
		AvailableHorizons:          availableHorizonsViewData,
		FilterParam:                filterParam,
		IsFilterAll:                filterParam == "all" || filterParam == "",
		FlashMsg:                   flashMsg,
		AOMHeadline:                aomHeadline,
		DateFormat:                 dateFormat,
		TimeFormat:                 timeFormat,
		CSRFToken:                  csrfToken,
		SidebarHTML:                template.HTML(RenderSidebarNavigation(budgetID, "budget", userLocale)),
		BottomDockHTML:             template.HTML(RenderBottomDock(budgetID, "budget", userLocale)),
		DateTimeFormatterScript:    template.HTML(DateTimeFormatterScript()),
		HasAccounts:                hasAccounts,
		AccountSummaries:           accountSummaries,
		RTAColor:                   rtaColor,
		RTADescClass:               rtaDescClass,
		RTADesc:                    rtaDesc,
		DefaultRTAFormatted:        i18n.FormatMoneyMinor(defaultRTA, "", userLocale, userNumberFormat),
		DefaultRTAHasDeficitWarning: defaultRTAHasDeficitWarning,
		DefaultRTAWarningTooltip:    defaultRTAWarningTooltip,
		HasNonDefaultRTACurrencies: len(nonDefaultCurrencies) > 0,
		TopRTACurrencies:           topCurrencies,
		ExtraRTACurrencies:         extraCurrencies,
		HasOverspentAlert:          hasOverspentAlert,
		OverspentAlertCurrencies:   overspentAlertCurrencies,
		OverspentFilterCurrencies:  nonDefaultOverspentCurrencies,
		TotalNeededFormatted:       i18n.FormatMoneyMinor(totalNeededAcrossAllMinor, "", userLocale, userNumberFormat),
		HasMandatoryP3Needed:       totalMandatoryP3NeededAcrossAllMinor > 0,
		MandatoryP3NeededFormatted: i18n.FormatMoneyMinor(totalMandatoryP3NeededAcrossAllMinor, "", userLocale, userNumberFormat),
		CountAll:                   countAll,
		CountAttention:             countAttention,
		CountOverspent:             countOverspent,
		CountBuffer:                countBuffer,
		AllBucketsCount:            len(allBuckets),
		DisplayBucketsCount:        len(displayBuckets),
		BucketGroups:               bucketGroups,
		AllBuckets:                 allBuckets,
		DistinctGroups:             distinctGroups,
		NewBucketGroupPlaceholder:  newBucketGroupPlaceholder,
		HasOtherBudgets:            hasOtherBudgets,
		OtherBudgets:               otherBudgets,
		AvailableCurrencies:        availCurrencies,
		AccountCurrencies:          accountCurrencies,
		UserFirstDayOfWeek:         userFirstDayOfWeek,
		BucketsJSON:                template.JS(rawBucketsJSON),
	}

	htmlContent, err := templates.RenderString("workspace/budget", pageData)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed rendering budget template", "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to render page")
	}

	return c.HTML(http.StatusOK, htmlContent)

}

func toJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// CreateBucket handles creating an envelope bucket.
func (h *WorkspaceHandler) CreateBucket(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	groupName := strings.TrimSpace(c.FormValue("group_name"))
	if groupName == "" {
		groupName = "Needs"
	}

	bID, err := budget.CreateBucket(c.Request().Context(), tenantDB, budget.CreateBucketInput{
		GroupName:     groupName,
		Name:          name,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "created", bID))
}

// RenameGroup handles renaming an entire bucket category/group across envelopes.
func (h *WorkspaceHandler) RenameGroup(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	oldName := strings.TrimSpace(c.FormValue("old_name"))
	newName := strings.TrimSpace(c.FormValue("new_name"))

	if err := budget.RenameBucketGroup(c.Request().Context(), tenantDB, budget.RenameBucketGroupInput{
		OldName:       oldName,
		NewName:       newName,
		AgentID:       userID,
		CorrelationID: corrID,
	}); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "updated", ""))
}

// MoveGroup handles reordering bucket groups up or down.
func (h *WorkspaceHandler) MoveGroup(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	groupName := strings.TrimSpace(c.FormValue("group_name"))
	dir := strings.TrimSpace(c.QueryParam("dir"))

	if err := budget.MoveBucketGroup(c.Request().Context(), tenantDB, groupName, dir); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "updated", ""))
}

// DeleteGroup handles deleting an empty bucket category group.
func (h *WorkspaceHandler) DeleteGroup(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	groupName := strings.TrimSpace(c.FormValue("group_name"))

	if err := budget.DeleteBucketGroup(c.Request().Context(), tenantDB, groupName); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "deleted", ""))
}


// UpdateBucket handles updating bucket name or group.
func (h *WorkspaceHandler) UpdateBucket(c echo.Context) error {
	budgetID := c.Param("budgetId")
	bucketID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	groupName := strings.TrimSpace(c.FormValue("group_name"))

	err := budget.UpdateBucket(c.Request().Context(), tenantDB, budget.UpdateBucketInput{
		ID:            bucketID,
		GroupName:     groupName,
		Name:          name,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "updated", bucketID))
}

// MoveBucketGroup handles moving a bucket into another category group via drag & drop or direct API.
func (h *WorkspaceHandler) MoveBucketGroup(c echo.Context) error {
	budgetID := c.Param("budgetId")
	bucketID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	groupName := strings.TrimSpace(c.FormValue("group_name"))
	if groupName == "" {
		// Also support JSON body if sent as application/json
		var req struct {
			GroupName string `json:"group_name"`
		}
		if err := c.Bind(&req); err == nil && req.GroupName != "" {
			groupName = strings.TrimSpace(req.GroupName)
		}
	}
	if groupName == "" {
		groupName = "Default"
	}

	err := budget.UpdateBucket(c.Request().Context(), tenantDB, budget.UpdateBucketInput{
		ID:            bucketID,
		GroupName:     groupName,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		if strings.Contains(c.Request().Header.Get("Accept"), "application/json") || c.Request().Header.Get("X-Requested-With") == "XMLHttpRequest" {
			return c.JSON(http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if strings.Contains(c.Request().Header.Get("Accept"), "application/json") || c.Request().Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return c.JSON(http.StatusOK, map[string]any{
			"success":    true,
			"bucket_id":  bucketID,
			"group_name": groupName,
		})
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "updated", bucketID))
}

// ArchiveBucket archives a bucket if it has zero balance and zero targets.
func (h *WorkspaceHandler) ArchiveBucket(c echo.Context) error {
	budgetID := c.Param("budgetId")
	bucketID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	err := budget.ArchiveBucket(c.Request().Context(), tenantDB, bucketID, userID, corrID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "archived", ""))
}

// AssignFunds assigns funds from ready to assign to bucket.
func (h *WorkspaceHandler) AssignFunds(c echo.Context) error {
	budgetID := c.Param("budgetId")
	bucketID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	currency := c.FormValue("currency")
	if currency == "" {
		currency = "PLN"
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	var targetID *string
	tID := c.FormValue("target_id")
	if tID != "" {
		targetID = &tID
	}

	err := budget.AssignFunds(c.Request().Context(), tenantDB, budget.AssignFundsInput{
		BucketID:      bucketID,
		TargetID:      targetID,
		Currency:      currency,
		AmountMinor:   amtMinor,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "assigned", bucketID))
}

// MoveFunds moves funds between buckets.
func (h *WorkspaceHandler) MoveFunds(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	srcID := c.FormValue("source_bucket_id")
	dstID := c.FormValue("dest_bucket_id")
	currency := c.FormValue("currency")
	if currency == "" {
		currency = "PLN"
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	err := budget.MoveFunds(c.Request().Context(), tenantDB, budget.MoveFundsInput{
		SourceBucketID: srcID,
		DestBucketID:   dstID,
		Currency:       currency,
		AmountMinor:    amtMinor,
		AgentID:        userID,
		CorrelationID:  corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "moved", dstID))
}

// MoveTargetFunds moves funds between targets or buffer within a bucket.
func (h *WorkspaceHandler) MoveTargetFunds(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	bucketID := c.FormValue("bucket_id")
	srcTargetID := c.FormValue("source_target_id")
	dstTargetID := c.FormValue("dest_target_id")
	currency := c.FormValue("currency")
	if currency == "" {
		currency = "PLN"
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	var srcPtr, dstPtr *string
	if srcTargetID != "" && srcTargetID != "BUFFER" {
		srcPtr = &srcTargetID
	}
	if dstTargetID != "" && dstTargetID != "BUFFER" {
		dstPtr = &dstTargetID
	}

	err := budget.MoveTargetFunds(c.Request().Context(), tenantDB, budget.MoveTargetFundsInput{
		BucketID:       bucketID,
		SourceTargetID: srcPtr,
		DestTargetID:   dstPtr,
		Currency:       currency,
		AmountMinor:    amtMinor,
		AgentID:        userID,
		CorrelationID:  corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "target_moved", bucketID))
}

// AutoAssign triggers priority waterfall auto-assign from available pool.
func (h *WorkspaceHandler) AutoAssign(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	currency := c.FormValue("currency")
	var defaultCurr, tz string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT default_currency, timezone FROM budgets WHERE id = ?`, budgetID).Scan(&defaultCurr, &tz)
	if currency == "" {
		currency = defaultCurr
	}
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	nowInTz := h.now(loc)
	horizon := c.FormValue("horizon")
	_, windowDays := budget.CalculateHorizonDays(horizon, nowInTz, loc)

	rtaMap, err := budget.CalculateReadyToAssign(c.Request().Context(), tenantDB)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	pool := rtaMap[currency]
	if pool <= 0 {
		return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "", ""))
	}

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT t.id, t.bucket_id, t.name, t.priority, t.sort_order, t.amount_minor, t.currency,
		       t.recurrence_cadence, t.recurrence_interval, t.due_date, t.non_spending_strategy,
		       t.ceiling_amount_minor, t.current_funded_minor
		FROM targets_projection t
		JOIN buckets_projection b ON t.bucket_id = b.id
		WHERE t.currency = ? AND t.is_archived = 0 AND b.is_archived = 0
		ORDER BY t.priority DESC, t.due_date ASC, t.sort_order ASC`, currency)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	defer rows.Close()

	var targets []*budget.TargetDefinition
	for rows.Next() {
		var t budget.TargetDefinition
		var nameNull sql.NullString
		var dueDate sql.NullInt64
		var ceiling sql.NullInt64
		var rInterval sql.NullInt64
		rows.Scan(&t.ID, &t.BucketID, &nameNull, &t.Priority, &t.SortOrder, &t.AmountMinor,
			&t.Currency, &t.RecurrenceCadence, &rInterval, &dueDate,
			&t.NonSpendingStrategy, &ceiling, &t.CurrentFundedMinor)
		if nameNull.Valid {
			t.Name = nameNull.String
		}
		if rInterval.Valid {
			t.RecurrenceInterval = int(rInterval.Int64)
		}
		if dueDate.Valid {
			t.DueDate = dueDate.Int64
		}
		if ceiling.Valid {
			cVal := ceiling.Int64
			t.CeilingAmountMinor = &cVal
		}
		targets = append(targets, &t)
	}

	allocs, _ := budget.PriorityAutoAssign(targets, windowDays, nowInTz, pool)
	for _, alloc := range allocs {
		if alloc.Assigned > 0 {
			// Find bucket for this target
			var bucketID string
			for _, t := range targets {
				if t.ID == alloc.TargetID {
					bucketID = t.BucketID
					break
				}
			}
			tID := alloc.TargetID
			_ = budget.AssignFunds(c.Request().Context(), tenantDB, budget.AssignFundsInput{
				BucketID:      bucketID,
				TargetID:      &tID,
				Currency:      currency,
				AmountMinor:   alloc.Assigned,
				AgentID:       userID,
				CorrelationID: corrID,
			})
		}
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "auto_assigned", ""))
}

// CreateTarget creates a new target in a bucket.
func (h *WorkspaceHandler) CreateTarget(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	bucketID := c.FormValue("bucket_id")
	name := strings.TrimSpace(c.FormValue("name"))
	tType := c.FormValue("target_type")
	if tType == "" {
		tType = "EXPENSE"
	}
	priority, _ := strconv.Atoi(c.FormValue("priority"))
	if priority < 1 {
		priority = 2
	}

	var defaultCurr string
	_ = h.mainDB.QueryRowContext(c.Request().Context(), "SELECT default_currency FROM budgets WHERE id = ?", budgetID).Scan(&defaultCurr)
	if defaultCurr == "" {
		defaultCurr = "PLN"
	}

	availCurrencies, err := budget.GetAvailableCurrencies(c.Request().Context(), tenantDB, defaultCurr)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed checking available currencies", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed checking available currencies")
	}

	currency := strings.ToUpper(strings.TrimSpace(c.FormValue("currency")))
	if currency == "" {
		currency = defaultCurr
	}

	isAllowed := false
	for _, ac := range availCurrencies {
		if ac == currency {
			isAllowed = true
			break
		}
	}
	if !isAllowed {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("currency %s is not available in budget", currency))
	}
	cadence := c.FormValue("recurrence_cadence")
	if cadence == "" {
		cadence = budget.CadenceMonthly
	}
	rInterval, _ := strconv.Atoi(c.FormValue("recurrence_interval"))
	if rInterval < 1 {
		rInterval, _ = strconv.Atoi(c.FormValue("recurrence_days"))
	}
	isCustom := cadence == budget.CadenceCustomDays || cadence == budget.CadenceCustomMonths || cadence == budget.CadenceCustomYears
	if isCustom && rInterval < 1 {
		switch cadence {
		case budget.CadenceCustomMonths:
			rInterval = 3
		case budget.CadenceCustomYears:
			rInterval = 1
		default:
			rInterval = 90
		}
	} else if !isCustom {
		rInterval = 0
	}

	var dueDate *int64
	if dueStr := strings.TrimSpace(c.FormValue("due_date")); dueStr != "" {
		if t, err := time.Parse("2006-01-02", dueStr); err == nil {
			u := t.UTC().UnixMicro()
			dueDate = &u
		}
	}
	var rDow *int
	if dowStr := strings.TrimSpace(c.FormValue("recurrence_day_of_week")); dowStr != "" {
		if v, err := strconv.Atoi(dowStr); err == nil && v >= 1 && v <= 7 {
			rDow = &v
		}
	}
	var rDom *int
	if domStr := strings.TrimSpace(c.FormValue("recurrence_day_of_month")); domStr != "" {
		if v, err := strconv.Atoi(domStr); err == nil && v >= 1 && v <= 31 {
			rDom = &v
		}
	}
	var rMoy *int
	if moyStr := strings.TrimSpace(c.FormValue("recurrence_month_of_year")); moyStr != "" {
		if v, err := strconv.Atoi(moyStr); err == nil && v >= 1 && v <= 12 {
			rMoy = &v
		}
	}

	// Compute next due date using RecurrenceSchedule if cadence is recurring and has schedule inputs
	loc := time.UTC
	if bTz, _ := h.tenantMgr.GetBudgetTimezone(c.Request().Context(), budgetID); bTz != "" {
		if l, err := time.LoadLocation(bTz); err == nil {
			loc = l
		}
	}
	nowInTz := h.now(loc)

	sched := budget.RecurrenceSchedule{
		Cadence:     cadence,
		Interval:    rInterval,
		DayOfWeek:   rDow,
		DayOfMonth:  rDom,
		MonthOfYear: rMoy,
	}
	if dueDate != nil {
		tVal := time.UnixMicro(*dueDate).In(loc)
		sched.AnchorDate = &tVal
	}
	if cadence != budget.CadenceOneOff && (rDow != nil || rDom != nil || dueDate != nil) {
		if nextDate, ok := sched.NextDueDate(nowInTz, loc); ok && !nextDate.IsZero() {
			u := nextDate.UnixMicro()
			dueDate = &u
		}
	}

	strategy := c.FormValue("non_spending_strategy")
	if strategy == "" {
		strategy = budget.StrategyRollOver
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	amountMode := c.FormValue("amount_mode")
	if amountMode == "" {
		amountMode = "FIXED"
	}
	var luaScript *string
	if amountMode == "LUA_FORMULA" {
		script := strings.TrimSpace(c.FormValue("lua_script"))
		if script != "" {
			luaScript = &script
		}
	}

	var ceilingMinor *int64
	if ceilingStr := strings.TrimSpace(c.FormValue("ceiling_amount")); ceilingStr != "" {
		var ceilFloat float64
		if _, err := fmt.Sscanf(ceilingStr, "%f", &ceilFloat); err == nil && ceilFloat > 0 {
			cMinor := int64(ceilFloat * 100)
			ceilingMinor = &cMinor
		}
	}

	_, err = budget.CreateTarget(c.Request().Context(), tenantDB, budget.CreateTargetInput{
		BucketID:              bucketID,
		Name:                  name,
		TargetType:            tType,
		Priority:              priority,
		AmountMode:            amountMode,
		AmountMinor:           amtMinor,
		LuaScript:             luaScript,
		Currency:              currency,
		RecurrenceCadence:     cadence,
		RecurrenceInterval:    rInterval,
		RecurrenceDayOfWeek:   rDow,
		RecurrenceDayOfMonth:  rDom,
		RecurrenceMonthOfYear: rMoy,
		DueDate:               dueDate,
		NonSpendingStrategy:   strategy,
		CeilingAmountMinor:    ceilingMinor,
		AgentID:               userID,
		CorrelationID:         corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "target_created", bucketID))
}

// UpdateTarget updates an existing target.
func (h *WorkspaceHandler) UpdateTarget(c echo.Context) error {
	budgetID := c.Param("budgetId")
	targetID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	var bucketID string
	_ = tenantDB.QueryRowContext(c.Request().Context(), "SELECT bucket_id FROM targets_projection WHERE id = ?", targetID).Scan(&bucketID)

	name := strings.TrimSpace(c.FormValue("name"))
	tType := c.FormValue("target_type")
	if tType == "" {
		tType = "EXPENSE"
	}
	priority, _ := strconv.Atoi(c.FormValue("priority"))
	if priority < 1 {
		priority = 2
	}
	cadence := c.FormValue("recurrence_cadence")
	if cadence == "" {
		cadence = budget.CadenceMonthly
	}
	rInterval, _ := strconv.Atoi(c.FormValue("recurrence_interval"))
	if rInterval < 1 {
		rInterval, _ = strconv.Atoi(c.FormValue("recurrence_days"))
	}
	isCustom := cadence == budget.CadenceCustomDays || cadence == budget.CadenceCustomMonths || cadence == budget.CadenceCustomYears
	if isCustom && rInterval < 1 {
		switch cadence {
		case budget.CadenceCustomMonths:
			rInterval = 3
		case budget.CadenceCustomYears:
			rInterval = 1
		default:
			rInterval = 90
		}
	} else if !isCustom {
		rInterval = 0
	}

	var dueDate *int64
	if dueStr := strings.TrimSpace(c.FormValue("due_date")); dueStr != "" {
		if t, err := time.Parse("2006-01-02", dueStr); err == nil {
			u := t.UTC().UnixMicro()
			dueDate = &u
		}
	}
	var rDow *int
	if dowStr := strings.TrimSpace(c.FormValue("recurrence_day_of_week")); dowStr != "" {
		if v, err := strconv.Atoi(dowStr); err == nil && v >= 1 && v <= 7 {
			rDow = &v
		}
	}
	var rDom *int
	if domStr := strings.TrimSpace(c.FormValue("recurrence_day_of_month")); domStr != "" {
		if v, err := strconv.Atoi(domStr); err == nil && v >= 1 && v <= 31 {
			rDom = &v
		}
	}
	var rMoy *int
	if moyStr := strings.TrimSpace(c.FormValue("recurrence_month_of_year")); moyStr != "" {
		if v, err := strconv.Atoi(moyStr); err == nil && v >= 1 && v <= 12 {
			rMoy = &v
		}
	}

	// Compute next due date using RecurrenceSchedule if cadence is recurring and has schedule inputs
	loc := time.UTC
	if bTz, _ := h.tenantMgr.GetBudgetTimezone(c.Request().Context(), budgetID); bTz != "" {
		if l, err := time.LoadLocation(bTz); err == nil {
			loc = l
		}
	}
	nowInTz := h.now(loc)

	sched := budget.RecurrenceSchedule{
		Cadence:     cadence,
		Interval:    rInterval,
		DayOfWeek:   rDow,
		DayOfMonth:  rDom,
		MonthOfYear: rMoy,
	}
	if dueDate != nil {
		tVal := time.UnixMicro(*dueDate).In(loc)
		sched.AnchorDate = &tVal
	}
	if cadence != budget.CadenceOneOff && (rDow != nil || rDom != nil || dueDate != nil) {
		if nextDate, ok := sched.NextDueDate(nowInTz, loc); ok && !nextDate.IsZero() {
			u := nextDate.UnixMicro()
			dueDate = &u
		}
	}

	strategy := c.FormValue("non_spending_strategy")
	if strategy == "" {
		strategy = budget.StrategyRollOver
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	amountMode := c.FormValue("amount_mode")
	if amountMode == "" {
		amountMode = "FIXED"
	}
	var luaScript *string
	if amountMode == "LUA_FORMULA" {
		script := strings.TrimSpace(c.FormValue("lua_script"))
		if script != "" {
			luaScript = &script
		}
	}

	var ceilingMinor *int64
	if ceilingStr := strings.TrimSpace(c.FormValue("ceiling_amount")); ceilingStr != "" {
		var ceilFloat float64
		if _, err := fmt.Sscanf(ceilingStr, "%f", &ceilFloat); err == nil && ceilFloat > 0 {
			cMinor := int64(ceilFloat * 100)
			ceilingMinor = &cMinor
		}
	}

	err := budget.UpdateTarget(c.Request().Context(), tenantDB, budget.UpdateTargetInput{
		ID:                    targetID,
		Name:                  name,
		TargetType:            tType,
		Priority:              priority,
		AmountMode:            amountMode,
		AmountMinor:           amtMinor,
		LuaScript:             luaScript,
		RecurrenceCadence:     cadence,
		RecurrenceInterval:    rInterval,
		RecurrenceDayOfWeek:   rDow,
		RecurrenceDayOfMonth:  rDom,
		RecurrenceMonthOfYear: rMoy,
		DueDate:               dueDate,
		NonSpendingStrategy:   strategy,
		CeilingAmountMinor:    ceilingMinor,
		AgentID:               userID,
		CorrelationID:         corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "target_updated", bucketID))
}

// DeleteTarget deletes (archives) a target.
func (h *WorkspaceHandler) DeleteTarget(c echo.Context) error {
	budgetID := c.Param("budgetId")
	targetID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	var bucketID string
	_ = tenantDB.QueryRowContext(c.Request().Context(), "SELECT bucket_id FROM targets_projection WHERE id = ?", targetID).Scan(&bucketID)

	err := budget.DeleteTarget(c.Request().Context(), tenantDB, targetID, userID, corrID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "target_deleted", bucketID))
}

// FundTarget funds a target's missing amount cascade-wise from Unassigned Buffer and Ready to Assign.
func (h *WorkspaceHandler) FundTarget(c echo.Context) error {
	budgetID := c.Param("budgetId")
	targetID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	var tz string
	_ = h.mainDB.QueryRowContext(c.Request().Context(), `SELECT timezone FROM budgets WHERE id = ?`, budgetID).Scan(&tz)

	horizon := c.FormValue("horizon")
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	nowInTz := h.now(loc)
	_, windowDays := budget.CalculateHorizonDays(horizon, nowInTz, loc)

	res, err := budget.AutoFundTarget(c.Request().Context(), tenantDB, budget.AutoFundTargetInput{
		TargetID:      targetID,
		WindowDays:    windowDays,
		Timezone:      tz,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	bucketID := ""
	if res != nil {
		bucketID = res.BucketID
	}

	return c.Redirect(http.StatusSeeOther, budgetRedirectURL(budgetID, c, "target_funded", bucketID))
}

// GenerateStructurePreview produces a proposed budget structure from an AI prompt.
func (h *WorkspaceHandler) GenerateStructurePreview(c echo.Context) error {
	budgetID := c.Param("budgetId")
	var req struct {
		Prompt   string `json:"prompt"`
		Currency string `json:"currency"`
	}
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Budget description is required"})
	}

	curr := strings.TrimSpace(req.Currency)
	if curr == "" {
		_ = h.mainDB.QueryRowContext(c.Request().Context(), `SELECT default_currency FROM budgets WHERE id = ?`, budgetID).Scan(&curr)
	}
	if curr == "" {
		curr = "PLN"
	}

	if h.structureAI == nil {
		h.structureAI = ai.NewBudgetStructureProvider()
	}

	existingGroups, _ := h.appService.GetExistingGroupNames(c.Request().Context(), budgetID)

	res, err := h.structureAI.GenerateBudgetStructure(c.Request().Context(), ai.BudgetStructurePromptRequest{
		Prompt:          req.Prompt,
		DefaultCurrency: curr,
		ExistingGroups:  existingGroups,
	})
	if err != nil {
		c.Set("error", err.Error())
		logger.Error(c.Request().Context(), "Failed generating budget structure preview with AI",
			"budget_id", budgetID,
			"error", err.Error(),
		)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.appService.DetectAndEnrichStructureConflicts(c.Request().Context(), budgetID, res); err != nil {
		logger.Warn(c.Request().Context(), "Failed enriching structure preview with conflicts", "budget_id", budgetID, "error", err.Error())
	}

	return c.JSON(http.StatusOK, res)
}

// PreviewSourceStructure reads and returns the existing structure of a source budget.
func (h *WorkspaceHandler) PreviewSourceStructure(c echo.Context) error {
	budgetID := c.Param("budgetId")
	sourceBudgetID := c.QueryParam("source_budget_id")
	if sourceBudgetID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source_budget_id is required"})
	}

	userID := c.Get(ContextUserIDKey).(string)
	var memberCount int
	err := h.mainDB.QueryRowContext(c.Request().Context(), `SELECT count(*) FROM budget_members WHERE budget_id = ? AND user_id = ?`, sourceBudgetID, userID).Scan(&memberCount)
	if err != nil || memberCount == 0 {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "Access denied to source budget"})
	}

	exported, err := h.appService.ExportBudgetStructure(c.Request().Context(), sourceBudgetID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed loading source structure"})
	}

	if err := h.appService.DetectAndEnrichStructureConflicts(c.Request().Context(), budgetID, exported); err != nil {
		logger.Warn(c.Request().Context(), "Failed enriching imported structure preview with conflicts", "budget_id", budgetID, "error", err.Error())
	}

	return c.JSON(http.StatusOK, exported)
}

// ApplyStructure applies a selected budget structure to the target budget.
func (h *WorkspaceHandler) ApplyStructure(c echo.Context) error {
	budgetID := c.Param("budgetId")
	userID := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationID").(string)

	var structure ai.BudgetStructure
	if err := c.Bind(&structure); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid structure payload"})
	}

	bCount, tCount, err := h.appService.ApplyBudgetStructure(c.Request().Context(), budgetID, &structure, userID, corrID)
	if err != nil {
		c.Set("error", err.Error())
		logger.Error(c.Request().Context(), "Failed applying budget structure",
			"budget_id", budgetID,
			"error", err.Error(),
		)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"success":         true,
		"buckets_created": bCount,
		"targets_created": tCount,
		"redirect":        fmt.Sprintf("/budgets/%s/budget", budgetID),
	})
}
