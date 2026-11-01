package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/history"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

// HistoryHandler handles entity audit timeline and temporal rollback.
type HistoryHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

// NewHistoryHandler creates a new HistoryHandler.
func NewHistoryHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *HistoryHandler {
	return &HistoryHandler{
		mainDB:    mainDB,
		tenantMgr: tenantMgr,
	}
}

type historyPageViewData struct {
	Locale                  string
	NumberFormat            string
	DateFormat              string
	TimeFormat              string
	EntityName              string
	BudgetID                string
	DrawerHTML              template.HTML
	DateTimeFormatterScript template.HTML
}

type historyDiffViewItem struct {
	FieldName   string
	Label       string
	OldValue    string
	NewValue    string
	IsLuaScript bool
}

type historyTimelineViewItem struct {
	Version         int
	Action          string
	IsCurrent       bool
	ActionIconHTML  template.HTML
	ActionBadgeHTML template.HTML
	CreatedAtTag    template.HTML
	ShortAgentID    string
	ShortCorrID     string
	FieldDiffs      []historyDiffViewItem
	CanRestoreItem  bool
}

type historyDrawerViewData struct {
	BudgetID        string
	EntityType      string
	EntityID        string
	EntityName      string
	IsDeleted       bool
	CurrentVersion  int
	CanRestore      bool
	CSRFToken       string
	FlashMsg        string
	IsAutomations   bool
	ShowMultiSquash bool
	Items           []historyTimelineViewItem
}

// HistoryDrawer renders the side drawer with timeline and field diffs for an entity.
func (h *HistoryHandler) HistoryDrawer(c echo.Context) error {
	budgetID := c.Param("budgetId")
	entityType := c.Param("entityType")
	entityID := c.Param("entityId")
	if entityID == "" {
		entityID = c.Param("id")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	role, _ := c.Get(ContextRoleKey).(string)
	canRestore := role == "ADMIN" || role == "OWNER"

	histResult, err := history.GetEntityHistory(c.Request().Context(), tenantDB, entityType, entityID)
	if err != nil {
		if err == history.ErrNotFound {
			return echo.NewHTTPError(http.StatusNotFound, "Entity not found in audit history")
		}
		if err == history.ErrInvalidEntity {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid entity type for history")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading entity history: "+err.Error())
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	isHTMX := c.Request().Header.Get("HX-Request") == "true"

	drawerHTML := renderHistoryDrawerMarkup(budgetID, histResult, canRestore, csrfToken, "", c)

	if isHTMX {
		return c.HTML(http.StatusOK, drawerHTML)
	}

	dFormat, tFormat := GetUserDateTimePreferences(c)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	pageData := historyPageViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		EntityName:              histResult.EntityName,
		BudgetID:                budgetID,
		DrawerHTML:              template.HTML(drawerHTML),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	}
	page, err := templates.RenderString("history/page", pageData)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering history page: "+err.Error())
	}

	return c.HTML(http.StatusOK, page)
}

// RestoreVersion restores the entity to a previous historical version.
func (h *HistoryHandler) RestoreVersion(c echo.Context) error {
	budgetID := c.Param("budgetId")
	entityType := c.Param("entityType")
	entityID := c.Param("entityId")
	if entityID == "" {
		entityID = c.Param("id")
	}

	role, _ := c.Get(ContextRoleKey).(string)
	if role != "ADMIN" && role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Restoring historical entity versions requires ADMIN or OWNER role")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationID").(string)
	if corrID == "" {
		corrID = domain.NewUUIDv7()
	}

	// Read target_version from form, query, or JSON
	targetVerStr := c.FormValue("target_version")
	if targetVerStr == "" {
		targetVerStr = c.QueryParam("target_version")
	}
	var targetVersion int
	if targetVerStr != "" {
		v, err := strconv.Atoi(targetVerStr)
		if err == nil {
			targetVersion = v
		}
	} else {
		type jsonReq struct {
			TargetVersion int `json:"target_version"`
		}
		var j jsonReq
		if err := c.Bind(&j); err == nil {
			targetVersion = j.TargetVersion
		}
	}

	if targetVersion <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid or missing target_version")
	}

	now := time.Now().UTC().UnixMicro()
	res, err := history.RestoreEntityVersion(c.Request().Context(), tenantDB, entityType, entityID, targetVersion, userID, corrID, now)
	if err != nil {
		if err == history.ErrNotFound {
			return echo.NewHTTPError(http.StatusNotFound, "Target version not found")
		}
		if err == history.ErrSoftDelete {
			return echo.NewHTTPError(http.StatusBadRequest, "Cannot restore a soft-delete version; select an active version to restore")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to restore version: "+err.Error())
	}

	// If client expects JSON
	if strings.Contains(c.Request().Header.Get("Accept"), "application/json") {
		return c.JSON(http.StatusOK, res)
	}

	// If HTMX request, re-render drawer with success alert and trigger update
	isHTMX := c.Request().Header.Get("HX-Request") == "true"
	if isHTMX {
		histResult, _ := history.GetEntityHistory(c.Request().Context(), tenantDB, entityType, entityID)
		csrfToken, _ := c.Get("csrfToken").(string)
		msg := fmt.Sprintf("Version %d was successfully restored as v%d!", targetVersion, res.NewVersion)
		c.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"entityRestored":{"id":"%s","version":%d}}`, entityID, res.NewVersion))
		return c.HTML(http.StatusOK, renderHistoryDrawerMarkup(budgetID, histResult, true, csrfToken, msg, c))
	}

	// Standard redirect
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/history/%s/%s", budgetID, entityType, entityID))
}

func renderHistoryDrawerMarkup(budgetID string, hist *history.EntityHistoryResult, canRestore bool, csrfToken, flashMsg string, c echo.Context) string {
	var items []historyTimelineViewItem
	for _, item := range hist.Items {
		var diffs []historyDiffViewItem
		for _, d := range item.FieldDiffs {
			diffs = append(diffs, historyDiffViewItem{
				FieldName:   d.FieldName,
				Label:       d.Label,
				OldValue:    d.OldValue,
				NewValue:    d.NewValue,
				IsLuaScript: d.FieldName == "judge_script" || d.FieldName == "executor_script",
			})
		}

		items = append(items, historyTimelineViewItem{
			Version:         item.Version,
			Action:          item.Action,
			IsCurrent:       item.IsCurrent,
			ActionIconHTML:  template.HTML(actionIcon(item.Action)),
			ActionBadgeHTML: template.HTML(actionBadge(item.Action)),
			CreatedAtTag:    template.HTML(RenderTimeTag(item.CreatedAtTime, true, c)),
			ShortAgentID:    shortenID(item.AgentID),
			ShortCorrID:     shortenID(item.CorrelationID),
			FieldDiffs:      diffs,
			CanRestoreItem:  canRestore && item.Action != "SOFT_DELETE" && (!item.IsCurrent || hist.IsDeleted),
		})
	}

	data := historyDrawerViewData{
		BudgetID:        budgetID,
		EntityType:      hist.EntityType,
		EntityID:        hist.EntityID,
		EntityName:      hist.EntityName,
		IsDeleted:       hist.IsDeleted,
		CurrentVersion:  hist.CurrentVersion,
		CanRestore:      canRestore,
		CSRFToken:       csrfToken,
		FlashMsg:        flashMsg,
		IsAutomations:   hist.EntityType == "automations",
		ShowMultiSquash: hist.EntityType == "automations" && len(hist.Items) >= 2,
		Items:           items,
	}

	out, err := templates.RenderString("history/drawer", data)
	if err != nil {
		return ""
	}
	return out
}

func actionIcon(action string) string {
	switch action {
	case "CREATE":
		return `<div class="badge badge-success badge-xs p-1">●</div>`
	case "UPDATE":
		return `<div class="badge badge-info badge-xs p-1">●</div>`
	case "RESTORE":
		return `<div class="badge badge-warning badge-xs p-1">●</div>`
	case "SOFT_DELETE":
		return `<div class="badge badge-error badge-xs p-1">●</div>`
	default:
		return `<div class="badge badge-neutral badge-xs p-1">●</div>`
	}
}

func actionBadge(action string) string {
	switch action {
	case "CREATE":
		return `<span class="badge badge-success badge-sm font-semibold text-xs">CREATE</span>`
	case "UPDATE":
		return `<span class="badge badge-info badge-sm font-semibold text-xs">UPDATE</span>`
	case "RESTORE":
		return `<span class="badge badge-warning badge-sm font-semibold text-xs">RESTORE</span>`
	case "SOFT_DELETE":
		return `<span class="badge badge-error badge-sm font-semibold text-xs">SOFT_DELETE</span>`
	default:
		return `<span class="badge badge-neutral badge-sm font-semibold text-xs">` + escape(action) + `</span>`
	}
}

func shortenID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func escape(s string) string {
	r := strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;", "\"", "&quot;", "'", "&#39;")
	return r.Replace(s)
}
