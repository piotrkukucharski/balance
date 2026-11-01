package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

type MembersHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

func NewMembersHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *MembersHandler {
	return &MembersHandler{mainDB: mainDB, tenantMgr: tenantMgr}
}

type memberViewRow struct {
	Initial   string
	Name      string
	Email     string
	Role      string
	RoleBadge string
	JoinedTag template.HTML
}

type membersViewData struct {
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	Members                 []memberViewRow
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

	var members []memberViewRow
	for rows.Next() {
		var id, email, name, mRole string
		var joinedAt int64
		rows.Scan(&id, &email, &name, &mRole, &joinedAt)

		joinedTag := RenderTimeTagMicro(joinedAt, false, c)
		roleBadge := "badge-soft badge-primary"
		if mRole == "OWNER" {
			roleBadge = "badge-primary"
		} else if mRole == "VIEWER" {
			roleBadge = "badge-neutral"
		}

		initial := "?"
		if len(name) > 0 {
			initial = strings.ToUpper(name[:1])
		}

		members = append(members, memberViewRow{
			Initial:   initial,
			Name:      name,
			Email:     email,
			Role:      mRole,
			RoleBadge: roleBadge,
			JoinedTag: template.HTML(joinedTag),
		})
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	data := membersViewData{
		BudgetID:                budgetID,
		BudgetName:              "Members",
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "members")),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "settings")),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
		Members:                 members,
	}

	_ = role
	out, err := templates.RenderString("members/view", data)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering members view: "+err.Error())
	}
	return c.HTML(http.StatusOK, out)
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

	if strings.Contains(c.Request().URL.Path, "/settings/members") {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings/members", budgetID))
	}
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/members", budgetID))
}
