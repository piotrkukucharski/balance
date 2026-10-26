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
	"github.com/balance/balance/internal/history"
	"github.com/balance/balance/internal/lua"
	"github.com/labstack/echo/v4"
	"strconv"
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
	PartyName   string
	BucketName  string
}

func fetchRecentTransactionsForPicker(ctx echo.Context, tenantDB *sql.DB, limit int) []RecentTxOption {
	rows, err := tenantDB.QueryContext(ctx.Request().Context(), `
		SELECT bt.id, bt.source_amount_minor, bt.source_currency, bt.transaction_date,
		       COALESCE(bt.notes, ''), COALESCE(b.name, ''),
		       COALESCE(p_src.name, COALESCE(p_dst.name, ''))
		FROM budget_transactions_current bt
		LEFT JOIN buckets_projection b ON bt.bucket_id = b.id
		LEFT JOIN accounts_current a_src ON bt.source_account_id = a_src.id
		LEFT JOIN parties_current p_src ON a_src.party_id = p_src.id AND p_src.is_user_owned = 0
		LEFT JOIN accounts_current a_dst ON bt.destination_account_id = a_dst.id
		LEFT JOIN parties_current p_dst ON a_dst.party_id = p_dst.id AND p_dst.is_user_owned = 0
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
		if err := rows.Scan(&o.ID, &o.AmountMinor, &o.Currency, &dateMicro, &o.Notes, &o.BucketName, &o.PartyName); err == nil {
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

	// Collect target transaction IDs (multi-transaction support)
	var txIDs []string
	if c.Request().Form == nil {
		_ = c.Request().ParseForm()
	}
	if ids, ok := c.Request().Form["target_tx_ids"]; ok {
		for _, id := range ids {
			for _, part := range strings.Split(id, ",") {
				p := strings.TrimSpace(part)
				if p != "" {
					txIDs = append(txIDs, p)
				}
			}
		}
	}
	if len(txIDs) == 0 {
		single := strings.TrimSpace(c.FormValue("target_tx_id"))
		if single == "" {
			single = strings.TrimSpace(c.FormValue("tx_id"))
		}
		if single != "" {
			for _, p := range strings.Split(single, ",") {
				part := strings.TrimSpace(p)
				if part != "" {
					txIDs = append(txIDs, part)
				}
			}
		}
	}

	if len(txIDs) == 0 {
		return c.HTML(http.StatusOK, `<div class="alert alert-warning py-2 text-xs">⚠️ Please select at least one transaction to test against.</div>`)
	}

	type dryRunResultItem struct {
		txID        string
		matched     bool
		outcome     *lua.AutomationOutcome
		txCtx       *lua.AutomationTxContext
		err         error
		testedNotes string
	}

	var results []dryRunResultItem
	matchCount := 0
	nonMatchCount := 0

	for _, id := range txIDs {
		matched, outcome, txCtx, err := automation.DryRunAutomation(c.Request().Context(), tenantDB, judgeScript, executorScript, id)
		testedNotes := "No notes"
		if txCtx != nil && txCtx.Notes != nil && *txCtx.Notes != "" {
			testedNotes = *txCtx.Notes
		}
		if err == nil {
			if matched {
				matchCount++
			} else {
				nonMatchCount++
			}
		}
		results = append(results, dryRunResultItem{
			txID:        id,
			matched:     matched,
			outcome:     outcome,
			txCtx:       txCtx,
			err:         err,
			testedNotes: testedNotes,
		})
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`
		<div class="mb-3">
			<div class="stats stats-horizontal shadow-xs border border-base-300 w-full mb-3 text-center">
				<div class="stat p-2">
					<div class="stat-title text-[10px]">Tested</div>
					<div class="stat-value text-base text-base-content">%d</div>
				</div>
				<div class="stat p-2">
					<div class="stat-title text-[10px]">Matched</div>
					<div class="stat-value text-base text-success">%d</div>
				</div>
				<div class="stat p-2">
					<div class="stat-title text-[10px]">No Match</div>
					<div class="stat-value text-base text-base-content/60">%d</div>
				</div>
			</div>
		</div>
		<div class="space-y-2 max-h-60 overflow-y-auto pr-1">`, len(txIDs), matchCount, nonMatchCount))

	for i, res := range results {
		if res.err != nil {
			b.WriteString(fmt.Sprintf(`
				<div class="alert alert-error py-1.5 px-3 text-xs">
					<span class="font-mono text-[10px]">#%d</span> ❌ Execution Error: %s
				</div>`, i+1, html.EscapeString(res.err.Error())))
			continue
		}

		if !res.matched {
			txInfo := ""
			if res.txCtx != nil {
				txInfo = fmt.Sprintf(" — %s (%.2f %s)", html.EscapeString(res.testedNotes), float64(res.txCtx.SourceAmountMinor)/100.0, res.txCtx.SourceCurrency)
			}
			b.WriteString(fmt.Sprintf(`
				<div class="card bg-base-200/40 border border-base-300 p-2 text-xs flex flex-row items-center justify-between gap-2">
					<div class="truncate">
						<span class="font-mono text-[10px] text-base-content/50">#%d</span>
						<span class="text-base-content/70">%s</span>
					</div>
					<span class="badge badge-xs badge-neutral shrink-0">No Match</span>
				</div>`, i+1, txInfo))
			continue
		}

		// Matched
		var changesDesc strings.Builder
		if res.outcome.Deleted {
			changesDesc.WriteString(`<span class="badge badge-xs badge-error">🗑️ Delete</span>`)
		} else if len(res.outcome.Splits) > 0 {
			changesDesc.WriteString(fmt.Sprintf(`<span class="badge badge-xs badge-info">✂️ Split into %d parts</span>`, len(res.outcome.Splits)))
		} else if res.outcome.Modified {
			var mods []string
			if res.txCtx.BucketName != res.outcome.UpdatedTx.BucketName {
				mods = append(mods, fmt.Sprintf(`Bucket: %s → <strong class="text-primary">%s</strong>`, html.EscapeString(res.txCtx.BucketName), html.EscapeString(res.outcome.UpdatedTx.BucketName)))
			}
			if res.txCtx.TargetName != res.outcome.UpdatedTx.TargetName {
				mods = append(mods, fmt.Sprintf(`Target: %s → <strong class="text-primary">%s</strong>`, html.EscapeString(res.txCtx.TargetName), html.EscapeString(res.outcome.UpdatedTx.TargetName)))
			}
			origNotes := ""
			if res.txCtx.Notes != nil {
				origNotes = *res.txCtx.Notes
			}
			newNotes := ""
			if res.outcome.UpdatedTx.Notes != nil {
				newNotes = *res.outcome.UpdatedTx.Notes
			}
			if origNotes != newNotes {
				mods = append(mods, fmt.Sprintf(`Notes: <del class="opacity-60">%s</del> → <strong class="text-primary">%s</strong>`, html.EscapeString(origNotes), html.EscapeString(newNotes)))
			}
			if len(mods) > 0 {
				changesDesc.WriteString(strings.Join(mods, ", "))
			} else {
				changesDesc.WriteString(`<span class="text-success">Matched (no fields modified)</span>`)
			}
		} else {
			changesDesc.WriteString(`<span class="text-success">Matched</span>`)
		}

		txInfo := ""
		if res.txCtx != nil {
			txInfo = fmt.Sprintf("%s (%.2f %s)", html.EscapeString(res.testedNotes), float64(res.txCtx.SourceAmountMinor)/100.0, res.txCtx.SourceCurrency)
		}

		b.WriteString(fmt.Sprintf(`
			<div class="card bg-success/5 border border-success/30 p-2 text-xs">
				<div class="flex items-center justify-between gap-2 mb-1">
					<div class="font-semibold truncate">
						<span class="font-mono text-[10px] text-base-content/50">#%d</span>
						<span>%s</span>
					</div>
					<span class="badge badge-xs badge-success shrink-0">Judge Match</span>
				</div>
				<div class="text-[11px] text-base-content/80 pl-4 border-l-2 border-success/40">
					%s
				</div>
			</div>`, i+1, txInfo, changesDesc.String()))
	}

	b.WriteString(`</div>`)
	return c.HTML(http.StatusOK, b.String())
}

// AIGenerate handles POST /budgets/:budgetId/automations/ai/generate.
func (h *AutomationsHandler) AIGenerate(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	mode := c.FormValue("mode") // "judge", "executor", "pair"
	prompt := strings.TrimSpace(c.FormValue("prompt"))
	existingJudge := strings.TrimSpace(c.FormValue("existing_judge"))
	existingExecutor := strings.TrimSpace(c.FormValue("existing_executor"))
	testResults := strings.TrimSpace(c.FormValue("test_results"))

	if prompt == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Prompt is required")
	}

	snap, _ := automation.LoadSnapshot(c.Request().Context(), tenantDB)

	// Collect example transactions if provided (e.g. from selected dry-run checkboxes)
	var exampleTxs []ai.TxContextExample
	rawExampleTxs := c.FormValue("example_txs")
	if rawExampleTxs != "" {
		_ = json.Unmarshal([]byte(rawExampleTxs), &exampleTxs)
	}

	// Also support tx_ids list passed to fetch example details automatically if example_txs not in body
	if len(exampleTxs) == 0 {
		rawTxIDs := c.FormValue("tx_ids")
		if rawTxIDs != "" {
			ids := strings.Split(rawTxIDs, ",")
			for _, id := range ids {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				txCtx, err := automation.ConvertBudgetTxToContext(id, tenantDB, snap)
				if err == nil && txCtx != nil {
					notes := ""
					if txCtx.Notes != nil {
						notes = *txCtx.Notes
					}
					exampleTxs = append(exampleTxs, ai.TxContextExample{
						ID:        txCtx.ID,
						Date:      time.UnixMicro(txCtx.TransactionDate).UTC().Format("2006-01-02"),
						Amount:    fmt.Sprintf("%.2f %s", float64(txCtx.SourceAmountMinor)/100.0, txCtx.SourceCurrency),
						Notes:     notes,
						Party:     txCtx.PartyName,
						Bucket:    txCtx.BucketName,
						IsOutflow: txCtx.IsUserOwnedSource && !txCtx.IsUserOwnedDest,
					})
				}
			}
		}
	}

	genCtx := ai.GenerateContext{
		Prompt:           prompt,
		ExistingJudge:    existingJudge,
		ExistingExecutor: existingExecutor,
		TestResults:      testResults,
		ExampleTxs:       exampleTxs,
	}

	switch mode {
	case "judge":
		script, err := h.aiProvider.GenerateJudge(c.Request().Context(), genCtx, snap)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "AI generation failed: "+err.Error())
		}
		return c.String(http.StatusOK, script)
	case "executor":
		script, err := h.aiProvider.GenerateExecutor(c.Request().Context(), genCtx, snap)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "AI generation failed: "+err.Error())
		}
		return c.String(http.StatusOK, script)
	case "pair":
		pair, err := h.aiProvider.GeneratePair(c.Request().Context(), genCtx, snap)
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

// SquashAutomation handles POST /budgets/:budgetId/automations/:id/squash.
func (h *AutomationsHandler) SquashAutomation(c echo.Context) error {
	budgetID := c.Param("budgetId")
	autoID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationId").(string)

	startVer, err := strconv.Atoi(c.FormValue("start_version"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid start_version")
	}
	endVer, err := strconv.Atoi(c.FormValue("end_version"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid end_version")
	}
	comment := strings.TrimSpace(c.FormValue("comment"))

	_, err = automation.SquashAutomationVersions(c.Request().Context(), tenantDB, automation.MultiSquashInput{
		ID:            autoID,
		StartVersion:  startVer,
		EndVersion:    endVer,
		Comment:       comment,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Squash failed: "+err.Error())
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		histResult, _ := history.GetEntityHistory(c.Request().Context(), tenantDB, "automations", autoID)
		csrfToken, _ := c.Get("csrfToken").(string)
		msg := fmt.Sprintf("Versions %d..%d were successfully squashed into v%d!", startVer, endVer, endVer)
		c.Response().Header().Set("HX-Trigger", `{"automationsApplied": true}`)
		return c.HTML(http.StatusOK, renderHistoryDrawerMarkup(budgetID, histResult, true, csrfToken, msg, c))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/automations/%s/history", budgetID, autoID))
}

// AISquashComment handles POST /budgets/:budgetId/automations/:id/ai/squash-comment.
func (h *AutomationsHandler) AISquashComment(c echo.Context) error {
	autoID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	startVer, err := strconv.Atoi(c.FormValue("start_version"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid start_version")
	}
	endVer, err := strconv.Atoi(c.FormValue("end_version"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid end_version")
	}

	var autoName string
	_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT name FROM automations_current WHERE id = ?`, autoID).Scan(&autoName)

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT version, comment, judge_script, executor_script
		FROM automations
		WHERE id = ? AND version BETWEEN ? AND ?
		ORDER BY version ASC`, autoID, startVer, endVer)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load versions: "+err.Error())
	}
	defer rows.Close()

	var versions []ai.AutomationVersionSummary
	for rows.Next() {
		var s ai.AutomationVersionSummary
		if err := rows.Scan(&s.Version, &s.Comment, &s.JudgeScript, &s.ExecutorScript); err == nil {
			versions = append(versions, s)
		}
	}

	comment, err := h.aiProvider.GenerateMultiVersionComment(c.Request().Context(), ai.MultiVersionCommentRequest{
		AutomationName: autoName,
		StartVersion:   startVer,
		EndVersion:     endVer,
		Versions:       versions,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "AI squash comment failed: "+err.Error())
	}
	return c.String(http.StatusOK, comment)
}

// LoadVersion handles GET /budgets/:budgetId/automations/:id/version/:ver/load.
func (h *AutomationsHandler) LoadVersion(c echo.Context) error {
	autoID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	verStr := c.Param("ver")
	ver, err := strconv.Atoi(verStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid version")
	}

	a, err := automation.GetAutomationVersion(c.Request().Context(), tenantDB, autoID, ver)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Version not found: "+err.Error())
	}

	return c.JSON(http.StatusOK, map[string]any{
		"id":              a.ID,
		"version":         a.Version,
		"name":            a.Name,
		"judge_script":    a.JudgeScript,
		"executor_script": a.ExecutorScript,
		"comment":         a.Comment,
	})
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

					<button class="btn btn-ghost btn-sm text-secondary" onclick='duplicateAutomation(%s)' title="Duplicate automation">
						📋 Duplicate
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
		partyStr := ""
		if tx.PartyName != "" {
			partyStr = fmt.Sprintf(" | %s", html.EscapeString(tx.PartyName))
		}
		txPickerOptions.WriteString(fmt.Sprintf(
			`<label class="label cursor-pointer justify-start gap-2 py-1 px-2 hover:bg-base-300/40 rounded border border-transparent hover:border-base-300">
				<input type="checkbox" name="target_tx_ids" value="%s" class="checkbox checkbox-xs checkbox-primary dry-run-tx-cb" />
				<span class="label-text text-xs flex-1 truncate">
					<span class="font-mono text-[10px] text-base-content/60 mr-1">%s</span>
					<strong class="font-medium text-base-content">%s</strong>
					<span class="text-base-content/60">(%.2f %s)</span>
					<span class="badge badge-xs badge-neutral">%s</span>%s
				</span>
			</label>`,
			tx.ID, tx.DateStr, html.EscapeString(noteStr), float64(tx.AmountMinor)/100.0, tx.Currency,
			html.EscapeString(bName), partyStr,
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
    <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
        <div class="flex items-center gap-2 px-2">
            <span class="text-2xl font-black text-primary">💰</span>
            <span class="font-extrabold text-xl tracking-tight">Balance</span>
        </div>
        %s
    </aside>

    <!-- MAIN CONTENT -->
    <main class="flex-1 p-4 sm:p-6 md:p-8 pb-24 md:pb-8 max-w-5xl mx-auto w-full">
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
                <div class="flex items-center gap-2">
                    <button type="button" id="modal_history_btn" class="btn btn-ghost btn-xs text-info gap-1 hidden" onclick="openHistoryFromModal()">
                        🕒 History
                    </button>
                    <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
                </div>
            </div>

            <!-- UNIFIED AI PROMPT GENERATOR -->
            <div class="bg-base-200/60 border border-primary/30 rounded-xl p-3 mb-4">
                <div class="flex items-center justify-between mb-1.5">
                    <span class="text-xs font-bold text-primary flex items-center gap-1.5">
                        <span>✨</span> AI Assistant: Generate Full Rule
                    </span>
                    <span class="text-[10px] text-base-content/60">Selected test transactions & existing code are sent as context</span>
                </div>
                <div class="flex flex-col gap-2">
                    <textarea id="ai_pair_prompt" rows="3" placeholder="Describe rule logic, e.g.:&#10;When notes mention Uber or Taxi, assign to Transport bucket. If amount is greater than 100, set note to 'High fare taxi'." class="textarea textarea-bordered textarea-sm w-full text-xs"></textarea>
                    <div class="flex justify-between items-center">
                        <span id="ai_few_shot_badge" class="text-[10px] text-info hidden"></span>
                        <button type="button" id="ai_generate_btn" class="btn btn-primary btn-sm text-xs gap-1 ml-auto" onclick="generatePairWithAI()">
                            <span>🤖</span> Generate Rule
                        </button>
                    </div>
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
                        <span>🧪 Multi-transaction Dry-Run & Few-Shot Examples</span>
                        <div class="flex items-center gap-2">
                            <button type="button" class="btn btn-ghost btn-xs text-[10px]" onclick="toggleSelectAllDryRun(true)">All</button>
                            <button type="button" class="btn btn-ghost btn-xs text-[10px]" onclick="toggleSelectAllDryRun(false)">None</button>
                            <span class="text-[10px] text-base-content/60">No database writes</span>
                        </div>
                    </div>
                    <div class="border border-base-300 bg-base-100 rounded-lg max-h-40 overflow-y-auto p-1 mb-2">
                        %s
                    </div>
                    <div class="flex justify-between items-center mb-2">
                        <span id="dry_run_selected_count" class="text-[11px] text-base-content/60">0 transactions selected</span>
                        <button type="button" class="btn btn-neutral btn-sm text-xs gap-1" onclick="runDryRunTest()">
                            <span>▶️</span> Run Dry-Run on Selected
                        </button>
                    </div>
                    <div id="dry_run_results" class="text-xs">
                        <div class="text-[11px] text-base-content/50 italic">Select one or more transactions and click Run Dry-Run to test Judge & Executor.</div>
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

        function getSelectedDryRunTxIDs() {
            const checked = document.querySelectorAll('.dry-run-tx-cb:checked');
            return Array.from(checked).map(cb => cb.value);
        }

        function updateDryRunSelectedCount() {
            const count = getSelectedDryRunTxIDs().length;
            const el = document.getElementById("dry_run_selected_count");
            if (el) el.innerText = count + " transaction" + (count === 1 ? "" : "s") + " selected";
            const badge = document.getElementById("ai_few_shot_badge");
            if (badge) {
                if (count > 0) {
                    badge.innerText = "ℹ️ " + count + " transaction(s) will be included as Few-Shot AI context";
                    badge.classList.remove("hidden");
                } else {
                    badge.classList.add("hidden");
                }
            }
        }

        function toggleSelectAllDryRun(select) {
            document.querySelectorAll('.dry-run-tx-cb').forEach(cb => cb.checked = select);
            updateDryRunSelectedCount();
        }

        document.addEventListener('change', function(e) {
            if (e.target && e.target.classList.contains('dry-run-tx-cb')) {
                updateDryRunSelectedCount();
            }
        });

        function duplicateAutomation(auto) {
            document.getElementById("modal_title").innerHTML = "<span>⚡</span> <span>New Automation (Copy)</span>";
            document.getElementById("form_auto_id").value = "";
            document.getElementById("form_name").value = "Kopia " + (auto.name || "");
            document.getElementById("form_judge_script").value = auto.judge_script || "";
            document.getElementById("form_executor_script").value = auto.executor_script || "";
            document.getElementById("form_comment").value = "Cloned from " + (auto.name || "existing rule");
            document.getElementById("squash_container").classList.add("hidden");
            document.getElementById("form_squash").checked = false;
            document.getElementById("modal_history_btn").classList.add("hidden");
            document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select one or more transactions and click Run Dry-Run to test Judge & Executor.</div>';

            const form = document.getElementById("automation_form");
            form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations");
            htmx.process(form);

            document.getElementById("automation_modal").showModal();
        }

        function openNewAutomationModal() {
            document.getElementById("modal_title").innerHTML = "<span>⚡</span> <span>New Automation</span>";
            document.getElementById("form_auto_id").value = "";
            document.getElementById("form_name").value = "";
            document.getElementById("form_judge_script").value = "";
            document.getElementById("form_executor_script").value = "";
            document.getElementById("form_comment").value = "";
            document.getElementById("ai_pair_prompt").value = "";
            document.getElementById("squash_container").classList.add("hidden");
            document.getElementById("form_squash").checked = false;
            document.getElementById("modal_history_btn").classList.add("hidden");
            document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select one or more transactions and click Run Dry-Run to test Judge & Executor.</div>';
            toggleSelectAllDryRun(false);

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
            document.getElementById("ai_pair_prompt").value = "";
            document.getElementById("modal_history_btn").classList.remove("hidden");
            document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select one or more transactions and click Run Dry-Run to test Judge & Executor.</div>';
            toggleSelectAllDryRun(false);

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

        function openHistoryFromModal() {
            const id = document.getElementById("form_auto_id").value;
            if (id) {
                htmx.ajax('GET', '/budgets/' + budgetID + '/automations/' + id + '/history', {target: 'body', swap: 'beforeend'});
            }
        }

        function loadHistoricalVersionIntoEditor(bID, aID, ver) {
            fetch('/budgets/' + bID + '/automations/' + aID + '/version/' + ver + '/load')
                .then(r => {
                    if (!r.ok) throw new Error("Failed loading version " + ver);
                    return r.json();
                })
                .then(data => {
                    document.getElementById("form_auto_id").value = data.id;
                    document.getElementById("form_name").value = data.name;
                    document.getElementById("form_judge_script").value = data.judge_script;
                    document.getElementById("form_executor_script").value = data.executor_script;
                    document.getElementById("form_comment").value = "Based on v" + ver;
                    document.getElementById("modal_title").innerHTML = "<span>✏️</span> <span>Edit Automation (loaded v" + ver + ")</span>";
                    document.getElementById("squash_container").classList.remove("hidden");
                    document.getElementById("form_squash").checked = false;
                    document.getElementById("modal_history_btn").classList.remove("hidden");

                    const form = document.getElementById("automation_form");
                    form.setAttribute("hx-post", "/budgets/" + bID + "/automations/" + data.id + "/update");
                    htmx.process(form);

                    document.getElementById("history_drawer_backdrop")?.remove();
                    document.getElementById("automation_modal").showModal();
                })
                .catch(err => alert(err.message));
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

            const btn = document.getElementById("ai_generate_btn");
            if (btn) btn.classList.add("loading");

            const formData = new FormData();
            formData.append("mode", "pair");
            formData.append("prompt", prompt);

            // Pass existing scripts if present
            const existingJudge = document.getElementById("form_judge_script").value.trim();
            const existingExecutor = document.getElementById("form_executor_script").value.trim();
            if (existingJudge) formData.append("existing_judge", existingJudge);
            if (existingExecutor) formData.append("existing_executor", existingExecutor);

            // Pass selected transactions as context
            const selectedTxIDs = getSelectedDryRunTxIDs();
            if (selectedTxIDs.length > 0) {
                formData.append("tx_ids", selectedTxIDs.join(","));
            }

            // Pass dry-run test results if available
            const resultsEl = document.getElementById("dry_run_results");
            if (resultsEl && resultsEl.innerText.trim() && !resultsEl.innerText.includes("Select one or more transactions")) {
                formData.append("test_results", resultsEl.innerText.trim());
            }

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
                        document.getElementById("form_name").value = prompt.length > 50 ? prompt.substring(0, 47) + "..." : prompt;
                    }
                } else {
                    const errText = await res.text();
                    alert("AI Generation failed: " + errText);
                }
            } catch (e) {
                console.error("AI Pair error:", e);
                alert("AI Pair error: " + e.message);
            } finally {
                if (btn) btn.classList.remove("loading");
            }
        }

        async function generateJudgeWithAI() {
            const prompt = promptUserAI("Enter trigger condition (e.g. When transaction notes mention Lidl):");
            if (!prompt) return;
            const formData = new FormData();
            formData.append("mode", "judge");
            formData.append("prompt", prompt);

            const existingJudge = document.getElementById("form_judge_script").value.trim();
            if (existingJudge) formData.append("existing_judge", existingJudge);

            const selectedTxIDs = getSelectedDryRunTxIDs();
            if (selectedTxIDs.length > 0) {
                formData.append("tx_ids", selectedTxIDs.join(","));
            }

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

            const existingExecutor = document.getElementById("form_executor_script").value.trim();
            if (existingExecutor) formData.append("existing_executor", existingExecutor);

            const selectedTxIDs = getSelectedDryRunTxIDs();
            if (selectedTxIDs.length > 0) {
                formData.append("tx_ids", selectedTxIDs.join(","));
            }

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
            const txIDs = getSelectedDryRunTxIDs();
            const judgeScript = document.getElementById("form_judge_script").value;
            const executorScript = document.getElementById("form_executor_script").value;
            if (txIDs.length === 0) {
                alert("Please select at least one transaction from the list above to test against.");
                return;
            }

            const formData = new FormData();
            txIDs.forEach(id => formData.append("target_tx_ids", id));
            formData.append("judge_script", judgeScript);
            formData.append("executor_script", executorScript);

            document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/60 flex items-center gap-1"><span class="loading loading-spinner loading-xs"></span> Evaluating rules against selected transactions...</div>';

            fetch("/budgets/" + budgetID + "/automations/dry-run", {
                method: "POST",
                headers: { "X-CSRF-Token": csrfToken },
                body: formData
            })
            .then(res => res.text())
            .then(html => {
                document.getElementById("dry_run_results").innerHTML = html;
            })
            .catch(err => {
                document.getElementById("dry_run_results").innerHTML = '<div class="alert alert-error py-1.5 px-3 text-xs">Error running dry run: ' + err.message + '</div>';
            });
        }
    </script>
</body>
</html>`,
		dFormat, tFormat, sidebar, listMarkup, dock,
		budgetID, csrfToken, txPickerOptions.String(), csrfToken, budgetID, csrfToken,
	)
}
