package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/history"
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
	// Standalone page wrap for direct navigation or non-HTMX clients
	page := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark" data-date-format="%s" data-time-format="%s">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="user-date-format" content="%s">
    <meta name="user-time-format" content="%s">
    <title>Balance — History of %s</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col justify-center items-center p-4">
    <div class="w-full max-w-2xl bg-base-100 rounded-2xl shadow-xl border border-base-300 p-6">
        <div class="mb-4">
            <a href="/budgets/%s/budget" class="btn btn-ghost btn-sm gap-2">
                ← Back to Budget
            </a>
        </div>
        %s
    </div>
    %s
</body>
</html>`, dFormat, tFormat, dFormat, tFormat, histResult.EntityName, budgetID, drawerHTML, DateTimeFormatterScript())

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
	var sb strings.Builder

	sb.WriteString(`<div id="history_drawer_backdrop" class="fixed inset-0 z-50 bg-black/60 backdrop-blur-xs flex justify-end" onclick="if(event.target === this) this.remove()">`)
	sb.WriteString(`<div class="w-full max-w-xl h-full bg-base-100 border-l border-base-300 shadow-2xl flex flex-col overflow-y-auto p-6 animate-in slide-in-from-right duration-200">`)

	// Header
	sb.WriteString(`
        <div class="flex items-start justify-between pb-4 border-b border-base-300 mb-4">
            <div>
                <div class="flex items-center gap-2 mb-1">
                    <span class="badge badge-neutral text-xs font-mono uppercase tracking-wider">` + escape(hist.EntityType) + `</span>
                    ` + statusBadge(hist.IsDeleted, hist.CurrentVersion) + `
                </div>
                <h3 class="font-extrabold text-xl tracking-tight text-base-content break-words">` + escape(hist.EntityName) + `</h3>
                <div class="text-xs text-base-content/50 font-mono mt-0.5">ID: ` + escape(hist.EntityID) + `</div>
            </div>
            <button type="button" class="btn btn-sm btn-circle btn-ghost" onclick="document.getElementById('history_drawer_backdrop')?.remove()" aria-label="Close">✕</button>
        </div>
    `)

	// Flash message if present
	if flashMsg != "" {
		sb.WriteString(`
            <div class="alert alert-success text-sm py-2 mb-4 flex items-center gap-2 shadow-xs">
                <span>✓</span>
                <span>` + escape(flashMsg) + `</span>
            </div>
        `)
	}

	if !canRestore {
		sb.WriteString(`
            <div class="alert alert-info text-xs py-2 mb-4 bg-info/10 text-info border-info/20">
                <span>ℹ</span>
                <span>Viewing history. Restoring prior versions requires ADMIN or OWNER role.</span>
            </div>
        `)
	}

	// Timeline
	sb.WriteString(`<div class="flex-1">`)

	if hist.EntityType == "automations" && len(hist.Items) >= 2 {
		sb.WriteString(fmt.Sprintf(`
            <div id="multi_squash_panel" class="bg-base-200 border border-primary/30 p-3 rounded-xl mb-4 text-xs">
                <div class="flex items-center justify-between mb-2">
                    <span class="font-bold flex items-center gap-1.5 text-primary">
                        <span>🗜️</span> Multi-Version Squash
                    </span>
                    <span id="squash_selection_badge" class="badge badge-sm badge-neutral">Select versions below</span>
                </div>
                <form id="multi_squash_form" hx-post="/budgets/%s/automations/%s/squash" hx-target="#history_drawer_backdrop" hx-swap="outerHTML" class="space-y-2">
                    <input type="hidden" name="csrf_token" value="%s">
                    <input type="hidden" id="squash_start_ver" name="start_version" value="">
                    <input type="hidden" id="squash_end_ver" name="end_version" value="">
                    <div class="flex gap-2">
                        <input type="text" id="squash_comment_input" name="comment" placeholder="Summary comment for squashed versions..." class="input input-xs input-bordered flex-1" />
                        <button type="button" id="btn_ai_squash_comment" class="btn btn-xs btn-outline btn-secondary gap-1" disabled onclick="generateAISquashComment('%s', '%s')">
                            ✨ AI Summary
                        </button>
                    </div>
                    <div class="flex justify-between items-center pt-1">
                        <span class="text-[11px] text-base-content/60">Selected range will be consolidated into the highest version.</span>
                        <button type="submit" id="btn_submit_squash" class="btn btn-xs btn-primary font-bold" disabled>
                            Squash Selected
                        </button>
                    </div>
                </form>
            </div>
        `, budgetID, hist.EntityID, csrfToken, budgetID, hist.EntityID))
	}

	sb.WriteString(`<ul class="timeline timeline-vertical timeline-compact">`)

	for _, item := range hist.Items {
		sb.WriteString(`<li>`)
		sb.WriteString(`<div class="timeline-middle">` + actionIcon(item.Action) + `</div>`)
		sb.WriteString(`<div class="timeline-end timeline-box w-full bg-base-200 border-base-300 p-4 mb-4 rounded-xl shadow-xs">`)

		// Version header
		versionCheck := ""
		if hist.EntityType == "automations" {
			versionCheck = fmt.Sprintf(`<input type="checkbox" class="checkbox checkbox-xs checkbox-primary squash-check mr-2" value="%d" onchange="handleSquashCheckChange()">`, item.Version)
		}
		sb.WriteString(`
            <div class="flex items-center justify-between gap-2 mb-2">
                <div class="flex items-center gap-1.5">
                    ` + versionCheck + `
                    <span class="font-extrabold font-mono text-sm text-primary">v` + fmt.Sprint(item.Version) + `</span>
                    ` + actionBadge(item.Action) + `
                    ` + currentBadge(item.IsCurrent) + `
                </div>
                <div class="text-xs text-base-content/50 font-mono">` + RenderTimeTag(item.CreatedAtTime, true, c) + `</div>
            </div>
        `)

		// Audit details
		sb.WriteString(`
            <div class="text-xs text-base-content/60 font-mono mb-3 flex flex-wrap gap-x-4 gap-y-1">
                <span>Agent: ` + shortenID(item.AgentID) + `</span>
                <span>Corr: ` + shortenID(item.CorrelationID) + `</span>
            </div>
        `)

		// Field Diffs / State
		if len(item.FieldDiffs) > 0 {
			sb.WriteString(`<div class="space-y-1.5 my-2">`)
			for _, diff := range item.FieldDiffs {
				if diff.FieldName == "judge_script" || diff.FieldName == "executor_script" {
					sb.WriteString(fmt.Sprintf(`
                        <div class="text-xs bg-base-100 p-2.5 rounded-lg border border-base-300/60 my-1">
                            <div class="flex justify-between items-center mb-1 font-semibold text-base-content/70">
                                <span>%s</span>
                                <span class="badge badge-xs badge-neutral font-mono">Lua</span>
                            </div>`, escape(diff.Label)))
					if diff.OldValue != "" {
						sb.WriteString(fmt.Sprintf(`
                            <div class="grid grid-cols-1 md:grid-cols-2 gap-2 mt-1 font-mono text-[11px]">
                                <div class="bg-error/10 border border-error/20 p-2 rounded text-error overflow-x-auto">
                                    <div class="font-bold text-[10px] uppercase opacity-70 mb-0.5">Previous</div>
                                    <pre class="whitespace-pre-wrap">%s</pre>
                                </div>
                                <div class="bg-success/10 border border-success/20 p-2 rounded text-success overflow-x-auto">
                                    <div class="font-bold text-[10px] uppercase opacity-70 mb-0.5">Updated</div>
                                    <pre class="whitespace-pre-wrap">%s</pre>
                                </div>
                            </div>`, escape(diff.OldValue), escape(diff.NewValue)))
					} else {
						sb.WriteString(fmt.Sprintf(`
                            <div class="bg-success/10 border border-success/20 p-2 rounded text-success font-mono text-[11px] overflow-x-auto">
                                <pre class="whitespace-pre-wrap">%s</pre>
                            </div>`, escape(diff.NewValue)))
					}
					sb.WriteString(`</div>`)
				} else {
					sb.WriteString(`
                        <div class="text-xs bg-base-100 p-2 rounded-lg border border-base-300/60 flex items-start justify-between gap-2">
                            <span class="font-semibold text-base-content/70">` + escape(diff.Label) + `:</span>
                            <div class="text-right font-mono">
                    `)
					if diff.OldValue != "" {
						sb.WriteString(`<span class="line-through text-error/70 mr-1.5">` + escape(diff.OldValue) + `</span> ➔ `)
					}
					sb.WriteString(`<span class="text-success font-bold">` + escape(diff.NewValue) + `</span>`)
					sb.WriteString(`</div></div>`)
				}
			}
			sb.WriteString(`</div>`)
		} else if item.Action == "SOFT_DELETE" {
			sb.WriteString(`<div class="text-xs text-error font-semibold italic my-1">Entity soft-deleted from current projection.</div>`)
		} else if item.Action == "RESTORE" {
			sb.WriteString(`<div class="text-xs text-primary font-semibold italic my-1">Restored from prior state.</div>`)
		} else if item.Action == "SQUASH" {
			sb.WriteString(`<div class="text-xs text-primary font-semibold italic my-1">Consolidated multi-version squash.</div>`)
		}

		// Action buttons (Restore & Load into Editor)
		var btnRow strings.Builder
		if hist.EntityType == "automations" {
			btnRow.WriteString(fmt.Sprintf(`
                <button type="button" class="btn btn-xs btn-outline btn-info gap-1" onclick="loadHistoricalVersionIntoEditor('%s', '%s', %d)">
                    📥 Load into Editor
                </button>
            `, budgetID, hist.EntityID, item.Version))
		}
		if canRestore && item.Action != "SOFT_DELETE" && (!item.IsCurrent || hist.IsDeleted) {
			restoreURL := fmt.Sprintf("/budgets/%s/history/%s/%s/restore", budgetID, hist.EntityType, hist.EntityID)
			btnRow.WriteString(fmt.Sprintf(`
                <form hx-post="%s" hx-target="#history_drawer_backdrop" hx-swap="outerHTML">
                    <input type="hidden" name="csrf_token" value="%s">
                    <input type="hidden" name="target_version" value="%d">
                    <button type="submit" class="btn btn-xs btn-outline btn-warning gap-1.5 font-bold">
                        ↺ Restore v%d
                    </button>
                </form>
            `, restoreURL, csrfToken, item.Version, item.Version))
		}

		if btnRow.Len() > 0 {
			sb.WriteString(fmt.Sprintf(`
                <div class="mt-3 pt-2 border-t border-base-300 flex items-center justify-end gap-2">
                    %s
                </div>
            `, btnRow.String()))
		}

		sb.WriteString(`</div>`)
		sb.WriteString(`<hr class="bg-base-300"/>`)
		sb.WriteString(`</li>`)
	}

	sb.WriteString(`</ul>`)
	sb.WriteString(`</div>`)

	// Footer close button
	sb.WriteString(`
        <div class="pt-4 border-t border-base-300 flex justify-end mt-4">
            <button type="button" class="btn btn-sm btn-ghost" onclick="document.getElementById('history_drawer_backdrop')?.remove()">
                Close
            </button>
        </div>
    `)

	if hist.EntityType == "automations" {
		sb.WriteString(fmt.Sprintf(`
            <script>
                function handleSquashCheckChange() {
                    const checks = Array.from(document.querySelectorAll('.squash-check:checked')).map(c => parseInt(c.value)).sort((a,b) => a - b);
                    const badge = document.getElementById('squash_selection_badge');
                    const btnAi = document.getElementById('btn_ai_squash_comment');
                    const btnSubmit = document.getElementById('btn_submit_squash');
                    const startInp = document.getElementById('squash_start_ver');
                    const endInp = document.getElementById('squash_end_ver');

                    if (checks.length < 2) {
                        badge.innerText = 'Select 2+ versions';
                        badge.className = 'badge badge-sm badge-neutral';
                        if (btnAi) btnAi.disabled = true;
                        if (btnSubmit) btnSubmit.disabled = true;
                        if (startInp) startInp.value = '';
                        if (endInp) endInp.value = '';
                        return;
                    }

                    const min = checks[0];
                    const max = checks[checks.length - 1];
                    const isContiguous = (max - min + 1) === checks.length;

                    if (!isContiguous) {
                        badge.innerText = '⚠️ Range must be contiguous';
                        badge.className = 'badge badge-sm badge-warning';
                        if (btnAi) btnAi.disabled = true;
                        if (btnSubmit) btnSubmit.disabled = true;
                        if (startInp) startInp.value = '';
                        if (endInp) endInp.value = '';
                        return;
                    }

                    badge.innerText = 'v' + min + ' .. v' + max + ' (' + checks.length + ' versions)';
                    badge.className = 'badge badge-sm badge-primary font-bold';
                    if (btnAi) btnAi.disabled = false;
                    if (btnSubmit) btnSubmit.disabled = false;
                    if (startInp) startInp.value = min;
                    if (endInp) endInp.value = max;
                }

                function generateAISquashComment(bID, aID) {
                    const startVer = document.getElementById('squash_start_ver').value;
                    const endVer = document.getElementById('squash_end_ver').value;
                    if (!startVer || !endVer) return;

                    const btn = document.getElementById('btn_ai_squash_comment');
                    const origText = btn.innerHTML;
                    btn.innerHTML = '⏳ Generating...';
                    btn.disabled = true;

                    const formData = new FormData();
                    formData.append('start_version', startVer);
                    formData.append('end_version', endVer);

                    fetch('/budgets/' + bID + '/automations/' + aID + '/ai/squash-comment', {
                        method: 'POST',
                        headers: { 'X-CSRF-Token': '%s' },
                        body: formData
                    })
                    .then(r => r.text())
                    .then(comment => {
                        document.getElementById('squash_comment_input').value = comment;
                    })
                    .catch(err => alert('AI comment error: ' + err))
                    .finally(() => {
                        btn.innerHTML = origText;
                        btn.disabled = false;
                    });
                }
            </script>
        `, csrfToken))
	}

	sb.WriteString(`</div></div>`)
	return sb.String()
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
		return `<span class="badge badge-neutral badge-sm font-semibold text-xs">` + action + `</span>`
	}
}

func statusBadge(isDeleted bool, curVer int) string {
	if isDeleted {
		return `<span class="badge badge-error badge-soft text-xs font-semibold">DELETED</span>`
	}
	return `<span class="badge badge-success badge-soft text-xs font-semibold">ACTIVE (v` + fmt.Sprint(curVer) + `)</span>`
}

func currentBadge(isCurrent bool) string {
	if isCurrent {
		return `<span class="badge badge-primary badge-outline text-xs font-bold">CURRENT</span>`
	}
	return ""
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
