package handlers

import (
	"database/sql"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
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
						<a href="/budgets/%s/parties/%s/rules" class="btn btn-ghost btn-xs text-primary font-semibold">Rules</a>
					</div>
				</td>
			</tr>`, icon, name, pType, badge, acctCount, budgetID, id, budgetID, id))
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

        <main class="flex-1 p-4 sm:p-6 md:p-8 pb-24 md:pb-8 overflow-y-auto">
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

type partyRuleViewData struct {
	ID            string
	Name          string
	Priority      int
	MatchField    string
	MatchOperator string
	MatchPattern  string
	BucketID      string
	BucketName    string
}

// PartyRulesView renders the dedicated page with matching rules for a specific party.
func (h *PartiesHandler) PartyRulesView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Verify party exists
	var partyName, partyType string
	var isUserOwned int
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT name, party_type, is_user_owned
		FROM parties_current
		WHERE id = ?`, partyID).Scan(&partyName, &partyType, &isUserOwned)
	if err != nil {
		if err == sql.ErrNoRows {
			return echo.NewHTTPError(http.StatusNotFound, "Party not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading party: "+err.Error())
	}

	// Fetch rules for this party (matching rules whose target_counterparty_account_id belongs to this party)
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT mr.id, mr.name, mr.priority, mr.match_field, mr.match_operator, mr.match_pattern,
		       COALESCE(mr.default_bucket_id, ''), COALESCE(bp.name, '')
		FROM matching_rules_current mr
		JOIN accounts_current a ON mr.target_counterparty_account_id = a.id
		LEFT JOIN buckets_projection bp ON mr.default_bucket_id = bp.id
		WHERE a.party_id = ?
		ORDER BY mr.priority ASC, mr.name ASC`, partyID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading matching rules: "+err.Error())
	}
	defer rows.Close()

	var rules []partyRuleViewData
	for rows.Next() {
		var r partyRuleViewData
		if err := rows.Scan(&r.ID, &r.Name, &r.Priority, &r.MatchField, &r.MatchOperator, &r.MatchPattern, &r.BucketID, &r.BucketName); err == nil {
			rules = append(rules, r)
		}
	}

	// Fetch buckets for dropdown
	bRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name
		FROM buckets_projection
		WHERE is_archived = 0
		ORDER BY name ASC`)
	var bucketOptions strings.Builder
	bucketOptions.WriteString(`<option value="">— None (Classification Only) —</option>`)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bID, bName string
			if err := bRows.Scan(&bID, &bName); err == nil {
				bucketOptions.WriteString(fmt.Sprintf(`<option value="%s">%s</option>`, bID, html.EscapeString(bName)))
			}
		}
	}

	var rulesRows strings.Builder
	for _, r := range rules {
		bucketBadge := `<span class="badge badge-ghost badge-sm text-base-content/50">None</span>`
		if r.BucketName != "" {
			bucketBadge = fmt.Sprintf(`<span class="badge badge-soft badge-primary badge-sm font-semibold">%s</span>`, html.EscapeString(r.BucketName))
		}

		rulesRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="font-bold text-sm">%s</div>
				</td>
				<td>
					<span class="badge badge-neutral badge-sm font-mono">%s</span>
				</td>
				<td>
					<span class="badge badge-ghost badge-sm font-semibold">%s</span>
				</td>
				<td>
					<code class="text-xs bg-base-300/60 px-2 py-0.5 rounded font-mono">%s</code>
				</td>
				<td>%s</td>
				<td class="text-center font-mono font-bold text-sm">%d</td>
				<td class="text-right">
					<form method="POST" action="/budgets/%s/parties/%s/rules/%s/delete" onsubmit="return confirm('Are you sure you want to delete this matching rule?')">
						<input type="hidden" name="csrf_token" value="%s" />
						<button type="submit" class="btn btn-ghost btn-xs text-error font-semibold">Delete</button>
					</form>
				</td>
			</tr>`,
			html.EscapeString(r.Name),
			html.EscapeString(r.MatchField),
			html.EscapeString(r.MatchOperator),
			html.EscapeString(r.MatchPattern),
			bucketBadge,
			r.Priority,
			budgetID, partyID, r.ID,
			c.Get("csrfToken"),
		))
	}

	if len(rules) == 0 {
		rulesRows.WriteString(`
			<tr>
				<td colspan="7" class="text-center py-8 text-base-content/50">
					No auto-matching rules configured for this counterparty. Add a rule below to automatically map bank payees.
				</td>
			</tr>
		`)
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Matching Rules for %s</title>
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

        <main class="flex-1 p-4 sm:p-6 md:p-8 pb-24 md:pb-8 overflow-y-auto">
            <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
                <div>
                    <div class="flex items-center gap-2 text-xs text-base-content/60 mb-1">
                        <a href="/budgets/%s/parties" class="hover:underline flex items-center gap-1">
                            <span>←</span>
                            <span>All Counterparties</span>
                        </a>
                        <span>/</span>
                        <span class="font-medium text-base-content">%s</span>
                    </div>
                    <h1 class="text-2xl font-black tracking-tight">Auto-Matching Rules</h1>
                    <p class="text-xs text-base-content/60 mt-1">Configure patterns to automatically classify raw bank transactions into <strong>%s</strong>.</p>
                </div>
                <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="new_rule_modal.showModal()">
                    <span>+</span>
                    <span>New Rule</span>
                </button>
            </div>

            <div class="card bg-base-100 border border-base-300 shadow-sm overflow-hidden mb-6">
                <div class="overflow-x-auto">
                    <table class="table table-zebra w-full">
                        <thead>
                            <tr class="bg-base-200/50 text-base-content/70">
                                <th>Rule Name</th>
                                <th>Field</th>
                                <th>Operator</th>
                                <th>Pattern</th>
                                <th>Default Envelope</th>
                                <th class="text-center">Priority</th>
                                <th class="text-right">Actions</th>
                            </tr>
                        </thead>
                        <tbody>
                            %s
                        </tbody>
                    </table>
                </div>
            </div>
        </main>
    </div>

    %s

    <!-- New Matching Rule Modal -->
    <dialog id="new_rule_modal" class="modal">
        <div class="modal-box">
            <h3 class="font-bold text-lg mb-2">Create Auto-Matching Rule</h3>
            <p class="text-xs text-base-content/60 mb-4">When a raw bank transaction matches this pattern, it will automatically link to <strong>%s</strong>.</p>
            <form method="POST" action="/budgets/%s/parties/%s/rules" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                
                <div class="form-control">
                    <label class="label"><span class="label-text font-semibold text-xs">Rule Name</span></label>
                    <input type="text" name="name" required placeholder="e.g. Zabka Grocery Store" class="input input-bordered w-full" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Match Field</span></label>
                        <select name="match_field" class="select select-bordered w-full">
                            <option value="RAW_PAYEE" selected>RAW_PAYEE (Bank Payee Name)</option>
                            <option value="COUNTERPARTY_IBAN">COUNTERPARTY_IBAN (Account Number)</option>
                            <option value="REFERENCE">REFERENCE (Remittance Info)</option>
                            <option value="ANY">ANY (All Fields)</option>
                        </select>
                    </div>

                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Match Operator</span></label>
                        <select name="match_operator" class="select select-bordered w-full">
                            <option value="CONTAINS" selected>CONTAINS (Substring)</option>
                            <option value="PREFIX">PREFIX (Starts With)</option>
                            <option value="EXACT">EXACT (Full Match)</option>
                            <option value="REGEX">REGEX (RE2 Regular Expression)</option>
                        </select>
                    </div>
                </div>

                <div class="form-control">
                    <label class="label"><span class="label-text font-semibold text-xs">Pattern</span></label>
                    <input type="text" name="match_pattern" required placeholder="e.g. ZABKA or ^ŻABKA.*" class="input input-bordered w-full font-mono text-sm" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Default Envelope (Optional)</span></label>
                        <select name="default_bucket_id" class="select select-bordered w-full">
                            %s
                        </select>
                    </div>

                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Priority</span></label>
                        <input type="number" name="priority" value="100" min="1" max="999" class="input input-bordered w-full font-mono" />
                        <span class="text-[10px] text-base-content/50 mt-1">Lower numbers are evaluated first.</span>
                    </div>
                </div>

                <div class="modal-action">
                    <button type="button" class="btn btn-ghost" onclick="new_rule_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Create Rule</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
</body>
</html>`,
		html.EscapeString(partyName),
		csrfToken,
		RenderSidebarNavigation(budgetID, "parties"),
		budgetID,
		html.EscapeString(partyName),
		html.EscapeString(partyName),
		rulesRows.String(),
		RenderBottomDock(budgetID, "parties"),
		html.EscapeString(partyName),
		budgetID, partyID,
		csrfToken,
		bucketOptions.String(),
	)

	return c.HTML(http.StatusOK, htmlContent)
}

// CreatePartyRule creates a new auto-matching rule for a counterparty.
func (h *PartiesHandler) CreatePartyRule(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	matchField := strings.TrimSpace(c.FormValue("match_field"))
	matchOperator := strings.TrimSpace(c.FormValue("match_operator"))
	matchPattern := strings.TrimSpace(c.FormValue("match_pattern"))
	priorityStr := strings.TrimSpace(c.FormValue("priority"))
	defaultBucketID := strings.TrimSpace(c.FormValue("default_bucket_id"))

	if name == "" || matchPattern == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name and Pattern are required")
	}

	validFields := map[string]bool{
		"RAW_PAYEE":         true,
		"COUNTERPARTY_IBAN": true,
		"REFERENCE":         true,
		"ANY":               true,
	}
	if !validFields[matchField] {
		matchField = "RAW_PAYEE"
	}

	validOps := map[string]bool{
		"CONTAINS": true,
		"PREFIX":   true,
		"EXACT":    true,
		"REGEX":    true,
	}
	if !validOps[matchOperator] {
		matchOperator = "CONTAINS"
	}

	// Validate RE2 regex safety if operator is REGEX
	if matchOperator == "REGEX" {
		if _, err := regexp.Compile(matchPattern); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid regular expression pattern: "+err.Error())
		}
	}

	priority := 100
	if p, err := strconv.Atoi(priorityStr); err == nil && p > 0 {
		priority = p
	}

	// Locate counterparty's account ID
	var targetAccountID string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT id FROM accounts_current WHERE party_id = ? LIMIT 1`, partyID).Scan(&targetAccountID)
	if err != nil {
		if err == sql.ErrNoRows {
			return echo.NewHTTPError(http.StatusBadRequest, "Counterparty has no associated account")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed locating party account: "+err.Error())
	}

	var bucketIDPtr *string
	if defaultBucketID != "" {
		bucketIDPtr = &defaultBucketID
	}

	ruleID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error: "+err.Error())
	}
	defer tx.Rollback()

	// Append rule to matching_rules
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO matching_rules (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
		ruleID, name, priority, matchField, matchOperator, matchPattern,
		targetAccountID, bucketIDPtr, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule: "+err.Error())
	}

	// Projection update to matching_rules_current
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO matching_rules_current (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ruleID, name, priority, matchField, matchOperator, matchPattern,
		targetAccountID, bucketIDPtr, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating matching rules projection: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}

// DeletePartyRule physically deletes a matching rule from both matching_rules and matching_rules_current.
func (h *PartiesHandler) DeletePartyRule(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	ruleID := c.Param("ruleId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error: "+err.Error())
	}
	defer tx.Rollback()

	// Hard DELETE per user specification
	_, err = tx.ExecContext(c.Request().Context(), `DELETE FROM matching_rules_current WHERE id = ?`, ruleID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed deleting from matching_rules_current: "+err.Error())
	}

	_, err = tx.ExecContext(c.Request().Context(), `DELETE FROM matching_rules WHERE id = ?`, ruleID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed deleting from matching_rules: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}
