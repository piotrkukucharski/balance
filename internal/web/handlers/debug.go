package handlers

import (
	"context"
	"database/sql"
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
	OrphanedBalances      []OrphanedBalanceItem    `json:"orphaned_balances"`
	AccountDiscrepancies  []AccountDiscrepancyItem `json:"account_discrepancies"`
	FundamentalEquationOK bool                     `json:"fundamental_equation_ok"`
	FundamentalDetails    string                   `json:"fundamental_details"`
	HasAnomalies          bool                     `json:"has_anomalies"`
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
	if len(report.OrphanedBalances) == 0 {
		orphanedHTML.WriteString(`<div class="text-xs text-success flex items-center gap-1.5 font-medium"><span>✓</span><span>No orphaned balances in account_currency_balances.</span></div>`)
	} else {
		orphanedHTML.WriteString(fmt.Sprintf(`
			<div class="alert alert-error alert-soft text-xs mb-3">
				<span>Found <strong>%d</strong> orphaned balance records without corresponding account!</span>
			</div>
			<div class="overflow-x-auto">
				<table class="table table-xs w-full">
					<thead>
						<tr><th>Account ID</th><th>Currency</th><th class="text-right">Balance</th></tr>
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

				<!-- Check 1: Orphaned Balances -->
				<div class="space-y-2">
					<h3 class="text-xs font-bold uppercase tracking-wider text-base-content/70 flex items-center gap-1.5">
						<span>1. Osierocone Salda (account_currency_balances)</span>
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
