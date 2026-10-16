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
	for rows.Next() {
		var id, name, pType string
		var isUserOwned, acctCount int
		rows.Scan(&id, &name, &pType, &isUserOwned, &acctCount)

		badge := "<span class=\"badge badge-outline\">Counterparty</span>"
		if isUserOwned == 1 {
			badge = "<span class=\"badge badge-primary\">User Owned</span>"
		}

		partyRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td class="font-bold">%s</td>
				<td>%s</td>
				<td>%s</td>
				<td class="text-center font-mono">%d</td>
			</tr>`, name, badge, pType, acctCount))
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - Parties</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5.0.0/dist/full.min.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
</head>
<body class="bg-base-200 min-h-screen flex flex-col">
    <header class="navbar bg-base-100 border-b border-base-300 px-6">
        <div class="flex-1 gap-4 items-center">
            <a href="/budgets" class="text-xl font-bold tracking-tight text-primary">Balance</a>
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
                <li><a href="/budgets/%s/transactions">2. Transactions</a></li>
                <li><a href="/budgets/%s/reports">3. Reports</a></li>
                <li><a href="/budgets/%s/parties" class="active font-bold">4. External Parties</a></li>
                <li><a href="/budgets/%s/members">5. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 overflow-y-auto">
            <div class="flex justify-between items-center mb-6">
                <div>
                    <h1 class="text-2xl font-bold">Parties & Counterparties</h1>
                    <p class="text-xs opacity-60">Manage merchants (Żabka), employers, and external accounts.</p>
                </div>
                <button class="btn btn-primary btn-sm" onclick="new_party_modal.showModal()">+ New Counterparty</button>
            </div>

            <div class="overflow-x-auto bg-base-100 rounded-box border border-base-300 shadow">
                <table class="table table-zebra">
                    <thead>
                        <tr>
                            <th>Party Name</th>
                            <th>Ownership</th>
                            <th>Type</th>
                            <th class="text-center">Accounts</th>
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
        <a href="/budgets/%s/transactions">Txns</a>
        <a href="/budgets/%s/reports">Reports</a>
        <a href="/budgets/%s/parties" class="active">Parties</a>
        <a href="/budgets/%s/members">Members</a>
    </div>

    <!-- New Counterparty Modal -->
    <dialog id="new_party_modal" class="modal">
        <div class="modal-box">
            <h3 class="font-bold text-lg mb-4">Add Counterparty Party</h3>
            <form method="POST" action="/budgets/%s/parties" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div>
                    <label class="label"><span class="label-text">Party Name</span></label>
                    <input type="text" name="name" required placeholder="e.g. Żabka, Employer, Landlord" class="input input-bordered w-full" />
                </div>
                <div>
                    <label class="label"><span class="label-text">Type</span></label>
                    <select name="party_type" class="select select-bordered w-full">
                        <option value="ORGANIZATION" selected>Organization / Merchant</option>
                        <option value="INDIVIDUAL">Individual / Person</option>
                    </select>
                </div>
                <div class="modal-action">
                    <button type="button" class="btn" onclick="new_party_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary">Save Party</button>
                </div>
            </form>
        </div>
    </dialog>
</body>
</html>`, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID,
		partyRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID,
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

	// Create virtual external account for this party
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
		acctID, partyID, name+" Account", userID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create external account")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties", budgetID))
}
