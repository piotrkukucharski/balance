package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/automation"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/history"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/web/templates"
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
	ID              string
	DateStr         string
	AmountMinor     int64
	AmountFormatted string
	Currency        string
	Notes           string
	NotesDisplay    string
	PartyName       string
	BucketName      string
	BucketDisplay   string
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
			o.AmountFormatted = fmt.Sprintf("%.2f", float64(o.AmountMinor)/100.0)
			o.NotesDisplay = o.Notes
			if o.NotesDisplay == "" {
				o.NotesDisplay = "Uncategorized Transaction"
			}
			o.BucketDisplay = o.BucketName
			if o.BucketDisplay == "" {
				o.BucketDisplay = "No bucket"
			}
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

type dryRunViewItem struct {
	Index           int
	ErrorMsg        string
	Matched         bool
	TxInfo          string
	ChangesDescHTML template.HTML
}

type dryRunViewData struct {
	TestedCount   int
	MatchCount    int
	NonMatchCount int
	Items         []dryRunViewItem
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

	var viewItems []dryRunViewItem
	for i, res := range results {
		if res.err != nil {
			viewItems = append(viewItems, dryRunViewItem{
				Index:    i + 1,
				ErrorMsg: res.err.Error(),
			})
			continue
		}

		if !res.matched {
			txInfo := ""
			if res.txCtx != nil {
				txInfo = fmt.Sprintf(" — %s (%.2f %s)", res.testedNotes, float64(res.txCtx.SourceAmountMinor)/100.0, res.txCtx.SourceCurrency)
			}
			viewItems = append(viewItems, dryRunViewItem{
				Index:   i + 1,
				Matched: false,
				TxInfo:  txInfo,
			})
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
			txInfo = fmt.Sprintf("%s (%.2f %s)", res.testedNotes, float64(res.txCtx.SourceAmountMinor)/100.0, res.txCtx.SourceCurrency)
		}

		viewItems = append(viewItems, dryRunViewItem{
			Index:           i + 1,
			Matched:         true,
			TxInfo:          txInfo,
			ChangesDescHTML: template.HTML(changesDesc.String()),
		})
	}

	out, err := templates.RenderString("automations/dry_run", dryRunViewData{
		TestedCount:   len(txIDs),
		MatchCount:    matchCount,
		NonMatchCount: nonMatchCount,
		Items:         viewItems,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering dry-run results")
	}
	return c.HTML(http.StatusOK, out)
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

type batchDryRunChangeItem struct {
	ActionBadgeHTML template.HTML
	OriginalNotes   string
	OriginalBucket  string
	NewBucket       string
}

type batchDryRunViewData struct {
	ProcessedCount int
	ModifiedCount  int
	SplitCount     int
	DeletedCount   int
	Changes        []batchDryRunChangeItem
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

	var changes []batchDryRunChangeItem
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
		changes = append(changes, batchDryRunChangeItem{
			ActionBadgeHTML: template.HTML(actionBadge),
			OriginalNotes:   ch.OriginalNotes,
			OriginalBucket:  origB,
			NewBucket:       newB,
		})
	}

	out, err := templates.RenderString("automations/batch_dry_run", batchDryRunViewData{
		ProcessedCount: res.ProcessedCount,
		ModifiedCount:  res.ModifiedCount,
		SplitCount:     res.SplitCount,
		DeletedCount:   res.DeletedCount,
		Changes:        changes,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering batch dry-run")
	}

	return c.HTML(http.StatusOK, out)
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

type automationListItemView struct {
	Index       int
	ID          string
	Name        string
	IsActive    bool
	Version     int
	JudgeScript string
	JSONAttr    template.JS
}

type automationsListViewData struct {
	BudgetID    string
	CSRFToken   string
	Automations []automationListItemView
}

func renderAutomationsListMarkup(budgetID string, automations []automation.Automation, csrfToken string) string {
	var items []automationListItemView
	for i, a := range automations {
		b, _ := json.Marshal(a)
		items = append(items, automationListItemView{
			Index:       i + 1,
			ID:          a.ID,
			Name:        a.Name,
			IsActive:    a.IsActive,
			Version:     a.Version,
			JudgeScript: a.JudgeScript,
			JSONAttr:    template.JS(b),
		})
	}

	out, err := templates.RenderString("automations/list", automationsListViewData{
		BudgetID:    budgetID,
		CSRFToken:   csrfToken,
		Automations: items,
	})
	if err != nil {
		return ""
	}
	return out
}

type automationsPageViewData struct {
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	ListMarkup              template.HTML
	RecentTxs               []RecentTxOption
}

func renderAutomationsPageMarkup(budgetID string, automations []automation.Automation, recentTxs []RecentTxOption, csrfToken string, c echo.Context) string {
	dFormat, tFormat := GetUserDateTimePreferences(c)
	sidebar := RenderSidebarNavigation(budgetID, "automations")
	dock := RenderBottomDock(budgetID, "automations")
	listMarkup := renderAutomationsListMarkup(budgetID, automations, csrfToken)

	data := automationsPageViewData{
		BudgetID:       budgetID,
		BudgetName:     "Automations",
		CSRFToken:      csrfToken,
		DateFormat:     dFormat,
		TimeFormat:     tFormat,
		SidebarHTML:    template.HTML(sidebar),
		BottomDockHTML: template.HTML(dock),
		ListMarkup:     template.HTML(listMarkup),
		RecentTxs:      recentTxs,
	}

	out, err := templates.RenderString("automations/view", data)
	if err != nil {
		return ""
	}
	return out
}
