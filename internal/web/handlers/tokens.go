package handlers

import (
	"database/sql"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/config"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/i18n"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

// APITokenItem represents an API token record in main.db
type APITokenItem struct {
	ID         string
	UserID     string
	Name       string
	TokenHash  string
	ExpiresAt  int64
	LastUsedAt *int64
	RevokedAt  *int64
	CreatedAt  int64
}

// TokensHandler handles managing Personal Access Tokens (PAT).
type TokensHandler struct {
	mainDB *sql.DB
}

// NewTokensHandler creates a new TokensHandler.
func NewTokensHandler(mainDB *sql.DB) *TokensHandler {
	return &TokensHandler{mainDB: mainDB}
}

type tokenViewRow struct {
	ID           string
	Name         string
	ShortHash    string
	IsRevoked    bool
	IsExpired    bool
	CanRevoke    bool
	LastUsedHTML template.HTML
	ExpiresHTML  template.HTML
}

type tokensViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	Role                    string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	PublicBaseURL           string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	Tokens                  []tokenViewRow
	NewTokenPlain           string
	NewTokenName            string
}

// TokensView renders the Personal Access Tokens management page.
func (h *TokensHandler) TokensView(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = logger.WithComponent(ctx, "web:tokens")

	budgetID := c.Param("budgetId")
	role, _ := c.Get(ContextRoleKey).(string)
	userID, _ := c.Get(ContextUserIDKey).(string)
	csrfToken, _ := c.Get("csrfToken").(string)

	rows, err := h.mainDB.QueryContext(ctx, `
		SELECT id, user_id, name, token_hash, expires_at, last_used_at, revoked_at, created_at
		FROM api_tokens
		WHERE user_id = ?
		ORDER BY created_at DESC`, userID)
	if err != nil {
		logger.Error(ctx, "Failed querying api_tokens", "user_id", userID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading tokens")
	}
	defer rows.Close()

	var tokens []APITokenItem
	for rows.Next() {
		var it APITokenItem
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(&it.ID, &it.UserID, &it.Name, &it.TokenHash, &it.ExpiresAt, &lastUsed, &revoked, &it.CreatedAt); err != nil {
			logger.Error(ctx, "Failed scanning token row", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading tokens")
		}
		if lastUsed.Valid {
			it.LastUsedAt = &lastUsed.Int64
		}
		if revoked.Valid {
			it.RevokedAt = &revoked.Int64
		}
		tokens = append(tokens, it)
	}

	nowMicro := time.Now().UTC().UnixMicro()
	dFormat, tFormat := GetUserDateTimePreferences(c)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	neverLabel := i18n.T(userLocale, "tokens-never")

	var tokenRows []tokenViewRow
	for _, t := range tokens {
		expiresStr := neverLabel
		isExpired := false
		if t.ExpiresAt > 0 {
			expiresStr = RenderTimeTagMicro(t.ExpiresAt, true, c)
			if t.ExpiresAt < nowMicro {
				isExpired = true
			}
		}

		lastUsedStr := neverLabel
		if t.LastUsedAt != nil && *t.LastUsedAt > 0 {
			lastUsedStr = RenderTimeTagMicro(*t.LastUsedAt, true, c)
		}

		isRevoked := t.RevokedAt != nil && *t.RevokedAt > 0
		shortHash := t.TokenHash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8] + "..."
		}

		tokenRows = append(tokenRows, tokenViewRow{
			ID:           t.ID,
			Name:         t.Name,
			ShortHash:    shortHash,
			IsRevoked:    isRevoked,
			IsExpired:    isExpired,
			CanRevoke:    !isRevoked && !isExpired,
			LastUsedHTML: template.HTML(lastUsedStr),
			ExpiresHTML:  template.HTML(expiresStr),
		})
	}

	data := tokensViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		Role:                    role,
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		PublicBaseURL:           config.GetPublicBaseURL(""),
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "tokens", userLocale)),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "settings", userLocale)),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
		Tokens:                  tokenRows,
		NewTokenPlain:           c.QueryParam("new_token"),
		NewTokenName:            c.QueryParam("new_token_name"),
	}

	htmlContent, err := templates.RenderString("tokens/view", data)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering tokens view: "+err.Error())
	}

	return c.HTML(http.StatusOK, htmlContent)
}

// CreateToken handles generating a new Personal Access Token for the user.
func (h *TokensHandler) CreateToken(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = logger.WithComponent(ctx, "web:tokens")

	budgetID := c.Param("budgetId")
	userID, _ := c.Get(ContextUserIDKey).(string)

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Token name is required")
	}

	expDaysStr := c.FormValue("expiration_days")
	expDays, _ := strconv.Atoi(expDaysStr)
	var expiresAt int64
	if expDays > 0 {
		expiresAt = time.Now().UTC().AddDate(0, 0, expDays).UnixMicro()
	}

	fullPAT, tokenHash, err := auth.GeneratePAT()
	if err != nil {
		logger.Error(ctx, "Failed generating PAT", "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Token generation error")
	}

	tokenID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, err = h.mainDB.ExecContext(ctx, `
		INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		tokenID, userID, name, tokenHash, expiresAt, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed persisting api_token", "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed saving token")
	}

	logger.Info(ctx, "Personal Access Token generated", "token_id", tokenID, "user_id", userID, "name", name)

	redirectURL := fmt.Sprintf("/budgets/%s/settings/tokens?new_token=%s&new_token_name=%s",
		budgetID, fullPAT, html.EscapeString(name))
	return c.Redirect(http.StatusSeeOther, redirectURL)
}

// RevokeToken handles revoking an existing Personal Access Token.
func (h *TokensHandler) RevokeToken(c echo.Context) error {
	ctx := c.Request().Context()
	ctx = logger.WithComponent(ctx, "web:tokens")

	budgetID := c.Param("budgetId")
	tokenID := c.Param("tokenId")
	userID, _ := c.Get(ContextUserIDKey).(string)

	now := time.Now().UTC().UnixMicro()

	res, err := h.mainDB.ExecContext(ctx, `
		UPDATE api_tokens
		SET revoked_at = ?
		WHERE id = ? AND user_id = ? AND (revoked_at IS NULL OR revoked_at = 0)`,
		now, tokenID, userID,
	)
	if err != nil {
		logger.Error(ctx, "Failed revoking token", "token_id", tokenID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed revoking token")
	}

	rowsAff, _ := res.RowsAffected()
	if rowsAff == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "Token not found or already revoked")
	}

	logger.Info(ctx, "Personal Access Token revoked", "token_id", tokenID, "user_id", userID)

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/settings/tokens", budgetID))
}
