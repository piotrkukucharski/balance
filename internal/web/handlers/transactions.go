package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/mock"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/rule"
	"github.com/balance/balance/internal/transaction"
	"github.com/labstack/echo/v4"
)

type TransactionsHandler struct {
	mainDB        *sql.DB
	tenantMgr     *db.TenantDBManager
	mockAdapter   *mock.MockAdapter
	syncMu        sync.Mutex
	activeSyncs   map[string]bool
	syncListeners map[string][]chan string
}

func NewTransactionsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *TransactionsHandler {
	return &TransactionsHandler{
		mainDB:        mainDB,
		tenantMgr:     tenantMgr,
		mockAdapter:   mock.NewMockAdapter(),
		activeSyncs:   make(map[string]bool),
		syncListeners: make(map[string][]chan string),
	}
}

// TransactionsView renders the unified transactions ledger and primary toolbar.
func (h *TransactionsHandler) TransactionsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var budgetName, defaultCurr string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr)

	// Fetch buckets for dropdown
	bucketRows, err := tenantDB.QueryContext(c.Request().Context(), `SELECT id, name FROM buckets_projection WHERE is_archived = 0 ORDER BY name ASC`)
	var bucketOptions strings.Builder
	var editBucketOptions strings.Builder
	bucketOptions.WriteString(`<option value="">Unassigned</option>`)
	editBucketOptions.WriteString(`<option value="">Unassigned</option>`)
	if err == nil {
		defer bucketRows.Close()
		for bucketRows.Next() {
			var bID, bName string
			bucketRows.Scan(&bID, &bName)
			opt := fmt.Sprintf(`<option value="%s">%s</option>`, bID, bName)
			bucketOptions.WriteString(opt)
			editBucketOptions.WriteString(opt)
		}
	}

	// Fetch user checking/cash accounts for dropdown and checkboxes
	acctRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.id, a.name, a.primary_currency FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.is_user_owned = 1 AND a.is_closed = 0
		ORDER BY a.name ASC`)
	var acctOptions strings.Builder
	var editAcctOptions strings.Builder
	var acctCheckboxes strings.Builder
	if err == nil {
		defer acctRows.Close()
		for acctRows.Next() {
			var aID, aName, aCurr string
			acctRows.Scan(&aID, &aName, &aCurr)
			opt := fmt.Sprintf(`<option value="%s">%s (%s)</option>`, aID, aName, aCurr)
			acctOptions.WriteString(opt)
			editAcctOptions.WriteString(opt)

			checkbox := fmt.Sprintf(`
				<label class="label cursor-pointer justify-start gap-3 py-1.5 px-2 hover:bg-base-200 rounded-lg transition-colors">
					<input type="checkbox" name="account_ids" value="%s" checked class="checkbox checkbox-primary checkbox-sm" />
					<span class="label-text font-medium text-sm">%s <span class="text-xs text-base-content/60">(%s)</span></span>
				</label>`, aID, aName, aCurr)
			acctCheckboxes.WriteString(checkbox)
		}
	}

	// Fetch recent budget transactions
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT bt.id, bt.source_amount_minor, bt.source_currency, bt.transaction_date,
		       COALESCE(bt.notes, ''), COALESCE(bp.name, 'Unassigned'), COALESCE(bt.bucket_id, ''),
		       bt.source_account_id
		FROM budget_transactions_current bt
		LEFT JOIN buckets_projection bp ON bt.bucket_id = bp.id
		ORDER BY bt.transaction_date DESC
		LIMIT 100`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading transactions")
	}
	defer rows.Close()

	var txRows strings.Builder
	hasTxs := false
	csrfToken, _ := c.Get("csrfToken").(string)

	for rows.Next() {
		hasTxs = true
		var id, curr, notes, bucketName, bucketIDVal, srcAcctID string
		var amountMinor, txDate int64
		rows.Scan(&id, &amountMinor, &curr, &txDate, &notes, &bucketName, &bucketIDVal, &srcAcctID)

		dateStr := time.UnixMicro(txDate).Format("2006-01-02")
		amountMajor := float64(amountMinor) / 100.0

		escapedNotes := strings.ReplaceAll(notes, "'", "\\'")
		escapedNotes = strings.ReplaceAll(escapedNotes, "\"", "&quot;")

		actionMenu := RenderPopoverActionMenu("tx", id, []TableActionItem{
			{
				Label:   "✏️ Edit",
				OnClick: fmt.Sprintf("openEditTxModal('%s', '%.2f', '%s', '%s', '%s', '%s')", id, amountMajor, dateStr, escapedNotes, bucketIDVal, srcAcctID),
			},
			{
				Label:      "🗑️ Delete",
				FormAction: fmt.Sprintf("/budgets/%s/transactions/%s/delete", budgetID, id),
				CSRFToken:  csrfToken,
				IsDanger:   true,
				ConfirmMsg: "Are you sure you want to delete this transaction?",
			},
		})

		txRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td class="font-mono text-xs">%s</td>
				<td><span class="badge badge-soft badge-primary font-semibold text-xs">%s</span></td>
				<td class="text-sm font-medium">%s</td>
				<td class="font-mono text-right font-bold text-sm">
					-%.2f <span class="text-xs font-normal text-base-content/60">%s</span>
				</td>
				<td class="text-right">
					%s
				</td>
			</tr>`,
			dateStr, bucketName, notes, amountMajor, curr,
			actionMenu,
		))
	}

	if !hasTxs {
		txRows.WriteString(`
			<tr>
				<td colspan="5" class="text-center py-8 text-base-content/50">
					No transactions recorded yet. Use 🔄 Sync with Bank or ➕ Add Transaction.
				</td>
			</tr>
		`)
	}

	// Fetch pending raw transactions (unresolved bank feeds)
	rawRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT r.id, r.account_id, r.booking_date, r.amount_minor, r.currency,
		       COALESCE(r.raw_payee, ''), COALESCE(r.counterparty_iban, ''), COALESCE(r.reference, ''),
		       a.name
		FROM raw_transactions_current r
		JOIN accounts_current a ON r.account_id = a.id
		WHERE r.status = 'PENDING'
		ORDER BY r.booking_date DESC
		LIMIT 100`)
	var rawTxRows strings.Builder
	pendingRawCount := 0
	if err == nil {
		defer rawRows.Close()

		// Load rule matching engine to calculate suggested match
		rulesEngine, _ := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)

		for rawRows.Next() {
			pendingRawCount++
			var rID, rAcctID, rCurrency, rPayee, rIBAN, rRef, rAcctName string
			var rDate, rAmountMinor int64
			rawRows.Scan(&rID, &rAcctID, &rDate, &rAmountMinor, &rCurrency, &rPayee, &rIBAN, &rRef, &rAcctName)

			rDateStr := time.UnixMicro(rDate).Format("2006-01-02")
			rAmountMajor := float64(rAmountMinor) / 100.0

			amountBadgeClass := "text-error"
			amountPrefix := ""
			if rAmountMinor > 0 {
				amountBadgeClass = "text-success"
				amountPrefix = "+"
			}

			// Suggest rule match if available
			suggestedBucketID := ""
			suggestedBadge := `<span class="badge badge-soft badge-ghost text-xs">Unmatched</span>`
			if rulesEngine != nil {
				if match := rulesEngine.Match(rPayee, rIBAN, rRef); match != nil && match.DefaultBucketID != nil {
					suggestedBucketID = *match.DefaultBucketID
					suggestedBadge = `<span class="badge badge-soft badge-success text-xs font-semibold">Matched Rule</span>`
				}
			}

			escapedPayee := strings.ReplaceAll(rPayee, "'", "\\'")
			escapedPayee = strings.ReplaceAll(escapedPayee, "\"", "&quot;")

			rawActionMenu := RenderPopoverActionMenu("raw", rID, []TableActionItem{
				{
					Label:   "➡️ Resolve to Envelope",
					OnClick: fmt.Sprintf("openResolveRawModal('%s', '%s', '%.2f', '%s', '%s', '%s')", rID, rAcctID, rAmountMajor, rCurrency, escapedPayee, suggestedBucketID),
				},
				{
					Label:      "🚫 Ignore Feed",
					FormAction: fmt.Sprintf("/budgets/%s/transactions/raw/%s/ignore", budgetID, rID),
					CSRFToken:  csrfToken,
					IsDanger:   true,
					ConfirmMsg: "Ignore this bank transaction feed?",
				},
			})

			rawTxRows.WriteString(fmt.Sprintf(`
				<tr class="hover">
					<td>
						<input type="checkbox" name="selected_raw_ids" value="%s" class="checkbox checkbox-primary checkbox-xs raw-select-cb" onchange="updateReconcileBatchCount()" />
					</td>
					<td class="font-mono text-xs">%s</td>
					<td class="text-xs font-medium text-base-content/80">%s</td>
					<td>
						<div class="font-bold text-sm">%s</div>
						<div class="text-xs text-base-content/50">%s</div>
					</td>
					<td>%s</td>
					<td class="font-mono text-right font-bold text-sm %s">
						%s%.2f <span class="text-xs font-normal text-base-content/60">%s</span>
					</td>
					<td class="text-right">
						%s
					</td>
				</tr>`,
				rID, rDateStr, rAcctName, rPayee, rRef, suggestedBadge, amountBadgeClass,
				amountPrefix, rAmountMajor, rCurrency,
				rawActionMenu,
			))
		}
	}

	if pendingRawCount == 0 {
		rawTxRows.WriteString(`
			<tr>
				<td colspan="7" class="text-center py-8 text-base-content/50">
					🎉 All bank transactions are reconciled. No pending bank feeds!
				</td>
			</tr>
		`)
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Transactions</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4/dist/ext/sse.js"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col">
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
        </div>
        <div class="navbar-end gap-3">
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm font-medium">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
            %s
        </aside>

        <main class="flex-1 p-6 md:p-8 overflow-y-auto">
            <!-- Primary Toolbar -->
            <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
                <div>
                    <h1 class="text-2xl font-black tracking-tight">Transactions Ledger</h1>
                    <p class="text-xs text-base-content/60 mt-1">Review activity, sync bank feeds, or record cash transactions.</p>
                </div>
                <div class="flex items-center gap-3">
                    <button type="button" class="btn btn-secondary btn-sm shadow font-semibold gap-1.5" onclick="bank_sync_modal.showModal()">
                        <span>🔄</span>
                        <span>Sync with Bank</span>
                    </button>
                    <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="add_tx_modal.showModal()">
                        <span>➕</span>
                        <span>Add Transaction</span>
                    </button>
                </div>
            </div>

            <!-- Sync Status & Notification Alerts -->
            <div id="sync-alerts" class="mb-4"></div>

            <!-- Tabs: Envelopes Ledger vs Pending Bank Feeds -->
            <div class="tabs tabs-box bg-base-100 p-1 rounded-xl shadow-xs border border-base-300 mb-6 flex">
                <input type="radio" name="tx_tab" id="tab_budget_txs" class="tab flex-1 font-bold text-sm" aria-label="📒 Envelope Ledger" checked />
                <div class="tab-content pt-4">
                    <div class="card card-border bg-base-100 border-base-300 shadow-md">
                        <div class="card-body p-0">
                            <div class="overflow-x-auto">
                                <table class="table table-zebra w-full">
                                    <thead>
                                        <tr class="bg-base-200/50">
                                            <th class="font-bold">Booking Date</th>
                                            <th class="font-bold">Envelope (Bucket)</th>
                                            <th class="font-bold">Notes / Payee</th>
                                            <th class="font-bold text-right">Amount</th>
                                            <th class="font-bold text-right">Actions</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        %s
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    </div>
                </div>

                <input type="radio" name="tx_tab" id="tab_raw_txs" class="tab flex-1 font-bold text-sm" aria-label="🏦 Pending Bank Feeds (%d)" />
                <div class="tab-content pt-4">
                    <!-- Workbench Toolbar -->
                    <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-3 bg-base-100 p-4 rounded-xl border border-base-300 shadow-sm mb-4">
                        <div>
                            <h3 class="font-bold text-base flex items-center gap-2">
                                <span>⚖️</span>
                                <span>Reconcile Workbench</span>
                            </h3>
                            <p class="text-xs text-base-content/60">Resolve raw bank transactions into envelope double-entry ledger items.</p>
                        </div>
                        <div class="flex items-center gap-2">
                            <form method="POST" action="/budgets/%s/transactions/reconcile/auto-match">
                                <input type="hidden" name="csrf_token" value="%s" />
                                <button type="submit" class="btn btn-secondary btn-sm shadow-xs font-semibold gap-1.5" title="Auto-match pending feeds with matching rules">
                                    <span>⚡</span>
                                    <span>Auto-Match</span>
                                </button>
                            </form>
                        </div>
                    </div>

                    <!-- Raw Transactions Feed Card -->
                    <div class="card card-border bg-base-100 border-base-300 shadow-md">
                        <div class="card-body p-0">
                            <div class="overflow-x-auto">
                                <table class="table table-zebra w-full">
                                    <thead>
                                        <tr class="bg-base-200/50">
                                            <th class="w-10">
                                                <input type="checkbox" class="checkbox checkbox-primary checkbox-xs" onclick="toggleAllRawCbs(this)" />
                                            </th>
                                            <th class="font-bold">Date</th>
                                            <th class="font-bold">Bank Account</th>
                                            <th class="font-bold">Raw Payee / Description</th>
                                            <th class="font-bold">Match Status</th>
                                            <th class="font-bold text-right">Amount</th>
                                            <th class="font-bold text-right">Resolve</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        %s
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        </main>
    </div>

    %s

    <!-- Bank Sync Options Modal -->
    <dialog id="bank_sync_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>🔄</span>
                    <span>Sync Bank Transactions</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/transactions/sync" 
                  hx-post="/budgets/%s/transactions/sync"
                  hx-target="#sync-alerts"
                  hx-swap="innerHTML"
                  onsubmit="bank_sync_modal.close()"
                  class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <fieldset class="fieldset">
                    <legend class="fieldset-legend font-semibold">Select Accounts to Synchronize</legend>
                    <div class="flex flex-col gap-1 max-h-48 overflow-y-auto border border-base-300 rounded-lg p-2 bg-base-100">
                        %s
                    </div>
                    <div class="flex justify-between items-center mt-1">
                        <span class="text-xs text-base-content/60">Check all accounts you wish to pull transactions for.</span>
                        <button type="button" class="btn btn-link btn-xs p-0 no-underline text-primary" onclick="toggleAllAccounts(this)">Select / Deselect All</button>
                    </div>
                </fieldset>
                <fieldset class="fieldset">
                    <legend class="fieldset-legend font-semibold">Initial Sync History Window</legend>
                    <select name="days_window" class="select select-bordered w-full select-md focus:select-primary">
                        <option value="30" selected>Last 30 Days (1 Month) - Recommended</option>
                        <option value="14">Last 14 Days (2 Weeks)</option>
                        <option value="7">Last 7 Days (1 Week)</option>
                        <option value="1">Last 24 Hours (1 Day)</option>
                    </select>
                    <p class="text-xs text-base-content/60 mt-1">For first-time syncs, transactions are fetched back to this date and starting balance is aligned automatically.</p>
                </fieldset>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="bank_sync_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md gap-1.5">
                        <span>🔄</span>
                        <span>Start Sync</span>
                    </button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Add Manual Transaction Modal -->
    <dialog id="add_tx_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Add Transaction</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/transactions" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="grid grid-cols-2 gap-4">
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Amount</legend>
                        <input type="number" step="0.01" name="amount" required placeholder="50.00" class="input input-bordered w-full input-md focus:input-primary" />
                    </fieldset>
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Date</legend>
                        <input type="date" name="transaction_date" value="%s" required class="input input-bordered w-full input-md focus:input-primary" />
                    </fieldset>
                </div>
                <div class="grid grid-cols-2 gap-4">
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Account</legend>
                        <select name="account_id" class="select select-bordered w-full select-md focus:select-primary">
                            %s
                        </select>
                    </fieldset>
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Envelope (Bucket)</legend>
                        <select name="bucket_id" class="select select-bordered w-full select-md focus:select-primary">
                            %s
                        </select>
                    </fieldset>
                </div>
                <fieldset class="fieldset">
                    <legend class="fieldset-legend">Description / Notes</legend>
                    <input type="text" name="notes" placeholder="e.g. Lunch with team or Groceries" class="input input-bordered w-full input-md focus:input-primary" />
                </fieldset>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="add_tx_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Record Outflow</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Edit Transaction Modal -->
    <dialog id="edit_tx_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Edit Transaction</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="edit_tx_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="grid grid-cols-2 gap-4">
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Amount</legend>
                        <input id="edit_tx_amount" type="number" step="0.01" name="amount" required placeholder="50.00" class="input input-bordered w-full input-md focus:input-primary" />
                    </fieldset>
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Date</legend>
                        <input id="edit_tx_date" type="date" name="transaction_date" required class="input input-bordered w-full input-md focus:input-primary" />
                    </fieldset>
                </div>
                <div class="grid grid-cols-2 gap-4">
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Account</legend>
                        <select id="edit_tx_account" name="account_id" class="select select-bordered w-full select-md focus:select-primary">
                            %s
                        </select>
                    </fieldset>
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend">Envelope (Bucket)</legend>
                        <select id="edit_tx_bucket" name="bucket_id" class="select select-bordered w-full select-md focus:select-primary">
                            %s
                        </select>
                    </fieldset>
                </div>
                <fieldset class="fieldset">
                    <legend class="fieldset-legend">Description / Notes</legend>
                    <input id="edit_tx_notes" type="text" name="notes" placeholder="e.g. Lunch with team or Groceries" class="input input-bordered w-full input-md focus:input-primary" />
                </fieldset>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="edit_tx_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Save Changes</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Resolve Raw Transaction Modal (Move to Envelope) -->
    <dialog id="resolve_raw_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>➡️</span>
                    <span>Resolve Bank Transaction</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="resolve_raw_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" id="resolve_raw_id" name="raw_id" value="" />
                
                <div class="bg-base-200/60 p-3 rounded-lg border border-base-300 text-sm flex justify-between items-center">
                    <div>
                        <div id="resolve_payee_display" class="font-bold"></div>
                        <div class="text-xs text-base-content/60">Bank Feed Payee</div>
                    </div>
                    <div id="resolve_amount_display" class="font-mono font-extrabold text-base"></div>
                </div>

                <fieldset class="fieldset">
                    <legend class="fieldset-legend">Assign to Envelope (Bucket)</legend>
                    <select id="resolve_bucket" name="bucket_id" class="select select-bordered w-full select-md focus:select-primary">
                        %s
                    </select>
                </fieldset>

                <fieldset class="fieldset">
                    <legend class="fieldset-legend">Counterparty Name / Party</legend>
                    <input id="resolve_party_name" type="text" name="party_name" placeholder="e.g. Żabka, Biedronka" class="input input-bordered w-full input-md focus:input-primary" />
                    <span class="text-xs text-base-content/50">Creates or maps counterparty on-the-fly.</span>
                </fieldset>

                <fieldset class="fieldset">
                    <legend class="fieldset-legend">Notes / Ledger Description</legend>
                    <input id="resolve_notes" type="text" name="notes" class="input input-bordered w-full input-md focus:input-primary" />
                </fieldset>

                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="resolve_raw_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Resolve into Ledger</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <script>
        const ACTIVE_BUDGET_ID = '%s';
        function openEditTxModal(id, amount, dateStr, notes, bucketId, accountId) {
            document.getElementById('edit_tx_amount').value = amount;
            document.getElementById('edit_tx_date').value = dateStr;
            document.getElementById('edit_tx_notes').value = notes;
            if (bucketId) {
                document.getElementById('edit_tx_bucket').value = bucketId;
            } else {
                document.getElementById('edit_tx_bucket').value = '';
            }
            if (accountId) {
                document.getElementById('edit_tx_account').value = accountId;
            }
            document.getElementById('edit_tx_form').action = '/budgets/' + ACTIVE_BUDGET_ID + '/transactions/' + id + '/update';
            edit_tx_modal.showModal();
        }

        function openResolveRawModal(rawId, acctId, amount, currency, rawPayee, suggestedBucketId) {
            document.getElementById('resolve_raw_id').value = rawId;
            document.getElementById('resolve_payee_display').innerText = rawPayee;
            document.getElementById('resolve_amount_display').innerText = amount + ' ' + currency;
            document.getElementById('resolve_notes').value = rawPayee;
            document.getElementById('resolve_party_name').value = rawPayee.split(' ')[0] || rawPayee;
            if (suggestedBucketId) {
                document.getElementById('resolve_bucket').value = suggestedBucketId;
            } else {
                document.getElementById('resolve_bucket').value = '';
            }
            document.getElementById('resolve_raw_form').action = '/budgets/' + ACTIVE_BUDGET_ID + '/transactions/raw/' + rawId + '/resolve';
            resolve_raw_modal.showModal();
        }

        function toggleAllRawCbs(master) {
            const boxes = document.querySelectorAll('.raw-select-cb');
            boxes.forEach(b => b.checked = master.checked);
        }

        function toggleAllAccounts(btn) {
            const boxes = document.querySelectorAll('input[name="account_ids"]');
            const allChecked = Array.from(boxes).every(b => b.checked);
            boxes.forEach(b => b.checked = !allChecked);
        }

        // Initialize SSE for bank sync notifications
        if (window.EventSource) {
            const sse = new EventSource('/budgets/' + ACTIVE_BUDGET_ID + '/transactions/sync/events');
            sse.addEventListener('sync-complete', (e) => {
                const data = JSON.parse(e.data || '{}');
                const alerts = document.getElementById('sync-alerts');
                if (alerts) {
                    alerts.innerHTML = '<div role="alert" class="alert alert-success shadow-lg animate-fade-in flex justify-between items-center">' +
                        '<div class="flex items-center gap-2">' +
                            '<span>✅</span>' +
                            '<div>' +
                                '<h4 class="font-bold">Bank Sync Complete</h4>' +
                                '<p class="text-xs">Successfully synced ' + (data.imported_count || 0) + ' transaction(s) across ' + (data.accounts_count || 1) + ' account(s).</p>' +
                            '</div>' +
                        '</div>' +
                        '<button class="btn btn-ghost btn-xs" onclick="window.location.reload()">Refresh Page</button>' +
                    '</div>';
                }
            });
            sse.addEventListener('sync-error', (e) => {
                const data = JSON.parse(e.data || '{}');
                const alerts = document.getElementById('sync-alerts');
                if (alerts) {
                    alerts.innerHTML = '<div role="alert" class="alert alert-error shadow-lg animate-fade-in">' +
                        '<span>❌</span>' +
                        '<div>' +
                            '<h4 class="font-bold">Bank Sync Failed</h4>' +
                            '<p class="text-xs">' + (data.error || 'An error occurred during synchronization.') + '</p>' +
                        '</div>' +
                        '</div>';
                }
            });
        }
    </script>
</body>
</html>`,
		budgetName, csrfToken,
		RenderSidebarNavigation(budgetID, "transactions"),
		txRows.String(),
		pendingRawCount,
		budgetID, csrfToken,
		rawTxRows.String(),
		RenderBottomDock(budgetID, "transactions"),
		budgetID, budgetID, csrfToken, acctCheckboxes.String(),
		budgetID, csrfToken, time.Now().UTC().Format("2006-01-02"), acctOptions.String(), bucketOptions.String(),
		csrfToken, editAcctOptions.String(), editBucketOptions.String(),
		csrfToken, bucketOptions.String(),
		budgetID)

	_ = role
	return c.HTML(http.StatusOK, html)
}

// SyncBankEvents streams real-time sync progress and completion events via SSE.
func (h *TransactionsHandler) SyncBankEvents(c echo.Context) error {
	budgetID := c.Param("budgetId")

	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	msgChan := make(chan string, 10)
	h.syncMu.Lock()
	h.syncListeners[budgetID] = append(h.syncListeners[budgetID], msgChan)
	h.syncMu.Unlock()

	defer func() {
		h.syncMu.Lock()
		defer h.syncMu.Unlock()
		listeners := h.syncListeners[budgetID]
		for i, ch := range listeners {
			if ch == msgChan {
				h.syncListeners[budgetID] = append(listeners[:i], listeners[i+1:]...)
				break
			}
		}
		close(msgChan)
	}()

	// Send initial ping
	fmt.Fprintf(w, "event: ping\ndata: connected\n\n")
	w.Flush()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-msgChan:
			if !ok {
				return nil
			}
			fmt.Fprint(w, msg)
			w.Flush()
		}
	}
}

func (h *TransactionsHandler) broadcastSyncEvent(budgetID, event, data string) {
	h.syncMu.Lock()
	defer h.syncMu.Unlock()

	listeners := h.syncListeners[budgetID]
	payload := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	for _, ch := range listeners {
		select {
		case ch <- payload:
		default:
		}
	}
}

type accountSyncResult struct {
	AccountID     string
	ImportedCount int
	Error         error
}

func (h *TransactionsHandler) executeAccountSync(ctx context.Context, tenantDB *sql.DB, userAcctID, userID, corrID string, daysWindow int) (int, error) {
	logCtx := logger.WithComponent(ctx, "biz:transaction:bank_sync:account")
	now := time.Now().UTC().UnixMicro()

	// Ensure account_integration_sync_logs table exists
	_, err := tenantDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS account_integration_sync_logs (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			synced_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_sync_logs_acct_time ON account_integration_sync_logs(account_id, synced_at DESC);
	`)
	if err != nil {
		logger.Error(logCtx, "Failed to ensure sync logs table", "error", err.Error())
	}

	// Rate limit check: max 4 imports per account in the last 24 hours (86400 seconds)
	oneDayAgo := now - 24*3600*1_000_000
	var syncCount24h int
	err = tenantDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM account_integration_sync_logs
		WHERE account_id = ? AND synced_at >= ?`, userAcctID, oneDayAgo).Scan(&syncCount24h)
	if err == nil && syncCount24h >= 4 {
		logger.Warn(logCtx, "Sync rejected: rate limit reached (max 4 per day)",
			"account_id", userAcctID,
			"sync_count", syncCount24h,
		)
		return 0, fmt.Errorf("RATE_LIMIT: Daily bank synchronization limit reached for account (maximum 4 imports per 24 hours)")
	}

	// Fetch primary currency for this account
	var acctCurrency string
	err = tenantDB.QueryRowContext(ctx, `
		SELECT primary_currency FROM accounts_current WHERE id = ?`, userAcctID).Scan(&acctCurrency)
	if err != nil {
		acctCurrency = "PLN"
	}

	// Check if this account already has any transactions (either raw_transactions or budget_transactions)
	var latestRawDate, latestBudgetTxDate sql.NullInt64
	_ = tenantDB.QueryRowContext(ctx, `
		SELECT MAX(booking_date) FROM raw_transactions_current WHERE account_id = ?`, userAcctID).Scan(&latestRawDate)
	_ = tenantDB.QueryRowContext(ctx, `
		SELECT MAX(transaction_date) FROM budget_transactions_current 
		WHERE source_account_id = ? OR destination_account_id = ?`, userAcctID, userAcctID).Scan(&latestBudgetTxDate)

	var latestKnownDate int64
	if latestRawDate.Valid && latestRawDate.Int64 > latestKnownDate {
		latestKnownDate = latestRawDate.Int64
	}
	if latestBudgetTxDate.Valid && latestBudgetTxDate.Int64 > latestKnownDate {
		latestKnownDate = latestBudgetTxDate.Int64
	}

	isFirstSync := latestKnownDate == 0

	// Determine sinceMicro
	var sinceMicro int64
	if !isFirstSync {
		// Incremental sync: only transactions newer than latest imported booking date
		sinceMicro = latestKnownDate
	} else {
		// First sync: calculate window from user input (clamp between 1 and 30 days)
		days := daysWindow
		if days < 1 {
			days = 1
		} else if days > 30 {
			days = 30
		}
		sinceMicro = now - int64(days)*24*3600*1_000_000
	}

	// Fetch transactions from mock bank adapter
	extTxs, err := h.mockAdapter.FetchTransactions(ctx, userAcctID, sinceMicro)
	if err != nil {
		logger.Error(logCtx, "Bank sync failed fetching transactions from adapter", "account_id", userAcctID, "error", err.Error())
		return 0, err
	}

	// Fetch external account balance from adapter for this account
	extAccounts, _ := h.mockAdapter.FetchAccounts(ctx, userID)
	var bankBalanceMinor int64
	for _, ea := range extAccounts {
		if ea.Currency == acctCurrency {
			bankBalanceMinor = ea.BalanceMinor
			break
		}
	}
	if bankBalanceMinor == 0 && len(extAccounts) > 0 {
		bankBalanceMinor = extAccounts[0].BalanceMinor
	}

	importedCount := 0
	var sumImportedDelta int64
	var oldestTxDate int64 = now

	for _, ext := range extTxs {
		dedup := transaction.ComputeCanonicalDedupHash(userAcctID, ext.ExternalTxID, ext.BookingDateMicro, ext.AmountMinor, ext.Currency, ext.Payee, ext.Reference)
		rawID := domain.NewUUIDv7()

		res, err := tenantDB.ExecContext(ctx, `
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, reference, status, updated_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?)
			ON CONFLICT(account_id, dedup_hash) DO NOTHING`,
			rawID, userAcctID, ext.ExternalTxID, dedup, ext.BookingDateMicro,
			ext.AmountMinor, ext.Currency, ext.Payee, ext.Reference, now,
		)
		if err == nil {
			if rows, _ := res.RowsAffected(); rows > 0 {
				importedCount++
				sumImportedDelta += ext.AmountMinor
				if ext.BookingDateMicro < oldestTxDate {
					oldestTxDate = ext.BookingDateMicro
				}
			}
		}
	}

	// If it's the first sync, ensure 'Set Balance' party and virtual external account exist,
	// then record an initial Set Balance transaction so starting balance + pending transactions = real bank balance.
	if isFirstSync {
		var setBalancePartyID, setBalanceAcctID string
		err := tenantDB.QueryRowContext(ctx, `
			SELECT p.id, a.id FROM parties_current p
			JOIN accounts_current a ON a.party_id = p.id
			WHERE p.name = 'Set Balance' AND p.is_user_owned = 0
			LIMIT 1`).Scan(&setBalancePartyID, &setBalanceAcctID)
		if err != nil {
			setBalancePartyID = domain.NewUUIDv7()
			setBalanceAcctID = domain.NewUUIDv7()
			tenantDB.ExecContext(ctx, `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'Set Balance', 'ORGANIZATION', 0, ?, ?)`, setBalancePartyID, userID, now)
			tenantDB.ExecContext(ctx, `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, 'Set Balance Offset', 'EXTERNAL', ?, 0, ?, ?)`, setBalanceAcctID, setBalancePartyID, acctCurrency, userID, now)
		}

		// Initial offset = Bank Balance - Net sum of imported transactions in window
		startingBalanceMinor := bankBalanceMinor - sumImportedDelta
		if startingBalanceMinor > 0 {
			notes := "Starting Balance (Set Balance Offset)"
			_, err = transaction.ExecuteResolution(ctx, tenantDB, transaction.ResolveResolutionInput{
				OperationType: transaction.OpDirectMove,
				BudgetTransactions: []transaction.BudgetTxInput{
					{
						SourceAccountID:      setBalanceAcctID,
						DestinationAccountID: userAcctID,
						SourceAmountMinor:    startingBalanceMinor,
						SourceCurrency:       acctCurrency,
						DestAmountMinor:      startingBalanceMinor,
						DestCurrency:         acctCurrency,
						BucketID:             nil, // Ready to assign
						Notes:                &notes,
						TransactionDate:      oldestTxDate,
					},
				},
				AgentID:       userID,
				CorrelationID: corrID,
			})
			if err != nil {
				logger.Error(logCtx, "Failed creating initial Set Balance transaction", "error", err.Error())
			} else {
				logger.Info(logCtx, "Created Set Balance starting transaction",
					"account_id", userAcctID,
					"bank_balance", bankBalanceMinor,
					"sum_imported_delta", sumImportedDelta,
					"starting_balance", startingBalanceMinor,
					"tx_date", oldestTxDate,
				)
			}
		}
	}

	// Log successful sync for rate limit accounting
	logID := domain.NewUUIDv7()
	_, _ = tenantDB.ExecContext(ctx, `
		INSERT INTO account_integration_sync_logs (id, account_id, synced_at)
		VALUES (?, ?, ?)`, logID, userAcctID, now)

	logger.Info(logCtx, "Account bank sync succeeded",
		"account_id", userAcctID,
		"is_first_sync", isFirstSync,
		"imported_count", importedCount,
	)

	return importedCount, nil
}

// SyncWithBank triggers bank integration synchronization, imports raw transactions and runs auto-matching.
func (h *TransactionsHandler) SyncWithBank(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:bank_sync")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	logger.Info(ctx, "Starting bank transaction synchronization",
		"budget_id", budgetID,
		"user_id", userID,
	)

	// Concurrency check: Reject if a bank sync is already in progress for this budget
	h.syncMu.Lock()
	if h.activeSyncs[budgetID] {
		h.syncMu.Unlock()
		logger.Warn(ctx, "Concurrent bank sync rejected", "budget_id", budgetID)
		if c.Request().Header.Get("HX-Request") == "true" {
			return c.HTML(http.StatusConflict, `
				<div role="alert" class="alert alert-warning shadow-lg animate-fade-in">
					<span>⚠️</span>
					<div>
						<h4 class="font-bold">Sync already in progress</h4>
						<p class="text-xs">Another synchronization is currently running for this budget. Please wait for it to complete.</p>
					</div>
				</div>
			`)
		}
		return echo.NewHTTPError(http.StatusConflict, "Bank synchronization is already running for this budget. Please wait.")
	}
	h.activeSyncs[budgetID] = true
	h.syncMu.Unlock()

	// Parse days_window (default 30)
	daysWindow := 30
	if dStr := c.FormValue("days_window"); dStr != "" {
		if parsedDays, err := strconv.Atoi(dStr); err == nil {
			daysWindow = parsedDays
		}
	}

	// Parse accounts to sync (support multi-select "account_ids" or single "account_id")
	var accountIDs []string
	formParams, err := c.FormParams()
	if err == nil {
		accountIDs = formParams["account_ids"]
	}
	if len(accountIDs) == 0 {
		if singleID := c.FormValue("account_id"); singleID != "" {
			accountIDs = append(accountIDs, singleID)
		}
	}

	// If still empty, fetch first user-owned checking account
	if len(accountIDs) == 0 {
		var firstAcctID string
		err := tenantDB.QueryRowContext(c.Request().Context(), `
			SELECT a.id FROM accounts_current a
			JOIN parties_current p ON a.party_id = p.id
			WHERE p.is_user_owned = 1
			ORDER BY a.name ASC
			LIMIT 1`).Scan(&firstAcctID)
		if err == nil {
			accountIDs = append(accountIDs, firstAcctID)
		} else {
			now := time.Now().UTC().UnixMicro()
			pID := domain.NewUUIDv7()
			firstAcctID = domain.NewUUIDv7()
			tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, ?, ?)`, pID, userID, now)
			tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, ?, ?)`, firstAcctID, pID, userID, now)
			accountIDs = append(accountIDs, firstAcctID)
		}
	}

	isAsync := c.Request().Header.Get("HX-Request") == "true" || c.FormValue("async") == "true"

	syncFunc := func(opCtx context.Context) ([]accountSyncResult, error) {
		defer func() {
			h.syncMu.Lock()
			delete(h.activeSyncs, budgetID)
			h.syncMu.Unlock()
		}()

		var results []accountSyncResult
		totalImported := 0
		var firstErr error

		for i, acctID := range accountIDs {
			h.broadcastSyncEvent(budgetID, "sync-progress", fmt.Sprintf(`{"account_id":"%s","index":%d,"total":%d}`, acctID, i+1, len(accountIDs)))
			imported, sErr := h.executeAccountSync(opCtx, tenantDB, acctID, userID, corrID, daysWindow)
			results = append(results, accountSyncResult{
				AccountID:     acctID,
				ImportedCount: imported,
				Error:         sErr,
			})
			if sErr != nil && firstErr == nil {
				firstErr = sErr
			}
			totalImported += imported
		}

		if firstErr != nil {
			h.broadcastSyncEvent(budgetID, "sync-error", fmt.Sprintf(`{"error":"%s"}`, firstErr.Error()))
		} else {
			h.broadcastSyncEvent(budgetID, "sync-complete", fmt.Sprintf(`{"imported_count":%d,"accounts_count":%d}`, totalImported, len(accountIDs)))
		}

		return results, firstErr
	}

	if isAsync {
		bgCtx := context.WithoutCancel(c.Request().Context())
		go syncFunc(bgCtx)
		return c.HTML(http.StatusAccepted, `
			<div role="alert" class="alert alert-info shadow-lg animate-fade-in">
				<span class="loading loading-spinner loading-sm text-info"></span>
				<div>
					<h4 class="font-bold">Sync in Progress</h4>
					<p class="text-xs">Synchronizing selected bank accounts in the background. You'll receive a notification upon completion.</p>
				</div>
			</div>
		`)
	}

	// Synchronous execution
	results, syncErr := syncFunc(c.Request().Context())
	if syncErr != nil {
		if strings.HasPrefix(syncErr.Error(), "RATE_LIMIT:") {
			return echo.NewHTTPError(http.StatusTooManyRequests, syncErr.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, syncErr.Error())
	}

	_ = results
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// AddManualTransaction handles direct cash transaction entry.
func (h *TransactionsHandler) AddManualTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:manual")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	var amountFloat float64
	fmt.Sscanf(c.FormValue("amount"), "%f", &amountFloat)
	amountMinor := int64(amountFloat * 100)

	logger.Info(ctx, "Recording manual cash transaction",
		"budget_id", budgetID,
		"user_id", userID,
		"amount_minor", amountMinor,
		"notes", c.FormValue("notes"),
	)

	if amountMinor <= 0 {
		logger.Warn(ctx, "Manual transaction rejected: invalid amount", "amount_minor", amountMinor)
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid transaction amount")
	}

	now := time.Now().UTC().UnixMicro()

	// Parse custom transaction date if provided (YYYY-MM-DD)
	txDate := now
	dateForm := c.FormValue("transaction_date")
	if dateForm != "" {
		if parsedDate, err := time.Parse("2006-01-02", dateForm); err == nil {
			txDate = parsedDate.UTC().UnixMicro()
		}
	}

	userAcctID := c.FormValue("account_id")
	if userAcctID == "" {
		err := tenantDB.QueryRowContext(c.Request().Context(), `
			SELECT a.id FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE p.is_user_owned = 1 LIMIT 1`).Scan(&userAcctID)
		if err != nil {
			pID := domain.NewUUIDv7()
			userAcctID = domain.NewUUIDv7()
			tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, ?, ?)`, pID, userID, now)
			tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, ?, ?)`, userAcctID, pID, userID, now)
		}
	}

	var extAcctID string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT a.id FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE p.is_user_owned = 0 LIMIT 1`).Scan(&extAcctID)
	if err != nil {
		pID := domain.NewUUIDv7()
		extAcctID = domain.NewUUIDv7()
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, 'General Merchant', 'ORGANIZATION', 0, ?, ?)`, pID, userID, now)
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, 'Merchant External', 'EXTERNAL', 'PLN', 0, ?, ?)`, extAcctID, pID, userID, now)
	}

	notes := c.FormValue("notes")
	var bucketIDPtr *string
	bucketIDVal := c.FormValue("bucket_id")
	if bucketIDVal != "" {
		bucketIDPtr = &bucketIDVal
	}

	opID, err := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
		OperationType: transaction.OpDirectMove,
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      userAcctID,
				DestinationAccountID: extAcctID,
				SourceAmountMinor:    amountMinor,
				SourceCurrency:       "PLN",
				DestAmountMinor:      amountMinor,
				DestCurrency:         "PLN",
				BucketID:             bucketIDPtr,
				Notes:                &notes,
				TransactionDate:      txDate,
			},
		},
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		logger.Error(ctx, "Failed executing resolution for manual transaction", "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to save transaction: %v", err))
	}

	logger.Info(ctx, "Manual transaction recorded successfully",
		"budget_id", budgetID,
		"operation_id", opID,
		"source_account_id", userAcctID,
		"dest_account_id", extAcctID,
		"amount_minor", amountMinor,
	)

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// UpdateTransaction handles editing an existing transaction.
func (h *TransactionsHandler) UpdateTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:update")

	budgetID := c.Param("budgetId")
	txID := c.Param("txId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	var amountFloat float64
	fmt.Sscanf(c.FormValue("amount"), "%f", &amountFloat)
	amountMinor := int64(amountFloat * 100)

	if amountMinor <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid transaction amount")
	}

	// Fetch existing transaction details
	var curSrcAcctID, curDstAcctID, curSrcCurr, curDstCurr string
	var curTxDate int64
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT source_account_id, destination_account_id, source_currency, destination_currency, transaction_date
		FROM budget_transactions_current WHERE id = ?`, txID).Scan(
		&curSrcAcctID, &curDstAcctID, &curSrcCurr, &curDstCurr, &curTxDate,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Transaction not found")
	}

	newSrcAcctID := c.FormValue("account_id")
	if newSrcAcctID == "" {
		newSrcAcctID = curSrcAcctID
	}

	txDate := curTxDate
	dateForm := c.FormValue("transaction_date")
	if dateForm != "" {
		if parsedDate, err := time.Parse("2006-01-02", dateForm); err == nil {
			txDate = parsedDate.UTC().UnixMicro()
		}
	}

	notes := c.FormValue("notes")
	var bucketIDPtr *string
	bucketIDVal := c.FormValue("bucket_id")
	if bucketIDVal != "" {
		bucketIDPtr = &bucketIDVal
	}

	err = transaction.UpdateBudgetTransaction(c.Request().Context(), tenantDB, transaction.UpdateBudgetTxInput{
		ID:                   txID,
		SourceAccountID:      newSrcAcctID,
		DestinationAccountID: curDstAcctID,
		SourceAmountMinor:    amountMinor,
		SourceCurrency:       curSrcCurr,
		DestAmountMinor:      amountMinor,
		DestCurrency:         curDstCurr,
		BucketID:             bucketIDPtr,
		TransactionDate:      txDate,
		Notes:                &notes,
		AgentID:              userID,
		CorrelationID:        corrID,
	})
	if err != nil {
		logger.Error(ctx, "Failed updating transaction", "tx_id", txID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to update transaction: %v", err))
	}

	logger.Info(ctx, "Transaction updated successfully", "budget_id", budgetID, "tx_id", txID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// DeleteTransaction handles deleting an existing transaction.
func (h *TransactionsHandler) DeleteTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:delete")

	budgetID := c.Param("budgetId")
	txID := c.Param("txId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	err := transaction.DeleteBudgetTransaction(c.Request().Context(), tenantDB, txID, userID, corrID)
	if err != nil {
		logger.Error(ctx, "Failed deleting transaction", "tx_id", txID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to delete transaction: %v", err))
	}

	logger.Info(ctx, "Transaction deleted successfully", "budget_id", budgetID, "tx_id", txID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// ResolveRawTransaction moves a single raw bank transaction into a budget transaction.
func (h *TransactionsHandler) ResolveRawTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:resolve")

	budgetID := c.Param("budgetId")
	rawID := c.Param("rawId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	// 1. Fetch raw transaction details
	var userAcctID, currency, rawPayee string
	var extTxID, counterpartyIBAN, ref sql.NullString
	var bookingDate sql.NullInt64
	var amountMinor int64
	var status string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT account_id, external_transaction_id, booking_date, amount_minor, currency,
		       raw_payee, counterparty_iban, reference, status
		FROM raw_transactions_current WHERE id = ?`, rawID).Scan(
		&userAcctID, &extTxID, &bookingDate, &amountMinor, &currency,
		&rawPayee, &counterpartyIBAN, &ref, &status,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Raw transaction not found")
	}
	if status != "PENDING" {
		return echo.NewHTTPError(http.StatusBadRequest, "Raw transaction is not in PENDING status")
	}

	partyName := strings.TrimSpace(c.FormValue("party_name"))
	if partyName == "" {
		partyName = strings.TrimSpace(rawPayee)
		if partyName == "" {
			partyName = "External Merchant"
		}
	}

	// 2. Find or create counterparty party & external account on-the-fly
	var counterpartyAcctID string
	err = tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT a.id FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.name = ? AND p.is_user_owned = 0
		LIMIT 1`, partyName).Scan(&counterpartyAcctID)
	if err != nil {
		pID := domain.NewUUIDv7()
		counterpartyAcctID = domain.NewUUIDv7()
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`, pID, partyName, userID, now)
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'EXTERNAL', ?, 0, ?, ?)`, counterpartyAcctID, pID, partyName, currency, userID, now)
	}

	var bucketIDPtr *string
	bucketIDVal := c.FormValue("bucket_id")
	if bucketIDVal != "" {
		bucketIDPtr = &bucketIDVal
	}

	notes := strings.TrimSpace(c.FormValue("notes"))
	if notes == "" {
		notes = rawPayee
	}

	txDate := now
	if bookingDate.Valid {
		txDate = bookingDate.Int64
	}

	// 3. Setup Double-Entry source & destination
	var srcAcct, dstAcct string
	var absAmountMinor int64
	if amountMinor < 0 {
		// Outflow / Expense: User Account -> External Counterparty
		srcAcct = userAcctID
		dstAcct = counterpartyAcctID
		absAmountMinor = -amountMinor
	} else {
		// Inflow / Income: External Counterparty -> User Account
		srcAcct = counterpartyAcctID
		dstAcct = userAcctID
		absAmountMinor = amountMinor
	}

	opID, err := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
		OperationType:     transaction.OpDirectMove,
		RawTransactionIDs: []string{rawID},
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      srcAcct,
				DestinationAccountID: dstAcct,
				SourceAmountMinor:    absAmountMinor,
				SourceCurrency:       currency,
				DestAmountMinor:      absAmountMinor,
				DestCurrency:         currency,
				BucketID:             bucketIDPtr,
				Notes:                &notes,
				TransactionDate:      txDate,
			},
		},
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		logger.Error(ctx, "Failed resolving raw transaction", "raw_id", rawID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to resolve transaction: %v", err))
	}

	logger.Info(ctx, "Raw transaction resolved successfully",
		"budget_id", budgetID,
		"raw_id", rawID,
		"operation_id", opID,
	)

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// IgnoreRawTransaction marks a raw bank transaction as IGNORED.
func (h *TransactionsHandler) IgnoreRawTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:ignore")

	budgetID := c.Param("budgetId")
	rawID := c.Param("rawId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	var rawVersion int
	var accountID, dedupHash, currency, rawPayee string
	var extTxID, counterpartyIBAN, reference sql.NullString
	var bookingDate, valueDate sql.NullInt64
	var amountMinor int64

	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
		       amount_minor, currency, raw_payee, counterparty_iban, reference
		FROM raw_transactions_current WHERE id = ? AND status = 'PENDING'`, rawID).Scan(
		&rawVersion, &accountID, &extTxID, &dedupHash, &bookingDate, &valueDate,
		&amountMinor, &currency, &rawPayee, &counterpartyIBAN, &reference,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Pending raw transaction not found")
	}

	newVersion := rawVersion + 1
	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO raw_transactions (
			id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
			amount_minor, currency, raw_payee, counterparty_iban, reference, status, action,
			agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'IGNORED', 'UPDATE', ?, ?, ?)`,
		rawID, newVersion, accountID, extTxID, dedupHash, bookingDate, valueDate,
		amountMinor, currency, rawPayee, counterpartyIBAN, reference,
		userID, corrID, now,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE raw_transactions_current
		SET version = ?, status = 'IGNORED', updated_at = ?
		WHERE id = ?`, newVersion, now, rawID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	logger.Info(ctx, "Raw transaction ignored successfully", "budget_id", budgetID, "raw_id", rawID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// AutoMatchRawTransactions runs auto-matching rules across all pending raw transactions.
func (h *TransactionsHandler) AutoMatchRawTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:reconcile:auto_match")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	rulesEngine, err := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)
	if err != nil || rulesEngine == nil {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
	}

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, account_id, booking_date, amount_minor, currency,
		       COALESCE(raw_payee, ''), COALESCE(counterparty_iban, ''), COALESCE(reference, '')
		FROM raw_transactions_current
		WHERE status = 'PENDING'
		ORDER BY booking_date ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type rawItem struct {
		id          string
		accountID   string
		date        int64
		amountMinor int64
		currency    string
		payee       string
		iban        string
		ref         string
	}
	var items []rawItem
	for rows.Next() {
		var it rawItem
		rows.Scan(&it.id, &it.accountID, &it.date, &it.amountMinor, &it.currency, &it.payee, &it.iban, &it.ref)
		items = append(items, it)
	}

	matchedCount := 0
	for _, it := range items {
		match := rulesEngine.Match(it.payee, it.iban, it.ref)
		if match == nil {
			continue
		}

		counterpartyAcctID := match.TargetCounterpartyAcctID
		if counterpartyAcctID == "" {
			// Create counterparty account if rule had party
			continue
		}

		var srcAcct, dstAcct string
		var absAmountMinor int64
		if it.amountMinor < 0 {
			srcAcct = it.accountID
			dstAcct = counterpartyAcctID
			absAmountMinor = -it.amountMinor
		} else {
			srcAcct = counterpartyAcctID
			dstAcct = it.accountID
			absAmountMinor = it.amountMinor
		}

		notes := it.payee
		_, sErr := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
			OperationType:     transaction.OpDirectMove,
			RawTransactionIDs: []string{it.id},
			BudgetTransactions: []transaction.BudgetTxInput{
				{
					SourceAccountID:      srcAcct,
					DestinationAccountID: dstAcct,
					SourceAmountMinor:    absAmountMinor,
					SourceCurrency:       it.currency,
					DestAmountMinor:      absAmountMinor,
					DestCurrency:         it.currency,
					BucketID:             match.DefaultBucketID,
					Notes:                &notes,
					TransactionDate:      it.date,
				},
			},
			AgentID:       userID,
			CorrelationID: corrID,
		})
		if sErr == nil {
			matchedCount++
		}
	}

	logger.Info(ctx, "Auto-match completed", "budget_id", budgetID, "matched_count", matchedCount, "evaluated", len(items))
	_ = now
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}
