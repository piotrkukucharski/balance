package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/service"
	"github.com/labstack/echo/v4"
)

type WorkspaceHandler struct {
	mainDB         *sql.DB
	tenantMgr      *db.TenantDBManager
	reportsHandler *ReportsHandler
	appService     *service.AppService
	structureAI    ai.BudgetStructureAIProvider
}

func NewWorkspaceHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager, reportsHandler *ReportsHandler) *WorkspaceHandler {
	return &WorkspaceHandler{
		mainDB:         mainDB,
		tenantMgr:      tenantMgr,
		reportsHandler: reportsHandler,
		structureAI:    ai.NewBudgetStructureProvider(),
	}
}

func (h *WorkspaceHandler) SetService(svc *service.AppService) {
	h.appService = svc
}

func (h *WorkspaceHandler) SetStructureAIProvider(p ai.BudgetStructureAIProvider) {
	h.structureAI = p
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
	var existingBudgetsOptions strings.Builder
	hasBudgets := false
	hasOwnerBudget := false
	for rows.Next() {
		hasBudgets = true
		var id, name, curr, tz, role string
		rows.Scan(&id, &name, &curr, &tz, &role)
		existingBudgetsOptions.WriteString(fmt.Sprintf(`<option value="%s">%s (%s)</option>`, id, html.EscapeString(name), html.EscapeString(curr)))

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
							<button type="button" class="flex items-center gap-2 py-1.5" onclick="openCloneBudgetModal('%s', '%s')">
								<span>📋</span>
								<span>Clone Budget</span>
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
			`, id, html.EscapeString(name), id, html.EscapeString(name), id, html.EscapeString(name), c.Get("csrfToken"))
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
				<div class="flex flex-wrap items-center justify-center gap-2">
					<button class="btn btn-primary btn-sm shadow font-semibold" onclick="openNewBudgetModal('empty')">+ Create Budget</button>
					<button class="btn btn-outline btn-primary btn-sm shadow font-semibold gap-1.5" onclick="openNewBudgetModal('ai')"><span>✨</span><span>Create with AI</span></button>
				</div>
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

    <!-- Clone Budget Modal -->
    <dialog id="clone_budget_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <div>
                    <h3 class="font-extrabold text-xl tracking-tight">Clone Budget</h3>
                    <p class="text-xs text-base-content/60 mt-0.5">Duplicate budget workspace with selective resource checklist</p>
                </div>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="clone_budget_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">New Budget Name</span></label>
                    <input type="text" id="clone_budget_name" name="name" required placeholder="e.g. My Budget (Copy)" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                
                <div class="divider my-0 text-xs text-base-content/60 font-semibold uppercase tracking-wider">Resources to Include</div>
                <div class="flex flex-col gap-2.5 bg-base-200/50 p-4 rounded-xl border border-base-300">
                    <label class="flex items-center gap-3 cursor-pointer">
                        <input type="checkbox" id="clone_include_members" name="include_members" value="1" checked class="checkbox checkbox-primary checkbox-sm" />
                        <span class="text-xs font-medium">Team Members <span class="text-base-content/50 font-normal">(roles and permissions)</span></span>
                    </label>
                    <label class="flex items-center gap-3 cursor-pointer">
                        <input type="checkbox" id="clone_include_accounts" name="include_accounts" value="1" checked onchange="syncCloneDependencies()" class="checkbox checkbox-primary checkbox-sm" />
                        <span class="text-xs font-medium">Accounts <span class="text-base-content/50 font-normal">(checking, cash, savings, external parties)</span></span>
                    </label>
                    <label class="flex items-center gap-3 cursor-pointer">
                        <input type="checkbox" id="clone_include_buckets" name="include_buckets" value="1" checked onchange="syncCloneDependencies()" class="checkbox checkbox-primary checkbox-sm" />
                        <span class="text-xs font-medium">Buckets & Groups <span class="text-base-content/50 font-normal">(envelope structure and categories)</span></span>
                    </label>
                    <label class="flex items-center gap-3 cursor-pointer">
                        <input type="checkbox" id="clone_include_targets" name="include_targets" value="1" checked onchange="syncCloneDependencies()" class="checkbox checkbox-primary checkbox-sm" />
                        <span class="text-xs font-medium">Targets <span class="text-base-content/50 font-normal">(funding goals and formulas; requires Buckets)</span></span>
                    </label>
                    <label class="flex items-center gap-3 cursor-pointer">
                        <input type="checkbox" id="clone_include_transactions" name="include_transactions" value="1" checked onchange="syncCloneDependencies()" class="checkbox checkbox-primary checkbox-sm" />
                        <span class="text-xs font-medium">Transactions <span class="text-base-content/50 font-normal">(ledger history; requires Accounts & Buckets)</span></span>
                    </label>
                </div>

                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="clone_budget_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md gap-1.5">
                        <span>📋</span>
                        <span>Clone Budget</span>
                    </button>
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

        function openCloneBudgetModal(budgetId, budgetName) {
            const form = document.getElementById('clone_budget_form');
            form.action = '/budgets/' + budgetId + '/clone';
            const nameInput = document.getElementById('clone_budget_name');
            nameInput.value = budgetName + ' (Copy)';
            document.getElementById('clone_include_members').checked = true;
            document.getElementById('clone_include_accounts').checked = true;
            document.getElementById('clone_include_buckets').checked = true;
            document.getElementById('clone_include_targets').checked = true;
            document.getElementById('clone_include_transactions').checked = true;
            syncCloneDependencies();
            document.getElementById('clone_budget_modal').showModal();
        }

        function syncCloneDependencies() {
            const accounts = document.getElementById('clone_include_accounts');
            const buckets = document.getElementById('clone_include_buckets');
            const targets = document.getElementById('clone_include_targets');
            const txs = document.getElementById('clone_include_transactions');

            // If user explicitly checks transactions, accounts and buckets must be checked
            if (txs.checked) {
                accounts.checked = true;
                buckets.checked = true;
            }
            // If user explicitly checks targets, buckets must be checked
            if (targets.checked) {
                buckets.checked = true;
            }
            // If buckets is unchecked, uncheck targets and transactions
            if (!buckets.checked) {
                targets.checked = false;
                txs.checked = false;
            }
            // If accounts is unchecked, uncheck transactions
            if (!accounts.checked) {
                txs.checked = false;
            }
        }
    </script>
`, csrfToken, csrfToken)
	}

	copyDisabledClass := ""
	copyDisabledAttr := ""
	if !hasBudgets {
		copyDisabledClass = "opacity-50 cursor-not-allowed"
		copyDisabledAttr = "disabled"
	}
	copyOptionsHTML := existingBudgetsOptions.String()
	if copyOptionsHTML == "" {
		copyOptionsHTML = `<option value="" disabled selected>No existing budgets available</option>`
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
            <button class="btn btn-primary shadow-md font-semibold gap-2" onclick="openNewBudgetModal('empty')">
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

                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Initial Structure</span></label>
                    <div class="grid grid-cols-3 gap-2">
                        <label class="cursor-pointer label border border-base-300 rounded-lg p-2 flex items-center gap-2 hover:bg-base-200">
                            <input type="radio" name="creation_mode" value="empty" class="radio radio-xs radio-primary" checked onchange="toggleCreationMode('empty')" />
                            <span class="label-text text-xs font-semibold">Empty</span>
                        </label>
                        <label class="cursor-pointer label border border-base-300 rounded-lg p-2 flex items-center gap-2 hover:bg-base-200">
                            <input type="radio" name="creation_mode" value="ai" class="radio radio-xs radio-primary" id="creation_mode_ai" onchange="toggleCreationMode('ai')" />
                            <span class="label-text text-xs font-semibold">✨ AI Gen</span>
                        </label>
                        <label class="cursor-pointer label border border-base-300 rounded-lg p-2 flex items-center gap-2 hover:bg-base-200 %s">
                            <input type="radio" name="creation_mode" value="copy" class="radio radio-xs radio-primary" id="creation_mode_copy" onchange="toggleCreationMode('copy')" %s />
                            <span class="label-text text-xs font-semibold">📥 Copy</span>
                        </label>
                    </div>
                </div>

                <div id="ai_prompt_container" class="hidden form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">AI Budget Description</span></label>
                    <textarea name="ai_prompt" id="ai_prompt_input" rows="3" placeholder="e.g. Rodzina 2+1, mieszkanie i kredyt 2500 PLN, jedzenie 1600 PLN, rachunki 400 PLN, oszczędności 500 PLN..." class="textarea textarea-bordered w-full text-xs focus:textarea-primary"></textarea>
                    <span class="text-[10px] text-base-content/50 mt-1">AI will automatically create envelopes, calculate targets and assign priority rules.</span>
                </div>

                <div id="copy_budget_container" class="hidden form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Source Budget to Copy From</span></label>
                    <select name="source_budget_id" class="select select-bordered w-full select-sm focus:select-primary">
                        %s
                    </select>
                </div>

                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="new_budget_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Create Budget</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
    <script>
        function openNewBudgetModal(mode) {
            if (mode === 'ai') {
                const aiRadio = document.getElementById('creation_mode_ai');
                if (aiRadio) { aiRadio.checked = true; toggleCreationMode('ai'); }
            } else if (mode === 'copy') {
                const copyRadio = document.getElementById('creation_mode_copy');
                if (copyRadio && !copyRadio.disabled) { copyRadio.checked = true; toggleCreationMode('copy'); }
            } else {
                const emptyRadio = document.querySelector('input[name="creation_mode"][value="empty"]');
                if (emptyRadio) { emptyRadio.checked = true; toggleCreationMode('empty'); }
            }
            new_budget_modal.showModal();
        }

        function toggleCreationMode(mode) {
            const aiCont = document.getElementById('ai_prompt_container');
            const copyCont = document.getElementById('copy_budget_container');
            if (aiCont) aiCont.classList.toggle('hidden', mode !== 'ai');
            if (copyCont) copyCont.classList.toggle('hidden', mode !== 'copy');
        }
    </script>
</body>
</html>`, csrfToken, budgetCards.String(), editBudgetModalHTML, csrfToken, copyDisabledClass, copyDisabledAttr, copyOptionsHTML)

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

	dFormat, tFormat := GetUserDateTimePreferences(c)
	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark" data-date-format="%s" data-time-format="%s">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <meta name="user-date-format" content="%s">
    <meta name="user-time-format" content="%s">
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

                <!-- Personalization Shortcut Card -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex items-center justify-between">
                            <div>
                                <h2 class="card-title text-base font-bold flex items-center gap-2">
                                    <span>👤</span>
                                    <span>Personal Preferences</span>
                                </h2>
                                <p class="text-xs text-base-content/60 mt-1">Configure your personal date format (%s) and time format (%s).</p>
                            </div>
                            <a href="/budgets/%s/settings/profile" class="btn btn-sm btn-outline btn-primary font-medium">
                                Customize Formats →
                            </a>
                        </div>
                    </div>
                </div>

                %s
            </div>
        </main>
    </div>

    %s
    %s
</body>
</html>`,
		html.EscapeString(dFormat), html.EscapeString(tFormat),
		html.EscapeString(dFormat), html.EscapeString(tFormat),
		html.EscapeString(name),
		html.EscapeString(name),
		html.EscapeString(role),
		csrfToken,
		sidebarHTML,
		editFormHTML,
		html.EscapeString(dFormat), html.EscapeString(tFormat),
		budgetID,
		dangerZoneHTML,
		bottomDockHTML,
		DateTimeFormatterScript(),
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

// CloneBudget clones an existing budget tenant and its resources based on checklist (requires OWNER).
func (h *WorkspaceHandler) CloneBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")
	budgetID := c.Param("budgetId")
	name := strings.TrimSpace(c.FormValue("name"))

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Budget name cannot be empty")
	}

	userID := c.Get(ContextUserIDKey).(string)

	includeMembers := c.FormValue("include_members") == "1" || c.FormValue("include_members") == "true" || c.FormValue("include_members") == "on"
	includeAccounts := c.FormValue("include_accounts") == "1" || c.FormValue("include_accounts") == "true" || c.FormValue("include_accounts") == "on"
	includeBuckets := c.FormValue("include_buckets") == "1" || c.FormValue("include_buckets") == "true" || c.FormValue("include_buckets") == "on"
	includeTargets := c.FormValue("include_targets") == "1" || c.FormValue("include_targets") == "true" || c.FormValue("include_targets") == "on"
	includeTransactions := c.FormValue("include_transactions") == "1" || c.FormValue("include_transactions") == "true" || c.FormValue("include_transactions") == "on"

	correlationID := c.Response().Header().Get("X-Correlation-ID")

	if h.appService != nil {
		newBudgetID, err := h.appService.CloneBudget(c.Request().Context(), service.CloneBudgetInput{
			SourceBudgetID:      budgetID,
			NewName:             name,
			RequestingUserID:    userID,
			IncludeMembers:      includeMembers,
			IncludeAccounts:     includeAccounts,
			IncludeBuckets:      includeBuckets,
			IncludeTargets:      includeTargets,
			IncludeTransactions: includeTransactions,
			CorrelationID:       correlationID,
		})
		if err != nil {
			if errors.Is(err, service.ErrInvalidInput) {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			if errors.Is(err, service.ErrUnauthorized) {
				return echo.NewHTTPError(http.StatusForbidden, err.Error())
			}
			if errors.Is(err, service.ErrBudgetNotFound) {
				return echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
			logger.Error(ctx, "Failed cloning budget via service", "source_budget_id", budgetID, "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to clone budget")
		}

		logger.Info(ctx, "Budget cloned successfully", "source_budget_id", budgetID, "new_budget_id", newBudgetID, "name", name)
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", newBudgetID))
	}

	return echo.NewHTTPError(http.StatusInternalServerError, "Service unavailable")
}

// DeleteBudget deletes the budget and its memberships from main.db, and deletes its tenant sqlite database (requires OWNER).
func (h *WorkspaceHandler) DeleteBudget(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:budget")
	budgetID := c.Param("budgetId")

	if h.appService != nil {
		if err := h.appService.RemoveBudget(c.Request().Context(), budgetID); err != nil {
			if errors.Is(err, service.ErrBudgetNotFound) {
				return echo.NewHTTPError(http.StatusNotFound, "Budget not found")
			}
			logger.Error(ctx, "Failed deleting budget via service", "budget_id", budgetID, "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete budget")
		}
		logger.Info(ctx, "Budget deleted successfully", "budget_id", budgetID)
		return c.Redirect(http.StatusSeeOther, "/budgets")
	}

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

	if h.appService != nil {
		id, err := h.appService.CreateBudget(c.Request().Context(), service.CreateBudgetInput{
			ID:              budgetID,
			Name:            name,
			OwnerUserID:     userID,
			DefaultCurrency: curr,
			Timezone:        tz,
		})
		if err != nil {
			logger.Error(ctx, "Failed creating budget via service", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create budget")
		}

		creationMode := c.FormValue("creation_mode")
		aiPrompt := strings.TrimSpace(c.FormValue("ai_prompt"))
		sourceBudgetID := strings.TrimSpace(c.FormValue("source_budget_id"))
		correlationID := c.Response().Header().Get("X-Correlation-ID")

		if creationMode == "ai" && aiPrompt != "" {
			if h.structureAI == nil {
				h.structureAI = ai.NewBudgetStructureProvider()
			}
			structRes, err := h.structureAI.GenerateBudgetStructure(c.Request().Context(), ai.BudgetStructurePromptRequest{
				Prompt:          aiPrompt,
				DefaultCurrency: curr,
			})
			if err != nil {
				logger.Error(ctx, "Failed generating budget structure during budget creation", "budget_id", id, "error", err.Error())
			} else if structRes != nil {
				bCount, tCount, errApply := h.appService.ApplyBudgetStructure(c.Request().Context(), id, structRes, userID, correlationID)
				if errApply != nil {
					logger.Error(ctx, "Failed applying generated budget structure during budget creation", "budget_id", id, "error", errApply.Error())
				} else {
					logger.Info(ctx, "Applied generated budget structure to new budget", "budget_id", id, "buckets", bCount, "targets", tCount)
				}
			}
		} else if creationMode == "copy" && sourceBudgetID != "" {
			exported, err := h.appService.ExportBudgetStructure(c.Request().Context(), sourceBudgetID)
			if err != nil {
				logger.Error(ctx, "Failed exporting structure from source budget during budget creation", "source_budget_id", sourceBudgetID, "error", err.Error())
			} else if exported != nil {
				bCount, tCount, errApply := h.appService.ApplyBudgetStructure(c.Request().Context(), id, exported, userID, correlationID)
				if errApply != nil {
					logger.Error(ctx, "Failed applying copied budget structure during budget creation", "budget_id", id, "error", errApply.Error())
				} else {
					logger.Info(ctx, "Applied copied budget structure to new budget", "budget_id", id, "buckets", bCount, "targets", tCount)
				}
			}
		}

		logger.Info(ctx, "New budget provisioned successfully", "budget_id", id, "name", name, "currency", curr, "timezone", tz, "owner_id", userID)
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/budget", id))
	}

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
	ID                  string  `json:"ID"`
	BucketID            string  `json:"BucketID"`
	Name                string  `json:"Name"`
	TargetType          string  `json:"TargetType"`
	Priority            int     `json:"Priority"`
	SortOrder           int     `json:"SortOrder"`
	AmountMode          string  `json:"AmountMode"`
	AmountMinor         int64   `json:"AmountMinor"`
	LuaScript           *string `json:"LuaScript"`
	LastFormulaError    *string `json:"LastFormulaError"`
	Currency            string  `json:"Currency"`
	RecurrenceCadence   string  `json:"RecurrenceCadence"`
	RecurrenceInterval  int     `json:"RecurrenceInterval"`
	RecurrenceDays      int     `json:"RecurrenceDays"`
	DueDate             int64   `json:"DueDate"`
	NonSpendingStrategy string  `json:"NonSpendingStrategy"`
	CeilingAmountMinor  *int64  `json:"CeilingAmountMinor"`
	CurrentFundedMinor  int64   `json:"CurrentFundedMinor"`
	RequiredMinor       int64   `json:"RequiredMinor"`
	HealthBadge         string  `json:"HealthBadge"`
	HealthBadgeClass    string  `json:"HealthBadgeClass"`
}

// BudgetView renders the YNAB-style Budget/Envelopes view with DaisyUI 5 stats, tables, and badges.
func (h *WorkspaceHandler) BudgetView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)

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
	snap, _ := budget.BuildBudgetSnapshot(c.Request().Context(), tenantDB, defaultCurr, windowDays)

	targetRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, bucket_id, name, target_type, priority, sort_order, amount_minor, currency,
		       recurrence_cadence, recurrence_interval, due_date, non_spending_strategy,
		       ceiling_amount_minor, current_funded_minor, amount_mode, lua_script, last_formula_error
		FROM targets_projection
		WHERE is_archived = 0
		ORDER BY priority DESC, due_date ASC, sort_order ASC`)
	targetsByBucket := make(map[string][]targetViewData)
	if err == nil {
		defer targetRows.Close()
		for targetRows.Next() {
			var t targetViewData
			var dueDate sql.NullInt64
			var rInterval sql.NullInt64
			var ceiling sql.NullInt64
			var luaScriptNull sql.NullString
			var lastErrNull sql.NullString
			targetRows.Scan(&t.ID, &t.BucketID, &t.Name, &t.TargetType, &t.Priority, &t.SortOrder,
				&t.AmountMinor, &t.Currency, &t.RecurrenceCadence, &rInterval,
				&dueDate, &t.NonSpendingStrategy, &ceiling, &t.CurrentFundedMinor,
				&t.AmountMode, &luaScriptNull, &lastErrNull)
			if rInterval.Valid {
				t.RecurrenceInterval = int(rInterval.Int64)
				t.RecurrenceDays = t.RecurrenceInterval
			}
			if dueDate.Valid {
				t.DueDate = dueDate.Int64
			}
			if ceiling.Valid {
				cVal := ceiling.Int64
				t.CeilingAmountMinor = &cVal
			}
			if luaScriptNull.Valid {
				sVal := luaScriptNull.String
				t.LuaScript = &sVal
			}
			if lastErrNull.Valid {
				eVal := lastErrNull.String
				t.LastFormulaError = &eVal
			}

			// Dynamic Lua formula evaluation
			if snap != nil && t.AmountMode == "LUA_FORMULA" && t.LuaScript != nil && *t.LuaScript != "" {
				evalVal, evalErr := lua.EvaluateFormula(c.Request().Context(), *t.LuaScript, snap)
				if evalErr == nil {
					t.AmountMinor = evalVal
					t.LastFormulaError = nil
				} else {
					errStr := evalErr.Error()
					t.LastFormulaError = &errStr
				}
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
				RecurrenceInterval:  t.RecurrenceInterval,
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
				goalText := ""
				if t.CeilingAmountMinor != nil {
					goalText = fmt.Sprintf(` <span class="text-primary font-semibold">(Goal: %.2f %s)</span>`, float64(*t.CeilingAmountMinor)/100.0, t.Currency)
				}
				dueText := ""
				if t.DueDate > 0 {
					dTime := time.UnixMicro(t.DueDate).UTC()
					dueText = fmt.Sprintf(` • Due: %s`, dTime.Format("2006-01-02"))
				}
				targetsSublist.WriteString(fmt.Sprintf(`
					<div class="flex items-center justify-between text-base-content/70 py-0.5">
						<span class="font-medium">• %s <span class="opacity-50 text-[10px]">(%s, %s%s)%s</span></span>
						<div class="flex items-center gap-2">
							<span class="badge %s badge-xs">%s</span>
							<span class="font-mono">%.2f / %.2f %s</span>
						</div>
					</div>`,
					html.EscapeString(t.Name), priorityLabel, t.RecurrenceCadence, dueText, goalText,
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
				<td colspan="4" class="p-8">
					<div class="card card-border bg-base-100 border-dashed border-base-300 p-8 text-center flex flex-col items-center justify-center gap-4">
						<div class="w-14 h-14 rounded-2xl bg-primary/10 text-primary flex items-center justify-center text-3xl font-bold">✨</div>
						<div>
							<h3 class="font-extrabold text-lg tracking-tight">No Envelopes Created Yet</h3>
							<p class="text-xs text-base-content/60 max-w-md mx-auto mt-1">Get started quickly by generating a complete envelope structure with AI, importing from another budget, or adding envelopes manually.</p>
						</div>
						<div class="flex flex-wrap items-center justify-center gap-2 mt-2">
							<button type="button" class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="openStructureModal('ai')">
								<span>✨</span>
								<span>Create from Description (AI)</span>
							</button>
							<button type="button" class="btn btn-outline btn-secondary btn-sm shadow font-semibold gap-1.5" onclick="openStructureModal('import')">
								<span>📥</span>
								<span>Import from Another Budget</span>
							</button>
							<button type="button" class="btn btn-ghost btn-sm" onclick="new_bucket_modal.showModal()">
								<span>+</span>
								<span>New Bucket Manually</span>
							</button>
						</div>
					</div>
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

	// Query user's other accessible budgets for Structure Import dropdown
	otherBudgetRows, err := h.mainDB.QueryContext(c.Request().Context(), `
		SELECT b.id, b.name, b.default_currency
		FROM budgets b
		JOIN budget_members bm ON b.id = bm.budget_id
		WHERE bm.user_id = ? AND b.id != ?
		ORDER BY b.name ASC`, userID, budgetID)
	var otherBudgetOptions strings.Builder
	hasOtherBudgets := false
	if err == nil {
		defer otherBudgetRows.Close()
		for otherBudgetRows.Next() {
			hasOtherBudgets = true
			var oID, oName, oCurr string
			if err := otherBudgetRows.Scan(&oID, &oName, &oCurr); err == nil {
				otherBudgetOptions.WriteString(fmt.Sprintf(`<option value="%s">%s (%s)</option>`, oID, html.EscapeString(oName), html.EscapeString(oCurr)))
			}
		}
	}
	otherBudgetSelectDisabled := ""
	if !hasOtherBudgets {
		otherBudgetSelectDisabled = "disabled"
		otherBudgetOptions.WriteString(`<option value="" disabled selected>No other budgets available to import from</option>`)
	}

	structureModalHTML := fmt.Sprintf(`
    <!-- Budget Structure Generator & Import Modal -->
    <dialog id="structure_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6 max-w-3xl w-11/12 max-h-[90vh] flex flex-col">
            <div class="flex justify-between items-center mb-4 shrink-0">
                <div class="flex items-center gap-2">
                    <span class="text-xl">✨</span>
                    <h3 class="font-extrabold text-xl tracking-tight">Envelope Structure Assistant</h3>
                </div>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>

            <!-- Tabs -->
            <div role="tablist" class="tabs tabs-lift mb-4 shrink-0">
                <button type="button" id="tab_btn_ai" role="tab" class="tab tab-active font-semibold text-xs" onclick="switchStructureTab('ai')">
                    ✨ Generate with AI
                </button>
                <button type="button" id="tab_btn_import" role="tab" class="tab font-semibold text-xs" onclick="switchStructureTab('import')">
                    📥 Import from Budget
                </button>
            </div>

            <!-- Tab 1: AI Prompt -->
            <div id="structure_tab_ai" class="flex flex-col gap-3 shrink-0">
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Describe your budget in natural language</span></label>
                    <textarea id="structure_ai_prompt" rows="3" placeholder="e.g. Rodzina 2+1, wynajem mieszkania 2800 PLN, rachunki i internet 350 PLN, zakupy spożywcze 1600 PLN, paliwo 400 PLN, poduszka finansowa 500 PLN, wakacje 300 PLN..." class="textarea textarea-bordered w-full text-xs focus:textarea-primary"></textarea>
                    <div class="text-[10px] text-base-content/50 mt-1">Our AI assistant will propose organized envelope groups, buckets, and monthly targets.</div>
                </div>
                <button type="button" id="btn_generate_preview" class="btn btn-primary btn-sm self-start font-semibold shadow gap-2" onclick="runAIGeneration()">
                    <span>✨</span>
                    <span>Generate Preview</span>
                </button>
            </div>

            <!-- Tab 2: Import from Budget -->
            <div id="structure_tab_import" class="hidden flex flex-col gap-3 shrink-0">
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Select source budget to copy from</span></label>
                    <select id="source_budget_select" class="select select-bordered w-full select-sm focus:select-primary" %s>
                        %s
                    </select>
                </div>
                <button type="button" id="btn_load_source" class="btn btn-secondary btn-sm self-start font-semibold shadow gap-2" onclick="runSourceImport()" %s>
                    <span>📥</span>
                    <span>Load Structure</span>
                </button>
            </div>

            <!-- Loading Spinner -->
            <div id="structure_loading" class="hidden flex flex-col items-center justify-center p-8 gap-3 shrink-0">
                <span class="loading loading-spinner loading-md text-primary"></span>
                <span class="text-xs text-base-content/60" id="structure_loading_text">Analyzing description and generating budget envelopes...</span>
            </div>

            <!-- Error Banner -->
            <div id="structure_error" class="hidden alert alert-error text-xs mt-3 shrink-0">
                <span id="structure_error_text"></span>
            </div>

            <!-- Interactive Hierarchical Preview -->
            <div id="structure_preview_wrapper" class="hidden flex-1 overflow-y-auto mt-4 border border-base-300 rounded-box p-4 bg-base-200/40">
                <div class="flex items-center justify-between pb-3 border-b border-base-300 mb-3">
                    <div class="flex items-center gap-2">
                        <input type="checkbox" id="structure_select_all" class="checkbox checkbox-primary checkbox-xs" checked onchange="toggleSelectAllStructure(this.checked)" />
                        <label for="structure_select_all" class="text-xs font-bold cursor-pointer">Select All Items</label>
                    </div>
                    <span id="structure_stats_badge" class="badge badge-neutral badge-xs font-mono">0 envelopes, 0 targets</span>
                </div>

                <div id="structure_preview_list" class="space-y-4 text-xs">
                    <!-- Populated dynamically via JS -->
                </div>
            </div>

            <div class="modal-action mt-4 pt-3 border-t border-base-300 flex justify-between items-center shrink-0">
                <button type="button" class="btn btn-ghost btn-sm" onclick="structure_modal.close()">Cancel</button>
                <button type="button" id="btn_apply_structure" class="btn btn-primary btn-sm font-semibold shadow gap-2 hidden" onclick="applyStructureConfirmed()">
                    <span>✓</span>
                    <span>Apply Selected Structure</span>
                </button>
            </div>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
`, otherBudgetSelectDisabled, otherBudgetOptions.String(), otherBudgetSelectDisabled)

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
                <div class="flex items-center gap-2">
                    <button type="button" class="btn btn-outline btn-primary btn-sm gap-1.5 shadow-xs font-semibold" onclick="openStructureModal('ai')">
                        <span>✨</span>
                        <span>AI / Import Structure</span>
                    </button>
                    <button class="btn btn-primary btn-sm gap-1.5 shadow font-semibold" onclick="new_bucket_modal.showModal()">
                        <span>+</span>
                        <span>New Bucket</span>
                    </button>
                </div>
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

    <!-- Create / Edit Target Modal -->
    <dialog id="target_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6 max-w-xl">
            <div class="flex justify-between items-center mb-4">
                <h3 id="target_modal_title" class="font-extrabold text-xl tracking-tight">Add Target</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="target_form" method="POST" action="/budgets/%s/targets" class="flex flex-col gap-4" onsubmit="prepareTargetSubmit()">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" id="target_bucket_id" name="bucket_id" value="" />
                <input type="hidden" id="target_currency" name="currency" value="%s" />

                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Target Name</span></label>
                    <input type="text" id="target_name" name="name" required placeholder="e.g. Monthly Rent, Electricity" class="input input-bordered w-full input-md focus:input-primary" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Target Type</span></label>
                        <select id="target_type" name="target_type" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="EXPENSE" selected>Expense</option>
                            <option value="SAVINGS">Savings</option>
                            <option value="DEBT_PAYMENT">Debt Payment</option>
                        </select>
                    </div>
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Priority</span></label>
                        <select id="target_priority" name="priority" class="select select-bordered w-full select-md focus:select-primary">
                            <option value="3" selected>Mandatory (3)</option>
                            <option value="2">Recommended (2)</option>
                            <option value="1">Optional (1)</option>
                        </select>
                    </div>
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label pb-1"><span class="label-text font-medium text-xs">Cadence</span></label>
                        <select id="target_cadence" name="recurrence_cadence" class="select select-bordered w-full select-md focus:select-primary" onchange="toggleCustomDays()">
                            <option value="MONTHLY" selected>Monthly</option>
                            <option value="WEEKLY">Weekly</option>
                            <option value="ANNUAL">Annual</option>
                            <option value="ONE_OFF">One-Off</option>
                            <option value="CUSTOM_DAYS">Custom Days</option>
                            <option value="CUSTOM_MONTHS">Custom Months</option>
                            <option value="CUSTOM_YEARS">Custom Years</option>
                        </select>
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

                <!-- Custom Interval Input -->
                <div id="custom_days_container" class="hidden form-control">
                    <label class="label pb-1"><span id="target_recurrence_interval_label" class="label-text font-medium text-xs font-semibold">Recurrence Interval (Days)</span></label>
                    <input type="number" id="target_recurrence_days" name="recurrence_interval" min="1" max="36500" placeholder="e.g. 90 for quarterly, 180 for semi-annual" class="input input-bordered w-full input-md focus:input-primary font-mono" />
                </div>

                <!-- Due Date / Anchor Date Input -->
                <div id="target_due_date_container" class="hidden form-control">
                    <label class="label pb-1"><span id="target_due_date_label" class="label-text font-medium text-xs font-semibold">Anchor Due Date</span></label>
                    <input type="date" id="target_due_date" name="due_date" class="input input-bordered w-full input-md focus:input-primary font-mono" />
                    <span id="target_due_date_hint" class="text-[10px] text-base-content/50 mt-1">Calendar anchor date used for calculating recurring cycles</span>
                </div>

                <!-- Calculation Mode Selector -->
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Calculation Mode</span></label>
                    <div class="join w-full grid grid-cols-2">
                        <input class="join-item btn btn-sm btn-outline checked:btn-primary text-xs" type="radio" name="amount_mode" id="mode_fixed" value="FIXED" aria-label="Fixed Amount" checked onchange="switchAmountMode('FIXED')" />
                        <input class="join-item btn btn-sm btn-outline checked:btn-primary text-xs" type="radio" name="amount_mode" id="mode_lua" value="LUA_FORMULA" aria-label="Dynamic Formula (Lua / AI)" onchange="switchAmountMode('LUA_FORMULA')" />
                    </div>
                </div>

                <!-- Fixed Amount Input -->
                <div id="fixed_amount_container" class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Period Amount</span></label>
                    <input type="number" step="0.01" min="0" id="target_amount" name="amount" required placeholder="1000.00" class="input input-bordered w-full input-md focus:input-primary font-mono" />
                </div>

                <!-- Lua Formula & AI Assistant Container -->
                <div id="lua_formula_container" class="hidden flex flex-col gap-3">
                    <div class="form-control">
                        <div class="flex justify-between items-center pb-1">
                            <label class="label-text font-medium text-xs font-semibold">Lua Script</label>
                            <span class="text-[10px] text-base-content/50 font-mono">minor units (int64)</span>
                        </div>
                        <textarea id="target_lua_script" name="lua_script" rows="3" placeholder="return math.max(0, money(2000) - target('Mortgage Installment').required)" class="textarea textarea-bordered w-full font-mono text-xs focus:textarea-primary"></textarea>
                        <div class="text-[10px] text-base-content/60 mt-1">
                            API: <code class="bg-base-200 px-1 rounded">money(val)</code>, <code class="bg-base-200 px-1 rounded">target(&quot;name&quot;).required</code>, <code class="bg-base-200 px-1 rounded">bucket.balance()</code>, <code class="bg-base-200 px-1 rounded">budget.income(days)</code>
                        </div>
                    </div>

                    <div class="flex items-center gap-2">
                        <button type="button" id="btn_validate_formula" class="btn btn-outline btn-xs gap-1 font-semibold" onclick="validateFormulaScript()">
                            🔍 Validate &amp; Preview
                        </button>
                        <button type="button" id="btn_toggle_ai" class="btn btn-secondary btn-xs gap-1 font-semibold" onclick="toggleAIPanel()">
                            ✨ Generate with AI
                        </button>
                    </div>

                    <div id="formula_preview_box" class="hidden text-xs p-3 rounded-lg border flex flex-col gap-1"></div>

                    <div id="ai_assistant_panel" class="hidden card bg-base-200/60 border border-base-300 p-3 rounded-lg flex flex-col gap-2">
                        <div class="flex justify-between items-center">
                            <span class="text-xs font-bold text-secondary flex items-center gap-1">✨ AI Formula Assistant</span>
                            <span class="text-[10px] text-base-content/50">Gemini 3.8 Flash</span>
                        </div>
                        <div class="join w-full">
                            <input type="text" id="ai_formula_prompt" placeholder="e.g. Save 15%% of total income over last 30 days capped at 5000 PLN" class="input input-bordered input-sm join-item w-full text-xs" />
                            <button type="button" id="btn_generate_ai" class="btn btn-secondary btn-sm join-item" onclick="generateFormulaAI()">Generate</button>
                        </div>
                        <div id="ai_status_msg" class="text-xs hidden"></div>
                    </div>
                </div>

                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Target Ceiling / Goal (Optional)</span></label>
                    <input type="number" step="0.01" min="0" id="target_ceiling" name="ceiling_amount" placeholder="20000.00" class="input input-bordered w-full input-md focus:input-primary font-mono" />
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
        const currentCurrency = '%s';
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
                    const ceilingHtml = t.CeilingAmountMinor ? ' • <span class="font-semibold text-primary">Goal: ' + (t.CeilingAmountMinor/100).toFixed(2) + ' ' + t.Currency + '</span>' : '';
                    const luaBadge = t.AmountMode === 'LUA_FORMULA' ? ' <span class="badge badge-outline badge-xs font-mono text-secondary border-secondary">LUA</span>' : '';
                    const formulaErrHtml = (t.LastFormulaError && t.LastFormulaError.length > 0) ? '<div class="text-[11px] text-error font-mono mt-1">⚠️ ' + escapeHtml(t.LastFormulaError) + '</div>' : '';
                    const rInt = t.RecurrenceInterval || t.RecurrenceDays;
                    const cadenceDisplay = t.RecurrenceCadence === 'CUSTOM_DAYS' ? ((rInt || 90) + 'd') :
                        (t.RecurrenceCadence === 'CUSTOM_MONTHS' ? ((rInt || 3) + 'm') :
                        (t.RecurrenceCadence === 'CUSTOM_YEARS' ? ((rInt || 1) + 'y') : t.RecurrenceCadence));

                    let dueHtml = '';
                    if (t.DueDate && t.DueDate > 0) {
                        const d = new Date(t.DueDate / 1000);
                        dueHtml = ' • Due: ' + d.toISOString().split('T')[0];
                    }

                    card.innerHTML = 
                        '<div class="flex justify-between items-start">' +
                            '<div>' +
                                '<div class="font-bold text-sm tracking-tight flex items-center gap-1">' +
                                    escapeHtml(t.Name) + luaBadge +
                                '</div>' +
                                '<div class="text-[11px] text-base-content/60">Priority: ' + priorityText + ' • Cadence: ' + cadenceDisplay + dueHtml + ' • Strategy: ' + t.NonSpendingStrategy + ceilingHtml + '</div>' +
                            '</div>' +
                            '<span class="badge ' + t.HealthBadgeClass + ' badge-sm font-semibold">' + t.HealthBadge + '</span>' +
                        '</div>' +
                        formulaErrHtml +
                        '<div class="flex justify-between items-center text-xs mt-1">' +
                            '<span class="font-mono">Funded: ' + (t.CurrentFundedMinor/100).toFixed(2) + ' / Required: ' + (t.RequiredMinor/100).toFixed(2) + ' ' + t.Currency + '</span>' +
                            '<div class="flex items-center gap-1">' +
                                '<button type="button" class="btn btn-ghost btn-xs text-primary font-semibold target-edit-btn" onclick="openEditTargetModal(\'' + t.ID + '\')">Edit</button>' +
                                '<form method="POST" action="/budgets/' + currentBudgetID + '/targets/' + t.ID + '/delete" onsubmit="return confirm(\'Delete this target?\')">' +
                                    '<input type="hidden" name="csrf_token" value="' + csrfTokenVal + '" />' +
                                    '<button type="submit" class="btn btn-ghost btn-xs text-error font-semibold">Delete</button>' +
                                '</form>' +
                            '</div>' +
                        '</div>';
                    container.appendChild(card);
                });
            } else {
                container.innerHTML = '<div class="text-xs text-base-content/50 py-3 text-center border border-dashed border-base-300 rounded-lg">No targets added to this envelope yet.</div>';
            }

            bucket_drawer.showModal();
        }

        function openEditTargetModal(id) {
            const b = bucketsData.find(x => x.ID === activeDrawerBucketID);
            if (!b || !b.Targets) return;
            const t = b.Targets.find(x => x.ID === id);
            if (!t) return;

            document.getElementById('target_modal_title').innerText = 'Edit Target';
            document.getElementById('target_form').action = '/budgets/' + currentBudgetID + '/targets/' + t.ID + '/update';
            document.getElementById('target_bucket_id').value = t.BucketID;
            document.getElementById('target_name').value = t.Name;
            document.getElementById('target_type').value = t.TargetType;
            document.getElementById('target_priority').value = t.Priority;
            document.getElementById('target_cadence').value = t.RecurrenceCadence;
            const rIntVal = t.RecurrenceInterval || t.RecurrenceDays || 90;
            if (document.getElementById('target_recurrence_days')) {
                document.getElementById('target_recurrence_days').value = rIntVal;
            }
            if (document.getElementById('target_due_date')) {
                if (t.DueDate && t.DueDate > 0) {
                    const d = new Date(t.DueDate / 1000);
                    document.getElementById('target_due_date').value = d.toISOString().split('T')[0];
                } else {
                    document.getElementById('target_due_date').value = '';
                }
            }
            document.getElementById('target_strategy').value = t.NonSpendingStrategy;
            document.getElementById('target_amount').value = (t.AmountMinor / 100).toFixed(2);
            document.getElementById('target_ceiling').value = t.CeilingAmountMinor ? (t.CeilingAmountMinor / 100).toFixed(2) : '';
            document.getElementById('target_currency').value = t.Currency;
            document.getElementById('target_lua_script').value = t.LuaScript || '';

            toggleCustomDays();
            hideFormulaPreview();
            hideAIPanel();

            const mode = t.AmountMode === 'LUA_FORMULA' ? 'LUA_FORMULA' : 'FIXED';
            switchAmountMode(mode);

            target_modal.showModal();
        }

        function openNewTargetModal() {
            document.getElementById('target_modal_title').innerText = 'Add Target';
            document.getElementById('target_form').action = '/budgets/' + currentBudgetID + '/targets';
            document.getElementById('target_bucket_id').value = activeDrawerBucketID;
            document.getElementById('target_name').value = '';
            document.getElementById('target_type').value = 'EXPENSE';
            document.getElementById('target_priority').value = '2';
            document.getElementById('target_cadence').value = 'MONTHLY';
            if (document.getElementById('target_recurrence_days')) {
                document.getElementById('target_recurrence_days').value = '90';
            }
            if (document.getElementById('target_due_date')) {
                document.getElementById('target_due_date').value = '';
            }
            document.getElementById('target_strategy').value = 'ROLL_OVER';
            document.getElementById('target_amount').value = '';
            document.getElementById('target_ceiling').value = '';
            document.getElementById('target_currency').value = currentCurrency;
            document.getElementById('target_lua_script').value = '';

            toggleCustomDays();
            hideFormulaPreview();
            hideAIPanel();
            switchAmountMode('FIXED');

            target_modal.showModal();
        }

        function toggleCustomDays() {
            const cadenceSelect = document.getElementById('target_cadence');
            const customContainer = document.getElementById('custom_days_container');
            const rInput = document.getElementById('target_recurrence_days');
            const rLabel = document.getElementById('target_recurrence_interval_label');
            const dueDateContainer = document.getElementById('target_due_date_container');
            const dueDateInput = document.getElementById('target_due_date');
            const dueDateLabel = document.getElementById('target_due_date_label');
            if (!cadenceSelect || !customContainer) return;

            const val = cadenceSelect.value;
            const isCustomDays = val === 'CUSTOM_DAYS';
            const isCustomMonths = val === 'CUSTOM_MONTHS';
            const isCustomYears = val === 'CUSTOM_YEARS';
            const isCustom = isCustomDays || isCustomMonths || isCustomYears;
            const isOneOff = val === 'ONE_OFF';

            const isWeeklyMonthlyAnnual = val === 'WEEKLY' || val === 'MONTHLY' || val === 'ANNUAL';

            if (isCustom) {
                customContainer.classList.remove('hidden');
                if (isCustomDays) {
                    if (rLabel) rLabel.innerText = 'Recurrence Interval (Days)';
                    if (rInput && (!rInput.value || parseInt(rInput.value) < 1)) rInput.value = '90';
                } else if (isCustomMonths) {
                    if (rLabel) rLabel.innerText = 'Recurrence Interval (Months)';
                    if (rInput && (!rInput.value || parseInt(rInput.value) < 1)) rInput.value = '3';
                } else if (isCustomYears) {
                    if (rLabel) rLabel.innerText = 'Recurrence Interval (Years)';
                    if (rInput && (!rInput.value || parseInt(rInput.value) < 1)) rInput.value = '1';
                }
            } else {
                customContainer.classList.add('hidden');
            }

            if (dueDateContainer && dueDateInput) {
                if (isCustomMonths || isCustomYears) {
                    dueDateContainer.classList.remove('hidden');
                    dueDateInput.required = true;
                    if (dueDateLabel) dueDateLabel.innerHTML = 'Anchor Due Date <span class="text-error">*</span>';
                    if (!dueDateInput.value) {
                        const d = new Date();
                        dueDateInput.value = d.toISOString().split('T')[0];
                    }
                } else if (isOneOff) {
                    dueDateContainer.classList.remove('hidden');
                    dueDateInput.required = false;
                    if (dueDateLabel) dueDateLabel.innerText = 'Target Due Date (Optional)';
                } else if (isWeeklyMonthlyAnnual) {
                    dueDateContainer.classList.remove('hidden');
                    dueDateInput.required = false;
                    if (dueDateLabel) dueDateLabel.innerText = 'Anchor Due Date (Optional)';
                } else {
                    dueDateContainer.classList.add('hidden');
                    dueDateInput.required = false;
                }
            }
        }

        function switchAmountMode(mode) {
            const isLua = mode === 'LUA_FORMULA';
            const rFixed = document.getElementById('mode_fixed');
            const rLua = document.getElementById('mode_lua');
            if (rFixed) rFixed.checked = !isLua;
            if (rLua) rLua.checked = isLua;

            const fixedContainer = document.getElementById('fixed_amount_container');
            const luaContainer = document.getElementById('lua_formula_container');
            const targetAmt = document.getElementById('target_amount');

            if (isLua) {
                if (fixedContainer) fixedContainer.classList.add('hidden');
                if (luaContainer) luaContainer.classList.remove('hidden');
                if (targetAmt) targetAmt.removeAttribute('required');
            } else {
                if (fixedContainer) fixedContainer.classList.remove('hidden');
                if (luaContainer) luaContainer.classList.add('hidden');
                if (targetAmt) targetAmt.setAttribute('required', 'required');
            }
        }

        function toggleAIPanel() {
            const panel = document.getElementById('ai_assistant_panel');
            if (panel) panel.classList.toggle('hidden');
        }

        function hideAIPanel() {
            const panel = document.getElementById('ai_assistant_panel');
            if (panel) panel.classList.add('hidden');
            const statusMsg = document.getElementById('ai_status_msg');
            if (statusMsg) {
                statusMsg.innerText = '';
                statusMsg.classList.add('hidden');
            }
        }

        function showFormulaPreview(type, htmlContent) {
            const box = document.getElementById('formula_preview_box');
            if (!box) return;
            box.className = 'text-xs p-3 rounded-lg border flex flex-col gap-1 ' + 
                (type === 'success' ? 'bg-success/10 border-success/30 text-success' : 
                (type === 'error' ? 'bg-error/10 border-error/30 text-error' : 'bg-base-200 border-base-300'));
            box.innerHTML = htmlContent;
            box.classList.remove('hidden');
        }

        function hideFormulaPreview() {
            const box = document.getElementById('formula_preview_box');
            if (box) {
                box.innerHTML = '';
                box.classList.add('hidden');
            }
        }

        function generateFormulaAI() {
            const promptInput = document.getElementById('ai_formula_prompt');
            const prompt = promptInput ? promptInput.value.trim() : '';
            const statusMsg = document.getElementById('ai_status_msg');
            const btnGen = document.getElementById('btn_generate_ai');

            if (!prompt) {
                if (statusMsg) {
                    statusMsg.className = 'text-xs text-error mt-1';
                    statusMsg.innerText = 'Please describe the formula in natural language.';
                    statusMsg.classList.remove('hidden');
                }
                return;
            }

            if (statusMsg) {
                statusMsg.className = 'text-xs text-base-content/70 mt-1 flex items-center gap-1';
                statusMsg.innerHTML = '<span class="loading loading-spinner loading-xs"></span> Generating formula with AI...';
                statusMsg.classList.remove('hidden');
            }
            if (btnGen) btnGen.disabled = true;

            fetch('/api/v1/budgets/' + currentBudgetID + '/targets/generate-formula', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-CSRF-Token': csrfTokenVal
                },
                body: JSON.stringify({
                    current_bucket_name: document.getElementById('drawer_bucket_name').innerText.trim() || 'General',
                    target_currency: currentCurrency || 'PLN',
                    user_prompt: prompt,
                    window_days: 30
                })
            })
            .then(res => res.json())
            .then(data => {
                if (btnGen) btnGen.disabled = false;
                if (data.error || !data.lua_script) {
                    if (statusMsg) {
                        statusMsg.className = 'text-xs text-error mt-1';
                        statusMsg.innerText = data.error || 'Failed generating formula';
                    }
                } else {
                    if (statusMsg) {
                        statusMsg.className = 'text-xs text-success mt-1';
                        statusMsg.innerHTML = '✨ <strong>Generated:</strong> ' + escapeHtml(data.explanation || '');
                    }
                    document.getElementById('target_lua_script').value = data.lua_script;
                    validateFormulaScript();
                }
            })
            .catch(err => {
                if (btnGen) btnGen.disabled = false;
                if (statusMsg) {
                    statusMsg.className = 'text-xs text-error mt-1';
                    statusMsg.innerText = 'Network error: ' + err.message;
                }
            });
        }

        function validateFormulaScript() {
            const script = document.getElementById('target_lua_script').value.trim();
            if (!script) {
                showFormulaPreview('error', '⚠️ Please enter a Lua script to validate.');
                return;
            }

            showFormulaPreview('info', '<div class="flex items-center gap-1"><span class="loading loading-spinner loading-xs"></span> Validating and running dry-run evaluation...</div>');

            fetch('/api/v1/budgets/' + currentBudgetID + '/targets/validate-formula', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-CSRF-Token': csrfTokenVal
                },
                body: JSON.stringify({
                    lua_script: script
                })
            })
            .then(res => res.json())
            .then(data => {
                if (data.valid) {
                    let depsHtml = (data.dependencies && data.dependencies.length > 0) 
                        ? 'Dependencies: <span class="font-mono">' + escapeHtml(data.dependencies.join(', ')) + '</span>' 
                        : 'No external target dependencies';
                    let previewHtml = '';
                    if (data.evaluated_minor !== undefined && data.evaluated_minor !== null) {
                        const evalAmt = (data.evaluated_minor / 100).toFixed(2);
                        previewHtml = '<div class="font-bold text-sm text-primary mt-1">Calculated Preview: ' + evalAmt + ' ' + currentCurrency + '</div>';
                        document.getElementById('target_amount').value = evalAmt;
                    }
                    showFormulaPreview('success', 
                        '<div class="flex items-center gap-1 font-bold text-success">✓ Valid Sandboxed Formula</div>' +
                        '<div class="text-[11px] text-base-content/70">' + depsHtml + '</div>' +
                        previewHtml
                    );
                } else {
                    showFormulaPreview('error', 
                        '<div class="font-bold text-error">✕ Validation Failed</div>' +
                        '<div class="font-mono text-[11px]">' + escapeHtml(data.error || 'Invalid script') + '</div>'
                    );
                }
            })
            .catch(err => {
                showFormulaPreview('error', '✕ Network error: ' + escapeHtml(err.message));
            });
        }

        function prepareTargetSubmit() {
            const rLua = document.getElementById('mode_lua');
            const targetAmt = document.getElementById('target_amount');
            if (rLua && rLua.checked) {
                if (!targetAmt.value || targetAmt.value.trim() === '') {
                    targetAmt.value = '0.00';
                }
            }
        }

        function openStructureModal(tab) {
            document.getElementById('structure_error').classList.add('hidden');
            document.getElementById('structure_preview_wrapper').classList.add('hidden');
            document.getElementById('btn_apply_structure').classList.add('hidden');
            switchStructureTab(tab || 'ai');
            structure_modal.showModal();
        }

        function switchStructureTab(tab) {
            const isAI = tab === 'ai';
            document.getElementById('tab_btn_ai').className = isAI ? 'tab tab-active font-semibold text-xs' : 'tab font-semibold text-xs';
            document.getElementById('tab_btn_import').className = !isAI ? 'tab tab-active font-semibold text-xs' : 'tab font-semibold text-xs';
            document.getElementById('structure_tab_ai').classList.toggle('hidden', !isAI);
            document.getElementById('structure_tab_import').classList.toggle('hidden', isAI);
        }

        let loadedStructureData = null;

        function runAIGeneration() {
            const prompt = document.getElementById('structure_ai_prompt').value.trim();
            if (!prompt) {
                showStructureError('Please enter a budget description.');
                return;
            }
            showStructureLoading(true, 'Analyzing description and generating budget envelopes with AI...');
            fetch('/budgets/' + currentBudgetID + '/structure/generate-ai-preview', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfTokenVal },
                body: JSON.stringify({ prompt: prompt, currency: currentCurrency })
            })
            .then(res => res.json())
            .then(data => {
                showStructureLoading(false);
                if (data.error) {
                    showStructureError(data.error);
                } else {
                    renderPreviewData(data);
                }
            })
            .catch(err => {
                showStructureLoading(false);
                showStructureError(err.message || 'Generation failed');
            });
        }

        function runSourceImport() {
            const sel = document.getElementById('source_budget_select');
            if (!sel || !sel.value) {
                showStructureError('Please select a source budget.');
                return;
            }
            showStructureLoading(true, 'Loading envelopes and targets from source budget...');
            fetch('/budgets/' + currentBudgetID + '/structure/preview-source?source_budget_id=' + encodeURIComponent(sel.value))
            .then(res => res.json())
            .then(data => {
                showStructureLoading(false);
                if (data.error) {
                    showStructureError(data.error);
                } else {
                    renderPreviewData(data);
                }
            })
            .catch(err => {
                showStructureLoading(false);
                showStructureError(err.message || 'Import failed');
            });
        }

        function showStructureLoading(show, text) {
            document.getElementById('structure_loading').classList.toggle('hidden', !show);
            document.getElementById('structure_error').classList.add('hidden');
            if (show) {
                document.getElementById('structure_preview_wrapper').classList.add('hidden');
                document.getElementById('btn_apply_structure').classList.add('hidden');
                if (text) document.getElementById('structure_loading_text').innerText = text;
            }
        }

        function showStructureError(msg) {
            const el = document.getElementById('structure_error');
            document.getElementById('structure_error_text').innerText = msg;
            el.classList.remove('hidden');
        }

        function renderPreviewData(data) {
            loadedStructureData = data;
            const container = document.getElementById('structure_preview_list');
            container.innerHTML = '';

            let totalBuckets = 0;
            let totalTargets = 0;

            if (!data.groups || data.groups.length === 0) {
                showStructureError('No envelopes or categories could be generated.');
                return;
            }

            data.groups.forEach((g, gIdx) => {
                const groupEl = document.createElement('div');
                groupEl.className = 'border border-base-300 rounded-lg p-3 bg-base-100 shadow-xs flex flex-col gap-2';

                let bucketsHtml = '';
                if (g.buckets) {
                    g.buckets.forEach((b, bIdx) => {
                        totalBuckets++;
                        let targetsHtml = '';
                        if (b.targets) {
                            b.targets.forEach((t, tIdx) => {
                                totalTargets++;
                                const prioText = t.priority === 3 ? 'Mandatory' : (t.priority === 2 ? 'Recommended' : 'Optional');
                                const prioBadgeClass = t.priority === 3 ? 'badge-error' : (t.priority === 2 ? 'badge-primary' : 'badge-ghost');
                                const amtVal = (t.amount_minor / 100).toFixed(2);

                                targetsHtml += 
                                    '<div class="flex items-center gap-2 pl-6 py-1 border-t border-base-200">' +
                                        '<input type="checkbox" class="checkbox checkbox-primary checkbox-xs target-chk" checked data-group="' + gIdx + '" data-bucket="' + bIdx + '" data-target="' + tIdx + '" onchange="updateCountsBadge()">' +
                                        '<input type="text" class="input input-bordered input-xs flex-1 target-name-input" value="' + escapeHtml(t.name) + '" data-group="' + gIdx + '" data-bucket="' + bIdx + '" data-target="' + tIdx + '">' +
                                        '<div class="flex items-center gap-1 shrink-0">' +
                                            '<input type="number" step="0.01" class="input input-bordered input-xs w-20 text-right font-mono target-amount-input" value="' + amtVal + '" data-group="' + gIdx + '" data-bucket="' + bIdx + '" data-target="' + tIdx + '">' +
                                            '<span class="text-[10px] font-bold text-base-content/60">' + (t.currency || currentCurrency) + '</span>' +
                                        '</div>' +
                                        '<span class="badge ' + prioBadgeClass + ' badge-xs shrink-0">' + prioText + '</span>' +
                                    '</div>';
                            });
                        }

                        bucketsHtml += 
                            '<div class="border border-base-200 rounded p-2 bg-base-200/30 flex flex-col gap-1.5">' +
                                '<div class="flex items-center gap-2">' +
                                    '<input type="checkbox" class="checkbox checkbox-primary checkbox-xs bucket-chk" checked data-group="' + gIdx + '" data-bucket="' + bIdx + '" onchange="onBucketCheckboxToggle(' + gIdx + ', ' + bIdx + ', this.checked)">' +
                                    '<span class="text-xs font-bold text-primary">📁</span>' +
                                    '<input type="text" class="input input-bordered input-xs font-bold flex-1 bucket-name-input" value="' + escapeHtml(b.name) + '" data-group="' + gIdx + '" data-bucket="' + bIdx + '">' +
                                '</div>' +
                                (targetsHtml ? '<div class="flex flex-col">' + targetsHtml + '</div>' : '') +
                            '</div>';
                    });
                }

                groupEl.innerHTML = 
                    '<div class="flex items-center justify-between pb-1">' +
                        '<span class="font-extrabold text-xs uppercase tracking-wider text-base-content/70">' + escapeHtml(g.group_name || 'General') + '</span>' +
                    '</div>' +
                    '<div class="flex flex-col gap-2">' + bucketsHtml + '</div>';

                container.appendChild(groupEl);
            });

            document.getElementById('structure_stats_badge').innerText = totalBuckets + ' envelopes, ' + totalTargets + ' targets';
            document.getElementById('structure_select_all').checked = true;
            document.getElementById('structure_preview_wrapper').classList.remove('hidden');
            document.getElementById('btn_apply_structure').classList.remove('hidden');
        }

        function escapeHtml(str) {
            return (str || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
        }

        function onBucketCheckboxToggle(gIdx, bIdx, checked) {
            const targets = document.querySelectorAll('.target-chk[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
            targets.forEach(t => t.checked = checked);
            updateCountsBadge();
        }

        function toggleSelectAllStructure(checked) {
            document.querySelectorAll('.bucket-chk, .target-chk').forEach(c => c.checked = checked);
            updateCountsBadge();
        }

        function updateCountsBadge() {
            const bChecked = document.querySelectorAll('.bucket-chk:checked').length;
            const tChecked = document.querySelectorAll('.target-chk:checked').length;
            document.getElementById('structure_stats_badge').innerText = bChecked + ' envelopes, ' + tChecked + ' targets selected';
        }

        function applyStructureConfirmed() {
            if (!loadedStructureData) return;

            const finalGroups = [];
            loadedStructureData.groups.forEach((g, gIdx) => {
                const finalBuckets = [];
                g.buckets.forEach((b, bIdx) => {
                    const bChk = document.querySelector('.bucket-chk[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
                    if (bChk && bChk.checked) {
                        const nameInput = document.querySelector('.bucket-name-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
                        const bucketName = nameInput ? nameInput.value.trim() : b.name;

                        const finalTargets = [];
                        if (b.targets) {
                            b.targets.forEach((t, tIdx) => {
                                const tChk = document.querySelector('.target-chk[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"][data-target="' + tIdx + '"]');
                                if (tChk && tChk.checked) {
                                    const tNameInput = document.querySelector('.target-name-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"][data-target="' + tIdx + '"]');
                                    const tAmtInput = document.querySelector('.target-amount-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"][data-target="' + tIdx + '"]');
                                    const tName = tNameInput ? tNameInput.value.trim() : t.name;
                                    let amtMinor = t.amount_minor;
                                    if (tAmtInput) {
                                        const parsed = parseFloat(tAmtInput.value);
                                        if (!isNaN(parsed) && parsed >= 0) {
                                            amtMinor = Math.round(parsed * 100);
                                        }
                                    }
                                    finalTargets.push({
                                        name: tName,
                                        target_type: t.target_type || 'EXPENSE',
                                        priority: t.priority || 2,
                                        amount_minor: amtMinor,
                                        currency: t.currency || currentCurrency,
                                        recurrence_cadence: t.recurrence_cadence || 'MONTHLY',
                                        recurrence_days: t.recurrence_days || null,
                                        non_spending_strategy: t.non_spending_strategy || 'RETURN_TO_POOL'
                                    });
                                }
                            });
                        }

                        finalBuckets.push({
                            name: bucketName,
                            targets: finalTargets
                        });
                    }
                });

                if (finalBuckets.length > 0) {
                    finalGroups.push({
                        group_name: g.group_name || 'Default',
                        buckets: finalBuckets
                    });
                }
            });

            if (finalGroups.length === 0) {
                showStructureError('No envelopes or targets selected.');
                return;
            }

            const btn = document.getElementById('btn_apply_structure');
            btn.disabled = true;
            btn.innerHTML = '<span class="loading loading-spinner loading-xs"></span> Applying...';

            fetch('/budgets/' + currentBudgetID + '/structure/apply', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-CSRF-Token': csrfTokenVal
                },
                body: JSON.stringify({ groups: finalGroups })
            })
            .then(res => res.json())
            .then(data => {
                if (data.error) {
                    btn.disabled = false;
                    btn.innerHTML = '<span>✓</span><span>Apply Selected Structure</span>';
                    showStructureError(data.error);
                } else {
                    window.location.reload();
                }
            })
            .catch(err => {
                btn.disabled = false;
                btn.innerHTML = '<span>✓</span><span>Apply Selected Structure</span>';
                showStructureError(err.message || 'Failed applying structure');
            });
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
		structureModalHTML,
		budgetID, csrfToken,
		csrfToken,
		csrfToken, defaultCurr, defaultCurr,
		budgetID, csrfToken, defaultCurr, bucketSelectOptions.String(), defaultCurr,
		budgetID, csrfToken, defaultCurr,
		budgetID, csrfToken, defaultCurr, toJSONString(buckets))

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
		       t.recurrence_cadence, t.recurrence_interval, t.due_date, t.non_spending_strategy,
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
		var rInterval sql.NullInt64
		rows.Scan(&t.ID, &t.BucketID, &t.Name, &t.Priority, &t.SortOrder, &t.AmountMinor,
			&t.Currency, &t.RecurrenceCadence, &rInterval, &dueDate,
			&t.NonSpendingStrategy, &ceiling, &t.CurrentFundedMinor)
		if rInterval.Valid {
			t.RecurrenceInterval = int(rInterval.Int64)
		}
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
	rInterval, _ := strconv.Atoi(c.FormValue("recurrence_interval"))
	if rInterval < 1 {
		rInterval, _ = strconv.Atoi(c.FormValue("recurrence_days"))
	}
	isCustom := cadence == budget.CadenceCustomDays || cadence == budget.CadenceCustomMonths || cadence == budget.CadenceCustomYears
	if isCustom && rInterval < 1 {
		switch cadence {
		case budget.CadenceCustomMonths:
			rInterval = 3
		case budget.CadenceCustomYears:
			rInterval = 1
		default:
			rInterval = 90
		}
	} else if !isCustom {
		rInterval = 0
	}

	var dueDate *int64
	if dueStr := strings.TrimSpace(c.FormValue("due_date")); dueStr != "" {
		if t, err := time.Parse("2006-01-02", dueStr); err == nil {
			u := t.UTC().UnixMicro()
			dueDate = &u
		}
	}
	strategy := c.FormValue("non_spending_strategy")
	if strategy == "" {
		strategy = budget.StrategyRollOver
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	amountMode := c.FormValue("amount_mode")
	if amountMode == "" {
		amountMode = "FIXED"
	}
	var luaScript *string
	if amountMode == "LUA_FORMULA" {
		script := strings.TrimSpace(c.FormValue("lua_script"))
		if script != "" {
			luaScript = &script
		}
	}

	var ceilingMinor *int64
	if ceilingStr := strings.TrimSpace(c.FormValue("ceiling_amount")); ceilingStr != "" {
		var ceilFloat float64
		if _, err := fmt.Sscanf(ceilingStr, "%f", &ceilFloat); err == nil && ceilFloat > 0 {
			cMinor := int64(ceilFloat * 100)
			ceilingMinor = &cMinor
		}
	}

	_, err := budget.CreateTarget(c.Request().Context(), tenantDB, budget.CreateTargetInput{
		BucketID:            bucketID,
		Name:                name,
		TargetType:          tType,
		Priority:            priority,
		AmountMode:          amountMode,
		AmountMinor:         amtMinor,
		LuaScript:           luaScript,
		Currency:            currency,
		RecurrenceCadence:   cadence,
		RecurrenceInterval:  rInterval,
		DueDate:             dueDate,
		NonSpendingStrategy: strategy,
		CeilingAmountMinor:  ceilingMinor,
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
	rInterval, _ := strconv.Atoi(c.FormValue("recurrence_interval"))
	if rInterval < 1 {
		rInterval, _ = strconv.Atoi(c.FormValue("recurrence_days"))
	}
	isCustom := cadence == budget.CadenceCustomDays || cadence == budget.CadenceCustomMonths || cadence == budget.CadenceCustomYears
	if isCustom && rInterval < 1 {
		switch cadence {
		case budget.CadenceCustomMonths:
			rInterval = 3
		case budget.CadenceCustomYears:
			rInterval = 1
		default:
			rInterval = 90
		}
	} else if !isCustom {
		rInterval = 0
	}

	var dueDate *int64
	if dueStr := strings.TrimSpace(c.FormValue("due_date")); dueStr != "" {
		if t, err := time.Parse("2006-01-02", dueStr); err == nil {
			u := t.UTC().UnixMicro()
			dueDate = &u
		}
	}
	strategy := c.FormValue("non_spending_strategy")
	if strategy == "" {
		strategy = budget.StrategyRollOver
	}
	amtStr := c.FormValue("amount")
	var amtFloat float64
	fmt.Sscanf(amtStr, "%f", &amtFloat)
	amtMinor := int64(amtFloat * 100)

	amountMode := c.FormValue("amount_mode")
	if amountMode == "" {
		amountMode = "FIXED"
	}
	var luaScript *string
	if amountMode == "LUA_FORMULA" {
		script := strings.TrimSpace(c.FormValue("lua_script"))
		if script != "" {
			luaScript = &script
		}
	}

	var ceilingMinor *int64
	if ceilingStr := strings.TrimSpace(c.FormValue("ceiling_amount")); ceilingStr != "" {
		var ceilFloat float64
		if _, err := fmt.Sscanf(ceilingStr, "%f", &ceilFloat); err == nil && ceilFloat > 0 {
			cMinor := int64(ceilFloat * 100)
			ceilingMinor = &cMinor
		}
	}

	err := budget.UpdateTarget(c.Request().Context(), tenantDB, budget.UpdateTargetInput{
		ID:                  targetID,
		Name:                name,
		TargetType:          tType,
		Priority:            priority,
		AmountMode:          amountMode,
		AmountMinor:         amtMinor,
		LuaScript:           luaScript,
		RecurrenceCadence:   cadence,
		RecurrenceInterval:  rInterval,
		DueDate:             dueDate,
		NonSpendingStrategy: strategy,
		CeilingAmountMinor:  ceilingMinor,
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

// GenerateStructurePreview produces a proposed budget structure from an AI prompt.
func (h *WorkspaceHandler) GenerateStructurePreview(c echo.Context) error {
	budgetID := c.Param("budgetId")
	var req struct {
		Prompt   string `json:"prompt"`
		Currency string `json:"currency"`
	}
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Budget description is required"})
	}

	curr := strings.TrimSpace(req.Currency)
	if curr == "" {
		_ = h.mainDB.QueryRowContext(c.Request().Context(), `SELECT default_currency FROM budgets WHERE id = ?`, budgetID).Scan(&curr)
	}
	if curr == "" {
		curr = "PLN"
	}

	if h.structureAI == nil {
		h.structureAI = ai.NewBudgetStructureProvider()
	}

	res, err := h.structureAI.GenerateBudgetStructure(c.Request().Context(), ai.BudgetStructurePromptRequest{
		Prompt:          req.Prompt,
		DefaultCurrency: curr,
	})
	if err != nil {
		c.Set("error", err.Error())
		logger.Error(c.Request().Context(), "Failed generating budget structure preview with AI",
			"budget_id", budgetID,
			"error", err.Error(),
		)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, res)
}

// PreviewSourceStructure reads and returns the existing structure of a source budget.
func (h *WorkspaceHandler) PreviewSourceStructure(c echo.Context) error {
	sourceBudgetID := c.QueryParam("source_budget_id")
	if sourceBudgetID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source_budget_id is required"})
	}

	userID := c.Get(ContextUserIDKey).(string)
	var memberCount int
	err := h.mainDB.QueryRowContext(c.Request().Context(), `SELECT count(*) FROM budget_members WHERE budget_id = ? AND user_id = ?`, sourceBudgetID, userID).Scan(&memberCount)
	if err != nil || memberCount == 0 {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "Access denied to source budget"})
	}

	exported, err := h.appService.ExportBudgetStructure(c.Request().Context(), sourceBudgetID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed loading source structure"})
	}

	return c.JSON(http.StatusOK, exported)
}

// ApplyStructure applies a selected budget structure to the target budget.
func (h *WorkspaceHandler) ApplyStructure(c echo.Context) error {
	budgetID := c.Param("budgetId")
	userID := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationID").(string)

	var structure ai.BudgetStructure
	if err := c.Bind(&structure); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid structure payload"})
	}

	bCount, tCount, err := h.appService.ApplyBudgetStructure(c.Request().Context(), budgetID, &structure, userID, corrID)
	if err != nil {
		c.Set("error", err.Error())
		logger.Error(c.Request().Context(), "Failed applying budget structure",
			"budget_id", budgetID,
			"error", err.Error(),
		)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"success":         true,
		"buckets_created": bCount,
		"targets_created": tCount,
		"redirect":        fmt.Sprintf("/budgets/%s/budget", budgetID),
	})
}
