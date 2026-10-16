package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/labstack/echo/v4"
)

type WorkspaceHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

func NewWorkspaceHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *WorkspaceHandler {
	return &WorkspaceHandler{
		mainDB:    mainDB,
		tenantMgr: tenantMgr,
	}
}

// BudgetsList renders the post-login budget selection dashboard with DaisyUI 5.
func (h *WorkspaceHandler) BudgetsList(c echo.Context) error {
	userID := c.Get(ContextUserIDKey).(string)

	rows, err := h.mainDB.QueryContext(c.Request().Context(), `
		SELECT b.id, b.name, b.default_currency, b.timezone, bm.role
		FROM budgets b
		JOIN budget_members bm ON b.id = bm.budget_id
		WHERE bm.user_id = ?
		ORDER BY b.created_at DESC`, userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load budgets")
	}
	defer rows.Close()

	var budgetCards strings.Builder
	hasBudgets := false
	for rows.Next() {
		hasBudgets = true
		var id, name, curr, tz, role string
		rows.Scan(&id, &name, &curr, &tz, &role)

		budgetCards.WriteString(fmt.Sprintf(`
			<div class="card card-border bg-base-100 shadow-md hover:shadow-xl transition-all duration-200 border-base-300">
				<div class="card-body p-6 gap-3">
					<div class="flex justify-between items-start">
						<div class="flex items-center gap-3">
							<div class="w-10 h-10 rounded-xl bg-primary/10 text-primary flex items-center justify-center font-bold text-lg">💰</div>
							<div>
								<h3 class="card-title text-xl font-bold tracking-tight">%s</h3>
								<div class="text-xs text-base-content/60">Timezone: %s</div>
							</div>
						</div>
						<span class="badge badge-soft badge-primary font-semibold text-xs">%s</span>
					</div>
					<div class="divider my-0"></div>
					<div class="flex items-center justify-between">
						<div class="flex items-center gap-2">
							<span class="badge badge-neutral badge-sm">%s</span>
							<span class="text-xs text-base-content/60">Default Currency</span>
						</div>
						<div class="card-actions">
							<a href="/budgets/%s/budget" class="btn btn-primary btn-sm gap-2 shadow-sm font-semibold">
								<span>Open Budget</span>
								<span>➔</span>
							</a>
						</div>
					</div>
				</div>
			</div>`, name, tz, role, curr, id))
	}

	if !hasBudgets {
		budgetCards.WriteString(`
			<div class="col-span-full card card-border bg-base-100 border-dashed border-base-300 p-8 text-center flex flex-col items-center justify-center gap-4">
				<div class="w-16 h-16 rounded-full bg-base-200 flex items-center justify-center text-3xl">📂</div>
				<div>
					<h3 class="font-bold text-lg">No Budgets Found</h3>
					<p class="text-xs text-base-content/60 max-w-sm mt-1">Create your first budget envelope workspace to begin tracking your cashflow and targets.</p>
				</div>
				<button class="btn btn-primary btn-sm shadow" onclick="new_budget_modal.showModal()">+ Create Budget</button>
			</div>
		`)
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Budgets</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col">
    <!-- Navbar -->
    <header class="navbar bg-base-100 border-b border-base-300 px-6 sticky top-0 z-30 shadow-xs">
        <div class="navbar-start gap-3 items-center">
            <span class="text-2xl font-black tracking-tight text-primary flex items-center gap-2">
                <span>⚖️</span>
                <span>Balance</span>
            </span>
        </div>
        <div class="navbar-end gap-3">
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm font-medium">Log Out</button>
            </form>
        </div>
    </header>

    <!-- Main Workspace -->
    <main class="flex-1 max-w-5xl w-full mx-auto p-6 md:p-8">
        <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-8">
            <div>
                <h1 class="text-3xl font-black tracking-tight">Your Budgets</h1>
                <p class="text-sm text-base-content/60 mt-1">Select an active budget or create a new envelope workspace.</p>
            </div>
            <button class="btn btn-primary shadow-md font-semibold gap-2" onclick="new_budget_modal.showModal()">
                <span>+</span>
                <span>New Budget</span>
            </button>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            %s
        </div>
    </main>

    <!-- New Budget Modal -->
    <dialog id="new_budget_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Create New Budget</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Budget Name</span></label>
                    <input type="text" name="name" required placeholder="Personal & Family Budget" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Primary Currency</span></label>
                        <select name="default_currency" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="PLN" selected>PLN (zł)</option>
                            <option value="EUR">EUR (€)</option>
                            <option value="USD">USD ($)</option>
                            <option value="GBP">GBP (£)</option>
                        </select>
                    </div>
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">IANA Timezone</span></label>
                        <input type="text" name="timezone" value="Europe/Warsaw" required class="input input-bordered w-full input-md focus:input-primary" />
                    </div>
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="new_budget_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Create Budget</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
</body>
</html>`, csrfToken, budgetCards.String(), csrfToken)

	return c.HTML(http.StatusOK, html)
}

// CreateBudget creates a new budget in main.db and provisions the tenant SQLite database.
func (h *WorkspaceHandler) CreateBudget(c echo.Context) error {
	userID := c.Get(ContextUserIDKey).(string)
	name := c.FormValue("name")
	curr := c.FormValue("default_currency")
	tz := c.FormValue("timezone")

	if name == "" || curr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Budget name and currency are required")
	}
	if tz == "" {
		tz = "UTC"
	}

	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	tx, err := h.mainDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Database error")
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		budgetID, name, curr, tz, userID, now, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create budget")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO budget_members (budget_id, user_id, role, joined_at)
		VALUES (?, ?, 'OWNER', ?)`,
		budgetID, userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to assign budget owner")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	// Provision tenant DB
	_, err = h.tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to initialize tenant database")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// BudgetView renders the YNAB-style Budget/Envelopes view with DaisyUI 5 stats, tables, and badges.
func (h *WorkspaceHandler) BudgetView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var budgetName, defaultCurr, tz string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency, timezone FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr, &tz)

	// Fetch buckets and balances
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT b.id, b.group_name, b.name, COALESCE(bcb.balance_minor, 0)
		FROM buckets_projection b
		LEFT JOIN bucket_currency_balances bcb ON b.id = bcb.bucket_id AND bcb.currency = ?
		WHERE b.is_archived = 0
		ORDER BY b.group_name, b.name`, defaultCurr)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading buckets")
	}
	defer rows.Close()

	var bucketRows strings.Builder
	hasBuckets := false
	for rows.Next() {
		hasBuckets = true
		var id, group, name string
		var balance int64
		rows.Scan(&id, &group, &name, &balance)

		badgeClass := "badge-success text-success-content"
		statusText := "Funded / On Track"
		statusDot := "bg-success"
		if balance < 0 {
			badgeClass = "badge-error text-error-content"
			statusText = "Overspent"
			statusDot = "bg-error"
		} else if balance == 0 {
			badgeClass = "badge-warning text-warning-content"
			statusText = "Underfunded"
			statusDot = "bg-warning"
		}

		bucketRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="flex items-center gap-3">
						<span class="w-2.5 h-2.5 rounded-full %s"></span>
						<div>
							<div class="font-bold text-sm">%s</div>
							<div class="text-xs text-base-content/50">%s</div>
						</div>
					</div>
				</td>
				<td>
					<span class="badge %s badge-sm font-semibold">%s</span>
				</td>
				<td class="font-mono text-right font-bold text-sm">
					%.2f <span class="text-xs font-normal text-base-content/60">%s</span>
				</td>
				<td class="text-right">
					<button class="btn btn-ghost btn-xs text-primary font-semibold">Inspect</button>
				</td>
			</tr>`, statusDot, name, group, badgeClass, statusText, float64(balance)/100.0, defaultCurr))
	}

	if !hasBuckets {
		bucketRows.WriteString(`
			<tr>
				<td colspan="4" class="text-center py-8 text-base-content/50">
					No buckets created yet. Create groups and envelopes to assign income.
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
    <title>Balance — %s</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col">
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
            <span class="badge badge-soft badge-primary font-semibold text-xs">%s</span>
        </div>
        <div class="navbar-end gap-3">
            <div class="badge badge-accent badge-soft font-bold py-3 text-xs gap-1.5">
                <span>⏳</span>
                <span>Age of Money: 28 days</span>
            </div>
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm font-medium">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <!-- Left Sidebar Navigation -->
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
            <ul class="menu menu-md w-full p-0 gap-1">
                <li><a href="/budgets/%s/budget" class="active font-bold">📂 1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/transactions">💳 2. Transactions</a></li>
                <li><a href="/budgets/%s/reports">📊 3. Reports</a></li>
                <li><a href="/budgets/%s/parties">🏢 4. External Parties</a></li>
                <li><a href="/budgets/%s/members">👥 5. Members</a></li>
            </ul>

            <div class="divider my-0"></div>

            <div class="px-2">
                <div class="text-xs font-bold uppercase tracking-wider text-base-content/50 mb-2">Accounts Summary</div>
                <div class="space-y-2">
                    <div class="flex justify-between items-center text-xs">
                        <span class="font-medium text-base-content/70">Checking Account</span>
                        <span class="font-mono font-bold">5,420.50 %s</span>
                    </div>
                    <div class="flex justify-between items-center text-xs">
                        <span class="font-medium text-base-content/70">Savings Reserve</span>
                        <span class="font-mono font-bold">12,000.00 %s</span>
                    </div>
                </div>
            </div>
        </aside>

        <!-- Center Workspace -->
        <main class="flex-1 p-6 md:p-8 overflow-y-auto">
            <!-- Top Stats & Ready to Assign Banner -->
            <div class="stats stats-horizontal shadow-md w-full bg-base-100 border border-base-300 mb-6">
                <div class="stat">
                    <div class="stat-figure text-success text-3xl">💵</div>
                    <div class="stat-title text-xs font-bold uppercase tracking-wider">Ready to Assign</div>
                    <div class="stat-value text-success text-2xl font-black">0.00 %s</div>
                    <div class="stat-desc text-xs">Unallocated funds in active window</div>
                </div>

                <div class="stat">
                    <div class="stat-title text-xs font-bold uppercase tracking-wider">Active Horizon</div>
                    <div class="stat-actions mt-2">
                        <div class="join">
                            <button class="btn btn-xs join-item btn-primary">Monthly</button>
                            <button class="btn btn-xs join-item btn-ghost">30d</button>
                            <button class="btn btn-xs join-item btn-ghost">90d</button>
                        </div>
                    </div>
                </div>

                <div class="stat">
                    <div class="stat-title text-xs font-bold uppercase tracking-wider">Quick Actions</div>
                    <div class="stat-actions mt-2">
                        <button class="btn btn-xs btn-outline btn-primary font-semibold">Auto-Assign by Priority</button>
                    </div>
                </div>
            </div>

            <!-- Buckets Table Card -->
            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra w-full">
                            <thead>
                                <tr class="bg-base-200/50">
                                    <th class="font-bold">Bucket / Category</th>
                                    <th class="font-bold">Health Status</th>
                                    <th class="font-bold text-right">Available Balance</th>
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
        <a href="/budgets/%s/budget" class="dock-active">
            <span class="text-xl">📂</span>
            <span class="dock-label">Budget</span>
        </a>
        <a href="/budgets/%s/transactions">
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
</body>
</html>`, budgetName, budgetName, role, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID,
		defaultCurr, defaultCurr,
		defaultCurr, bucketRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID)

	return c.HTML(http.StatusOK, html)
}
