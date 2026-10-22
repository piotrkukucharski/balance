package handlers

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/balance/balance/internal/service"
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
	appService    *service.AppService
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

func (h *TransactionsHandler) SetService(svc *service.AppService) {
	h.appService = svc
}

// TransactionsView renders the unified transactions ledger, triage workbench, and rapid entry toolbar.
func (h *TransactionsHandler) TransactionsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var budgetName, defaultCurr string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr)
	if defaultCurr == "" {
		defaultCurr = "PLN"
	}

	filterAcctID := c.QueryParam("account_id")

	// 1. Fetch buckets for dropdowns
	bucketRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name FROM buckets_projection 
		WHERE is_archived = 0 
		ORDER BY name ASC`)
	var bucketOptions strings.Builder
	var editBucketOptions strings.Builder
	var qaBucketOptions strings.Builder
	bucketOptions.WriteString(`<option value="">Unassigned</option>`)
	editBucketOptions.WriteString(`<option value="">Unassigned</option>`)
	qaBucketOptions.WriteString(`<option value="">📂 Select Envelope (Bucket)</option>`)
	if err == nil {
		defer bucketRows.Close()
		for bucketRows.Next() {
			var bID, bName string
			bucketRows.Scan(&bID, &bName)
			opt := fmt.Sprintf(`<option value="%s">%s</option>`, bID, bName)
			bucketOptions.WriteString(opt)
			editBucketOptions.WriteString(opt)
			qaBucketOptions.WriteString(opt)
		}
	}

	// 2. Fetch user checking/cash accounts with balances for pills & dropdowns
	acctRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.id, a.name, a.primary_currency, COALESCE(acb.balance_minor, 0) 
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN account_currency_balances acb ON a.id = acb.account_id AND a.primary_currency = acb.currency
		WHERE p.is_user_owned = 1 AND a.is_closed = 0
		ORDER BY a.name ASC`)
	var acctOptions strings.Builder
	var editAcctOptions strings.Builder
	var qaDestAcctOptions strings.Builder
	var acctCheckboxes strings.Builder
	var acctPills strings.Builder

	// All accounts filter pill
	allActiveCls := "btn-primary font-bold shadow-xs"
	if filterAcctID != "" {
		allActiveCls = "btn-ghost border border-base-300"
	}
	acctPills.WriteString(fmt.Sprintf(`
		<a href="/budgets/%s/transactions" class="btn btn-sm %s">
			<span>🏦 All Accounts</span>
		</a>`, budgetID, allActiveCls))

	firstUserAcctID := ""
	if err == nil {
		defer acctRows.Close()
		for acctRows.Next() {
			var aID, aName, aCurr string
			var aBalMinor int64
			acctRows.Scan(&aID, &aName, &aCurr, &aBalMinor)
			if firstUserAcctID == "" {
				firstUserAcctID = aID
			}

			aBalMajor := float64(aBalMinor) / 100.0

			optSelected := ""
			if filterAcctID != "" && filterAcctID == aID {
				optSelected = "selected"
			}
			opt := fmt.Sprintf(`<option value="%s" %s>%s (%s)</option>`, aID, optSelected, aName, aCurr)
			acctOptions.WriteString(opt)
			editAcctOptions.WriteString(opt)
			qaDestAcctOptions.WriteString(fmt.Sprintf(`<option value="%s">⇄ %s (%s)</option>`, aID, aName, aCurr))

			checkbox := fmt.Sprintf(`
				<label class="label cursor-pointer justify-start gap-3 py-1.5 px-2 hover:bg-base-200 rounded-lg transition-colors">
					<input type="checkbox" name="account_ids" value="%s" checked class="checkbox checkbox-primary checkbox-sm" />
					<span class="label-text font-medium text-sm">%s <span class="text-xs text-base-content/60">(%.2f %s)</span></span>
				</label>`, aID, aName, aBalMajor, aCurr)
			acctCheckboxes.WriteString(checkbox)

			// Account filter pill
			pillActiveCls := "btn-ghost border border-base-300"
			if filterAcctID == aID {
				pillActiveCls = "btn-primary font-bold shadow-xs"
			}
			acctPills.WriteString(fmt.Sprintf(`
				<a href="/budgets/%s/transactions?account_id=%s" class="btn btn-sm %s flex items-center gap-1.5">
					<span class="font-medium">%s</span>
					<span class="font-mono text-xs opacity-75">(%.2f %s)</span>
				</a>`, budgetID, aID, pillActiveCls, aName, aBalMajor, aCurr))
		}
	}

	// 3. Fetch external parties for autocomplete suggestions
	var partySuggestions strings.Builder
	partyRows, pErr := tenantDB.QueryContext(c.Request().Context(), `
		SELECT DISTINCT name FROM parties_current 
		WHERE is_user_owned = 0 AND name != '' 
		ORDER BY name ASC 
		LIMIT 100`)
	if pErr == nil {
		defer partyRows.Close()
		for partyRows.Next() {
			var pName string
			partyRows.Scan(&pName)
			escaped := strings.ReplaceAll(pName, `"`, `&quot;`)
			partySuggestions.WriteString(fmt.Sprintf(`<option value="%s"></option>`, escaped))
		}
	}

	// 4. Fetch budget transactions (filtered by account if specified)
	query := `
		SELECT bt.id, bt.source_amount_minor, bt.source_currency, bt.transaction_date,
		       COALESCE(bt.notes, ''), COALESCE(bp.name, 'Unassigned'), COALESCE(bt.bucket_id, ''),
		       bt.source_account_id, bt.destination_account_id,
		       sa.name, da.name, sp.is_user_owned, dp.is_user_owned
		FROM budget_transactions_current bt
		LEFT JOIN buckets_projection bp ON bt.bucket_id = bp.id
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN parties_current dp ON da.party_id = dp.id`
	var queryArgs []any
	if filterAcctID != "" {
		query += ` WHERE bt.source_account_id = ? OR bt.destination_account_id = ?`
		queryArgs = append(queryArgs, filterAcctID, filterAcctID)
	}
	query += ` ORDER BY bt.transaction_date DESC LIMIT 150`

	rows, err := tenantDB.QueryContext(c.Request().Context(), query, queryArgs...)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading transactions: "+err.Error())
	}
	defer rows.Close()

	var txRows strings.Builder
	hasTxs := false
	csrfToken, _ := c.Get("csrfToken").(string)

	for rows.Next() {
		hasTxs = true
		var id, curr, notes, bucketName, bucketIDVal, srcAcctID, dstAcctID, srcAcctName, dstAcctName string
		var amountMinor, txDate int64
		var srcUserOwned, dstUserOwned int
		rows.Scan(&id, &amountMinor, &curr, &txDate, &notes, &bucketName, &bucketIDVal,
			&srcAcctID, &dstAcctID, &srcAcctName, &dstAcctName, &srcUserOwned, &dstUserOwned)

		dateStr := time.UnixMicro(txDate).Format("2006-01-02")
		amountMajor := float64(amountMinor) / 100.0

		escapedNotes := strings.ReplaceAll(notes, "'", "\\'")
		escapedNotes = strings.ReplaceAll(escapedNotes, "\"", "&quot;")

		// Format amount & account display based on double-entry type
		amountDisplay := fmt.Sprintf(`-%.2f <span class="text-xs font-normal text-base-content/60">%s</span>`, amountMajor, curr)
		amountClass := "text-error"
		accountDisplay := fmt.Sprintf(`<span class="text-xs font-medium">%s</span>`, srcAcctName)

		if srcUserOwned == 0 && dstUserOwned == 1 {
			// Income: external party -> user account
			amountDisplay = fmt.Sprintf(`+%.2f <span class="text-xs font-normal text-base-content/60">%s</span>`, amountMajor, curr)
			amountClass = "text-success"
			accountDisplay = fmt.Sprintf(`<span class="text-xs font-medium text-success">%s</span>`, dstAcctName)
		} else if srcUserOwned == 1 && dstUserOwned == 1 {
			// Internal transfer
			amountDisplay = fmt.Sprintf(`⇄ %.2f <span class="text-xs font-normal text-base-content/60">%s</span>`, amountMajor, curr)
			amountClass = "text-info"
			accountDisplay = fmt.Sprintf(`<span class="text-xs font-medium text-info">%s → %s</span>`, srcAcctName, dstAcctName)
		}

		bucketBadge := fmt.Sprintf(`<span class="badge badge-soft badge-primary font-semibold text-xs">%s</span>`, bucketName)
		if bucketName == "Unassigned" {
			bucketBadge = `<span class="badge badge-soft badge-ghost text-xs">Unassigned</span>`
		}

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
			<tr class="hover group transition-colors">
				<td class="font-mono text-xs text-base-content/80 whitespace-nowrap">%s</td>
				<td>%s</td>
				<td class="text-sm font-semibold max-w-xs truncate" title="%s">%s</td>
				<td>%s</td>
				<td class="font-mono text-right font-bold text-sm whitespace-nowrap %s">
					%s
				</td>
				<td class="text-right">
					%s
				</td>
			</tr>`,
			dateStr, bucketBadge, escapedNotes, notes, accountDisplay,
			amountClass, amountDisplay, actionMenu,
		))
	}

	if !hasTxs {
		txRows.WriteString(`
			<tr>
				<td colspan="6" class="text-center py-12 text-base-content/50">
					<div class="text-3xl mb-2">💸</div>
					<div class="font-semibold text-sm">No transactions recorded yet.</div>
					<div class="text-xs text-base-content/40 mt-1">Use 🔄 Sync with Bank or enter one via the quick-add row above.</div>
				</td>
			</tr>
		`)
	}

	// 5. Fetch pending raw transactions (unresolved bank feeds)
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
	matchedRulesCount := 0

	if err == nil {
		defer rawRows.Close()

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
					matchedRulesCount++
				}
			}

			escapedPayee := strings.ReplaceAll(rPayee, "'", "\\'")
			escapedPayee = strings.ReplaceAll(escapedPayee, "\"", "&quot;")

			// Build bucket options with pre-selected suggested bucket
			var inlineBucketOptions strings.Builder
			inlineBucketOptions.WriteString(`<option value="">Unassigned</option>`)
			// We can reconstruct bucket options with suggested pre-selected
			bRows2, _ := tenantDB.QueryContext(c.Request().Context(), `SELECT id, name FROM buckets_projection WHERE is_archived = 0 ORDER BY name ASC`)
			if bRows2 != nil {
				for bRows2.Next() {
					var bID, bName string
					bRows2.Scan(&bID, &bName)
					sel := ""
					if bID == suggestedBucketID {
						sel = "selected"
					}
					inlineBucketOptions.WriteString(fmt.Sprintf(`<option value="%s" %s>%s</option>`, bID, sel, bName))
				}
				bRows2.Close()
			}

			rawActionMenu := RenderPopoverActionMenu("raw", rID, []TableActionItem{
				{
					Label:   "➡️ Detailed Resolve Modal",
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
				<tr class="hover group transition-colors">
					<td class="w-10 text-center">
						<input type="checkbox" name="selected_raw_ids" value="%s" 
						       data-raw-id="%s" data-amount="%d" data-currency="%s" data-payee="%s"
						       class="checkbox checkbox-primary checkbox-xs raw-select-cb" 
						       onchange="updateFloatingActionBar()" />
					</td>
					<td class="font-mono text-xs whitespace-nowrap">%s</td>
					<td class="text-xs font-medium text-base-content/80 whitespace-nowrap">%s</td>
					<td class="max-w-xs">
						<div class="font-bold text-sm truncate" title="%s">%s</div>
						<div class="text-xs text-base-content/50 truncate" title="%s">%s</div>
					</td>
					<td>%s</td>
					<td class="font-mono text-right font-bold text-sm whitespace-nowrap %s">
						%s%.2f <span class="text-xs font-normal text-base-content/60">%s</span>
					</td>
					<td>
						<form method="POST" action="/budgets/%s/transactions/raw/%s/resolve" class="flex items-center gap-1.5 justify-end">
							<input type="hidden" name="csrf_token" value="%s" />
							<input type="hidden" name="party_name" value="%s" />
							<input type="hidden" name="notes" value="%s" />
							<select name="bucket_id" class="select select-xs select-bordered w-36 focus:select-primary text-xs font-medium">
								%s
							</select>
							<button type="submit" class="btn btn-success btn-xs font-bold gap-1 shadow-xs" title="1-Click Approve (Direct Move)">
								<span>✓</span>
								<span>Approve</span>
							</button>
						</form>
					</td>
					<td class="text-right">
						%s
					</td>
				</tr>`,
				rID, rID, rAmountMinor, rCurrency, escapedPayee,
				rDateStr, rAcctName, rPayee, rPayee, rRef, rRef, suggestedBadge,
				amountBadgeClass, amountPrefix, rAmountMajor, rCurrency,
				budgetID, rID, csrfToken, escapedPayee, escapedPayee,
				inlineBucketOptions.String(), rawActionMenu,
			))
		}
	}

	if pendingRawCount == 0 {
		rawTxRows.WriteString(`
			<tr>
				<td colspan="8" class="text-center py-10 text-base-content/50">
					<div class="text-3xl mb-2">🎉</div>
					<div class="font-bold text-base text-base-content/80">All bank transactions are reconciled. No pending bank feeds!</div>
					<div class="text-xs text-base-content/50 mt-1">Use 🔄 Sync with Bank to fetch newly cleared transactions from your accounts.</div>
				</td>
			</tr>
		`)
	}

	todayDateStr := time.Now().UTC().Format("2006-01-02")

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
    <!-- Party Autocomplete Suggestions -->
    <datalist id="party_suggestions">
        %s
    </datalist>

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

        <main class="flex-1 p-4 md:p-8 overflow-y-auto pb-28">
            <!-- Primary Header Toolbar -->
            <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-5">
                <div>
                    <h1 class="text-2xl font-black tracking-tight flex items-center gap-2">
                        <span>💸</span>
                        <span>Transactions Ledger</span>
                    </h1>
                    <p class="text-xs text-base-content/60 mt-0.5">Unified double-entry cashflow workbench and rapid bank reconciliation.</p>
                </div>
                <div class="flex items-center gap-2.5">
                    <button type="button" class="btn btn-secondary btn-sm shadow font-semibold gap-1.5" onclick="bank_sync_modal.showModal()">
                        <span>🔄</span>
                        <span>Sync with Bank</span>
                    </button>
                    <button type="button" class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="focusQuickAdd()">
                        <span>➕</span>
                        <span>Add Transaction <kbd class="kbd kbd-xs bg-primary-content/20 text-primary-content border-none">N</kbd></span>
                    </button>
                </div>
            </div>

            <!-- Account Filter Bar (Pills) -->
            <div class="flex items-center gap-2 overflow-x-auto pb-3 mb-4 scrollbar-none">
                %s
            </div>

            <!-- Sync Alerts Container -->
            <div id="sync-alerts" class="mb-4"></div>

            <!-- 1. PENDING BANK FEEDS SECTION (Triage Queue) -->
            <div class="card card-border bg-base-100 border-base-300 shadow-md mb-6">
                <div class="card-header bg-base-200/50 px-5 py-3.5 border-b border-base-300 flex flex-wrap justify-between items-center gap-3">
                    <div class="flex items-center gap-2.5">
                        <span class="text-lg">🏦</span>
                        <h2 class="font-extrabold text-sm tracking-wide">
                            Pending Bank Feeds
                        </h2>
                        <span class="badge badge-primary font-bold text-xs">%d</span>
                        <span class="text-xs text-base-content/50 hidden sm:inline">• Review and resolve bank imports into budget envelopes</span>
                    </div>
                    <div class="flex items-center gap-2">
                        <form method="POST" action="/budgets/%s/transactions/reconcile/auto-match">
                            <input type="hidden" name="csrf_token" value="%s" />
                            <button type="submit" class="btn btn-secondary btn-xs shadow-xs font-semibold gap-1" title="Auto-match pending feeds matching existing rules">
                                <span>⚡</span>
                                <span>Auto-Match (%d)</span>
                            </button>
                        </form>
                    </div>
                </div>
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra table-sm w-full">
                            <thead>
                                <tr class="bg-base-200/30 text-xs text-base-content/70">
                                    <th class="w-10 text-center">
                                        <input type="checkbox" class="checkbox checkbox-primary checkbox-xs" onclick="toggleAllRawCbs(this)" title="Select all" />
                                    </th>
                                    <th class="font-bold">Booking Date</th>
                                    <th class="font-bold">Account</th>
                                    <th class="font-bold">Raw Payee / Description</th>
                                    <th class="font-bold">Rule Match</th>
                                    <th class="font-bold text-right">Amount</th>
                                    <th class="font-bold text-right">Assign & 1-Click Resolve</th>
                                    <th class="font-bold text-right w-12">Actions</th>
                                </tr>
                            </thead>
                            <tbody>
                                %s
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>

            <!-- 2. BUDGET TRANSACTIONS LEDGER (Main Journal) -->
            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-header bg-base-200/50 px-5 py-3.5 border-b border-base-300 flex justify-between items-center">
                    <div class="flex items-center gap-2">
                        <span class="text-lg">📒</span>
                        <h2 class="font-extrabold text-sm tracking-wide">Budget Transactions Ledger</h2>
                        <span class="text-xs text-base-content/50 hidden sm:inline">• Active double-entry envelope activity</span>
                    </div>
                    <div class="text-xs text-base-content/60 font-mono">
                        Press <kbd class="kbd kbd-xs">N</kbd> or <kbd class="kbd kbd-xs">A</kbd> to quick-add
                    </div>
                </div>
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra table-sm w-full">
                            <thead>
                                <tr class="bg-base-200/30 text-xs text-base-content/70">
                                    <th class="font-bold">Date</th>
                                    <th class="font-bold">Envelope (Bucket)</th>
                                    <th class="font-bold">Notes / Payee</th>
                                    <th class="font-bold">Account Flow</th>
                                    <th class="font-bold text-right">Amount</th>
                                    <th class="font-bold text-right w-12">Actions</th>
                                </tr>
                            </thead>
                            <tbody>
                                <!-- INLINE QUICK-ADD ROW -->
                                <tr id="quick-add-row" class="bg-base-200/80 border-b-2 border-primary/40">
                                    <td colspan="6" class="p-3">
                                        <form id="quick_add_form" method="POST" action="/budgets/%s/transactions" class="flex flex-col gap-2.5">
                                            <input type="hidden" name="csrf_token" value="%s" />
                                            <input type="hidden" id="qa_tx_type" name="tx_type" value="EXPENSE" />

                                            <div class="flex flex-wrap items-center justify-between gap-2">
                                                <div class="join shadow-xs">
                                                    <button type="button" class="join-item btn btn-xs qa-type-btn btn-active btn-error font-bold" onclick="setQuickAddType('EXPENSE', this)">🔴 Outflow (Expense)</button>
                                                    <button type="button" class="join-item btn btn-xs qa-type-btn btn-ghost font-bold" onclick="setQuickAddType('INCOME', this)">🟢 Inflow (Income)</button>
                                                    <button type="button" class="join-item btn btn-xs qa-type-btn btn-ghost font-bold" onclick="setQuickAddType('TRANSFER', this)">🔵 Transfer / FX</button>
                                                </div>
                                                <div class="text-xs text-base-content/50 font-medium">
                                                    <kbd class="kbd kbd-xs">Tab</kbd> navigate • <kbd class="kbd kbd-xs">Enter</kbd> save • <kbd class="kbd kbd-xs">Shift+Enter</kbd> save & next
                                                </div>
                                            </div>

                                            <div class="grid grid-cols-1 sm:grid-cols-12 gap-2 items-center">
                                                <div class="sm:col-span-2">
                                                    <input type="date" id="qa_date" name="transaction_date" value="%s" required class="input input-sm input-bordered w-full font-mono text-xs focus:input-primary" />
                                                </div>
                                                <div class="sm:col-span-2">
                                                    <select id="qa_account_id" name="account_id" class="select select-sm select-bordered w-full text-xs focus:select-primary">
                                                        %s
                                                    </select>
                                                </div>
                                                <div class="sm:col-span-3">
                                                    <input type="text" id="qa_payee" name="notes" placeholder="Payee / Merchant (e.g. Żabka)" list="party_suggestions" class="input input-sm input-bordered w-full text-xs focus:input-primary" />
                                                    <select id="qa_dest_account_id" name="dest_account_id" class="select select-sm select-bordered w-full text-xs focus:select-primary hidden">
                                                        %s
                                                    </select>
                                                </div>
                                                <div class="sm:col-span-2" id="qa_bucket_wrapper">
                                                    <select id="qa_bucket_id" name="bucket_id" class="select select-sm select-bordered w-full text-xs focus:select-primary">
                                                        %s
                                                    </select>
                                                </div>
                                                <div class="sm:col-span-2">
                                                    <input type="text" id="qa_amount" name="amount" required placeholder="50.00 or 15+4.50" class="input input-sm input-bordered w-full font-mono font-bold text-xs focus:input-primary text-right" />
                                                </div>
                                                <div class="sm:col-span-1 flex justify-end">
                                                    <button type="submit" class="btn btn-primary btn-sm font-bold w-full shadow-xs">
                                                        Save
                                                    </button>
                                                </div>
                                            </div>
                                        </form>
                                    </td>
                                </tr>

                                %s
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>
        </main>
    </div>

    <!-- FLOATING ACTION BAR FOR BATCH RECONCILIATION -->
    <div id="floating-action-bar" class="fixed bottom-6 left-1/2 -translate-x-1/2 z-40 bg-base-100/95 backdrop-blur-md border-2 border-primary/40 shadow-2xl rounded-2xl px-5 py-3 items-center gap-4 hidden flex-wrap">
        <div class="flex items-center gap-2">
            <span id="fab-count" class="badge badge-primary font-bold">0</span>
            <span class="text-xs font-medium text-base-content/70">selected</span>
        </div>
        <div class="divider divider-horizontal my-0"></div>
        <div id="fab-net-display" class="font-mono font-bold text-sm text-primary">Net: 0.00 PLN</div>
        <div class="divider divider-horizontal my-0"></div>
        <div class="flex items-center gap-2">
            <form method="POST" action="/budgets/%s/transactions/resolve/batch" id="fab-batch-approve-form">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" name="operation_type" value="APPROVE" />
                <div id="fab-approve-inputs"></div>
                <button type="submit" class="btn btn-success btn-sm gap-1.5 font-bold shadow-xs">
                    <span>✓</span>
                    <span>Approve</span>
                </button>
            </form>
            <button type="button" class="btn btn-primary btn-sm gap-1.5 font-bold shadow-xs" onclick="openMergeModal()">
                <span>🔗</span>
                <span>Merge (N-to-1)</span>
            </button>
            <button id="fab-split-btn" type="button" class="btn btn-secondary btn-sm gap-1.5 font-bold shadow-xs" onclick="openSplitModal()" disabled>
                <span>✂️</span>
                <span>Split Receipt</span>
            </button>
            <form method="POST" action="/budgets/%s/transactions/raw/batch-ignore" id="fab-batch-ignore-form" onsubmit="return confirm('Ignore selected bank feeds?')">
                <input type="hidden" name="csrf_token" value="%s" />
                <div id="fab-ignore-inputs"></div>
                <button type="submit" class="btn btn-ghost btn-sm text-error font-semibold">
                    <span>🚫</span>
                    <span>Ignore</span>
                </button>
            </form>
            <button type="button" class="btn btn-ghost btn-sm btn-circle" onclick="clearAllSelections()" title="Clear selection">✕</button>
        </div>
    </div>

    %s

    <!-- MERGE MODAL -->
    <dialog id="merge_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-3">
                <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>🔗</span>
                    <span>Merge Selected Bank Feeds</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <p class="text-xs text-base-content/60 mb-4">
                Combines multiple selected bank transactions (e.g. shared restaurant bill and friend reimbursements) into a single net budget ledger transaction.
            </p>
            <form id="merge_form" method="POST" action="/budgets/%s/transactions/resolve/batch" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" name="operation_type" value="MERGE" />
                <div id="merge_raw_ids_container"></div>

                <div class="bg-base-200/70 p-3 rounded-xl border border-base-300 flex justify-between items-center">
                    <div>
                        <div class="text-xs text-base-content/60 font-medium">Net Amount to Post</div>
                        <div id="merge_count_label" class="text-sm font-bold">0 transactions</div>
                    </div>
                    <div id="merge_net_amount_label" class="font-mono font-black text-lg text-primary">0.00 PLN</div>
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend font-semibold">Account</legend>
                        <select name="account_id" class="select select-bordered w-full select-md focus:select-primary">
                            %s
                        </select>
                    </fieldset>
                    <fieldset class="fieldset">
                        <legend class="fieldset-legend font-semibold">Envelope (Bucket)</legend>
                        <select name="bucket_id" class="select select-bordered w-full select-md focus:select-primary" required>
                            %s
                        </select>
                    </fieldset>
                </div>

                <fieldset class="fieldset">
                    <legend class="fieldset-legend font-semibold">Counterparty / Merchant</legend>
                    <input type="text" name="party_name" placeholder="e.g. Restauracja Pod Baranem" list="party_suggestions" class="input input-bordered w-full input-md focus:input-primary" />
                </fieldset>

                <fieldset class="fieldset">
                    <legend class="fieldset-legend font-semibold">Ledger Description</legend>
                    <input type="text" name="notes" placeholder="e.g. Dinner with friends net share" class="input input-bordered w-full input-md focus:input-primary" />
                </fieldset>

                <div class="modal-action mt-4 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="merge_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-bold shadow-md gap-1.5">
                        <span>🔗</span>
                        <span>Confirm Merge</span>
                    </button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- SPLIT MODAL -->
    <dialog id="split_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6 max-w-xl">
            <div class="flex justify-between items-center mb-3">
                <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>✂️</span>
                    <span>Split Transaction</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <p class="text-xs text-base-content/60 mb-3">
                Splits one raw bank receipt into multiple budget transactions across different envelopes.
            </p>
            <form id="split_form" method="POST" action="/budgets/%s/transactions/resolve/batch" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" name="operation_type" value="SPLIT" />
                <input type="hidden" id="split_raw_id" name="raw_id" value="" />

                <div class="bg-base-200/70 p-3 rounded-xl border border-base-300 flex justify-between items-center text-sm">
                    <div>
                        <div id="split_payee_label" class="font-bold"></div>
                        <div class="text-xs text-base-content/60">Total Amount: <span id="split_total_label" class="font-mono font-bold"></span></div>
                    </div>
                    <div>
                        <span class="text-xs text-base-content/60">Remaining:</span>
                        <div id="split_remaining_label" class="font-mono font-bold text-success text-base">0.00 PLN</div>
                    </div>
                </div>

                <div id="split_rows_container" class="flex flex-col gap-3">
                    <!-- Dynamic split rows injected via JavaScript -->
                </div>

                <div class="flex justify-between items-center pt-2">
                    <button type="button" class="btn btn-outline btn-xs gap-1" onclick="addSplitRow()">
                        <span>➕</span> Add Split Line
                    </button>
                    <span id="split_validation_msg" class="text-xs text-error font-medium"></span>
                </div>

                <div class="modal-action mt-4 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="split_modal.close()">Cancel</button>
                    <button type="submit" id="split_submit_btn" class="btn btn-primary font-bold shadow-md gap-1.5">
                        <span>✂️</span> Confirm Split
                    </button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

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
                  hx-swap="none"
                  onsubmit="bank_sync_modal.close(); const a = document.getElementById('sync-alerts'); if(a){ a.innerHTML = '<div role=\'alert\' class=\'alert alert-info shadow-lg animate-fade-in flex items-center gap-2\'><span class=\'loading loading-spinner loading-sm text-info\'></span><div><h4 class=\'font-bold\'>Sync in Progress</h4><p class=\'text-xs\'>Connecting to bank and fetching transactions...</p></div></div>'; }"
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
                <div class="divider my-1 text-xs text-base-content/50">OR</div>

                <div class="bg-base-200/50 p-3 rounded-lg border border-base-300 flex items-center justify-between gap-3">
                    <div class="flex items-center gap-2">
                        <span class="text-xl">📄</span>
                        <div>
                            <div class="font-bold text-xs">Import from CSV File</div>
                            <div class="text-xs text-base-content/60">Upload historical bank statement exports</div>
                        </div>
                    </div>
                    <a href="/budgets/%s/transactions/import-csv" class="btn btn-outline btn-xs gap-1 font-semibold">
                        <span>Upload CSV</span>
                        <span>→</span>
                    </a>
                </div>

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

    <!-- Detailed Resolve Raw Modal (for advanced manual overrides) -->
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
                    <input id="resolve_party_name" type="text" name="party_name" placeholder="e.g. Żabka, Biedronka" list="party_suggestions" class="input input-bordered w-full input-md focus:input-primary" />
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
    <template id="bucket_options_tpl">%s</template>

    <script>
        const ACTIVE_BUDGET_ID = '%s';
        const BUCKET_OPTIONS_HTML = document.getElementById('bucket_options_tpl') ? document.getElementById('bucket_options_tpl').innerHTML : '';

        function focusQuickAdd() {
            const el = document.getElementById('qa_amount');
            if (el) {
                el.scrollIntoView({ behavior: 'smooth', block: 'center' });
                el.focus();
            }
        }

        function setQuickAddType(type, btn) {
            document.getElementById('qa_tx_type').value = type;
            document.querySelectorAll('.qa-type-btn').forEach(b => {
                b.classList.remove('btn-active', 'btn-error', 'btn-success', 'btn-info');
                b.classList.add('btn-ghost');
            });
            btn.classList.remove('btn-ghost');
            btn.classList.add('btn-active');

            const payeeInput = document.getElementById('qa_payee');
            const destAcctSelect = document.getElementById('qa_dest_account_id');
            const bucketWrapper = document.getElementById('qa_bucket_wrapper');
            const bucketSelect = document.getElementById('qa_bucket_id');

            if (type === 'TRANSFER') {
                btn.classList.add('btn-info');
                payeeInput.classList.add('hidden');
                destAcctSelect.classList.remove('hidden');
                bucketSelect.disabled = true;
                bucketSelect.value = '';
                bucketWrapper.classList.add('opacity-40');
            } else if (type === 'INCOME') {
                btn.classList.add('btn-success');
                payeeInput.classList.remove('hidden');
                payeeInput.placeholder = 'Payer / Source (e.g. Employer Inc.)';
                destAcctSelect.classList.add('hidden');
                bucketSelect.disabled = false;
                bucketWrapper.classList.remove('opacity-40');
            } else {
                // EXPENSE
                btn.classList.add('btn-error');
                payeeInput.classList.remove('hidden');
                payeeInput.placeholder = 'Payee / Merchant (e.g. Żabka)';
                destAcctSelect.classList.add('hidden');
                bucketSelect.disabled = false;
                bucketWrapper.classList.remove('opacity-40');
            }
        }

        // Global hotkeys (N or A for quick add)
        document.addEventListener('keydown', function(e) {
            const tag = document.activeElement ? document.activeElement.tagName.toLowerCase() : '';
            if (tag === 'input' || tag === 'select' || tag === 'textarea') {
                return;
            }
            if (e.key === 'n' || e.key === 'a' || e.key === 'N' || e.key === 'A') {
                e.preventDefault();
                focusQuickAdd();
            }
        });

        // Quick add form Shift+Enter handler
        const qaForm = document.getElementById('quick_add_form');
        if (qaForm) {
            qaForm.addEventListener('keydown', function(e) {
                if (e.key === 'Enter' && e.shiftKey) {
                    e.preventDefault();
                    qaForm.submit();
                }
            });
        }

        function updateFloatingActionBar() {
            const checkedBoxes = Array.from(document.querySelectorAll('.raw-select-cb:checked'));
            const bar = document.getElementById('floating-action-bar');
            const countLabel = document.getElementById('fab-count');
            const netDisplay = document.getElementById('fab-net-display');
            const splitBtn = document.getElementById('fab-split-btn');
            const ignoreInputs = document.getElementById('fab-ignore-inputs');
            const approveInputs = document.getElementById('fab-approve-inputs');

            if (!bar) return;

            if (checkedBoxes.length === 0) {
                bar.classList.add('hidden');
                return;
            }

            bar.classList.remove('hidden');
            countLabel.innerText = checkedBoxes.length;

            let netMinor = 0;
            let currency = 'PLN';
            if (ignoreInputs) ignoreInputs.innerHTML = '';
            if (approveInputs) approveInputs.innerHTML = '';

            checkedBoxes.forEach(cb => {
                const amt = parseInt(cb.dataset.amount || '0', 10);
                currency = cb.dataset.currency || currency;
                netMinor += amt;

                if (ignoreInputs) {
                    const inp = document.createElement('input');
                    inp.type = 'hidden';
                    inp.name = 'raw_ids';
                    inp.value = cb.value;
                    ignoreInputs.appendChild(inp);
                }

                if (approveInputs) {
                    const inp = document.createElement('input');
                    inp.type = 'hidden';
                    inp.name = 'raw_ids';
                    inp.value = cb.value;
                    approveInputs.appendChild(inp);
                }
            });

            const netMajor = Math.abs(netMinor) / 100.0;
            const sign = netMinor < 0 ? '-' : '+';
            netDisplay.innerText = 'Net: ' + sign + netMajor.toFixed(2) + ' ' + currency;

            if (splitBtn) {
                splitBtn.disabled = checkedBoxes.length !== 1;
            }
        }

        function clearAllSelections() {
            document.querySelectorAll('.raw-select-cb').forEach(cb => cb.checked = false);
            updateFloatingActionBar();
        }

        function toggleAllRawCbs(master) {
            document.querySelectorAll('.raw-select-cb').forEach(b => b.checked = master.checked);
            updateFloatingActionBar();
        }

        function openMergeModal() {
            const checkedBoxes = Array.from(document.querySelectorAll('.raw-select-cb:checked'));
            if (checkedBoxes.length === 0) return;

            const container = document.getElementById('merge_raw_ids_container');
            container.innerHTML = '';

            let netMinor = 0;
            let currency = 'PLN';
            checkedBoxes.forEach(cb => {
                const amt = parseInt(cb.dataset.amount || '0', 10);
                currency = cb.dataset.currency || currency;
                netMinor += amt;

                const inp = document.createElement('input');
                inp.type = 'hidden';
                inp.name = 'raw_ids';
                inp.value = cb.value;
                container.appendChild(inp);
            });

            const netMajor = Math.abs(netMinor) / 100.0;
            const sign = netMinor < 0 ? '-' : '+';
            document.getElementById('merge_count_label').innerText = checkedBoxes.length + ' transactions selected';
            document.getElementById('merge_net_amount_label').innerText = sign + netMajor.toFixed(2) + ' ' + currency;

            merge_modal.showModal();
        }

        let splitTotalAmountMinor = 0;
        let splitCurrency = 'PLN';

        function openSplitModal() {
            const checked = document.querySelector('.raw-select-cb:checked');
            if (!checked) return;

            const rawId = checked.value;
            const amountMinor = Math.abs(parseInt(checked.dataset.amount || '0', 10));
            splitTotalAmountMinor = amountMinor;
            splitCurrency = checked.dataset.currency || 'PLN';
            const payee = checked.dataset.payee || '';

            document.getElementById('split_raw_id').value = rawId;
            document.getElementById('split_payee_label').innerText = payee;
            document.getElementById('split_total_label').innerText = (amountMinor / 100.0).toFixed(2) + ' ' + splitCurrency;

            const container = document.getElementById('split_rows_container');
            container.innerHTML = '';

            // Add initial 2 split lines
            addSplitRow((amountMinor / 200.0).toFixed(2));
            addSplitRow((amountMinor / 200.0).toFixed(2));
            recalcSplit();

            split_modal.showModal();
        }

        function addSplitRow(defaultAmount) {
            const container = document.getElementById('split_rows_container');
            const row = document.createElement('div');
            row.className = 'grid grid-cols-12 gap-2 items-center split-line-row';
            row.innerHTML = 
                '<div class="col-span-5">' +
                    '<select name="split_bucket_id" class="select select-sm select-bordered w-full text-xs" required>' +
                        BUCKET_OPTIONS_HTML +
                    '</select>' +
                '</div>' +
                '<div class="col-span-4">' +
                    '<input type="text" name="split_note" placeholder="Item note" class="input input-sm input-bordered w-full text-xs" />' +
                '</div>' +
                '<div class="col-span-2">' +
                    '<input type="number" step="0.01" name="split_amount" value="' + (defaultAmount || '') + '" required placeholder="0.00" class="input input-sm input-bordered w-full font-mono font-bold text-xs split-amt-input" oninput="recalcSplit()" />' +
                '</div>' +
                '<div class="col-span-1 text-right">' +
                    '<button type="button" class="btn btn-ghost btn-xs btn-circle text-error" onclick="removeSplitRow(this)">✕</button>' +
                '</div>';
            container.appendChild(row);
            recalcSplit();
        }

        function removeSplitRow(btn) {
            const row = btn.closest('.split-line-row');
            if (row && document.querySelectorAll('.split-line-row').length > 1) {
                row.remove();
                recalcSplit();
            }
        }

        function recalcSplit() {
            const inputs = document.querySelectorAll('.split-amt-input');
            let sumMinor = 0;
            inputs.forEach(inp => {
                const f = parseFloat(inp.value || '0');
                if (!isNaN(f)) {
                    sumMinor += Math.round(f * 100);
                }
            });

            const remainingMinor = splitTotalAmountMinor - sumMinor;
            const remainingLabel = document.getElementById('split_remaining_label');
            const submitBtn = document.getElementById('split_submit_btn');
            const valMsg = document.getElementById('split_validation_msg');

            remainingLabel.innerText = (remainingMinor / 100.0).toFixed(2) + ' ' + splitCurrency;

            if (remainingMinor === 0) {
                remainingLabel.className = 'font-mono font-bold text-success text-base';
                valMsg.innerText = '';
                submitBtn.disabled = false;
            } else {
                remainingLabel.className = 'font-mono font-bold text-error text-base';
                valMsg.innerText = remainingMinor > 0 ? 'Remaining amount must be 0.00' : 'Split amounts exceed total receipt!';
                submitBtn.disabled = true;
            }
        }

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

        function toggleAllAccounts(btn) {
            const boxes = document.querySelectorAll('input[name="account_ids"]');
            const allChecked = Array.from(boxes).every(b => b.checked);
            boxes.forEach(b => b.checked = !allChecked);
        }

        // Initialize SSE for bank sync notifications
        if (window.EventSource) {
            const sseUrl = '/budgets/' + ACTIVE_BUDGET_ID + '/transactions/sync/events';
            console.log('[SSE] Initializing EventSource connection to:', sseUrl);
            const sse = new EventSource(sseUrl);
            sse.onopen = () => console.log('[SSE] Successfully connected to bank sync events');
            sse.onerror = (err) => console.warn('[SSE] EventSource error or reconnection:', err);

            sse.addEventListener('sync-progress', (e) => {
                console.log('[SSE] sync-progress received:', e.data);
                const data = JSON.parse(e.data || '{}');
                const alerts = document.getElementById('sync-alerts');
                if (alerts) {
                    alerts.innerHTML = '<div role="alert" class="alert alert-info shadow-lg animate-fade-in flex items-center gap-2">' +
                        '<span class="loading loading-spinner loading-sm text-info"></span>' +
                        '<div>' +
                            '<h4 class="font-bold">Sync in Progress (' + data.index + '/' + data.total + ')</h4>' +
                            '<p class="text-xs">Synchronizing bank accounts in background...</p>' +
                        '</div>' +
                    '</div>';
                }
            });

            sse.addEventListener('sync-complete', (e) => {
                console.log('[SSE] sync-complete received:', e.data);
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
                        '<div class="flex items-center gap-2">' +
                            '<button class="btn btn-primary btn-xs font-bold" onclick="window.location.reload()">Refresh Page</button>' +
                            '<button class="btn btn-ghost btn-xs btn-circle" onclick="document.getElementById(\'sync-alerts\').innerHTML=\'\'">✕</button>' +
                        '</div>' +
                    '</div>';
                }
            });

            sse.addEventListener('sync-error', (e) => {
                console.log('[SSE] sync-error received:', e.data);
                const data = JSON.parse(e.data || '{}');
                const alerts = document.getElementById('sync-alerts');
                if (alerts) {
                    alerts.innerHTML = '<div role="alert" class="alert alert-error shadow-lg animate-fade-in flex justify-between items-center">' +
                        '<div class="flex items-center gap-2">' +
                            '<span>❌</span>' +
                            '<div>' +
                                '<h4 class="font-bold">Bank Sync Failed</h4>' +
                                '<p class="text-xs">' + (data.error || 'An error occurred during synchronization.') + '</p>' +
                            '</div>' +
                        '</div>' +
                        '<button class="btn btn-ghost btn-xs btn-circle text-error-content" onclick="document.getElementById(\'sync-alerts\').innerHTML=\'\'">✕</button>' +
                    '</div>';
                }
            });
        }
    </script>
</body>
</html>`,
		partySuggestions.String(),
		budgetName, csrfToken,
		RenderSidebarNavigation(budgetID, "transactions"),
		acctPills.String(),
		pendingRawCount, budgetID, csrfToken, matchedRulesCount,
		rawTxRows.String(),
		budgetID, csrfToken, todayDateStr, acctOptions.String(), qaDestAcctOptions.String(), qaBucketOptions.String(),
		txRows.String(),
		budgetID, csrfToken,
		budgetID, csrfToken,
		RenderBottomDock(budgetID, "transactions"),
		budgetID, csrfToken, acctOptions.String(), bucketOptions.String(),
		budgetID, csrfToken,
		budgetID, budgetID, csrfToken, acctCheckboxes.String(), budgetID,
		csrfToken, editAcctOptions.String(), editBucketOptions.String(),
		csrfToken, bucketOptions.String(),
		bucketOptions.String(), budgetID)

	_ = role
	return c.HTML(http.StatusOK, html)
}

// SyncBankEvents streams real-time sync progress and completion events via SSE.
func (h *TransactionsHandler) SyncBankEvents(c echo.Context) error {
	budgetID := c.Param("budgetId")
	corrID, _ := c.Get("correlationID").(string)

	reqCtx := c.Request().Context()
	if corrID != "" {
		reqCtx, _ = logger.WithCorrelationID(reqCtx, corrID)
	}
	logCtx := logger.WithComponent(reqCtx, "biz:transaction:bank_sync:sse")
	logger.Info(logCtx, "Client connected to bank sync SSE stream", "budget_id", budgetID)

	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

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
		logger.Info(logCtx, "Client disconnected from bank sync SSE stream", "budget_id", budgetID)
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

func (h *TransactionsHandler) broadcastSyncEvent(ctx context.Context, budgetID, corrID, event, data string) {
	logCtx := ctx
	if corrID != "" {
		logCtx, _ = logger.WithCorrelationID(logCtx, corrID)
	}
	logCtx = logger.WithComponent(logCtx, "biz:transaction:bank_sync:event")

	h.syncMu.Lock()
	defer h.syncMu.Unlock()

	listeners := h.syncListeners[budgetID]
	logger.Info(logCtx, "Broadcasting sync SSE event to clients",
		"budget_id", budgetID,
		"event", event,
		"data", data,
		"listeners_count", len(listeners),
	)

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

	// Check if this account is explicitly linked to a mock or sandbox integration
	var integrationID string
	_ = tenantDB.QueryRowContext(ctx, `
		SELECT COALESCE(integration_id, '') FROM account_integration_links
		WHERE account_id = ? LIMIT 1`, userAcctID).Scan(&integrationID)

	isMockAccount := false
	if integrationID != "" {
		if strings.HasPrefix(integrationID, "mock_") {
			isMockAccount = true
		} else {
			var provider, institutionID string
			_ = h.mainDB.QueryRowContext(ctx, `
				SELECT provider, institution_id FROM user_integrations
				WHERE id = ?`, integrationID).Scan(&provider, &institutionID)
			if provider == "MOCK" || strings.HasPrefix(institutionID, "MOCK_") || strings.Contains(strings.ToUpper(institutionID), "SANDBOX") {
				isMockAccount = true
			}
		}
	}

	// Rate limit check: max 4 imports per account in the last 24 hours (86400 seconds) - exempt for mock/sandbox accounts
	if !isMockAccount {
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
			h.broadcastSyncEvent(opCtx, budgetID, corrID, "sync-progress", fmt.Sprintf(`{"account_id":"%s","index":%d,"total":%d,"correlation_id":"%s"}`, acctID, i+1, len(accountIDs), corrID))
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
			h.broadcastSyncEvent(opCtx, budgetID, corrID, "sync-error", fmt.Sprintf(`{"error":"%s","correlation_id":"%s"}`, firstErr.Error(), corrID))
		} else {
			h.broadcastSyncEvent(opCtx, budgetID, corrID, "sync-complete", fmt.Sprintf(`{"imported_count":%d,"accounts_count":%d,"correlation_id":"%s"}`, totalImported, len(accountIDs), corrID))
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
func parseFlexibleAmount(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty amount")
	}
	// Support simple addition like 15+4.50
	if strings.Contains(raw, "+") {
		parts := strings.Split(raw, "+")
		var total float64
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			var f float64
			if _, err := fmt.Sscanf(p, "%f", &f); err == nil {
				total += f
			}
		}
		return int64(total * 100), nil
	}
	var f float64
	if _, err := fmt.Sscanf(raw, "%f", &f); err != nil {
		return 0, err
	}
	return int64(f * 100), nil
}

func findOrCreateExternalAccount(ctx context.Context, tenantDB *sql.DB, partyName, currency, userID string, now int64) (string, error) {
	cleanName := strings.TrimSpace(partyName)
	if cleanName == "" {
		cleanName = "General Merchant"
	}
	var counterpartyAcctID string
	err := tenantDB.QueryRowContext(ctx, `
		SELECT a.id FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.name = ? AND p.is_user_owned = 0
		LIMIT 1`, cleanName).Scan(&counterpartyAcctID)
	if err == nil {
		return counterpartyAcctID, nil
	}

	pID := domain.NewUUIDv7()
	counterpartyAcctID = domain.NewUUIDv7()
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`, pID, cleanName, userID, now)
	if err != nil {
		return "", err
	}
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'EXTERNAL', ?, 0, ?, ?)`, counterpartyAcctID, pID, cleanName, currency, userID, now)
	if err != nil {
		return "", err
	}
	return counterpartyAcctID, nil
}

// AddManualTransaction handles direct cash transaction entry (Expense, Income, or Transfer).
func (h *TransactionsHandler) AddManualTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:manual")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	amountMinor, err := parseFlexibleAmount(c.FormValue("amount"))
	if err != nil || amountMinor <= 0 {
		logger.Warn(ctx, "Manual transaction rejected: invalid amount", "raw_amount", c.FormValue("amount"))
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid transaction amount")
	}

	// Parse custom transaction date if provided (YYYY-MM-DD)
	txDate := now
	dateForm := c.FormValue("transaction_date")
	if dateForm != "" {
		if parsedDate, err := time.Parse("2006-01-02", dateForm); err == nil {
			txDate = parsedDate.UTC().UnixMicro()
		}
	}

	txType := strings.ToUpper(strings.TrimSpace(c.FormValue("tx_type")))
	if txType == "" {
		txType = "EXPENSE"
	}

	if h.appService != nil {
		tType := service.TxExpense
		switch txType {
		case "INCOME":
			tType = service.TxIncome
		case "TRANSFER":
			tType = service.TxTransfer
		}

		var bIDPtr *string
		bVal := c.FormValue("bucket_id")
		if bVal != "" {
			bIDPtr = &bVal
		}

		var dMinor int64
		if dAmt := c.FormValue("dest_amount"); dAmt != "" {
			if parsedDAmt, err := parseFlexibleAmount(dAmt); err == nil && parsedDAmt > 0 {
				dMinor = parsedDAmt
			}
		}

		opID, err := h.appService.AddTransaction(c.Request().Context(), service.AddTransactionInput{
			BudgetID:          budgetID,
			UserID:            userID,
			CorrelationID:     corrID,
			TxType:            tType,
			SourceAccountID:   c.FormValue("account_id"),
			DestinationAcctID: c.FormValue("dest_account_id"),
			AmountMinor:       amountMinor,
			DestAmountMinor:   dMinor,
			BucketID:          bIDPtr,
			Notes:             strings.TrimSpace(c.FormValue("notes")),
			TransactionDate:   time.UnixMicro(txDate),
		})
		if err != nil {
			logger.Error(ctx, "Failed adding transaction via service", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		logger.Info(ctx, "Manual transaction recorded successfully", "budget_id", budgetID, "operation_id", opID)
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
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

	var userAcctCurr string
	_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT primary_currency FROM accounts_current WHERE id = ?`, userAcctID).Scan(&userAcctCurr)
	if userAcctCurr == "" {
		userAcctCurr = "PLN"
	}

	notes := strings.TrimSpace(c.FormValue("notes"))
	var bucketIDPtr *string
	bucketIDVal := c.FormValue("bucket_id")
	if bucketIDVal != "" {
		bucketIDPtr = &bucketIDVal
	}

	var srcAcct, dstAcct string
	var srcCurr, dstCurr string
	var destAmountMinor int64 = amountMinor

	switch txType {
	case "INCOME":
		dstAcct = userAcctID
		dstCurr = userAcctCurr
		srcCurr = userAcctCurr
		payeeName := notes
		if payeeName == "" {
			payeeName = "External Payer / Income"
		}
		extAcct, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, payeeName, srcCurr, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external payer account")
		}
		srcAcct = extAcct

	case "TRANSFER":
		srcAcct = userAcctID
		srcCurr = userAcctCurr
		dstAcct = c.FormValue("dest_account_id")
		if dstAcct == "" || dstAcct == srcAcct {
			return echo.NewHTTPError(http.StatusBadRequest, "Transfer requires distinct source and destination accounts")
		}
		_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT primary_currency FROM accounts_current WHERE id = ?`, dstAcct).Scan(&dstCurr)
		if dstCurr == "" {
			dstCurr = srcCurr
		}
		if dAmt := c.FormValue("dest_amount"); dAmt != "" {
			if parsedDAmt, err := parseFlexibleAmount(dAmt); err == nil && parsedDAmt > 0 {
				destAmountMinor = parsedDAmt
			}
		}
		bucketIDPtr = nil // Internal transfers do not use buckets

	default: // EXPENSE
		srcAcct = userAcctID
		srcCurr = userAcctCurr
		dstCurr = userAcctCurr
		payeeName := notes
		if payeeName == "" {
			payeeName = "General Merchant"
		}
		extAcct, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, payeeName, dstCurr, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external merchant account")
		}
		dstAcct = extAcct
	}

	opID, err := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
		OperationType: transaction.OpDirectMove,
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      srcAcct,
				DestinationAccountID: dstAcct,
				SourceAmountMinor:    amountMinor,
				SourceCurrency:       srcCurr,
				DestAmountMinor:      destAmountMinor,
				DestCurrency:         dstCurr,
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
		"tx_type", txType,
		"source_account_id", srcAcct,
		"dest_account_id", dstAcct,
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

// BatchResolveRawTransactions handles MERGE and SPLIT operations from the Floating Action Bar.
func (h *TransactionsHandler) BatchResolveRawTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:batch_resolve")
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	opType := c.FormValue("operation_type")
	formParams, _ := c.FormParams()
	rawIDs := formParams["raw_ids"]
	if len(rawIDs) == 0 {
		if single := c.FormValue("raw_id"); single != "" {
			rawIDs = append(rawIDs, single)
		}
	}

	if len(rawIDs) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "No raw transactions selected")
	}

	if opType == transaction.OpMerge {
		// N-to-1 Merge
		var totalNetDelta int64
		var currency string
		var userAcctID string
		var latestDate int64

		for _, rID := range rawIDs {
			var amt int64
			var curr, acctID string
			var bDate sql.NullInt64
			err := tenantDB.QueryRowContext(c.Request().Context(), `
				SELECT amount_minor, currency, account_id, booking_date
				FROM raw_transactions_current WHERE id = ? AND status = 'PENDING'`, rID).
				Scan(&amt, &curr, &acctID, &bDate)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Pending raw transaction %s not found", rID))
			}
			totalNetDelta += amt
			currency = curr
			userAcctID = acctID
			if bDate.Valid && bDate.Int64 > latestDate {
				latestDate = bDate.Int64
			}
		}

		if latestDate == 0 {
			latestDate = now
		}

		if fAcct := c.FormValue("account_id"); fAcct != "" {
			userAcctID = fAcct
		}

		partyName := strings.TrimSpace(c.FormValue("party_name"))
		if partyName == "" {
			partyName = "Merged Counterparty"
		}
		extAcctID, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, partyName, currency, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external party")
		}

		var bucketIDPtr *string
		if bID := c.FormValue("bucket_id"); bID != "" {
			bucketIDPtr = &bID
		}

		notes := strings.TrimSpace(c.FormValue("notes"))
		if notes == "" {
			notes = fmt.Sprintf("Merged %d bank transactions", len(rawIDs))
		}

		var srcAcct, dstAcct string
		var absAmountMinor int64
		if totalNetDelta < 0 {
			srcAcct = userAcctID
			dstAcct = extAcctID
			absAmountMinor = -totalNetDelta
		} else {
			srcAcct = extAcctID
			dstAcct = userAcctID
			absAmountMinor = totalNetDelta
		}

		if absAmountMinor == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "Net amount of merged transactions is 0. Cannot create 0-amount budget transaction.")
		}

		_, err = transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
			OperationType:     transaction.OpMerge,
			RawTransactionIDs: rawIDs,
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
					TransactionDate:      latestDate,
				},
			},
			AgentID:       userID,
			CorrelationID: corrID,
		})
		if err != nil {
			logger.Error(ctx, "Failed executing merge resolution", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to merge transactions: %v", err))
		}

	} else if opType == transaction.OpSplit {
		// 1-to-N Split
		if len(rawIDs) != 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "Split operation requires exactly 1 raw transaction")
		}
		rawID := rawIDs[0]

		var userAcctID, currency, rawPayee string
		var bookingDate sql.NullInt64
		var amountMinor int64
		err := tenantDB.QueryRowContext(c.Request().Context(), `
			SELECT account_id, booking_date, amount_minor, currency, raw_payee
			FROM raw_transactions_current WHERE id = ? AND status = 'PENDING'`, rawID).
			Scan(&userAcctID, &bookingDate, &amountMinor, &currency, &rawPayee)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Pending raw transaction not found")
		}

		txDate := now
		if bookingDate.Valid {
			txDate = bookingDate.Int64
		}

		partyName := strings.TrimSpace(c.FormValue("party_name"))
		if partyName == "" {
			partyName = rawPayee
		}
		extAcctID, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, partyName, currency, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external party")
		}

		splitBucketIDs := formParams["split_bucket_id"]
		splitAmounts := formParams["split_amount"]
		splitNotes := formParams["split_note"]

		if len(splitBucketIDs) == 0 || len(splitAmounts) != len(splitBucketIDs) {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid split configuration")
		}

		var btxInputs []transaction.BudgetTxInput
		var sumSplitMinor int64

		for i, bID := range splitBucketIDs {
			amtMinor, aErr := parseFlexibleAmount(splitAmounts[i])
			if aErr != nil || amtMinor <= 0 {
				return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid split amount for entry %d", i+1))
			}
			sumSplitMinor += amtMinor

			var bIDPtr *string
			if bID != "" {
				bIDPtr = &bID
			}
			note := rawPayee
			if i < len(splitNotes) && strings.TrimSpace(splitNotes[i]) != "" {
				note = strings.TrimSpace(splitNotes[i])
			}

			var srcAcct, dstAcct string
			if amountMinor < 0 {
				srcAcct = userAcctID
				dstAcct = extAcctID
			} else {
				srcAcct = extAcctID
				dstAcct = userAcctID
			}

			btxInputs = append(btxInputs, transaction.BudgetTxInput{
				SourceAccountID:      srcAcct,
				DestinationAccountID: dstAcct,
				SourceAmountMinor:    amtMinor,
				SourceCurrency:       currency,
				DestAmountMinor:      amtMinor,
				DestCurrency:         currency,
				BucketID:             bIDPtr,
				Notes:                &note,
				TransactionDate:      txDate,
			})
		}

		expectedAbsMinor := amountMinor
		if expectedAbsMinor < 0 {
			expectedAbsMinor = -expectedAbsMinor
		}
		if sumSplitMinor != expectedAbsMinor {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Split amounts sum (%d minor) does not match original transaction amount (%d minor)", sumSplitMinor, expectedAbsMinor))
		}

		_, err = transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
			OperationType:      transaction.OpSplit,
			RawTransactionIDs:  []string{rawID},
			BudgetTransactions: btxInputs,
			AgentID:            userID,
			CorrelationID:      corrID,
		})
		if err != nil {
			logger.Error(ctx, "Failed executing split resolution", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to split transaction: %v", err))
		}
	} else if opType == "APPROVE" {
		// Batch 1-to-1 Direct Move (Approve) for all selected raw transactions
		rulesEngine, _ := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)
		approvedCount := 0

		for _, rID := range rawIDs {
			var rawVersion int
			var accountID, dedupHash, currency, rawPayee string
			var extTxID, counterpartyIBAN, reference sql.NullString
			var bookingDate, valueDate sql.NullInt64
			var amountMinor int64

			err := tenantDB.QueryRowContext(c.Request().Context(), `
				SELECT version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
				       amount_minor, currency, raw_payee, counterparty_iban, reference
				FROM raw_transactions_current WHERE id = ? AND status = 'PENDING'`, rID).Scan(
				&rawVersion, &accountID, &extTxID, &dedupHash, &bookingDate, &valueDate,
				&amountMinor, &currency, &rawPayee, &counterpartyIBAN, &reference,
			)
			if err != nil {
				continue
			}

			txDate := now
			if bookingDate.Valid {
				txDate = bookingDate.Int64
			}

			// Clean payee and lookup auto-match rule
			cleanPayee := strings.TrimSpace(rawPayee)
			if cleanPayee == "" {
				cleanPayee = "External Merchant"
			}
			refStr := ""
			if reference.Valid {
				refStr = reference.String
			}
			ibanStr := ""
			if counterpartyIBAN.Valid {
				ibanStr = counterpartyIBAN.String
			}

			var targetBucketID *string
			var targetCounterpartyAcctID string

			if rulesEngine != nil {
				match := rulesEngine.Match(cleanPayee, ibanStr, refStr)
				if match != nil {
					targetBucketID = match.DefaultBucketID
					targetCounterpartyAcctID = match.TargetCounterpartyAcctID
				}
			}

			if targetCounterpartyAcctID == "" {
				extAcct, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, cleanPayee, currency, userID, now)
				if err != nil {
					continue
				}
				targetCounterpartyAcctID = extAcct
			}

			var srcAcct, dstAcct string
			var absAmountMinor int64
			if amountMinor < 0 {
				srcAcct = accountID
				dstAcct = targetCounterpartyAcctID
				absAmountMinor = -amountMinor
			} else {
				srcAcct = targetCounterpartyAcctID
				dstAcct = accountID
				absAmountMinor = amountMinor
			}

			note := cleanPayee
			_, sErr := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
				OperationType:     transaction.OpDirectMove,
				RawTransactionIDs: []string{rID},
				BudgetTransactions: []transaction.BudgetTxInput{
					{
						SourceAccountID:      srcAcct,
						DestinationAccountID: dstAcct,
						SourceAmountMinor:    absAmountMinor,
						SourceCurrency:       currency,
						DestAmountMinor:      absAmountMinor,
						DestCurrency:         currency,
						BucketID:             targetBucketID,
						Notes:                &note,
						TransactionDate:      txDate,
					},
				},
				AgentID:       userID,
				CorrelationID: corrID,
			})
			if sErr == nil {
				approvedCount++
			}
		}

		logger.Info(ctx, "Batch approved raw transactions", "count", approvedCount, "total_requested", len(rawIDs))
	} else {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unsupported operation type: %s", opType))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// BatchIgnoreRawTransactions marks multiple raw transactions as IGNORED.
func (h *TransactionsHandler) BatchIgnoreRawTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:batch_ignore")
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	formParams, _ := c.FormParams()
	rawIDs := formParams["raw_ids"]
	if len(rawIDs) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
	}

	for _, rawID := range rawIDs {
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
		if err == nil {
			newVersion := rawVersion + 1
			tx, tErr := tenantDB.BeginTx(c.Request().Context(), nil)
			if tErr == nil {
				_, _ = tx.ExecContext(c.Request().Context(), `
					INSERT INTO raw_transactions (
						id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
						amount_minor, currency, raw_payee, counterparty_iban, reference, status, action,
						agent_id, correlation_id, created_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'IGNORED', 'UPDATE', ?, ?, ?)`,
					rawID, newVersion, accountID, extTxID, dedupHash, bookingDate, valueDate,
					amountMinor, currency, rawPayee, counterpartyIBAN, reference,
					userID, corrID, now,
				)
				_, _ = tx.ExecContext(c.Request().Context(), `
					UPDATE raw_transactions_current
					SET version = ?, status = 'IGNORED', updated_at = ?
					WHERE id = ?`, newVersion, now, rawID)
				_ = tx.Commit()
			}
		}
	}

	logger.Info(ctx, "Batch ignored raw transactions", "count", len(rawIDs))
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

