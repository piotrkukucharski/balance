package handlers

import (
	"database/sql"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
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

	sidebarHTML := RenderSidebarNavigation(budgetID, "tokens")
	bottomDockHTML := RenderBottomDock(budgetID, "settings")

	nowMicro := time.Now().UTC().UnixMicro()

	var rowsHTML strings.Builder
	if len(tokens) == 0 {
		rowsHTML.WriteString(`<tr><td colspan="5" class="text-center py-6 text-base-content/60">No API tokens generated yet.</td></tr>`)
	} else {
		for _, t := range tokens {
			expiresStr := "Never"
			isExpired := false
			if t.ExpiresAt > 0 {
				expiresStr = RenderTimeTagMicro(t.ExpiresAt, true, c)
				if t.ExpiresAt < nowMicro {
					isExpired = true
				}
			}

			lastUsedStr := "Never"
			if t.LastUsedAt != nil && *t.LastUsedAt > 0 {
				lastUsedStr = RenderTimeTagMicro(*t.LastUsedAt, true, c)
			}

			statusBadge := `<span class="badge badge-success badge-sm gap-1">Active</span>`
			isRevoked := t.RevokedAt != nil && *t.RevokedAt > 0
			if isRevoked {
				statusBadge = `<span class="badge badge-error badge-sm gap-1">Revoked</span>`
			} else if isExpired {
				statusBadge = `<span class="badge badge-warning badge-sm gap-1">Expired</span>`
			}

			revokeBtn := ""
			if !isRevoked && !isExpired {
				revokeBtn = fmt.Sprintf(`
					<form method="POST" action="/budgets/%s/settings/tokens/%s/revoke" onsubmit="return confirm('Are you sure you want to revoke this token? Any script or service using it will stop working immediately.');">
						<input type="hidden" name="csrf_token" value="%s" />
						<button type="submit" class="btn btn-ghost btn-xs text-error font-semibold hover:bg-error/10">Revoke</button>
					</form>
				`, budgetID, t.ID, csrfToken)
			}

			shortHash := t.TokenHash
			if len(shortHash) > 8 {
				shortHash = shortHash[:8] + "..."
			}

			rowsHTML.WriteString(fmt.Sprintf(`
				<tr class="hover">
					<td>
						<div class="font-bold">%s</div>
						<div class="text-xs text-base-content/50 font-mono">hash: %s</div>
					</td>
					<td>%s</td>
					<td class="text-xs text-base-content/70">%s</td>
					<td class="text-xs text-base-content/70">%s</td>
					<td class="text-right">%s</td>
				</tr>
			`, html.EscapeString(t.Name), shortHash, statusBadge, lastUsedStr, expiresStr, revokeBtn))
		}
	}

	// Check if a newly generated token was passed in query or session flash
	newTokenPlain := c.QueryParam("new_token")
	newTokenName := c.QueryParam("new_token_name")

	modalHTML := ""
	if newTokenPlain != "" {
		modalHTML = fmt.Sprintf(`
		<dialog id="token_created_modal" class="modal modal-open">
			<div class="modal-box border border-success/30 shadow-2xl">
				<h3 class="font-bold text-lg text-success flex items-center gap-2">
					<svg xmlns="http://www.w3.org/2000/svg" class="stroke-current shrink-0 h-6 w-6" fill="none" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" /></svg>
					Personal Access Token Generated!
				</h3>
				<p class="py-2 text-sm text-base-content/80">
					Here is your new token for <strong>%s</strong>. Make sure to copy it now — for security reasons, <strong>it will not be shown again</strong>.
				</p>
				<div class="form-control my-4">
					<div class="join w-full">
						<input id="new-token-input" type="text" readonly value="%s" class="input input-bordered input-primary join-item w-full font-mono text-xs select-all bg-base-200" />
						<button class="btn btn-primary join-item" onclick="navigator.clipboard.writeText(document.getElementById('new-token-input').value); this.innerText = 'Copied!'; setTimeout(() => this.innerText = 'Copy', 2000);">Copy</button>
					</div>
				</div>
				<div class="alert alert-warning text-xs">
					<svg xmlns="http://www.w3.org/2000/svg" class="stroke-current shrink-0 h-4 w-4" fill="none" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" /></svg>
					<span>Anyone with this token can access your account through the REST API without entering your password or CSRF token.</span>
				</div>
				<div class="modal-action">
					<a href="/budgets/%s/settings/tokens" class="btn btn-sm">I have safely stored this token</a>
				</div>
			</div>
		</dialog>
		`, html.EscapeString(newTokenName), html.EscapeString(newTokenPlain), budgetID)
	}

	dFormat, tFormat := GetUserDateTimePreferences(c)

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark" data-date-format="%s" data-time-format="%s">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <meta name="user-date-format" content="%s">
    <meta name="user-time-format" content="%s">
    <title>Balance — Personal Access Tokens</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 min-h-screen text-base-content flex flex-col justify-between">
    <div class="navbar bg-base-100 border-b border-base-300 px-4 md:px-8">
        <div class="flex-1">
            <a href="/budgets/%s/budget" class="btn btn-ghost text-xl font-bold tracking-tight text-primary">💰 Balance</a>
            <span class="badge badge-outline ml-2 text-xs font-mono font-semibold uppercase">%s</span>
        </div>
        <div class="flex-none gap-2">
            <a href="/budgets" class="btn btn-ghost btn-sm">Switch Budget</a>
            <form method="POST" action="/logout" class="inline">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-outline btn-error btn-sm">Logout</button>
            </form>
        </div>
    </div>

    <div class="flex-1 flex max-w-7xl w-full mx-auto p-4 md:p-6 gap-6">
        <aside class="w-64 hidden md:block shrink-0">
            %s
        </aside>

        <main class="flex-1 flex flex-col gap-6">
            <div>
                <h1 class="text-2xl font-black tracking-tight">Personal Access Tokens (PAT)</h1>
                <p class="text-sm text-base-content/70 mt-1">Manage API tokens (<code>bal_pat_...</code>) for programmatic REST API access (OpenAPI 3.1).</p>
            </div>

            <!-- Create Token Card -->
            <div class="card bg-base-100 shadow border border-base-300">
                <div class="card-body">
                    <h2 class="card-title text-base font-bold">Generate New Token</h2>
                    <form method="POST" action="/budgets/%s/settings/tokens" class="flex flex-col md:flex-row gap-4 items-end mt-2">
                        <input type="hidden" name="csrf_token" value="%s" />
                        
                        <div class="form-control w-full md:flex-1">
                            <label class="label"><span class="label-text font-semibold">Token Description / Name</span></label>
                            <input type="text" name="name" required placeholder="e.g. CLI Sync, CI/CD Script, Mobile Client" class="input input-bordered w-full input-sm" />
                        </div>

                        <div class="form-control w-full md:w-48">
                            <label class="label"><span class="label-text font-semibold">Expiration</span></label>
                            <select name="expiration_days" class="select select-bordered select-sm w-full">
                                <option value="30" selected>30 days</option>
                                <option value="90">90 days</option>
                                <option value="365">1 year</option>
                                <option value="0">Never (No expiration)</option>
                            </select>
                        </div>

                        <button type="submit" class="btn btn-primary btn-sm w-full md:w-auto font-semibold">Generate Token</button>
                    </form>
                </div>
            </div>

            <!-- Existing Tokens Card -->
            <div class="card bg-base-100 shadow border border-base-300">
                <div class="card-body p-0">
                    <div class="p-4 border-b border-base-200">
                        <h2 class="card-title text-base font-bold">Active and Past Tokens</h2>
                    </div>
                    <div class="overflow-x-auto">
                        <table class="table w-full">
                            <thead>
                                <tr class="text-xs uppercase text-base-content/60 bg-base-200/50">
                                    <th>Token Name</th>
                                    <th>Status</th>
                                    <th>Last Used</th>
                                    <th>Expires</th>
                                    <th class="text-right">Actions</th>
                                </tr>
                            </thead>
                            <tbody>
                                %s
                            </tbody>
                        </table>
                    </div>
                </div>
            </div>

            <!-- API Usage Instructions -->
            <div class="card bg-base-100 shadow border border-base-300">
                <div class="card-body">
                    <h3 class="font-bold text-sm">How to use your Personal Access Token</h3>
                    <p class="text-xs text-base-content/70 mt-1">Authenticate requests by passing the token in the <code>Authorization</code> header. PAT requests are exempt from CSRF checks:</p>
                    <div class="mockup-code text-xs mt-3 bg-neutral text-neutral-content">
                        <pre data-prefix="$"><code>curl -H "Authorization: Bearer bal_pat_your_token_here" \</code></pre>
                        <pre data-prefix=" "><code>     -H "Accept: application/json" \</code></pre>
                        <pre data-prefix=" "><code>     http://localhost:8080/api/v1/budgets/%s</code></pre>
                    </div>
                </div>
            </div>
        </main>
    </div>

    %s
    %s
    %s
</body>
</html>`,
		dFormat, tFormat, dFormat, tFormat,
		budgetID, role, csrfToken,
		sidebarHTML,
		budgetID, csrfToken,
		rowsHTML.String(),
		budgetID,
		bottomDockHTML,
		modalHTML,
		DateTimeFormatterScript(),
	)

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
