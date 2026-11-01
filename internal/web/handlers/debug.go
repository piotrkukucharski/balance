package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/gocardless"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

// DebugHandler handles diagnostic audit and corrective actions for budget owners.
type DebugHandler struct {
	mainDB           *sql.DB
	tenantMgr        *db.TenantDBManager
	gocardlessClient *gocardless.Client
	httpClient       *http.Client
}

// NewDebugHandler initializes a new DebugHandler.
func NewDebugHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *DebugHandler {
	secretID := os.Getenv("GOCARDLESS_SECRET_ID")
	secretKey := os.Getenv("GOCARDLESS_SECRET_KEY")
	redirectURL := os.Getenv("GOCARDLESS_REDIRECT_URL")
	var gcClient *gocardless.Client
	if secretID != "" && secretKey != "" {
		gcClient = gocardless.NewClient(secretID, secretKey, redirectURL)
	}

	return &DebugHandler{
		mainDB:           mainDB,
		tenantMgr:        tenantMgr,
		gocardlessClient: gcClient,
		httpClient:       &http.Client{Timeout: 15 * time.Second},
	}
}

// SetGoCardlessClient sets the GoCardless client (used in testing).
func (h *DebugHandler) SetGoCardlessClient(gc *gocardless.Client) {
	h.gocardlessClient = gc
}

// IntegrationAuditResult represents the standard diagnostic response for an external integration.
type IntegrationAuditResult struct {
	Success       bool   `json:"success"`
	StatusCode    int    `json:"status_code"`
	RawResponse   string `json:"raw_response"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlation_id"`
}

// OrphanedBalanceItem represents a record in account_currency_balances with no active account.
type OrphanedBalanceItem struct {
	AccountID    string `json:"account_id"`
	Currency     string `json:"currency"`
	BalanceMinor int64  `json:"balance_minor"`
}

// OrphanedTransactionItem represents a budget or raw transaction referencing a missing account.
type OrphanedTransactionItem struct {
	TransactionID string `json:"transaction_id"`
	Type          string `json:"type"` // "BUDGET" or "RAW"
	AccountID     string `json:"account_id"`
	Role          string `json:"role"` // "SOURCE", "DESTINATION", or "RAW_ACCOUNT"
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	NotesOrPayee  string `json:"notes_or_payee"`
	Date          int64  `json:"date"`
}

// OrphanedAccountSummary groups orphaned transactions per missing account.
type OrphanedAccountSummary struct {
	AccountID          string `json:"account_id"`
	AccountName        string `json:"account_name"` // From historical accounts table if available, else "(Unknown)"
	BudgetTxCount      int    `json:"budget_tx_count"`
	RawTxCount         int    `json:"raw_tx_count"`
	CanResurrect       bool   `json:"can_resurrect"` // true if present in accounts historical table
	LastRecordedStatus string `json:"last_recorded_status"`
}

// AccountDiscrepancyItem represents a difference between account_currency_balances and net sum of budget transactions.
type AccountDiscrepancyItem struct {
	AccountID     string `json:"account_id"`
	AccountName   string `json:"account_name"`
	Currency      string `json:"currency"`
	StoredMinor   int64  `json:"stored_minor"`
	ComputedMinor int64  `json:"computed_minor"`
	DiffMinor     int64  `json:"diff_minor"`
}

// IntegrityAuditReport represents the full status of all diagnostic checks.
type IntegrityAuditReport struct {
	OrphanedBalances      []OrphanedBalanceItem     `json:"orphaned_balances"`
	OrphanedTransactions  []OrphanedTransactionItem `json:"orphaned_transactions"`
	OrphanedAccounts      []OrphanedAccountSummary  `json:"orphaned_accounts"`
	AccountDiscrepancies  []AccountDiscrepancyItem  `json:"account_discrepancies"`
	FundamentalEquationOK bool                      `json:"fundamental_equation_ok"`
	FundamentalDetails    string                    `json:"fundamental_details"`
	HasAnomalies          bool                      `json:"has_anomalies"`
}

// RunAudit performs the read-only integrity audit checks.
func (h *DebugHandler) RunAudit(ctx context.Context, tenantDB *sql.DB) (IntegrityAuditReport, error) {
	var report IntegrityAuditReport

	// 1. Check orphaned records in account_currency_balances (account_id not in accounts_current)
	orphanedRows, err := tenantDB.QueryContext(ctx, `
		SELECT acb.account_id, acb.currency, acb.balance_minor
		FROM account_currency_balances acb
		LEFT JOIN accounts_current a ON acb.account_id = a.id
		WHERE a.id IS NULL`)
	if err != nil {
		return report, fmt.Errorf("failed querying orphaned balances: %w", err)
	}
	defer orphanedRows.Close()

	for orphanedRows.Next() {
		var item OrphanedBalanceItem
		if err := orphanedRows.Scan(&item.AccountID, &item.Currency, &item.BalanceMinor); err == nil {
			report.OrphanedBalances = append(report.OrphanedBalances, item)
			report.HasAnomalies = true
		}
	}

	// 1b. Check orphaned budget transactions (source_account_id or destination_account_id not in accounts_current)
	// Map to track accounts for summary
	acctSummaryMap := make(map[string]*OrphanedAccountSummary)

	bRows, err := tenantDB.QueryContext(ctx, `
		SELECT b.id, b.source_account_id, b.destination_account_id, b.source_amount_minor, b.source_currency,
		       b.destination_amount_minor, b.destination_currency, COALESCE(b.notes, ''), b.transaction_date,
		       sa.id, da.id
		FROM budget_transactions_current b
		LEFT JOIN accounts_current sa ON b.source_account_id = sa.id
		LEFT JOIN accounts_current da ON b.destination_account_id = da.id
		WHERE sa.id IS NULL OR da.id IS NULL`)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bID, srcID, dstID, srcCurr, dstCurr, notes string
			var srcAmt, dstAmt, txDate int64
			var saID, daID sql.NullString
			if err := bRows.Scan(&bID, &srcID, &dstID, &srcAmt, &srcCurr, &dstAmt, &dstCurr, &notes, &txDate, &saID, &daID); err == nil {
				if !saID.Valid {
					report.OrphanedTransactions = append(report.OrphanedTransactions, OrphanedTransactionItem{
						TransactionID: bID,
						Type:          "BUDGET",
						AccountID:     srcID,
						Role:          "SOURCE",
						AmountMinor:   srcAmt,
						Currency:      srcCurr,
						NotesOrPayee:  notes,
						Date:          txDate,
					})
					report.HasAnomalies = true
					if _, ok := acctSummaryMap[srcID]; !ok {
						acctSummaryMap[srcID] = &OrphanedAccountSummary{AccountID: srcID}
					}
					acctSummaryMap[srcID].BudgetTxCount++
				}
				if !daID.Valid {
					report.OrphanedTransactions = append(report.OrphanedTransactions, OrphanedTransactionItem{
						TransactionID: bID,
						Type:          "BUDGET",
						AccountID:     dstID,
						Role:          "DESTINATION",
						AmountMinor:   dstAmt,
						Currency:      dstCurr,
						NotesOrPayee:  notes,
						Date:          txDate,
					})
					report.HasAnomalies = true
					if _, ok := acctSummaryMap[dstID]; !ok {
						acctSummaryMap[dstID] = &OrphanedAccountSummary{AccountID: dstID}
					}
					acctSummaryMap[dstID].BudgetTxCount++
				}
			}
		}
	}

	// 1c. Check orphaned raw transactions (account_id not in accounts_current)
	rRows, err := tenantDB.QueryContext(ctx, `
		SELECT r.id, r.account_id, r.amount_minor, r.currency, r.raw_payee, r.booking_date
		FROM raw_transactions_current r
		LEFT JOIN accounts_current a ON r.account_id = a.id
		WHERE a.id IS NULL`)
	if err == nil {
		defer rRows.Close()
		for rRows.Next() {
			var rID, acctID, curr, payee string
			var amt, bDate int64
			if err := rRows.Scan(&rID, &acctID, &amt, &curr, &payee, &bDate); err == nil {
				report.OrphanedTransactions = append(report.OrphanedTransactions, OrphanedTransactionItem{
					TransactionID: rID,
					Type:          "RAW",
					AccountID:     acctID,
					Role:          "RAW_ACCOUNT",
					AmountMinor:   amt,
					Currency:      curr,
					NotesOrPayee:  payee,
					Date:          bDate,
				})
				report.HasAnomalies = true
				if _, ok := acctSummaryMap[acctID]; !ok {
					acctSummaryMap[acctID] = &OrphanedAccountSummary{AccountID: acctID}
				}
				acctSummaryMap[acctID].RawTxCount++
			}
		}
	}

	// Also include accounts from orphaned balances if not yet in summary
	for _, ob := range report.OrphanedBalances {
		if _, ok := acctSummaryMap[ob.AccountID]; !ok {
			acctSummaryMap[ob.AccountID] = &OrphanedAccountSummary{AccountID: ob.AccountID}
		}
	}

	// Populate metadata for orphaned accounts from historical accounts table
	for acctID, summary := range acctSummaryMap {
		var name, act string
		err := tenantDB.QueryRowContext(ctx, `
			SELECT name, action FROM accounts
			WHERE id = ?
			ORDER BY version DESC LIMIT 1`, acctID).Scan(&name, &act)
		if err == nil {
			summary.AccountName = name
			summary.CanResurrect = true
			summary.LastRecordedStatus = act
		} else {
			summary.AccountName = "(Unknown / Deleted History)"
			summary.CanResurrect = false
			summary.LastRecordedStatus = "UNKNOWN"
		}
		report.OrphanedAccounts = append(report.OrphanedAccounts, *summary)
	}

	// 2. Check transaction sum vs account_currency_balances for active user-owned accounts
	acctRows, err := tenantDB.QueryContext(ctx, `
		SELECT a.id, a.name, a.primary_currency, COALESCE(acb.balance_minor, 0)
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN account_currency_balances acb ON a.id = acb.account_id AND a.primary_currency = acb.currency
		WHERE p.is_user_owned = 1 AND a.is_closed = 0`)
	if err != nil {
		return report, fmt.Errorf("failed querying active accounts: %w", err)
	}
	defer acctRows.Close()

	type acctInfo struct {
		id          string
		name        string
		curr        string
		storedMinor int64
	}
	var accts []acctInfo
	for acctRows.Next() {
		var ai acctInfo
		if err := acctRows.Scan(&ai.id, &ai.name, &ai.curr, &ai.storedMinor); err == nil {
			accts = append(accts, ai)
		}
	}

	for _, a := range accts {
		// Calculate inflows (destination) minus outflows (source)
		var destSum, srcSum int64
		_ = tenantDB.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(destination_amount_minor), 0)
			FROM budget_transactions_current
			WHERE destination_account_id = ? AND destination_currency = ?`, a.id, a.curr).Scan(&destSum)

		_ = tenantDB.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(source_amount_minor), 0)
			FROM budget_transactions_current
			WHERE source_account_id = ? AND source_currency = ?`, a.id, a.curr).Scan(&srcSum)

		computedMinor := destSum - srcSum
		if computedMinor != a.storedMinor {
			report.AccountDiscrepancies = append(report.AccountDiscrepancies, AccountDiscrepancyItem{
				AccountID:     a.id,
				AccountName:   a.name,
				Currency:      a.curr,
				StoredMinor:   a.storedMinor,
				ComputedMinor: computedMinor,
				DiffMinor:     a.storedMinor - computedMinor,
			})
			report.HasAnomalies = true
		}
	}

	// 3. Fundamental equation check: SUM(Cash) == SUM(Buckets) + ReadyToAssign
	rtaMap, err := budget.CalculateReadyToAssign(ctx, tenantDB)
	if err != nil {
		report.FundamentalDetails = fmt.Sprintf("Error calculating Ready to Assign: %v", err)
		report.HasAnomalies = true
	} else {
		// Verify per currency
		report.FundamentalEquationOK = true
		var details []string
		for curr, rta := range rtaMap {
			var totalCash int64
			_ = tenantDB.QueryRowContext(ctx, `
				SELECT COALESCE(SUM(acb.balance_minor), 0)
				FROM account_currency_balances acb
				JOIN accounts_current a ON acb.account_id = a.id
				JOIN parties_current p ON a.party_id = p.id
				WHERE p.is_user_owned = 1 AND a.is_closed = 0 AND acb.currency = ?`, curr).Scan(&totalCash)

			var totalBuckets int64
			_ = tenantDB.QueryRowContext(ctx, `
				SELECT COALESCE(SUM(bcb.balance_minor), 0)
				FROM bucket_currency_balances bcb
				JOIN buckets_projection b ON bcb.bucket_id = b.id
				WHERE b.is_archived = 0 AND bcb.currency = ?`, curr).Scan(&totalBuckets)

			expected := totalBuckets + rta
			if totalCash != expected {
				report.FundamentalEquationOK = false
				report.HasAnomalies = true
				details = append(details, fmt.Sprintf("%s: Cash (%.2f) != Envelopes (%.2f) + RTA (%.2f)", curr, float64(totalCash)/100.0, float64(totalBuckets)/100.0, float64(rta)/100.0))
			} else {
				details = append(details, fmt.Sprintf("%s: Cash (%.2f) == Envelopes (%.2f) + RTA (%.2f) [Balanced]", curr, float64(totalCash)/100.0, float64(totalBuckets)/100.0, float64(rta)/100.0))
			}
		}
		if len(details) == 0 {
			report.FundamentalDetails = "No active currency balances found."
		} else {
			report.FundamentalDetails = strings.Join(details, "<br/>")
		}
	}

	return report, nil
}

type debugViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	AuditCardHTML           template.HTML
}

type debugOrphanedBalanceRow struct {
	AccountID        string
	Currency         string
	BalanceFormatted string
}

type debugOrphanedTxRow struct {
	Type            string
	Role            string
	AccountID       string
	NotesOrPayee    string
	AmountFormatted string
	Currency        string
}

type debugDiscrepancyRow struct {
	AccountName       string
	Currency          string
	StoredFormatted   string
	ComputedFormatted string
	DiffFormatted     string
}

type debugAuditCardViewData struct {
	BudgetID               string
	CSRFToken              string
	HasAnomalies           bool
	OrphanedBalances       []debugOrphanedBalanceRow
	OrphanedAccounts       []OrphanedAccountSummary
	OrphanedTransactions   []debugOrphanedTxRow
	AccountDiscrepancies   []debugDiscrepancyRow
	FundamentalEquationOK  bool
	FundamentalDetailsHTML template.HTML
}

// DebugView renders the GET /budgets/:budgetId/debug page.
func (h *DebugHandler) DebugView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Diagnostic tools are restricted to budget OWNER")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	var name string
	err := h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name FROM budgets WHERE id = ?`, budgetID).Scan(&name)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	sidebarHTML := RenderSidebarNavigation(budgetID, "debug", userLocale)
	bottomDockHTML := RenderBottomDock(budgetID, "settings", userLocale)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	// Run initial audit to render initial state
	report, _ := h.RunAudit(c.Request().Context(), tenantDB)
	auditHTML := RenderAuditCard(budgetID, csrfToken, report)

	data := debugViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		BudgetName:              name,
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		SidebarHTML:             template.HTML(sidebarHTML),
		BottomDockHTML:          template.HTML(bottomDockHTML),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
		AuditCardHTML:           template.HTML(auditHTML),
	}

	out, err := templates.RenderString("debug/view", data)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering debug view: "+err.Error())
	}
	return c.HTML(http.StatusOK, out)
}

// RenderAuditCard produces the HTML snippet for the integrity status card.
func RenderAuditCard(budgetID, csrfToken string, report IntegrityAuditReport) string {
	var obRows []debugOrphanedBalanceRow
	for _, o := range report.OrphanedBalances {
		obRows = append(obRows, debugOrphanedBalanceRow{
			AccountID:        o.AccountID,
			Currency:         o.Currency,
			BalanceFormatted: fmt.Sprintf("%.2f", float64(o.BalanceMinor)/100.0),
		})
	}

	var otRows []debugOrphanedTxRow
	for _, ot := range report.OrphanedTransactions {
		otRows = append(otRows, debugOrphanedTxRow{
			Type:            ot.Type,
			Role:            ot.Role,
			AccountID:       ot.AccountID,
			NotesOrPayee:    ot.NotesOrPayee,
			AmountFormatted: fmt.Sprintf("%.2f", float64(ot.AmountMinor)/100.0),
			Currency:        ot.Currency,
		})
	}

	var discRows []debugDiscrepancyRow
	for _, d := range report.AccountDiscrepancies {
		discRows = append(discRows, debugDiscrepancyRow{
			AccountName:       d.AccountName,
			Currency:          d.Currency,
			StoredFormatted:   fmt.Sprintf("%.2f", float64(d.StoredMinor)/100.0),
			ComputedFormatted: fmt.Sprintf("%.2f", float64(d.ComputedMinor)/100.0),
			DiffFormatted:     fmt.Sprintf("%.2f", float64(d.DiffMinor)/100.0),
		})
	}

	data := debugAuditCardViewData{
		BudgetID:               budgetID,
		CSRFToken:              csrfToken,
		HasAnomalies:           report.HasAnomalies,
		OrphanedBalances:       obRows,
		OrphanedAccounts:       report.OrphanedAccounts,
		OrphanedTransactions:   otRows,
		AccountDiscrepancies:   discRows,
		FundamentalEquationOK:  report.FundamentalEquationOK,
		FundamentalDetailsHTML: template.HTML(report.FundamentalDetails),
	}

	out, err := templates.RenderString("debug/audit_card", data)
	if err != nil {
		return ""
	}
	return out
}

// AuditAction handles POST /budgets/:budgetId/debug/audit (HTMX endpoint returning refreshed audit card).
func (h *DebugHandler) AuditAction(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Forbidden")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	csrfToken, _ := c.Get("csrfToken").(string)

	report, err := h.RunAudit(c.Request().Context(), tenantDB)
	if err != nil {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Audit error: `+templateEscape(err.Error())+`", "success": false}}`)
		return c.String(http.StatusInternalServerError, "Failed to run audit: "+err.Error())
	}

	msg := "Audyt integralności zakończony sukcesem."
	if report.HasAnomalies {
		msg = "Audyt zakończony: wykryto anomalie bilansowe."
	}
	c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "`+msg+`", "success": `+fmt.Sprintf("%t", !report.HasAnomalies)+`}}`)

	return c.HTML(http.StatusOK, RenderAuditCard(budgetID, csrfToken, report))
}

// CleanupOrphanedAction handles POST /budgets/:budgetId/debug/actions/cleanup-orphaned.
func (h *DebugHandler) CleanupOrphanedAction(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Forbidden")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	csrfToken, _ := c.Get("csrfToken").(string)

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error")
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(c.Request().Context(), `
		DELETE FROM account_currency_balances
		WHERE account_id NOT IN (SELECT id FROM accounts_current)`)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed deleting orphaned balances", "budget_id", budgetID, "error", err.Error())
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Błąd czyszczenia osieroconych sald", "success": false}}`)
		return c.String(http.StatusInternalServerError, "DB error: "+err.Error())
	}

	deletedCount, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Błąd zapisu transakcji czyszczenia", "success": false}}`)
		return c.String(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	report, _ := h.RunAudit(c.Request().Context(), tenantDB)
	msg := fmt.Sprintf("Wyczyszczono %d osieroconych rekordów sald.", deletedCount)
	c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "`+msg+`", "success": true}}`)

	return c.HTML(http.StatusOK, RenderAuditCard(budgetID, csrfToken, report))
}

// ResurrectAccountAction handles POST /budgets/:budgetId/debug/actions/resurrect-account.
// Recreates a soft-deleted account in accounts_current from the latest version in accounts history, set as is_closed = 1.
func (h *DebugHandler) ResurrectAccountAction(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Forbidden")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationID").(string)
	if corrID == "" {
		corrID = domain.NewUUIDv7()
	}
	csrfToken, _ := c.Get("csrfToken").(string)
	acctID := c.FormValue("account_id")
	if acctID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Missing account_id")
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error")
	}
	defer tx.Rollback()

	// 1. Fetch latest historical version of the account
	var maxVer int
	var partyID, name, aType, curr string
	var iban, favType, favVal sql.NullString
	err = tx.QueryRowContext(c.Request().Context(), `
		SELECT version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value
		FROM accounts
		WHERE id = ?
		ORDER BY version DESC LIMIT 1`, acctID).Scan(&maxVer, &partyID, &name, &aType, &curr, &iban, &favType, &favVal)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Konto nie istnieje w historii accounts", "success": false}}`)
			return c.String(http.StatusNotFound, "Account history not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error fetching account history: "+err.Error())
	}

	// 2. Ensure party exists in parties_current; if not, restore or create fallback
	var partyExists int
	_ = tx.QueryRowContext(c.Request().Context(), `SELECT count(*) FROM parties_current WHERE id = ?`, partyID).Scan(&partyExists)
	if partyExists == 0 {
		var pName, pType string
		var pUserOwned int
		pErr := tx.QueryRowContext(c.Request().Context(), `
			SELECT name, party_type, is_user_owned FROM parties
			WHERE id = ? ORDER BY version DESC LIMIT 1`, partyID).Scan(&pName, &pType, &pUserOwned)
		if pErr == nil {
			_, _ = tx.ExecContext(c.Request().Context(), `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, ?, ?, ?, ?, ?)`, partyID, pName, pType, pUserOwned, userID, now)
		} else {
			// Fallback user party
			_, _ = tx.ExecContext(c.Request().Context(), `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'Restored Party', 'INDIVIDUAL', 1, ?, ?)`, partyID, userID, now)
		}
	}

	nextVer := maxVer + 1
	fType := favType.String
	if fType == "" {
		fType = "EMOJI"
	}

	// 3. Append RESTORE record in accounts historical table with is_closed = 1
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'RESTORE', ?, ?, ?)`,
		acctID, nextVer, partyID, name, aType, curr, iban.String, fType, favVal.String, userID, corrID, now)
	if err != nil {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Błąd zapisu historii wskrzeszenia konta", "success": false}}`)
		return c.String(http.StatusInternalServerError, "Failed appending restore account: "+err.Error())
	}

	// 4. Upsert into accounts_current as closed (is_closed = 1)
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			party_id = excluded.party_id,
			name = excluded.name,
			account_type = excluded.account_type,
			primary_currency = excluded.primary_currency,
			iban = excluded.iban,
			favicon_type = excluded.favicon_type,
			favicon_value = excluded.favicon_value,
			is_closed = excluded.is_closed,
			agent_id = excluded.agent_id,
			updated_at = excluded.updated_at`,
		acctID, nextVer, partyID, name, aType, curr, iban.String, fType, favVal.String, userID, now)
	if err != nil {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Błąd przywracania accounts_current", "success": false}}`)
		return c.String(http.StatusInternalServerError, "Failed upserting accounts_current: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Błąd zatwierdzania transakcji wskrzeszenia", "success": false}}`)
		return c.String(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	report, _ := h.RunAudit(c.Request().Context(), tenantDB)
	msg := fmt.Sprintf("Konto \"%s\" zostało pomyślnie wskrzeszone jako zamknięte (is_closed=1).", name)
	c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "`+templateEscape(msg)+`", "success": true}}`)

	return c.HTML(http.StatusOK, RenderAuditCard(budgetID, csrfToken, report))
}

// DeleteOrphanedTransactionsAction handles POST /budgets/:budgetId/debug/actions/delete-orphaned-transactions.
// Atomically removes orphaned budget and raw transactions whose account_id is not in accounts_current.
func (h *DebugHandler) DeleteOrphanedTransactionsAction(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Forbidden")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	csrfToken, _ := c.Get("csrfToken").(string)

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error")
	}
	defer tx.Rollback()

	// 1. Find all budget transactions in budget_transactions_current referencing missing accounts
	bRows, err := tx.QueryContext(c.Request().Context(), `
		SELECT b.id FROM budget_transactions_current b
		LEFT JOIN accounts_current sa ON b.source_account_id = sa.id
		LEFT JOIN accounts_current da ON b.destination_account_id = da.id
		WHERE sa.id IS NULL OR da.id IS NULL`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Error finding orphaned budget transactions: "+err.Error())
	}
	var bTxIDs []string
	for bRows.Next() {
		var id string
		if err := bRows.Scan(&id); err == nil {
			bTxIDs = append(bTxIDs, id)
		}
	}
	bRows.Close()

	// 2. Find all raw transactions in raw_transactions_current referencing missing accounts
	rRows, err := tx.QueryContext(c.Request().Context(), `
		SELECT r.id FROM raw_transactions_current r
		LEFT JOIN accounts_current a ON r.account_id = a.id
		WHERE a.id IS NULL`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Error finding orphaned raw transactions: "+err.Error())
	}
	var rTxIDs []string
	for rRows.Next() {
		var id string
		if err := rRows.Scan(&id); err == nil {
			rTxIDs = append(rTxIDs, id)
		}
	}
	rRows.Close()

	// Delete from budget_transactions_current and budget_transactions
	for _, id := range bTxIDs {
		_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM budget_transactions_current WHERE id = ?`, id)
		_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM budget_transactions WHERE id = ?`, id)
		_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM transaction_links WHERE budget_transaction_id = ?`, id)
	}

	// Delete from raw_transactions_current and raw_transactions
	for _, id := range rTxIDs {
		_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM raw_transactions_current WHERE id = ?`, id)
		_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM raw_transactions WHERE id = ?`, id)
		_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM transaction_links WHERE raw_transaction_id = ?`, id)
	}

	if err := tx.Commit(); err != nil {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Błąd zapisu usuwania osieroconych transakcji", "success": false}}`)
		return c.String(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	report, _ := h.RunAudit(c.Request().Context(), tenantDB)
	msg := fmt.Sprintf("Pomyślnie usunięto %d transakcji budżetowych i %d transakcji raw.", len(bTxIDs), len(rTxIDs))
	c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "`+templateEscape(msg)+`", "success": true}}`)

	return c.HTML(http.StatusOK, RenderAuditCard(budgetID, csrfToken, report))
}

// ReconcileDiscrepanciesAction handles POST /budgets/:budgetId/debug/actions/reconcile-discrepancies.
// Generates atomic starting/reconciliation adjustment transactions to sync transactions with account_currency_balances.
func (h *DebugHandler) ReconcileDiscrepanciesAction(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Forbidden")
	}

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	csrfToken, _ := c.Get("csrfToken").(string)

	report, err := h.RunAudit(c.Request().Context(), tenantDB)
	if err != nil {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Błąd pobierania audytu", "success": false}}`)
		return c.String(http.StatusInternalServerError, err.Error())
	}

	if len(report.AccountDiscrepancies) == 0 {
		c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "Brak rozbieżności sald do skorygowania.", "success": true}}`)
		return c.HTML(http.StatusOK, RenderAuditCard(budgetID, csrfToken, report))
	}

	now := time.Now().UTC().UnixMicro()

	// Ensure virtual 'Set Balance' / 'Reconciliation Adjustment' counterparty exists
	var setBalPartyID, setBalAcctID string
	err = tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT p.id, a.id
		FROM parties_current p
		JOIN accounts_current a ON a.party_id = p.id
		WHERE p.name = 'Set Balance' AND p.is_user_owned = 0
		LIMIT 1`).Scan(&setBalPartyID, &setBalAcctID)
	if err != nil {
		setBalPartyID = domain.NewUUIDv7()
		setBalAcctID = domain.NewUUIDv7()
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'Set Balance', 'ORGANIZATION', 0, ?, ?)`, setBalPartyID, userID, now)
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, 'Set Balance Offset', 'EXTERNAL', 'PLN', 0, ?, ?)`, setBalAcctID, setBalPartyID, userID, now)
	}

	createdCount := 0
	for _, disc := range report.AccountDiscrepancies {
		diff := disc.DiffMinor // Stored - Computed
		if diff == 0 {
			continue
		}

		notes := fmt.Sprintf("Reconciliation Adjustment (Diff: %.2f %s)", float64(diff)/100.0, disc.Currency)
		var srcID, dstID string
		var srcAmt, dstAmt int64

		if diff > 0 {
			// Stored is higher than computed: inject inflow from Set Balance virtual account
			srcID = setBalAcctID
			dstID = disc.AccountID
			srcAmt = diff
			dstAmt = diff
		} else {
			// Stored is lower than computed: inject outflow to Set Balance virtual account
			srcID = disc.AccountID
			dstID = setBalAcctID
			srcAmt = -diff
			dstAmt = -diff
		}

		// Directly insert into budget_transactions and budget_transactions_current without modifying account_currency_balances
		// because account_currency_balances is already at StoredMinor!
		txID := domain.NewUUIDv7()
		_, err := tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO budget_transactions (id, version, source_account_id, destination_account_id,
				source_amount_minor, source_currency, destination_amount_minor, destination_currency,
				bucket_id, target_id, transaction_date, notes, action, agent_id, correlation_id, created_at)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, 'CREATE', ?, ?, ?)`,
			txID, srcID, dstID, srcAmt, disc.Currency, dstAmt, disc.Currency, now, notes, userID, corrID, now)
		if err == nil {
			tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO budget_transactions_current (id, version, source_account_id, destination_account_id,
					source_amount_minor, source_currency, destination_amount_minor, destination_currency,
					bucket_id, target_id, transaction_date, notes, updated_at)
				VALUES (?, 1, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?)`,
				txID, srcID, dstID, srcAmt, disc.Currency, dstAmt, disc.Currency, now, notes, now)
			createdCount++
		}
	}

	refreshedReport, _ := h.RunAudit(c.Request().Context(), tenantDB)
	msg := fmt.Sprintf("Wygenerowano %d transakcji korygujących (Reconciliation Adjustment).", createdCount)
	c.Response().Header().Set("HX-Trigger", `{"debugNotification": {"message": "`+msg+`", "success": true}}`)

	return c.HTML(http.StatusOK, RenderAuditCard(budgetID, csrfToken, refreshedReport))
}

func templateEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// AuditAIAgentAction handles POST /budgets/:budgetId/debug/integrations/ai/audit.
// Sends a simple "2 + 2 =" prompt to the Gemini API and returns structured status, HTTP code, raw body and correlationId.
func (h *DebugHandler) AuditAIAgentAction(c echo.Context) error {
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Restricted to budget OWNER")
	}

	corrID := logger.CorrelationIDFromContext(c.Request().Context())
	apiKey := os.Getenv("GEMINI_API_KEY")
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = os.Getenv("AI_GEMINI_MODEL")
	}
	if model == "" {
		model = "gemini-3.8-flash"
	}

	if apiKey == "" {
		return c.JSON(http.StatusOK, IntegrationAuditResult{
			Success:       false,
			StatusCode:    http.StatusUnauthorized,
			RawResponse:   `{"error": {"code": 401, "message": "GEMINI_API_KEY environment variable is not set"}}`,
			Message:       "Brak skonfigurowanego klucza GEMINI_API_KEY w środowisku",
			CorrelationID: corrID,
		})
	}

	baseURL := os.Getenv("GEMINI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", strings.TrimRight(baseURL, "/"), model, apiKey)

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]any{
					{"text": "2 + 2 ="},
				},
			},
		},
		"generationConfig": map[string]any{
			"temperature":     0.1,
			"maxOutputTokens": 32,
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return c.JSON(http.StatusOK, IntegrationAuditResult{
			Success:       false,
			StatusCode:    http.StatusInternalServerError,
			RawResponse:   err.Error(),
			Message:       "Failed encoding Gemini audit payload: " + err.Error(),
			CorrelationID: corrID,
		})
	}

	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return c.JSON(http.StatusOK, IntegrationAuditResult{
			Success:       false,
			StatusCode:    http.StatusInternalServerError,
			RawResponse:   err.Error(),
			Message:       "Failed creating Gemini HTTP request: " + err.Error(),
			CorrelationID: corrID,
		})
	}
	req.Header.Set("Content-Type", "application/json")

	client := h.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	start := time.Now()
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.Error(c.Request().Context(), "Gemini audit HTTP request failed", "error", err.Error(), "correlation_id", corrID, "duration_ms", duration.Milliseconds())
		return c.JSON(http.StatusOK, IntegrationAuditResult{
			Success:       false,
			StatusCode:    http.StatusBadGateway,
			RawResponse:   err.Error(),
			Message:       "Błąd połączenia z API Gemini: " + err.Error(),
			CorrelationID: corrID,
		})
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	rawBody := string(respBytes)

	if resp.StatusCode != http.StatusOK {
		apiErr := ai.ParseGeminiError(resp.StatusCode, rawBody, corrID)
		logger.Error(c.Request().Context(), "Gemini audit API returned error",
			"status", resp.StatusCode,
			"error_code", apiErr.ErrorCode,
			"raw", rawBody,
			"correlation_id", corrID,
		)
		return c.JSON(http.StatusOK, IntegrationAuditResult{
			Success:       false,
			StatusCode:    resp.StatusCode,
			RawResponse:   rawBody,
			Message:       apiErr.Message,
			CorrelationID: corrID,
		})
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	replyText := ""
	if err := json.Unmarshal(respBytes, &parsed); err == nil && len(parsed.Candidates) > 0 && len(parsed.Candidates[0].Content.Parts) > 0 {
		replyText = strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text)
	}

	return c.JSON(http.StatusOK, IntegrationAuditResult{
		Success:       true,
		StatusCode:    http.StatusOK,
		RawResponse:   rawBody,
		Message:       fmt.Sprintf("Połączenie z Google Gemini API (%s) działa poprawnie. Odpowiedź na '2 + 2 =': %q", model, replyText),
		CorrelationID: corrID,
	})
}

// AuditGoCardlessAction handles POST /budgets/:budgetId/debug/integrations/gocardless/audit.
// Verifies connectivity and authorization by querying GoCardless /institutions.
func (h *DebugHandler) AuditGoCardlessAction(c echo.Context) error {
	role, _ := c.Get(ContextRoleKey).(string)
	if role != "OWNER" {
		return echo.NewHTTPError(http.StatusForbidden, "Restricted to budget OWNER")
	}

	corrID := logger.CorrelationIDFromContext(c.Request().Context())
	secretID := os.Getenv("GOCARDLESS_SECRET_ID")
	secretKey := os.Getenv("GOCARDLESS_SECRET_KEY")

	if secretID == "" || secretKey == "" {
		return c.JSON(http.StatusOK, IntegrationAuditResult{
			Success:       false,
			StatusCode:    http.StatusUnauthorized,
			RawResponse:   `{"error": {"code": 401, "message": "GOCARDLESS_SECRET_ID or GOCARDLESS_SECRET_KEY is not configured"}}`,
			Message:       "Brak skonfigurowanych kluczy GOCARDLESS_SECRET_ID / GOCARDLESS_SECRET_KEY w środowisku",
			CorrelationID: corrID,
		})
	}

	gcClient := h.gocardlessClient
	if gcClient == nil {
		redirectURL := os.Getenv("GOCARDLESS_REDIRECT_URL")
		gcClient = gocardless.NewClient(secretID, secretKey, redirectURL)
	}

	institutions, err := gcClient.ListInstitutions(c.Request().Context(), "PL")
	if err != nil {
		logger.Error(c.Request().Context(), "GoCardless audit failed", "error", err.Error(), "correlation_id", corrID)
		statusCode := http.StatusBadGateway
		rawMsg := err.Error()
		if strings.Contains(rawMsg, "status 401") || strings.Contains(rawMsg, "401") {
			statusCode = http.StatusUnauthorized
		} else if strings.Contains(rawMsg, "status 400") {
			statusCode = http.StatusBadRequest
		}

		return c.JSON(http.StatusOK, IntegrationAuditResult{
			Success:       false,
			StatusCode:    statusCode,
			RawResponse:   rawMsg,
			Message:       "Błąd komunikacji z GoCardless API: " + rawMsg,
			CorrelationID: corrID,
		})
	}

	return c.JSON(http.StatusOK, IntegrationAuditResult{
		Success:       true,
		StatusCode:    http.StatusOK,
		RawResponse:   fmt.Sprintf(`{"institutions_count": %d}`, len(institutions)),
		Message:       fmt.Sprintf("Połączenie z GoCardless API poprawne. Pobrano %d instytucji bankowych dla kraju PL.", len(institutions)),
		CorrelationID: corrID,
	})
}
