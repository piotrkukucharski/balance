package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/rule"
	"github.com/labstack/echo/v4"
)

type PartiesHandler struct {
	mainDB          *sql.DB
	tenantMgr       *db.TenantDBManager
	smartRuleAIProv ai.SmartRuleAIProvider
}

func NewPartiesHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *PartiesHandler {
	return &PartiesHandler{
		mainDB:          mainDB,
		tenantMgr:       tenantMgr,
		smartRuleAIProv: ai.NewSmartRuleAIProvider(),
	}
}

func (h *PartiesHandler) SetSmartRuleAIProvider(prov ai.SmartRuleAIProvider) {
	h.smartRuleAIProv = prov
}

type partyAccountInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
	IBAN     string `json:"iban,omitempty"`
}

type partyListItem struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	PartyType   string             `json:"party_type"`
	IsUserOwned int                `json:"is_user_owned"`
	Accounts    []partyAccountInfo `json:"accounts"`
}

// PartiesView renders external counterparties, matching rules and counterparty cashflow.
func (h *PartiesHandler) PartiesView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Fetch all parties
	pRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name, party_type, is_user_owned
		FROM parties_current
		ORDER BY is_user_owned DESC, name ASC`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading parties")
	}
	defer pRows.Close()

	var parties []partyListItem
	partyMap := make(map[string]int)
	for pRows.Next() {
		var it partyListItem
		pRows.Scan(&it.ID, &it.Name, &it.PartyType, &it.IsUserOwned)
		it.Accounts = make([]partyAccountInfo, 0)
		partyMap[it.ID] = len(parties)
		parties = append(parties, it)
	}

	// Fetch all accounts belonging to parties
	aRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, party_id, name, account_type, primary_currency, COALESCE(iban, '')
		FROM accounts_current
		ORDER BY name ASC`)
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var aID, pID, aName, aType, aCurr, aIBAN string
			aRows.Scan(&aID, &pID, &aName, &aType, &aCurr, &aIBAN)
			if idx, ok := partyMap[pID]; ok {
				parties[idx].Accounts = append(parties[idx].Accounts, partyAccountInfo{
					ID:       aID,
					Name:     aName,
					Type:     aType,
					Currency: aCurr,
					IBAN:     aIBAN,
				})
			}
		}
	}

	var partyRows strings.Builder
	for _, p := range parties {
		badge := `<span class="badge badge-neutral text-base-content font-bold text-xs">External Counterparty</span>`
		icon := "🏬"
		if p.IsUserOwned == 1 {
			badge = `<span class="badge badge-primary font-bold text-xs">User Owned</span>`
			icon = "👤"
		}

		// Accounts display
		var acctsHTML strings.Builder
		if len(p.Accounts) == 0 {
			acctsHTML.WriteString(`<span class="text-xs text-base-content/40 italic">No accounts</span>`)
		} else {
			acctsHTML.WriteString(`<div class="flex flex-col gap-1.5 max-w-xs">`)
			for _, acct := range p.Accounts {
				ibanBadge := ""
				if acct.IBAN != "" {
					ibanBadge = fmt.Sprintf(`<span class="font-mono text-[10px] text-base-content/50 truncate block" title="%s">%s</span>`, html.EscapeString(acct.IBAN), html.EscapeString(acct.IBAN))
				}
				acctsHTML.WriteString(fmt.Sprintf(`
					<div class="flex items-center justify-between gap-2 bg-base-200/60 hover:bg-base-200 px-2.5 py-1 rounded-lg border border-base-300/50">
						<div class="min-w-0">
							<span class="text-xs font-semibold truncate block">%s</span>
							%s
						</div>
						<a href="/budgets/%s/transactions?account_id=%s" 
						   class="btn btn-ghost btn-xs text-primary px-1.5 py-0 h-6 min-h-0 text-[11px] font-semibold gap-1"
						   title="View transactions for %s">
							<span>Txns ↗</span>
						</a>
					</div>`,
					html.EscapeString(acct.Name),
					ibanBadge,
					budgetID, acct.ID,
					html.EscapeString(acct.Name),
				))
			}
			acctsHTML.WriteString(`</div>`)
		}

		safeName := html.EscapeString(p.Name)
		jsSafeName := strings.ReplaceAll(strings.ReplaceAll(p.Name, "\\", "\\\\"), "'", "\\'")

		partyRows.WriteString(fmt.Sprintf(`
			<tr class="hover group">
				<td class="w-10 text-center">
					<input type="checkbox" name="party_ids" value="%s" data-party-name="%s" class="party-select-cb checkbox checkbox-primary checkbox-sm" onchange="updateSelectionBar()" />
				</td>
				<td>
					<div class="flex items-center gap-3">
						<span class="text-xl">%s</span>
						<div>
							<div class="font-bold text-sm flex items-center gap-2">
								<span>%s</span>
								<button class="btn btn-ghost btn-circle btn-xs opacity-0 group-hover:opacity-100 transition-opacity" 
								        onclick="openRenameModal('%s', '%s')" title="Rename party">
									✏️
								</button>
							</div>
							<div class="text-xs text-base-content/50">%s</div>
						</div>
					</div>
				</td>
				<td>%s</td>
				<td>
					%s
				</td>
				<td class="text-right">
					<div class="flex items-center justify-end gap-1">
						<a href="/budgets/%s/transactions?party_id=%s" class="btn btn-ghost btn-xs text-secondary font-semibold" title="View all transactions with this party">
							Txns
						</a>
						<button class="btn btn-ghost btn-xs text-info font-semibold"
							hx-get="/budgets/%s/history/parties/%s"
							hx-target="body"
							hx-swap="beforeend">
							History
						</button>
						<a href="/budgets/%s/parties/%s/rules" class="btn btn-ghost btn-xs text-primary font-semibold">Rules</a>
					</div>
				</td>
			</tr>`,
			p.ID, safeName,
			icon, safeName,
			p.ID, jsSafeName,
			html.EscapeString(p.PartyType),
			badge,
			acctsHTML.String(),
			budgetID, p.ID,
			budgetID, p.ID,
			budgetID, p.ID,
		))
	}

	if len(parties) == 0 {
		partyRows.WriteString(`
			<tr>
				<td colspan="5" class="text-center py-8 text-base-content/50">
					No counterparties configured. Add merchants, employers or external accounts to track spend.
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
            <a href="/budgets/%s/parties/rules" class="btn btn-ghost btn-sm font-semibold gap-1.5">
                <span>⚡</span>
                <span>All Rules</span>
            </a>
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
                    <p class="text-xs text-base-content/60 mt-1">Manage merchants, employers, landlords, and external accounts.</p>
                </div>
                <div class="flex items-center gap-2">
                    <a href="/budgets/%s/parties/rules" class="btn btn-outline btn-sm shadow font-semibold gap-1.5">
                        <span>⚡</span>
                        <span>Manage All Rules</span>
                    </a>
                    <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="new_party_modal.showModal()">
                        <span>+</span>
                        <span>New Counterparty</span>
                    </button>
                </div>
            </div>

            <!-- Floating Batch Selection Bar (Visual mechanism matching Transactions Merge) -->
            <div id="selection_bar" class="hidden sticky top-2 z-20 mb-4 bg-primary text-primary-content px-4 py-3 rounded-2xl shadow-xl flex items-center justify-between animate-in fade-in slide-in-from-top duration-200">
                <div class="flex items-center gap-3">
                    <span class="font-extrabold text-sm"><span id="selected_count">0</span> parties selected</span>
                    <button type="button" class="btn btn-ghost btn-xs text-primary-content hover:bg-primary-focus underline" onclick="clearSelection()">Deselect all</button>
                </div>
                <div class="flex items-center gap-2">
                    <button type="button" id="merge_btn" class="btn btn-neutral btn-sm font-bold shadow-md gap-1.5" onclick="openMergeModal()">
                        <span>🔗</span>
                        <span>Merge Selected Parties</span>
                    </button>
                </div>
            </div>

            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra w-full">
                            <thead>
                                <tr class="bg-base-200/50">
                                    <th class="w-10 text-center">
                                        <input type="checkbox" id="select_all_cb" class="checkbox checkbox-primary checkbox-sm" onchange="toggleSelectAll(this)" />
                                    </th>
                                    <th class="font-bold">Party / Counterparty</th>
                                    <th class="font-bold">Ownership Status</th>
                                    <th class="font-bold">Accounts</th>
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

    <!-- Rename Party Modal -->
    <dialog id="rename_party_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>✏️</span>
                    <span>Rename Party</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form id="rename_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Party Name</span></label>
                    <input type="text" id="rename_name_input" name="name" required class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="rename_party_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Save Changes</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- MERGE PARTIES MODAL (Visual counterpart of transactions merge) -->
    <dialog id="merge_parties_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-3">
                <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>🔗</span>
                    <span>Merge Selected Counterparties</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <p class="text-xs text-base-content/60 mb-4">
                Consolidates multiple parties into a single target party. All accounts and auto-matching rules will be safely transferred to the destination party, and merged parties will be archived.
            </p>
            <form id="merge_parties_form" method="POST" action="/budgets/%s/parties/merge" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div id="merge_hidden_inputs"></div>

                <div class="bg-base-200/70 p-3 rounded-xl border border-base-300 flex justify-between items-center">
                    <div>
                        <div class="text-xs text-base-content/60 font-medium">Selected for Merge</div>
                        <div id="merge_count_label" class="text-sm font-bold">0 parties selected</div>
                    </div>
                </div>

                <fieldset class="fieldset">
                    <legend class="fieldset-legend font-semibold">Target Party (Destination)</legend>
                    <select id="merge_target_party_id" name="target_party_id" class="select select-bordered w-full select-md focus:select-primary" required onchange="handleTargetPartyChange()">
                    </select>
                </fieldset>

                <fieldset class="fieldset">
                    <legend class="fieldset-legend font-semibold">Final Party Name</legend>
                    <input type="text" id="merge_final_name" name="target_name" placeholder="Consolidated party name" class="input input-bordered w-full input-md focus:input-primary" required />
                </fieldset>

                <div class="modal-action mt-4 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="merge_parties_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-bold shadow-md gap-1.5">
                        <span>🔗</span>
                        <span>Confirm Merge</span>
                    </button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <script>
        function updateSelectionBar() {
            const checked = Array.from(document.querySelectorAll('.party-select-cb:checked'));
            const bar = document.getElementById('selection_bar');
            const countLabel = document.getElementById('selected_count');
            const mergeBtn = document.getElementById('merge_btn');

            if (checked.length === 0) {
                bar.classList.add('hidden');
            } else {
                bar.classList.remove('hidden');
                countLabel.innerText = checked.length;
                mergeBtn.disabled = checked.length < 2;
            }
        }

        function toggleSelectAll(masterCb) {
            document.querySelectorAll('.party-select-cb').forEach(cb => {
                cb.checked = masterCb.checked;
            });
            updateSelectionBar();
        }

        function clearSelection() {
            document.querySelectorAll('.party-select-cb').forEach(cb => cb.checked = false);
            const master = document.getElementById('select_all_cb');
            if (master) master.checked = false;
            updateSelectionBar();
        }

        function openRenameModal(partyId, currentName) {
            const form = document.getElementById('rename_form');
            form.action = '/budgets/%s/parties/' + partyId + '/rename';
            document.getElementById('rename_name_input').value = currentName;
            rename_party_modal.showModal();
        }

        function openMergeModal() {
            const checked = Array.from(document.querySelectorAll('.party-select-cb:checked'));
            if (checked.length < 2) return;

            const hiddenContainer = document.getElementById('merge_hidden_inputs');
            hiddenContainer.innerHTML = '';
            const targetSelect = document.getElementById('merge_target_party_id');
            targetSelect.innerHTML = '';

            checked.forEach((cb, idx) => {
                const pId = cb.value;
                const pName = cb.getAttribute('data-party-name') || pId;

                const input = document.createElement('input');
                input.type = 'hidden';
                input.name = 'source_party_ids';
                input.value = pId;
                hiddenContainer.appendChild(input);

                const opt = document.createElement('option');
                opt.value = pId;
                opt.text = pName;
                if (idx === 0) {
                    opt.selected = true;
                    document.getElementById('merge_final_name').value = pName;
                }
                targetSelect.appendChild(opt);
            });

            document.getElementById('merge_count_label').innerText = checked.length + ' counterparties selected';
            merge_parties_modal.showModal();
        }

        function handleTargetPartyChange() {
            const sel = document.getElementById('merge_target_party_id');
            const text = sel.options[sel.selectedIndex].text;
            document.getElementById('merge_final_name').value = text;
        }
    </script>
</body>
</html>`,
		budgetID,
		csrfToken,
		RenderSidebarNavigation(budgetID, "parties"),
		budgetID,
		partyRows.String(),
		RenderBottomDock(budgetID, "parties"),
		budgetID, csrfToken,
		csrfToken,
		budgetID, csrfToken,
		budgetID,
	)

	return c.HTML(http.StatusOK, html)
}

// RenameParty renames an existing party.
func (h *PartiesHandler) RenameParty(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name is required")
	}

	var curVer int
	var curType string
	var curOwned int
	var curNotes sql.NullString
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, party_type, is_user_owned, notes
		FROM parties_current WHERE id = ?`, partyID).Scan(&curVer, &curType, &curOwned, &curNotes)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Party not found")
	}

	now := time.Now().UTC().UnixMicro()
	nextVer := curVer + 1

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		partyID, nextVer, name, curType, curOwned, curNotes.String, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting party event")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE parties_current
		SET version = ?, name = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, name, userID, now, partyID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating party projection")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties", budgetID))
}

// MergeParties merges multiple parties into one target party.
func (h *PartiesHandler) MergeParties(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	targetPartyID := strings.TrimSpace(c.FormValue("target_party_id"))
	targetName := strings.TrimSpace(c.FormValue("target_name"))
	sourcePartyIDs := c.Request().Form["source_party_ids"]

	if targetPartyID == "" || len(sourcePartyIDs) < 2 {
		return echo.NewHTTPError(http.StatusBadRequest, "Must select at least two parties to merge")
	}

	// Verify target party exists
	var curVer int
	var curType string
	var curOwned int
	var curNotes sql.NullString
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, party_type, is_user_owned, notes
		FROM parties_current WHERE id = ?`, targetPartyID).Scan(&curVer, &curType, &curOwned, &curNotes)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Target party not found")
	}

	if targetName == "" {
		targetName = "Merged Party"
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// 1. Update target party name and version
	nextVer := curVer + 1
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		targetPartyID, nextVer, targetName, curType, curOwned, curNotes.String, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating target party event")
	}
	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE parties_current
		SET version = ?, name = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, targetName, userID, now, targetPartyID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating target party current")
	}

	// Find target primary account for matching rules
	var targetAccountID string
	_ = tx.QueryRowContext(c.Request().Context(), `SELECT id FROM accounts_current WHERE party_id = ? LIMIT 1`, targetPartyID).Scan(&targetAccountID)

	// 2. For each other source party: move accounts and matching rules, then SOFT_DELETE the party
	for _, srcID := range sourcePartyIDs {
		if srcID == targetPartyID {
			continue
		}

		var srcVer int
		var srcName, sType string
		var sOwned int
		var sNotes sql.NullString
		sErr := tx.QueryRowContext(c.Request().Context(), `
			SELECT version, name, party_type, is_user_owned, notes
			FROM parties_current WHERE id = ?`, srcID).Scan(&srcVer, &srcName, &sType, &sOwned, &sNotes)
		if sErr != nil {
			continue
		}

		// Re-assign accounts
		_, err = tx.ExecContext(c.Request().Context(), `
			UPDATE accounts_current SET party_id = ?, updated_at = ? WHERE party_id = ?`,
			targetPartyID, now, srcID,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed transferring accounts")
		}

		// Re-assign matching rules if target account exists
		if targetAccountID != "" {
			_, err = tx.ExecContext(c.Request().Context(), `
				UPDATE matching_rules_current SET target_counterparty_account_id = ?, updated_at = ?
				WHERE target_counterparty_account_id IN (SELECT id FROM accounts_current WHERE party_id = ?)`,
				targetAccountID, now, srcID,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed transferring matching rules")
			}
		}

		// Soft delete source party
		sNextVer := srcVer + 1
		_, err = tx.ExecContext(c.Request().Context(), `
			INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'SOFT_DELETE', ?, ?, ?)`,
			srcID, sNextVer, srcName, sType, sOwned, sNotes.String, userID, corrID, now,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed soft-deleting merged party event")
		}

		_, err = tx.ExecContext(c.Request().Context(), `DELETE FROM parties_current WHERE id = ?`, srcID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed removing merged party from projection")
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties", budgetID))
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
	ID            string `json:"id"`
	PartyID       string `json:"party_id"`
	PartyName     string `json:"party_name"`
	Name          string `json:"name"`
	Priority      int    `json:"priority"`
	MatchField    string `json:"match_field"`
	MatchOperator string `json:"match_operator"`
	MatchPattern  string `json:"match_pattern"`
	BucketID      string `json:"bucket_id"`
	BucketName    string `json:"bucket_name"`
}

// AllRulesView renders the global rules page at /budgets/{budgetId}/parties/rules.
func (h *PartiesHandler) AllRulesView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Fetch all rules with party and bucket information
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT mr.id, p.id, p.name, mr.name, mr.priority, mr.match_field, mr.match_operator, mr.match_pattern,
		       COALESCE(mr.default_bucket_id, ''), COALESCE(bp.name, '')
		FROM matching_rules_current mr
		JOIN accounts_current a ON mr.target_counterparty_account_id = a.id
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN buckets_projection bp ON mr.default_bucket_id = bp.id
		ORDER BY mr.priority ASC, mr.name ASC`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading rules: "+err.Error())
	}
	defer rows.Close()

	var rules []partyRuleViewData
	for rows.Next() {
		var r partyRuleViewData
		if err := rows.Scan(&r.ID, &r.PartyID, &r.PartyName, &r.Name, &r.Priority, &r.MatchField, &r.MatchOperator, &r.MatchPattern, &r.BucketID, &r.BucketName); err == nil {
			rules = append(rules, r)
		}
	}

	// Fetch buckets
	bRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name FROM buckets_projection WHERE is_archived = 0 ORDER BY name ASC`)
	var bucketOptions strings.Builder
	bucketOptions.WriteString(`<option value="">— None (Classification Only) —</option>`)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bID, bName string
			bRows.Scan(&bID, &bName)
			bucketOptions.WriteString(fmt.Sprintf(`<option value="%s">%s</option>`, bID, html.EscapeString(bName)))
		}
	}

	// Fetch parties for rule assignment
	partyRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name FROM parties_current ORDER BY name ASC`)
	var partyOptions strings.Builder
	if err == nil {
		defer partyRows.Close()
		for partyRows.Next() {
			var pID, pName string
			partyRows.Scan(&pID, &pName)
			partyOptions.WriteString(fmt.Sprintf(`<option value="%s">%s</option>`, pID, html.EscapeString(pName)))
		}
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	var rulesRows strings.Builder
	for _, r := range rules {
		bucketBadge := `<span class="badge badge-ghost badge-sm text-base-content/50">None</span>`
		if r.BucketName != "" {
			bucketBadge = fmt.Sprintf(`<span class="badge badge-soft badge-primary badge-sm font-semibold">%s</span>`, html.EscapeString(r.BucketName))
		}

		rJSON, _ := json.Marshal(r)
		escapedJSON := html.EscapeString(string(rJSON))

		rulesRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="font-bold text-sm">%s</div>
				</td>
				<td>
					<a href="/budgets/%s/parties/%s/rules" class="text-xs font-semibold text-primary hover:underline flex items-center gap-1">
						<span>🏬</span>
						<span>%s</span>
					</a>
				</td>
				<td><span class="badge badge-neutral badge-sm font-mono">%s</span></td>
				<td><span class="badge badge-ghost badge-sm font-semibold">%s</span></td>
				<td><code class="text-xs bg-base-300/60 px-2 py-0.5 rounded font-mono">%s</code></td>
				<td>%s</td>
				<td class="text-center font-mono font-bold text-sm">%d</td>
				<td class="text-right">
					<div class="flex items-center justify-end gap-1">
						<button type="button" class="btn btn-ghost btn-xs text-info font-semibold" onclick='openEditRuleModal(%s)'>
							Edit
						</button>
						<form method="POST" action="/budgets/%s/parties/%s/rules/%s/delete?redirect=/budgets/%s/parties/rules" onsubmit="return confirm('Delete this matching rule?')">
							<input type="hidden" name="csrf_token" value="%s" />
							<button type="submit" class="btn btn-ghost btn-xs text-error font-semibold">Delete</button>
						</form>
					</div>
				</td>
			</tr>`,
			html.EscapeString(r.Name),
			budgetID, r.PartyID, html.EscapeString(r.PartyName),
			html.EscapeString(r.MatchField),
			html.EscapeString(r.MatchOperator),
			html.EscapeString(r.MatchPattern),
			bucketBadge,
			r.Priority,
			escapedJSON,
			budgetID, r.PartyID, r.ID, budgetID,
			csrfToken,
		))
	}

	if len(rules) == 0 {
		rulesRows.WriteString(`
			<tr>
				<td colspan="8" class="text-center py-8 text-base-content/50">
					No auto-matching rules configured. Click "Smart new rule" to have AI propose rules from unassigned transactions, or create one manually.
				</td>
			</tr>
		`)
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — All Auto-Matching Rules</title>
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
                <span>Rules</span>
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
                            <span>Parties & Counterparties</span>
                        </a>
                        <span>/</span>
                        <span class="font-medium text-base-content">All Auto-Matching Rules</span>
                    </div>
                    <h1 class="text-2xl font-black tracking-tight">Auto-Matching Rules</h1>
                    <p class="text-xs text-base-content/60 mt-1">Global priority-ordered pattern rules for classifying bank feeds into counterparties and envelopes.</p>
                </div>
                <div class="flex items-center gap-2">
                    <button class="btn btn-secondary btn-sm shadow font-semibold gap-1.5" onclick="openSmartRuleModal()">
                        <span>✨</span>
                        <span>Smart new rule</span>
                    </button>
                    <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="openCreateGlobalRuleModal()">
                        <span>+</span>
                        <span>New Rule</span>
                    </button>
                </div>
            </div>

            <div class="card bg-base-100 border border-base-300 shadow-sm overflow-hidden mb-6">
                <div class="overflow-x-auto">
                    <table class="table table-zebra w-full">
                        <thead>
                            <tr class="bg-base-200/50 text-base-content/70">
                                <th>Rule Name</th>
                                <th>Target Party</th>
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

    <!-- Universal Rule Edit Modal -->
    <dialog id="rule_modal" class="modal">
        <div class="modal-box">
            <h3 id="rule_modal_title" class="font-bold text-lg mb-2">Auto-Matching Rule</h3>
            <form id="rule_modal_form" method="POST" action="" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <input type="hidden" id="rule_redirect" name="redirect" value="/budgets/%s/parties/rules" />
                
                <div class="form-control">
                    <label class="label"><span class="label-text font-semibold text-xs">Target Party</span></label>
                    <select id="rule_party_id" name="party_id" class="select select-bordered w-full" required>
                        %s
                    </select>
                </div>

                <div class="form-control">
                    <label class="label"><span class="label-text font-semibold text-xs">Rule Name</span></label>
                    <input type="text" id="rule_name" name="name" required placeholder="e.g. Zabka Grocery Store" class="input input-bordered w-full" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Match Field</span></label>
                        <select id="rule_match_field" name="match_field" class="select select-bordered w-full">
                            <option value="RAW_PAYEE" selected>RAW_PAYEE (Bank Payee Name)</option>
                            <option value="COUNTERPARTY_IBAN">COUNTERPARTY_IBAN (Account Number)</option>
                            <option value="REFERENCE">REFERENCE (Remittance Info)</option>
                            <option value="ANY">ANY (All Fields)</option>
                        </select>
                    </div>

                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Match Operator</span></label>
                        <select id="rule_match_operator" name="match_operator" class="select select-bordered w-full">
                            <option value="CONTAINS" selected>CONTAINS (Substring)</option>
                            <option value="PREFIX">PREFIX (Starts With)</option>
                            <option value="EXACT">EXACT (Full Match)</option>
                            <option value="REGEX">REGEX (RE2 Regular Expression)</option>
                        </select>
                    </div>
                </div>

                <div class="form-control">
                    <label class="label"><span class="label-text font-semibold text-xs">Pattern</span></label>
                    <input type="text" id="rule_match_pattern" name="match_pattern" required placeholder="e.g. ZABKA or ^ŻABKA.*" class="input input-bordered w-full font-mono text-sm" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Default Envelope (Optional)</span></label>
                        <select id="rule_default_bucket_id" name="default_bucket_id" class="select select-bordered w-full">
                            %s
                        </select>
                    </div>

                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Priority</span></label>
                        <input type="number" id="rule_priority" name="priority" value="100" min="1" max="999" class="input input-bordered w-full font-mono" />
                        <span class="text-[10px] text-base-content/50 mt-1">Lower numbers are evaluated first.</span>
                    </div>
                </div>

                <div class="modal-action">
                    <button type="button" class="btn btn-ghost" onclick="rule_modal.close()">Cancel</button>
                    <button type="submit" id="rule_submit_btn" class="btn btn-primary font-semibold shadow-md">Save Rule</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <!-- AI SMART RULE MODAL -->
    <dialog id="smart_rule_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box w-11/12 max-w-4xl p-6">
            <div class="flex justify-between items-center mb-3">
                <h3 class="font-extrabold text-xl tracking-tight flex items-center gap-2">
                    <span>✨</span>
                    <span>Smart New Rules (AI Assistant)</span>
                </h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <p class="text-xs text-base-content/60 mb-4">
                Analyzes unassigned bank transactions not covered by existing rules and proposes intelligent auto-matching rules. You can test matches, modify patterns, and save selected rules.
            </p>

            <div id="smart_loading" class="flex flex-col items-center justify-center py-12 gap-3">
                <span class="loading loading-spinner loading-lg text-primary"></span>
                <span class="text-sm font-semibold text-base-content/70">Scanning unassigned bank feeds & consulting AI...</span>
            </div>

            <div id="smart_content" class="hidden flex flex-col gap-4">
                <div class="flex justify-between items-center bg-base-200/60 p-3 rounded-xl border border-base-300">
                    <span id="smart_summary" class="text-xs font-medium text-base-content/80"></span>
                    <button type="button" class="btn btn-outline btn-xs font-semibold gap-1" onclick="testProposedRules()">
                        <span>🧪</span>
                        <span>Test How Rules Apply</span>
                    </button>
                </div>

                <div id="test_results_banner" class="hidden p-3 rounded-xl text-xs font-medium"></div>

                <form id="smart_save_form" method="POST" action="/budgets/%s/parties/rules/smart-save" class="flex flex-col gap-4">
                    <input type="hidden" name="csrf_token" value="%s" />
                    <div id="smart_rules_container" class="flex flex-col gap-3 max-h-96 overflow-y-auto pr-1"></div>

                    <div class="modal-action mt-4 gap-2">
                        <button type="button" class="btn btn-ghost" onclick="smart_rule_modal.close()">Close</button>
                        <button type="submit" class="btn btn-primary font-bold shadow-md gap-1.5">
                            <span>💾</span>
                            <span>Save Selected Rules</span>
                        </button>
                    </div>
                </form>
            </div>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <script>
        const budgetID = "%s";

        function openCreateGlobalRuleModal() {
            document.getElementById('rule_modal_title').innerText = "Create Auto-Matching Rule";
            document.getElementById('rule_submit_btn').innerText = "Create Rule";
            const partySel = document.getElementById('rule_party_id');
            partySel.disabled = false;
            const pId = partySel.value;
            const form = document.getElementById('rule_modal_form');
            form.action = '/budgets/' + budgetID + '/parties/' + pId + '/rules?redirect=/budgets/' + budgetID + '/parties/rules';
            
            document.getElementById('rule_name').value = '';
            document.getElementById('rule_match_field').value = 'RAW_PAYEE';
            document.getElementById('rule_match_operator').value = 'CONTAINS';
            document.getElementById('rule_match_pattern').value = '';
            document.getElementById('rule_default_bucket_id').value = '';
            document.getElementById('rule_priority').value = '100';
            
            partySel.onchange = function() {
                form.action = '/budgets/' + budgetID + '/parties/' + partySel.value + '/rules?redirect=/budgets/' + budgetID + '/parties/rules';
            };

            rule_modal.showModal();
        }

        function openEditRuleModal(rule) {
            document.getElementById('rule_modal_title').innerText = "Edit Auto-Matching Rule";
            document.getElementById('rule_submit_btn').innerText = "Save Changes";
            
            const partySel = document.getElementById('rule_party_id');
            partySel.value = rule.party_id;
            partySel.disabled = true; // Bound to existing party's account

            const form = document.getElementById('rule_modal_form');
            form.action = '/budgets/' + budgetID + '/parties/' + rule.party_id + '/rules/' + rule.id + '/update?redirect=/budgets/' + budgetID + '/parties/rules';

            document.getElementById('rule_name').value = rule.name;
            document.getElementById('rule_match_field').value = rule.match_field;
            document.getElementById('rule_match_operator').value = rule.match_operator;
            document.getElementById('rule_match_pattern').value = rule.match_pattern;
            document.getElementById('rule_default_bucket_id').value = rule.bucket_id || '';
            document.getElementById('rule_priority').value = rule.priority;

            rule_modal.showModal();
        }

        let proposedRules = [];

        function openSmartRuleModal() {
            smart_rule_modal.showModal();
            document.getElementById('smart_loading').classList.remove('hidden');
            document.getElementById('smart_content').classList.add('hidden');
            document.getElementById('test_results_banner').classList.add('hidden');

            fetch('/budgets/' + budgetID + '/parties/rules/smart-suggest')
                .then(r => r.json())
                .then(data => {
                    document.getElementById('smart_loading').classList.add('hidden');
                    document.getElementById('smart_content').classList.remove('hidden');

                    proposedRules = data.proposed_rules || [];
                    document.getElementById('smart_summary').innerText = 'Found ' + data.unassigned_count + ' unclassified transactions. AI proposed ' + proposedRules.length + ' rules.';
                    renderProposedRules(proposedRules);
                })
                .catch(err => {
                    document.getElementById('smart_loading').innerHTML = '<div class="alert alert-error text-xs">Failed generating smart rules: ' + err + '</div>';
                });
        }

        function renderProposedRules(rules) {
            const container = document.getElementById('smart_rules_container');
            container.innerHTML = '';
            if (rules.length === 0) {
                container.innerHTML = '<div class="text-center py-6 text-base-content/50">All incoming transactions already match existing rules! No new rules needed.</div>';
                return;
            }

            rules.forEach((r, idx) => {
                const card = document.createElement('div');
                card.className = "card bg-base-200/50 border border-base-300 p-4 rounded-xl flex flex-col gap-3";
                card.innerHTML = "<div class=\"flex items-center justify-between gap-2\">" +
                    "<label class=\"flex items-center gap-2 cursor-pointer\">" +
                        "<input type=\"checkbox\" name=\"selected_rules\" value=\"" + idx + "\" checked class=\"checkbox checkbox-primary checkbox-sm smart-rule-cb\" />" +
                        "<span class=\"font-bold text-sm\">" + (r.name || "") + "</span>" +
                    "</label>" +
                    "<span class=\"badge badge-sm badge-secondary font-semibold\">" + Math.round((r.confidence || 0.8) * 100) + "%% Confidence</span>" +
                "</div>" +
                "<div class=\"grid grid-cols-1 sm:grid-cols-3 gap-2\">" +
                    "<input type=\"text\" name=\"rule_" + idx + "_name\" value=\"" + (r.name || "") + "\" class=\"input input-bordered input-sm w-full\" placeholder=\"Rule Name\" />" +
                    "<input type=\"text\" name=\"rule_" + idx + "_target_party\" value=\"" + (r.target_party_name || "") + "\" class=\"input input-bordered input-sm w-full\" placeholder=\"Party Name\" />" +
                    "<input type=\"text\" name=\"rule_" + idx + "_pattern\" value=\"" + (r.match_pattern || "") + "\" class=\"input input-bordered input-sm w-full font-mono\" placeholder=\"Pattern\" />" +
                "</div>" +
                "<div class=\"flex items-center gap-2 text-xs text-base-content/60\">" +
                    "<span>Field: <b>" + (r.match_field || "") + "</b></span> &bull; " +
                    "<span>Operator: <b>" + (r.match_operator || "") + "</b></span> &bull; " +
                    "<span>Bucket: <b>" + (r.suggested_bucket || "None") + "</b></span>" +
                "</div>" +
                "<input type=\"hidden\" name=\"rule_" + idx + "_field\" value=\"" + (r.match_field || "") + "\" />" +
                "<input type=\"hidden\" name=\"rule_" + idx + "_operator\" value=\"" + (r.match_operator || "") + "\" />" +
                "<input type=\"hidden\" name=\"rule_" + idx + "_bucket\" value=\"" + (r.suggested_bucket || "") + "\" />";
                container.appendChild(card);
            });
        }

        function testProposedRules() {
            const banner = document.getElementById('test_results_banner');
            banner.className = "p-3 rounded-xl text-xs font-semibold bg-info/10 text-info border border-info/20";
            banner.innerText = "Simulating matching against unclassified transactions...";
            banner.classList.remove('hidden');

            const payload = {
                rules: proposedRules
            };

            fetch('/budgets/' + budgetID + '/parties/rules/smart-test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            })
            .then(r => r.json())
            .then(res => {
                banner.className = "p-3 rounded-xl text-xs font-semibold bg-success/10 text-success border border-success/20";
                banner.innerText = "✓ Test Simulation: Rules will successfully auto-classify " + res.matched_count + " of " + res.total_unassigned + " raw transactions!";
            })
            .catch(err => {
                banner.className = "p-3 rounded-xl text-xs font-semibold bg-error/10 text-error border border-error/20";
                banner.innerText = "Test failed: " + err;
            });
        }
    </script>
</body>
</html>`,
		csrfToken,
		RenderSidebarNavigation(budgetID, "parties"),
		budgetID,
		rulesRows.String(),
		RenderBottomDock(budgetID, "parties"),
		csrfToken,
		budgetID,
		partyOptions.String(),
		bucketOptions.String(),
		budgetID, csrfToken,
		budgetID,
	)

	return c.HTML(http.StatusOK, htmlContent)
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

	// Fetch rules for this party
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
		r.PartyID = partyID
		r.PartyName = partyName
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

	csrfToken, _ := c.Get("csrfToken").(string)

	var rulesRows strings.Builder
	for _, r := range rules {
		bucketBadge := `<span class="badge badge-ghost badge-sm text-base-content/50">None</span>`
		if r.BucketName != "" {
			bucketBadge = fmt.Sprintf(`<span class="badge badge-soft badge-primary badge-sm font-semibold">%s</span>`, html.EscapeString(r.BucketName))
		}

		rJSON, _ := json.Marshal(r)
		escapedJSON := html.EscapeString(string(rJSON))

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
					<div class="flex items-center justify-end gap-1">
						<button type="button" class="btn btn-ghost btn-xs text-info font-semibold" onclick='openEditRuleModal(%s)'>
							Edit
						</button>
						<form method="POST" action="/budgets/%s/parties/%s/rules/%s/delete" onsubmit="return confirm('Are you sure you want to delete this matching rule?')">
							<input type="hidden" name="csrf_token" value="%s" />
							<button type="submit" class="btn btn-ghost btn-xs text-error font-semibold">Delete</button>
						</form>
					</div>
				</td>
			</tr>`,
			html.EscapeString(r.Name),
			html.EscapeString(r.MatchField),
			html.EscapeString(r.MatchOperator),
			html.EscapeString(r.MatchPattern),
			bucketBadge,
			r.Priority,
			escapedJSON,
			budgetID, partyID, r.ID,
			csrfToken,
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
            <a href="/budgets/%s/parties/rules" class="btn btn-ghost btn-sm font-semibold gap-1.5">
                <span>⚡</span>
                <span>All Rules</span>
            </a>
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
                <div class="flex items-center gap-2">
                    <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="openCreateRuleModal()">
                        <span>+</span>
                        <span>New Rule</span>
                    </button>
                </div>
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

    <!-- Universal Rule Modal (Create & Edit) -->
    <dialog id="rule_modal" class="modal">
        <div class="modal-box">
            <h3 id="rule_modal_title" class="font-bold text-lg mb-2">Auto-Matching Rule</h3>
            <p class="text-xs text-base-content/60 mb-4">When a raw bank transaction matches this pattern, it will automatically link to <strong>%s</strong>.</p>
            <form id="rule_modal_form" method="POST" action="/budgets/%s/parties/%s/rules" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                
                <div class="form-control">
                    <label class="label"><span class="label-text font-semibold text-xs">Rule Name</span></label>
                    <input type="text" id="rule_name" name="name" required placeholder="e.g. Zabka Grocery Store" class="input input-bordered w-full" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Match Field</span></label>
                        <select id="rule_match_field" name="match_field" class="select select-bordered w-full">
                            <option value="RAW_PAYEE" selected>RAW_PAYEE (Bank Payee Name)</option>
                            <option value="COUNTERPARTY_IBAN">COUNTERPARTY_IBAN (Account Number)</option>
                            <option value="REFERENCE">REFERENCE (Remittance Info)</option>
                            <option value="ANY">ANY (All Fields)</option>
                        </select>
                    </div>

                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Match Operator</span></label>
                        <select id="rule_match_operator" name="match_operator" class="select select-bordered w-full">
                            <option value="CONTAINS" selected>CONTAINS (Substring)</option>
                            <option value="PREFIX">PREFIX (Starts With)</option>
                            <option value="EXACT">EXACT (Full Match)</option>
                            <option value="REGEX">REGEX (RE2 Regular Expression)</option>
                        </select>
                    </div>
                </div>

                <div class="form-control">
                    <label class="label"><span class="label-text font-semibold text-xs">Pattern</span></label>
                    <input type="text" id="rule_match_pattern" name="match_pattern" required placeholder="e.g. ZABKA or ^ŻABKA.*" class="input input-bordered w-full font-mono text-sm" />
                </div>

                <div class="grid grid-cols-2 gap-4">
                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Default Envelope (Optional)</span></label>
                        <select id="rule_default_bucket_id" name="default_bucket_id" class="select select-bordered w-full">
                            %s
                        </select>
                    </div>

                    <div class="form-control">
                        <label class="label"><span class="label-text font-semibold text-xs">Priority</span></label>
                        <input type="number" id="rule_priority" name="priority" value="100" min="1" max="999" class="input input-bordered w-full font-mono" />
                        <span class="text-[10px] text-base-content/50 mt-1">Lower numbers are evaluated first.</span>
                    </div>
                </div>

                <div class="modal-action">
                    <button type="button" class="btn btn-ghost" onclick="rule_modal.close()">Cancel</button>
                    <button type="submit" id="rule_submit_btn" class="btn btn-primary font-semibold shadow-md">Create Rule</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>

    <script>
        const budgetID = "%s";
        const partyID = "%s";

        function openCreateRuleModal() {
            document.getElementById('rule_modal_title').innerText = "Create Auto-Matching Rule";
            document.getElementById('rule_submit_btn').innerText = "Create Rule";
            const form = document.getElementById('rule_modal_form');
            form.action = '/budgets/' + budgetID + '/parties/' + partyID + '/rules';
            
            document.getElementById('rule_name').value = '';
            document.getElementById('rule_match_field').value = 'RAW_PAYEE';
            document.getElementById('rule_match_operator').value = 'CONTAINS';
            document.getElementById('rule_match_pattern').value = '';
            document.getElementById('rule_default_bucket_id').value = '';
            document.getElementById('rule_priority').value = '100';

            rule_modal.showModal();
        }

        function openEditRuleModal(rule) {
            document.getElementById('rule_modal_title').innerText = "Edit Auto-Matching Rule";
            document.getElementById('rule_submit_btn').innerText = "Save Changes";
            const form = document.getElementById('rule_modal_form');
            form.action = '/budgets/' + budgetID + '/parties/' + partyID + '/rules/' + rule.id + '/update';

            document.getElementById('rule_name').value = rule.name;
            document.getElementById('rule_match_field').value = rule.match_field;
            document.getElementById('rule_match_operator').value = rule.match_operator;
            document.getElementById('rule_match_pattern').value = rule.match_pattern;
            document.getElementById('rule_default_bucket_id').value = rule.bucket_id || '';
            document.getElementById('rule_priority').value = rule.priority;

            rule_modal.showModal();
        }
    </script>
</body>
</html>`,
		html.EscapeString(partyName),
		budgetID,
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
		budgetID,
		partyID,
	)

	return c.HTML(http.StatusOK, htmlContent)
}

// CreatePartyRule creates an auto-matching rule for a party.
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
	redirectURL := c.QueryParam("redirect")
	if redirectURL == "" {
		redirectURL = c.FormValue("redirect")
	}

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
			// Auto create external account if none exists
			var partyName string
			_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT name FROM parties_current WHERE id = ?`, partyID).Scan(&partyName)
			targetAccountID = domain.NewUUIDv7()
			now := time.Now().UTC().UnixMicro()
			_, _ = tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
				targetAccountID, partyID, partyName+" Account", userID, now,
			)
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed locating party account: "+err.Error())
		}
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

	if redirectURL != "" {
		return c.Redirect(http.StatusSeeOther, redirectURL)
	}
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}

// UpdatePartyRule updates an existing matching rule.
func (h *PartiesHandler) UpdatePartyRule(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	ruleID := c.Param("ruleId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := strings.TrimSpace(c.FormValue("name"))
	matchField := strings.TrimSpace(c.FormValue("match_field"))
	matchOperator := strings.TrimSpace(c.FormValue("match_operator"))
	matchPattern := strings.TrimSpace(c.FormValue("match_pattern"))
	priorityStr := strings.TrimSpace(c.FormValue("priority"))
	defaultBucketID := strings.TrimSpace(c.FormValue("default_bucket_id"))
	redirectURL := c.QueryParam("redirect")
	if redirectURL == "" {
		redirectURL = c.FormValue("redirect")
	}

	if name == "" || matchPattern == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name and Pattern are required")
	}

	if matchOperator == "REGEX" {
		if _, err := regexp.Compile(matchPattern); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid regular expression pattern: "+err.Error())
		}
	}

	priority := 100
	if p, err := strconv.Atoi(priorityStr); err == nil && p > 0 {
		priority = p
	}

	var bucketIDPtr *string
	if defaultBucketID != "" {
		bucketIDPtr = &defaultBucketID
	}

	var curVer int
	var tgtAcctID string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, target_counterparty_account_id
		FROM matching_rules_current WHERE id = ?`, ruleID).Scan(&curVer, &tgtAcctID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Matching rule not found")
	}

	now := time.Now().UTC().UnixMicro()
	nextVer := curVer + 1

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO matching_rules (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		ruleID, nextVer, name, priority, matchField, matchOperator, matchPattern,
		tgtAcctID, bucketIDPtr, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule event")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE matching_rules_current
		SET version = ?, name = ?, priority = ?, match_field = ?, match_operator = ?, match_pattern = ?, default_bucket_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, name, priority, matchField, matchOperator, matchPattern, bucketIDPtr, now, ruleID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating matching rules projection")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	if redirectURL != "" {
		return c.Redirect(http.StatusSeeOther, redirectURL)
	}
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}

// DeletePartyRule deletes a matching rule.
func (h *PartiesHandler) DeletePartyRule(c echo.Context) error {
	budgetID := c.Param("budgetId")
	partyID := c.Param("partyId")
	ruleID := c.Param("ruleId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	redirectURL := c.QueryParam("redirect")

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Transaction error: "+err.Error())
	}
	defer tx.Rollback()

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

	if redirectURL != "" {
		return c.Redirect(http.StatusSeeOther, redirectURL)
	}
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/%s/rules", budgetID, partyID))
}

// SmartRuleSuggestResponse is returned by SmartSuggestRules.
type SmartRuleSuggestResponse struct {
	UnassignedCount int                     `json:"unassigned_count"`
	ProposedRules   []ai.SmartRuleCandidate `json:"proposed_rules"`
}

// SmartSuggestRules inspects unassigned raw transactions and suggests AI rules.
func (h *PartiesHandler) SmartSuggestRules(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// 1. Compile active matching rules engine
	rulesEngine, err := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)
	if err != nil {
		rulesEngine, _ = rule.NewMatchingEngine([]*rule.MatchingRule{})
	}

	// 2. Fetch pending transactions
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, raw_payee, COALESCE(counterparty_iban, ''), COALESCE(reference, ''), currency, amount_minor
		FROM raw_transactions_current
		WHERE status = 'PENDING'
		ORDER BY booking_date DESC LIMIT 200`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading transactions: "+err.Error())
	}
	defer rows.Close()

	var unassignedTxs []ai.UnassignedTxInfo
	for rows.Next() {
		var id, payee, iban, ref, curr string
		var amountMinor int64
		if err := rows.Scan(&id, &payee, &iban, &ref, &curr, &amountMinor); err == nil {
			// Check if already matched
			if match := rulesEngine.Match(payee, iban, ref); match == nil {
				unassignedTxs = append(unassignedTxs, ai.UnassignedTxInfo{
					ID:        id,
					RawPayee:  payee,
					IBAN:      iban,
					Reference: ref,
					Currency:  curr,
					Amount:    fmt.Sprintf("%.2f", float64(amountMinor)/100.0),
				})
			}
		}
	}

	// 3. Available buckets
	var buckets []string
	bRows, err := tenantDB.QueryContext(c.Request().Context(), `SELECT name FROM buckets_projection WHERE is_archived = 0 ORDER BY name ASC`)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bName string
			bRows.Scan(&bName)
			buckets = append(buckets, bName)
		}
	}

	// 4. Existing parties
	var parties []string
	pRows, err := tenantDB.QueryContext(c.Request().Context(), `SELECT name FROM parties_current ORDER BY name ASC`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var pName string
			pRows.Scan(&pName)
			parties = append(parties, pName)
		}
	}

	if len(unassignedTxs) == 0 {
		return c.JSON(http.StatusOK, SmartRuleSuggestResponse{
			UnassignedCount: 0,
			ProposedRules:   []ai.SmartRuleCandidate{},
		})
	}

	proposed, err := h.smartRuleAIProv.SuggestRules(c.Request().Context(), ai.SmartRulePromptRequest{
		Transactions:     unassignedTxs,
		AvailableBuckets: buckets,
		ExistingParties:  parties,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed calling AI rule generator: "+err.Error())
	}

	return c.JSON(http.StatusOK, SmartRuleSuggestResponse{
		UnassignedCount: len(unassignedTxs),
		ProposedRules:   proposed,
	})
}

// SmartTestRules tests proposed rules against current unassigned transactions without saving.
func (h *PartiesHandler) SmartTestRules(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var req struct {
		Rules []ai.SmartRuleCandidate `json:"rules"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request payload")
	}

	var testRules []*rule.MatchingRule
	for idx, r := range req.Rules {
		testRules = append(testRules, &rule.MatchingRule{
			ID:                       fmt.Sprintf("test-%d", idx),
			Name:                     r.Name,
			Priority:                 100 + idx,
			MatchField:               r.MatchField,
			MatchOperator:            r.MatchOperator,
			MatchPattern:             r.MatchPattern,
			TargetCounterpartyAcctID: "dummy-target-id",
		})
	}

	testEngine, err := rule.NewMatchingEngine(testRules)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid rule pattern in proposed rules: "+err.Error())
	}

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT raw_payee, COALESCE(counterparty_iban, ''), COALESCE(reference, '')
		FROM raw_transactions_current
		WHERE status = 'PENDING'`)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed querying raw transactions")
	}
	defer rows.Close()

	total := 0
	matched := 0
	for rows.Next() {
		total++
		var payee, iban, ref string
		rows.Scan(&payee, &iban, &ref)
		if testEngine.Match(payee, iban, ref) != nil {
			matched++
		}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"total_unassigned": total,
		"matched_count":    matched,
	})
}

// SmartSaveRules saves the selected AI-proposed rules.
func (h *PartiesHandler) SmartSaveRules(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	if err := c.Request().ParseForm(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid form data")
	}

	formParams, _ := c.FormParams()
	selectedIndices := formParams["selected_rules"]
	if len(selectedIndices) == 0 {
		selectedIndices = c.Request().Form["selected_rules"]
	}
	if len(selectedIndices) == 0 {
		selectedIndices = c.Request().PostForm["selected_rules"]
	}
	if len(selectedIndices) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/rules", budgetID))
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	getVal := func(key string) string {
		v := c.FormValue(key)
		if v != "" { return v }
		if vals, ok := formParams[key]; ok && len(vals) > 0 { return vals[0] }
		if c.Request().Form != nil && len(c.Request().Form[key]) > 0 { return c.Request().Form[key][0] }
		if c.Request().PostForm != nil && len(c.Request().PostForm[key]) > 0 { return c.Request().PostForm[key][0] }
		return ""
	}

	for _, idx := range selectedIndices {
		name := strings.TrimSpace(getVal("rule_" + idx + "_name"))
		targetPartyName := strings.TrimSpace(getVal("rule_" + idx + "_target_party"))
		pattern := strings.TrimSpace(getVal("rule_" + idx + "_pattern"))
		field := strings.TrimSpace(getVal("rule_" + idx + "_field"))
		operator := strings.TrimSpace(getVal("rule_" + idx + "_operator"))
		bucketName := strings.TrimSpace(getVal("rule_" + idx + "_bucket"))

		if name == "" || pattern == "" || targetPartyName == "" {
			continue
		}
		if field == "" {
			field = "RAW_PAYEE"
		}
		if operator == "" {
			operator = "CONTAINS"
		}

		// Ensure party exists
		var partyID string
		err := tx.QueryRowContext(c.Request().Context(), `SELECT id FROM parties_current WHERE LOWER(name) = LOWER(?) LIMIT 1`, targetPartyName).Scan(&partyID)
		if err == sql.ErrNoRows {
			partyID = domain.NewUUIDv7()
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO parties (id, version, name, party_type, is_user_owned, action, agent_id, correlation_id, created_at)
				VALUES (?, 1, ?, 'ORGANIZATION', 0, 'CREATE', ?, ?, ?)`,
				partyID, targetPartyName, userID, corrID, now,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating party: "+err.Error())
			}
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`,
				partyID, targetPartyName, userID, now,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed updating party projection: "+err.Error())
			}
		}

		// Ensure external account exists
		var accountID string
		err = tx.QueryRowContext(c.Request().Context(), `SELECT id FROM accounts_current WHERE party_id = ? LIMIT 1`, partyID).Scan(&accountID)
		if err == sql.ErrNoRows {
			accountID = domain.NewUUIDv7()
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
				accountID, partyID, targetPartyName+" Account", userID, now,
			)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating account: "+err.Error())
			}
		}

		// Resolve bucket ID
		var bucketIDPtr *string
		if bucketName != "" {
			var bID string
			if err := tx.QueryRowContext(c.Request().Context(), `SELECT id FROM buckets_projection WHERE LOWER(name) = LOWER(?) LIMIT 1`, bucketName).Scan(&bID); err == nil {
				bucketIDPtr = &bID
			}
		}

		ruleID := domain.NewUUIDv7()
		_, err = tx.ExecContext(c.Request().Context(), `
			INSERT INTO matching_rules (
				id, version, name, priority, match_field, match_operator, match_pattern,
				target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
			) VALUES (?, 1, ?, 100, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
			ruleID, name, field, operator, pattern, accountID, bucketIDPtr, userID, corrID, now,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule: "+err.Error())
		}

		_, err = tx.ExecContext(c.Request().Context(), `
			INSERT INTO matching_rules_current (
				id, version, name, priority, match_field, match_operator, match_pattern,
				target_counterparty_account_id, default_bucket_id, updated_at
			) VALUES (?, 1, ?, 100, ?, ?, ?, ?, ?, ?)`,
			ruleID, name, field, operator, pattern, accountID, bucketIDPtr, now,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed inserting matching rule current: "+err.Error())
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error: "+err.Error())
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/parties/rules", budgetID))
}
