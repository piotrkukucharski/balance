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
	"github.com/balance/balance/internal/logger"
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

	// Fetch user checking/cash accounts for dropdown
	acctRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.id, a.name, a.primary_currency FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.is_user_owned = 1 AND a.is_closed = 0
		ORDER BY a.name ASC`)
	var acctOptions strings.Builder
	var editAcctOptions strings.Builder
	if err == nil {
		defer acctRows.Close()
		for acctRows.Next() {
			var aID, aName, aCurr string
			acctRows.Scan(&aID, &aName, &aCurr)
			opt := fmt.Sprintf(`<option value="%s">%s (%s)</option>`, aID, aName, aCurr)
			acctOptions.WriteString(opt)
			editAcctOptions.WriteString(opt)
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
                <li><a href="/budgets/%s/accounts">💳 2. Accounts</a></li>
                <li><a href="/budgets/%s/transactions" class="active font-bold">💸 3. Transactions</a></li>
                <li><a href="/budgets/%s/reports">📊 4. Reports</a></li>
                <li><a href="/budgets/%s/parties">🏢 5. External Parties</a></li>
                <li><a href="/budgets/%s/members">👥 6. Members</a></li>
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
        </main>
    </div>

    <!-- Mobile Bottom Navigation Dock -->
    <div class="dock md:hidden bg-base-100 border-t border-base-300">
        <a href="/budgets/%s/budget">
            <span class="text-xl">📂</span>
            <span class="dock-label">Budget</span>
        </a>
        <a href="/budgets/%s/accounts">
            <span class="text-xl">💳</span>
            <span class="dock-label">Accounts</span>
        </a>
        <a href="/budgets/%s/transactions" class="dock-active">
            <span class="text-xl">💸</span>
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
    </script>
</body>
</html>`,
		budgetName, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID, budgetID,
		budgetID, csrfToken, txRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID, budgetID,
		budgetID, csrfToken, time.Now().UTC().Format("2006-01-02"), acctOptions.String(), bucketOptions.String(),
		csrfToken, editAcctOptions.String(), editBucketOptions.String(),
		budgetID)

	_ = role
	return c.HTML(http.StatusOK, html)
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
		logger.Info(ctx, "Auto-created default checking account for synchronization", "account_id", userAcctID)
	}

	// Fetch transactions from mock bank
	extTxs, err := h.mockAdapter.FetchTransactions(c.Request().Context(), userAcctID, 0)
	if err != nil {
		logger.Error(ctx, "Bank sync failed fetching transactions from adapter", "account_id", userAcctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Bank sync error")
	}

	importedCount := 0
	for _, ext := range extTxs {
		dedup := transaction.ComputeCanonicalDedupHash(userAcctID, ext.ExternalTxID, ext.BookingDateMicro, ext.AmountMinor, ext.Currency, ext.Payee, ext.Reference)
		rawID := domain.NewUUIDv7()

		res, err := tenantDB.ExecContext(c.Request().Context(), `
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
			}
		}
	}

	logger.Info(ctx, "Bank transaction synchronization completed",
		"budget_id", budgetID,
		"account_id", userAcctID,
		"fetched_count", len(extTxs),
		"imported_count", importedCount,
	)

	_ = corrID
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

