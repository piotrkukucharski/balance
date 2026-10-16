package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/mock"
	"github.com/labstack/echo/v4"
)

type AccountsHandler struct {
	mainDB      *sql.DB
	tenantMgr   *db.TenantDBManager
	mockAdapter *mock.MockAdapter
}

func NewAccountsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *AccountsHandler {
	return &AccountsHandler{
		mainDB:      mainDB,
		tenantMgr:   tenantMgr,
		mockAdapter: mock.NewMockAdapter(),
	}
}

// AccountsView lists all user-owned financial accounts with multi-currency balances and CRUD controls.
func (h *AccountsHandler) AccountsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.id, a.name, a.account_type, a.primary_currency, COALESCE(a.iban, ''),
		       a.is_closed, COALESCE(acb.balance_minor, 0),
		       COALESCE(ail.external_account_id, '')
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN account_currency_balances acb ON a.id = acb.account_id AND a.primary_currency = acb.currency
		LEFT JOIN account_integration_links ail ON a.id = ail.account_id
		WHERE p.is_user_owned = 1
		ORDER BY a.is_closed ASC, a.name ASC`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading accounts")
	}
	defer rows.Close()

	var accountRows strings.Builder
	hasAccounts := false
	for rows.Next() {
		hasAccounts = true
		var id, name, aType, curr, iban, extID string
		var isClosed int
		var balanceMinor int64
		rows.Scan(&id, &name, &aType, &curr, &iban, &isClosed, &balanceMinor, &extID)

		statusBadge := `<span class="badge badge-success badge-sm font-semibold">Active</span>`
		if isClosed == 1 {
			statusBadge = `<span class="badge badge-neutral badge-sm font-semibold opacity-60">Closed</span>`
		}

		typeBadge := "badge-soft badge-primary"
		switch aType {
		case "SAVINGS":
			typeBadge = "badge-soft badge-secondary"
		case "CREDIT_CARD":
			typeBadge = "badge-soft badge-warning"
		case "CASH":
			typeBadge = "badge-soft badge-accent"
		case "INVESTMENT":
			typeBadge = "badge-soft badge-info"
		}

		integrationBadge := `<span class="badge badge-ghost badge-sm text-xs opacity-50">Manual</span>`
		if extID != "" {
			integrationBadge = `<span class="badge badge-info badge-soft badge-sm font-bold gap-1"><span>🔄</span><span>Bank Linked</span></span>`
		}

		accountRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="font-bold text-sm">%s</div>
					<div class="text-xs font-mono opacity-50">%s</div>
				</td>
				<td><span class="badge %s text-xs font-bold">%s</span></td>
				<td>%s</td>
				<td>%s</td>
				<td class="font-mono text-right font-bold text-sm">
					%.2f <span class="text-xs font-normal opacity-60">%s</span>
				</td>
				<td class="text-right">
					<div class="dropdown dropdown-end">
						<button class="btn btn-ghost btn-xs">•••</button>
						<ul tabindex="0" class="dropdown-content menu p-2 shadow-lg bg-base-100 rounded-box w-36 text-xs z-10 border border-base-300">
							<li><a onclick="openEditModal('%s', '%s', '%s', '%s')">✏️ Edit</a></li>
							<li>
								<form method="POST" action="/budgets/%s/accounts/%s/toggle-close">
									<input type="hidden" name="csrf_token" value="%s" />
									<button type="submit" class="text-left w-full">%s</button>
								</form>
							</li>
							<li>
								<form method="POST" action="/budgets/%s/accounts/%s/delete" onsubmit="return confirm('Delete this account?')">
									<input type="hidden" name="csrf_token" value="%s" />
									<button type="submit" class="text-error text-left w-full">🗑️ Delete</button>
								</form>
							</li>
						</ul>
					</div>
				</td>
			</tr>`,
			name, iban, typeBadge, aType, integrationBadge, statusBadge,
			float64(balanceMinor)/100.0, curr,
			id, strings.ReplaceAll(name, "'", "\\'"), curr, aType,
			budgetID, id, c.Get("csrfToken"), toggleActionText(isClosed),
			budgetID, id, c.Get("csrfToken"),
		))
	}

	if !hasAccounts {
		accountRows.WriteString(`
			<tr>
				<td colspan="6" class="text-center py-8 text-base-content/50">
					No accounts found. Create a manual account or connect via Bank Integration below.
				</td>
			</tr>
		`)
	}

	// Fetch available mock bank accounts for integration connection
	userID := c.Get(ContextUserIDKey).(string)
	mockAccounts, _ := h.mockAdapter.FetchAccounts(c.Request().Context(), userID)
	var mockOptions strings.Builder
	for _, ma := range mockAccounts {
		mockOptions.WriteString(fmt.Sprintf(
			`<option value="%s">%s (%s - %.2f %s)</option>`,
			ma.ID, ma.Name, ma.IBAN, float64(ma.BalanceMinor)/100.0, ma.Currency,
		))
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Accounts</title>
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
                <span>Accounts</span>
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
                <li><a href="/budgets/%s/accounts" class="active font-bold">💳 2. Accounts</a></li>
                <li><a href="/budgets/%s/transactions">💸 3. Transactions</a></li>
                <li><a href="/budgets/%s/reports">📊 4. Reports</a></li>
                <li><a href="/budgets/%s/parties">🏢 5. External Parties</a></li>
                <li><a href="/budgets/%s/members">👥 6. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 md:p-8 overflow-y-auto">
            <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
                <div>
                    <h1 class="text-2xl font-black tracking-tight">Financial Accounts</h1>
                    <p class="text-xs text-base-content/60 mt-1">Manage checking, savings, credit card, and investment accounts.</p>
                </div>
                <div class="flex items-center gap-3">
                    <button class="btn btn-secondary btn-sm shadow font-semibold gap-1.5" onclick="link_bank_modal.showModal()">
                        <span>🔄</span>
                        <span>Link Bank Account</span>
                    </button>
                    <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="new_account_modal.showModal()">
                        <span>+</span>
                        <span>New Account</span>
                    </button>
                </div>
            </div>

            <!-- Accounts Table Card -->
            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra w-full">
                            <thead>
                                <tr class="bg-base-200/50">
                                    <th class="font-bold">Account Name</th>
                                    <th class="font-bold">Type</th>
                                    <th class="font-bold">Integration</th>
                                    <th class="font-bold">Status</th>
                                    <th class="font-bold text-right">Balance</th>
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
        <a href="/budgets/%s/accounts" class="dock-active">
            <span class="text-xl">💳</span>
            <span class="dock-label">Accounts</span>
        </a>
        <a href="/budgets/%s/transactions">
            <span class="text-xl">💸</span>
            <span class="dock-label">Txns</span>
        </a>
        <a href="/budgets/%s/reports">
            <span class="text-xl">📊</span>
            <span class="dock-label">Reports</span>
        </a>
        <a href="/budgets/%s/members">
            <span class="text-xl">👥</span>
            <span class="dock-label">Members</span>
        </a>
    </div>

    <!-- New Manual Account Modal -->
    <dialog id="new_account_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Add Financial Account</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/accounts" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Account Name</span></label>
                    <input type="text" name="name" required placeholder="e.g. Main Checking, Emergency Savings" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Account Type</span></label>
                        <select name="account_type" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="CHECKING" selected>Checking (Bieżące)</option>
                            <option value="SAVINGS">Savings (Oszczędnościowe)</option>
                            <option value="CREDIT_CARD">Credit Card (Karta)</option>
                            <option value="CASH">Cash (Gotówka / Portfel)</option>
                            <option value="INVESTMENT">Investment (Inwestycyjne)</option>
                        </select>
                    </div>
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Primary Currency</span></label>
                        <select name="primary_currency" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="PLN" selected>PLN (zł)</option>
                            <option value="EUR">EUR (€)</option>
                            <option value="USD">USD ($)</option>
                            <option value="GBP">GBP (£)</option>
                        </select>
                    </div>
                </div>
                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Initial Balance (Optional)</span></label>
                        <input type="number" step="0.01" name="initial_balance" placeholder="0.00" class="input input-bordered w-full input-md focus:input-primary" />
                    </div>
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">IBAN / Number (Optional)</span></label>
                        <input type="text" name="iban" placeholder="PL..." class="input input-bordered w-full input-md focus:input-primary" />
                    </div>
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="new_account_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Create Account</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Link Bank Account Modal -->
    <dialog id="link_bank_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <div class="flex items-center gap-2">
                    <span class="text-2xl">🏦</span>
                    <h3 class="font-extrabold text-xl tracking-tight">Connect via Bank Integration</h3>
                </div>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <p class="text-xs text-base-content/70 mb-4">Connect a live or simulated open banking feed (GoCardless / Mock Adapter) to automatically import statements.</p>
            <form method="POST" action="/budgets/%s/accounts/link-bank" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Discovered Institution Account</span></label>
                    <select name="external_account_id" class="select select-bordered w-full select-md focus:select-primary">
                        %s
                    </select>
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Account Label in Balance</span></label>
                    <input type="text" name="name" required placeholder="e.g. mBank Main Account" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="link_bank_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-secondary font-semibold shadow-md">Link & Sync Feed</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Edit Account Modal -->
    <dialog id="edit_account_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Edit Account</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="edit_account_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Account Name</span></label>
                    <input id="edit_name" type="text" name="name" required class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="edit_account_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Save Changes</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <script>
        function openEditModal(id, name, curr, type) {
            document.getElementById('edit_name').value = name;
            document.getElementById('edit_account_form').action = '/budgets/%s/accounts/' + id + '/update';
            edit_account_modal.showModal();
        }
    </script>
</body>
</html>`, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID, budgetID,
		accountRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID,
		budgetID, csrfToken,
		budgetID, csrfToken, mockOptions.String(),
		csrfToken, budgetID)

	return c.HTML(http.StatusOK, html)
}

func toggleActionText(isClosed int) string {
	if isClosed == 1 {
		return "🔓 Reopen Account"
	}
	return "🔒 Close Account"
}

// CreateAccount creates a new manual financial account.
func (h *AccountsHandler) CreateAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := c.FormValue("name")
	aType := c.FormValue("account_type")
	curr := c.FormValue("primary_currency")
	iban := c.FormValue("iban")

	if name == "" || aType == "" || curr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name, type, and currency are required")
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// Append account log
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, 0, 'CREATE', ?, ?, ?)`,
		acctID, userPartyID, name, aType, curr, iban, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to insert account")
	}

	// Upsert accounts_current
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, 0, ?, ?)`,
		acctID, userPartyID, name, aType, curr, iban, userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update accounts_current")
	}

	// Handle initial balance if provided
	if initBalStr := c.FormValue("initial_balance"); initBalStr != "" {
		var initFloat float64
		fmt.Sscanf(initBalStr, "%f", &initFloat)
		minor := int64(initFloat * 100)
		if minor != 0 {
			tx.ExecContext(c.Request().Context(), `
				INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
				VALUES (?, ?, ?, ?)`, acctID, curr, minor, now)
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// LinkBankAccount connects a bank feed (mock or GoCardless) to a new account.
func (h *AccountsHandler) LinkBankAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	extAcctID := c.FormValue("external_account_id")
	name := c.FormValue("name")
	if extAcctID == "" || name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "External account selection and name required")
	}

	// Fetch details from mock adapter
	mockAccounts, _ := h.mockAdapter.FetchAccounts(c.Request().Context(), userID)
	var matched *mock.ExtAccount
	for _, ma := range mockAccounts {
		if ma.ID == extAcctID {
			matched = &ma
			break
		}
	}
	if matched == nil {
		return echo.NewHTTPError(http.StatusNotFound, "Discovered bank account not found")
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// 1. Create account
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'CHECKING', ?, ?, 0, ?, ?)`,
		acctID, userPartyID, name, matched.Currency, matched.IBAN, userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating account")
	}

	// 2. Link integration
	linkID := domain.NewUUIDv7()
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		linkID, acctID, "mock_integration_"+userID, matched.ID, now, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating integration link")
	}

	// 3. Set balance
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, ?, ?, ?)`,
		acctID, matched.Currency, matched.BalanceMinor, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed setting balance")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	_ = corrID
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// UpdateAccount updates account name.
func (h *AccountsHandler) UpdateAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	name := c.FormValue("name")

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name required")
	}

	now := time.Now().UTC().UnixMicro()
	_, err := tenantDB.ExecContext(c.Request().Context(), `
		UPDATE accounts_current SET name = ?, updated_at = ?, agent_id = ? WHERE id = ?`,
		name, now, userID, acctID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Update error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// ToggleCloseAccount toggles between active and closed status.
func (h *AccountsHandler) ToggleCloseAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)

	now := time.Now().UTC().UnixMicro()
	_, err := tenantDB.ExecContext(c.Request().Context(), `
		UPDATE accounts_current SET is_closed = 1 - is_closed, updated_at = ?, agent_id = ? WHERE id = ?`,
		now, userID, acctID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Status toggle error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// DeleteAccount soft-deletes the account from current projection.
func (h *AccountsHandler) DeleteAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	now := time.Now().UTC().UnixMicro()
	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// Append soft delete to historical table
	var version int
	var partyID, name, aType, curr string
	var iban sql.NullString
	err = tx.QueryRowContext(c.Request().Context(), `
		SELECT version, party_id, name, account_type, primary_currency, iban
		FROM accounts_current WHERE id = ?`, acctID).Scan(&version, &partyID, &name, &aType, &curr, &iban)
	if err == nil {
		tx.ExecContext(c.Request().Context(), `
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, 'SOFT_DELETE', ?, ?, ?)`,
			acctID, version+1, partyID, name, aType, curr, iban, userID, corrID, now)
	}

	// Remove from accounts_current
	tx.ExecContext(c.Request().Context(), `DELETE FROM accounts_current WHERE id = ?`, acctID)
	tx.ExecContext(c.Request().Context(), `DELETE FROM account_integration_links WHERE account_id = ?`, acctID)

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

func (h *AccountsHandler) ensureUserParty(ctx context.Context, db *sql.DB, userID string, now int64) string {
	var partyID string
	err := db.QueryRowContext(ctx, `SELECT id FROM parties_current WHERE is_user_owned = 1 LIMIT 1`).Scan(&partyID)
	if err == nil {
		return partyID
	}

	partyID = domain.NewUUIDv7()
	db.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'User Family', 'INDIVIDUAL', 1, ?, ?)`, partyID, userID, now)
	return partyID
}
