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

// BudgetsList renders the post-login budget selection dashboard.
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
	for rows.Next() {
		var id, name, curr, tz, role string
		rows.Scan(&id, &name, &curr, &tz, &role)

		budgetCards.WriteString(fmt.Sprintf(`
			<div class="card bg-base-100 shadow border border-base-300 hover:border-primary transition">
				<div class="card-body">
					<div class="flex justify-between items-start">
						<h3 class="card-title text-xl font-bold">%s</h3>
						<span class="badge badge-outline">%s</span>
					</div>
					<p class="text-sm opacity-70">Currency: <span class="font-semibold">%s</span> | Timezone: %s</p>
					<div class="card-actions justify-end mt-4">
						<a href="/budgets/%s/budget" class="btn btn-primary btn-sm">Open Budget</a>
					</div>
				</div>
			</div>`, name, role, curr, tz, id))
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - My Budgets</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@latest/full.min.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 min-h-screen flex flex-col">
    <!-- Global Header -->
    <header class="navbar bg-base-100 border-b border-base-300 px-6">
        <div class="flex-1">
            <span class="text-xl font-bold tracking-tight text-primary">Balance</span>
        </div>
        <div class="flex-none gap-4">
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm">Log Out</button>
            </form>
        </div>
    </header>

    <!-- Main Content -->
    <main class="flex-1 max-w-5xl w-full mx-auto p-6">
        <div class="flex justify-between items-center mb-6">
            <div>
                <h1 class="text-3xl font-extrabold tracking-tight">Your Budgets</h1>
                <p class="text-sm opacity-70 mt-1">Select an active budget or create a new envelope workspace.</p>
            </div>
            <button class="btn btn-primary" onclick="new_budget_modal.showModal()">+ New Budget</button>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            %s
        </div>
    </main>

    <!-- New Budget Modal -->
    <dialog id="new_budget_modal" class="modal">
        <div class="modal-box">
            <h3 class="font-bold text-lg mb-4">Create New Budget</h3>
            <form method="POST" action="/budgets" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div>
                    <label class="label"><span class="label-text">Budget Name</span></label>
                    <input type="text" name="name" required placeholder="Personal & Family" class="input input-bordered w-full" />
                </div>
                <div class="grid grid-cols-2 gap-4">
                    <div>
                        <label class="label"><span class="label-text">Currency</span></label>
                        <select name="default_currency" class="select select-bordered w-full">
                            <option value="PLN" selected>PLN</option>
                            <option value="EUR">EUR</option>
                            <option value="USD">USD</option>
                            <option value="GBP">GBP</option>
                        </select>
                    </div>
                    <div>
                        <label class="label"><span class="label-text">Timezone</span></label>
                        <input type="text" name="timezone" value="Europe/Warsaw" required class="input input-bordered w-full" />
                    </div>
                </div>
                <div class="modal-action">
                    <button type="button" class="btn" onclick="new_budget_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary">Create</button>
                </div>
            </form>
        </div>
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

	// Lazy initialize tenant DB
	_, err = h.tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to initialize tenant database")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// BudgetView renders the YNAB-style Budget/Envelopes view.
func (h *WorkspaceHandler) BudgetView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Fetch budget metadata
	var budgetName, defaultCurr, tz string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency, timezone FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr, &tz)

	// Fetch buckets and targets
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
	for rows.Next() {
		var id, group, name string
		var balance int64
		rows.Scan(&id, &group, &name, &balance)

		// YNAB Health Status badge
		badgeClass := "badge-success text-white"
		statusText := "🟢 Funded"
		if balance < 0 {
			badgeClass = "badge-error text-white"
			statusText = "🔴 Overspent"
		} else if balance == 0 {
			badgeClass = "badge-warning"
			statusText = "🟡 Underfunded"
		}

		bucketRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="font-bold">%s</div>
					<div class="text-xs opacity-60">%s</div>
				</td>
				<td><span class="badge %s text-xs">%s</span></td>
				<td class="font-mono text-right">%.2f %s</td>
				<td class="text-right">
					<button class="btn btn-ghost btn-xs">Inspect</button>
				</td>
			</tr>`, name, group, badgeClass, statusText, float64(balance)/100.0, defaultCurr))
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - %s</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@latest/full.min.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 min-h-screen flex flex-col">
    <!-- Top Bar -->
    <header class="navbar bg-base-100 border-b border-base-300 px-6">
        <div class="flex-1 gap-4 items-center">
            <a href="/budgets" class="text-xl font-bold tracking-tight text-primary">Balance</a>
            <div class="badge badge-outline">%s</div>
            <div class="badge badge-ghost text-xs">Role: %s</div>
        </div>
        <div class="flex-none gap-4">
            <span class="badge badge-accent badge-sm">Age of Money: 28 days</span>
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <!-- Desktop Left Sidebar Navigation -->
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-2">
            <ul class="menu menu-md w-full p-0">
                <li><a href="/budgets/%s/budget" class="active font-bold">1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/transactions">2. Transactions</a></li>
                <li><a href="/budgets/%s/reports">3. Reports</a></li>
                <li><a href="/budgets/%s/parties">4. External Parties</a></li>
                <li><a href="/budgets/%s/members">5. Members</a></li>
            </ul>
        </aside>

        <!-- Center Workspace -->
        <main class="flex-1 p-6 overflow-y-auto">
            <!-- Top Horizon & Ready to Assign Banner -->
            <div class="card bg-base-100 shadow border border-base-300 mb-6">
                <div class="card-body p-4 flex flex-row justify-between items-center">
                    <div>
                        <div class="text-xs uppercase font-bold tracking-wider opacity-60">Ready to Assign</div>
                        <div class="text-3xl font-extrabold text-success">0.00 %s</div>
                    </div>
                    <div class="flex items-center gap-3">
                        <div class="join">
                            <button class="btn btn-sm join-item btn-active">Monthly</button>
                            <button class="btn btn-sm join-item">30d</button>
                            <button class="btn btn-sm join-item">90d</button>
                        </div>
                        <button class="btn btn-sm btn-outline btn-primary">Auto-Assign by Priority</button>
                    </div>
                </div>
            </div>

            <!-- Buckets Table -->
            <div class="overflow-x-auto bg-base-100 rounded-box border border-base-300 shadow">
                <table class="table table-zebra">
                    <thead>
                        <tr>
                            <th>Bucket Name</th>
                            <th>Health Status</th>
                            <th class="text-right">Available Balance</th>
                            <th class="text-right">Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        %s
                    </tbody>
                </table>
            </div>
        </main>
    </div>

    <!-- Mobile Bottom Navigation Dock (DaisyUI 5 dock) -->
    <div class="dock md:hidden">
        <a href="/budgets/%s/budget" class="active">Budget</a>
        <a href="/budgets/%s/transactions">Txns</a>
        <a href="/budgets/%s/reports">Reports</a>
        <a href="/budgets/%s/parties">Parties</a>
        <a href="/budgets/%s/members">Members</a>
    </div>
</body>
</html>`, budgetName, budgetName, role, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID,
		defaultCurr, bucketRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID)

	return c.HTML(http.StatusOK, html)
}
