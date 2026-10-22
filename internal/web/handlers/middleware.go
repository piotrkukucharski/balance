package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/labstack/echo/v4"
)

const (
	SessionCookieName  = "balance_session"
	ContextUserIDKey   = "userID"
	ContextSessionKey  = "sessionToken"
	ContextRoleKey     = "budgetRole"
	ContextBudgetDBKey = "budgetDB"
)

// CorrelationIDMiddleware assigns or propagates correlation ID and attaches it to request context.
func CorrelationIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			corrID := c.Request().Header.Get("X-Correlation-ID")
			if corrID == "" || !domain.IsValidUUIDv7(corrID) {
				corrID = domain.NewUUIDv7()
			}
			c.Set("correlationID", corrID)
			c.Response().Header().Set("X-Correlation-ID", corrID)

			reqCtx, _ := logger.WithCorrelationID(c.Request().Context(), corrID)
			c.SetRequest(c.Request().WithContext(reqCtx))
			return next(c)
		}
	}
}

// StructuredRequestLoggerMiddleware logs every HTTP request with unified JSON structure and correlation ID.
func StructuredRequestLoggerMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			latency := time.Since(start)

			req := c.Request()
			res := c.Response()
			ctx := req.Context()
			ctx = logger.WithComponent(ctx, "http")

			status := res.Status
			if err != nil {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				}
			}

			args := []any{
				"method", req.Method,
				"path", req.URL.Path,
				"status", status,
				"latency_ms", float64(latency.Microseconds()) / 1000.0,
				"bytes_out", res.Size,
				"remote_ip", c.RealIP(),
			}

			if userID, ok := c.Get(ContextUserIDKey).(string); ok && userID != "" {
				args = append(args, "user_id", userID)
			}
			if budgetID, ok := c.Get("budgetID").(string); ok && budgetID != "" {
				args = append(args, "budget_id", budgetID)
			}

			if err != nil {
				args = append(args, "error", err.Error())
				logger.Error(ctx, "HTTP request failed", args...)
			} else {
				logger.Info(ctx, "HTTP request handled", args...)
			}

			return err
		}
	}
}

// SessionOrBearerAuthMiddleware enforces dual auth: session cookie OR Bearer PAT.
func SessionOrBearerAuthMiddleware(mainDB *sql.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				// PAT Authentication
				token := strings.TrimPrefix(authHeader, "Bearer ")
				tokenHash := auth.HashToken(token)

				var userID string
				var expiresAt int64
				var revokedAt sql.NullInt64
				now := time.Now().UTC().UnixMicro()

				err := mainDB.QueryRowContext(c.Request().Context(), `
					SELECT user_id, expires_at, revoked_at FROM api_tokens
					WHERE token_hash = ?`, tokenHash).Scan(&userID, &expiresAt, &revokedAt)
				if err != nil || (revokedAt.Valid && revokedAt.Int64 > 0) || (expiresAt > 0 && expiresAt < now) {
					return echo.NewHTTPError(http.StatusUnauthorized, "Invalid or expired Bearer token")
				}

				// Asynchronously update last_used_at in the background to avoid blocking request
				go func(db *sql.DB, hash string, usedAt int64) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_, _ = db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`, usedAt, hash)
				}(mainDB, tokenHash, now)

				c.Set(ContextUserIDKey, userID)
				return next(c)
			}

			// Session Cookie Authentication
			cookie, err := c.Cookie(SessionCookieName)
			if err != nil || cookie.Value == "" {
				// If HTMX / HTML request, redirect to /login
				if strings.Contains(c.Request().Header.Get("Accept"), "text/html") {
					return c.Redirect(http.StatusSeeOther, "/login")
				}
				return echo.NewHTTPError(http.StatusUnauthorized, "Authentication required")
			}

			tokenHash := auth.HashToken(cookie.Value)
			var userID, csrfToken string
			var expiresAt int64
			now := time.Now().UTC().UnixMicro()

			err = mainDB.QueryRowContext(c.Request().Context(), `
				SELECT user_id, csrf_token, expires_at FROM sessions
				WHERE token_hash = ?`, tokenHash).Scan(&userID, &csrfToken, &expiresAt)
			if err != nil || expiresAt < now {
				// Clear the invalid or expired session cookie
				c.SetCookie(&http.Cookie{
					Name:     SessionCookieName,
					Value:    "",
					Path:     "/",
					MaxAge:   -1,
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
				})

				if strings.Contains(c.Request().Header.Get("Accept"), "text/html") {
					return c.Redirect(http.StatusSeeOther, "/login")
				}
				return echo.NewHTTPError(http.StatusUnauthorized, "Session expired or invalid")
			}

			// CSRF Check on mutating requests
			method := c.Request().Method
			if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
				reqCSRF := c.Request().Header.Get("X-CSRF-Token")
				if reqCSRF == "" {
					reqCSRF = c.FormValue("csrf_token")
				}
				if reqCSRF == "" || reqCSRF != csrfToken {
					return echo.NewHTTPError(http.StatusForbidden, "Invalid or missing CSRF token")
				}
			}

			c.Set(ContextUserIDKey, userID)
			c.Set(ContextSessionKey, cookie.Value)
			c.Set("csrfToken", csrfToken)
			return next(c)
		}
	}
}

// TenantAuthzMiddleware checks budget UUIDv7 validity, user membership and role before opening tenant DB.
func TenantAuthzMiddleware(tenantMgr *db.TenantDBManager, minRole string) echo.MiddlewareFunc {
	roleWeights := map[string]int{
		"VIEWER": 1,
		"EDITOR": 2,
		"ADMIN":  3,
		"OWNER":  4,
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			budgetID := c.Param("budgetId")
			if !domain.IsValidUUIDv7(budgetID) {
				return echo.NewHTTPError(http.StatusBadRequest, "Invalid budget ID format")
			}

			userID, ok := c.Get(ContextUserIDKey).(string)
			if !ok || userID == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
			}

			role, err := tenantMgr.VerifyUserMembership(c.Request().Context(), budgetID, userID)
			if err != nil {
				return echo.NewHTTPError(http.StatusForbidden, err.Error())
			}

			if roleWeights[role] < roleWeights[minRole] {
				return echo.NewHTTPError(http.StatusForbidden, "Insufficient budget permissions")
			}

			tenantDB, err := tenantMgr.GetTenantDB(budgetID, false)
			if err != nil {
				return echo.NewHTTPError(http.StatusNotFound, "Tenant database not found")
			}

			c.Set(ContextRoleKey, role)
			c.Set(ContextBudgetDBKey, tenantDB)
			c.Set("budgetID", budgetID)
			return next(c)
		}
	}
}
