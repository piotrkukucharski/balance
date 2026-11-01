package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/i18n"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/service"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

// IsRegistrationAllowed checks if user registration is enabled via ALLOW_REGISTRATION environment variable.
// Delegates to auth.IsRegistrationAllowed().
func IsRegistrationAllowed() bool {
	return auth.IsRegistrationAllowed()
}

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
	if !IsRegistrationAllowed() {
		return echo.NewHTTPError(http.StatusForbidden, "User registration is currently disabled.")
	}
	locale := i18n.DetectLocaleFromHeader(c.Request().Header.Get("Accept-Language"))
	data := map[string]any{
		"Locale": locale,
	}
	out, err := templates.RenderString("auth/register", data)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering register page")
	}
	return c.HTML(http.StatusOK, out)
}

// HandleRegister processes user signup and initializes stateful session.
func (h *AuthHandler) HandleRegister(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:auth")

	if !IsRegistrationAllowed() {
		logger.Warn(ctx, "Registration attempt blocked: registration is disabled via ALLOW_REGISTRATION")
		return echo.NewHTTPError(http.StatusForbidden, "User registration is currently disabled.")
	}

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
		userLocale := i18n.DetectLocaleFromHeader(c.Request().Header.Get("Accept-Language"))

		_, err = h.mainDB.ExecContext(c.Request().Context(), `
			INSERT INTO users (id, email, password_hash, display_name, locale, date_format, time_format, number_format, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'auto', 'auto', 'auto', ?, ?)`,
			userID, email, hash, displayName, userLocale, now, now,
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

type loginPageData struct {
	Locale              string
	ErrorMessage        string
	EmailValue          string
	RegistrationAllowed bool
}

func (h *AuthHandler) renderLoginPage(c echo.Context, status int, errMsg string, emailVal string) error {
	locale := i18n.DetectLocaleFromHeader(c.Request().Header.Get("Accept-Language"))
	data := loginPageData{
		Locale:              locale,
		ErrorMessage:        errMsg,
		EmailValue:          emailVal,
		RegistrationAllowed: IsRegistrationAllowed(),
	}
	pageHTML, err := templates.RenderString("auth/login", data)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering login page")
	}
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
