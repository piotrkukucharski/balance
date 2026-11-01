package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/gocardless"
	"github.com/balance/balance/internal/integration/mock"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/service"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

type AccountsHandler struct {
	mainDB           *sql.DB
	tenantMgr        *db.TenantDBManager
	mockAdapter      *mock.MockAdapter
	gocardlessClient *gocardless.Client
	appService       *service.AppService
}

func NewAccountsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *AccountsHandler {
	secretID := os.Getenv("GOCARDLESS_SECRET_ID")
	secretKey := os.Getenv("GOCARDLESS_SECRET_KEY")
	redirectURL := os.Getenv("GOCARDLESS_REDIRECT_URL")
	var gcClient *gocardless.Client
	if secretID != "" && secretKey != "" {
		gcClient = gocardless.NewClient(secretID, secretKey, redirectURL)
	}

	return &AccountsHandler{
		mainDB:           mainDB,
		tenantMgr:        tenantMgr,
		mockAdapter:      mock.NewMockAdapter(),
		gocardlessClient: gcClient,
	}
}

func (h *AccountsHandler) SetService(svc *service.AppService) {
	h.appService = svc
}

// isDevMode checks whether the application is running in development mode.
// Any environment other than "production" or "prod" is treated as development.
func isDevMode() bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	return env != "production" && env != "prod"
}

type accountRowView struct {
	ID                string
	Name              string
	NameJS            string
	IBAN              string
	AccountType       string
	TypeBadgeClass    string
	Currency          string
	BalanceFormatted  string
	FaviconType       string
	FaviconValue      string
	EmojiChar         string
	IsBankLinked      bool
	IsConsentExpired  bool
	IsClosed          bool
	ReconnectBtnClass string
	ReconnectBtnText  string
	ActionMenuHTML    template.HTML
}

type accountsViewData struct {
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	IsDevMode               bool
	Accounts                []accountRowView
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
}

// AccountsView lists all user-owned financial accounts with multi-currency balances and CRUD controls.
func (h *AccountsHandler) AccountsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.id, a.name, a.account_type, a.primary_currency, COALESCE(a.iban, ''),
		       a.is_closed, COALESCE(acb.balance_minor, 0),
		       COALESCE(ail.external_account_id, ''),
		       COALESCE(ail.integration_id, ''),
		       COALESCE(a.favicon_type, 'EMOJI'),
		       COALESCE(a.favicon_value, '')
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN account_currency_balances acb ON a.id = acb.account_id AND a.primary_currency = acb.currency
		LEFT JOIN account_integration_links ail ON a.id = ail.account_id
		WHERE p.is_user_owned = 1
		ORDER BY a.is_closed ASC, a.name ASC`)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed querying accounts_current", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading accounts")
	}
	defer rows.Close()

	now := time.Now().UTC().UnixMicro()
	csrfToken, _ := c.Get("csrfToken").(string)

	var accountViews []accountRowView
	for rows.Next() {
		var id, name, aType, curr, iban, extID, intID, favType, favVal string
		var isClosed int
		var balanceMinor int64
		rows.Scan(&id, &name, &aType, &curr, &iban, &isClosed, &balanceMinor, &extID, &intID, &favType, &favVal)

		// Check if bank integration is expired in main.db
		isConsentExpired := false
		if intID != "" {
			var intStatus string
			var expiresAt sql.NullInt64
			err := h.mainDB.QueryRowContext(c.Request().Context(), `
				SELECT status, expires_at FROM user_integrations WHERE id = ?`, intID).Scan(&intStatus, &expiresAt)
			if err == nil {
				if intStatus == "EXPIRED" || (expiresAt.Valid && expiresAt.Int64 > 0 && expiresAt.Int64 < now) {
					isConsentExpired = true
				}
			}
		}

		typeBadge := "badge-soft badge-primary"
		switch aType {
		case "SAVINGS":
			typeBadge = "badge-soft badge-secondary"
		case "CREDIT_CARD":
			typeBadge = "badge-soft badge-warning"
		case "CASH":
			typeBadge = "badge-soft badge-accent"
		case "INVESTMENT":
			typeBadge = "badge-soft badge-info"
		}

		reconnectBtnClass := "btn-ghost text-primary"
		reconnectBtnText := "Reconnect"
		if isConsentExpired {
			reconnectBtnClass = "btn-warning animate-bounce font-bold shadow-xs"
			reconnectBtnText = "⚠️ Reconnect"
		}

		emojiChar := favVal
		if emojiChar == "" {
			emojiChar = "💳"
		}

		// Check if account can be safely deleted:
		// Cannot be deleted if it has raw transactions or budget transactions (other than initial 'Set Balance Offset' / starting balance).
		canDeleteAccount := true
		var rawCount int
		_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT count(*) FROM raw_transactions_current WHERE account_id = ?`, id).Scan(&rawCount)
		if rawCount > 0 {
			canDeleteAccount = false
		} else {
			var regularTxCount int
			_ = tenantDB.QueryRowContext(c.Request().Context(), `
				SELECT count(*) FROM budget_transactions_current
				WHERE (source_account_id = ? OR destination_account_id = ?)
				  AND (notes IS NULL OR (notes NOT LIKE '%Set Balance Offset%' AND notes NOT LIKE '%Starting Balance%'))`, id, id).Scan(&regularTxCount)
			if regularTxCount > 0 {
				canDeleteAccount = false
			}
		}

		actions := []TableActionItem{
			{
				Label: "🕒 History",
				HXGet: fmt.Sprintf("/budgets/%s/history/accounts/%s", budgetID, id),
			},
			{
				Label:   "✏️ Edit",
				OnClick: fmt.Sprintf("openEditModal('%s', '%s', '%s', '%s', '%s', '%s')", id, strings.ReplaceAll(name, "'", "\\'"), curr, aType, favType, strings.ReplaceAll(favVal, "'", "\\'")),
			},
			{
				Label:      toggleActionText(isClosed),
				FormAction: fmt.Sprintf("/budgets/%s/accounts/%s/toggle-close", budgetID, id),
				CSRFToken:  csrfToken,
			},
		}

		if canDeleteAccount {
			actions = append(actions, TableActionItem{
				Label:      "🗑️ Delete",
				FormAction: fmt.Sprintf("/budgets/%s/accounts/%s/delete", budgetID, id),
				CSRFToken:  csrfToken,
				IsDanger:   true,
				ConfirmMsg: "Delete this account?",
			})
		}

		accountViews = append(accountViews, accountRowView{
			ID:                id,
			Name:              name,
			NameJS:            strings.ReplaceAll(name, "'", "\\'"),
			IBAN:              iban,
			AccountType:       aType,
			TypeBadgeClass:    typeBadge,
			Currency:          curr,
			BalanceFormatted:  fmt.Sprintf("%.2f", float64(balanceMinor)/100.0),
			FaviconType:       favType,
			FaviconValue:      favVal,
			EmojiChar:         emojiChar,
			IsBankLinked:      extID != "",
			IsConsentExpired:  isConsentExpired,
			IsClosed:          isClosed == 1,
			ReconnectBtnClass: reconnectBtnClass,
			ReconnectBtnText:  reconnectBtnText,
			ActionMenuHTML:    template.HTML(RenderPopoverActionMenu("account", id, actions)),
		})
	}

	out, err := templates.RenderString("accounts/view", accountsViewData{
		BudgetID:                budgetID,
		BudgetName:              "Accounts",
		CSRFToken:               csrfToken,
		IsDevMode:               isDevMode(),
		Accounts:                accountViews,
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "accounts")),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "accounts")),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	})
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, out)
}

func toggleActionText(isClosed int) string {
	if isClosed == 1 {
		return "🔓 Reopen Account"
	}
	return "🔒 Close Account"
}

// CreateAccount creates a new manual financial account.
func (h *AccountsHandler) CreateAccount(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:manual")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	name := c.FormValue("name")
	aType := c.FormValue("account_type")
	curr := c.FormValue("primary_currency")
	iban := c.FormValue("iban")
	initBalStr := c.FormValue("initial_balance")
	favType := c.FormValue("favicon_type")
	favVal := c.FormValue("favicon_value")

	if favType != "URL" {
		favType = "EMOJI"
	}
	if favVal == "" {
		if favType == "EMOJI" {
			favVal = "💵"
		}
	}

	logger.Info(ctx, "Initiating manual financial account creation",
		"budget_id", budgetID,
		"user_id", userID,
		"name", name,
		"account_type", aType,
		"currency", curr,
		"iban", iban,
		"favicon_type", favType,
		"favicon_value", favVal,
		"initial_balance_raw", initBalStr,
	)

	if name == "" || aType == "" || curr == "" {
		logger.Warn(ctx, "Manual account creation rejected: validation failed",
			"budget_id", budgetID,
			"name", name,
			"account_type", aType,
			"currency", curr,
		)
		return echo.NewHTTPError(http.StatusBadRequest, "Name, type, and currency are required")
	}

	if h.appService != nil {
		var initMinor int64
		if initBalStr != "" {
			var initFloat float64
			fmt.Sscanf(initBalStr, "%f", &initFloat)
			initMinor = int64(initFloat * 100)
		}
		acctID, err := h.appService.CreateFinancialAccount(c.Request().Context(), service.CreateAccountInput{
			BudgetID:            budgetID,
			UserID:              userID,
			CorrelationID:       corrID,
			Name:                name,
			AccountType:         aType,
			PrimaryCurrency:     curr,
			IBAN:                iban,
			FaviconType:         favType,
			FaviconValue:        favVal,
			InitialBalanceMinor: initMinor,
		})
		if err != nil {
			logger.Error(ctx, "Failed creating account via service", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create account")
		}
		logger.Info(ctx, "Manual financial account created successfully",
			"budget_id", budgetID,
			"account_id", acctID,
			"name", name,
			"account_type", aType,
			"currency", curr,
			"initial_balance_minor", initMinor,
		)
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Manual account creation DB begin transaction failed", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// Append account log
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 0, 'CREATE', ?, ?, ?)`,
		acctID, userPartyID, name, aType, curr, iban, favType, favVal, userID, corrID, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed writing to accounts ledger events table", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to insert account")
	}

	// Upsert accounts_current
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		acctID, userPartyID, name, aType, curr, iban, favType, favVal, userID, now,
	)
	if err != nil {
		logger.Error(ctx, "Failed projecting to accounts_current", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update accounts_current")
	}

	// Handle initial balance if provided
	var initialBalanceMinor int64
	if initBalStr != "" {
		var initFloat float64
		fmt.Sscanf(initBalStr, "%f", &initFloat)
		initialBalanceMinor = int64(initFloat * 100)
		if initialBalanceMinor != 0 {
			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
				VALUES (?, ?, ?, ?)`, acctID, curr, initialBalanceMinor, now)
			if err != nil {
				logger.Error(ctx, "Failed inserting initial account balance", "account_id", acctID, "currency", curr, "balance_minor", initialBalanceMinor, "error", err.Error())
			}
		}
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Manual account creation transaction commit failed", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	logger.Info(ctx, "Manual financial account created successfully",
		"budget_id", budgetID,
		"account_id", acctID,
		"party_id", userPartyID,
		"name", name,
		"account_type", aType,
		"currency", curr,
		"initial_balance_minor", initialBalanceMinor,
	)

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// LinkBankAccount connects a bank feed (mock or GoCardless) to a new account.
func (h *AccountsHandler) LinkBankAccount(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:bank_link")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	extAcctID := c.FormValue("external_account_id")
	name := c.FormValue("name")

	logger.Info(ctx, "Initiating bank account link flow",
		"budget_id", budgetID,
		"user_id", userID,
		"external_account_id", extAcctID,
		"account_name", name,
	)

	if extAcctID == "" || name == "" {
		logger.Warn(ctx, "Bank link failed: external account selection and name required",
			"budget_id", budgetID,
			"external_account_id", extAcctID,
			"name", name,
		)
		return echo.NewHTTPError(http.StatusBadRequest, "External account selection and name required")
	}

	if !isDevMode() {
		return echo.NewHTTPError(http.StatusForbidden, "Mock bank link is disabled in production environment")
	}

	// Fetch details from mock adapter
	mockAccounts, _ := h.mockAdapter.FetchAccounts(c.Request().Context(), userID)
	var matched *mock.ExtAccount
	for _, ma := range mockAccounts {
		if ma.ID == extAcctID {
			matched = &ma
			break
		}
	}
	if matched == nil {
		logger.Warn(ctx, "Discovered bank account not found in provider",
			"budget_id", budgetID,
			"external_account_id", extAcctID,
		)
		return echo.NewHTTPError(http.StatusNotFound, "Discovered bank account not found")
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
	acctID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		logger.Error(ctx, "Bank link DB begin transaction failed", "budget_id", budgetID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// 1. Create account
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'CHECKING', ?, ?, 0, ?, ?)`,
		acctID, userPartyID, name, matched.Currency, matched.IBAN, userID, now,
	)
	if err != nil {
		logger.Error(ctx, "Bank link failed creating account projection", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating account")
	}

	// 2. Link integration
	linkID := domain.NewUUIDv7()
	intID := "mock_integration_" + userID
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		linkID, acctID, intID, matched.ID, now, now,
	)
	if err != nil {
		logger.Error(ctx, "Bank link failed creating integration link", "account_id", acctID, "link_id", linkID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating integration link")
	}

	// 3. Set balance
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, ?, ?, ?)`,
		acctID, matched.Currency, matched.BalanceMinor, now,
	)
	if err != nil {
		logger.Error(ctx, "Bank link failed storing initial balance", "account_id", acctID, "balance_minor", matched.BalanceMinor, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed setting balance")
	}

	if err := tx.Commit(); err != nil {
		logger.Error(ctx, "Bank link transaction commit failed", "account_id", acctID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	logger.Info(ctx, "Bank account linked and initial balance synchronized successfully",
		"budget_id", budgetID,
		"account_id", acctID,
		"external_account_id", matched.ID,
		"institution_name", matched.Name,
		"currency", matched.Currency,
		"balance_minor", matched.BalanceMinor,
		"integration_id", intID,
	)

	_ = corrID
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// UpdateAccount updates account name and favicon.
func (h *AccountsHandler) UpdateAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationID").(string)
	if corrID == "" {
		corrID = domain.NewUUIDv7()
	}
	name := c.FormValue("name")
	favType := c.FormValue("favicon_type")
	favVal := c.FormValue("favicon_value")

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Name required")
	}

	if favType != "URL" {
		favType = "EMOJI"
	}
	if favVal == "" && favType == "EMOJI" {
		favVal = "💳"
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	var curVer int
	var partyID, aType, curr string
	var iban, oldFavType, oldFavVal sql.NullString
	var isClosed int
	err = tx.QueryRowContext(c.Request().Context(), `
		SELECT version, party_id, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed
		FROM accounts_current WHERE id = ?`, acctID).Scan(&curVer, &partyID, &aType, &curr, &iban, &oldFavType, &oldFavVal, &isClosed)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Account not found")
	}

	newVer := curVer + 1

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		acctID, newVer, partyID, name, aType, curr, iban, favType, favVal, isClosed, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed appending account version")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE accounts_current SET version = ?, name = ?, favicon_type = ?, favicon_value = ?, updated_at = ?, agent_id = ? WHERE id = ?`,
		newVer, name, favType, favVal, now, userID, acctID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Update error")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// ToggleCloseAccount toggles between active and closed status.
func (h *AccountsHandler) ToggleCloseAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID, _ := c.Get("correlationID").(string)
	if corrID == "" {
		corrID = domain.NewUUIDv7()
	}

	now := time.Now().UTC().UnixMicro()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	var curVer int
	var partyID, name, aType, curr string
	var iban, favType, favVal sql.NullString
	var isClosed int
	err = tx.QueryRowContext(c.Request().Context(), `
		SELECT version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed
		FROM accounts_current WHERE id = ?`, acctID).Scan(&curVer, &partyID, &name, &aType, &curr, &iban, &favType, &favVal, &isClosed)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Account not found")
	}

	newVer := curVer + 1
	newClosed := 1 - isClosed

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		acctID, newVer, partyID, name, aType, curr, iban, favType.String, favVal.String, newClosed, userID, corrID, now,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed appending account version")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE accounts_current SET version = ?, is_closed = ?, updated_at = ?, agent_id = ? WHERE id = ?`,
		newVer, newClosed, now, userID, acctID,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Status toggle error")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// DeleteAccount soft-deletes the account from current projection.
// Account can only be deleted if it has NO raw transactions and NO budget transactions
// (other than initial 'Set Balance Offset' / starting balance).
// If it has starting transactions, they and account_currency_balances are cleaned up.
func (h *AccountsHandler) DeleteAccount(c echo.Context) error {
	budgetID := c.Param("budgetId")
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	// 1. Check if account exists
	var acctExists int
	err := tenantDB.QueryRowContext(c.Request().Context(), `SELECT count(*) FROM accounts_current WHERE id = ?`, acctID).Scan(&acctExists)
	if err != nil || acctExists == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "Account not found")
	}

	// 2. Reject if any raw transactions exist
	var rawCount int
	err = tenantDB.QueryRowContext(c.Request().Context(), `SELECT count(*) FROM raw_transactions_current WHERE account_id = ?`, acctID).Scan(&rawCount)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error checking raw transactions")
	}
	if rawCount > 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "Cannot delete account with existing transactions; close the account instead")
	}

	// 3. Reject if any non-starting budget transactions exist
	var nonStartingCount int
	err = tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT count(*) FROM budget_transactions_current
		WHERE (source_account_id = ? OR destination_account_id = ?)
		  AND (notes IS NULL OR (notes NOT LIKE '%Set Balance Offset%' AND notes NOT LIKE '%Starting Balance%'))`, acctID, acctID).Scan(&nonStartingCount)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error checking budget transactions")
	}
	if nonStartingCount > 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "Cannot delete account with existing transactions; close the account instead")
	}

	// Find any starting/Set Balance transactions for this account to clean them up
	startRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id FROM budget_transactions_current
		WHERE (source_account_id = ? OR destination_account_id = ?)
		  AND (notes LIKE '%Set Balance Offset%' OR notes LIKE '%Starting Balance%')`, acctID, acctID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error checking starting transactions")
	}
	var startingTxIDs []string
	for startRows.Next() {
		var sID string
		startRows.Scan(&sID)
		startingTxIDs = append(startingTxIDs, sID)
	}
	startRows.Close()

	now := time.Now().UTC().UnixMicro()
	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "DB error")
	}
	defer tx.Rollback()

	// Append soft delete to historical table
	var version int
	var partyID, name, aType, curr string
	var iban, favType, favVal sql.NullString
	err = tx.QueryRowContext(c.Request().Context(), `
		SELECT version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value
		FROM accounts_current WHERE id = ?`, acctID).Scan(&version, &partyID, &name, &aType, &curr, &iban, &favType, &favVal)
	if err == nil {
		tx.ExecContext(c.Request().Context(), `
			INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'SOFT_DELETE', ?, ?, ?)`,
			acctID, version+1, partyID, name, aType, curr, iban, favType.String, favVal.String, userID, corrID, now)
	}

	// Remove from accounts_current
	tx.ExecContext(c.Request().Context(), `DELETE FROM accounts_current WHERE id = ?`, acctID)
	tx.ExecContext(c.Request().Context(), `DELETE FROM account_integration_links WHERE account_id = ?`, acctID)

	// Clean up starting transactions from projection and ledger
	for _, stxID := range startingTxIDs {
		tx.ExecContext(c.Request().Context(), `DELETE FROM budget_transactions_current WHERE id = ?`, stxID)
		tx.ExecContext(c.Request().Context(), `DELETE FROM budget_transactions WHERE id = ?`, stxID)
	}

	// Clean up account_currency_balances for this account
	tx.ExecContext(c.Request().Context(), `DELETE FROM account_currency_balances WHERE account_id = ?`, acctID)

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Commit error")
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

func (h *AccountsHandler) ensureUserParty(ctx context.Context, db *sql.DB, userID string, now int64) string {
	var partyID string
	err := db.QueryRowContext(ctx, `SELECT id FROM parties_current WHERE is_user_owned = 1 LIMIT 1`).Scan(&partyID)
	if err == nil {
		return partyID
	}

	partyID = domain.NewUUIDv7()
	db.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'User Family', 'INDIVIDUAL', 1, ?, ?)`, partyID, userID, now)
	return partyID
}

// InstitutionItem defines the common response model for bank search.
type InstitutionItem struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	BIC       string   `json:"bic"`
	Countries []string `json:"countries"`
	Logo      string   `json:"logo"`
	Provider  string   `json:"provider"`
}

// GetInstitutions returns the list of available banks for a selected provider and country.
func (h *AccountsHandler) GetInstitutions(c echo.Context) error {
	provider := strings.ToUpper(c.QueryParam("provider"))
	country := strings.ToUpper(c.QueryParam("country"))
	if country == "" {
		country = "PL"
	}

	var results []InstitutionItem

	if provider == "GOCARDLESS" && h.gocardlessClient != nil {
		gcInsts, err := h.gocardlessClient.ListInstitutions(c.Request().Context(), country)
		if err == nil {
			for _, inst := range gcInsts {
				results = append(results, InstitutionItem{
					ID:        inst.ID,
					Name:      inst.Name,
					BIC:       inst.BIC,
					Countries: inst.Countries,
					Logo:      inst.Logo,
					Provider:  "GOCARDLESS",
				})
			}
		}
	}

	if provider == "MOCK" && !isDevMode() {
		return echo.NewHTTPError(http.StatusForbidden, "Mock bank provider is disabled in production environment")
	}

	// Always provide MOCK institutions as default or fallback in dev mode
	if isDevMode() && (provider == "MOCK" || len(results) == 0) {
		for _, inst := range h.mockAdapter.ListInstitutions(country) {
			results = append(results, InstitutionItem{
				ID:        inst.ID,
				Name:      inst.Name,
				BIC:       inst.BIC,
				Countries: inst.Countries,
				Logo:      inst.Logo,
				Provider:  "MOCK",
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"provider":     provider,
		"country":      country,
		"institutions": results,
	})
}

type ConnectBankRequest struct {
	Provider       string   `json:"provider"`
	Country        string   `json:"country"`
	InstitutionIDs []string `json:"institution_ids"`
}

// ConnectBankQueue processes selected bank institutions.
// If multiple institutions are selected, it connects each sequentially.
func (h *AccountsHandler) ConnectBankQueue(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:connect_queue")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)

	var req ConnectBankRequest
	if err := c.Bind(&req); err != nil {
		// Fallback to form values
		req.Provider = c.FormValue("provider")
		req.Country = c.FormValue("country")
		if instIDs := c.Request().Form["institution_ids"]; len(instIDs) > 0 {
			req.InstitutionIDs = instIDs
		} else if instID := c.FormValue("institution_id"); instID != "" {
			req.InstitutionIDs = strings.Split(instID, ",")
		}
	}

	logger.Info(ctx, "Starting bank connection queue process",
		"budget_id", budgetID,
		"user_id", userID,
		"provider", req.Provider,
		"country", req.Country,
		"selected_institutions_count", len(req.InstitutionIDs),
		"institutions", req.InstitutionIDs,
	)

	if len(req.InstitutionIDs) == 0 {
		logger.Warn(ctx, "Bank connection queue rejected: no institutions selected", "budget_id", budgetID)
		return echo.NewHTTPError(http.StatusBadRequest, "No institutions selected")
	}

	req.Provider = strings.ToUpper(req.Provider)
	if req.Provider == "" {
		if isDevMode() {
			req.Provider = "MOCK"
		} else {
			req.Provider = "GOCARDLESS"
		}
	}

	if (req.Provider == "MOCK" || req.Provider == "GOCARDLESS_SANDBOX") && !isDevMode() {
		logger.Warn(ctx, "Mock bank connection rejected: not allowed in production", "provider", req.Provider)
		return echo.NewHTTPError(http.StatusForbidden, "Mock and sandbox bank providers are disabled in production environment")
	}

	now := time.Now().UTC().UnixMicro()
	userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)

	type ConnectedBankResult struct {
		InstitutionID string `json:"institution_id"`
		Status        string `json:"status"`
		AccountsCount int    `json:"accounts_count"`
		AuthURL       string `json:"auth_url,omitempty"`
	}

	var results []ConnectedBankResult

	for idx, instID := range req.InstitutionIDs {
		instID = strings.TrimSpace(instID)
		if instID == "" {
			continue
		}

		logger.Info(ctx, "Processing bank institution in connection queue",
			"queue_index", idx+1,
			"total_in_queue", len(req.InstitutionIDs),
			"institution_id", instID,
			"provider", req.Provider,
		)

		if (req.Provider == "GOCARDLESS" || req.Provider == "GOCARDLESS_SANDBOX") && h.gocardlessClient != nil {
			userRef := fmt.Sprintf("bal_%s_%s_%s", budgetID, userID, domain.NewUUIDv7())
			requisition, err := h.gocardlessClient.CreateRequisition(c.Request().Context(), instID, userRef)
			if err != nil {
				logger.Error(ctx, "Failed to create GoCardless requisition for institution",
					"institution_id", instID,
					"user_ref", userRef,
					"error", err.Error(),
				)
				results = append(results, ConnectedBankResult{
					InstitutionID: instID,
					Status:        "error: " + err.Error(),
				})
				continue
			}

			// Store requisition in user_integrations
			intID := domain.NewUUIDv7()
			h.mainDB.ExecContext(c.Request().Context(), `
				INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, created_at, updated_at)
				VALUES (?, ?, 'GOCARDLESS', ?, ?, 'PENDING', ?, ?)`,
				intID, userID, instID, requisition.ID, now, now,
			)

			logger.Info(ctx, "GoCardless requisition created, awaiting user bank authorization",
				"institution_id", instID,
				"requisition_id", requisition.ID,
				"user_ref", userRef,
				"integration_id", intID,
			)

			results = append(results, ConnectedBankResult{
				InstitutionID: instID,
				Status:        "awaiting_authorization",
				AuthURL:       requisition.Link,
			})
			continue
		}

		if req.Provider == "GOCARDLESS_SANDBOX" && h.gocardlessClient == nil {
			// Mock sandbox requisition when gocardless client is not configured
			intID := domain.NewUUIDv7()
			reqRef := fmt.Sprintf("sandbox_ref_%s", domain.NewUUIDv7())
			h.mainDB.ExecContext(c.Request().Context(), `
				INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, created_at, updated_at)
				VALUES (?, ?, 'GOCARDLESS', ?, ?, 'PENDING', ?, ?)`,
				intID, userID, instID, reqRef, now, now,
			)
			results = append(results, ConnectedBankResult{
				InstitutionID: instID,
				Status:        "awaiting_authorization",
				AuthURL:       fmt.Sprintf("https://ob.gocardless.com/psd2/start/%s", reqRef),
			})
			continue
		}

		// Mock Provider: Deterministic sequential link
		mockAccounts, err := h.mockAdapter.FetchAccounts(c.Request().Context(), userID)
		if err != nil {
			logger.Error(ctx, "Failed fetching mock provider accounts", "institution_id", instID, "error", err.Error())
		}
		accountsAdded := 0

		tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
		if err != nil {
			logger.Error(ctx, "Failed beginning transaction for mock institution import", "institution_id", instID, "error", err.Error())
			continue
		}

		for mIdx, ma := range mockAccounts {
			acctID := domain.NewUUIDv7()
			accName := fmt.Sprintf("%s (%s #%d)", ma.Name, instID, mIdx+1)

			_, err = tx.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, ?, 'CHECKING', ?, ?, 0, ?, ?)`,
				acctID, userPartyID, accName, ma.Currency, ma.IBAN, userID, now,
			)
			if err != nil {
				logger.Error(ctx, "Failed inserting accounts_current in mock queue", "institution_id", instID, "account_id", acctID, "error", err.Error())
				continue
			}

			linkID := domain.NewUUIDv7()
			intID := fmt.Sprintf("mock_int_%s_%s", instID, userID)
			tx.ExecContext(c.Request().Context(), `
				INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				linkID, acctID, intID, fmt.Sprintf("%s_%s", ma.ID, instID), now, now,
			)

			tx.ExecContext(c.Request().Context(), `
				INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
				VALUES (?, ?, ?, ?)`,
				acctID, ma.Currency, ma.BalanceMinor, now,
			)
			accountsAdded++
		}
		if err := tx.Commit(); err != nil {
			logger.Error(ctx, "Failed committing accounts for mock institution", "institution_id", instID, "error", err.Error())
		} else {
			logger.Info(ctx, "Mock bank accounts connected successfully",
				"institution_id", instID,
				"accounts_added_count", accountsAdded,
			)
		}

		results = append(results, ConnectedBankResult{
			InstitutionID: instID,
			Status:        "connected",
			AccountsCount: accountsAdded,
		})
	}

	logger.Info(ctx, "Bank connection queue processing completed",
		"budget_id", budgetID,
		"results_count", len(results),
	)

	// If requested via JSON API or fetch, return structured response
	if strings.Contains(c.Request().Header.Get("Accept"), "application/json") || c.Request().Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return c.JSON(http.StatusOK, map[string]any{
			"budget_id": budgetID,
			"results":   results,
		})
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", budgetID))
}

// ReconnectAccount handles in-place re-consent / re-authorization for an existing bank-linked account.
func (h *AccountsHandler) ReconnectAccount(c echo.Context) error {
	acctID := c.Param("id")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)

	now := time.Now().UTC().UnixMicro()

	// 1. Fetch existing account and its integration mapping
	var integrationID, externalAcctID, acctName string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT a.name, COALESCE(ail.integration_id, ''), COALESCE(ail.external_account_id, '')
		FROM accounts_current a
		LEFT JOIN account_integration_links ail ON a.id = ail.account_id
		WHERE a.id = ?`, acctID).Scan(&acctName, &integrationID, &externalAcctID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Account not found")
	}

	if externalAcctID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Account is not linked to any bank integration")
	}

	// 2. Fetch integration record from main.db
	var provider, institutionID string
	err = h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT provider, COALESCE(institution_id, '')
		FROM user_integrations WHERE id = ? AND user_id = ?`, integrationID, userID).Scan(&provider, &institutionID)
	if err != nil {
		// If integration was not registered or mock pattern
		provider = "MOCK"
	}

	if provider == "GOCARDLESS" && h.gocardlessClient != nil {
		budgetID := c.Param("budgetId")
		userRef := fmt.Sprintf("bal_recon_%s_%s_%s", budgetID, userID, domain.NewUUIDv7())
		requisition, err := h.gocardlessClient.CreateRequisition(c.Request().Context(), institutionID, userRef)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create bank reconnection session: "+err.Error())
		}

		// Update or insert renewed integration
		newIntID := domain.NewUUIDv7()
		h.mainDB.ExecContext(c.Request().Context(), `
			INSERT INTO user_integrations (id, user_id, provider, institution_id, external_reference_id, status, created_at, updated_at)
			VALUES (?, ?, 'GOCARDLESS', ?, ?, 'PENDING', ?, ?)`,
			newIntID, userID, institutionID, requisition.ID, now, now,
		)

		// Link account to new integration record
		tenantDB.ExecContext(c.Request().Context(), `
			UPDATE account_integration_links SET integration_id = ?, last_synced_at = ? WHERE account_id = ?`,
			newIntID, now, acctID,
		)

		return c.JSON(http.StatusOK, map[string]any{
			"status":   "awaiting_authorization",
			"auth_url": requisition.Link,
			"account":  acctName,
		})
	}

	// Mock Provider: Instant re-activation and renewal
	if !isDevMode() {
		return echo.NewHTTPError(http.StatusForbidden, "Mock bank reconnection is disabled in production environment")
	}

	if integrationID != "" {
		h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE user_integrations SET status = 'ACTIVE', expires_at = ?, updated_at = ? WHERE id = ?`,
			now+180*24*3600*1_000_000, now, integrationID,
		)
	}

	tenantDB.ExecContext(c.Request().Context(), `
		UPDATE account_integration_links SET last_synced_at = ? WHERE account_id = ?`,
		now, acctID,
	)

	return c.JSON(http.StatusOK, map[string]any{
		"status":  "ACTIVE",
		"account": acctName,
		"message": "Bank access successfully renewed",
	})
}

// GoCardlessCallback handles OAuth redirect from GoCardless after user authenticates with their bank.
func (h *AccountsHandler) GoCardlessCallback(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:account:gocardless_callback")

	ref := c.QueryParam("ref")
	if ref == "" {
		logger.Warn(ctx, "GoCardless callback rejected: missing requisition reference")
		return echo.NewHTTPError(http.StatusBadRequest, "Missing requisition reference")
	}

	userID, _ := c.Get(ContextUserIDKey).(string)
	logger.Info(ctx, "GoCardless OAuth callback received", "ref", ref, "user_id", userID)

	now := time.Now().UTC().UnixMicro()

	// 1. Locate user integration by requisition external_reference_id OR ref pattern
	var intID, instID, dbUserID, requisitionID string
	err := h.mainDB.QueryRowContext(c.Request().Context(), `
		SELECT id, institution_id, user_id, COALESCE(external_reference_id, '')
		FROM user_integrations
		WHERE external_reference_id = ?
		LIMIT 1`, ref).Scan(&intID, &instID, &dbUserID, &requisitionID)

	if err != nil {
		// Try matching by reference prefix if ref is the userRef
		err = h.mainDB.QueryRowContext(c.Request().Context(), `
			SELECT id, institution_id, user_id, COALESCE(external_reference_id, '')
			FROM user_integrations
			WHERE provider = 'GOCARDLESS' AND status = 'PENDING'
			ORDER BY created_at DESC LIMIT 1`).Scan(&intID, &instID, &dbUserID, &requisitionID)
	}

	if userID == "" && dbUserID != "" {
		userID = dbUserID
	}

	// Extract budgetID from userRef pattern: bal_<budgetID>_<userID>_<timestamp>
	var targetBudgetID string
	parts := strings.Split(ref, "_")
	if len(parts) >= 3 && domain.IsValidUUIDv7(parts[1]) {
		targetBudgetID = parts[1]
	} else if len(parts) >= 4 && parts[1] == "recon" && domain.IsValidUUIDv7(parts[2]) {
		targetBudgetID = parts[2]
	}

	// Fallback to query param if passed
	if targetBudgetID == "" {
		targetBudgetID = c.QueryParam("budget_id")
	}

	// Fallback to first user budget if still empty
	if targetBudgetID == "" && userID != "" {
		h.mainDB.QueryRowContext(c.Request().Context(), `
			SELECT budget_id FROM budget_members WHERE user_id = ? ORDER BY joined_at ASC LIMIT 1`,
			userID).Scan(&targetBudgetID)
	}

	if targetBudgetID == "" {
		return c.Redirect(http.StatusSeeOther, "/budgets")
	}

	// 2. If gocardless client is configured, inspect requisition accounts and import them
	if h.gocardlessClient != nil {
		reqLookupID := requisitionID
		if reqLookupID == "" {
			reqLookupID = ref
		}

		// Polling loop: banks often take 1-3 seconds to finalize PSD2 account consent and link accounts to requisition
		var reqDetails *gocardless.Requisition
		var reqErr error
		for attempt := 1; attempt <= 4; attempt++ {
			reqDetails, reqErr = h.gocardlessClient.GetRequisition(c.Request().Context(), reqLookupID)
			if reqErr != nil {
				logger.Warn(ctx, "GoCardless GetRequisition attempt failed", "attempt", attempt, "requisition_id", reqLookupID, "error", reqErr.Error())
			} else if reqDetails != nil {
				logger.Info(ctx, "GoCardless requisition status polled",
					"attempt", attempt,
					"requisition_id", reqLookupID,
					"status", reqDetails.Status,
					"accounts_count", len(reqDetails.Accounts),
				)
				if len(reqDetails.Accounts) > 0 || reqDetails.Status == "EX" || reqDetails.Status == "RJ" {
					break
				}
			}
			if attempt < 4 {
				select {
				case <-c.Request().Context().Done():
					break
				case <-time.After(time.Duration(attempt*800) * time.Millisecond):
				}
			}
		}

		if reqErr != nil {
			logger.Error(ctx, "Failed to fetch GoCardless requisition details after polling", "requisition_id", reqLookupID, "ref", ref, "error", reqErr.Error())
		} else if reqDetails == nil || len(reqDetails.Accounts) == 0 {
			var status string
			if reqDetails != nil {
				status = reqDetails.Status
			}
			logger.Warn(ctx, "GoCardless requisition returned no accounts", "requisition_id", reqLookupID, "ref", ref, "status", status)
		} else {
			tenantDB, err := h.tenantMgr.GetTenantDB(targetBudgetID, false)
			if err != nil || tenantDB == nil {
				logger.Error(ctx, "Failed to open tenant DB for imported accounts", "budget_id", targetBudgetID, "error", err)
			} else {
				userPartyID := h.ensureUserParty(c.Request().Context(), tenantDB, userID, now)
				for idx, extAcctID := range reqDetails.Accounts {
					details, err := h.gocardlessClient.GetAccountDetails(c.Request().Context(), extAcctID)
					var acctName, iban, curr string
					curr = "PLN"
					if err == nil && details != nil {
						iban = details.Account.IBAN
						if details.Account.Currency != "" {
							curr = details.Account.Currency
						}
						if details.Account.Name != "" {
							acctName = details.Account.Name
						} else if details.Account.DisplayName != "" {
							acctName = details.Account.DisplayName
						} else if iban != "" {
							acctName = iban
						}
					}
					if acctName == "" {
						acctName = fmt.Sprintf("Bank Account #%d (%s)", idx+1, instID)
					}

					// Check if account already linked
					var existingAcctID string
					tenantDB.QueryRowContext(c.Request().Context(), `
						SELECT account_id FROM account_integration_links WHERE external_account_id = ?`,
						extAcctID).Scan(&existingAcctID)

					if existingAcctID != "" {
						// Update last synced and integration id
						tenantDB.ExecContext(c.Request().Context(), `
							UPDATE account_integration_links SET integration_id = ?, last_synced_at = ? WHERE account_id = ?`,
							intID, now, existingAcctID)
						logger.Info(ctx, "Existing bank account integration link refreshed",
							"account_id", existingAcctID,
							"external_account_id", extAcctID,
							"integration_id", intID,
						)
					} else {
						newAcctID := domain.NewUUIDv7()
						tenantDB.ExecContext(c.Request().Context(), `
							INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, is_closed, agent_id, updated_at)
							VALUES (?, 1, ?, ?, 'CHECKING', ?, ?, 0, ?, ?)`,
							newAcctID, userPartyID, acctName, curr, iban, userID, now)

						tenantDB.ExecContext(c.Request().Context(), `
							INSERT INTO account_integration_links (id, account_id, integration_id, external_account_id, last_synced_at, created_at)
							VALUES (?, ?, ?, ?, ?, ?)`,
							domain.NewUUIDv7(), newAcctID, intID, extAcctID, now, now)

						logger.Info(ctx, "New bank account imported from GoCardless OAuth callback",
							"account_id", newAcctID,
							"name", acctName,
							"currency", curr,
							"iban", iban,
							"external_account_id", extAcctID,
							"integration_id", intID,
						)
					}
				}
			}
		}
	}

	// 3. Transition integration to ACTIVE in mainDB
	if intID != "" {
		h.mainDB.ExecContext(c.Request().Context(), `
			UPDATE user_integrations SET status = 'ACTIVE', expires_at = ?, updated_at = ? WHERE id = ?`,
			now+90*24*3600*1_000_000, now, intID)
		logger.Info(ctx, "GoCardless integration activated", "integration_id", intID, "expires_in_days", 90)
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/accounts", targetBudgetID))
}

// ExtractFavicon handles website URL discovery to extract the highest-quality favicon link.
func (h *AccountsHandler) ExtractFavicon(c echo.Context) error {
	rawURL := c.QueryParam("url")
	if rawURL == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "URL query param is required")
	}

	bestFavicon, err := DiscoverFavicon(c.Request().Context(), rawURL)
	if err != nil {
		return c.JSON(http.StatusOK, FaviconDiscoveryResult{
			URL:         rawURL,
			BestFavicon: "",
			Status:      "error",
			Message:     err.Error(),
		})
	}

	return c.JSON(http.StatusOK, FaviconDiscoveryResult{
		URL:         rawURL,
		BestFavicon: bestFavicon,
		Status:      "success",
	})
}
