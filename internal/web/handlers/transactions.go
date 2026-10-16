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
	for rows.Next() {
		var id, curr, notes, bucketName string
		var amountMinor, txDate int64
		rows.Scan(&id, &amountMinor, &curr, &txDate, &notes, &bucketName)

		dateStr := time.UnixMicro(txDate).Format("2006-01-02")
		txRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>%s</td>
				<td><span class="badge badge-ghost badge-sm">%s</span></td>
				<td>%s</td>
				<td class="font-mono text-right">%.2f %s</td>
			</tr>`, dateStr, bucketName, notes, float64(amountMinor)/100.0, curr))
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - Transactions</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5.0.0/dist/full.min.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
</head>
<body class="bg-base-200 min-h-screen flex flex-col">
    <header class="navbar bg-base-100 border-b border-base-300 px-6">
        <div class="flex-1 gap-4 items-center">
            <a href="/budgets" class="text-xl font-bold tracking-tight text-primary">Balance</a>
            <div class="badge badge-outline">%s</div>
        </div>
        <div class="flex-none gap-4">
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-2">
            <ul class="menu menu-md w-full p-0">
                <li><a href="/budgets/%s/budget">1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/transactions" class="active font-bold">2. Transactions</a></li>
                <li><a href="/budgets/%s/reports">3. Reports</a></li>
                <li><a href="/budgets/%s/parties">4. External Parties</a></li>
                <li><a href="/budgets/%s/members">5. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 overflow-y-auto">
            <!-- Primary Toolbar: Sync with Bank & Add Manual Transaction -->
            <div class="flex justify-between items-center mb-6">
                <h1 class="text-2xl font-bold">Transactions Ledger</h1>
                <div class="flex gap-3">
                    <form method="POST" action="/budgets/%s/transactions/sync">
                        <input type="hidden" name="csrf_token" value="%s" />
                        <button type="submit" class="btn btn-secondary btn-sm">🔄 Sync with Bank</button>
                    </form>
                    <button class="btn btn-primary btn-sm" onclick="add_tx_modal.showModal()">➕ Add Manual Transaction</button>
                </div>
            </div>

            <!-- Ledger Table -->
            <div class="overflow-x-auto bg-base-100 rounded-box border border-base-300 shadow">
                <table class="table table-zebra">
                    <thead>
                        <tr>
                            <th>Date</th>
                            <th>Bucket</th>
                            <th>Notes</th>
                            <th class="text-right">Amount</th>
                        </tr>
                    </thead>
                    <tbody>
                        %s
                    </tbody>
                </table>
            </div>
        </main>
    </div>

    <!-- Mobile Bottom Navigation Dock -->
    <div class="dock md:hidden">
        <a href="/budgets/%s/budget">Budget</a>
        <a href="/budgets/%s/transactions" class="active">Txns</a>
        <a href="/budgets/%s/reports">Reports</a>
        <a href="/budgets/%s/parties">Parties</a>
        <a href="/budgets/%s/members">Members</a>
    </div>

    <!-- Add Manual Transaction Modal -->
    <dialog id="add_tx_modal" class="modal">
        <div class="modal-box">
            <h3 class="font-bold text-lg mb-4">Add Manual Transaction</h3>
            <form method="POST" action="/budgets/%s/transactions" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div>
                    <label class="label"><span class="label-text">Amount (e.g. 50.00)</span></label>
                    <input type="number" step="0.01" name="amount" required placeholder="50.00" class="input input-bordered w-full" />
                </div>
                <div>
                    <label class="label"><span class="label-text">Notes</span></label>
                    <input type="text" name="notes" placeholder="Cash expense" class="input input-bordered w-full" />
                </div>
                <div class="modal-action">
                    <button type="button" class="btn" onclick="add_tx_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary">Save Transaction</button>
                </div>
            </form>
        </div>
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
		// Create default user account if none exists
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

	importedCount := 0
	for _, ext := range extTxs {
		dedup := transaction.ComputeCanonicalDedupHash(userAcctID, ext.ExternalTxID, ext.BookingDateMicro, ext.AmountMinor, ext.Currency, ext.Payee, ext.Reference)
		rawID := domain.NewUUIDv7()

		_, err := tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, reference, status, updated_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?)
			ON CONFLICT(account_id, dedup_hash) DO NOTHING`,
			rawID, userAcctID, ext.ExternalTxID, dedup, ext.BookingDateMicro,
			ext.AmountMinor, ext.Currency, ext.Payee, ext.Reference, now,
		)
		if err == nil {
			importedCount++
		}
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

	// Ensure at least one user account and one external account exist
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
