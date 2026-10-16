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
		memberRows.WriteString(fmt.Sprintf(`
			<tr class="hover">
				<td>
					<div class="font-bold">%s</div>
					<div class="text-xs opacity-60">%s</div>
				</td>
				<td><span class="badge badge-outline font-semibold">%s</span></td>
				<td class="text-xs opacity-70">%s</td>
			</tr>`, name, email, mRole, joinedStr))
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - Members</title>
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
                <li><a href="/budgets/%s/parties">4. External Parties</a></li>
                <li><a href="/budgets/%s/members" class="active font-bold">5. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 overflow-y-auto">
            <div class="flex justify-between items-center mb-6">
                <div>
                    <h1 class="text-2xl font-bold">Budget Members & RBAC</h1>
                    <p class="text-xs opacity-60">Manage permissions across OWNER, ADMIN, EDITOR, and VIEWER roles.</p>
                </div>
                <button class="btn btn-primary btn-sm" onclick="invite_modal.showModal()">+ Invite Member</button>
            </div>

            <div class="overflow-x-auto bg-base-100 rounded-box border border-base-300 shadow">
                <table class="table table-zebra">
                    <thead>
                        <tr>
                            <th>User</th>
                            <th>Role</th>
                            <th>Joined</th>
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
        <a href="/budgets/%s/parties">Parties</a>
        <a href="/budgets/%s/members" class="active">Members</a>
    </div>

    <!-- Invite Member Modal -->
    <dialog id="invite_modal" class="modal">
        <div class="modal-box">
            <h3 class="font-bold text-lg mb-4">Invite New Member</h3>
            <form method="POST" action="/budgets/%s/members/invite" class="flex flex-col gap-4">
                <input type="hidden" name="csrf_token" value="%s" />
                <div>
                    <label class="label"><span class="label-text">Invited Email</span></label>
                    <input type="email" name="email" required placeholder="collaborator@example.com" class="input input-bordered w-full" />
                </div>
                <div>
                    <label class="label"><span class="label-text">Role</span></label>
                    <select name="role" class="select select-bordered w-full">
                        <option value="EDITOR" selected>EDITOR (Can modify transactions, buckets, targets)</option>
                        <option value="VIEWER">VIEWER (Read-only access)</option>
                        <option value="ADMIN">ADMIN (Can invite members and restore versions)</option>
                    </select>
                </div>
                <div class="modal-action">
                    <button type="button" class="btn" onclick="invite_modal.close()">Cancel</button>
                    <button type="submit" class="btn btn-primary">Send Invitation</button>
                </div>
            </form>
        </div>
    </dialog>
</body>
</html>`, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID,
		memberRows.String(),
		budgetID, budgetID, budgetID, budgetID, budgetID,
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
