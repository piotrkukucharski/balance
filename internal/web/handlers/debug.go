package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

// DebugHandler handles diagnostic audit and corrective actions for budget owners.
type DebugHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

// NewDebugHandler initializes a new DebugHandler.
func NewDebugHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *DebugHandler {
	return &DebugHandler{
		mainDB:    mainDB,
		tenantMgr: tenantMgr,
	}
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
	sidebarHTML := RenderSidebarNavigation(budgetID, "debug")
	bottomDockHTML := RenderBottomDock(budgetID, "settings")
	dFormat, tFormat := GetUserDateTimePreferences(c)

	// Run initial audit to render initial state
	report, err := h.RunAudit(c.Request().Context(), tenantDB)
	auditHTML := RenderAuditCard(budgetID, csrfToken, report)

	return c.HTML(http.StatusOK, fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark" data-date-format="%s" data-time-format="%s">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — %s Diagnostic Tools (Debug)</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col">
    <!-- Toast Notification Container -->
    <div id="debug-toast" class="toast toast-top toast-end z-50 transition-opacity duration-300 hidden">
        <div id="debug-toast-alert" class="alert alert-success text-xs shadow-lg flex items-center gap-2">
            <span id="debug-toast-icon">✓</span>
            <span id="debug-toast-msg">Operation completed</span>
        </div>
    </div>

    <!-- Navbar -->
    <header class="navbar bg-base-100 border-b border-base-300 px-6 sticky top-0 z-30 shadow-xs">
        <div class="navbar-start gap-4 items-center">
            <a href="/budgets" class="text-2xl font-black tracking-tight text-primary flex items-center gap-2">
                <span>⚖️</span>
                <span>Balance</span>
            </a>
            <div class="badge badge-neutral font-semibold gap-1.5 py-3">
                <span class="w-2 h-2 rounded-full bg-success"></span>
                <span>%s</span>
            </div>
            <span class="badge badge-warning font-semibold text-xs">Diagnostic Tools (Debug)</span>
        </div>
        <div class="navbar-end gap-3">
            <a href="/budgets/%s/settings" class="btn btn-ghost btn-sm">← Back to Settings</a>
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm font-medium">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <!-- Sidebar -->
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
            %s
        </aside>

        <!-- Main Content -->
        <main class="flex-1 p-4 sm:p-6 md:p-8 pb-24 md:pb-8 overflow-y-auto max-w-4xl">
            <div class="mb-6 flex flex-col sm:flex-row sm:items-center justify-between gap-4">
                <div>
                    <h1 class="text-2xl font-black tracking-tight flex items-center gap-2">
                        <span>🛠️</span>
                        <span>Diagnostic Tools (Debug)</span>
                    </h1>
                    <p class="text-xs text-base-content/60 mt-1">Audit ledger integrity in real-time, detect orphaned projections, and trigger transactional repair routines.</p>
                </div>
            </div>

            <div id="audit-card-container">
                %s
            </div>
        </main>
    </div>

    %s
    %s

    <script>
        function showDebugToast(message, isSuccess = true) {
            const toast = document.getElementById('debug-toast');
            if (!toast) return;
            const alertBox = document.getElementById('debug-toast-alert');
            const msgEl = document.getElementById('debug-toast-msg');
            const iconEl = document.getElementById('debug-toast-icon');

            if (msgEl) msgEl.innerText = message;
            if (iconEl) iconEl.innerText = isSuccess ? '✓' : '⚠️';
            if (alertBox) {
                alertBox.className = 'alert text-xs shadow-lg flex items-center gap-2 ' + (isSuccess ? 'alert-success' : 'alert-error');
            }
            toast.classList.remove('hidden');
            toast.style.opacity = '1';
            clearTimeout(window.__debugToastTimeout);
            window.__debugToastTimeout = setTimeout(() => {
                toast.style.opacity = '0';
                setTimeout(() => toast.classList.add('hidden'), 300);
            }, 4000);
        }

        document.addEventListener('debugNotification', function(evt) {
            if (evt.detail && evt.detail.message) {
                showDebugToast(evt.detail.message, evt.detail.success !== false);
            }
        });
    </script>
</body>
</html>`,
		html.EscapeString(dFormat), html.EscapeString(tFormat),
		html.EscapeString(name),
		html.EscapeString(name),
		budgetID,
		csrfToken,
		sidebarHTML,
		auditHTML,
		bottomDockHTML,
		DateTimeFormatterScript(),
	))
}

// RenderAuditCard produces the HTML snippet for the integrity status card.
func RenderAuditCard(budgetID, csrfToken string, report IntegrityAuditReport) string {
	var statusBadge string
	if !report.HasAnomalies {
		statusBadge = `<span class="badge badge-success font-bold gap-1 text-xs py-2 px-3">✓ All Ledger Checks Passed</span>`
	} else {
		statusBadge = `<span class="badge badge-error font-bold gap-1 text-xs py-2 px-3">⚠️ Anomalies Detected</span>`
	}

	var orphanedHTML strings.Builder
	if len(report.OrphanedBalances) == 0 && len(report.OrphanedTransactions) == 0 {
		orphanedHTML.WriteString(`<div class="text-xs text-success flex items-center gap-1.5 font-medium"><span>✓</span><span>Brak osieroconych sald ani transakcji powiązanych z nieistniejącymi kontami.</span></div>`)
	} else {
		if len(report.OrphanedBalances) > 0 {
			orphanedHTML.WriteString(fmt.Sprintf(`
				<div class="alert alert-error alert-soft text-xs mb-3">
					<span>Wykryto <strong>%d</strong> osieroconych rekordów sald w <code>account_currency_balances</code>!</span>
				</div>
				<div class="overflow-x-auto mb-4">
					<table class="table table-xs w-full">
						<thead>
							<tr><th>Account ID</th><th>Waluta</th><th class="text-right">Saldo</th></tr>
						</thead>
						<tbody>`, len(report.OrphanedBalances)))
			for _, o := range report.OrphanedBalances {
				orphanedHTML.WriteString(fmt.Sprintf(`
					<tr>
						<td class="font-mono text-xs">%s</td>
						<td><span class="badge badge-neutral text-xs">%s</span></td>
						<td class="font-mono text-right font-bold">%.2f</td>
					</tr>`, html.EscapeString(o.AccountID), html.EscapeString(o.Currency), float64(o.BalanceMinor)/100.0))
			}
			orphanedHTML.WriteString(`</tbody></table></div>`)
		}

		if len(report.OrphanedAccounts) > 0 {
			orphanedHTML.WriteString(fmt.Sprintf(`
				<div class="alert alert-warning alert-soft text-xs mb-3">
					<span>Wykryto <strong>%d</strong> nieistniejących kont powiązanych z transakcjami lub saldami:</span>
				</div>
				<div class="overflow-x-auto mb-4">
					<table class="table table-xs w-full">
						<thead>
							<tr>
								<th>Nazwa Konta (Historia)</th>
								<th>ID Konta</th>
								<th class="text-center">Transakcje Budżetowe</th>
								<th class="text-center">Transakcje Raw</th>
								<th>Status w Historii</th>
								<th class="text-right">Działania Naprawcze</th>
							</tr>
						</thead>
						<tbody>`, len(report.OrphanedAccounts)))
			for _, oa := range report.OrphanedAccounts {
				resurrectBtn := ""
				if oa.CanResurrect {
					resurrectBtn = fmt.Sprintf(`
						<form hx-post="/budgets/%s/debug/actions/resurrect-account" hx-target="#audit-card-container" hx-swap="innerHTML" class="inline"
						      hx-disabled-elt="find button"
						      onsubmit="return confirm('Przywrócić konto &quot;%s&quot; w stanie zamkniętym (is_closed=1)?') ? (this.querySelector('button').disabled = true, true) : false;">
							<input type="hidden" name="csrf_token" value="%s" />
							<input type="hidden" name="account_id" value="%s" />
							<button type="submit" class="btn btn-xs btn-primary gap-1">
								<span>♻️</span>
								<span>Wskrześ Konto</span>
							</button>
						</form>`, budgetID, html.EscapeString(oa.AccountName), csrfToken, html.EscapeString(oa.AccountID))
				} else {
					resurrectBtn = `<span class="badge badge-ghost text-xs">Brak w historii</span>`
				}

				orphanedHTML.WriteString(fmt.Sprintf(`
					<tr>
						<td class="font-bold text-xs">%s</td>
						<td class="font-mono text-xs">%s</td>
						<td class="text-center font-bold">%d</td>
						<td class="text-center font-bold">%d</td>
						<td><span class="badge badge-neutral text-xs font-mono">%s</span></td>
						<td class="text-right">%s</td>
					</tr>`,
					html.EscapeString(oa.AccountName),
					html.EscapeString(oa.AccountID),
					oa.BudgetTxCount,
					oa.RawTxCount,
					html.EscapeString(oa.LastRecordedStatus),
					resurrectBtn,
				))
			}
			orphanedHTML.WriteString(`</tbody></table></div>`)
		}

		if len(report.OrphanedTransactions) > 0 {
			orphanedHTML.WriteString(fmt.Sprintf(`
				<div class="text-xs font-semibold text-base-content/70 mb-1 flex items-center justify-between">
					<span>Szczegóły osieroconych transakcji (%d):</span>
				</div>
				<div class="overflow-x-auto max-h-48 border border-base-300 rounded-lg mb-2">
					<table class="table table-xs w-full">
						<thead class="sticky top-0 bg-base-200">
							<tr>
								<th>Typ</th>
								<th>Rola brakującego konta</th>
								<th>Brakujące Account ID</th>
								<th>Opis / Payee</th>
								<th class="text-right">Kwota</th>
							</tr>
						</thead>
						<tbody>`, len(report.OrphanedTransactions)))
			for _, ot := range report.OrphanedTransactions {
				orphanedHTML.WriteString(fmt.Sprintf(`
					<tr>
						<td><span class="badge badge-outline text-xs">%s</span></td>
						<td class="font-mono text-xs">%s</td>
						<td class="font-mono text-xs">%s</td>
						<td class="text-xs truncate max-w-xs" title="%s">%s</td>
						<td class="font-mono text-right font-bold">%.2f %s</td>
					</tr>`,
					html.EscapeString(ot.Type),
					html.EscapeString(ot.Role),
					html.EscapeString(ot.AccountID),
					html.EscapeString(ot.NotesOrPayee),
					html.EscapeString(ot.NotesOrPayee),
					float64(ot.AmountMinor)/100.0,
					html.EscapeString(ot.Currency),
				))
			}
			orphanedHTML.WriteString(`</tbody></table></div>`)
		}
	}

	var discrepancyHTML strings.Builder
	if len(report.AccountDiscrepancies) == 0 {
		discrepancyHTML.WriteString(`<div class="text-xs text-success flex items-center gap-1.5 font-medium"><span>✓</span><span>All account balances exactly match the net sum of budget transactions.</span></div>`)
	} else {
		discrepancyHTML.WriteString(fmt.Sprintf(`
			<div class="alert alert-warning alert-soft text-xs mb-3">
				<span>Found <strong>%d</strong> accounts with discrepancy between stored balance and transaction history!</span>
			</div>
			<div class="overflow-x-auto">
				<table class="table table-xs w-full">
					<thead>
						<tr><th>Account</th><th>Currency</th><th class="text-right">Stored</th><th class="text-right">Computed</th><th class="text-right">Diff</th></tr>
					</thead>
					<tbody>`, len(report.AccountDiscrepancies)))
		for _, d := range report.AccountDiscrepancies {
			discrepancyHTML.WriteString(fmt.Sprintf(`
				<tr>
					<td class="font-bold text-xs">%s</td>
					<td><span class="badge badge-neutral text-xs">%s</span></td>
					<td class="font-mono text-right">%.2f</td>
					<td class="font-mono text-right">%.2f</td>
					<td class="font-mono text-right font-bold text-error">%.2f</td>
				</tr>`, html.EscapeString(d.AccountName), html.EscapeString(d.Currency), float64(d.StoredMinor)/100.0, float64(d.ComputedMinor)/100.0, float64(d.DiffMinor)/100.0))
		}
		discrepancyHTML.WriteString(`</tbody></table></div>`)
	}

	var equationBadge string
	if report.FundamentalEquationOK {
		equationBadge = `<span class="badge badge-success text-xs font-bold">Balanced</span>`
	} else {
		equationBadge = `<span class="badge badge-error text-xs font-bold">Unbalanced</span>`
	}

	return fmt.Sprintf(`
		<div class="card card-border bg-base-100 border-base-300 shadow-md">
			<div class="card-body p-6 space-y-6">
				<div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-3 border-b border-base-200 pb-4">
					<div>
						<h2 class="card-title text-base font-bold flex items-center gap-2">
							<span>📊</span>
							<span>Ledger Spójność & Integralność Bilansowa</span>
						</h2>
						<p class="text-xs text-base-content/60 mt-0.5">Real-time consistency checks evaluated in an isolated transaction.</p>
					</div>
					<div>%s</div>
				</div>

				<!-- Check 1: Orphaned Balances and Transactions -->
				<div class="space-y-2">
					<h3 class="text-xs font-bold uppercase tracking-wider text-base-content/70 flex items-center gap-1.5">
						<span>1. Osierocone Salda & Transakcje (Konta usunięte z accounts_current)</span>
					</h3>
					%s
				</div>

				<!-- Check 2: Transaction Sum Discrepancy -->
				<div class="space-y-2">
					<h3 class="text-xs font-bold uppercase tracking-wider text-base-content/70 flex items-center gap-1.5">
						<span>2. Zgodność Transakcji z Saldami Kont</span>
					</h3>
					%s
				</div>

				<!-- Check 3: Fundamental Equation -->
				<div class="space-y-2">
					<div class="flex items-center justify-between">
						<h3 class="text-xs font-bold uppercase tracking-wider text-base-content/70 flex items-center gap-1.5">
							<span>3. Równanie Fundamentalne (Gotówka == Koperty + RTA)</span>
						</h3>
						%s
					</div>
					<div class="text-xs text-base-content/80 font-mono bg-base-200/50 p-3 rounded-lg border border-base-300">
						%s
					</div>
				</div>

				<!-- Action Controls -->
				<div class="border-t border-base-200 pt-4 flex flex-wrap items-center justify-between gap-3">
					<form hx-post="/budgets/%s/debug/audit" hx-target="#audit-card-container" hx-swap="innerHTML" class="inline"
					      hx-disabled-elt="find button"
					      onsubmit="this.querySelector('button').disabled = true;">
						<input type="hidden" name="csrf_token" value="%s" />
						<button type="submit" class="btn btn-sm btn-outline gap-1.5">
							<span>🔄</span>
							<span>Uruchom Audyt Ponownie</span>
						</button>
					</form>

					<div class="flex flex-wrap items-center gap-2">
						<form hx-post="/budgets/%s/debug/actions/cleanup-orphaned" hx-target="#audit-card-container" hx-swap="innerHTML" class="inline"
						      hx-disabled-elt="find button"
						      onsubmit="return confirm('Czy na pewno chcesz usunąć osierocone rekordy sald?') ? (this.querySelector('button').disabled = true, true) : false;">
							<input type="hidden" name="csrf_token" value="%s" />
							<button type="submit" class="btn btn-sm btn-warning gap-1.5" %s>
								<span>🧹</span>
								<span>Usuń Osierocone Salda</span>
							</button>
						</form>

						<form hx-post="/budgets/%s/debug/actions/delete-orphaned-transactions" hx-target="#audit-card-container" hx-swap="innerHTML" class="inline"
						      hx-disabled-elt="find button"
						      onsubmit="return confirm('Czy na pewno chcesz trwale usunąć wszystkie osierocone transakcje budżetowe i raw?') ? (this.querySelector('button').disabled = true, true) : false;">
							<input type="hidden" name="csrf_token" value="%s" />
							<button type="submit" class="btn btn-sm btn-error gap-1.5" %s>
								<span>🗑️</span>
								<span>Usuń Osierocone Transakcje</span>
							</button>
						</form>

						<form hx-post="/budgets/%s/debug/actions/reconcile-discrepancies" hx-target="#audit-card-container" hx-swap="innerHTML" class="inline"
						      hx-disabled-elt="find button"
						      onsubmit="return confirm('Utworzyć automatyczne transakcje korygujące dla wykrytych rozbieżności sald?') ? (this.querySelector('button').disabled = true, true) : false;">
							<input type="hidden" name="csrf_token" value="%s" />
							<button type="submit" class="btn btn-sm btn-primary gap-1.5" %s>
								<span>⚖️</span>
								<span>Generuj Korektę Bilansową</span>
							</button>
						</form>
					</div>
				</div>
			</div>
		</div>`,
		statusBadge,
		orphanedHTML.String(),
		discrepancyHTML.String(),
		equationBadge,
		report.FundamentalDetails,
		budgetID, csrfToken,
		budgetID, csrfToken, disabledAttr(len(report.OrphanedBalances) == 0),
		budgetID, csrfToken, disabledAttr(len(report.OrphanedTransactions) == 0),
		budgetID, csrfToken, disabledAttr(len(report.AccountDiscrepancies) == 0),
	)
}

func disabledAttr(disabled bool) string {
	if disabled {
		return "disabled"
	}
	return ""
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
