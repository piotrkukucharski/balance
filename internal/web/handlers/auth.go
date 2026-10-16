package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/labstack/echo/v4"
)

type AuthHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

func NewAuthHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *AuthHandler {
	return &AuthHandler{
		mainDB:    mainDB,
		tenantMgr: tenantMgr,
	}
}

// RegisterPage renders the registration HTML page.
func (h *AuthHandler) RegisterPage(c echo.Context) error {
	html := `<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - Register</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5.0.0/dist/full.min.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
</head>
<body class="bg-base-200 min-h-screen flex items-center justify-center p-4">
    <div class="card w-full max-w-md bg-base-100 shadow-xl border border-base-300">
        <div class="card-body">
            <h2 class="card-title text-2xl font-bold justify-center mb-4">Create Account</h2>
            <form method="POST" action="/register" class="flex flex-col gap-4">
                <div>
                    <label class="label"><span class="label-text">Display Name</span></label>
                    <input type="text" name="display_name" required placeholder="Jan Kowalski" class="input input-bordered w-full" />
                </div>
                <div>
                    <label class="label"><span class="label-text">Email</span></label>
                    <input type="email" name="email" required placeholder="jan@example.com" class="input input-bordered w-full" />
                </div>
                <div>
                    <label class="label"><span class="label-text">Password</span></label>
                    <input type="password" name="password" required placeholder="••••••••" class="input input-bordered w-full" />
                </div>
                <button type="submit" class="btn btn-primary mt-2">Sign Up</button>
            </form>
            <div class="text-center mt-4">
                <span class="text-sm opacity-70">Already have an account?</span>
                <a href="/login" class="link link-hover link-primary text-sm font-semibold">Log In</a>
            </div>
        </div>
    </div>
</body>
</html>`
	return c.HTML(http.StatusOK, html)
}

// HandleRegister processes user signup and initializes stateful session.
func (h *AuthHandler) HandleRegister(c echo.Context) error {
	email := c.FormValue("email")
	password := c.FormValue("password")
	displayName := c.FormValue("display_name")

	if email == "" || len(password) < 8 || displayName == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid registration input (password min 8 characters)")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to hash password")
	}

	now := time.Now().UTC().UnixMicro()
	userID := domain.NewUUIDv7()

	_, err = h.mainDB.ExecContext(c.Request().Context(), `
		INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, email, hash, displayName, now, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusConflict, "Email already in use")
	}

	return h.createSessionAndRedirect(c, userID)
}

// LoginPage renders the login HTML page.
func (h *AuthHandler) LoginPage(c echo.Context) error {
	html := `<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - Login</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5.0.0/dist/full.min.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
</head>
<body class="bg-base-200 min-h-screen flex items-center justify-center p-4">
    <div class="card w-full max-w-md bg-base-100 shadow-xl border border-base-300">
        <div class="card-body">
            <h2 class="card-title text-2xl font-bold justify-center mb-4">Welcome to Balance</h2>
            <form method="POST" action="/login" class="flex flex-col gap-4">
                <div>
                    <label class="label"><span class="label-text">Email</span></label>
                    <input type="email" name="email" required placeholder="jan@example.com" class="input input-bordered w-full" />
                </div>
                <div>
                    <label class="label"><span class="label-text">Password</span></label>
                    <input type="password" name="password" required placeholder="••••••••" class="input input-bordered w-full" />
                </div>
                <button type="submit" class="btn btn-primary mt-2">Log In</button>
            </form>
            <div class="text-center mt-4">
                <span class="text-sm opacity-70">Don't have an account?</span>
                <a href="/register" class="link link-hover link-primary text-sm font-semibold">Sign Up</a>
            </div>
        </div>
    </div>
</body>
</html>`
	return c.HTML(http.StatusOK, html)
}

// HandleLogin authenticates password and creates a new stateful session.
func (h *AuthHandler) HandleLogin(c echo.Context) error {
	email := c.FormValue("email")
	password := c.FormValue("password")

	var userID, hash string
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT id, password_hash FROM users WHERE email = ?`, email).Scan(&userID, &hash)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "Invalid credentials")
	}

	valid, err := auth.VerifyPassword(password, hash)
	if err != nil || !valid {
		return echo.NewHTTPError(http.StatusUnauthorized, "Invalid credentials")
	}

	return h.createSessionAndRedirect(c, userID)
}

// HandleLogout revokes the session and clears the cookie.
func (h *AuthHandler) HandleLogout(c echo.Context) error {
	cookie, err := c.Cookie(SessionCookieName)
	if err == nil && cookie.Value != "" {
		tokenHash := auth.HashToken(cookie.Value)
		h.mainDB.ExecContext(c.Request().Context(), `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	}

	c.SetCookie(&http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return c.Redirect(http.StatusSeeOther, "/login")
}

func (h *AuthHandler) createSessionAndRedirect(c echo.Context, userID string) error {
	rawToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Session error")
	}
	csrfToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Session error")
	}

	tokenHash := auth.HashToken(rawToken)
	sessionID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	expiresAt := now + int64(30*24*3600*1_000_000) // 30 days

	_, err = h.mainDB.ExecContext(c.Request().Context(), `
		INSERT INTO sessions (id, token_hash, user_id, csrf_token, created_at, last_active_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, tokenHash, userID, csrfToken, now, now, expiresAt,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create session")
	}

	c.SetCookie(&http.Cookie{
		Name:     SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		Expires:  time.Now().Add(30 * 24 * time.Hour),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return c.Redirect(http.StatusSeeOther, "/budgets")
}
