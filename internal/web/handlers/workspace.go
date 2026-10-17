package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

type WorkspaceHandler struct {
	mainDB         *sql.DB
	tenantMgr      *db.TenantDBManager
	reportsHandler *ReportsHandler
}

func NewWorkspaceHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager, reportsHandler *ReportsHandler) *WorkspaceHandler {
	return &WorkspaceHandler{
		mainDB:         mainDB,
		tenantMgr:      tenantMgr,
		reportsHandler: reportsHandler,
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
	hasOwnerBudget := false
	for rows.Next() {
		hasBudgets = true
		var id, name, curr, tz, role string
		rows.Scan(&id, &name, &curr, &tz, &role)

		var ownerControls string
		if role == "OWNER" {
			hasOwnerBudget = true
			ownerControls = fmt.Sprintf(`
				<div class="dropdown dropdown-end">
					<div tabindex="0" role="button" class="btn btn-ghost btn-xs btn-square" title="Budget Options">
						<span>⋮</span>
					</div>
					<ul tabindex="0" class="dropdown-content menu bg-base-100 rounded-box z-10 w-44 p-2 shadow-lg border border-base-300 gap-1 text-xs">
						<li>
							<button type="button" class="flex items-center gap-2 py-1.5" onclick="openEditBudgetModal('%s', '%s')">
								<span>✏️</span>
								<span>Rename</span>
							</button>
						</li>
						<li>
							<form method="POST" action="/budgets/%s/delete" onsubmit="return confirm('Are you sure you want to delete budget \'%s\'? This action is permanent and deletes all ledger data.')" class="p-0">
								<input type="hidden" name="csrf_token" value="%s" />
								<button type="submit" class="flex items-center gap-2 text-error py-1.5 w-full hover:bg-error/10">
									<span>🗑️</span>
									<span>Delete Budget</span>
								</button>
							</form>
						</li>
					</ul>
				</div>
			`, id, html.EscapeString(name), id, html.EscapeString(name), c.Get("csrfToken"))
		}

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
						<div class="flex items-center gap-2">
							<span class="badge badge-soft badge-primary font-semibold text-xs">%s</span>
							%s
						</div>
					</div>
					<div class="divider my-0"></div>
					<div class="flex items-center justify-between">
						<div class="flex items-center gap-2">
							<span class="badge badge-neutral badge-sm">%s</span>
							<span class="text-xs text-base-content/60">Default Currency</span>
						</div>
						<div class="card-actions flex items-center gap-2">
							<a href="/budgets/%s/accounts" class="btn btn-outline btn-secondary btn-sm gap-1.5 shadow-xs font-semibold" title="Link Bank Account or manage accounts">
								<span>🏦</span>
								<span>Link Bank</span>
							</a>
							<a href="/budgets/%s/budget" class="btn btn-primary btn-sm gap-1.5 shadow-sm font-semibold">
								<span>Open</span>
								<span>➔</span>
							</a>
						</div>
					</div>
				</div>
			</div>`, html.EscapeString(name), html.EscapeString(tz), html.EscapeString(role), ownerControls, html.EscapeString(curr), id, id))
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

	var editBudgetModalHTML string
	if hasOwnerBudget {
		editBudgetModalHTML = fmt.Sprintf(`
    <!-- Edit Budget Modal -->
    <dialog id="edit_budget_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Rename Budget</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="edit_budget_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">New Budget Name</span></label>
                    <input type="text" id="edit_budget_name" name="name" required placeholder="Budget Name" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="edit_budget_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Save Changes</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
    <script>
        function openEditBudgetModal(budgetId, budgetName) {
            const form = document.getElementById('edit_budget_form');
            form.action = '/budgets/' + budgetId + '/update';
            const nameInput = document.getElementById('edit_budget_name');
            nameInput.value = budgetName;
            document.getElementById('edit_budget_modal').showModal();
        }
    </script>
`, csrfToken)
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Budgets</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
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

    %s

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
</html>`, csrfToken, budgetCards.String(), editBudgetModalHTML, csrfToken)

	return c.HTML(http.StatusOK, htmlContent)
}

// SettingsView renders the budget settings page (general settings, name, timezone, and danger zone).
func (h *WorkspaceHandler) SettingsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)

	var name, defaultCurrency, timezone string
	var createdAt, updatedAt int64
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT name, default_currency, timezone, created_at, updated_at
		FROM budgets WHERE id = ?`, budgetID).Scan(&name, &defaultCurrency, &timezone, &createdAt, &updatedAt)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	sidebarHTML := RenderSidebarNavigation(budgetID, "settings")
	bottomDockHTML := RenderBottomDock(budgetID, "settings")

	var dangerZoneHTML string
	if role == "OWNER" {
		dangerZoneHTML = fmt.Sprintf(`
			<div class="card card-border bg-base-100 border-error/30 shadow-md">
				<div class="card-body p-6">
					<h2 class="card-title text-error text-lg font-bold flex items-center gap-2">
						<span>⚠️</span>
						<span>Danger Zone</span>
					</h2>
					<p class="text-xs text-base-content/70">
						Permanently delete this budget envelope, ledger records, transactions, and account history.
						This action cannot be undone.
					</p>
					<div class="card-actions justify-end mt-4">
						<form method="POST" action="/budgets/%s/delete" onsubmit="return confirm('Are you absolutely sure you want to delete this budget? All data will be permanently wiped.')">
							<input type="hidden" name="csrf_token" value="%s" />
							<button type="submit" class="btn btn-error btn-sm shadow font-semibold">
								Delete Budget
							</button>
						</form>
					</div>
				</div>
			</div>
		`, budgetID, csrfToken)
	} else {
		dangerZoneHTML = `
			<div class="card card-border bg-base-100 border-base-300 shadow-sm opacity-70">
				<div class="card-body p-6">
					<h2 class="card-title text-base font-bold text-base-content/70">Danger Zone</h2>
					<p class="text-xs text-base-content/60">
						Only the budget OWNER can delete or destroy this workspace.
					</p>
				</div>
			</div>
		`
	}

	var editFormHTML string
	if role == "OWNER" {
		editFormHTML = fmt.Sprintf(`
			<form method="POST" action="/budgets/%s/settings" class="flex flex-col gap-4">
				<input type="hidden" name="csrf_token" value="%s" />
				<div class="form-control">
					<label class="label pb-1"><span class="label-text font-medium text-xs">Budget Name</span></label>
					<input type="text" name="name" value="%s" required class="input input-bordered w-full input-md focus:input-primary font-semibold" />
				</div>
				<div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
					<div class="form-control">
						<label class="label pb-1"><span class="label-text font-medium text-xs">Default Currency</span></label>
						<input type="text" value="%s" disabled class="input input-bordered w-full input-md bg-base-200 cursor-not-allowed font-mono text-xs" />
						<label class="label pt-1"><span class="label-text-alt text-base-content/50">Base currency is immutable once provisioned</span></label>
					</div>
					<div class="form-control">
						<label class="label pb-1"><span class="label-text font-medium text-xs">IANA Timezone</span></label>
						<input type="text" name="timezone" value="%s" class="input input-bordered w-full input-md focus:input-primary text-xs" />
					</div>
				</div>
				<div class="card-actions justify-end mt-4">
					<button type="submit" class="btn btn-primary btn-sm shadow font-semibold">Save Settings</button>
				</div>
			</form>
		`, budgetID, csrfToken, html.EscapeString(name), html.EscapeString(defaultCurrency), html.EscapeString(timezone))
	} else {
		editFormHTML = fmt.Sprintf(`
			<div class="flex flex-col gap-4">
				<div class="form-control">
					<label class="label pb-1"><span class="label-text font-medium text-xs">Budget Name</span></label>
					<input type="text" value="%s" disabled class="input input-bordered w-full input-md bg-base-200 cursor-not-allowed font-semibold" />
				</div>
				<div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
					<div class="form-control">
						<label class="label pb-1"><span class="label-text font-medium text-xs">Default Currency</span></label>
						<input type="text" value="%s" disabled class="input input-bordered w-full input-md bg-base-200 cursor-not-allowed font-mono text-xs" />
					</div>
					<div class="form-control">
						<label class="label pb-1"><span class="label-text font-medium text-xs">IANA Timezone</span></label>
						<input type="text" value="%s" disabled class="input input-bordered w-full input-md bg-base-200 cursor-not-allowed text-xs" />
					</div>
				</div>
				<div class="alert alert-info alert-soft text-xs mt-2">
					<span>ℹ️ You are viewing budget settings with <strong>%s</strong> permissions. Only the OWNER can update budget properties.</span>
				</div>
			</div>
		`, html.EscapeString(name), html.EscapeString(defaultCurrency), html.EscapeString(timezone), html.EscapeString(role))
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — %s Settings</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
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
        <main class="flex-1 p-6 md:p-8 overflow-y-auto max-w-4xl">
            <div class="mb-6">
                <h1 class="text-2xl font-black tracking-tight">Budget Settings</h1>
                <p class="text-xs text-base-content/60 mt-1">Configure workspace preferences, default timezones, and ledger parameters.</p>
            </div>

            <div class="space-y-6">
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <h2 class="card-title text-lg font-bold mb-2">General Information</h2>
                        %s
                    </div>
                </div>

                %s
            </div>
        </main>
    </div>

    %s
</body>
</html>`,
		html.EscapeString(name),
		html.EscapeString(name),
		html.EscapeString(role),
		csrfToken,
		sidebarHTML,
		editFormHTML,
		dangerZoneHTML,
		bottomDockHTML,
	)

	return c.HTML(http.StatusOK, htmlContent)
}

// UpdateBudget updates the budget name and settings in main.db (requires OWNER).
func (h *WorkspaceHandler) UpdateBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")
	budgetID := c.Param("budgetId")
	name := strings.TrimSpace(c.FormValue("name"))
	tz := strings.TrimSpace(c.FormValue("timezone"))

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Budget name cannot be empty")
	}

	now := time.Now().UTC().UnixMicro()
	var res sql.Result
	var err error
	if tz != "" {
		res, err = h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE budgets SET name = ?, timezone = ?, updated_at = ?
			WHERE id = ?`, name, tz, now, budgetID)
	} else {
		res, err = h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE budgets SET name = ?, updated_at = ?
			WHERE id = ?`, name, now, budgetID)
	}
	if err != nil {
		logger.Error(ctx, "Failed to update budget settings", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Database error")
	}

	rowsAff, err := res.RowsAffected()
	if err != nil || rowsAff == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
	}

	logger.Info(ctx, "Budget updated successfully", "budget_id", budgetID, "new_name", name)
	if strings.Contains(c.Request().URL.Path, "/settings") {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings", budgetID))
	}
	return c.Redirect(http.StatusSeeOther, "/budgets")
}

// DeleteBudget deletes the budget and its memberships from main.db, and deletes its tenant sqlite database (requires OWNER).
func (h *WorkspaceHandler) DeleteBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")
	budgetID := c.Param("budgetId")

	tx, err := h.mainDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Failed to begin transaction for budget deletion", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Database error")
	}
	defer tx.Rollback()

	// Clean up related records in main.db
	_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM budget_invitations WHERE budget_id = ?`, budgetID)
	_, _ = tx.ExecContext(c.Request().Context(), `DELETE FROM budget_members WHERE budget_id = ?`, budgetID)
	res, err := tx.ExecContext(c.Request().Context(), `DELETE FROM budgets WHERE id = ?`, budgetID)
	if err != nil {
		logger.Error(ctx, "Failed to delete budget record", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete budget")
	}

	rowsAff, err := res.RowsAffected()
	if err != nil || rowsAff == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Failed committing budget deletion", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	// Delete tenant SQLite database file and evict from LRU cache
	if err := h.tenantMgr.DeleteTenantDB(budgetID); err != nil {
		logger.Warn(ctx, "Failed deleting tenant db file from disk", "budget_id", budgetID, "error", err.Error())
	}

	logger.Info(ctx, "Budget deleted successfully", "budget_id", budgetID)
	return c.Redirect(http.StatusSeeOther, "/budgets")
}


// CreateBudget creates a new budget in main.db and provisions the tenant SQLite database.
func (h *WorkspaceHandler) CreateBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")

	userID := c.Get(ContextUserIDKey).(string)
	name := c.FormValue("name")
	curr := c.FormValue("default_currency")
	tz := c.FormValue("timezone")

	if name == "" || curr == "" {
		logger.Warn(ctx, "Budget creation failed validation: name and currency required", "user_id", userID)
		return echo.NewHTTPError(http.StatusBadRequest, "Budget name and currency are required")
	}
	if tz == "" {
		tz = "UTC"
	}

	budgetID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	tx, err := h.mainDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Failed starting transaction for budget creation", "user_id", userID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Database error")
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		budgetID, name, curr, tz, userID, now, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed to insert budget into main.db", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create budget")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO budget_members (budget_id, user_id, role, joined_at)
		VALUES (?, ?, 'OWNER', ?)`,
		budgetID, userID, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed to assign budget owner", "budget_id", budgetID, "user_id", userID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to assign budget owner")
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Failed committing budget creation transaction", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	// Provision tenant DB
	_, err = h.tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		logger.Error(ctx, "Failed initializing tenant database for budget", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to initialize tenant database")
	}

	logger.Info(ctx, "New budget provisioned successfully", "budget_id", budgetID, "name", name, "currency", curr, "timezone", tz, "owner_id", userID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

type bucketViewData struct {
	ID           string
	GroupName    string
	Name         string
	BalanceMinor int64
	Currency     string
	Targets      []targetViewData
}

type targetViewData struct {
	ID                  string
	BucketID            string
	Name                string
	TargetType          string
	Priority            int
	SortOrder           int
	AmountMinor         int64
	Currency            string
	RecurrenceCadence   string
	RecurrenceDays      int
	DueDate             int64
	NonSpendingStrategy string
	CeilingAmountMinor  *int64
	CurrentFundedMinor  int64
	RequiredMinor       int64
	HealthBadge         string
	HealthBadgeClass    string
}

// BudgetView renders the YNAB-style Budget/Envelopes view with DaisyUI 5 stats, tables, and badges.
func (h *WorkspaceHandler) BudgetView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var budgetName, defaultCurr, tz string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency, timezone FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr, &tz)

	// Horizon parameter (e.g. 30, 90, or 30 for monthly)
	horizonParam := c.QueryParam("horizon")
	if horizonParam == "" {
		horizonParam = "monthly"
	}
	windowDays := 30
	if horizonParam == "90d" {
		windowDays = 90
	} else if horizonParam == "30d" {
		windowDays = 30
	}

	// 1. Calculate Age of Money dynamically
	aomHeadline := 0
	if h.reportsHandler != nil {
		if aomResult, _ := h.reportsHandler.CalculateAgeOfMoneyForBudget(c.Request().Context(), tenantDB); aomResult != nil {
			aomHeadline = aomResult.HeadlineDays
		}
	}

	// 2. Calculate Ready to Assign dynamically
	rtaMap, err := budget.CalculateReadyToAssign(c.Request().Context(), tenantDB)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed calculating ready to assign", "error", err.Error())
		rtaMap = make(map[string]int64)
	}
	defaultRTA := rtaMap[defaultCurr]

	var rtaOtherBadges strings.Builder
	for curr, amt := range rtaMap {
		if curr != defaultCurr && amt != 0 {
			badgeColor := "badge-info"
			if amt < 0 {
				badgeColor = "badge-error"
			}
			rtaOtherBadges.WriteString(fmt.Sprintf(`<span class="badge %s badge-sm font-bold ml-1">%.2f %s</span>`, badgeColor, float64(amt)/100.0, curr))
		}
	}

	// 3. Fetch all active targets
	nowUTC := time.Now().UTC()
	targetRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, bucket_id, name, target_type, priority, sort_order, amount_minor, currency,
		       recurrence_cadence, recurrence_days, due_date, non_spending_strategy,
		       ceiling_amount_minor, current_funded_minor
		FROM targets_projection
		WHERE is_archived = 0
		ORDER BY priority DESC, due_date ASC, sort_order ASC`)
	targetsByBucket := make(map[string][]targetViewData)
	if err == nil {
		defer targetRows.Close()
		for targetRows.Next() {
			var t targetViewData
			var dueDate sql.NullInt64
			var ceiling sql.NullInt64
			targetRows.Scan(&t.ID, &t.BucketID, &t.Name, &t.TargetType, &t.Priority, &t.SortOrder,
				&t.AmountMinor, &t.Currency, &t.RecurrenceCadence, &t.RecurrenceDays,
				&dueDate, &t.NonSpendingStrategy, &ceiling, &t.CurrentFundedMinor)
			if dueDate.Valid {
				t.DueDate = dueDate.Int64
			}
			if ceiling.Valid {
				cVal := ceiling.Int64
				t.CeilingAmountMinor = &cVal
			}

			tDef := &budget.TargetDefinition{
				ID:                  t.ID,
				BucketID:            t.BucketID,
				Name:                t.Name,
				Priority:            t.Priority,
				SortOrder:           t.SortOrder,
				AmountMinor:         t.AmountMinor,
				Currency:            t.Currency,
				RecurrenceCadence:   t.RecurrenceCadence,
				RecurrenceDays:      t.RecurrenceDays,
				DueDate:             t.DueDate,
				NonSpendingStrategy: t.NonSpendingStrategy,
				CeilingAmountMinor:  t.CeilingAmountMinor,
				CurrentFundedMinor:  t.CurrentFundedMinor,
			}
			t.RequiredMinor = budget.ComputeTargetRequirement(tDef, windowDays, nowUTC)

			// Health evaluation
			if t.CurrentFundedMinor < 0 {
				t.HealthBadge = "Overspent"
				t.HealthBadgeClass = "badge-error text-error-content"
			} else if t.CurrentFundedMinor >= t.RequiredMinor && t.RequiredMinor > 0 {
				t.HealthBadge = "Funded / On Track"
				t.HealthBadgeClass = "badge-success text-success-content"
			} else if t.CurrentFundedMinor < t.RequiredMinor {
				t.HealthBadge = "Underfunded"
				t.HealthBadgeClass = "badge-warning text-warning-content"
			} else {
				t.HealthBadge = "Funded / On Track"
				t.HealthBadgeClass = "badge-success text-success-content"
			}

			targetsByBucket[t.BucketID] = append(targetsByBucket[t.BucketID], t)
		}
	}

	// 4. Fetch buckets and balances
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

	var buckets []bucketViewData
	for rows.Next() {
		var b bucketViewData
		rows.Scan(&b.ID, &b.GroupName, &b.Name, &b.BalanceMinor)
		b.Currency = defaultCurr
		b.Targets = targetsByBucket[b.ID]
		buckets = append(buckets, b)
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	var bucketRows strings.Builder
	for _, b := range buckets {
		// Calculate bucket overall health
		var totalRequired int64
		for _, t := range b.Targets {
			if t.Currency == defaultCurr {
				totalRequired += t.RequiredMinor
			}
		}

		badgeClass := "badge-success text-success-content"
		statusText := "Funded / On Track"
		statusDot := "bg-success"
		if b.BalanceMinor < 0 {
			badgeClass = "badge-error text-error-content"
			statusText = "Overspent"
			statusDot = "bg-error"
		} else if totalRequired > 0 && b.BalanceMinor < totalRequired {
			badgeClass = "badge-warning text-warning-content"
			statusText = "Underfunded"
			statusDot = "bg-warning"
		} else if totalRequired == 0 && b.BalanceMinor == 0 {
			badgeClass = "badge-ghost text-base-content/60"
			statusText = "Zero Balance"
			statusDot = "bg-base-300"
		}

		var targetsSublist strings.Builder
		if len(b.Targets) > 0 {
			targetsSublist.WriteString(`<div class="mt-2 space-y-1 pl-4 border-l-2 border-base-300 text-xs">`)
			for _, t := range b.Targets {
				priorityLabel := "Optional"
				if t.Priority == 3 {
					priorityLabel = "Mandatory"
				} else if t.Priority == 2 {
					priorityLabel = "Recommended"
				}
				targetsSublist.WriteString(fmt.Sprintf(`
					<div class="flex items-center justify-between text-base-content/70 py-0.5">
						<span class="font-medium">• %s <span class="opacity-50 text-[10px]">(%s, %s)</span></span>
						<div class="flex items-center gap-2">
							<span class="badge %s badge-xs">%s</span>
							<span class="font-mono">%.2f / %.2f %s</span>
						</div>
					</div>`,
					html.EscapeString(t.Name), priorityLabel, t.RecurrenceCadence,
					t.HealthBadgeClass, t.HealthBadge,
					float64(t.CurrentFundedMinor)/100.0, float64(t.RequiredMinor)/100.0, t.Currency))
			}
			targetsSublist.WriteString(`</div>`)
		}

		bucketRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="flex flex-col">
						<div class="flex items-center gap-3">
							<span class="w-2.5 h-2.5 rounded-full %s"></span>
							<div>
								<div class="font-bold text-sm">%s</div>
								<div class="text-xs text-base-content/50">%s (%d targets)</div>
							</div>
						</div>
						%s
					</div>
				</td>
				<td>
					<span class="badge %s badge-sm font-semibold">%s</span>
				</td>
				<td class="font-mono text-right font-bold text-sm">
					%.2f <span class="text-xs font-normal text-base-content/60">%s</span>
				</td>
				<td class="text-right">
					<div class="flex items-center justify-end gap-1.5">
						<button class="btn btn-ghost btn-xs text-primary font-semibold" onclick="openBucketDrawer('%s', '%s', '%s', '%.2f', '%s')">Inspect</button>
						%s
					</div>
				</td>
			</tr>`,
			statusDot, html.EscapeString(b.Name), html.EscapeString(b.GroupName), len(b.Targets),
			targetsSublist.String(),
			badgeClass, statusText,
			float64(b.BalanceMinor)/100.0, b.Currency,
			b.ID, strings.ReplaceAll(b.Name, "'", "\\'"), strings.ReplaceAll(b.GroupName, "'", "\\'"), float64(b.BalanceMinor)/100.0, b.Currency,
			RenderPopoverActionMenu("bucket", b.ID, []TableActionItem{
				{
					Label:   "✏️ Edit Bucket",
					OnClick: fmt.Sprintf("openEditBucketModal('%s', '%s', '%s')", b.ID, strings.ReplaceAll(b.Name, "'", "\\'"), strings.ReplaceAll(b.GroupName, "'", "\\'")),
				},
				{
					Label:      "💵 Assign Funds",
					OnClick:    fmt.Sprintf("openAssignModal('%s', '%s', '%s')", b.ID, strings.ReplaceAll(b.Name, "'", "\\'"), b.Currency),
				},
				{
					Label:      "↔️ Move Money",
					OnClick:    fmt.Sprintf("openMoveModal('%s', '%s', '%s')", b.ID, strings.ReplaceAll(b.Name, "'", "\\'"), b.Currency),
				},
				{
					Label:      "📁 Archive Bucket",
					FormAction: fmt.Sprintf("/budgets/%s/buckets/%s/archive", budgetID, b.ID),
					CSRFToken:  csrfToken,
					IsDanger:   true,
					ConfirmMsg: "Archive this bucket? It must have zero balance and zero targets.",
				},
			}),
		))
	}

	if len(buckets) == 0 {
		bucketRows.WriteString(`
			<tr>
				<td colspan="4" class="text-center py-8 text-base-content/50">
					No buckets created yet. Create groups and envelopes to assign income.
				</td>
			</tr>
		`)
	}

	// Fetch user-owned accounts and balances for sidebar summary
	accRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.name, COALESCE(acb.balance_minor, 0), a.primary_currency,
		       COALESCE(a.favicon_type, 'EMOJI'), COALESCE(a.favicon_value, '')
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN account_currency_balances acb ON a.id = acb.account_id AND a.primary_currency = acb.currency
		WHERE p.is_user_owned = 1 AND a.is_closed = 0
		ORDER BY a.name ASC`)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed querying accounts for workspace summary", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading accounts")
	}
	defer accRows.Close()

	var accountSummaryRows strings.Builder
	hasAccounts := false
	for accRows.Next() {
		hasAccounts = true
		var aName, aCurr, fType, fVal string
		var aBalance int64
		accRows.Scan(&aName, &aBalance, &aCurr, &fType, &fVal)

		iconEl := ""
		if fType == "URL" && fVal != "" {
			iconEl = fmt.Sprintf(`<img src="%s" class="w-4 h-4 object-contain inline-block shrink-0 rounded" onerror="this.outerHTML='💳'" />`, fVal)
		} else {
			emojiChar := fVal
			if emojiChar == "" {
				emojiChar = "💳"
			}
			iconEl = fmt.Sprintf(`<span class="text-sm shrink-0 select-none">%s</span>`, emojiChar)
		}

		accountSummaryRows.WriteString(fmt.Sprintf(`
			<div class="flex justify-between items-center text-xs gap-2">
				<div class="flex items-center gap-1.5 min-w-0">
					%s
					<span class="font-medium text-base-content/70 truncate max-w-[120px]" title="%s">%s</span>
				</div>
				<span class="font-mono font-bold shrink-0">%.2f %s</span>
			</div>`, iconEl, aName, aName, float64(aBalance)/100.0, aCurr))
	}

	if !hasAccounts {
		accountSummaryRows.WriteString(fmt.Sprintf(`
			<div class="text-xs text-base-content/50 py-1">
				<p>No accounts connected yet.</p>
				<a href="/budgets/%s/accounts" class="btn btn-xs btn-outline btn-primary mt-2 w-full">+ Add Account</a>
			</div>
		`, budgetID))
	}

	// Active Horizon Join button classes
	monthlyActive := "btn-ghost"
	d30Active := "btn-ghost"
	d90Active := "btn-ghost"
	switch horizonParam {
	case "30d":
		d30Active = "btn-primary"
	case "90d":
		d90Active = "btn-primary"
	default:
		monthlyActive = "btn-primary"
	}

	// Other bucket options for Move Money modal
	var bucketSelectOptions strings.Builder
	for _, b := range buckets {
		bucketSelectOptions.WriteString(fmt.Sprintf(`<option value="%s">%s (%s)</option>`, b.ID, html.EscapeString(b.Name), html.EscapeString(b.GroupName)))
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — %s</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
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
                <span>Age of Money: %d days</span>
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
            %s

            <div class="divider my-0"></div>

            <div class="px-2">
                <div class="text-xs font-bold uppercase tracking-wider text-base-content/50 mb-2">Accounts Summary</div>
                <div class="space-y-2">
                    %s
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
                    <div class="stat-value text-success text-2xl font-black">
                        %.2f %s
                        %s
                    </div>
                    <div class="stat-desc text-xs">Unallocated liquid funds across budget accounts</div>
                </div>

                <div class="stat">
                    <div class="stat-title text-xs font-bold uppercase tracking-wider">Active Horizon</div>
                    <div class="stat-actions mt-2">
                        <div class="join">
                            <a href="/budgets/%s/budget?horizon=monthly" class="btn btn-xs join-item %s">Monthly</a>
                            <a href="/budgets/%s/budget?horizon=30d" class="btn btn-xs join-item %s">30d</a>
                            <a href="/budgets/%s/budget?horizon=90d" class="btn btn-xs join-item %s">90d</a>
                        </div>
                    </div>
                </div>

                <div class="stat">
                    <div class="stat-title text-xs font-bold uppercase tracking-wider">Quick Actions</div>
                    <div class="stat-actions mt-2">
                        <form method="POST" action="/budgets/%s/budget/auto-assign" class="inline">
                            <input type="hidden" name="csrf_token" value="%s" />
                            <input type="hidden" name="currency" value="%s" />
                            <input type="hidden" name="horizon" value="%s" />
                            <button type="submit" class="btn btn-xs btn-outline btn-primary font-semibold shadow-xs">Auto-Assign by Priority</button>
                        </form>
                    </div>
                </div>
            </div>

            <!-- Buckets Table Header Toolbar -->
            <div class="flex justify-between items-center mb-4">
                <h2 class="text-xl font-bold tracking-tight">Envelopes & Categories</h2>
                <button class="btn btn-primary btn-sm gap-1.5 shadow font-semibold" onclick="new_bucket_modal.showModal()">
                    <span>+</span>
                    <span>New Bucket</span>
                </button>
            </div>

            <!-- Buckets Table Card -->
            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra w-full">
                            <thead>
                                <tr class="bg-base-200/50">
                                    <th class="font-bold">Bucket / Category & Targets</th>
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

    %s

    <!-- New Bucket Modal -->
    <dialog id="new_bucket_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Create New Bucket</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/buckets" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Bucket Name</span></label>
                    <input type="text" name="name" required placeholder="e.g. Groceries, Living Expenses" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Group / Category</span></label>
                    <input type="text" name="group_name" value="Needs" required placeholder="e.g. Needs, Wants, Savings" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="new_bucket_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Create Bucket</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Edit Bucket Modal -->
    <dialog id="edit_bucket_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Edit Bucket</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="edit_bucket_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Bucket Name</span></label>
                    <input type="text" id="edit_bucket_name" name="name" required class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Group / Category</span></label>
                    <input type="text" id="edit_bucket_group" name="group_name" required class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="edit_bucket_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Save Changes</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Assign Funds Modal -->
    <dialog id="assign_funds_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Assign Funds to Envelope</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="assign_funds_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" id="assign_currency" name="currency" value="%s" />
                <div class="text-xs text-base-content/70">
                    Assign funds directly from Ready to Assign into <span id="assign_bucket_name" class="font-bold text-primary"></span>.
                    (Enter negative amount to return funds back to pool).
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Amount</span></label>
                    <div class="join w-full">
                        <input type="number" step="0.01" name="amount" required placeholder="100.00" class="input input-bordered join-item w-full input-md focus:input-primary font-mono" />
                        <span id="assign_currency_label" class="btn join-item btn-neutral no-animation">%s</span>
                    </div>
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="assign_funds_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Assign Funds</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Move Funds Modal -->
    <dialog id="move_funds_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Move Funds Between Envelopes</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/buckets/move" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" id="move_source_id" name="source_bucket_id" value="" />
                <input type="hidden" id="move_currency" name="currency" value="%s" />
                <div class="text-xs text-base-content/70">
                    Move money out of <span id="move_source_name" class="font-bold text-primary"></span> into another envelope.
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Destination Envelope</span></label>
                    <select name="dest_bucket_id" required class="select select-bordered w-full select-md focus:select-primary">
                        %s
                    </select>
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Amount to Move</span></label>
                    <div class="join w-full">
                        <input type="number" step="0.01" min="0.01" name="amount" required placeholder="50.00" class="input input-bordered join-item w-full input-md focus:input-primary font-mono" />
                        <span id="move_currency_label" class="btn join-item btn-neutral no-animation">%s</span>
                    </div>
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="move_funds_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Move Funds</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Bucket Inspector Drawer / Modal -->
    <dialog id="bucket_drawer" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6 max-w-2xl">
            <div class="flex justify-between items-start mb-4">
                <div>
                    <h3 id="drawer_bucket_name" class="font-extrabold text-2xl tracking-tight"></h3>
                    <div id="drawer_bucket_meta" class="text-xs text-base-content/60 mt-0.5"></div>
                </div>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>

            <div class="stats stats-horizontal shadow-sm w-full bg-base-200/60 border border-base-300 mb-6">
                <div class="stat p-4">
                    <div class="stat-title text-[10px] font-bold uppercase tracking-wider">Available Envelope Balance</div>
                    <div id="drawer_bucket_balance" class="stat-value text-xl font-mono font-black text-primary"></div>
                </div>
            </div>

            <!-- Target Management Section -->
            <div class="flex justify-between items-center mb-3">
                <h4 class="font-bold text-sm uppercase tracking-wider text-base-content/70">Targets in this Bucket</h4>
                <button class="btn btn-xs btn-primary gap-1 font-semibold" onclick="openNewTargetModal()">+ Add Target</button>
            </div>

            <div id="drawer_targets_container" class="space-y-3 mb-6">
                <!-- Populated dynamically via JS -->
            </div>

            <div class="modal-action">
                <button type="button" class="btn btn-ghost btn-sm" onclick="bucket_drawer.close()">Close</button>
            </div>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- Create Target Modal -->
    <dialog id="target_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 id="target_modal_title" class="font-extrabold text-xl tracking-tight">Add Target</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="target_form" method="POST" action="/budgets/%s/targets" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" id="target_bucket_id" name="bucket_id" value="" />
                <input type="hidden" id="target_currency" name="currency" value="%s" />

                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Target Name</span></label>
                    <input type="text" id="target_name" name="name" required placeholder="e.g. Monthly Rent, Electricity" class="input input-bordered w-full input-md focus:input-primary" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Priority</span></label>
                        <select id="target_priority" name="priority" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="3" selected>Mandatory (3)</option>
                            <option value="2">Recommended (2)</option>
                            <option value="1">Optional (1)</option>
                        </select>
                    </div>
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Cadence</span></label>
                        <select id="target_cadence" name="recurrence_cadence" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="MONTHLY" selected>Monthly</option>
                            <option value="WEEKLY">Weekly</option>
                            <option value="ANNUAL">Annual</option>
                            <option value="ONE_OFF">One-Off</option>
                            <option value="CUSTOM_DAYS">Custom Days</option>
                        </select>
                    </div>
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Required Amount</span></label>
                        <input type="number" step="0.01" min="0" id="target_amount" name="amount" required placeholder="2000.00" class="input input-bordered w-full input-md focus:input-primary font-mono" />
                    </div>
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Non-Spending Strategy</span></label>
                        <select id="target_strategy" name="non_spending_strategy" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="ROLL_OVER" selected>Roll Over</option>
                            <option value="RETURN_TO_POOL">Return to Pool</option>
                            <option value="ARREARS_CUMULATIVE">Arrears Cumulative</option>
                            <option value="CAP_AND_HOLD">Cap and Hold</option>
                            <option value="PRORATE_FUTURE">Prorate Future</option>
                        </select>
                    </div>
                </div>

                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="target_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Save Target</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <script>
        const currentBudgetID = '%s';
        const csrfTokenVal = '%s';
        const bucketsData = %s;

        function openEditBucketModal(id, name, group) {
            document.getElementById('edit_bucket_form').action = '/budgets/' + currentBudgetID + '/buckets/' + id + '/update';
            document.getElementById('edit_bucket_name').value = name;
            document.getElementById('edit_bucket_group').value = group;
            edit_bucket_modal.showModal();
        }

        function openAssignModal(id, name, curr) {
            document.getElementById('assign_funds_form').action = '/budgets/' + currentBudgetID + '/buckets/' + id + '/assign';
            document.getElementById('assign_bucket_name').innerText = name;
            document.getElementById('assign_currency').value = curr;
            document.getElementById('assign_currency_label').innerText = curr;
            assign_funds_modal.showModal();
        }

        function openMoveModal(id, name, curr) {
            document.getElementById('move_source_id').value = id;
            document.getElementById('move_source_name').innerText = name;
            document.getElementById('move_currency').value = curr;
            document.getElementById('move_currency_label').innerText = curr;
            move_funds_modal.showModal();
        }

        let activeDrawerBucketID = '';

        function openBucketDrawer(id, name, group, bal, curr) {
            activeDrawerBucketID = id;
            document.getElementById('drawer_bucket_name').innerText = name;
            document.getElementById('drawer_bucket_meta').innerText = 'Group: ' + group + ' • ID: ' + id;
            document.getElementById('drawer_bucket_balance').innerText = bal + ' ' + curr;

            const b = bucketsData.find(x => x.ID === id);
            const container = document.getElementById('drawer_targets_container');
            container.innerHTML = '';

            if (b && b.Targets && b.Targets.length > 0) {
                b.Targets.forEach(t => {
                    const card = document.createElement('div');
                    card.className = 'card card-border bg-base-100 border-base-300 shadow-xs p-4 flex flex-col gap-2';
                    const priorityText = t.Priority === 3 ? 'Mandatory' : (t.Priority === 2 ? 'Recommended' : 'Optional');
                    card.innerHTML = 
                        '<div class="flex justify-between items-start">' +
                            '<div>' +
                                '<div class="font-bold text-sm tracking-tight">' + t.Name + '</div>' +
                                '<div class="text-[11px] text-base-content/60">Priority: ' + priorityText + ' • Cadence: ' + t.RecurrenceCadence + ' • Strategy: ' + t.NonSpendingStrategy + '</div>' +
                            '</div>' +
                            '<span class="badge ' + t.HealthBadgeClass + ' badge-sm font-semibold">' + t.HealthBadge + '</span>' +
                        '</div>' +
                        '<div class="flex justify-between items-center text-xs mt-1">' +
                            '<span class="font-mono">Funded: ' + (t.CurrentFundedMinor/100).toFixed(2) + ' / Required: ' + (t.RequiredMinor/100).toFixed(2) + ' ' + t.Currency + '</span>' +
                            '<form method="POST" action="/budgets/' + currentBudgetID + '/targets/' + t.ID + '/delete" onsubmit="return confirm(\'Delete this target?\')">' +
                                '<input type="hidden" name="csrf_token" value="' + csrfTokenVal + '" />' +
                                '<button type="submit" class="btn btn-ghost btn-xs text-error font-semibold">Delete</button>' +
                            '</form>' +
                        '</div>';
                    container.appendChild(card);
                });
            } else {
                container.innerHTML = '<div class="text-xs text-base-content/50 py-3 text-center border border-dashed border-base-300 rounded-lg">No targets added to this envelope yet.</div>';
            }

            bucket_drawer.showModal();
        }

        function openNewTargetModal() {
            document.getElementById('target_form').action = '/budgets/' + currentBudgetID + '/targets';
            document.getElementById('target_bucket_id').value = activeDrawerBucketID;
            document.getElementById('target_name').value = '';
            document.getElementById('target_amount').value = '';
            target_modal.showModal();
        }
    </script>
</body>
</html>`,
		budgetName, budgetName, role, aomHeadline, csrfToken,
		RenderSidebarNavigation(budgetID, "budget"),
		accountSummaryRows.String(),
		float64(defaultRTA)/100.0, defaultCurr, rtaOtherBadges.String(),
		budgetID, monthlyActive, budgetID, d30Active, budgetID, d90Active,
		budgetID, csrfToken, defaultCurr, horizonParam,
		bucketRows.String(),
		RenderBottomDock(budgetID, "budget"),
		budgetID, csrfToken,
		csrfToken,
		csrfToken, defaultCurr, defaultCurr,
		budgetID, csrfToken, defaultCurr, bucketSelectOptions.String(), defaultCurr,
		budgetID, csrfToken, defaultCurr,
		budgetID, csrfToken, toJSONString(buckets))

	return c.HTML(http.StatusOK, htmlContent)
}

func toJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// CreateBucket handles creating an envelope bucket.
func (h *WorkspaceHandler) CreateBucket(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	groupName := strings.TrimSpace(c.FormValue("group_name"))
	if groupName == "" {
		groupName = "Needs"
	}

	_, err := budget.CreateBucket(c.Request().Context(), tenantDB, budget.CreateBucketInput{
		GroupName:     groupName,
		Name:          name,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// UpdateBucket handles updating bucket name or group.
func (h *WorkspaceHandler) UpdateBucket(c echo.Context) error {
	budgetID := c.Param("budgetId")
	bucketID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	groupName := strings.TrimSpace(c.FormValue("group_name"))

	err := budget.UpdateBucket(c.Request().Context(), tenantDB, budget.UpdateBucketInput{
		ID:            bucketID,
		GroupName:     groupName,
		Name:          name,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// ArchiveBucket archives a bucket if it has zero balance and zero targets.
func (h *WorkspaceHandler) ArchiveBucket(c echo.Context) error {
	budgetID := c.Param("budgetId")
	bucketID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	err := budget.ArchiveBucket(c.Request().Context(), tenantDB, bucketID, userID, corrID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// AssignFunds assigns funds from ready to assign to bucket.
func (h *WorkspaceHandler) AssignFunds(c echo.Context) error {
	budgetID := c.Param("budgetId")
	bucketID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	currency := c.FormValue("currency")
	if currency == "" {
		currency = "PLN"
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	var targetID *string
	tID := c.FormValue("target_id")
	if tID != "" {
		targetID = &tID
	}

	err := budget.AssignFunds(c.Request().Context(), tenantDB, budget.AssignFundsInput{
		BucketID:      bucketID,
		TargetID:      targetID,
		Currency:      currency,
		AmountMinor:   amtMinor,
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// MoveFunds moves funds between buckets.
func (h *WorkspaceHandler) MoveFunds(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	srcID := c.FormValue("source_bucket_id")
	dstID := c.FormValue("dest_bucket_id")
	currency := c.FormValue("currency")
	if currency == "" {
		currency = "PLN"
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	err := budget.MoveFunds(c.Request().Context(), tenantDB, budget.MoveFundsInput{
		SourceBucketID: srcID,
		DestBucketID:   dstID,
		Currency:       currency,
		AmountMinor:    amtMinor,
		AgentID:        userID,
		CorrelationID:  corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// AutoAssign triggers priority waterfall auto-assign from available pool.
func (h *WorkspaceHandler) AutoAssign(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	currency := c.FormValue("currency")
	if currency == "" {
		var defaultCurr string
		h.mainDB.QueryRowContext(c.Request().Context(), `SELECT default_currency FROM budgets WHERE id = ?`, budgetID).Scan(&defaultCurr)
		currency = defaultCurr
	}
	horizon := c.FormValue("horizon")
	windowDays := 30
	if horizon == "90d" {
		windowDays = 90
	}

	rtaMap, err := budget.CalculateReadyToAssign(c.Request().Context(), tenantDB)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	pool := rtaMap[currency]
	if pool <= 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget?horizon=%s", budgetID, horizon))
	}

	nowUTC := time.Now().UTC()
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT t.id, t.bucket_id, t.name, t.priority, t.sort_order, t.amount_minor, t.currency,
		       t.recurrence_cadence, t.recurrence_days, t.due_date, t.non_spending_strategy,
		       t.ceiling_amount_minor, t.current_funded_minor
		FROM targets_projection t
		JOIN buckets_projection b ON t.bucket_id = b.id
		WHERE t.currency = ? AND t.is_archived = 0 AND b.is_archived = 0
		ORDER BY t.priority DESC, t.due_date ASC, t.sort_order ASC`, currency)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	defer rows.Close()

	var targets []*budget.TargetDefinition
	for rows.Next() {
		var t budget.TargetDefinition
		var dueDate sql.NullInt64
		var ceiling sql.NullInt64
		rows.Scan(&t.ID, &t.BucketID, &t.Name, &t.Priority, &t.SortOrder, &t.AmountMinor,
			&t.Currency, &t.RecurrenceCadence, &t.RecurrenceDays, &dueDate,
			&t.NonSpendingStrategy, &ceiling, &t.CurrentFundedMinor)
		if dueDate.Valid {
			t.DueDate = dueDate.Int64
		}
		if ceiling.Valid {
			cVal := ceiling.Int64
			t.CeilingAmountMinor = &cVal
		}
		targets = append(targets, &t)
	}

	allocs, _ := budget.PriorityAutoAssign(targets, windowDays, nowUTC, pool)
	for _, alloc := range allocs {
		if alloc.Assigned > 0 {
			// Find bucket for this target
			var bucketID string
			for _, t := range targets {
				if t.ID == alloc.TargetID {
					bucketID = t.BucketID
					break
				}
			}
			tID := alloc.TargetID
			_ = budget.AssignFunds(c.Request().Context(), tenantDB, budget.AssignFundsInput{
				BucketID:      bucketID,
				TargetID:      &tID,
				Currency:      currency,
				AmountMinor:   alloc.Assigned,
				AgentID:       userID,
				CorrelationID: corrID,
			})
		}
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget?horizon=%s", budgetID, horizon))
}

// CreateTarget creates a new target in a bucket.
func (h *WorkspaceHandler) CreateTarget(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	bucketID := c.FormValue("bucket_id")
	name := strings.TrimSpace(c.FormValue("name"))
	tType := c.FormValue("target_type")
	if tType == "" {
		tType = "EXPENSE"
	}
	priority, _ := strconv.Atoi(c.FormValue("priority"))
	if priority < 1 {
		priority = 2
	}
	currency := c.FormValue("currency")
	if currency == "" {
		currency = "PLN"
	}
	cadence := c.FormValue("recurrence_cadence")
	if cadence == "" {
		cadence = budget.CadenceMonthly
	}
	strategy := c.FormValue("non_spending_strategy")
	if strategy == "" {
		strategy = budget.StrategyRollOver
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	_, err := budget.CreateTarget(c.Request().Context(), tenantDB, budget.CreateTargetInput{
		BucketID:            bucketID,
		Name:                name,
		TargetType:          tType,
		Priority:            priority,
		AmountMinor:         amtMinor,
		Currency:            currency,
		RecurrenceCadence:   cadence,
		NonSpendingStrategy: strategy,
		AgentID:             userID,
		CorrelationID:       corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// UpdateTarget updates an existing target.
func (h *WorkspaceHandler) UpdateTarget(c echo.Context) error {
	budgetID := c.Param("budgetId")
	targetID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	tType := c.FormValue("target_type")
	if tType == "" {
		tType = "EXPENSE"
	}
	priority, _ := strconv.Atoi(c.FormValue("priority"))
	if priority < 1 {
		priority = 2
	}
	cadence := c.FormValue("recurrence_cadence")
	if cadence == "" {
		cadence = budget.CadenceMonthly
	}
	strategy := c.FormValue("non_spending_strategy")
	if strategy == "" {
		strategy = budget.StrategyRollOver
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	err := budget.UpdateTarget(c.Request().Context(), tenantDB, budget.UpdateTargetInput{
		ID:                  targetID,
		Name:                name,
		TargetType:          tType,
		Priority:            priority,
		AmountMinor:         amtMinor,
		RecurrenceCadence:   cadence,
		NonSpendingStrategy: strategy,
		AgentID:             userID,
		CorrelationID:       corrID,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}

// DeleteTarget deletes (archives) a target.
func (h *WorkspaceHandler) DeleteTarget(c echo.Context) error {
	budgetID := c.Param("budgetId")
	targetID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	err := budget.DeleteTarget(c.Request().Context(), tenantDB, targetID, userID, corrID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", budgetID))
}
