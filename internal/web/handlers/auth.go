package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/service"
	"github.com/labstack/echo/v4"
)

type AuthHandler struct {
	mainDB     *sql.DB
	tenantMgr  *db.TenantDBManager
	appService *service.AppService
}

func NewAuthHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *AuthHandler {
	return &AuthHandler{
		mainDB:    mainDB,
		tenantMgr: tenantMgr,
	}
}

func (h *AuthHandler) SetService(svc *service.AppService) {
	h.appService = svc
}

// RegisterPage renders the registration HTML page with DaisyUI 5 styling.
func (h *AuthHandler) RegisterPage(c echo.Context) error {
	html := `<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Create Account</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex items-center justify-center p-4">
    <div class="card card-border bg-base-100 shadow-2xl w-full max-w-md border-base-300">
        <div class="card-body gap-4 p-8">
            <div class="flex flex-col items-center text-center gap-1">
                <div class="w-14 h-14 rounded-2xl bg-primary/10 text-primary flex items-center justify-center text-3xl font-bold shadow-inner mb-2">⚖️</div>
                <h1 class="text-2xl font-black tracking-tight">Create your account</h1>
                <p class="text-xs text-base-content/60">Envelope budgeting with multi-currency clarity.</p>
            </div>

            <div class="divider my-0 text-xs opacity-40">ENTER DETAILS</div>

            <form method="POST" action="/register" class="flex flex-col gap-4">
                <div class="form-control">
                    <label class="label pb-1">
                        <span class="label-text font-medium text-xs">Full Name / Display Name</span>
                    </label>
                    <input type="text" name="display_name" required placeholder="Jan Kowalski" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1">
                        <span class="label-text font-medium text-xs">Email Address</span>
                    </label>
                    <input type="email" name="email" required placeholder="jan@example.com" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1">
                        <span class="label-text font-medium text-xs">Password</span>
                    </label>
                    <input type="password" name="password" minlength="8" required placeholder="Minimum 8 characters" class="input input-bordered w-full input-md focus:input-primary" />
                </div>

                <button type="submit" class="btn btn-primary w-full shadow-lg mt-2 font-semibold">
                    Get Started Free
                </button>
            </form>

            <div class="text-center pt-2">
                <span class="text-xs text-base-content/60">Already have an account?</span>
                <a href="/login" class="link link-primary link-hover text-xs font-bold ml-1">Log in here</a>
            </div>
        </div>
    </div>
</body>
</html>`
	return c.HTML(http.StatusOK, html)
}

// HandleRegister processes user signup and initializes stateful session.
func (h *AuthHandler) HandleRegister(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:auth")

	email := c.FormValue("email")
	password := c.FormValue("password")
	displayName := c.FormValue("display_name")

	if email == "" || len(password) < 8 || displayName == "" {
		logger.Warn(ctx, "Registration failed validation: invalid input", "email", email)
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid registration input (password min 8 characters)")
	}

	var userID string
	if h.appService != nil {
		id, err := h.appService.CreateUser(c.Request().Context(), service.CreateUserInput{
			Email:       email,
			Password:    password,
			DisplayName: displayName,
		})
		if err != nil {
			if errors.Is(err, service.ErrUserAlreadyExists) {
				logger.Warn(ctx, "Registration conflict: email already registered", "email", email)
				return echo.NewHTTPError(http.StatusConflict, "Email already in use")
			}
			logger.Warn(ctx, "Registration failed: "+err.Error(), "email", email)
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		userID = id
	} else {
		hash, err := auth.HashPassword(password)
		if err != nil {
			logger.Error(ctx, "Registration failed: password hashing error", "email", email, "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to hash password")
		}

		now := time.Now().UTC().UnixMicro()
		userID = domain.NewUUIDv7()

		_, err = h.mainDB.ExecContext(c.Request().Context(), `
			INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			userID, email, hash, displayName, now, now,
		)
		if err != nil {
			logger.Warn(ctx, "Registration conflict: email already registered", "email", email)
			return echo.NewHTTPError(http.StatusConflict, "Email already in use")
		}
	}

	logger.Info(ctx, "User account registered successfully", "user_id", userID, "email", email, "display_name", displayName)
	return h.createSessionAndRedirect(c, userID)
}

// LoginPage renders the login HTML page with DaisyUI 5 styling.
func (h *AuthHandler) LoginPage(c echo.Context) error {
	return h.renderLoginPage(c, http.StatusOK, "", "")
}

func (h *AuthHandler) renderLoginPage(c echo.Context, status int, errMsg string, emailVal string) error {
	errorBanner := ""
	if errMsg != "" {
		errorBanner = fmt.Sprintf(`
            <div role="alert" class="alert alert-error alert-soft text-xs font-medium shadow-xs">
                <span>⚠️</span>
                <span>%s</span>
            </div>`, html.EscapeString(errMsg))
	}

	pageHTML := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Welcome Back</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex items-center justify-center p-4">
    <div class="card card-border bg-base-100 shadow-2xl w-full max-w-md border-base-300">
        <div class="card-body gap-4 p-8">
            <div class="flex flex-col items-center text-center gap-1">
                <div class="w-14 h-14 rounded-2xl bg-primary/10 text-primary flex items-center justify-center text-3xl font-bold shadow-inner mb-2">⚖️</div>
                <h1 class="text-2xl font-black tracking-tight">Welcome to Balance</h1>
                <p class="text-xs text-base-content/60">Log in to manage your budget and envelopes.</p>
            </div>

            <div class="divider my-0 text-xs opacity-40">CREDENTIALS</div>
            %s
            <form method="POST" action="/login" class="flex flex-col gap-4">
                <div class="form-control">
                    <label class="label pb-1">
                        <span class="label-text font-medium text-xs">Email Address</span>
                    </label>
                    <input type="email" name="email" value="%s" required placeholder="jan@example.com" class="input input-bordered w-full input-md focus:input-primary" />
                </div>
                <div class="form-control">
                    <label class="label pb-1">
                        <span class="label-text font-medium text-xs">Password</span>
                    </label>
                    <input type="password" name="password" required placeholder="••••••••" class="input input-bordered w-full input-md focus:input-primary" />
                </div>

                <button type="submit" class="btn btn-primary w-full shadow-lg mt-2 font-semibold">
                    Sign In
                </button>
            </form>

            <div class="text-center pt-2">
                <span class="text-xs text-base-content/60">Don't have an account?</span>
                <a href="/register" class="link link-primary link-hover text-xs font-bold ml-1">Create one here</a>
            </div>
        </div>
    </div>
</body>
</html>`, errorBanner, html.EscapeString(emailVal))
	return c.HTML(status, pageHTML)
}

// HandleLogin authenticates password and creates a new stateful session.
func (h *AuthHandler) HandleLogin(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:auth")

	email := c.FormValue("email")
	password := c.FormValue("password")

	var userID, hash string
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT id, password_hash FROM users WHERE email = ?`, email).Scan(&userID, &hash)
	if err != nil {
		logger.Warn(ctx, "Login failed: user not found", "email", email)
		return h.renderLoginPage(c, http.StatusUnauthorized, "Invalid email or password. Please try again.", email)
	}

	valid, err := auth.VerifyPassword(password, hash)
	if err != nil || !valid {
		logger.Warn(ctx, "Login failed: invalid password", "email", email, "user_id", userID)
		return h.renderLoginPage(c, http.StatusUnauthorized, "Invalid email or password. Please try again.", email)
	}

	logger.Info(ctx, "User authenticated successfully", "user_id", userID, "email", email)
	return h.createSessionAndRedirect(c, userID)
}

// HandleLogout revokes the session and clears the cookie.
func (h *AuthHandler) HandleLogout(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:auth")

	cookie, err := c.Cookie(SessionCookieName)
	if err == nil && cookie.Value != "" {
		tokenHash := auth.HashToken(cookie.Value)
		h.mainDB.ExecContext(c.Request().Context(), `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		logger.Info(ctx, "User session revoked upon logout")
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
	ctx := logger.WithComponent(c.Request().Context(), "biz:auth")

	rawToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		logger.Error(ctx, "Failed generating session token", "user_id", userID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Session error")
	}
	csrfToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		logger.Error(ctx, "Failed generating CSRF token", "user_id", userID, "error", err.Error())
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
		logger.Error(ctx, "Failed saving session to database", "user_id", userID, "error", err.Error())
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

	logger.Info(ctx, "Session established successfully", "user_id", userID, "session_id", sessionID)
	return c.Redirect(http.StatusSeeOther, "/budgets")
}
