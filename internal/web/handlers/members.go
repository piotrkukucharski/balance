package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/labstack/echo/v4"
)

type MembersHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

func NewMembersHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *MembersHandler {
	return &MembersHandler{mainDB: mainDB, tenantMgr: tenantMgr}
}

// MembersView lists members and token invitations for the budget.
func (h *MembersHandler) MembersView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)

	rows, err := h.mainDB.QueryContext(c.Request().Context(), `
		SELECT u.id, u.email, u.display_name, bm.role, bm.joined_at
		FROM budget_members bm
		JOIN users u ON bm.user_id = u.id
		WHERE bm.budget_id = ?
		ORDER BY bm.joined_at ASC`, budgetID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading members")
	}
	defer rows.Close()

	var memberRows strings.Builder
	for rows.Next() {
		var id, email, name, mRole string
		var joinedAt int64
		rows.Scan(&id, &email, &name, &mRole, &joinedAt)

		joinedStr := time.UnixMicro(joinedAt).Format("2006-01-02")
		roleBadge := "badge-soft badge-primary"
		if mRole == "OWNER" {
			roleBadge = "badge-primary"
		} else if mRole == "VIEWER" {
			roleBadge = "badge-neutral"
		}

		memberRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="flex items-center gap-3">
						<div class="avatar placeholder">
							<div class="bg-base-300 text-base-content rounded-full w-9">
								<span class="text-xs font-bold">%s</span>
							</div>
						</div>
						<div>
							<div class="font-bold text-sm">%s</div>
							<div class="text-xs text-base-content/50">%s</div>
						</div>
					</div>
				</td>
				<td><span class="badge %s font-bold text-xs">%s</span></td>
				<td class="text-xs font-mono text-base-content/60">%s</td>
			</tr>`, strings.ToUpper(name[:1]), name, email, roleBadge, mRole, joinedStr))
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Members & Access</title>
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
                <span>Members</span>
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
                <li><a href="/budgets/%s/accounts">💳 2. Accounts</a></li>
                <li><a href="/budgets/%s/transactions">💸 3. Transactions</a></li>
                <li><a href="/budgets/%s/reports">📊 4. Reports</a></li>
                <li><a href="/budgets/%s/parties">🏢 5. External Parties</a></li>
                <li><a href="/budgets/%s/members" class="active font-bold">👥 6. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 md:p-8 overflow-y-auto">
            <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4 mb-6">
                <div>
                    <h1 class="text-2xl font-black tracking-tight">Members & Access Control</h1>
                    <p class="text-xs text-base-content/60 mt-1">Multi-user RBAC hierarchy: OWNER, ADMIN, EDITOR, and VIEWER roles.</p>
                </div>
                <button class="btn btn-primary btn-sm shadow font-semibold gap-1.5" onclick="invite_modal.showModal()">
                    <span>+</span>
                    <span>Invite Member</span>
                </button>
            </div>

            <div class="card card-border bg-base-100 border-base-300 shadow-md">
                <div class="card-body p-0">
                    <div class="overflow-x-auto">
                        <table class="table table-zebra w-full">
                            <thead>
                                <tr class="bg-base-200/50">
                                    <th class="font-bold">Collaborator</th>
                                    <th class="font-bold">Assigned Role</th>
                                    <th class="font-bold">Member Since</th>
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
        <a href="/budgets/%s/accounts">
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
        <a href="/budgets/%s/parties">
            <span class="text-xl">🏢</span>
            <span class="dock-label">Parties</span>
        </a>
        <a href="/budgets/%s/members" class="dock-active">
            <span class="text-xl">👥</span>
            <span class="dock-label">Members</span>
        </a>
    </div>

    <!-- Invite Member Modal -->
    <dialog id="invite_modal" class="modal modal-bottom sm:modal-middle">
        <div class="modal-box card-border border-base-300 p-6">
            <div class="flex justify-between items-center mb-4">
                <h3 class="font-extrabold text-xl tracking-tight">Invite Collaborator</h3>
                <form method="dialog"><button class="btn btn-ghost btn-sm btn-circle">✕</button></form>
            </div>
            <form method="POST" action="/budgets/%s/members/invite" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Invited Email Address</span></label>
                    <input type="email" name="email" required placeholder="collaborator@example.com" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1"><span class="label-text font-medium text-xs">Permission Role</span></label>
                    <select name="role" class="select select-bordered w-full select-md focus:select-primary">
                        <option value="EDITOR" selected>EDITOR — Can record transactions, modify buckets and targets</option>
                        <option value="VIEWER">VIEWER — Strictly read-only access to dashboard and reports</option>
                        <option value="ADMIN">ADMIN — Can invite members and restore historical audit versions</option>
                    </select>
                </div>
                <div class="modal-action mt-6 gap-2">
                    <button type="button" class="btn btn-ghost" onclick="invite_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary font-semibold shadow-md">Send Invitation</button>
                </div>
            </form>
        </div>
        <form method="dialog" class="modal-backdrop"><button>close</button></form>
    </dialog>
</body>
</html>`, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID, budgetID,
		memberRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID, budgetID,
		budgetID, csrfToken)

	_ = role
	return c.HTML(http.StatusOK, html)
}

// InviteMember handles sending token-based invitation in main.db.
func (h *MembersHandler) InviteMember(c echo.Context) error {
	budgetID := c.Param("budgetId")
	inviterID := c.Get(ContextUserIDKey).(string)

	email := c.FormValue("email")
	role := c.FormValue("role")
	if email == "" || (role != "ADMIN" && role != "EDITOR" && role != "VIEWER") {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid invitation parameters")
	}

	rawToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Token error")
	}
	tokenHash := auth.HashToken(rawToken)

	inviteID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	expiresAt := now + int64(7*24*3600*1_000_000) // 7 days

	_, err = h.mainDB.ExecContext(c.Request().Context(), `
		INSERT INTO budget_invitations (id, budget_id, invited_email, role, token_hash, invited_by_user_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inviteID, budgetID, email, role, tokenHash, inviterID, expiresAt, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create invitation")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/members", budgetID))
}
