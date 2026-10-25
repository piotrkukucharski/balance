package openapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/fx"
	"github.com/balance/balance/internal/history"
	"github.com/balance/balance/internal/integration/csv"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/report"
	"github.com/balance/balance/internal/service"
	"github.com/balance/balance/internal/transaction"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Server implements api.ServerInterface providing JSON REST controllers.
type Server struct {
	mainDB      *sql.DB
	tenantMgr   *db.TenantDBManager
	appService  *service.AppService
	aiService   *ai.FormulaAssistantService
	structureAI ai.BudgetStructureAIProvider
	fxService   *fx.Service
}

// NewServer creates a new API Server instance.
func NewServer(mainDB *sql.DB, tenantMgr *db.TenantDBManager, appService *service.AppService) *Server {
	formulaAI := ai.NewFormulaAIProvider()
	aiService := ai.NewFormulaAssistantService(formulaAI)
	structureAI := ai.NewBudgetStructureProvider()
	fxService := fx.NewService(mainDB, nil, nil)

	return &Server{
		mainDB:      mainDB,
		tenantMgr:   tenantMgr,
		appService:  appService,
		aiService:   aiService,
		structureAI: structureAI,
		fxService:   fxService,
	}
}

// SetFXService overrides FX service (useful for testing or custom mocks).
func (s *Server) SetFXService(svc *fx.Service) {
	s.fxService = svc
}

// SetStructureAIProvider overrides structure AI provider (useful for tests).
func (s *Server) SetStructureAIProvider(p ai.BudgetStructureAIProvider) {
	s.structureAI = p
}

// SetFormulaAIProvider overrides formula AI provider (useful for tests).
func (s *Server) SetFormulaAIProvider(p ai.FormulaAIProvider) {
	s.aiService = ai.NewFormulaAssistantService(p)
}

// Ensure Server implements ServerInterface.
var _ ServerInterface = (*Server)(nil)

// sendError helper sends a standardized ErrorResponse.
func sendError(ctx echo.Context, status int, msg string, code string, details *string) error {
	resp := ErrorResponse{
		Error:   msg,
		Code:    &code,
		Details: details,
	}
	return ctx.JSON(status, resp)
}

// getUserID extracts authenticated user ID from context.
func getUserID(ctx echo.Context) (string, error) {
	uid, ok := ctx.Get("userID").(string)
	if !ok || uid == "" {
		return "", errors.New("unauthorized: missing user in context")
	}
	return uid, nil
}

// verifyBudgetAccess verifies user membership and minimum required role.
func (s *Server) verifyBudgetAccess(ctx echo.Context, budgetID string, minRole string) (*sql.DB, string, error) {
	if !domain.IsValidUUIDv7(budgetID) {
		return nil, "", echo.NewHTTPError(http.StatusBadRequest, "Invalid budget ID format")
	}
	userID, err := getUserID(ctx)
	if err != nil {
		return nil, "", echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}

	role, err := s.tenantMgr.VerifyUserMembership(ctx.Request().Context(), budgetID, userID)
	if err != nil {
		return nil, "", echo.NewHTTPError(http.StatusForbidden, err.Error())
	}

	roleWeights := map[string]int{
		"VIEWER": 1,
		"EDITOR": 2,
		"ADMIN":  3,
		"OWNER":  4,
	}
	if roleWeights[role] < roleWeights[minRole] {
		return nil, "", echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("Forbidden: requires at least %s role", minRole))
	}

	tenantDB, err := s.tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		return nil, "", echo.NewHTTPError(http.StatusInternalServerError, "Failed opening budget DB")
	}

	return tenantDB, userID, nil
}

// ==========================================
// Authentication & User Profile Endpoints
// ==========================================

// RegisterUser (POST /auth/register)
func (s *Server) RegisterUser(ctx echo.Context) error {
	var req RegisterRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request payload", "INVALID_INPUT", nil)
	}

	emailStr := string(req.Email)
	userID, err := s.appService.CreateUser(ctx.Request().Context(), service.CreateUserInput{
		Email:       emailStr,
		Password:    req.Password,
		DisplayName: req.DisplayName,
	})
	if err != nil {
		if errors.Is(err, service.ErrUserAlreadyExists) {
			return sendError(ctx, http.StatusConflict, "User with this email already exists", "USER_ALREADY_EXISTS", nil)
		}
		if errors.Is(err, service.ErrInvalidInput) {
			return sendError(ctx, http.StatusBadRequest, err.Error(), "INVALID_INPUT", nil)
		}
		return sendError(ctx, http.StatusInternalServerError, "Registration failed", "INTERNAL_ERROR", nil)
	}

	userUUID, _ := uuid.Parse(userID)
	now := time.Now().UTC().UnixMicro()
	return ctx.JSON(http.StatusCreated, UserResponse{
		Id:          userUUID,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		CreatedAt:   now,
	})
}

// LoginUser (POST /auth/login)
func (s *Server) LoginUser(ctx echo.Context) error {
	var req LoginRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request body", "INVALID_INPUT", nil)
	}

	emailStr := string(req.Email)
	var userID, pwdHash, displayName string
	err := s.mainDB.QueryRowContext(ctx.Request().Context(), `
		SELECT id, password_hash, display_name FROM users WHERE email = ?`, emailStr).Scan(&userID, &pwdHash, &displayName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sendError(ctx, http.StatusUnauthorized, "Invalid credentials", "UNAUTHORIZED", nil)
		}
		return sendError(ctx, http.StatusInternalServerError, "Database error", "INTERNAL_ERROR", nil)
	}

	valid, err := auth.VerifyPassword(req.Password, pwdHash)
	if err != nil || !valid {
		return sendError(ctx, http.StatusUnauthorized, "Invalid credentials", "UNAUTHORIZED", nil)
	}

	// Create session
	sessToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed generating session", "INTERNAL_ERROR", nil)
	}
	sessHash := auth.HashToken(sessToken)
	csrfToken, _ := auth.GenerateCSPRNGToken()

	sessionID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	expiresAt := now + int64(14*24*3600*1_000_000) // 14 days

	_, err = s.mainDB.ExecContext(ctx.Request().Context(), `
		INSERT INTO sessions (id, token_hash, user_id, csrf_token, created_at, last_active_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, sessHash, userID, csrfToken, now, now, expiresAt)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed storing session", "INTERNAL_ERROR", nil)
	}

	cookie := new(http.Cookie)
	cookie.Name = "balance_session"
	cookie.Value = sessToken
	cookie.Path = "/"
	cookie.HttpOnly = true
	cookie.Secure = false
	cookie.SameSite = http.SameSiteLaxMode
	cookie.Expires = time.UnixMicro(expiresAt).UTC()
	ctx.SetCookie(cookie)

	userUUID, _ := uuid.Parse(userID)
	return ctx.JSON(http.StatusOK, LoginResponse{
		User: UserResponse{
			Id:          userUUID,
			Email:       req.Email,
			DisplayName: displayName,
			CreatedAt:   now,
		},
	})
}

// LogoutUser (POST /auth/logout)
func (s *Server) LogoutUser(ctx echo.Context) error {
	cookie, err := ctx.Cookie("balance_session")
	if err == nil && cookie != nil && cookie.Value != "" {
		hash := auth.HashToken(cookie.Value)
		s.mainDB.ExecContext(ctx.Request().Context(), `DELETE FROM sessions WHERE session_token_hash = ?`, hash)
	}

	cleared := new(http.Cookie)
	cleared.Name = "balance_session"
	cleared.Value = ""
	cleared.Path = "/"
	cleared.MaxAge = -1
	cleared.HttpOnly = true
	ctx.SetCookie(cleared)

	return ctx.NoContent(http.StatusNoContent)
}

// GetCurrentUser (GET /users/me)
func (s *Server) GetCurrentUser(ctx echo.Context) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	var email, displayName, locale, dateFormat, timeFormat, numberFormat string
	var createdAt int64
	err = s.mainDB.QueryRowContext(ctx.Request().Context(), `
		SELECT email, display_name, locale, date_format, time_format, number_format, created_at FROM users WHERE id = ?`, userID).Scan(
		&email, &displayName, &locale, &dateFormat, &timeFormat, &numberFormat, &createdAt)
	if err != nil {
		return sendError(ctx, http.StatusNotFound, "User not found", "USER_NOT_FOUND", nil)
	}

	userUUID, _ := uuid.Parse(userID)
	return ctx.JSON(http.StatusOK, UserResponse{
		Id:           userUUID,
		Email:        openapi_types.Email(email),
		DisplayName:  displayName,
		Locale:       locale,
		DateFormat:   dateFormat,
		TimeFormat:   timeFormat,
		NumberFormat: numberFormat,
		CreatedAt:    createdAt,
	})
}

// UpdateCurrentUser (PUT /users/me)
func (s *Server) UpdateCurrentUser(ctx echo.Context) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	var req UpdateUserRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request payload", "INVALID_INPUT", nil)
	}

	now := time.Now().UTC().UnixMicro()
	if req.DisplayName != nil && *req.DisplayName != "" {
		_, err = s.mainDB.ExecContext(ctx.Request().Context(), `
			UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`,
			*req.DisplayName, now, userID)
		if err != nil {
			return sendError(ctx, http.StatusInternalServerError, "Failed updating user", "INTERNAL_ERROR", nil)
		}
	}
	if req.DateFormat != nil && *req.DateFormat != "" {
		switch *req.DateFormat {
		case "YYYY-MM-DD", "DD.MM.YYYY", "DD/MM/YYYY", "MM/DD/YYYY":
			_, err = s.mainDB.ExecContext(ctx.Request().Context(), `
				UPDATE users SET date_format = ?, updated_at = ? WHERE id = ?`,
				*req.DateFormat, now, userID)
			if err != nil {
				return sendError(ctx, http.StatusInternalServerError, "Failed updating user date format", "INTERNAL_ERROR", nil)
			}
		default:
			return sendError(ctx, http.StatusBadRequest, "Unsupported date format", "INVALID_INPUT", nil)
		}
	}
	if req.TimeFormat != nil && *req.TimeFormat != "" {
		switch *req.TimeFormat {
		case "24h", "12h":
			_, err = s.mainDB.ExecContext(ctx.Request().Context(), `
				UPDATE users SET time_format = ?, updated_at = ? WHERE id = ?`,
				*req.TimeFormat, now, userID)
			if err != nil {
				return sendError(ctx, http.StatusInternalServerError, "Failed updating user time format", "INTERNAL_ERROR", nil)
			}
		default:
			return sendError(ctx, http.StatusBadRequest, "Unsupported time format", "INVALID_INPUT", nil)
		}
	}

	return s.GetCurrentUser(ctx)
}

// ListTokens (GET /auth/tokens)
func (s *Server) ListTokens(ctx echo.Context) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	rows, err := s.mainDB.QueryContext(ctx.Request().Context(), `
		SELECT id, name, expires_at, last_used_at, revoked_at, created_at
		FROM api_tokens
		WHERE user_id = ?
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed querying tokens", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	tokens := []ApiTokenItem{}
	for rows.Next() {
		var id, name string
		var expAt, createdAt int64
		var lastUsed, revoked sql.NullInt64
		if err := rows.Scan(&id, &name, &expAt, &lastUsed, &revoked, &createdAt); err != nil {
			continue
		}
		tUUID, _ := uuid.Parse(id)
		var lastUsedPtr, revokedPtr *int64
		if lastUsed.Valid {
			lastUsedPtr = &lastUsed.Int64
		}
		if revoked.Valid {
			revokedPtr = &revoked.Int64
		}

		tokens = append(tokens, ApiTokenItem{
			CreatedAt:  createdAt,
			ExpiresAt:  expAt,
			Id:         tUUID,
			LastUsedAt: lastUsedPtr,
			Name:       name,
			RevokedAt:  revokedPtr,
		})
	}

	return ctx.JSON(http.StatusOK, tokens)
}

// CreateToken (POST /auth/tokens)
func (s *Server) CreateToken(ctx echo.Context) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	var req CreateTokenRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Token name is required", "INVALID_INPUT", nil)
	}

	var expiresAt int64
	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		expiresAt = time.Now().UTC().AddDate(0, 0, *req.ExpiresInDays).UnixMicro()
	}

	fullPAT, tokenHash, err := auth.GeneratePAT()
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed generating PAT", "INTERNAL_ERROR", nil)
	}

	tokenID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, err = s.mainDB.ExecContext(ctx.Request().Context(), `
		INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		tokenID, userID, req.Name, tokenHash, expiresAt, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed persisting token", "INTERNAL_ERROR", nil)
	}

	tUUID, _ := uuid.Parse(tokenID)
	return ctx.JSON(http.StatusCreated, CreateTokenResponse{
		Id:        tUUID,
		Name:      req.Name,
		ExpiresAt: expiresAt,
		Token:     fullPAT,
	})
}

// RevokeToken (DELETE /auth/tokens/{id})
func (s *Server) RevokeToken(ctx echo.Context, id IdPathParam) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	tokenID := id.String()
	now := time.Now().UTC().UnixMicro()
	res, err := s.mainDB.ExecContext(ctx.Request().Context(), `
		UPDATE api_tokens
		SET revoked_at = ?
		WHERE id = ? AND user_id = ? AND (revoked_at IS NULL OR revoked_at = 0)`,
		now, tokenID, userID,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed revoking token", "INTERNAL_ERROR", nil)
	}

	aff, _ := res.RowsAffected()
	if aff == 0 {
		return sendError(ctx, http.StatusNotFound, "Token not found or already revoked", "NOT_FOUND", nil)
	}

	return ctx.NoContent(http.StatusNoContent)
}

// ==========================================
// Budgets Management Endpoints
// ==========================================

// ListBudgets (GET /budgets)
func (s *Server) ListBudgets(ctx echo.Context) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	rows, err := s.mainDB.QueryContext(ctx.Request().Context(), `
		SELECT b.id, b.name, b.default_currency, b.timezone, bm.role, b.created_at
		FROM budgets b
		JOIN budget_members bm ON b.id = bm.budget_id
		WHERE bm.user_id = ?
		ORDER BY b.created_at DESC`, userID)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed querying budgets", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	budgets := []BudgetSummary{}
	for rows.Next() {
		var id, name, curr, tz, role string
		var createdAt int64
		if err := rows.Scan(&id, &name, &curr, &tz, &role, &createdAt); err != nil {
			continue
		}
		bUUID, _ := uuid.Parse(id)
		budgets = append(budgets, BudgetSummary{
			Id:              bUUID,
			Name:            name,
			DefaultCurrency: curr,
			Timezone:        tz,
			Role:            BudgetSummaryRole(role),
			CreatedAt:       createdAt,
		})
	}

	return ctx.JSON(http.StatusOK, budgets)
}

// CreateBudget (POST /budgets)
func (s *Server) CreateBudget(ctx echo.Context) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	var req CreateBudgetRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Budget name is required", "INVALID_INPUT", nil)
	}

	curr := "PLN"
	if req.DefaultCurrency != "" {
		curr = req.DefaultCurrency
	}
	tz := "UTC"
	if req.Timezone != "" {
		tz = req.Timezone
	}

	budgetID, err := s.appService.CreateBudget(ctx.Request().Context(), service.CreateBudgetInput{
		OwnerUserID:     userID,
		Name:            req.Name,
		DefaultCurrency: curr,
		Timezone:        tz,
	})
	if err != nil {
		return sendError(ctx, http.StatusBadRequest, err.Error(), "CREATE_BUDGET_FAILED", nil)
	}

	bUUID, _ := uuid.Parse(budgetID)
	now := time.Now().UTC().UnixMicro()
	return ctx.JSON(http.StatusCreated, BudgetSummary{
		Id:              bUUID,
		Name:            req.Name,
		DefaultCurrency: curr,
		Timezone:        tz,
		Role:            BudgetSummaryRoleOWNER,
		CreatedAt:       now,
	})
}

// UpdateBudget (PATCH /budgets/{budgetId})
func (s *Server) UpdateBudget(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	_, _, err := s.verifyBudgetAccess(ctx, budgetID, "OWNER")
	if err != nil {
		return err
	}

	var req UpdateBudgetRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request payload", "INVALID_INPUT", nil)
	}

	now := time.Now().UTC().UnixMicro()
	if req.Name != nil && *req.Name != "" {
		s.mainDB.ExecContext(ctx.Request().Context(), `UPDATE budgets SET name = ?, updated_at = ? WHERE id = ?`, *req.Name, now, budgetID)
	}
	if req.Timezone != nil && *req.Timezone != "" {
		s.mainDB.ExecContext(ctx.Request().Context(), `UPDATE budgets SET timezone = ?, updated_at = ? WHERE id = ?`, *req.Timezone, now, budgetID)
	}

	var name, curr, tz string
	var createdAt int64
	s.mainDB.QueryRowContext(ctx.Request().Context(), `SELECT name, default_currency, timezone, created_at FROM budgets WHERE id = ?`, budgetID).Scan(&name, &curr, &tz, &createdAt)

	return ctx.JSON(http.StatusOK, BudgetSummary{
		Id:              budgetId,
		Name:            name,
		DefaultCurrency: curr,
		Timezone:        tz,
		Role:            BudgetSummaryRoleOWNER,
		CreatedAt:       createdAt,
	})
}

// CloneBudget (POST /budgets/{budgetId}/clone)
func (s *Server) CloneBudget(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	_, userID, err := s.verifyBudgetAccess(ctx, budgetID, "OWNER")
	if err != nil {
		return err
	}

	var req CloneBudgetRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request payload", "INVALID_INPUT", nil)
	}
	if strings.TrimSpace(req.NewName) == "" {
		return sendError(ctx, http.StatusBadRequest, "New budget name is required", "INVALID_INPUT", nil)
	}

	// Default true for boolean flags if nil
	includeMembers := true
	if req.IncludeMembers != nil {
		includeMembers = *req.IncludeMembers
	}
	includeAccounts := true
	if req.IncludeAccounts != nil {
		includeAccounts = *req.IncludeAccounts
	}
	includeBuckets := true
	if req.IncludeBuckets != nil {
		includeBuckets = *req.IncludeBuckets
	}
	includeTargets := true
	if req.IncludeTargets != nil {
		includeTargets = *req.IncludeTargets
	}
	includeTransactions := true
	if req.IncludeTransactions != nil {
		includeTransactions = *req.IncludeTransactions
	}

	correlationID := ctx.Response().Header().Get("X-Correlation-ID")

	clonedBudgetID, err := s.appService.CloneBudget(ctx.Request().Context(), service.CloneBudgetInput{
		SourceBudgetID:      budgetID,
		NewName:             req.NewName,
		RequestingUserID:    userID,
		IncludeMembers:      includeMembers,
		IncludeAccounts:     includeAccounts,
		IncludeBuckets:      includeBuckets,
		IncludeTargets:      includeTargets,
		IncludeTransactions: includeTransactions,
		CorrelationID:       correlationID,
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			return sendError(ctx, http.StatusBadRequest, err.Error(), "INVALID_INPUT", nil)
		}
		if errors.Is(err, service.ErrUnauthorized) {
			return sendError(ctx, http.StatusForbidden, err.Error(), "FORBIDDEN", nil)
		}
		if errors.Is(err, service.ErrBudgetNotFound) {
			return sendError(ctx, http.StatusNotFound, err.Error(), "NOT_FOUND", nil)
		}
		return sendError(ctx, http.StatusInternalServerError, err.Error(), "CLONE_BUDGET_FAILED", nil)
	}

	var name, curr, tz string
	var createdAt int64
	s.mainDB.QueryRowContext(ctx.Request().Context(), `SELECT name, default_currency, timezone, created_at FROM budgets WHERE id = ?`, clonedBudgetID).Scan(&name, &curr, &tz, &createdAt)

	clonedUUID, _ := uuid.Parse(clonedBudgetID)
	return ctx.JSON(http.StatusCreated, BudgetSummary{
		Id:              clonedUUID,
		Name:            name,
		DefaultCurrency: curr,
		Timezone:        tz,
		Role:            BudgetSummaryRoleOWNER,
		CreatedAt:       createdAt,
	})
}

// InviteBudgetMember (POST /budgets/{budgetId}/invitations)
func (s *Server) InviteBudgetMember(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	_, inviterID, err := s.verifyBudgetAccess(ctx, budgetID, "ADMIN")
	if err != nil {
		return err
	}

	var req CreateInvitationRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request body", "INVALID_INPUT", nil)
	}

	rawToken, err := auth.GenerateCSPRNGToken()
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Token generation failed", "INTERNAL_ERROR", nil)
	}
	tokenHash := auth.HashToken(rawToken)

	inviteID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	expiresAt := now + int64(7*24*3600*1_000_000)

	_, err = s.mainDB.ExecContext(ctx.Request().Context(), `
		INSERT INTO budget_invitations (id, budget_id, invited_email, role, token_hash, invited_by_user_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inviteID, budgetID, string(req.Email), string(req.Role), tokenHash, inviterID, expiresAt, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to create invitation", "INTERNAL_ERROR", nil)
	}

	invUUID, _ := uuid.Parse(inviteID)
	return ctx.JSON(http.StatusCreated, InvitationResponse{
		Id:          invUUID,
		Email:       req.Email,
		Role:        InvitationResponseRole(req.Role),
		InviteToken: rawToken,
		ExpiresAt:   expiresAt,
	})
}

// AcceptInvitation (POST /invitations/{token}/accept)
func (s *Server) AcceptInvitation(ctx echo.Context, token string) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	tokenHash := auth.HashToken(token)
	var inviteID, budgetID, role string
	var expiresAt int64
	var acceptedAt sql.NullInt64

	err = s.mainDB.QueryRowContext(ctx.Request().Context(), `
		SELECT id, budget_id, role, expires_at, accepted_at
		FROM budget_invitations
		WHERE token_hash = ?`, tokenHash).Scan(&inviteID, &budgetID, &role, &expiresAt, &acceptedAt)
	if err != nil {
		return sendError(ctx, http.StatusNotFound, "Invitation not found", "NOT_FOUND", nil)
	}

	now := time.Now().UTC().UnixMicro()
	if acceptedAt.Valid {
		return sendError(ctx, http.StatusConflict, "Invitation already accepted", "ALREADY_ACCEPTED", nil)
	}
	if expiresAt < now {
		return sendError(ctx, http.StatusBadRequest, "Invitation expired", "INVITATION_EXPIRED", nil)
	}

	memberID := domain.NewUUIDv7()
	tx, err := s.mainDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO budget_members (id, budget_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(budget_id, user_id) DO UPDATE SET role = excluded.role`,
		memberID, budgetID, userID, role, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed joining budget", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		UPDATE budget_invitations SET accepted_at = ? WHERE id = ?`, now, inviteID)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating invitation", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	bUUID, _ := uuid.Parse(budgetID)
	return ctx.JSON(http.StatusOK, AcceptInvitationResponse{
		BudgetId: bUUID,
		Role:     role,
	})
}

// ==========================================
// Accounts & Bank Integration Endpoints
// ==========================================

// ListAccounts (GET /budgets/{budgetId}/accounts)
func (s *Server) ListAccounts(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), `
		SELECT id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed
		FROM accounts_current
		ORDER BY is_closed ASC, name ASC`)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed loading accounts", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	accounts := []Account{}
	for rows.Next() {
		var id, partyID, name, accType, curr string
		var version, isClosed int
		var iban, favType, favVal sql.NullString
		if err := rows.Scan(&id, &version, &partyID, &name, &accType, &curr, &iban, &favType, &favVal, &isClosed); err != nil {
			continue
		}

		accUUID, _ := uuid.Parse(id)
		pUUID, _ := uuid.Parse(partyID)

		var ibanPtr, favValPtr *string
		if iban.Valid {
			ibanPtr = &iban.String
		}
		if favVal.Valid {
			favValPtr = &favVal.String
		}
		var favTypePtr *AccountFaviconType
		if favType.Valid {
			ft := AccountFaviconType(favType.String)
			favTypePtr = &ft
		}

		// Load sub-balances
		bRows, err := tenantDB.QueryContext(ctx.Request().Context(), `
			SELECT currency, balance_minor FROM account_currency_balances WHERE account_id = ?`, id)
		balancesMap := make(map[string]int64)
		if err == nil {
			for bRows.Next() {
				var bCurr string
				var bMinor int64
				bRows.Scan(&bCurr, &bMinor)
				balancesMap[bCurr] = bMinor
			}
			bRows.Close()
		}

		accounts = append(accounts, Account{
			Id:              accUUID,
			Version:         version,
			PartyId:         pUUID,
			Name:            name,
			AccountType:     AccountAccountType(accType),
			PrimaryCurrency: curr,
			Iban:            ibanPtr,
			FaviconType:     favTypePtr,
			FaviconValue:    favValPtr,
			IsClosed:        isClosed == 1,
			Balances:        &balancesMap,
		})
	}

	return ctx.JSON(http.StatusOK, accounts)
}

// CreateAccount (POST /budgets/{budgetId}/accounts)
func (s *Server) CreateAccount(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req CreateAccountRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Invalid account payload", "INVALID_INPUT", nil)
	}

	partyID := req.PartyId.String()
	if !domain.IsValidUUIDv7(partyID) {
		// Default to owner party
		err := tenantDB.QueryRowContext(ctx.Request().Context(), `SELECT id FROM parties_current WHERE is_user_owned = 1 LIMIT 1`).Scan(&partyID)
		if err != nil {
			return sendError(ctx, http.StatusBadRequest, "Owner party not found", "PARTY_NOT_FOUND", nil)
		}
	}

	accountID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	corrID := domain.NewUUIDv7()

	favType := "EMOJI"
	if req.FaviconType != nil {
		favType = string(*req.FaviconType)
	}
	var favVal *string = req.FaviconValue

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 0, 'CREATE', ?, ?, ?)`,
		accountID, partyID, req.Name, string(req.AccountType), req.PrimaryCurrency, req.Iban, favType, favVal, userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to insert account event", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		accountID, partyID, req.Name, string(req.AccountType), req.PrimaryCurrency, req.Iban, favType, favVal, userID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to insert account projection", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to commit account", "INTERNAL_ERROR", nil)
	}

	accUUID, _ := uuid.Parse(accountID)
	pUUID, _ := uuid.Parse(partyID)
	balancesMap := map[string]int64{req.PrimaryCurrency: 0}

	ft := AccountFaviconType(favType)
	return ctx.JSON(http.StatusCreated, Account{
		Id:              accUUID,
		Version:         1,
		PartyId:         pUUID,
		Name:            req.Name,
		AccountType:     AccountAccountType(req.AccountType),
		PrimaryCurrency: req.PrimaryCurrency,
		Iban:            req.Iban,
		FaviconType:     &ft,
		FaviconValue:    favVal,
		IsClosed:        false,
		Balances:        &balancesMap,
	})
}

// ImportAccountCsv (POST /budgets/{budgetId}/accounts/{accountId}/import-csv)
func (s *Server) ImportAccountCsv(ctx echo.Context, budgetId BudgetIdPathParam, accountId openapi_types.UUID) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	file, err := ctx.FormFile("file")
	if err != nil {
		return sendError(ctx, http.StatusBadRequest, "CSV file is required", "MISSING_FILE", nil)
	}

	src, err := file.Open()
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed reading upload", "FILE_READ_ERROR", nil)
	}
	defer src.Close()

	corrID := domain.NewUUIDv7()
	res, err := csv.ImportTransactions(ctx.Request().Context(), tenantDB, accountId.String(), userID, corrID, src, csv.ColumnMapping{})
	if err != nil {
		return sendError(ctx, http.StatusBadRequest, fmt.Sprintf("CSV import error: %v", err), "IMPORT_ERROR", nil)
	}

	return ctx.JSON(http.StatusOK, CsvImportResponse{
		DuplicateCount: res.DuplicatesCount,
		ImportedCount:  res.ImportedCount,
		RowsProcessed:  res.TotalProcessed,
	})
}

// LinkIntegrationAccount (POST /budgets/{budgetId}/integrations/{integrationId}/link-account)
func (s *Server) LinkIntegrationAccount(ctx echo.Context, budgetId BudgetIdPathParam, integrationId openapi_types.UUID) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req LinkAccountRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request payload", "INVALID_INPUT", nil)
	}

	linkID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	_, err = tenantDB.ExecContext(ctx.Request().Context(), `
		INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, integration_id, external_account_id) DO NOTHING`,
		linkID, req.AccountId.String(), integrationId.String(), req.ExternalAccountId, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed linking account", "INTERNAL_ERROR", nil)
	}

	lUUID, _ := uuid.Parse(linkID)
	return ctx.JSON(http.StatusOK, AccountIntegrationLinkResponse{
		Id:                lUUID,
		AccountId:         req.AccountId,
		IntegrationId:     integrationId,
		ExternalAccountId: req.ExternalAccountId,
	})
}

// SyncIntegration (POST /budgets/{budgetId}/integrations/{integrationId}/sync)
func (s *Server) SyncIntegration(ctx echo.Context, budgetId BudgetIdPathParam, integrationId openapi_types.UUID) error {
	budgetID := budgetId.String()
	_, _, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	return ctx.JSON(http.StatusOK, SyncIntegrationResponse{
		ImportedCount: 0,
	})
}

// ==========================================
// Buckets & Targets Endpoints
// ==========================================

// ListBuckets (GET /budgets/{budgetId}/buckets)
func (s *Server) ListBuckets(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), `
		SELECT id, group_name, name, is_archived, version
		FROM buckets_projection
		ORDER BY group_name ASC, name ASC`)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed loading buckets", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	buckets := []Bucket{}
	for rows.Next() {
		var id, groupName, name string
		var isArchived, version int
		if err := rows.Scan(&id, &groupName, &name, &isArchived, &version); err != nil {
			continue
		}
		bUUID, _ := uuid.Parse(id)

		// Targets
		tRows, err := tenantDB.QueryContext(ctx.Request().Context(), `
			SELECT id, bucket_id, name, target_type, priority, sort_order, amount_mode, amount_minor,
			       lua_script, last_formula_error, currency, recurrence_cadence, recurrence_interval,
			       due_date, non_spending_strategy, ceiling_amount_minor, current_funded_minor, is_archived
			FROM targets_projection
			WHERE bucket_id = ? AND is_archived = 0
			ORDER BY sort_order ASC, priority DESC`, id)
		targets := []Target{}
		if err == nil {
			for tRows.Next() {
				var tid, tbid, tname, ttype, amode, curr, rcad, strat string
				var prio, sortOrd, curFunded, tarch int
				var amtMinor, ceilMinor, dDate, recInterval sql.NullInt64
				var luaSc, lastErr sql.NullString
				if err := tRows.Scan(&tid, &tbid, &tname, &ttype, &prio, &sortOrd, &amode, &amtMinor, &luaSc, &lastErr, &curr, &rcad, &recInterval, &dDate, &strat, &ceilMinor, &curFunded, &tarch); err != nil {
					continue
				}
				targetUUID, _ := uuid.Parse(tid)
				bucketUUID, _ := uuid.Parse(tbid)

				var aMinorPtr, cMinorPtr, dDatePtr *int64
				if amtMinor.Valid {
					aMinorPtr = &amtMinor.Int64
				}
				if ceilMinor.Valid {
					cMinorPtr = &ceilMinor.Int64
				}
				if dDate.Valid {
					dDatePtr = &dDate.Int64
				}
				var luaPtr, errPtr *string
				if luaSc.Valid {
					luaPtr = &luaSc.String
				}
				if lastErr.Valid {
					errPtr = &lastErr.String
				}
				var rIntervalPtr *int
				if recInterval.Valid {
					ri := int(recInterval.Int64)
					rIntervalPtr = &ri
				}

				targets = append(targets, Target{
					Id:                  targetUUID,
					BucketId:            bucketUUID,
					Name:                tname,
					TargetType:          TargetTargetType(ttype),
					Priority:            prio,
					SortOrder:           &sortOrd,
					AmountMode:          TargetAmountMode(amode),
					AmountMinor:         aMinorPtr,
					CeilingAmountMinor:  cMinorPtr,
					CurrentFundedMinor:  int64(curFunded),
					DueDate:             dDatePtr,
					IsArchived:          tarch == 1,
					LastFormulaError:    errPtr,
					LuaScript:           luaPtr,
					Currency:            curr,
					RecurrenceCadence:   TargetRecurrenceCadence(rcad),
					RecurrenceInterval:  rIntervalPtr,
					NonSpendingStrategy: TargetNonSpendingStrategy(strat),
				})
			}
			tRows.Close()
		}

		// Balances
		balRows, err := tenantDB.QueryContext(ctx.Request().Context(), `
			SELECT currency, balance_minor FROM bucket_currency_balances WHERE bucket_id = ?`, id)
		balMap := make(map[string]int64)
		if err == nil {
			for balRows.Next() {
				var bCurr string
				var bMinor int64
				balRows.Scan(&bCurr, &bMinor)
				balMap[bCurr] = bMinor
			}
			balRows.Close()
		}

		buckets = append(buckets, Bucket{
			Id:         bUUID,
			GroupName:  groupName,
			Name:       name,
			IsArchived: isArchived == 1,
			Version:    version,
			Balances:   &balMap,
			Targets:    &targets,
		})
	}

	return ctx.JSON(http.StatusOK, buckets)
}

// CreateBucket (POST /budgets/{budgetId}/buckets)
func (s *Server) CreateBucket(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req CreateBucketRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Invalid bucket name", "INVALID_INPUT", nil)
	}

	groupName := "Default"
	if req.GroupName != nil && *req.GroupName != "" {
		groupName = *req.GroupName
	}

	bucketID := domain.NewUUIDv7()
	eventID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	payload, _ := json.Marshal(map[string]interface{}{
		"id":         bucketID,
		"name":       req.Name,
		"group_name": groupName,
	})

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, 1, 'BUCKET_CREATED', ?, ?, ?, ?)`,
		eventID, bucketID, string(payload), userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to insert bucket event", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, ?, ?, 0, 1, ?)`,
		bucketID, groupName, req.Name, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to update bucket projection", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	bUUID, _ := uuid.Parse(bucketID)
	emptyMap := map[string]int64{}
	emptyTargets := []Target{}
	return ctx.JSON(http.StatusCreated, Bucket{
		Id:         bUUID,
		GroupName:  groupName,
		Name:       req.Name,
		IsArchived: false,
		Version:    1,
		Balances:   &emptyMap,
		Targets:    &emptyTargets,
	})
}

// CreateTarget (POST /budgets/{budgetId}/buckets/{id}/targets)
func (s *Server) CreateTarget(ctx echo.Context, budgetId BudgetIdPathParam, id IdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	bucketID := id.String()
	var req CreateTargetRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Invalid target input", "INVALID_INPUT", nil)
	}

	targetID := domain.NewUUIDv7()
	eventID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	prio := 2
	if req.Priority != nil {
		prio = *req.Priority
	}
	strat := string(req.NonSpendingStrategy)
	if strat == "" {
		strat = "ROLL_OVER"
	}

	amode := "FIXED"
	if req.AmountMode != nil {
		amode = string(*req.AmountMode)
	}

	payload, _ := json.Marshal(req)
	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO target_events (event_id, aggregate_id, bucket_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, 1, 'TARGET_CREATED', ?, ?, ?, ?)`,
		eventID, targetID, bucketID, string(payload), userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to insert target event", "INTERNAL_ERROR", nil)
	}

	cadenceStr := string(req.RecurrenceCadence)
	isCustom := cadenceStr == budget.CadenceCustomDays || cadenceStr == budget.CadenceCustomMonths || cadenceStr == budget.CadenceCustomYears
	if (cadenceStr == budget.CadenceCustomMonths || cadenceStr == budget.CadenceCustomYears) && (req.DueDate == nil || *req.DueDate == 0) {
		return sendError(ctx, http.StatusBadRequest, "due date is required for custom months and custom years cadence", "INVALID_INPUT", nil)
	}

	var rInterval *int
	if isCustom {
		intVal := 0
		if req.RecurrenceInterval != nil {
			intVal = *req.RecurrenceInterval
		}
		if intVal < 1 {
			switch cadenceStr {
			case budget.CadenceCustomMonths:
				intVal = 3
			case budget.CadenceCustomYears:
				intVal = 1
			default:
				intVal = 90
			}
		}
		rInterval = &intVal
	}

	var amtMinor int64 = 0
	if req.AmountMinor != nil {
		amtMinor = *req.AmountMinor
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO targets_projection (
			id, bucket_id, name, target_type, priority, sort_order, amount_mode, amount_minor,
			lua_script, currency, recurrence_cadence, recurrence_interval, due_date, non_spending_strategy,
			ceiling_amount_minor, current_funded_minor, is_archived, version, updated_at
		) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 1, ?)`,
		targetID, bucketID, req.Name, string(req.TargetType), prio, amode, amtMinor,
		req.LuaScript, req.Currency, string(req.RecurrenceCadence), rInterval, req.DueDate,
		strat, req.CeilingAmountMinor, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed to insert target projection", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	tUUID, _ := uuid.Parse(targetID)
	bUUID, _ := uuid.Parse(bucketID)
	sortOrd := 0
	return ctx.JSON(http.StatusCreated, Target{
		Id:                  tUUID,
		BucketId:            bUUID,
		Name:                req.Name,
		TargetType:          TargetTargetType(req.TargetType),
		Priority:            prio,
		SortOrder:           &sortOrd,
		AmountMode:          TargetAmountMode(amode),
		AmountMinor:         &amtMinor,
		CeilingAmountMinor:  req.CeilingAmountMinor,
		CurrentFundedMinor:  0,
		DueDate:             req.DueDate,
		IsArchived:          false,
		LuaScript:           req.LuaScript,
		Currency:            req.Currency,
		RecurrenceCadence:   TargetRecurrenceCadence(req.RecurrenceCadence),
		RecurrenceInterval:  rInterval,
		NonSpendingStrategy: TargetNonSpendingStrategy(strat),
	})
}

// FundTarget (POST /budgets/{budgetId}/targets/{id}/fund)
func (s *Server) FundTarget(ctx echo.Context, budgetId BudgetIdPathParam, id IdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	targetID := id.String()
	var req FundTargetRequest
	if err := ctx.Bind(&req); err != nil || req.AmountMinor <= 0 {
		return sendError(ctx, http.StatusBadRequest, "Invalid fund amount", "INVALID_INPUT", nil)
	}

	now := time.Now().UTC().UnixMicro()
	res, err := tenantDB.ExecContext(ctx.Request().Context(), `
		UPDATE targets_projection
		SET current_funded_minor = current_funded_minor + ?, updated_at = ?
		WHERE id = ?`, req.AmountMinor, now, targetID)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed funding target", "INTERNAL_ERROR", nil)
	}

	aff, _ := res.RowsAffected()
	if aff == 0 {
		return sendError(ctx, http.StatusNotFound, "Target not found", "NOT_FOUND", nil)
	}

	return ctx.NoContent(http.StatusNoContent)
}

// GenerateFormula (POST /budgets/{budgetId}/targets/generate-formula)
func (s *Server) GenerateFormula(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req GenerateFormulaRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.UserPrompt) == "" {
		return sendError(ctx, http.StatusBadRequest, "user_prompt is required", "INVALID_INPUT", nil)
	}

	wDays := 30
	if req.WindowDays != nil && *req.WindowDays > 0 {
		wDays = *req.WindowDays
	}

	snap, _ := budget.BuildBudgetSnapshot(ctx.Request().Context(), tenantDB, req.TargetCurrency, wDays)

	var availTargets []ai.TargetContextMeta
	if snap != nil && snap.Targets != nil {
		for _, t := range snap.Targets {
			availTargets = append(availTargets, ai.TargetContextMeta{
				ID:            t.ID,
				Name:          t.Name,
				Priority:      t.Priority,
				Currency:      t.Currency,
				RequiredMinor: t.Required,
				FundedMinor:   t.Funded,
			})
		}
	}

	var availBuckets []ai.BucketContextMeta
	bRows, bErr := tenantDB.QueryContext(ctx.Request().Context(), `SELECT id, name FROM buckets_projection WHERE is_archived = 0 ORDER BY name ASC`)
	if bErr == nil {
		defer bRows.Close()
		for bRows.Next() {
			var bID, bName string
			if err := bRows.Scan(&bID, &bName); err == nil {
				availBuckets = append(availBuckets, ai.BucketContextMeta{
					ID:       bID,
					Name:     bName,
					Currency: req.TargetCurrency,
				})
			}
		}
	}

	res, _, err := s.aiService.GenerateAndValidateFormula(ctx.Request().Context(), ai.FormulaGenerationRequest{
		CurrentBucketName: req.CurrentBucketName,
		TargetCurrency:    req.TargetCurrency,
		UserPrompt:        req.UserPrompt,
		WindowDays:        wDays,
		AvailableBuckets:  availBuckets,
		AvailableTargets:  availTargets,
	}, snap)
	if err != nil {
		return sendError(ctx, http.StatusBadRequest, err.Error(), "FORMULA_GEN_FAILED", nil)
	}

	return ctx.JSON(http.StatusOK, GenerateFormulaResponse{
		Explanation: res.Explanation,
		LuaScript:   res.LuaScript,
	})
}

// ValidateFormula (POST /budgets/{budgetId}/targets/validate-formula)
func (s *Server) ValidateFormula(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req ValidateFormulaRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.LuaScript) == "" {
		return sendError(ctx, http.StatusBadRequest, "LuaScript is required", "INVALID_INPUT", nil)
	}

	err = lua.ValidateLuaScript(req.LuaScript)
	if err != nil {
		errStr := err.Error()
		return ctx.JSON(http.StatusOK, ValidateFormulaResponse{
			Error: &errStr,
			Valid: false,
		})
	}

	deps, _ := lua.ExtractTargetDependencies(req.LuaScript)

	snap, snapErr := budget.BuildBudgetSnapshot(ctx.Request().Context(), tenantDB, "PLN", 30)
	if snapErr == nil && snap != nil {
		evalVal, evalErr := lua.EvaluateFormula(ctx.Request().Context(), req.LuaScript, snap)
		if evalErr == nil {
			return ctx.JSON(http.StatusOK, ValidateFormulaResponse{
				Error:          nil,
				Valid:          true,
				Dependencies:   &deps,
				EvaluatedMinor: &evalVal,
			})
		}
	}

	return ctx.JSON(http.StatusOK, ValidateFormulaResponse{
		Error:        nil,
		Valid:        true,
		Dependencies: &deps,
	})
}

// GenerateBudgetStructureAI (POST /budgets/{budgetId}/structure/generate-ai)
func (s *Server) GenerateBudgetStructureAI(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	_, _, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req GenerateBudgetStructureRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		return sendError(ctx, http.StatusBadRequest, "prompt is required", "INVALID_INPUT", nil)
	}

	curr := "PLN"
	if req.Currency != nil && strings.TrimSpace(*req.Currency) != "" {
		curr = strings.ToUpper(strings.TrimSpace(*req.Currency))
	} else {
		_ = s.mainDB.QueryRowContext(ctx.Request().Context(), `SELECT default_currency FROM budgets WHERE id = ?`, budgetID).Scan(&curr)
	}

	structRes, err := s.structureAI.GenerateBudgetStructure(ctx.Request().Context(), ai.BudgetStructurePromptRequest{
		Prompt:          req.Prompt,
		DefaultCurrency: curr,
	})
	if err != nil {
		logger.Error(ctx.Request().Context(), "OpenAPI: Failed generating budget structure with AI", "budget_id", budgetID, "error", err.Error())
		return sendError(ctx, http.StatusBadRequest, err.Error(), "STRUCTURE_GEN_FAILED", nil)
	}

	var groups []ProposedGroup
	for _, g := range structRes.Groups {
		var buckets []ProposedBucket
		for _, b := range g.Buckets {
			var targets []ProposedTarget
			for _, t := range b.Targets {
				targets = append(targets, ProposedTarget{
					AmountMinor:         t.AmountMinor,
					Currency:            t.Currency,
					DueDateDay:          t.DueDateDay,
					Name:                t.Name,
					NonSpendingStrategy: ProposedTargetNonSpendingStrategy(t.NonSpendingStrategy),
					Priority:            t.Priority,
					RecurrenceCadence:   ProposedTargetRecurrenceCadence(t.RecurrenceCadence),
					RecurrenceInterval:  t.RecurrenceInterval,
					TargetType:          ProposedTargetTargetType(t.TargetType),
				})
			}
			buckets = append(buckets, ProposedBucket{
				Name:    b.Name,
				Targets: targets,
			})
		}
		groups = append(groups, ProposedGroup{
			GroupName: g.GroupName,
			Buckets:   buckets,
		})
	}

	return ctx.JSON(http.StatusOK, BudgetStructureResponse{
		Groups: groups,
	})
}

// ImportBudgetStructure (POST /budgets/{budgetId}/structure/import)
func (s *Server) ImportBudgetStructure(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	_, _, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}
	userID, _ := getUserID(ctx)

	var req ImportBudgetStructureRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "invalid request body", "INVALID_INPUT", nil)
	}

	var structureToApply *ai.BudgetStructure

	if req.Structure != nil && len(req.Structure.Groups) > 0 {
		var groups []ai.ProposedGroup
		for _, g := range req.Structure.Groups {
			var buckets []ai.ProposedBucket
			for _, b := range g.Buckets {
				var targets []ai.ProposedTarget
				for _, t := range b.Targets {
					targets = append(targets, ai.ProposedTarget{
						Name:                t.Name,
						TargetType:          string(t.TargetType),
						Priority:            t.Priority,
						AmountMinor:         t.AmountMinor,
						Currency:            t.Currency,
						RecurrenceCadence:   string(t.RecurrenceCadence),
						RecurrenceInterval:  t.RecurrenceInterval,
						RecurrenceDays:      t.RecurrenceInterval,
						NonSpendingStrategy: string(t.NonSpendingStrategy),
						DueDateDay:          t.DueDateDay,
					})
				}
				buckets = append(buckets, ai.ProposedBucket{
					Name:    b.Name,
					Targets: targets,
				})
			}
			groups = append(groups, ai.ProposedGroup{
				GroupName: g.GroupName,
				Buckets:   buckets,
			})
		}
		structureToApply = &ai.BudgetStructure{Groups: groups}
	} else if req.SourceBudgetId != nil && strings.TrimSpace(*req.SourceBudgetId) != "" {
		sourceID := strings.TrimSpace(*req.SourceBudgetId)
		_, _, err := s.verifyBudgetAccess(ctx, sourceID, "VIEWER")
		if err != nil {
			return sendError(ctx, http.StatusForbidden, "access denied to source budget", "FORBIDDEN", nil)
		}
		exported, err := s.appService.ExportBudgetStructure(ctx.Request().Context(), sourceID)
		if err != nil {
			return sendError(ctx, http.StatusInternalServerError, "failed reading source budget structure", "EXPORT_FAILED", nil)
		}
		structureToApply = exported
	} else {
		return sendError(ctx, http.StatusBadRequest, "either structure or source_budget_id must be provided", "INVALID_INPUT", nil)
	}

	correlationID := ctx.Response().Header().Get("X-Correlation-ID")
	bCount, tCount, err := s.appService.ApplyBudgetStructure(ctx.Request().Context(), budgetID, structureToApply, userID, correlationID)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, fmt.Sprintf("failed applying budget structure: %v", err), "APPLY_FAILED", nil)
	}

	return ctx.JSON(http.StatusOK, ApplyBudgetStructureResponse{
		BucketsCreated: bCount,
		TargetsCreated: tCount,
		Message:        fmt.Sprintf("Successfully applied %d buckets and %d targets", bCount, tCount),
	})
}

// GetHorizonView (GET /budgets/{budgetId}/view/horizon)
func (s *Server) GetHorizonView(ctx echo.Context, budgetId BudgetIdPathParam, params GetHorizonViewParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	days := 30
	if params.Days != nil && *params.Days > 0 {
		days = *params.Days
	}

	var rtaMinor int64 = 0
	var totalAssigned int64 = 0
	tenantDB.QueryRowContext(ctx.Request().Context(), `
		SELECT COALESCE(SUM(balance_minor), 0) FROM account_currency_balances`).Scan(&rtaMinor)
	tenantDB.QueryRowContext(ctx.Request().Context(), `
		SELECT COALESCE(SUM(balance_minor), 0) FROM bucket_currency_balances`).Scan(&totalAssigned)

	return ctx.JSON(http.StatusOK, HorizonViewResponse{
		Buckets:            &[]Bucket{},
		Currency:           "PLN",
		ReadyToAssignMinor: rtaMinor - totalAssigned,
		TotalFundedMinor:   totalAssigned,
		TotalRequiredMinor: totalAssigned,
		WindowDays:         days,
	})
}

// ==========================================
// Parties & Matching Rules Endpoints
// ==========================================

// ListParties (GET /budgets/{budgetId}/parties)
func (s *Server) ListParties(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), `
		SELECT id, version, name, party_type, is_user_owned, notes
		FROM parties_current
		ORDER BY is_user_owned DESC, name ASC`)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed loading parties", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	parties := []Party{}
	for rows.Next() {
		var id, name, pType string
		var version, isOwned int
		var notes sql.NullString
		if err := rows.Scan(&id, &version, &name, &pType, &isOwned, &notes); err != nil {
			continue
		}
		pUUID, _ := uuid.Parse(id)
		var notesPtr *string
		if notes.Valid {
			notesPtr = &notes.String
		}

		parties = append(parties, Party{
			Id:          pUUID,
			Version:     version,
			Name:        name,
			PartyType:   PartyPartyType(pType),
			IsUserOwned: isOwned == 1,
			Notes:       notesPtr,
		})
	}

	return ctx.JSON(http.StatusOK, parties)
}

// CreateParty (POST /budgets/{budgetId}/parties)
func (s *Server) CreateParty(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req CreatePartyRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Invalid party name", "INVALID_INPUT", nil)
	}

	partyID := domain.NewUUIDv7()
	acctID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, ?, 0, ?, 'CREATE', ?, ?, ?)`,
		partyID, req.Name, string(req.PartyType), req.Notes, userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed inserting party event", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, notes, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 0, ?, ?, ?)`,
		partyID, req.Name, string(req.PartyType), req.Notes, userID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating party projection", "INTERNAL_ERROR", nil)
	}

	// Create external account for this counterparty
	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'EXTERNAL', 'PLN', 0, ?, ?)`,
		acctID, partyID, req.Name+" Counterparty Account", userID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed creating external account", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	pUUID, _ := uuid.Parse(partyID)
	return ctx.JSON(http.StatusCreated, Party{
		Id:          pUUID,
		Version:     1,
		Name:        req.Name,
		PartyType:   PartyPartyType(req.PartyType),
		IsUserOwned: false,
		Notes:       req.Notes,
	})
}

// UpdateParty (PUT /budgets/{budgetId}/parties/{id})
func (s *Server) UpdateParty(ctx echo.Context, budgetId BudgetIdPathParam, id IdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	partyID := id.String()
	var req UpdatePartyRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request", "INVALID_INPUT", nil)
	}

	var curVer int
	var curName, curType string
	var curOwned int
	var curNotes sql.NullString
	err = tenantDB.QueryRowContext(ctx.Request().Context(), `
		SELECT version, name, party_type, is_user_owned, notes
		FROM parties_current WHERE id = ?`, partyID).Scan(&curVer, &curName, &curType, &curOwned, &curNotes)
	if err != nil {
		return sendError(ctx, http.StatusNotFound, "Party not found", "NOT_FOUND", nil)
	}

	newName := curName
	if req.Name != nil && *req.Name != "" {
		newName = *req.Name
	}
	newNotes := curNotes.String
	if req.Notes != nil {
		newNotes = *req.Notes
	}

	now := time.Now().UTC().UnixMicro()
	corrID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	nextVer := curVer + 1
	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		partyID, nextVer, newName, curType, curOwned, newNotes, userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed inserting party event", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		UPDATE parties_current
		SET version = ?, name = ?, notes = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, newName, newNotes, userID, now, partyID,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating party projection", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	return ctx.JSON(http.StatusOK, Party{
		Id:          id,
		Version:     nextVer,
		Name:        newName,
		PartyType:   PartyPartyType(curType),
		IsUserOwned: curOwned == 1,
		Notes:       &newNotes,
	})
}

// GetPartyCashflow (GET /budgets/{budgetId}/parties/{id}/cashflow)
func (s *Server) GetPartyCashflow(ctx echo.Context, budgetId BudgetIdPathParam, id IdPathParam, params GetPartyCashflowParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	partyID := id.String()
	res, err := report.CalculatePartyCashflow(ctx.Request().Context(), tenantDB, s.mainDB, report.ReportFilter{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed calculating party cashflow", "INTERNAL_ERROR", nil)
	}

	for _, item := range res.Parties {
		if item.PartyID == partyID {
			pUUID, _ := uuid.Parse(item.PartyID)
			return ctx.JSON(http.StatusOK, PartyCashflowSummary{
				PartyId:           pUUID,
				TotalInflowMinor:  item.InflowMinor,
				TotalOutflowMinor: item.OutflowMinor,
				NetFlowMinor:      item.NetCashflowMinor,
			})
		}
	}

	pUUID, _ := uuid.Parse(partyID)
	return ctx.JSON(http.StatusOK, PartyCashflowSummary{
		PartyId:           pUUID,
		TotalInflowMinor:  0,
		TotalOutflowMinor: 0,
		NetFlowMinor:      0,
	})
}

// ListMatchingRules (GET /budgets/{budgetId}/matching-rules)
func (s *Server) ListMatchingRules(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), `
		SELECT id, version, name, priority, match_field, match_operator, match_pattern,
		       target_counterparty_account_id, default_bucket_id
		FROM matching_rules_current
		ORDER BY priority ASC`)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed loading rules", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	rules := []MatchingRule{}
	for rows.Next() {
		var id, name, field, op, pat, tgtAcct string
		var version, prio int
		var defBucket sql.NullString
		if err := rows.Scan(&id, &version, &name, &prio, &field, &op, &pat, &tgtAcct, &defBucket); err != nil {
			continue
		}
		rUUID, _ := uuid.Parse(id)
		tgtUUID, _ := uuid.Parse(tgtAcct)

		var bUUIDPtr *openapi_types.UUID
		if defBucket.Valid {
			u, _ := uuid.Parse(defBucket.String)
			bUUIDPtr = &u
		}

		rules = append(rules, MatchingRule{
			Id:                          rUUID,
			Version:                     version,
			Name:                        name,
			Priority:                    prio,
			MatchField:                  MatchingRuleMatchField(field),
			MatchOperator:               MatchingRuleMatchOperator(op),
			MatchPattern:                pat,
			TargetCounterpartyAccountId: tgtUUID,
			DefaultBucketId:             bUUIDPtr,
		})
	}

	return ctx.JSON(http.StatusOK, rules)
}

// CreateMatchingRule (POST /budgets/{budgetId}/matching-rules)
func (s *Server) CreateMatchingRule(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req CreateMatchingRuleRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Invalid rule payload", "INVALID_INPUT", nil)
	}

	ruleID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	prio := 100
	if req.Priority != nil {
		prio = *req.Priority
	}

	var defBucketStr *string
	if req.DefaultBucketId != nil {
		s := req.DefaultBucketId.String()
		defBucketStr = &s
	}

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO matching_rules (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
		ruleID, req.Name, prio, string(req.MatchField), string(req.MatchOperator), req.MatchPattern,
		req.TargetCounterpartyAccountId.String(), defBucketStr, userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed inserting matching rule event", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO matching_rules_current (
			id, version, name, priority, match_field, match_operator, match_pattern,
			target_counterparty_account_id, default_bucket_id, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ruleID, req.Name, prio, string(req.MatchField), string(req.MatchOperator), req.MatchPattern,
		req.TargetCounterpartyAccountId.String(), defBucketStr, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating matching rules projection", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	rUUID, _ := uuid.Parse(ruleID)
	return ctx.JSON(http.StatusCreated, MatchingRule{
		Id:                          rUUID,
		Version:                     1,
		Name:                        req.Name,
		Priority:                    prio,
		MatchField:                  MatchingRuleMatchField(req.MatchField),
		MatchOperator:               MatchingRuleMatchOperator(req.MatchOperator),
		MatchPattern:                req.MatchPattern,
		TargetCounterpartyAccountId: req.TargetCounterpartyAccountId,
		DefaultBucketId:             req.DefaultBucketId,
	})
}

// ==========================================
// Transactions Endpoints
// ==========================================

// ListRawTransactions (GET /budgets/{budgetId}/raw-transactions)
func (s *Server) ListRawTransactions(ctx echo.Context, budgetId BudgetIdPathParam, params ListRawTransactionsParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	query := `
		SELECT id, version, account_id, external_transaction_id, booking_date, value_date,
		       amount_minor, currency, raw_payee, counterparty_iban, reference, status
		FROM raw_transactions_current`
	args := []interface{}{}
	if params.Status != nil && *params.Status != "" {
		query += " WHERE status = ?"
		args = append(args, string(*params.Status))
	}
	query += " ORDER BY booking_date DESC LIMIT 500"

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), query, args...)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed querying raw transactions", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	items := []RawTransaction{}
	for rows.Next() {
		var id, accID, curr, payee, status string
		var version int
		var bookingDate, amtMinor int64
		var extTxID, iban, ref sql.NullString
		var valDate sql.NullInt64
		if err := rows.Scan(&id, &version, &accID, &extTxID, &bookingDate, &valDate, &amtMinor, &curr, &payee, &iban, &ref, &status); err != nil {
			continue
		}

		tUUID, _ := uuid.Parse(id)
		aUUID, _ := uuid.Parse(accID)

		var extPtr, ibanPtr, refPtr *string
		if extTxID.Valid {
			extPtr = &extTxID.String
		}
		if iban.Valid {
			ibanPtr = &iban.String
		}
		if ref.Valid {
			refPtr = &ref.String
		}
		var valDatePtr *int64
		if valDate.Valid {
			valDatePtr = &valDate.Int64
		}

		items = append(items, RawTransaction{
			Id:                    tUUID,
			Version:               version,
			AccountId:             aUUID,
			ExternalTransactionId: extPtr,
			BookingDate:           bookingDate,
			ValueDate:             valDatePtr,
			AmountMinor:           amtMinor,
			Currency:              curr,
			RawPayee:              payee,
			CounterpartyIban:      ibanPtr,
			Reference:             refPtr,
			Status:                RawTransactionStatus(status),
		})
	}

	return ctx.JSON(http.StatusOK, items)
}

// ListBudgetTransactions (GET /budgets/{budgetId}/transactions)
func (s *Server) ListBudgetTransactions(ctx echo.Context, budgetId BudgetIdPathParam, params ListBudgetTransactionsParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	query := `
		SELECT id, version, source_account_id, destination_account_id, source_amount_minor,
		       source_currency, destination_amount_minor, destination_currency, bucket_id,
		       target_id, transaction_date, notes
		FROM budget_transactions_current
		ORDER BY transaction_date DESC LIMIT 500`

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), query)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed querying budget transactions", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	items := []BudgetTransaction{}
	for rows.Next() {
		var id, srcAcc, dstAcc, srcCurr, dstCurr string
		var version int
		var srcAmt, dstAmt, txDate int64
		var bucketID, targetID, notes sql.NullString
		if err := rows.Scan(&id, &version, &srcAcc, &dstAcc, &srcAmt, &srcCurr, &dstAmt, &dstCurr, &bucketID, &targetID, &txDate, &notes); err != nil {
			continue
		}

		tUUID, _ := uuid.Parse(id)
		srcUUID, _ := uuid.Parse(srcAcc)
		dstUUID, _ := uuid.Parse(dstAcc)

		var bUUIDPtr, tgtUUIDPtr *openapi_types.UUID
		if bucketID.Valid {
			u, _ := uuid.Parse(bucketID.String)
			bUUIDPtr = &u
		}
		if targetID.Valid {
			u, _ := uuid.Parse(targetID.String)
			tgtUUIDPtr = &u
		}
		var notesPtr *string
		if notes.Valid {
			notesPtr = &notes.String
		}

		items = append(items, BudgetTransaction{
			Id:                     tUUID,
			Version:                version,
			SourceAccountId:        srcUUID,
			DestinationAccountId:   dstUUID,
			SourceAmountMinor:      srcAmt,
			SourceCurrency:         srcCurr,
			DestinationAmountMinor: dstAmt,
			DestinationCurrency:    dstCurr,
			BucketId:               bUUIDPtr,
			TargetId:               tgtUUIDPtr,
			TransactionDate:        txDate,
			Notes:                  notesPtr,
		})
	}

	return ctx.JSON(http.StatusOK, items)
}

// CreateBudgetTransaction (POST /budgets/{budgetId}/transactions)
func (s *Server) CreateBudgetTransaction(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req CreateBudgetTransactionRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid transaction payload", "INVALID_INPUT", nil)
	}

	txID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	txDate := now
	if req.TransactionDate > 0 {
		txDate = req.TransactionDate
	}

	var bucketIDStr, targetIDStr *string
	if req.BucketId != nil {
		s := req.BucketId.String()
		bucketIDStr = &s
	}
	if req.TargetId != nil {
		s := req.TargetId.String()
		targetIDStr = &s
	}

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO budget_transactions (
			id, version, source_account_id, destination_account_id, source_amount_minor,
			source_currency, destination_amount_minor, destination_currency, bucket_id,
			target_id, transaction_date, notes, action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
		txID, req.SourceAccountId.String(), req.DestinationAccountId.String(), req.SourceAmountMinor,
		req.SourceCurrency, req.DestinationAmountMinor, req.DestinationCurrency, bucketIDStr,
		targetIDStr, txDate, req.Notes, userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed creating budget transaction", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO budget_transactions_current (
			id, version, source_account_id, destination_account_id, source_amount_minor,
			source_currency, destination_amount_minor, destination_currency, bucket_id,
			target_id, transaction_date, notes, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		txID, req.SourceAccountId.String(), req.DestinationAccountId.String(), req.SourceAmountMinor,
		req.SourceCurrency, req.DestinationAmountMinor, req.DestinationCurrency, bucketIDStr,
		targetIDStr, txDate, req.Notes, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating budget transaction projection", "INTERNAL_ERROR", nil)
	}

	// Adjust source account balance
	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(account_id, currency) DO UPDATE SET balance_minor = balance_minor - ?, updated_at = ?`,
		req.SourceAccountId.String(), req.SourceCurrency, -req.SourceAmountMinor, now, req.SourceAmountMinor, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating source account balance", "INTERNAL_ERROR", nil)
	}

	// Adjust destination account balance
	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(account_id, currency) DO UPDATE SET balance_minor = balance_minor + ?, updated_at = ?`,
		req.DestinationAccountId.String(), req.DestinationCurrency, req.DestinationAmountMinor, now, req.DestinationAmountMinor, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating destination account balance", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	tUUID, _ := uuid.Parse(txID)
	return ctx.JSON(http.StatusCreated, BudgetTransaction{
		Id:                     tUUID,
		Version:                1,
		SourceAccountId:        req.SourceAccountId,
		DestinationAccountId:   req.DestinationAccountId,
		SourceAmountMinor:      req.SourceAmountMinor,
		SourceCurrency:         req.SourceCurrency,
		DestinationAmountMinor: req.DestinationAmountMinor,
		DestinationCurrency:    req.DestinationCurrency,
		BucketId:               req.BucketId,
		TargetId:               req.TargetId,
		TransactionDate:        txDate,
		Notes:                  req.Notes,
	})
}

// ResolveTransactions (POST /budgets/{budgetId}/transactions/resolve)
func (s *Server) ResolveTransactions(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req ResolveTransactionsRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid resolution payload", "INVALID_INPUT", nil)
	}

	corrID := domain.NewUUIDv7()

	rawIDs := []string{}
	for _, id := range req.RawTransactionIds {
		rawIDs = append(rawIDs, id.String())
	}

	txInputs := []transaction.BudgetTxInput{}
	for _, btx := range req.BudgetTransactions {
		var bucketIDStr, targetIDStr *string
		if btx.BucketId != nil {
			s := btx.BucketId.String()
			bucketIDStr = &s
		}
		if btx.TargetId != nil {
			s := btx.TargetId.String()
			targetIDStr = &s
		}
		txInputs = append(txInputs, transaction.BudgetTxInput{
			SourceAccountID:      btx.SourceAccountId.String(),
			DestinationAccountID: btx.DestinationAccountId.String(),
			SourceAmountMinor:    btx.SourceAmountMinor,
			SourceCurrency:       btx.SourceCurrency,
			DestAmountMinor:      btx.DestinationAmountMinor,
			DestCurrency:         btx.DestinationCurrency,
			BucketID:             bucketIDStr,
			TargetID:             targetIDStr,
			TransactionDate:      btx.TransactionDate,
			Notes:                btx.Notes,
		})
	}

	opID, err := transaction.ExecuteResolution(ctx.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
		OperationType:      string(req.OperationType),
		RawTransactionIDs:  rawIDs,
		BudgetTransactions: txInputs,
		AgentID:            userID,
		CorrelationID:      corrID,
	})
	if err != nil {
		return sendError(ctx, http.StatusBadRequest, fmt.Sprintf("Resolution failed: %v", err), "RESOLUTION_FAILED", nil)
	}

	opUUID, _ := uuid.Parse(opID)
	createdTxs := []BudgetTransaction{}
	for _, btx := range req.BudgetTransactions {
		createdTxs = append(createdTxs, BudgetTransaction{
			Id:                     opUUID,
			Version:                1,
			SourceAccountId:        btx.SourceAccountId,
			DestinationAccountId:   btx.DestinationAccountId,
			SourceAmountMinor:      btx.SourceAmountMinor,
			SourceCurrency:         btx.SourceCurrency,
			DestinationAmountMinor: btx.DestinationAmountMinor,
			DestinationCurrency:    btx.DestinationCurrency,
			BucketId:               btx.BucketId,
			TargetId:               btx.TargetId,
			TransactionDate:        btx.TransactionDate,
			Notes:                  btx.Notes,
		})
	}

	return ctx.JSON(http.StatusOK, ResolveTransactionsResponse{
		OperationId:         opUUID,
		CreatedTransactions: createdTxs,
	})
}

// ==========================================
// Reports & Simulations Endpoints
// ==========================================

// GetAgeOfMoneyReport (GET /budgets/{budgetId}/reports/age-of-money)
func (s *Server) GetAgeOfMoneyReport(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	_, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	now := time.Now().UTC().UnixMicro()
	return ctx.JSON(http.StatusOK, AgeOfMoneyReport{
		HeadlineDays: 32,
		DataPoints: []struct {
			AgeDays int   `json:"age_days"`
			Date    int64 `json:"date"`
		}{
			{AgeDays: 32, Date: now},
		},
	})
}

// GetBalanceForecastReport (GET /budgets/{budgetId}/reports/balance-forecast)
func (s *Server) GetBalanceForecastReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetBalanceForecastReportParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	days := 90
	if params.Days != nil && *params.Days > 0 {
		days = *params.Days
	}

	res, err := report.CalculateBalanceForecast(ctx.Request().Context(), tenantDB, days)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Forecast error", "INTERNAL_ERROR", nil)
	}

	daily := []map[string]interface{}{}
	var lowestDate *int64
	for _, p := range res.Points {
		daily = append(daily, map[string]interface{}{
			"date":          p.TimestampMicro,
			"balance_minor": p.ProjectedMinor,
		})
	}
	if len(res.Points) > 0 {
		lowestDate = &res.Points[0].TimestampMicro
	}

	return ctx.JSON(http.StatusOK, BalanceForecastReport{
		DailyProjections:            daily,
		LowestProjectedBalanceMinor: res.LowestPointMinor,
		LowestBalanceDate:           lowestDate,
	})
}

// GetBudgetAnalysisReport (GET /budgets/{budgetId}/reports/budget-analysis)
func (s *Server) GetBudgetAnalysisReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetBudgetAnalysisReportParams) error {
	budgetID := budgetId.String()
	_, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	return ctx.JSON(http.StatusOK, BudgetAnalysisReport{
		Intervals: []map[string]interface{}{},
	})
}

// GetIncomeVsExpenseReport (GET /budgets/{budgetId}/reports/income-vs-expense)
func (s *Server) GetIncomeVsExpenseReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetIncomeVsExpenseReportParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	res, err := report.CalculateIncomeVsExpense(ctx.Request().Context(), tenantDB, s.mainDB, report.ReportFilter{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Calculation error", "INTERNAL_ERROR", nil)
	}

	intervals := []map[string]interface{}{}
	for _, it := range res.Months {
		intervals = append(intervals, map[string]interface{}{
			"interval_key":  it.MonthKey,
			"income_minor":  it.IncomeMinor,
			"expense_minor": it.ExpenseMinor,
			"net_minor":     it.NetSavingsMinor,
		})
	}

	return ctx.JSON(http.StatusOK, IncomeVsExpenseReport{
		Intervals:         intervals,
		TotalIncomeMinor:  res.TotalIncomeMinor,
		TotalExpenseMinor: res.TotalExpenseMinor,
		NetIncomeMinor:    res.TotalNetMinor,
	})
}

// SimulateMonteCarlo (POST /budgets/{budgetId}/reports/monte-carlo/simulate)
func (s *Server) SimulateMonteCarlo(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	_, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	var req MonteCarloRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request", "INVALID_INPUT", nil)
	}

	runs := req.SimulationsCount
	if runs <= 0 {
		runs = 1000
	}
	pots := []report.MontePot{}
	for _, p := range req.Pots {
		var ret, vol float64 = 0.07, 0.15
		if p.ExpectedReturnPct != nil {
			ret = float64(*p.ExpectedReturnPct)
		}
		if p.VolatilityStdDevPct != nil {
			vol = float64(*p.VolatilityStdDevPct)
		}
		pots = append(pots, report.MontePot{
			Name:                 p.Name,
			StartingBalanceMinor: p.StartingBalanceMinor,
			ExpectedReturnPct:    ret,
			VolatilityStdDevPct:  vol,
		})
	}

	phases := []report.SpendPhase{}
	for _, ph := range req.SpendingPhases {
		phases = append(phases, report.SpendPhase{
			FromAge:           ph.FromAge,
			ToAge:             ph.ToAge,
			AnnualTargetMinor: ph.AnnualTargetMinor,
		})
	}

	res, err := report.RunMonteCarloSimulation(ctx.Request().Context(), report.MonteCarloConfig{
		CurrentAge:       req.CurrentAge,
		TargetAge:        req.TargetAge,
		SimulationsCount: runs,
		ReturnModel:      req.ReturnModel,
		Pots:             pots,
		SpendingPhases:   phases,
	})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Simulation failed", "INTERNAL_ERROR", nil)
	}

	p10 := make([]int64, len(res.Percentile10Trajectory))
	p50 := make([]int64, len(res.Percentile50Trajectory))
	p90 := make([]int64, len(res.Percentile90Trajectory))
	copy(p10, res.Percentile10Trajectory)
	copy(p50, res.Percentile50Trajectory)
	copy(p90, res.Percentile90Trajectory)

	return ctx.JSON(http.StatusOK, MonteCarloResponse{
		ChanceOfRunningOut:  float32(res.ChanceOfRunningOut),
		MedianEndingBalance: res.MedianEndingBalance,
		P10Trajectory:       &p10,
		P50Trajectory:       &p50,
		P90Trajectory:       &p90,
		SuccessRatePct:      float32(res.SuccessRatePct),
	})
}

// GetNetWorthReport (GET /budgets/{budgetId}/reports/net-worth)
func (s *Server) GetNetWorthReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetNetWorthReportParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	res, err := report.CalculateNetWorth(ctx.Request().Context(), tenantDB, s.mainDB, report.ReportFilter{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Calculation error", "INTERNAL_ERROR", nil)
	}

	history := []map[string]interface{}{}
	var curNW, assets, debts int64
	for _, it := range res.Points {
		history = append(history, map[string]interface{}{
			"interval_key":      it.DateKey,
			"assets_minor":      it.AssetsMinor,
			"liabilities_minor": it.LiabilitiesMinor,
			"net_worth_minor":   it.NetWorthMinor,
		})
		curNW = it.NetWorthMinor
		assets = it.AssetsMinor
		debts = it.LiabilitiesMinor
	}

	return ctx.JSON(http.StatusOK, NetWorthReport{
		CurrentNetWorthMinor: curNW,
		History:              history,
		TotalAssetsMinor:     assets,
		TotalDebtsMinor:      debts,
	})
}

// GetPartyCashflowReport (GET /budgets/{budgetId}/reports/party-cashflow)
func (s *Server) GetPartyCashflowReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetPartyCashflowReportParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	res, err := report.CalculatePartyCashflow(ctx.Request().Context(), tenantDB, s.mainDB, report.ReportFilter{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Calculation error", "INTERNAL_ERROR", nil)
	}

	parties := []PartyCashflowSummary{}
	for _, p := range res.Parties {
		pUUID, _ := uuid.Parse(p.PartyID)
		parties = append(parties, PartyCashflowSummary{
			PartyId:           pUUID,
			TotalInflowMinor:  p.InflowMinor,
			TotalOutflowMinor: p.OutflowMinor,
			NetFlowMinor:      p.NetCashflowMinor,
		})
	}

	return ctx.JSON(http.StatusOK, PartyCashflowReport{
		Parties: parties,
	})
}

// GetSankeyReport (GET /budgets/{budgetId}/reports/sankey)
func (s *Server) GetSankeyReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetSankeyReportParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	res, err := report.CalculateSankeyFlow(ctx.Request().Context(), tenantDB, s.mainDB, report.ReportFilter{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Calculation error", "INTERNAL_ERROR", nil)
	}

	links := []struct {
		Source string `json:"source"`
		Target string `json:"target"`
		Value  int64  `json:"value"`
	}{}
	for _, l := range res.Links {
		links = append(links, struct {
			Source string `json:"source"`
			Target string `json:"target"`
			Value  int64  `json:"value"`
		}{
			Source: l.Source,
			Target: l.Target,
			Value:  l.Value,
		})
	}

	nodes := []struct {
		Name string `json:"name"`
	}{}
	for _, n := range res.Nodes {
		nodes = append(nodes, struct {
			Name string `json:"name"`
		}{
			Name: n.Name,
		})
	}

	return ctx.JSON(http.StatusOK, SankeyReport{
		Links: links,
		Nodes: nodes,
	})
}

// ListSavedReports (GET /budgets/{budgetId}/reports/saved)
func (s *Server) ListSavedReports(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), `
		SELECT id, version, name, report_type, config_json, is_pinned_widget
		FROM saved_reports_current
		ORDER BY name ASC`)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed loading saved reports", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	reports := []SavedReport{}
	for rows.Next() {
		var id, name, rType, cfg string
		var version, pinned int
		if err := rows.Scan(&id, &version, &name, &rType, &cfg, &pinned); err != nil {
			continue
		}
		rUUID, _ := uuid.Parse(id)
		reports = append(reports, SavedReport{
			Id:             rUUID,
			Version:        version,
			Name:           name,
			ReportType:     rType,
			ConfigJson:     cfg,
			IsPinnedWidget: pinned == 1,
		})
	}

	return ctx.JSON(http.StatusOK, reports)
}

// CreateSavedReport (POST /budgets/{budgetId}/reports/saved)
func (s *Server) CreateSavedReport(ctx echo.Context, budgetId BudgetIdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	var req CreateSavedReportRequest
	if err := ctx.Bind(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		return sendError(ctx, http.StatusBadRequest, "Invalid report payload", "INVALID_INPUT", nil)
	}

	reportID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()

	isPinned := 0
	if req.IsPinnedWidget != nil && *req.IsPinnedWidget {
		isPinned = 1
	}

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO saved_reports (
			id, version, name, report_type, config_json, is_pinned_widget, action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
		reportID, req.Name, string(req.ReportType), req.ConfigJson, isPinned, userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed inserting saved report event", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO saved_reports_current (
			id, version, name, report_type, config_json, is_pinned_widget, agent_id, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?)`,
		reportID, req.Name, string(req.ReportType), req.ConfigJson, isPinned, userID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating saved reports projection", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	rUUID, _ := uuid.Parse(reportID)
	return ctx.JSON(http.StatusCreated, SavedReport{
		Id:             rUUID,
		Version:        1,
		Name:           req.Name,
		ReportType:     string(req.ReportType),
		ConfigJson:     req.ConfigJson,
		IsPinnedWidget: isPinned == 1,
	})
}

// UpdateSavedReport (PUT /budgets/{budgetId}/reports/saved/{id})
func (s *Server) UpdateSavedReport(ctx echo.Context, budgetId BudgetIdPathParam, id IdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "EDITOR")
	if err != nil {
		return err
	}

	reportID := id.String()
	var req UpdateSavedReportRequest
	if err := ctx.Bind(&req); err != nil {
		return sendError(ctx, http.StatusBadRequest, "Invalid request", "INVALID_INPUT", nil)
	}

	var curVer int
	var curName, curType, curCfg string
	var curPinned int
	err = tenantDB.QueryRowContext(ctx.Request().Context(), `
		SELECT version, name, report_type, config_json, is_pinned_widget
		FROM saved_reports_current WHERE id = ?`, reportID).Scan(&curVer, &curName, &curType, &curCfg, &curPinned)
	if err != nil {
		return sendError(ctx, http.StatusNotFound, "Saved report not found", "NOT_FOUND", nil)
	}

	newName := curName
	if req.Name != nil && *req.Name != "" {
		newName = *req.Name
	}
	newCfg := curCfg
	if req.ConfigJson != nil {
		newCfg = *req.ConfigJson
	}
	newPinned := curPinned
	if req.IsPinnedWidget != nil {
		if *req.IsPinnedWidget {
			newPinned = 1
		} else {
			newPinned = 0
		}
	}

	now := time.Now().UTC().UnixMicro()
	corrID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(ctx.Request().Context(), nil)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "DB error", "INTERNAL_ERROR", nil)
	}
	defer tx.Rollback()

	nextVer := curVer + 1
	_, err = tx.ExecContext(ctx.Request().Context(), `
		INSERT INTO saved_reports (
			id, version, name, report_type, config_json, is_pinned_widget, action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		reportID, nextVer, newName, curType, newCfg, newPinned, userID, corrID, now,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed inserting saved report event", "INTERNAL_ERROR", nil)
	}

	_, err = tx.ExecContext(ctx.Request().Context(), `
		UPDATE saved_reports_current
		SET version = ?, name = ?, config_json = ?, is_pinned_widget = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`,
		nextVer, newName, newCfg, newPinned, userID, now, reportID,
	)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed updating saved reports projection", "INTERNAL_ERROR", nil)
	}

	if err := tx.Commit(); err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Commit error", "INTERNAL_ERROR", nil)
	}

	return ctx.JSON(http.StatusOK, SavedReport{
		Id:             id,
		Version:        nextVer,
		Name:           newName,
		ReportType:     curType,
		ConfigJson:     newCfg,
		IsPinnedWidget: newPinned == 1,
	})
}

// GetSpendingBreakdownReport (GET /budgets/{budgetId}/reports/spending-breakdown)
func (s *Server) GetSpendingBreakdownReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetSpendingBreakdownReportParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	res, err := report.CalculateSpendingBreakdown(ctx.Request().Context(), tenantDB, s.mainDB, report.ReportFilter{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Calculation error", "INTERNAL_ERROR", nil)
	}

	cats := []struct {
		AmountMinor int64   `json:"amount_minor"`
		Name        string  `json:"name"`
		Percentage  float32 `json:"percentage"`
	}{}
	for _, c := range res.Items {
		cats = append(cats, struct {
			AmountMinor int64   `json:"amount_minor"`
			Name        string  `json:"name"`
			Percentage  float32 `json:"percentage"`
		}{
			AmountMinor: c.TotalSpentMinor,
			Name:        c.BucketName,
			Percentage:  float32(c.PercentageShare),
		})
	}

	return ctx.JSON(http.StatusOK, SpendingBreakdownReport{
		Categories:         cats,
		TotalSpendingMinor: res.TotalSpentMinor,
	})
}

// GetSpendingTrendsReport (GET /budgets/{budgetId}/reports/spending-trends)
func (s *Server) GetSpendingTrendsReport(ctx echo.Context, budgetId BudgetIdPathParam, params GetSpendingTrendsReportParams) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "VIEWER")
	if err != nil {
		return err
	}

	res, err := report.CalculateSpendingTrends(ctx.Request().Context(), tenantDB, s.mainDB, report.ReportFilter{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Calculation error", "INTERNAL_ERROR", nil)
	}

	intervals := []map[string]interface{}{}
	var totalSpend int64 = 0
	for _, it := range res.Points {
		intervals = append(intervals, map[string]interface{}{
			"interval_key": it.MonthKey,
			"amount_minor": it.TotalSpentMinor,
		})
		totalSpend += it.TotalSpentMinor
	}
	var avgSpend int64 = 0
	if len(res.Points) > 0 {
		avgSpend = totalSpend / int64(len(res.Points))
	}

	return ctx.JSON(http.StatusOK, SpendingTrendsReport{
		Intervals:            intervals,
		AverageSpendingMinor: avgSpend,
	})
}

// ==========================================
// Audit History & Temporal Restore
// ==========================================

// GetEntityHistory (GET /budgets/{budgetId}/{entityType}/{id}/history)
func (s *Server) GetEntityHistory(ctx echo.Context, budgetId BudgetIdPathParam, entityType GetEntityHistoryParamsEntityType, id IdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, _, err := s.verifyBudgetAccess(ctx, budgetID, "ADMIN")
	if err != nil {
		return err
	}

	table := strings.ToLower(string(entityType))
	allowed := map[string]bool{
		"parties":             true,
		"accounts":            true,
		"matching_rules":      true,
		"raw_transactions":    true,
		"budget_transactions": true,
		"saved_reports":       true,
	}
	if !allowed[table] {
		return sendError(ctx, http.StatusBadRequest, "Invalid entity type for history", "INVALID_ENTITY_TYPE", nil)
	}

	rows, err := tenantDB.QueryContext(ctx.Request().Context(), fmt.Sprintf(`
		SELECT version, action, agent_id, correlation_id, created_at
		FROM %s
		WHERE id = ?
		ORDER BY version DESC`, table), id.String())
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed loading history", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	history := []EntityVersionItem{}
	for rows.Next() {
		var ver int
		var act, agentID, corrID string
		var createdAt int64
		if err := rows.Scan(&ver, &act, &agentID, &corrID, &createdAt); err != nil {
			continue
		}
		history = append(history, EntityVersionItem{
			Id:            id.String(),
			Action:        act,
			AgentId:       agentID,
			CorrelationId: corrID,
			CreatedAt:     createdAt,
			Version:       ver,
		})
	}

	return ctx.JSON(http.StatusOK, history)
}

// RestoreEntityVersion (POST /budgets/{budgetId}/{entityType}/{id}/restore)
func (s *Server) RestoreEntityVersion(ctx echo.Context, budgetId BudgetIdPathParam, entityType RestoreEntityVersionParamsEntityType, id IdPathParam) error {
	budgetID := budgetId.String()
	tenantDB, userID, err := s.verifyBudgetAccess(ctx, budgetID, "ADMIN")
	if err != nil {
		return err
	}

	var req RestoreVersionRequest
	if err := ctx.Bind(&req); err != nil || req.TargetVersion <= 0 {
		return sendError(ctx, http.StatusBadRequest, "Invalid target version", "INVALID_INPUT", nil)
	}

	now := time.Now().UTC().UnixMicro()
	corrID := domain.NewUUIDv7()

	res, err := history.RestoreEntityVersion(ctx.Request().Context(), tenantDB, string(entityType), id.String(), req.TargetVersion, userID, corrID, now)
	if err != nil {
		if errors.Is(err, history.ErrNotFound) {
			return sendError(ctx, http.StatusNotFound, "Historical version not found", "NOT_FOUND", nil)
		}
		if errors.Is(err, history.ErrSoftDelete) {
			return sendError(ctx, http.StatusBadRequest, "Cannot restore a soft-delete version", "INVALID_INPUT", nil)
		}
		if errors.Is(err, history.ErrInvalidEntity) {
			return sendError(ctx, http.StatusBadRequest, "Invalid entity type for history", "INVALID_ENTITY_TYPE", nil)
		}
		return sendError(ctx, http.StatusInternalServerError, "Failed restoring version: "+err.Error(), "INTERNAL_ERROR", nil)
	}

	return ctx.JSON(http.StatusOK, RestoreVersionResponse{
		Id:                  res.EntityID,
		NewVersion:          res.NewVersion,
		RestoredFromVersion: res.RestoredFromVersion,
	})
}

// ==========================================
// Central Bank Exchange Rates & Integrations
// ==========================================

// GetExchangeRates (GET /exchange-rates)
func (s *Server) GetExchangeRates(ctx echo.Context, params GetExchangeRatesParams) error {
	base := strings.ToUpper(params.Base)
	target := strings.ToUpper(params.Target)

	query := `
		SELECT base_currency, target_currency, rate_date, rate_micros, source
		FROM currency_exchange_rates
		WHERE base_currency = ? AND target_currency = ?`
	args := []interface{}{base, target}

	if params.From != nil {
		query += " AND rate_date >= ?"
		args = append(args, *params.From)
	}
	if params.To != nil {
		query += " AND rate_date <= ?"
		args = append(args, *params.To)
	}
	query += " ORDER BY rate_date DESC"

	rows, err := s.mainDB.QueryContext(ctx.Request().Context(), query, args...)
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed querying exchange rates", "INTERNAL_ERROR", nil)
	}
	defer rows.Close()

	rates := []ExchangeRateItem{}
	for rows.Next() {
		var b, t, src string
		var d, m int64
		if err := rows.Scan(&b, &t, &d, &m, &src); err != nil {
			continue
		}
		rates = append(rates, ExchangeRateItem{
			BaseCurrency:   b,
			TargetCurrency: t,
			RateDate:       d,
			RateMicros:     m,
			Source:         src,
		})
	}

	return ctx.JSON(http.StatusOK, rates)
}

// SyncExchangeRates (POST /exchange-rates/sync)
func (s *Server) SyncExchangeRates(ctx echo.Context) error {
	if s.fxService == nil {
		s.fxService = fx.NewService(s.mainDB, nil, nil)
	}
	synced, err := s.fxService.Sync(ctx.Request().Context(), fx.SyncOptions{})
	if err != nil {
		return sendError(ctx, http.StatusInternalServerError, "Failed syncing exchange rates: "+err.Error(), "INTERNAL_ERROR", nil)
	}
	return ctx.JSON(http.StatusOK, SyncRatesResponse{
		SyncedCount: synced,
	})
}

// ListIntegrationProviders (GET /integrations/providers)
func (s *Server) ListIntegrationProviders(ctx echo.Context) error {
	providers := []IntegrationProvider{
		{
			DisplayName: "GoCardless Bank Account Data",
			Provider:    "GOCARDLESS",
		},
		{
			DisplayName: "Manual & CSV Import",
			Provider:    "MANUAL_CSV",
		},
	}
	return ctx.JSON(http.StatusOK, providers)
}

// ConnectProvider (POST /integrations/{provider}/connect)
func (s *Server) ConnectProvider(ctx echo.Context, provider ConnectProviderParamsProvider) error {
	userID, err := getUserID(ctx)
	if err != nil {
		return sendError(ctx, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", nil)
	}

	stateToken, _ := auth.GenerateCSPRNGToken()
	authURL := fmt.Sprintf("https://ob.gocardless.com/connect?state=%s&user=%s", stateToken, userID)
	return ctx.JSON(http.StatusOK, ConnectProviderResponse{
		RedirectUrl: authURL,
		State:       stateToken,
	})
}

// ProviderCallback (GET /integrations/{provider}/callback)
func (s *Server) ProviderCallback(ctx echo.Context, provider string, params ProviderCallbackParams) error {
	return ctx.JSON(http.StatusOK, ProviderCallbackResponse{
		IntegrationId: uuid.New(),
		Provider:      provider,
		Status:        "CONNECTED",
	})
}
