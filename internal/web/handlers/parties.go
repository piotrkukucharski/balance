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

type PartiesHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

func NewPartiesHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *PartiesHandler {
	return &PartiesHandler{mainDB: mainDB, tenantMgr: tenantMgr}
}

// PartiesView renders external counterparties, matching rules and counterparty cashflow.
func (h *PartiesHandler) PartiesView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT p.id, p.name, p.party_type, p.is_user_owned, COUNT(a.id)
		FROM parties_current p
		LEFT JOIN accounts_current a ON p.id = a.party_id
		GROUP BY p.id, p.name, p.party_type, p.is_user_owned
		ORDER BY p.is_user_owned DESC, p.name ASC`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading parties")
	}
	defer rows.Close()

	var partyRows strings.Builder
	hasParties := false
	for rows.Next() {
		hasParties = true
		var id, name, pType string
		var isUserOwned, acctCount int
		rows.Scan(&id, &name, &pType, &isUserOwned, &acctCount)

		badge := "<span class=\"badge badge-soft badge-neutral font-semibold text-xs\">External Counterparty</span>"
		icon := "🏬"
		if isUserOwned == 1 {
			badge = "<span class=\"badge badge-soft badge-primary font-semibold text-xs\">User Owned</span>"
			icon = "👤"
		}

		partyRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="flex items-center gap-3">
						<span class="text-xl">%s</span>
						<div>
							<div class="font-bold text-sm">%s</div>
							<div class="text-xs text-base-content/50">%s</div>
						</div>
					</div>
				</td>
				<td>%s</td>
				<td class="text-center font-mono font-bold text-sm">%d</td>
				<td class="text-right">
					<div class="flex items-center justify-end gap-1">
						<button class="btn btn-ghost btn-xs text-info font-semibold"
							hx-get="/budgets/%s/history/parties/%s"
							hx-target="body"
							hx-swap="beforeend">
							History
						</button>
						<button class="btn btn-ghost btn-xs text-primary font-semibold">Rules</button>
					</div>
				</td>
			</tr>`, icon, name, pType, badge, acctCount, budgetID, id))
	}

	if !hasParties {
		partyRows.WriteString(`
			<tr>
				<td colspan="4" class="text-center py-8 text-base-content/50">
					No counterparties configured. Add merchants like Żabka to track spend.
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
    <title>Balance — Parties & Counterparties</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
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
                <span>Parties</span>
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

        <main class="flex-1 p-6 md:p-8 overflow-y-auto">
            <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
                <div>
                    <h1 class="text-2xl font-black tracking-tight">Parties & Counterparties</h1>
                    <p class="text-xs text-base-content/60 mt-1">Manage merchants (Żabka), employers, landlords, and external accounts.</p>
                </div>
                <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="new_party_modal.showModal()">
                    <span>+</span>
                    <span>New Counterparty</span>
                </button>
            </div>

            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra w-full">
                            <thead>
                                <tr class="bg-base-200/50">
                                    <th class="font-bold">Party / Counterparty</th>
                                    <th class="font-bold">Ownership Status</th>
                                    <th class="font-bold text-center">Accounts</th>
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
    %s

    <!-- New Counterparty Modal -->
    <dialog id="new_party_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Add Counterparty Party</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/parties" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Party Name</span></label>
                    <input type="text" name="name" required placeholder="e.g. Żabka, Employer, Landlord" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Entity Type</span></label>
                    <select name="party_type" class="select select-bordered w-full select-md focus:select-primary">
                        <option value="ORGANIZATION" selected>Organization / Retail Chain</option>
                        <option value="INDIVIDUAL">Individual / Counterparty Person</option>
                    </select>
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="new_party_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Create Party</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
</body>
</html>`, csrfToken,
		RenderSidebarNavigation(budgetID, "parties"),
		partyRows.String(),
		RenderBottomDock(budgetID, "parties"),
		budgetID, csrfToken)

	return c.HTML(http.StatusOK, html)
}

// CreateParty creates a new party and its virtual external account.
func (h *PartiesHandler) CreateParty(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := c.FormValue("name")
	partyType := c.FormValue("party_type")
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name is required")
	}
	if partyType == "" {
		partyType = "ORGANIZATION"
	}

	now := time.Now().UTC().UnixMicro()
	partyID := domain.NewUUIDv7()
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// Append party
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, ?, 0, 'CREATE', ?, ?, ?)`,
		partyID, name, partyType, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to insert party")
	}

	// Update parties_current
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 0, ?, ?)`,
		partyID, name, partyType, userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update parties_current")
	}

	// Create virtual external account
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
		acctID, partyID, name+" POS Account", userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create external account")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties", budgetID))
}
