package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/mock"
	"github.com/balance/balance/internal/transaction"
	"github.com/labstack/echo/v4"
)

type TransactionsHandler struct {
	mainDB      *sql.DB
	tenantMgr   *db.TenantDBManager
	mockAdapter *mock.MockAdapter
}

func NewTransactionsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *TransactionsHandler {
	return &TransactionsHandler{
		mainDB:      mainDB,
		tenantMgr:   tenantMgr,
		mockAdapter: mock.NewMockAdapter(),
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

	// Fetch recent budget transactions
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT bt.id, bt.source_amount_minor, bt.source_currency, bt.transaction_date,
		       COALESCE(bt.notes, ''), COALESCE(bp.name, 'Unassigned')
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
	for rows.Next() {
		hasTxs = true
		var id, curr, notes, bucketName string
		var amountMinor, txDate int64
		rows.Scan(&id, &amountMinor, &curr, &txDate, &notes, &bucketName)

		dateStr := time.UnixMicro(txDate).Format("2006-01-02")
		txRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td class="font-mono text-xs">%s</td>
				<td><span class="badge badge-soft badge-primary font-semibold text-xs">%s</span></td>
				<td class="text-sm font-medium">%s</td>
				<td class="font-mono text-right font-bold text-sm">
					-%.2f <span class="text-xs font-normal text-base-content/60">%s</span>
				</td>
			</tr>`, dateStr, bucketName, notes, float64(amountMinor)/100.0, curr))
	}

	if !hasTxs {
		txRows.WriteString(`
			<tr>
				<td colspan="4" class="text-center py-8 text-base-content/50">
					No transactions recorded yet. Use 🔄 Sync with Bank or ➕ Add Manual Transaction.
				</td>
			</tr>
		`)
	}

	csrfToken, _ := c.Get("csrfToken").(string)

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
            <ul class="menu menu-md w-full p-0 gap-1">
                <li><a href="/budgets/%s/budget">📂 1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/transactions" class="active font-bold">💳 2. Transactions</a></li>
                <li><a href="/budgets/%s/reports">📊 3. Reports</a></li>
                <li><a href="/budgets/%s/parties">🏢 4. External Parties</a></li>
                <li><a href="/budgets/%s/members">👥 5. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 md:p-8 overflow-y-auto">
            <!-- Primary Toolbar -->
            <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
                <div>
                    <h1 class="text-2xl font-black tracking-tight">Transactions Ledger</h1>
                    <p class="text-xs text-base-content/60 mt-1">Review activity, sync bank feeds, or record cash transactions.</p>
                </div>
                <div class="flex items-center gap-3">
                    <form method="POST" action="/budgets/%s/transactions/sync">
                        <input type="hidden" name="csrf_token" value="%s" />
                        <button type="submit" class="btn btn-secondary btn-sm shadow font-semibold gap-1.5">
                            <span>🔄</span>
                            <span>Sync with Bank</span>
                        </button>
                    </form>
                    <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="add_tx_modal.showModal()">
                        <span>➕</span>
                        <span>Add Transaction</span>
                    </button>
                </div>
            </div>

            <!-- Ledger Table Card -->
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
                                </tr>
                            </thead>
                            <tbody>
                                %s
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>
        </main>
    </div>

    <!-- Mobile Bottom Navigation Dock -->
    <div class="dock md:hidden bg-base-100 border-t border-base-300">
        <a href="/budgets/%s/budget">
            <span class="text-xl">📂</span>
            <span class="dock-label">Budget</span>
        </a>
        <a href="/budgets/%s/transactions" class="dock-active">
            <span class="text-xl">💳</span>
            <span class="dock-label">Txns</span>
        </a>
        <a href="/budgets/%s/reports">
            <span class="text-xl">📊</span>
            <span class="dock-label">Reports</span>
        </a>
        <a href="/budgets/%s/parties">
            <span class="text-xl">🏢</span>
            <span class="dock-label">Parties</span>
        </a>
        <a href="/budgets/%s/members">
            <span class="text-xl">👥</span>
            <span class="dock-label">Members</span>
        </a>
    </div>

    <!-- Add Manual Transaction Modal -->
    <dialog id="add_tx_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Add Manual Transaction</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/transactions" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Amount (in major units e.g. 45.50)</span></label>
                    <input type="number" step="0.01" name="amount" required placeholder="50.00" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Description / Payee</span></label>
                    <input type="text" name="notes" placeholder="e.g. Lunch with team or Groceries" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="add_tx_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Record Outflow</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
</body>
</html>`, budgetName, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID,
		budgetID, csrfToken, txRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID,
		budgetID, csrfToken)

	_ = role
	return c.HTML(http.StatusOK, html)
}

// SyncWithBank triggers bank integration synchronization, imports raw transactions and runs auto-matching.
func (h *TransactionsHandler) SyncWithBank(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	now := time.Now().UTC().UnixMicro()

	// Fetch first user-owned checking account
	var userAcctID string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT a.id FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.is_user_owned = 1
		LIMIT 1`).Scan(&userAcctID)
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

	// Fetch transactions from mock bank
	extTxs, err := h.mockAdapter.FetchTransactions(c.Request().Context(), userAcctID, 0)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Bank sync error")
	}

	for _, ext := range extTxs {
		dedup := transaction.ComputeCanonicalDedupHash(userAcctID, ext.ExternalTxID, ext.BookingDateMicro, ext.AmountMinor, ext.Currency, ext.Payee, ext.Reference)
		rawID := domain.NewUUIDv7()

		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, reference, status, updated_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?)
			ON CONFLICT(account_id, dedup_hash) DO NOTHING`,
			rawID, userAcctID, ext.ExternalTxID, dedup, ext.BookingDateMicro,
			ext.AmountMinor, ext.Currency, ext.Payee, ext.Reference, now,
		)
	}

	_ = corrID
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// AddManualTransaction handles direct cash transaction entry.
func (h *TransactionsHandler) AddManualTransaction(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	var amountFloat float64
	fmt.Sscanf(c.FormValue("amount"), "%f", &amountFloat)
	amountMinor := int64(amountFloat * 100)
	if amountMinor <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid transaction amount")
	}

	now := time.Now().UTC().UnixMicro()

	var userAcctID, extAcctID string
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

	err = tenantDB.QueryRowContext(c.Request().Context(), `
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
	_, err = transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
		OperationType: transaction.OpDirectMove,
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      userAcctID,
				DestinationAccountID: extAcctID,
				SourceAmountMinor:    amountMinor,
				SourceCurrency:       "PLN",
				DestAmountMinor:      amountMinor,
				DestCurrency:         "PLN",
				Notes:                &notes,
				TransactionDate:      now,
			},
		},
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to save transaction: %v", err))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}
