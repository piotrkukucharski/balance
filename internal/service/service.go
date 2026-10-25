package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/auth"
	"github.com/balance/balance/internal/budget"
	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/transaction"
)

var (
	ErrUserAlreadyExists = errors.New("user already exists")
	ErrUserNotFound      = errors.New("user not found")
	ErrBudgetNotFound    = errors.New("budget not found")
	ErrInvalidInput      = errors.New("invalid input")
)

// AppService coordinates domain actions for both HTTP handlers and CLI operations.
type AppService struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
	clock     clock.Clock
}

// NewAppService constructs a new AppService.
func NewAppService(mainDB *sql.DB, tenantMgr *db.TenantDBManager, clk clock.Clock) *AppService {
	if clk == nil {
		clk = clock.NewSystemClock(time.UTC)
	}
	return &AppService{
		mainDB:    mainDB,
		tenantMgr: tenantMgr,
		clock:     clk,
	}
}

// Clock returns the configured clock.
func (s *AppService) Clock() clock.Clock {
	return s.clock
}

// SetClock allows dynamically changing or advancing the clock (e.g. for simulations).
func (s *AppService) SetClock(clk clock.Clock) {
	s.clock = clk
}

// CreateUserInput defines input parameters for creating a user account.
type CreateUserInput struct {
	ID            string
	Email         string
	Password      string
	DisplayName   string
	FirstActionAt *time.Time
}

// ValidateCreateUserInput validates user signup parameters.
func ValidateCreateUserInput(input CreateUserInput) error {
	if strings.TrimSpace(input.Email) == "" {
		return fmt.Errorf("%w: email is required", ErrInvalidInput)
	}
	if _, err := mail.ParseAddress(input.Email); err != nil {
		return fmt.Errorf("%w: invalid email address format", ErrInvalidInput)
	}
	if len(input.Password) < 8 {
		return fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidInput)
	}
	if input.ID != "" && !domain.IsValidUUIDv7(input.ID) {
		return fmt.Errorf("%w: userId must be a valid UUIDv7", ErrInvalidInput)
	}
	return nil
}

// CreateUser creates a new user account in mainDB.
func (s *AppService) CreateUser(ctx context.Context, input CreateUserInput) (string, error) {
	if err := ValidateCreateUserInput(input); err != nil {
		return "", err
	}

	userID := input.ID
	if userID == "" {
		userID = domain.NewUUIDv7()
	}

	var exists int
	err := s.mainDB.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ? OR email = ? LIMIT 1`, userID, input.Email).Scan(&exists)
	if err == nil {
		return "", ErrUserAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("check existing user: %w", err)
	}

	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	var createdAtMicros int64
	if input.FirstActionAt != nil {
		createdAtMicros = input.FirstActionAt.UTC().UnixMicro()
	} else {
		createdAtMicros = s.clock.Now().UTC().UnixMicro()
	}

	displayName := input.DisplayName
	if strings.TrimSpace(displayName) == "" {
		// Default display name to email username
		parts := strings.Split(input.Email, "@")
		displayName = parts[0]
	}

	_, err = s.mainDB.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, input.Email, hash, displayName, createdAtMicros, createdAtMicros,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", ErrUserAlreadyExists
		}
		return "", fmt.Errorf("insert user: %w", err)
	}

	return userID, nil
}

// RemoveUser removes a user and their memberships/sessions from mainDB.
func (s *AppService) RemoveUser(ctx context.Context, userID string) error {
	if userID == "" {
		return fmt.Errorf("%w: userId is required", ErrInvalidInput)
	}

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil || rows == 0 {
		return ErrUserNotFound
	}

	return tx.Commit()
}

// CleanExistingUserAndBudgets finds any existing user by email or ID, removes all their owned budgets, and deletes the user.
func (s *AppService) CleanExistingUserAndBudgets(ctx context.Context, email, budgetID string) error {
	// 1. If budgetID exists, delete it first
	if budgetID != "" {
		_ = s.RemoveBudget(ctx, budgetID)
	}

	// 2. Find user by email
	if email != "" {
		var existingUserID string
		err := s.mainDB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&existingUserID)
		if err == nil && existingUserID != "" {
			// Find all budgets owned by this user and remove them
			rows, qErr := s.mainDB.QueryContext(ctx, `SELECT budget_id FROM budget_members WHERE user_id = ?`, existingUserID)
			if qErr == nil {
				var bIDs []string
				for rows.Next() {
					var bID string
					if err := rows.Scan(&bID); err == nil {
						bIDs = append(bIDs, bID)
					}
				}
				rows.Close()
				for _, bID := range bIDs {
					_ = s.RemoveBudget(ctx, bID)
				}
			}

			// Remove user
			_ = s.RemoveUser(ctx, existingUserID)
		}
	}

	return nil
}

// CreateBudgetInput parameters.
type CreateBudgetInput struct {
	ID              string
	Name            string
	OwnerUserID     string
	DefaultCurrency string
	Timezone        string
	FirstActionAt   *time.Time
	CorrelationID   string
}

// ValidateCreateBudgetInput checks fields before budget creation.
func ValidateCreateBudgetInput(input CreateBudgetInput) error {
	if strings.TrimSpace(input.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if strings.TrimSpace(input.OwnerUserID) == "" {
		return fmt.Errorf("%w: ownerUserId is required", ErrInvalidInput)
	}
	if input.ID != "" && !domain.IsValidUUIDv7(input.ID) {
		return fmt.Errorf("%w: budgetId must be a valid UUIDv7", ErrInvalidInput)
	}
	if input.Timezone != "" {
		if _, err := time.LoadLocation(input.Timezone); err != nil {
			return fmt.Errorf("%w: invalid timezone location '%s'", ErrInvalidInput, input.Timezone)
		}
	}
	return nil
}

// CreateBudget provisions a new budget tenant and binds the owner.
func (s *AppService) CreateBudget(ctx context.Context, input CreateBudgetInput) (string, error) {
	if err := ValidateCreateBudgetInput(input); err != nil {
		return "", err
	}

	budgetID := input.ID
	if budgetID == "" {
		budgetID = domain.NewUUIDv7()
	}

	curr := strings.ToUpper(strings.TrimSpace(input.DefaultCurrency))
	if curr == "" {
		curr = "PLN"
	}

	tz := strings.TrimSpace(input.Timezone)
	if tz == "" {
		tz = "UTC"
	}

	// Fetch owner user and creation date to validate: budget creation date cannot precede owner creation date!
	var userCreatedAt int64
	err := s.mainDB.QueryRowContext(ctx, `SELECT created_at FROM users WHERE id = ?`, input.OwnerUserID).Scan(&userCreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: owner user %s does not exist", ErrUserNotFound, input.OwnerUserID)
		}
		return "", fmt.Errorf("query owner user: %w", err)
	}

	var budgetCreatedAt int64
	if input.FirstActionAt != nil {
		budgetCreatedAt = input.FirstActionAt.UTC().UnixMicro()
	} else {
		budgetCreatedAt = s.clock.Now().UTC().UnixMicro()
	}

	if budgetCreatedAt < userCreatedAt {
		return "", fmt.Errorf("%w: budget firstActionAt (%v) cannot be older than owner user created_at (%v)",
			ErrInvalidInput, time.UnixMicro(budgetCreatedAt).UTC(), time.UnixMicro(userCreatedAt).UTC())
	}

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		budgetID, input.Name, curr, tz, input.OwnerUserID, budgetCreatedAt, budgetCreatedAt,
	)
	if err != nil {
		return "", fmt.Errorf("insert budget: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO budget_members (budget_id, user_id, role, joined_at)
		VALUES (?, ?, 'OWNER', ?)`,
		budgetID, input.OwnerUserID, budgetCreatedAt,
	)
	if err != nil {
		return "", fmt.Errorf("assign budget owner: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit budget: %w", err)
	}

	// Provision tenant DB
	_, err = s.tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		return "", fmt.Errorf("initialize tenant db: %w", err)
	}

	return budgetID, nil
}

// RemoveBudget deletes budget from mainDB and removes the tenant database file.
func (s *AppService) RemoveBudget(ctx context.Context, budgetID string) error {
	if budgetID == "" {
		return fmt.Errorf("%w: budgetId is required", ErrInvalidInput)
	}

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, _ = tx.ExecContext(ctx, `DELETE FROM budget_invitations WHERE budget_id = ?`, budgetID)
	_, _ = tx.ExecContext(ctx, `DELETE FROM budget_members WHERE budget_id = ?`, budgetID)
	res, err := tx.ExecContext(ctx, `DELETE FROM budgets WHERE id = ?`, budgetID)
	if err != nil {
		return fmt.Errorf("delete budget: %w", err)
	}
	rowsAff, err := res.RowsAffected()
	if err != nil || rowsAff == 0 {
		return ErrBudgetNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete budget: %w", err)
	}

	if err := s.tenantMgr.DeleteTenantDB(budgetID); err != nil {
		// Log or return warning
		_ = err
	}

	return nil
}

// CreateAccountInput creates a financial account in a budget tenant.
type CreateAccountInput struct {
	BudgetID            string
	UserID              string
	CorrelationID       string
	Name                string
	AccountType         string
	PrimaryCurrency     string
	IBAN                string
	FaviconType         string
	FaviconValue        string
	InitialBalanceMinor int64
	ActionAt            *time.Time
}

// CreateFinancialAccount adds a user-owned financial account.
func (s *AppService) CreateFinancialAccount(ctx context.Context, input CreateAccountInput) (string, error) {
	if input.Name == "" || input.AccountType == "" || input.PrimaryCurrency == "" {
		return "", fmt.Errorf("%w: name, account_type, and currency are required", ErrInvalidInput)
	}

	tenantDB, err := s.tenantMgr.GetTenantDB(input.BudgetID, false)
	if err != nil {
		return "", fmt.Errorf("get tenant db: %w", err)
	}

	var now int64
	if input.ActionAt != nil {
		now = input.ActionAt.UTC().UnixMicro()
	} else {
		now = s.clock.Now().UTC().UnixMicro()
	}

	userPartyID := s.ensureUserParty(ctx, tenantDB, input.UserID, now)
	acctID := domain.NewUUIDv7()

	favType := input.FaviconType
	if favType != "URL" {
		favType = "EMOJI"
	}
	favVal := input.FaviconValue
	if favVal == "" {
		favVal = "💳"
	}

	corrID := input.CorrelationID
	if corrID == "" {
		corrID = domain.NewUUIDv7()
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 0, 'CREATE', ?, ?, ?)`,
		acctID, userPartyID, input.Name, input.AccountType, input.PrimaryCurrency, input.IBAN, favType, favVal, input.UserID, corrID, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert into accounts: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		acctID, userPartyID, input.Name, input.AccountType, input.PrimaryCurrency, input.IBAN, favType, favVal, input.UserID, now,
	)
	if err != nil {
		return "", fmt.Errorf("insert into accounts_current: %w", err)
	}

	if input.InitialBalanceMinor != 0 {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
			VALUES (?, ?, ?, ?)`, acctID, input.PrimaryCurrency, input.InitialBalanceMinor, now)
		if err != nil {
			return "", fmt.Errorf("insert balance: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit account: %w", err)
	}

	return acctID, nil
}

func (s *AppService) ensureUserParty(ctx context.Context, tenantDB *sql.DB, userID string, now int64) string {
	var partyID string
	err := tenantDB.QueryRowContext(ctx, `SELECT id FROM parties_current WHERE is_user_owned = 1 LIMIT 1`).Scan(&partyID)
	if err == nil {
		return partyID
	}

	partyID = domain.NewUUIDv7()
	tenantDB.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, 'Primary Family', 'INDIVIDUAL', 1, ?, ?)`, partyID, userID, now)
	return partyID
}

// FindOrCreateExternalAccount ensures an external merchant or payer party and account exists.
func (s *AppService) FindOrCreateExternalAccount(ctx context.Context, tenantDB *sql.DB, partyName, currency, userID string, now int64) (string, error) {
	cleanName := strings.TrimSpace(partyName)
	if cleanName == "" {
		cleanName = "General Merchant"
	}
	var counterpartyAcctID string
	err := tenantDB.QueryRowContext(ctx, `
		SELECT a.id FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.name = ? AND p.is_user_owned = 0
		LIMIT 1`, cleanName).Scan(&counterpartyAcctID)
	if err == nil {
		return counterpartyAcctID, nil
	}

	pID := domain.NewUUIDv7()
	counterpartyAcctID = domain.NewUUIDv7()
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`, pID, cleanName, userID, now)
	if err != nil {
		return "", err
	}
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'EXTERNAL', ?, 0, ?, ?)`, counterpartyAcctID, pID, cleanName, currency, userID, now)
	if err != nil {
		return "", err
	}
	return counterpartyAcctID, nil
}

// TransactionType defines entry types.
type TransactionType string

const (
	TxExpense  TransactionType = "EXPENSE"
	TxIncome   TransactionType = "INCOME"
	TxTransfer TransactionType = "TRANSFER"
)

// AddTransactionInput specifies parameters for adding a financial transaction.
type AddTransactionInput struct {
	BudgetID            string
	UserID              string
	CorrelationID       string
	TxType              TransactionType
	SourceAccountID     string
	DestinationAcctID   string
	AmountMinor         int64
	Currency            string
	DestAmountMinor     int64
	DestCurrency        string
	BucketID            *string
	Notes               string
	TransactionDate     time.Time
}

// AddTransaction executes a transaction using the unified double-entry ledger engine.
func (s *AppService) AddTransaction(ctx context.Context, input AddTransactionInput) (string, error) {
	if input.AmountMinor <= 0 {
		return "", fmt.Errorf("%w: amount must be positive", ErrInvalidInput)
	}

	tenantDB, err := s.tenantMgr.GetTenantDB(input.BudgetID, false)
	if err != nil {
		return "", fmt.Errorf("tenant db: %w", err)
	}

	txDateMicros := input.TransactionDate.UTC().UnixMicro()
	nowMicros := s.clock.Now().UTC().UnixMicro()
	_ = nowMicros

	var srcAcct, dstAcct string
	var srcCurr, dstCurr string
	destAmountMinor := input.DestAmountMinor
	if destAmountMinor <= 0 {
		destAmountMinor = input.AmountMinor
	}

	corrID := input.CorrelationID
	if corrID == "" {
		corrID = domain.NewUUIDv7()
	}

	switch input.TxType {
	case TxIncome:
		dstAcct = input.DestinationAcctID
		if dstAcct == "" {
			dstAcct = input.SourceAccountID
		}
		_ = tenantDB.QueryRowContext(ctx, `SELECT primary_currency FROM accounts_current WHERE id = ?`, dstAcct).Scan(&dstCurr)
		if dstCurr == "" {
			dstCurr = input.Currency
		}
		srcCurr = dstCurr

		payerName := input.Notes
		if payerName == "" {
			payerName = "External Payer"
		}
		extAcct, err := s.FindOrCreateExternalAccount(ctx, tenantDB, payerName, srcCurr, input.UserID, txDateMicros)
		if err != nil {
			return "", fmt.Errorf("external payer account: %w", err)
		}
		srcAcct = extAcct

	case TxTransfer:
		srcAcct = input.SourceAccountID
		dstAcct = input.DestinationAcctID
		if srcAcct == "" || dstAcct == "" || srcAcct == dstAcct {
			return "", fmt.Errorf("%w: transfer requires distinct source and destination accounts", ErrInvalidInput)
		}
		_ = tenantDB.QueryRowContext(ctx, `SELECT primary_currency FROM accounts_current WHERE id = ?`, srcAcct).Scan(&srcCurr)
		_ = tenantDB.QueryRowContext(ctx, `SELECT primary_currency FROM accounts_current WHERE id = ?`, dstAcct).Scan(&dstCurr)
		if srcCurr == "" {
			srcCurr = input.Currency
		}
		if dstCurr == "" {
			dstCurr = srcCurr
		}
		input.BucketID = nil // transfers don't consume budget buckets

	default: // EXPENSE
		srcAcct = input.SourceAccountID
		_ = tenantDB.QueryRowContext(ctx, `SELECT primary_currency FROM accounts_current WHERE id = ?`, srcAcct).Scan(&srcCurr)
		if srcCurr == "" {
			srcCurr = input.Currency
		}
		dstCurr = srcCurr

		merchantName := input.Notes
		if merchantName == "" {
			merchantName = "General Merchant"
		}
		extAcct, err := s.FindOrCreateExternalAccount(ctx, tenantDB, merchantName, dstCurr, input.UserID, txDateMicros)
		if err != nil {
			return "", fmt.Errorf("external merchant account: %w", err)
		}
		dstAcct = extAcct
	}

	notes := input.Notes
	opID, err := transaction.ExecuteResolution(ctx, tenantDB, transaction.ResolveResolutionInput{
		OperationType: transaction.OpDirectMove,
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      srcAcct,
				DestinationAccountID: dstAcct,
				SourceAmountMinor:    input.AmountMinor,
				SourceCurrency:       srcCurr,
				DestAmountMinor:      destAmountMinor,
				DestCurrency:         dstCurr,
				BucketID:             input.BucketID,
				Notes:                &notes,
				TransactionDate:      txDateMicros,
			},
		},
		AgentID:       input.UserID,
		CorrelationID: corrID,
	})
	if err != nil {
		return "", fmt.Errorf("execute resolution: %w", err)
	}

	return opID, nil
}

// CreateBucketAndTarget helpers wrap budget package logic.
func (s *AppService) CreateBucket(ctx context.Context, budgetID string, input budget.CreateBucketInput) (string, error) {
	tenantDB, err := s.tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		return "", err
	}
	return budget.CreateBucket(ctx, tenantDB, input)
}

func (s *AppService) CreateTarget(ctx context.Context, budgetID string, input budget.CreateTargetInput) (string, error) {
	tenantDB, err := s.tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		return "", err
	}
	return budget.CreateTarget(ctx, tenantDB, input)
}

// CheckOwnerMembership verifies if a given user is OWNER of the budget.
func (s *AppService) CheckOwnerMembership(ctx context.Context, budgetID, userID string) (bool, error) {
	var role string
	err := s.mainDB.QueryRowContext(ctx, `SELECT role FROM budget_members WHERE budget_id = ? AND user_id = ?`, budgetID, userID).Scan(&role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return role == "OWNER", nil
}

// ApplyBudgetStructure applies an entire budget structure to the tenant database.
func (s *AppService) ApplyBudgetStructure(ctx context.Context, budgetID string, structure *ai.BudgetStructure, agentID, correlationID string) (int, int, error) {
	tenantDB, err := s.tenantMgr.GetTenantDB(budgetID, false)
	if err != nil {
		return 0, 0, err
	}
	return budget.ApplyBudgetStructure(ctx, tenantDB, structure, agentID, correlationID)
}

// ExportBudgetStructure exports an existing budget structure from the tenant database.
func (s *AppService) ExportBudgetStructure(ctx context.Context, budgetID string) (*ai.BudgetStructure, error) {
	tenantDB, err := s.tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		return nil, err
	}
	return budget.ExportBudgetStructure(ctx, tenantDB)
}

