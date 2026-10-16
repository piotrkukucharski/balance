package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/gocardless"
	"github.com/balance/balance/internal/integration/mock"
	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

type AccountsHandler struct {
	mainDB           *sql.DB
	tenantMgr        *db.TenantDBManager
	mockAdapter      *mock.MockAdapter
	gocardlessClient *gocardless.Client
}

func NewAccountsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *AccountsHandler {
	secretID := os.Getenv("GOCARDLESS_SECRET_ID")
	secretKey := os.Getenv("GOCARDLESS_SECRET_KEY")
	redirectURL := os.Getenv("GOCARDLESS_REDIRECT_URL")
	var gcClient *gocardless.Client
	if secretID != "" && secretKey != "" {
		gcClient = gocardless.NewClient(secretID, secretKey, redirectURL)
	}

	return &AccountsHandler{
		mainDB:           mainDB,
		tenantMgr:        tenantMgr,
		mockAdapter:      mock.NewMockAdapter(),
		gocardlessClient: gcClient,
	}
}

// AccountsView lists all user-owned financial accounts with multi-currency balances and CRUD controls.
func (h *AccountsHandler) AccountsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.id, a.name, a.account_type, a.primary_currency, COALESCE(a.iban, ''),
		       a.is_closed, COALESCE(acb.balance_minor, 0),
		       COALESCE(ail.external_account_id, ''),
		       COALESCE(ail.integration_id, '')
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

	now := time.Now().UTC().UnixMicro()

	var accountRows strings.Builder
	hasAccounts := false
	for rows.Next() {
		hasAccounts = true
		var id, name, aType, curr, iban, extID, intID string
		var isClosed int
		var balanceMinor int64
		rows.Scan(&id, &name, &aType, &curr, &iban, &isClosed, &balanceMinor, &extID, &intID)

		// Check if bank integration is expired in main.db
		isConsentExpired := false
		if intID != "" {
			var intStatus string
			var expiresAt sql.NullInt64
			err := h.mainDB.QueryRowContext(c.Request().Context(), `
				SELECT status, expires_at FROM user_integrations WHERE id = ?`, intID).Scan(&intStatus, &expiresAt)
			if err == nil {
				if intStatus == "EXPIRED" || (expiresAt.Valid && expiresAt.Int64 > 0 && expiresAt.Int64 < now) {
					isConsentExpired = true
				}
			}
		}

		statusBadge := `<span class="badge badge-success badge-sm font-semibold">Active</span>`
		if isConsentExpired {
			statusBadge = `<span class="badge badge-warning badge-sm font-bold gap-1 animate-pulse"><span>⚠️</span><span>Consent Expired</span></span>`
		} else if isClosed == 1 {
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

		reconnectBtn := ""
		if extID != "" {
			btnClass := "btn-ghost text-primary"
			btnText := "Reconnect"
			if isConsentExpired {
				btnClass = "btn-warning animate-bounce font-bold shadow-xs"
				btnText = "⚠️ Reconnect"
			}
			reconnectBtn = fmt.Sprintf(`
				<button onclick="reconnectAccount('%s', '%s')" class="btn btn-xs %s gap-1" title="Renew bank authorization">%s</button>
			`, id, strings.ReplaceAll(name, "'", "\\'"), btnClass, btnText)
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
					<div class="flex items-center justify-end gap-1.5">
						%s
						<button popovertarget="account-menu-%s" style="anchor-name:--account-anchor-%s" class="btn btn-ghost btn-xs">•••</button>
						<ul class="dropdown dropdown-end menu p-2 shadow-lg bg-base-100 rounded-box w-36 text-xs border border-base-300" popover id="account-menu-%s" style="position-anchor:--account-anchor-%s">
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
			reconnectBtn,
			id, id, id, id,
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

    <!-- Link Bank Account Multi-Step Modal (Actual Budget & Open Banking Style) -->
    <dialog id="link_bank_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6 max-w-2xl">
            <div class="flex justify-between items-center mb-4">
                <div class="flex items-center gap-2">
                    <span class="text-2xl">🏦</span>
                    <div>
                        <h3 class="font-extrabold text-xl tracking-tight">Connect Bank Accounts</h3>
                        <p id="bank_wizard_subtitle" class="text-xs text-base-content/60">Step 1 of 3: Choose Integration Provider</p>
                    </div>
                </div>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle" onclick="resetBankWizard()">✕</button></form>
            </div>

            <!-- Wizard Steps Indicator -->
            <div class="flex items-center gap-2 mb-6">
                <div id="step_dot_1" class="h-1.5 flex-1 rounded-full bg-primary transition-all"></div>
                <div id="step_dot_2" class="h-1.5 flex-1 rounded-full bg-base-300 transition-all"></div>
                <div id="step_dot_3" class="h-1.5 flex-1 rounded-full bg-base-300 transition-all"></div>
                <div id="step_dot_4" class="h-1.5 flex-1 rounded-full bg-base-300 transition-all"></div>
            </div>

            <!-- STEP 1: Choose Provider (Mock vs GoCardless) -->
            <div id="bank_step_1" class="space-y-4">
                <p class="text-xs text-base-content/70">Select your bank sync provider to connect accounts and import transactions:</p>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
                    <div onclick="selectProvider('MOCK')" class="card card-border border-base-300 bg-base-100 hover:border-primary cursor-pointer p-4 transition-all hover:shadow-md flex flex-col justify-between gap-3">
                        <div class="flex items-start gap-3">
                            <span class="text-3xl">🧪</span>
                            <div>
                                <h4 class="font-bold text-sm">Mock Sandbox</h4>
                                <span class="badge badge-warning badge-xs font-semibold mt-1">Dev & Testing</span>
                                <p class="text-xs text-base-content/60 mt-1">Deterministic instant simulated accounts (mBank PLN, Savings EUR) with auto-generated test transactions.</p>
                            </div>
                        </div>
                        <button type="button" class="btn btn-outline btn-primary btn-xs w-full">Select Mock Provider</button>
                    </div>

                    <div onclick="selectProvider('GOCARDLESS')" class="card card-border border-base-300 bg-base-100 hover:border-primary cursor-pointer p-4 transition-all hover:shadow-md flex flex-col justify-between gap-3">
                        <div class="flex items-start gap-3">
                            <span class="text-3xl">🇪🇺</span>
                            <div>
                                <h4 class="font-bold text-sm">GoCardless</h4>
                                <span class="badge badge-success badge-xs font-semibold mt-1">Live Open Banking</span>
                                <p class="text-xs text-base-content/60 mt-1">Connect official European bank feeds via PSD2 Open Banking (Poland, Germany, UK, France, and more).</p>
                            </div>
                        </div>
                        <button type="button" class="btn btn-primary btn-xs w-full">Select GoCardless</button>
                    </div>
                </div>
            </div>

            <!-- STEP 2: Choose Country -->
            <div id="bank_step_2" class="space-y-4 hidden">
                <div class="flex justify-between items-center">
                    <p class="text-xs text-base-content/70">Select the country of your financial institution:</p>
                    <button type="button" onclick="goToStep(1)" class="btn btn-ghost btn-xs text-base-content/60">← Change Provider</button>
                </div>
                <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
                    <button type="button" onclick="selectCountry('PL')" class="btn btn-outline hover:btn-primary justify-start gap-2 h-auto py-2.5">
                        <span class="text-lg">🇵🇱</span>
                        <div class="text-left">
                            <div class="font-bold text-xs">Poland</div>
                            <div class="text-[10px] text-base-content/50">PL</div>
                        </div>
                    </button>
                    <button type="button" onclick="selectCountry('DE')" class="btn btn-outline hover:btn-primary justify-start gap-2 h-auto py-2.5">
                        <span class="text-lg">🇩🇪</span>
                        <div class="text-left">
                            <div class="font-bold text-xs">Germany</div>
                            <div class="text-[10px] text-base-content/50">DE</div>
                        </div>
                    </button>
                    <button type="button" onclick="selectCountry('GB')" class="btn btn-outline hover:btn-primary justify-start gap-2 h-auto py-2.5">
                        <span class="text-lg">🇬🇧</span>
                        <div class="text-left">
                            <div class="font-bold text-xs">United Kingdom</div>
                            <div class="text-[10px] text-base-content/50">GB</div>
                        </div>
                    </button>
                    <button type="button" onclick="selectCountry('FR')" class="btn btn-outline hover:btn-primary justify-start gap-2 h-auto py-2.5">
                        <span class="text-lg">🇫🇷</span>
                        <div class="text-left">
                            <div class="font-bold text-xs">France</div>
                            <div class="text-[10px] text-base-content/50">FR</div>
                        </div>
                    </button>
                </div>
            </div>

            <!-- STEP 3: Choose Institutions (Multi-Select Supported) -->
            <div id="bank_step_3" class="space-y-4 hidden">
                <div class="flex justify-between items-center gap-2">
                    <div class="flex-1">
                        <input id="bank_search_input" type="text" placeholder="Search banks by name or BIC..." oninput="filterInstitutions()" class="input input-bordered input-sm w-full" />
                    </div>
                    <button type="button" onclick="goToStep(2)" class="btn btn-ghost btn-xs text-base-content/60">← Back</button>
                </div>

                <div class="flex justify-between items-center text-xs px-1 text-base-content/70">
                    <span>Select one or more banks to connect:</span>
                    <span id="selected_banks_count" class="badge badge-neutral badge-xs font-semibold">0 selected</span>
                </div>

                <div id="institutions_list_container" class="max-h-64 overflow-y-auto space-y-2 border border-base-300 rounded-lg p-2 bg-base-200/30">
                    <div class="py-8 text-center text-xs text-base-content/50 flex flex-col items-center gap-2">
                        <span class="loading loading-spinner loading-sm text-primary"></span>
                        <span>Loading available institutions...</span>
                    </div>
                </div>

                <div class="modal-action mt-4 flex justify-between items-center">
                    <button type="button" class="btn btn-ghost btn-sm" onclick="goToStep(2)">Back</button>
                    <button id="connect_banks_btn" type="button" disabled onclick="startConnectionQueue()" class="btn btn-primary btn-sm font-semibold shadow">
                        Connect Selected (<span id="btn_selected_count">0</span>)
                    </button>
                </div>
            </div>

            <!-- STEP 4: Execution Queue Progress (Sequential Processing) -->
            <div id="bank_step_4" class="space-y-4 hidden">
                <div class="p-4 bg-base-200/50 rounded-lg border border-base-300 space-y-3">
                    <h4 class="font-bold text-sm flex items-center gap-2">
                        <span id="queue_spinner" class="loading loading-spinner loading-xs text-primary"></span>
                        <span id="queue_title">Connecting Banks Sequentially...</span>
                    </h4>
                    <p class="text-xs text-base-content/70" id="queue_desc">Processing selected institutions one by one. Please do not close this window.</p>
                    <div id="queue_items_list" class="space-y-2"></div>
                </div>

                <div class="modal-action">
                    <button id="queue_done_btn" type="button" class="btn btn-primary btn-sm hidden font-semibold shadow" onclick="window.location.reload()">Done & View Accounts</button>
                </div>
            </div>
        </div>
        <form method="dialog" class="modal-backdrop"><button onclick="resetBankWizard()">close</button></form>
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
        const ACTIVE_BUDGET_ID = '%s';
        const CSRF_TOKEN = '%s';
        let wizardState = {
            provider: 'MOCK',
            country: 'PL',
            institutions: [],
            selectedInstitutionIDs: new Set()
        };

        function resetBankWizard() {
            goToStep(1);
            wizardState.selectedInstitutionIDs.clear();
            updateSelectedCount();
        }

        function goToStep(step) {
            [1, 2, 3, 4].forEach(s => {
                const el = document.getElementById('bank_step_' + s);
                const dot = document.getElementById('step_dot_' + s);
                if (el) {
                    if (s === step) el.classList.remove('hidden');
                    else el.classList.add('hidden');
                }
                if (dot) {
                    if (s <= step) {
                        dot.classList.add('bg-primary');
                        dot.classList.remove('bg-base-300');
                    } else {
                        dot.classList.remove('bg-primary');
                        dot.classList.add('bg-base-300');
                    }
                }
            });

            const subtitle = document.getElementById('bank_wizard_subtitle');
            if (subtitle) {
                if (step === 1) subtitle.innerText = 'Step 1 of 3: Choose Integration Provider';
                else if (step === 2) subtitle.innerText = 'Step 2 of 3: Select Country (' + wizardState.provider + ')';
                else if (step === 3) subtitle.innerText = 'Step 3 of 3: Choose Bank Institutions (' + wizardState.country + ')';
                else if (step === 4) subtitle.innerText = 'Connecting Bank Accounts';
            }
        }

        function selectProvider(p) {
            wizardState.provider = p;
            goToStep(2);
        }

        function selectCountry(c) {
            wizardState.country = c;
            goToStep(3);
            loadInstitutions();
        }

        async function loadInstitutions() {
            const container = document.getElementById('institutions_list_container');
            container.innerHTML = '<div class="py-8 text-center text-xs text-base-content/50 flex flex-col items-center gap-2"><span class="loading loading-spinner loading-sm text-primary"></span><span>Loading institutions for ' + wizardState.country + '...</span></div>';

            try {
                const res = await fetch('/budgets/' + ACTIVE_BUDGET_ID + '/accounts/institutions?provider=' + wizardState.provider + '&country=' + wizardState.country);
                const data = await res.json();
                wizardState.institutions = data.institutions || [];
                renderInstitutions(wizardState.institutions);
            } catch (err) {
                container.innerHTML = '<div class="py-6 text-center text-xs text-error">Failed loading institutions. Please try again.</div>';
            }
        }

        function renderInstitutions(list) {
            const container = document.getElementById('institutions_list_container');
            if (!list || list.length === 0) {
                container.innerHTML = '<div class="py-6 text-center text-xs text-base-content/50">No banking institutions found for the selected country.</div>';
                return;
            }

            let html = '';
            list.forEach(inst => {
                const isChecked = wizardState.selectedInstitutionIDs.has(inst.id);
                html += '<label class="flex items-center justify-between p-3 rounded-lg bg-base-100 hover:bg-base-200/70 border border-base-300 cursor-pointer transition-colors">' +
                    '<div class="flex items-center gap-3">' +
                        '<input type="checkbox" value="' + inst.id + '" ' + (isChecked ? 'checked' : '') + ' onchange="toggleInstitutionSelection(\'' + inst.id + '\', this.checked)" class="checkbox checkbox-primary checkbox-sm" />' +
                        '<div>' +
                            '<div class="font-bold text-xs text-base-content">' + inst.name + '</div>' +
                            '<div class="text-[10px] text-base-content/60 font-mono">BIC: ' + (inst.bic || 'N/A') + '</div>' +
                        '</div>' +
                    '</div>' +
                    '<span class="badge badge-soft badge-neutral badge-xs">' + inst.id + '</span>' +
                '</label>';
            });
            container.innerHTML = html;
        }

        function filterInstitutions() {
            const q = document.getElementById('bank_search_input').value.toLowerCase().trim();
            if (!q) {
                renderInstitutions(wizardState.institutions);
                return;
            }
            const filtered = wizardState.institutions.filter(i => (i.name && i.name.toLowerCase().includes(q)) || (i.bic && i.bic.toLowerCase().includes(q)) || (i.id && i.id.toLowerCase().includes(q)));
            renderInstitutions(filtered);
        }

        function toggleInstitutionSelection(id, checked) {
            if (checked) wizardState.selectedInstitutionIDs.add(id);
            else wizardState.selectedInstitutionIDs.delete(id);
            updateSelectedCount();
        }

        function updateSelectedCount() {
            const count = wizardState.selectedInstitutionIDs.size;
            document.getElementById('selected_banks_count').innerText = count + ' selected';
            document.getElementById('btn_selected_count').innerText = count;
            document.getElementById('connect_banks_btn').disabled = count === 0;
        }

        async function startConnectionQueue() {
            const selected = Array.from(wizardState.selectedInstitutionIDs);
            if (selected.length === 0) return;

            goToStep(4);
            const listContainer = document.getElementById('queue_items_list');
            listContainer.innerHTML = '';

            selected.forEach(instId => {
                const row = document.createElement('div');
                row.id = 'queue_item_' + instId;
                row.className = 'flex items-center justify-between p-2.5 rounded border border-base-300 bg-base-100 text-xs';
                row.innerHTML = '<span class="font-bold font-mono">' + instId + '</span><span class="badge badge-neutral badge-xs">Queued</span>';
                listContainer.appendChild(row);
            });

            // Process sequentially one by one
            for (let i = 0; i < selected.length; i++) {
                const instId = selected[i];
                const itemEl = document.getElementById('queue_item_' + instId);
                if (itemEl) itemEl.querySelector('.badge').className = 'badge badge-warning badge-xs animate-pulse';
                if (itemEl) itemEl.querySelector('.badge').innerText = 'Connecting...';

                try {
                    const res = await fetch('/budgets/' + ACTIVE_BUDGET_ID + '/accounts/connect-queue', {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Requested-With': 'XMLHttpRequest',
                            'X-CSRF-Token': CSRF_TOKEN
                        },
                        body: JSON.stringify({
                            csrf_token: CSRF_TOKEN,
                            provider: wizardState.provider,
                            country: wizardState.country,
                            institution_ids: [instId]
                        })
                    });
                    const data = await res.json();
                    const r = (data.results && data.results[0]) || {};

                    if (itemEl) {
                        if (r.status === 'awaiting_authorization' && r.auth_url) {
                            itemEl.querySelector('.badge').className = 'badge badge-info badge-xs';
                            itemEl.querySelector('.badge').innerHTML = '<a href="' + r.auth_url + '" target="_blank" class="underline font-bold">Authorize Bank ➔</a>';
                        } else if (r.status === 'connected') {
                            itemEl.querySelector('.badge').className = 'badge badge-success badge-xs font-semibold';
                            itemEl.querySelector('.badge').innerText = 'Connected (' + (r.accounts_count || 1) + ' accounts)';
                        } else {
                            itemEl.querySelector('.badge').className = 'badge badge-error badge-xs';
                            itemEl.querySelector('.badge').innerText = r.status || 'Failed';
                        }
                    }
                } catch (err) {
                    if (itemEl) {
                        itemEl.querySelector('.badge').className = 'badge badge-error badge-xs';
                        itemEl.querySelector('.badge').innerText = 'Network error';
                    }
                }
            }

            document.getElementById('queue_spinner').classList.add('hidden');
            document.getElementById('queue_title').innerText = 'All Bank Connections Processed';
            document.getElementById('queue_done_btn').classList.remove('hidden');
        }

        async function reconnectAccount(acctId, acctName) {
            if (!confirm('Reconnect bank feed for "' + acctName + '"? This will renew your open banking authorization.')) return;
            try {
                const res = await fetch('/budgets/' + ACTIVE_BUDGET_ID + '/accounts/' + acctId + '/reconnect', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                        'X-Requested-With': 'XMLHttpRequest',
                        'X-CSRF-Token': CSRF_TOKEN
                    },
                    body: JSON.stringify({ csrf_token: CSRF_TOKEN })
                });
                const data = await res.json();
                if (data.auth_url) {
                    window.location.href = data.auth_url;
                } else if (data.status === 'ACTIVE' || data.status === 'connected') {
                    window.location.reload();
                } else {
                    alert('Reconnect response: ' + (data.message || data.status || 'OK'));
                    window.location.reload();
                }
            } catch (err) {
                alert('Failed to initiate reconnect: ' + err.message);
            }
        }

        function openEditModal(id, name, curr, type) {
            document.getElementById('edit_name').value = name;
            document.getElementById('edit_account_form').action = '/budgets/' + ACTIVE_BUDGET_ID + '/accounts/' + id + '/update';
            edit_account_modal.showModal();
        }
    </script>
</body>
</html>`, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID, budgetID,
		accountRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID,
		budgetID, csrfToken,
		csrfToken,
		budgetID, csrfToken)

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
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:manual")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := c.FormValue("name")
	aType := c.FormValue("account_type")
	curr := c.FormValue("primary_currency")
	iban := c.FormValue("iban")
	initBalStr := c.FormValue("initial_balance")

	logger.Info(ctx, "Initiating manual financial account creation",
		"budget_id", budgetID,
		"user_id", userID,
		"name", name,
		"account_type", aType,
		"currency", curr,
		"iban", iban,
		"initial_balance_raw", initBalStr,
	)

	if name == "" || aType == "" || curr == "" {
		logger.Warn(ctx, "Manual account creation rejected: validation failed",
			"budget_id", budgetID,
			"name", name,
			"account_type", aType,
			"currency", curr,
		)
		return echo.NewHTTPError(http.StatusBadRequest, "Name, type, and currency are required")
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Manual account creation DB begin transaction failed", "budget_id", budgetID, "error", err.Error())
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
		logger.Error(ctx, "Failed writing to accounts ledger events table", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to insert account")
	}

	// Upsert accounts_current
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, 0, ?, ?)`,
		acctID, userPartyID, name, aType, curr, iban, userID, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed projecting to accounts_current", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update accounts_current")
	}

	// Handle initial balance if provided
	var initialBalanceMinor int64
	if initBalStr != "" {
		var initFloat float64
		fmt.Sscanf(initBalStr, "%f", &initFloat)
		initialBalanceMinor = int64(initFloat * 100)
		if initialBalanceMinor != 0 {
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
				VALUES (?, ?, ?, ?)`, acctID, curr, initialBalanceMinor, now)
			if err != nil {
				logger.Error(ctx, "Failed inserting initial account balance", "account_id", acctID, "currency", curr, "balance_minor", initialBalanceMinor, "error", err.Error())
			}
		}
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Manual account creation transaction commit failed", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	logger.Info(ctx, "Manual financial account created successfully",
		"budget_id", budgetID,
		"account_id", acctID,
		"party_id", userPartyID,
		"name", name,
		"account_type", aType,
		"currency", curr,
		"initial_balance_minor", initialBalanceMinor,
	)

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// LinkBankAccount connects a bank feed (mock or GoCardless) to a new account.
func (h *AccountsHandler) LinkBankAccount(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:bank_link")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	extAcctID := c.FormValue("external_account_id")
	name := c.FormValue("name")

	logger.Info(ctx, "Initiating bank account link flow",
		"budget_id", budgetID,
		"user_id", userID,
		"external_account_id", extAcctID,
		"account_name", name,
	)

	if extAcctID == "" || name == "" {
		logger.Warn(ctx, "Bank link failed: external account selection and name required",
			"budget_id", budgetID,
			"external_account_id", extAcctID,
			"name", name,
		)
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
		logger.Warn(ctx, "Discovered bank account not found in provider",
			"budget_id", budgetID,
			"external_account_id", extAcctID,
		)
		return echo.NewHTTPError(http.StatusNotFound, "Discovered bank account not found")
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Bank link DB begin transaction failed", "budget_id", budgetID, "error", err.Error())
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
		logger.Error(ctx, "Bank link failed creating account projection", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating account")
	}

	// 2. Link integration
	linkID := domain.NewUUIDv7()
	intID := "mock_integration_" + userID
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		linkID, acctID, intID, matched.ID, now, now,
	)
	if err != nil {
		logger.Error(ctx, "Bank link failed creating integration link", "account_id", acctID, "link_id", linkID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating integration link")
	}

	// 3. Set balance
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, ?, ?, ?)`,
		acctID, matched.Currency, matched.BalanceMinor, now,
	)
	if err != nil {
		logger.Error(ctx, "Bank link failed storing initial balance", "account_id", acctID, "balance_minor", matched.BalanceMinor, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed setting balance")
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Bank link transaction commit failed", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	logger.Info(ctx, "Bank account linked and initial balance synchronized successfully",
		"budget_id", budgetID,
		"account_id", acctID,
		"external_account_id", matched.ID,
		"institution_name", matched.Name,
		"currency", matched.Currency,
		"balance_minor", matched.BalanceMinor,
		"integration_id", intID,
	)

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

// InstitutionItem defines the common response model for bank search.
type InstitutionItem struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	BIC       string   `json:"bic"`
	Countries []string `json:"countries"`
	Logo      string   `json:"logo"`
	Provider  string   `json:"provider"`
}

// GetInstitutions returns the list of available banks for a selected provider and country.
func (h *AccountsHandler) GetInstitutions(c echo.Context) error {
	provider := strings.ToUpper(c.QueryParam("provider"))
	country := strings.ToUpper(c.QueryParam("country"))
	if country == "" {
		country = "PL"
	}

	var results []InstitutionItem

	if provider == "GOCARDLESS" && h.gocardlessClient != nil {
		gcInsts, err := h.gocardlessClient.ListInstitutions(c.Request().Context(), country)
		if err == nil {
			for _, inst := range gcInsts {
				results = append(results, InstitutionItem{
					ID:        inst.ID,
					Name:      inst.Name,
					BIC:       inst.BIC,
					Countries: inst.Countries,
					Logo:      inst.Logo,
					Provider:  "GOCARDLESS",
				})
			}
		}
	}

	// Always provide MOCK institutions as default or fallback
	if provider == "MOCK" || len(results) == 0 {
		for _, inst := range h.mockAdapter.ListInstitutions(country) {
			results = append(results, InstitutionItem{
				ID:        inst.ID,
				Name:      inst.Name,
				BIC:       inst.BIC,
				Countries: inst.Countries,
				Logo:      inst.Logo,
				Provider:  "MOCK",
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"provider":     provider,
		"country":      country,
		"institutions": results,
	})
}

type ConnectBankRequest struct {
	Provider       string   `json:"provider"`
	Country        string   `json:"country"`
	InstitutionIDs []string `json:"institution_ids"`
}

// ConnectBankQueue processes selected bank institutions.
// If multiple institutions are selected, it connects each sequentially.
func (h *AccountsHandler) ConnectBankQueue(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:connect_queue")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)

	var req ConnectBankRequest
	if err := c.Bind(&req); err != nil {
		// Fallback to form values
		req.Provider = c.FormValue("provider")
		req.Country = c.FormValue("country")
		if instIDs := c.Request().Form["institution_ids"]; len(instIDs) > 0 {
			req.InstitutionIDs = instIDs
		} else if instID := c.FormValue("institution_id"); instID != "" {
			req.InstitutionIDs = strings.Split(instID, ",")
		}
	}

	logger.Info(ctx, "Starting bank connection queue process",
		"budget_id", budgetID,
		"user_id", userID,
		"provider", req.Provider,
		"country", req.Country,
		"selected_institutions_count", len(req.InstitutionIDs),
		"institutions", req.InstitutionIDs,
	)

	if len(req.InstitutionIDs) == 0 {
		logger.Warn(ctx, "Bank connection queue rejected: no institutions selected", "budget_id", budgetID)
		return echo.NewHTTPError(http.StatusBadRequest, "No institutions selected")
	}

	req.Provider = strings.ToUpper(req.Provider)
	if req.Provider == "" {
		req.Provider = "MOCK"
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)

	type ConnectedBankResult struct {
		InstitutionID string `json:"institution_id"`
		Status        string `json:"status"`
		AccountsCount int    `json:"accounts_count"`
		AuthURL       string `json:"auth_url,omitempty"`
	}

	var results []ConnectedBankResult

	for idx, instID := range req.InstitutionIDs {
		instID = strings.TrimSpace(instID)
		if instID == "" {
			continue
		}

		logger.Info(ctx, "Processing bank institution in connection queue",
			"queue_index", idx+1,
			"total_in_queue", len(req.InstitutionIDs),
			"institution_id", instID,
			"provider", req.Provider,
		)

		if req.Provider == "GOCARDLESS" && h.gocardlessClient != nil {
			userRef := fmt.Sprintf("bal_%s_%s_%d", budgetID, userID, time.Now().Unix())
			requisition, err := h.gocardlessClient.CreateRequisition(c.Request().Context(), instID, userRef)
			if err != nil {
				logger.Error(ctx, "Failed to create GoCardless requisition for institution",
					"institution_id", instID,
					"user_ref", userRef,
					"error", err.Error(),
				)
				results = append(results, ConnectedBankResult{
					InstitutionID: instID,
					Status:        "error: " + err.Error(),
				})
				continue
			}

			// Store requisition in user_integrations
			intID := domain.NewUUIDv7()
			h.mainDB.ExecContext(c.Request().Context(), `
				INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, created_at, updated_at)
				VALUES (?, ?, 'GOCARDLESS', ?, ?, 'PENDING', ?, ?)`,
				intID, userID, instID, requisition.ID, now, now,
			)

			logger.Info(ctx, "GoCardless requisition created, awaiting user bank authorization",
				"institution_id", instID,
				"requisition_id", requisition.ID,
				"user_ref", userRef,
				"integration_id", intID,
			)

			results = append(results, ConnectedBankResult{
				InstitutionID: instID,
				Status:        "awaiting_authorization",
				AuthURL:       requisition.Link,
			})
			continue
		}

		// Mock Provider: Deterministic sequential link
		mockAccounts, err := h.mockAdapter.FetchAccounts(c.Request().Context(), userID)
		if err != nil {
			logger.Error(ctx, "Failed fetching mock provider accounts", "institution_id", instID, "error", err.Error())
		}
		accountsAdded := 0

		tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
		if err != nil {
			logger.Error(ctx, "Failed beginning transaction for mock institution import", "institution_id", instID, "error", err.Error())
			continue
		}

		for mIdx, ma := range mockAccounts {
			acctID := domain.NewUUIDv7()
			accName := fmt.Sprintf("%s (%s #%d)", ma.Name, instID, mIdx+1)

			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, ?, 'CHECKING', ?, ?, 0, ?, ?)`,
				acctID, userPartyID, accName, ma.Currency, ma.IBAN, userID, now,
			)
			if err != nil {
				logger.Error(ctx, "Failed inserting accounts_current in mock queue", "institution_id", instID, "account_id", acctID, "error", err.Error())
				continue
			}

			linkID := domain.NewUUIDv7()
			intID := fmt.Sprintf("mock_int_%s_%s", instID, userID)
			tx.ExecContext(c.Request().Context(), `
				INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				linkID, acctID, intID, fmt.Sprintf("%s_%s", ma.ID, instID), now, now,
			)

			tx.ExecContext(c.Request().Context(), `
				INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
				VALUES (?, ?, ?, ?)`,
				acctID, ma.Currency, ma.BalanceMinor, now,
			)
			accountsAdded++
		}
		if err := tx.Commit(); err != nil {
			logger.Error(ctx, "Failed committing accounts for mock institution", "institution_id", instID, "error", err.Error())
		} else {
			logger.Info(ctx, "Mock bank accounts connected successfully",
				"institution_id", instID,
				"accounts_added_count", accountsAdded,
			)
		}

		results = append(results, ConnectedBankResult{
			InstitutionID: instID,
			Status:        "connected",
			AccountsCount: accountsAdded,
		})
	}

	logger.Info(ctx, "Bank connection queue processing completed",
		"budget_id", budgetID,
		"results_count", len(results),
	)

	// If requested via JSON API or fetch, return structured response
	if strings.Contains(c.Request().Header.Get("Accept"), "application/json") || c.Request().Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return c.JSON(http.StatusOK, map[string]any{
			"budget_id": budgetID,
			"results":   results,
		})
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// ReconnectAccount handles in-place re-consent / re-authorization for an existing bank-linked account.
func (h *AccountsHandler) ReconnectAccount(c echo.Context) error {
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)

	now := time.Now().UTC().UnixMicro()

	// 1. Fetch existing account and its integration mapping
	var integrationID, externalAcctID, acctName string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT a.name, COALESCE(ail.integration_id, ''), COALESCE(ail.external_account_id, '')
		FROM accounts_current a
		LEFT JOIN account_integration_links ail ON a.id = ail.account_id
		WHERE a.id = ?`, acctID).Scan(&acctName, &integrationID, &externalAcctID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Account not found")
	}

	if externalAcctID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Account is not linked to any bank integration")
	}

	// 2. Fetch integration record from main.db
	var provider, institutionID string
	err = h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT provider, COALESCE(institution_id, '')
		FROM user_integrations WHERE id = ? AND user_id = ?`, integrationID, userID).Scan(&provider, &institutionID)
	if err != nil {
		// If integration was not registered or mock pattern
		provider = "MOCK"
	}

	if provider == "GOCARDLESS" && h.gocardlessClient != nil {
		budgetID := c.Param("budgetId")
		userRef := fmt.Sprintf("bal_recon_%s_%s_%d", budgetID, userID, time.Now().Unix())
		requisition, err := h.gocardlessClient.CreateRequisition(c.Request().Context(), institutionID, userRef)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create bank reconnection session: "+err.Error())
		}

		// Update or insert renewed integration
		newIntID := domain.NewUUIDv7()
		h.mainDB.ExecContext(c.Request().Context(), `
			INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, created_at, updated_at)
			VALUES (?, ?, 'GOCARDLESS', ?, ?, 'PENDING', ?, ?)`,
			newIntID, userID, institutionID, requisition.ID, now, now,
		)

		// Link account to new integration record
		tenantDB.ExecContext(c.Request().Context(), `
			UPDATE account_integration_links SET integration_id = ?, last_synced_at = ? WHERE account_id = ?`,
			newIntID, now, acctID,
		)

		return c.JSON(http.StatusOK, map[string]any{
			"status":   "awaiting_authorization",
			"auth_url": requisition.Link,
			"account":  acctName,
		})
	}

	// Mock Provider: Instant re-activation and renewal
	if integrationID != "" {
		h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE user_integrations SET status = 'ACTIVE', expires_at = ?, updated_at = ? WHERE id = ?`,
			now+180*24*3600*1_000_000, now, integrationID,
		)
	}

	tenantDB.ExecContext(c.Request().Context(), `
		UPDATE account_integration_links SET last_synced_at = ? WHERE account_id = ?`,
		now, acctID,
	)

	return c.JSON(http.StatusOK, map[string]any{
		"status":  "ACTIVE",
		"account": acctName,
		"message": "Bank access successfully renewed",
	})
}

// GoCardlessCallback handles OAuth redirect from GoCardless after user authenticates with their bank.
func (h *AccountsHandler) GoCardlessCallback(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:gocardless_callback")

	ref := c.QueryParam("ref")
	if ref == "" {
		logger.Warn(ctx, "GoCardless callback rejected: missing requisition reference")
		return echo.NewHTTPError(http.StatusBadRequest, "Missing requisition reference")
	}

	userID, _ := c.Get(ContextUserIDKey).(string)
	logger.Info(ctx, "GoCardless OAuth callback received", "ref", ref, "user_id", userID)

	now := time.Now().UTC().UnixMicro()

	// 1. Locate user integration by requisition external_reference_id OR ref pattern
	var intID, instID, dbUserID string
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT id, institution_id, user_id
		FROM user_integrations
		WHERE external_reference_id = ?
		LIMIT 1`, ref).Scan(&intID, &instID, &dbUserID)

	if err != nil {
		// Try matching by reference prefix if ref is the userRef
		err = h.mainDB.QueryRowContext(c.Request().Context(), `
			SELECT id, institution_id, user_id
			FROM user_integrations
			WHERE provider = 'GOCARDLESS' AND status = 'PENDING'
			ORDER BY created_at DESC LIMIT 1`).Scan(&intID, &instID, &dbUserID)
	}

	if userID == "" && dbUserID != "" {
		userID = dbUserID
	}

	// Extract budgetID from userRef pattern: bal_<budgetID>_<userID>_<timestamp>
	var targetBudgetID string
	parts := strings.Split(ref, "_")
	if len(parts) >= 3 && domain.IsValidUUIDv7(parts[1]) {
		targetBudgetID = parts[1]
	} else if len(parts) >= 4 && parts[1] == "recon" && domain.IsValidUUIDv7(parts[2]) {
		targetBudgetID = parts[2]
	}

	// Fallback to query param if passed
	if targetBudgetID == "" {
		targetBudgetID = c.QueryParam("budget_id")
	}

	// Fallback to first user budget if still empty
	if targetBudgetID == "" && userID != "" {
		h.mainDB.QueryRowContext(c.Request().Context(), `
			SELECT budget_id FROM budget_members WHERE user_id = ? ORDER BY joined_at ASC LIMIT 1`,
			userID).Scan(&targetBudgetID)
	}

	if targetBudgetID == "" {
		return c.Redirect(http.StatusSeeOther, "/budgets")
	}

	// 2. If gocardless client is configured, inspect requisition accounts and import them
	if h.gocardlessClient != nil {
		reqDetails, err := h.gocardlessClient.GetRequisition(c.Request().Context(), ref)
		if err == nil && reqDetails != nil && len(reqDetails.Accounts) > 0 {
			tenantDB, err := h.tenantMgr.GetTenantDB(targetBudgetID, false)
			if err == nil && tenantDB != nil {
				userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
				for idx, extAcctID := range reqDetails.Accounts {
					details, err := h.gocardlessClient.GetAccountDetails(c.Request().Context(), extAcctID)
					var acctName, iban, curr string
					curr = "PLN"
					if err == nil && details != nil {
						iban = details.Account.IBAN
						if details.Account.Currency != "" {
							curr = details.Account.Currency
						}
						if details.Account.Name != "" {
							acctName = details.Account.Name
						} else if details.Account.DisplayName != "" {
							acctName = details.Account.DisplayName
						} else if iban != "" {
							acctName = iban
						}
					}
					if acctName == "" {
						acctName = fmt.Sprintf("Bank Account #%d (%s)", idx+1, instID)
					}

					// Check if account already linked
					var existingAcctID string
					tenantDB.QueryRowContext(c.Request().Context(), `
						SELECT account_id FROM account_integration_links WHERE external_account_id = ?`,
						extAcctID).Scan(&existingAcctID)

					if existingAcctID != "" {
						// Update last synced and integration id
						tenantDB.ExecContext(c.Request().Context(), `
							UPDATE account_integration_links SET integration_id = ?, last_synced_at = ? WHERE account_id = ?`,
							intID, now, existingAcctID)
						logger.Info(ctx, "Existing bank account integration link refreshed",
							"account_id", existingAcctID,
							"external_account_id", extAcctID,
							"integration_id", intID,
						)
					} else {
						newAcctID := domain.NewUUIDv7()
						tenantDB.ExecContext(c.Request().Context(), `
							INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
							VALUES (?, 1, ?, ?, 'CHECKING', ?, ?, 0, ?, ?)`,
							newAcctID, userPartyID, acctName, curr, iban, userID, now)

						tenantDB.ExecContext(c.Request().Context(), `
							INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
							VALUES (?, ?, ?, ?, ?, ?)`,
							domain.NewUUIDv7(), newAcctID, intID, extAcctID, now, now)

						logger.Info(ctx, "New bank account imported from GoCardless OAuth callback",
							"account_id", newAcctID,
							"name", acctName,
							"currency", curr,
							"iban", iban,
							"external_account_id", extAcctID,
							"integration_id", intID,
						)
					}
				}
			}
		}
	}

	// 3. Transition integration to ACTIVE in mainDB
	if intID != "" {
		h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE user_integrations SET status = 'ACTIVE', expires_at = ?, updated_at = ? WHERE id = ?`,
			now+90*24*3600*1_000_000, now, intID)
		logger.Info(ctx, "GoCardless integration activated", "integration_id", intID, "expires_in_days", 90)
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", targetBudgetID))
}


