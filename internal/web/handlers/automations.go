package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/automation"
	"github.com/balance/balance/internal/db"
	"github.com/labstack/echo/v4"
)

type AutomationsHandler struct {
	mainDB     *sql.DB
	tenantMgr  *db.TenantDBManager
	aiProvider ai.AutomationAIProvider
}

func NewAutomationsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *AutomationsHandler {
	return &AutomationsHandler{
		mainDB:     mainDB,
		tenantMgr:  tenantMgr,
		aiProvider: ai.NewAutomationAIProvider(),
	}
}

// RecentTxOption represents a budget transaction candidate for dry-run testing.
type RecentTxOption struct {
	ID          string
	DateStr     string
	AmountMinor int64
	Currency    string
	Notes       string
	BucketName  string
}

func fetchRecentTransactionsForPicker(ctx echo.Context, tenantDB *sql.DB, limit int) []RecentTxOption {
	rows, err := tenantDB.QueryContext(ctx.Request().Context(), `
		SELECT bt.id, bt.source_amount_minor, bt.source_currency, bt.transaction_date,
		       COALESCE(bt.notes, ''), COALESCE(b.name, '')
		FROM budget_transactions_current bt
		LEFT JOIN buckets_projection b ON bt.bucket_id = b.id
		ORDER BY bt.transaction_date DESC, bt.updated_at DESC
		LIMIT ?`, limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var list []RecentTxOption
	for rows.Next() {
		var o RecentTxOption
		var dateMicro int64
		if err := rows.Scan(&o.ID, &o.AmountMinor, &o.Currency, &dateMicro, &o.Notes, &o.BucketName); err == nil {
			o.DateStr = time.UnixMicro(dateMicro).UTC().Format("2006-01-02")
			list = append(list, o)
		}
	}
	return list
}

// AutomationsView renders the complete Automations page with execution list and modals.
func (h *AutomationsHandler) AutomationsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	automations, err := automation.ListAutomations(c.Request().Context(), tenantDB)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading automations: "+err.Error())
	}

	recentTxs := fetchRecentTransactionsForPicker(c, tenantDB, 20)
	csrfToken, _ := c.Get("csrfToken").(string)

	isHTMX := c.Request().Header.Get("HX-Request") == "true"
	if isHTMX && c.QueryParam("partial") == "list" {
		return c.HTML(http.StatusOK, renderAutomationsListMarkup(budgetID, automations, csrfToken))
	}

	pageMarkup := renderAutomationsPageMarkup(budgetID, automations, recentTxs, csrfToken, c)
	return c.HTML(http.StatusOK, pageMarkup)
}

// CreateAutomation handles POST /budgets/:budgetId/automations.
func (h *AutomationsHandler) CreateAutomation(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationId").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	judgeScript := strings.TrimSpace(c.FormValue("judge_script"))
	executorScript := strings.TrimSpace(c.FormValue("executor_script"))
	comment := strings.TrimSpace(c.FormValue("comment"))

	if name == "" || judgeScript == "" || executorScript == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name, Judge Script, and Executor Script are required")
	}

	_, err := automation.CreateAutomation(c.Request().Context(), tenantDB, automation.CreateAutomationInput{
		Name:           name,
		JudgeScript:    judgeScript,
		ExecutorScript: executorScript,
		Comment:        comment,
		AgentID:        userID,
		CorrelationID:  corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Failed creating automation: "+err.Error())
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		automations, _ := automation.ListAutomations(c.Request().Context(), tenantDB)
		csrfToken, _ := c.Get("csrfToken").(string)
		c.Response().Header().Set("HX-Trigger", "closeAutomationModal")
		return c.HTML(http.StatusOK, renderAutomationsListMarkup(budgetID, automations, csrfToken))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/automations", budgetID))
}

// UpdateAutomation handles POST /budgets/:budgetId/automations/:id/update.
func (h *AutomationsHandler) UpdateAutomation(c echo.Context) error {
	budgetID := c.Param("budgetId")
	autoID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationId").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	judgeScript := strings.TrimSpace(c.FormValue("judge_script"))
	executorScript := strings.TrimSpace(c.FormValue("executor_script"))
	comment := strings.TrimSpace(c.FormValue("comment"))
	squash := c.FormValue("squash") == "on" || c.FormValue("squash") == "true"

	if name == "" || judgeScript == "" || executorScript == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name, Judge Script, and Executor Script are required")
	}

	_, err := automation.UpdateAutomation(c.Request().Context(), tenantDB, automation.UpdateAutomationInput{
		ID:             autoID,
		Name:           name,
		JudgeScript:    judgeScript,
		ExecutorScript: executorScript,
		Comment:        comment,
		Squash:         squash,
		AgentID:        userID,
		CorrelationID:  corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Failed updating automation: "+err.Error())
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		automations, _ := automation.ListAutomations(c.Request().Context(), tenantDB)
		csrfToken, _ := c.Get("csrfToken").(string)
		c.Response().Header().Set("HX-Trigger", "closeAutomationModal")
		return c.HTML(http.StatusOK, renderAutomationsListMarkup(budgetID, automations, csrfToken))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/automations", budgetID))
}

// DeleteAutomation handles POST /budgets/:budgetId/automations/:id/delete.
func (h *AutomationsHandler) DeleteAutomation(c echo.Context) error {
	budgetID := c.Param("budgetId")
	autoID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationId").(string)

	if err := automation.DeleteAutomation(c.Request().Context(), tenantDB, autoID, userID, corrID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Failed deleting automation: "+err.Error())
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		automations, _ := automation.ListAutomations(c.Request().Context(), tenantDB)
		csrfToken, _ := c.Get("csrfToken").(string)
		return c.HTML(http.StatusOK, renderAutomationsListMarkup(budgetID, automations, csrfToken))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/automations", budgetID))
}

// ToggleAutomation handles POST /budgets/:budgetId/automations/:id/toggle.
func (h *AutomationsHandler) ToggleAutomation(c echo.Context) error {
	budgetID := c.Param("budgetId")
	autoID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	_, err := automation.ToggleAutomation(c.Request().Context(), tenantDB, autoID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Failed toggling automation: "+err.Error())
	}

	automations, _ := automation.ListAutomations(c.Request().Context(), tenantDB)
	csrfToken, _ := c.Get("csrfToken").(string)
	return c.HTML(http.StatusOK, renderAutomationsListMarkup(budgetID, automations, csrfToken))
}

// MoveAutomation handles POST /budgets/:budgetId/automations/:id/move?dir=up|down.
func (h *AutomationsHandler) MoveAutomation(c echo.Context) error {
	budgetID := c.Param("budgetId")
	autoID := c.Param("id")
	dir := c.QueryParam("dir")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	if err := automation.MoveAutomation(c.Request().Context(), tenantDB, autoID, dir); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Failed moving automation: "+err.Error())
	}

	automations, _ := automation.ListAutomations(c.Request().Context(), tenantDB)
	csrfToken, _ := c.Get("csrfToken").(string)
	return c.HTML(http.StatusOK, renderAutomationsListMarkup(budgetID, automations, csrfToken))
}

// DryRun handles POST /budgets/:budgetId/automations/dry-run.
func (h *AutomationsHandler) DryRun(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	judgeScript := strings.TrimSpace(c.FormValue("judge_script"))
	executorScript := strings.TrimSpace(c.FormValue("executor_script"))
	txID := strings.TrimSpace(c.FormValue("target_tx_id"))
	if txID == "" {
		txID = strings.TrimSpace(c.FormValue("tx_id"))
	}

	if txID == "" {
		return c.HTML(http.StatusOK, `<div class="alert alert-warning py-2 text-xs">⚠️ Please select a transaction to test against.</div>`)
	}

	matched, outcome, txCtx, err := automation.DryRunAutomation(c.Request().Context(), tenantDB, judgeScript, executorScript, txID)
	if err != nil {
		return c.HTML(http.StatusOK, fmt.Sprintf(`<div class="alert alert-error py-2 text-xs">❌ Execution Error: %s</div>`, html.EscapeString(err.Error())))
	}

	testedNotes := "No notes"
	if txCtx.Notes != nil && *txCtx.Notes != "" {
		testedNotes = *txCtx.Notes
	}
	if !matched {
		return c.HTML(http.StatusOK, fmt.Sprintf(`
			<div class="alert alert-neutral py-2 text-xs flex justify-between items-center">
				<span>🔍 <strong>Judge Result:</strong> Did NOT match this transaction.</span>
				<span class="badge badge-sm badge-neutral">No Action</span>
			</div>
			<div class="text-[11px] text-base-content/60 mt-1">Transaction tested: %s (%s, %.2f %s)</div>`,
			html.EscapeString(testedNotes),
			txCtx.SourceCurrency, float64(txCtx.SourceAmountMinor)/100.0, txCtx.SourceCurrency,
		))
	}

	var changesHTML strings.Builder
	changesHTML.WriteString(`<div class="alert alert-success py-2 text-xs mb-2">✅ <strong>Judge Match!</strong> Script condition matched.</div>`)

	if outcome.Deleted {
		changesHTML.WriteString(`<div class="badge badge-error gap-1 text-xs">🗑️ Transaction would be DELETED</div>`)
	} else if len(outcome.Splits) > 0 {
		changesHTML.WriteString(fmt.Sprintf(`<div class="font-bold text-xs mb-1 text-info">✂️ Split into %d parts:</div><ul class="text-xs list-disc pl-4 space-y-1">`, len(outcome.Splits)))
		for i, sp := range outcome.Splits {
			bName := "No bucket"
			if sp.BucketID != nil {
				bName = *sp.BucketID
			}
			noteStr := ""
			if sp.Notes != nil {
				noteStr = fmt.Sprintf(" - %q", *sp.Notes)
			}
			changesHTML.WriteString(fmt.Sprintf(`<li>Part %d: <strong>%.2f</strong> to bucket <em>%s</em>%s</li>`, i+1, float64(sp.AmountMinor)/100.0, html.EscapeString(bName), html.EscapeString(noteStr)))
		}
		changesHTML.WriteString(`</ul>`)
	} else if outcome.Modified {
		changesHTML.WriteString(`<div class="font-bold text-xs mb-1 text-success">📝 Proposed modifications:</div><ul class="text-xs list-disc pl-4 space-y-1">`)
		origBucket := txCtx.BucketName
		newBucket := outcome.UpdatedTx.BucketName
		if origBucket != newBucket {
			changesHTML.WriteString(fmt.Sprintf(`<li>Bucket: <del class="opacity-60">%s</del> → <strong class="text-primary">%s</strong></li>`, html.EscapeString(origBucket), html.EscapeString(newBucket)))
		}
		origTarget := txCtx.TargetName
		newTarget := outcome.UpdatedTx.TargetName
		if origTarget != newTarget {
			changesHTML.WriteString(fmt.Sprintf(`<li>Target: <del class="opacity-60">%s</del> → <strong class="text-primary">%s</strong></li>`, html.EscapeString(origTarget), html.EscapeString(newTarget)))
		}
		origNotes := ""
		if txCtx.Notes != nil {
			origNotes = *txCtx.Notes
		}
		newNotes := ""
		if outcome.UpdatedTx.Notes != nil {
			newNotes = *outcome.UpdatedTx.Notes
		}
		if origNotes != newNotes {
			changesHTML.WriteString(fmt.Sprintf(`<li>Notes: <del class="opacity-60">%s</del> → <strong class="text-primary">%s</strong></li>`, html.EscapeString(origNotes), html.EscapeString(newNotes)))
		}
		changesHTML.WriteString(`</ul>`)
	} else {
		changesHTML.WriteString(`<div class="text-xs text-base-content/70">Executor ran without modifying transaction fields.</div>`)
	}

	return c.HTML(http.StatusOK, changesHTML.String())
}

// AIGenerate handles POST /budgets/:budgetId/automations/ai/generate.
func (h *AutomationsHandler) AIGenerate(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	mode := c.FormValue("mode") // "judge", "executor", "pair"
	prompt := strings.TrimSpace(c.FormValue("prompt"))

	if prompt == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Prompt is required")
	}

	snap, _ := automation.LoadSnapshot(c.Request().Context(), tenantDB)

	switch mode {
	case "judge":
		script, err := h.aiProvider.GenerateJudge(c.Request().Context(), prompt, snap)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "AI generation failed: "+err.Error())
		}
		return c.String(http.StatusOK, script)
	case "executor":
		script, err := h.aiProvider.GenerateExecutor(c.Request().Context(), prompt, snap)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "AI generation failed: "+err.Error())
		}
		return c.String(http.StatusOK, script)
	case "pair":
		pair, err := h.aiProvider.GeneratePair(c.Request().Context(), prompt, snap)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "AI generation failed: "+err.Error())
		}
		return c.JSON(http.StatusOK, pair)
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid mode")
	}
}

// AIComment handles POST /budgets/:budgetId/automations/ai/comment.
func (h *AutomationsHandler) AIComment(c echo.Context) error {
	var req ai.VersionCommentRequest
	req.AutomationName = c.FormValue("name")
	req.OldJudgeScript = c.FormValue("old_judge_script")
	req.NewJudgeScript = c.FormValue("new_judge_script")
	req.OldExecutorScript = c.FormValue("old_executor_script")
	req.NewExecutorScript = c.FormValue("new_executor_script")

	comment, err := h.aiProvider.GenerateComment(c.Request().Context(), req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "AI comment generation failed: "+err.Error())
	}
	return c.String(http.StatusOK, comment)
}

// BatchDryRun handles POST /budgets/:budgetId/automations/batch-dry-run.
func (h *AutomationsHandler) BatchDryRun(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationId").(string)

	rawTxIDs := c.FormValue("tx_ids")
	var txIDs []string
	if rawTxIDs != "" {
		txIDs = strings.Split(rawTxIDs, ",")
	}
	if len(txIDs) == 0 {
		return c.HTML(http.StatusOK, `<div class="alert alert-warning py-2 text-xs">⚠️ No transactions selected.</div>`)
	}

	res, err := automation.RunAutomationsBatch(c.Request().Context(), tenantDB, txIDs, true, userID, corrID)
	if err != nil {
		return c.HTML(http.StatusOK, fmt.Sprintf(`<div class="alert alert-error py-2 text-xs">❌ Pipeline Error: %s</div>`, html.EscapeString(err.Error())))
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`
		<div class="mb-4">
			<div class="stats stats-horizontal shadow-sm border border-base-300 w-full mb-3 text-center">
				<div class="stat p-3">
					<div class="stat-title text-xs">Selected</div>
					<div class="stat-value text-lg">%d</div>
				</div>
				<div class="stat p-3 text-success">
					<div class="stat-title text-xs">Modified</div>
					<div class="stat-value text-lg">%d</div>
				</div>
				<div class="stat p-3 text-info">
					<div class="stat-title text-xs">Splits</div>
					<div class="stat-value text-lg">%d</div>
				</div>
				<div class="stat p-3 text-error">
					<div class="stat-title text-xs">Deleted</div>
					<div class="stat-value text-lg">%d</div>
				</div>
			</div>`,
		res.ProcessedCount, res.ModifiedCount, res.SplitCount, res.DeletedCount,
	))

	if len(res.Changes) == 0 {
		b.WriteString(`<div class="alert alert-neutral text-xs">ℹ️ None of the selected transactions matched active automation rules.</div>`)
	} else {
		b.WriteString(`
			<div class="overflow-x-auto max-h-60 rounded border border-base-300">
				<table class="table table-xs table-pin-rows">
					<thead>
						<tr>
							<th>Action</th>
							<th>Notes</th>
							<th>Bucket Change</th>
						</tr>
					</thead>
					<tbody>`)
		for _, ch := range res.Changes {
			actionBadge := `<span class="badge badge-success badge-xs">MODIFIED</span>`
			if ch.Action == "DELETED" {
				actionBadge = `<span class="badge badge-error badge-xs">DELETE</span>`
			} else if ch.Action == "SPLIT" {
				actionBadge = fmt.Sprintf(`<span class="badge badge-info badge-xs">SPLIT (%d)</span>`, ch.SplitsCount)
			}
			origB := ch.OriginalBucket
			if origB == "" {
				origB = "None"
			}
			newB := ch.NewBucket
			if newB == "" {
				newB = "None"
			}
			b.WriteString(fmt.Sprintf(`
				<tr>
					<td>%s</td>
					<td class="font-mono text-[11px] truncate max-w-xs">%s</td>
					<td class="text-xs">%s → <strong class="text-primary">%s</strong></td>
				</tr>`,
				actionBadge, html.EscapeString(ch.OriginalNotes),
				html.EscapeString(origB),
				html.EscapeString(newB),
			))
		}
		b.WriteString(`</tbody></table></div>`)
	}
	b.WriteString(`</div>`)

	return c.HTML(http.StatusOK, b.String())
}

// BatchRun handles POST /budgets/:budgetId/automations/batch-run (permanent execution).
func (h *AutomationsHandler) BatchRun(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationId").(string)

	rawTxIDs := c.FormValue("tx_ids")
	var txIDs []string
	if rawTxIDs != "" {
		txIDs = strings.Split(rawTxIDs, ",")
	}
	if len(txIDs) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "No transactions selected")
	}

	res, err := automation.RunAutomationsBatch(c.Request().Context(), tenantDB, txIDs, false, userID, corrID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Failed executing automations: "+err.Error())
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		c.Response().Header().Set("HX-Trigger", "automationsApplied")
		return c.String(http.StatusOK, fmt.Sprintf("Applied automations to %d transactions (%d modified, %d split, %d deleted)",
			res.ProcessedCount, res.ModifiedCount, res.SplitCount, res.DeletedCount))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

func renderAutomationsListMarkup(budgetID string, automations []automation.Automation, csrfToken string) string {
	if len(automations) == 0 {
		return `
		<div class="card bg-base-100 border border-base-300 p-8 text-center rounded-2xl shadow-sm">
			<div class="text-4xl mb-2">⚡</div>
			<h3 class="font-bold text-lg mb-1">No automations configured</h3>
			<p class="text-xs text-base-content/60 max-w-md mx-auto mb-4">
				Automations run Lua scripts to categorize transactions into buckets, attach targets, split receipts, or clean up payees automatically.
			</p>
			<div>
				<button class="btn btn-primary btn-sm gap-2" onclick="openNewAutomationModal()">
					<span>+</span> Create First Automation
				</button>
			</div>
		</div>`
	}

	var b strings.Builder
	b.WriteString(`<div id="automations-list" class="space-y-3">`)

	for i, a := range automations {
		activeChecked := ""
		if a.IsActive {
			activeChecked = " checked"
		}
		statusBadge := `<span class="badge badge-xs badge-ghost">Inactive</span>`
		if a.IsActive {
			statusBadge = `<span class="badge badge-xs badge-success">Active</span>`
		}

		b.WriteString(fmt.Sprintf(`
		<div class="card bg-base-100 border border-base-300 shadow-xs rounded-xl p-4 transition-all hover:border-primary/40">
			<div class="flex items-center justify-between gap-4 flex-wrap">
				<div class="flex items-center gap-3">
					<div class="flex flex-col gap-0.5">
						<button class="btn btn-ghost btn-xs p-0.5 h-auto min-h-0"
						        hx-post="/budgets/%s/automations/%s/move?dir=up"
						        hx-target="#automations-list-container"
						        hx-headers='{"X-CSRF-Token": "%s"}'
						        title="Move Up">▲</button>
						<button class="btn btn-ghost btn-xs p-0.5 h-auto min-h-0"
						        hx-post="/budgets/%s/automations/%s/move?dir=down"
						        hx-target="#automations-list-container"
						        hx-headers='{"X-CSRF-Token": "%s"}'
						        title="Move Down">▼</button>
					</div>
					<div class="badge badge-neutral font-mono text-xs">#%d</div>
					<div>
						<div class="flex items-center gap-2">
							<a href="javascript:void(0)" onclick='openEditAutomationModal(%s)' class="font-bold text-base hover:text-primary transition-colors">
								%s
							</a>
							%s
							<span class="badge badge-xs badge-outline font-mono">v%d</span>
						</div>
						<div class="text-xs text-base-content/60 font-mono mt-0.5 truncate max-w-md">
							Judge: <span class="text-info">%s</span>
						</div>
					</div>
				</div>

				<div class="flex items-center gap-3">
					<input type="checkbox" class="toggle toggle-success toggle-sm"%s
					       hx-post="/budgets/%s/automations/%s/toggle"
					       hx-target="#automations-list-container"
					       hx-headers='{"X-CSRF-Token": "%s"}'
					       title="Toggle Active" />

					<button class="btn btn-ghost btn-sm" onclick='openEditAutomationModal(%s)'>
						✏️ Edit
					</button>

					<button class="btn btn-ghost btn-sm text-info"
					        hx-get="/budgets/%s/automations/%s/history"
					        hx-target="body" hx-swap="beforeend">
						🕒 History
					</button>

					<button class="btn btn-ghost btn-sm text-error" onclick="openDeleteAutomationModal('%s', '%s')">
						🗑️
					</button>
				</div>
			</div>
		</div>`,
			budgetID, a.ID, csrfToken,
			budgetID, a.ID, csrfToken,
			i+1,
			escapeJSONForAttr(a),
			html.EscapeString(a.Name),
			statusBadge,
			a.Version,
			html.EscapeString(a.JudgeScript),
			activeChecked,
			budgetID, a.ID, csrfToken,
			escapeJSONForAttr(a),
			budgetID, a.ID,
			a.ID, html.EscapeString(a.Name),
		))
	}

	b.WriteString(`</div>`)
	return b.String()
}

func escapeJSONForAttr(v any) string {
	b, _ := json.Marshal(v)
	return html.EscapeString(string(b))
}

func renderAutomationsPageMarkup(budgetID string, automations []automation.Automation, recentTxs []RecentTxOption, csrfToken string, c echo.Context) string {
	dFormat, tFormat := GetUserDateTimePreferences(c)
	sidebar := RenderSidebarNavigation(budgetID, "automations")
	dock := RenderBottomDock(budgetID, "automations")
	listMarkup := renderAutomationsListMarkup(budgetID, automations, csrfToken)

	var txPickerOptions strings.Builder
	for _, tx := range recentTxs {
		noteStr := tx.Notes
		if noteStr == "" {
			noteStr = "Uncategorized Transaction"
		}
		bName := tx.BucketName
		if bName == "" {
			bName = "No bucket"
		}
		txPickerOptions.WriteString(fmt.Sprintf(
			`<option value="%s">%s — %s (%.2f %s) [%s]</option>`,
			tx.ID, tx.DateStr, html.EscapeString(noteStr), float64(tx.AmountMinor)/100.0, tx.Currency,
			html.EscapeString(bName),
		))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark" data-date-format="%s" data-time-format="%s">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance — Automations</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col md:flex-row pb-20 md:pb-0">
    <!-- SIDEBAR -->
    <aside class="w-full md:w-64 bg-base-100 border-r border-base-300 p-4 flex flex-col gap-4">
        <div class="flex items-center gap-2 px-2">
            <span class="text-2xl font-black text-primary">💰</span>
            <span class="font-extrabold text-xl tracking-tight">Balance</span>
        </div>
        %s
    </aside>

    <!-- MAIN CONTENT -->
    <main class="flex-1 p-4 md:p-8 max-w-5xl mx-auto w-full">
        <div class="flex items-center justify-between mb-6 flex-wrap gap-4">
            <div>
                <h1 class="text-2xl md:text-3xl font-extrabold tracking-tight flex items-center gap-2">
                    <span>⚡</span> Automations
                </h1>
                <p class="text-xs text-base-content/60 mt-1">
                    Custom Lua rules (Judge + Executor) executing sequentially to automate transaction categories, splits, and updates.
                </p>
            </div>
            <div class="flex items-center gap-2">
                <button class="btn btn-primary btn-sm gap-2 shadow-sm" onclick="openNewAutomationModal()">
                    <span>+</span> New Automation
                </button>
            </div>
        </div>

        <!-- LIST CONTAINER -->
        <div id="automations-list-container">
            %s
        </div>
    </main>

    <!-- BOTTOM DOCK FOR MOBILE -->
    %s

    <!-- NEW / EDIT AUTOMATION MODAL -->
    <dialog id="automation_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6 max-w-2xl max-h-[90vh] overflow-y-auto">
            <div class="flex justify-between items-center mb-4">
                <h3 id="modal_title" class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>⚡</span> <span>New Automation</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>

            <!-- UNIFIED AI PROMPT GENERATOR -->
            <div class="bg-base-200/60 border border-primary/30 rounded-xl p-3 mb-4">
                <div class="flex items-center justify-between mb-1.5">
                    <span class="text-xs font-bold text-primary flex items-center gap-1.5">
                        <span>✨</span> AI Assistant: Generate Full Rule
                    </span>
                </div>
                <div class="flex gap-2">
                    <input type="text" id="ai_pair_prompt" placeholder="e.g. When transaction notes mention Uber, assign to Transport bucket" class="input input-sm input-bordered flex-1 text-xs" />
                    <button type="button" class="btn btn-primary btn-sm text-xs gap-1" onclick="generatePairWithAI()">
                        <span>🤖</span> Generate
                    </button>
                </div>
            </div>

            <form id="automation_form" method="POST" hx-post="/budgets/%s/automations" hx-target="#automations-list-container">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" id="form_auto_id" value="" />

                <!-- NAME -->
                <div class="form-control mb-3">
                    <label class="label py-1"><span class="label-text text-xs font-bold">Rule Name</span></label>
                    <input type="text" name="name" id="form_name" placeholder="e.g. Auto Categorize Uber Trips" required class="input input-sm input-bordered w-full text-xs font-semibold" />
                </div>

                <!-- JUDGE SCRIPT -->
                <div class="form-control mb-3">
                    <div class="flex items-center justify-between mb-1">
                        <label class="label py-0"><span class="label-text text-xs font-bold">1. Judge Script (Conditional trigger)</span></label>
                        <button type="button" class="btn btn-ghost btn-xs text-primary gap-1" onclick="generateJudgeWithAI()">
                            <span>🤖</span> AI Judge
                        </button>
                    </div>
                    <textarea name="judge_script" id="form_judge_script" rows="2" placeholder="text.contains(tx.notes, 'Uber')" required class="textarea textarea-bordered font-mono text-xs w-full"></textarea>
                    <div class="text-[10px] text-base-content/50 mt-1">Evaluates tx.* (notes, amount, party, bucket, date) and text.* helpers. Returns true to run Executor.</div>
                </div>

                <!-- EXECUTOR SCRIPT -->
                <div class="form-control mb-4">
                    <div class="flex items-center justify-between mb-1">
                        <label class="label py-0"><span class="label-text text-xs font-bold">2. Executor Script (Mutations & Actions)</span></label>
                        <button type="button" class="btn btn-ghost btn-xs text-primary gap-1" onclick="generateExecutorWithAI()">
                            <span>🤖</span> AI Executor
                        </button>
                    </div>
                    <textarea name="executor_script" id="form_executor_script" rows="3" placeholder="tx.set_bucket('Transport')" required class="textarea textarea-bordered font-mono text-xs w-full"></textarea>
                    <div class="text-[10px] text-base-content/50 mt-1">Actions: tx.set_bucket("Name"), tx.set_target("Target"), tx.set_notes("..."), tx.split({...}), tx.delete().</div>
                </div>

                <!-- DRY RUN VALIDATION & PREVIEW SECTION -->
                <div class="card bg-base-200/50 border border-base-300 p-3 rounded-xl mb-4">
                    <div class="text-xs font-bold mb-2 flex items-center justify-between">
                        <span>🧪 Validate & Test (Dry-Run Preview)</span>
                        <span class="text-[10px] text-base-content/60">No database writes</span>
                    </div>
                    <div class="flex gap-2 mb-2">
                        <select id="dry_run_tx_picker" class="select select-sm select-bordered flex-1 text-xs">
                            <option value="">-- Choose transaction to test --</option>
                            %s
                        </select>
                        <button type="button" class="btn btn-neutral btn-sm text-xs gap-1" onclick="runDryRunTest()">
                            <span>▶️</span> Test
                        </button>
                    </div>
                    <div id="dry_run_results" class="text-xs">
                        <div class="text-[11px] text-base-content/50 italic">Select a transaction and click Test to preview Judge and Executor results.</div>
                    </div>
                </div>

                <!-- VERSION COMMENT & SQUASH -->
                <div class="form-control mb-4">
                    <div class="flex items-center justify-between mb-1">
                        <label class="label py-0"><span class="label-text text-xs font-bold">Version Comment (Optional)</span></label>
                        <button type="button" class="btn btn-ghost btn-xs text-primary gap-1" onclick="generateCommentWithAI()">
                            <span>✨</span> AI Comment
                        </button>
                    </div>
                    <input type="text" name="comment" id="form_comment" placeholder="Describe what changed in this version" class="input input-sm input-bordered w-full text-xs" />
                </div>

                <!-- SQUASH CHECKBOX (HIDDEN ON V1 OR NEW) -->
                <div id="squash_container" class="form-control mb-5 hidden">
                    <label class="label cursor-pointer justify-start gap-3 py-1">
                        <input type="checkbox" name="squash" id="form_squash" class="checkbox checkbox-sm checkbox-warning" />
                        <div>
                            <span class="label-text text-xs font-bold">Squash with predecessor</span>
                            <div class="text-[10px] text-base-content/60">Replaces version N-1 and saves as current version, reducing version count.</div>
                        </div>
                    </label>
                </div>

                <div class="modal-action">
                    <button type="button" class="btn btn-ghost btn-sm" onclick="closeAutomationModal()">Cancel</button>
                    <button type="submit" id="modal_submit_btn" class="btn btn-primary btn-sm">Save Automation</button>
                </div>
            </form>
        </div>
    </dialog>

    <!-- DELETE CONFIRMATION MODAL -->
    <dialog id="delete_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2 text-error mb-2">
                <span>⚠️</span> <span>Delete Automation</span>
            </h3>
            <p class="text-xs text-base-content/70 mb-4">
                Are you sure you want to delete <strong id="delete_auto_name" class="text-base-content"></strong>? This will soft-delete the rule from active processing.
            </p>
            <form id="delete_form" method="POST" hx-target="#automations-list-container">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="modal-action">
                    <button type="button" class="btn btn-ghost btn-sm" onclick="document.getElementById('delete_modal').close()">Cancel</button>
                    <button type="submit" class="btn btn-error btn-sm">Yes, Delete</button>
                </div>
            </form>
        </div>
    </dialog>

    <script>
        const budgetID = "%s";
        const csrfToken = "%s";

        function openNewAutomationModal() {
            document.getElementById("modal_title").innerHTML = "<span>⚡</span> <span>New Automation</span>";
            document.getElementById("form_auto_id").value = "";
            document.getElementById("form_name").value = "";
            document.getElementById("form_judge_script").value = "";
            document.getElementById("form_executor_script").value = "";
            document.getElementById("form_comment").value = "";
            document.getElementById("squash_container").classList.add("hidden");
            document.getElementById("form_squash").checked = false;
            document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select a transaction and click Test to preview Judge and Executor results.</div>';

            const form = document.getElementById("automation_form");
            form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations");
            htmx.process(form);

            document.getElementById("automation_modal").showModal();
        }

        function openEditAutomationModal(auto) {
            document.getElementById("modal_title").innerHTML = "<span>✏️</span> <span>Edit Automation (v" + auto.version + ")</span>";
            document.getElementById("form_auto_id").value = auto.id;
            document.getElementById("form_name").value = auto.name;
            document.getElementById("form_judge_script").value = auto.judge_script;
            document.getElementById("form_executor_script").value = auto.executor_script;
            document.getElementById("form_comment").value = "";
            document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select a transaction and click Test to preview Judge and Executor results.</div>';

            // Squash is enabled only for versions > 1 (per /grill-me decision)
            if (auto.version > 1) {
                document.getElementById("squash_container").classList.remove("hidden");
                document.getElementById("form_squash").checked = false;
            } else {
                document.getElementById("squash_container").classList.add("hidden");
                document.getElementById("form_squash").checked = false;
            }

            const form = document.getElementById("automation_form");
            form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations/" + auto.id + "/update");
            htmx.process(form);

            document.getElementById("automation_modal").showModal();
        }

        function closeAutomationModal() {
            document.getElementById("automation_modal").close();
        }

        document.body.addEventListener("closeAutomationModal", function() {
            closeAutomationModal();
        });

        function openDeleteAutomationModal(id, name) {
            document.getElementById("delete_auto_name").innerText = name;
            const form = document.getElementById("delete_form");
            form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations/" + id + "/delete");
            htmx.process(form);
            document.getElementById("delete_modal").showModal();
        }

        async function generatePairWithAI() {
            const prompt = document.getElementById("ai_pair_prompt").value.trim();
            if (!prompt) return;
            const formData = new FormData();
            formData.append("mode", "pair");
            formData.append("prompt", prompt);

            try {
                const res = await fetch("/budgets/" + budgetID + "/automations/ai/generate", {
                    method: "POST",
                    headers: { "X-CSRF-Token": csrfToken },
                    body: formData
                });
                if (res.ok) {
                    const data = await res.json();
                    if (data.judge_script) document.getElementById("form_judge_script").value = data.judge_script;
                    if (data.executor_script) document.getElementById("form_executor_script").value = data.executor_script;
                    if (!document.getElementById("form_name").value) {
                        document.getElementById("form_name").value = prompt;
                    }
                }
            } catch (e) {
                console.error("AI Pair error:", e);
            }
        }

        async function generateJudgeWithAI() {
            const prompt = promptUserAI("Enter trigger condition (e.g. When transaction notes mention Lidl):");
            if (!prompt) return;
            const formData = new FormData();
            formData.append("mode", "judge");
            formData.append("prompt", prompt);

            const res = await fetch("/budgets/" + budgetID + "/automations/ai/generate", {
                method: "POST",
                headers: { "X-CSRF-Token": csrfToken },
                body: formData
            });
            if (res.ok) {
                document.getElementById("form_judge_script").value = await res.text();
            }
        }

        async function generateExecutorWithAI() {
            const prompt = promptUserAI("Enter action to perform (e.g. Set bucket to Groceries):");
            if (!prompt) return;
            const formData = new FormData();
            formData.append("mode", "executor");
            formData.append("prompt", prompt);

            const res = await fetch("/budgets/" + budgetID + "/automations/ai/generate", {
                method: "POST",
                headers: { "X-CSRF-Token": csrfToken },
                body: formData
            });
            if (res.ok) {
                document.getElementById("form_executor_script").value = await res.text();
            }
        }

        async function generateCommentWithAI() {
            const formData = new FormData();
            formData.append("name", document.getElementById("form_name").value);
            formData.append("new_judge_script", document.getElementById("form_judge_script").value);
            formData.append("new_executor_script", document.getElementById("form_executor_script").value);

            const res = await fetch("/budgets/" + budgetID + "/automations/ai/comment", {
                method: "POST",
                headers: { "X-CSRF-Token": csrfToken },
                body: formData
            });
            if (res.ok) {
                document.getElementById("form_comment").value = await res.text();
            }
        }

        function promptUserAI(msg) {
            return window.prompt(msg);
        }

        function runDryRunTest() {
            const txID = document.getElementById("dry_run_tx_picker").value;
            const judgeScript = document.getElementById("form_judge_script").value;
            const executorScript = document.getElementById("form_executor_script").value;
            if (!txID) {
                alert("Please select a transaction to test against.");
                return;
            }

            const formData = new FormData();
            formData.append("target_tx_id", txID);
            formData.append("judge_script", judgeScript);
            formData.append("executor_script", executorScript);

            fetch("/budgets/" + budgetID + "/automations/dry-run", {
                method: "POST",
                headers: { "X-CSRF-Token": csrfToken },
                body: formData
            })
            .then(res => res.text())
            .then(html => {
                document.getElementById("dry_run_results").innerHTML = html;
            });
        }
    </script>
</body>
</html>`,
		dFormat, tFormat, sidebar, listMarkup, dock,
		budgetID, csrfToken, txPickerOptions.String(), csrfToken, budgetID, csrfToken,
	)
}
