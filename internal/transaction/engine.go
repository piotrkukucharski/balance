package transaction

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
)

// Resolution Operation Types
const (
	OpDirectMove = "DIRECT_MOVE"
	OpMerge      = "MERGE"
	OpSplit      = "SPLIT"
	OpComposite  = "COMPOSITE"
)

// ComputeCanonicalDedupHash computes SHA256 over length-prefixed fields.
// SHA256(len_prefixed(account_id, external_tx_id_or_seq, booking_date, amount_minor, currency, clean_payee, clean_reference))
func ComputeCanonicalDedupHash(accountID, extTxIDOrSeq string, bookingDate, amountMinor int64, currency, payee, ref string) string {
	cleanPayee := strings.TrimSpace(payee)
	cleanRef := strings.TrimSpace(ref)

	var b strings.Builder
	writeField := func(s string) {
		fmt.Fprintf(&b, "%d:%s\n", len(s), s)
	}

	writeField(accountID)
	writeField(extTxIDOrSeq)
	writeField(fmt.Sprintf("%d", bookingDate))
	writeField(fmt.Sprintf("%d", amountMinor))
	writeField(currency)
	writeField(cleanPayee)
	writeField(cleanRef)

	h := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", h[:])
}

// BudgetTxInput represents the payload for inserting a budget transaction.
type BudgetTxInput struct {
	SourceAccountID      string
	DestinationAccountID string
	SourceAmountMinor    int64
	SourceCurrency       string
	DestAmountMinor      int64
	DestCurrency         string
	BucketID             *string
	TargetID             *string
	TransactionDate      int64
	Notes                *string
}

// ResolveResolutionInput parameters for Move / Merge / Split atomic operation.
type ResolveResolutionInput struct {
	OperationType      string // DIRECT_MOVE, MERGE, SPLIT, COMPOSITE
	RawTransactionIDs  []string
	BudgetTransactions []BudgetTxInput
	AgentID            string
	CorrelationID      string
}

// ExecuteResolution executes an atomic Move / Merge / Split operation on a tenant DB:
// 1. Validates raw transactions exist in raw_transactions_current with status = 'PENDING'.
// 2. Enforces ledger invariants on BudgetTransactions:
//   - No same-account same-currency self-transfer
//   - At least one side owned by user (is_user_owned = 1)
//   - Safe amount bounds
//
// 3. Atomically:
//   - Appends budget_transactions & updates budget_transactions_current
//   - Transitions raw_transactions to status = 'PROCESSED' (append version & update current)
//   - Writes transaction_links with operation_id
//   - Updates account_currency_balances and bucket_currency_balances
func ExecuteResolution(ctx context.Context, tenantDB *sql.DB, input ResolveResolutionInput) (string, error) {
	if len(input.RawTransactionIDs) == 0 && len(input.BudgetTransactions) == 0 {
		return "", errors.New("empty resolution payload")
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()
	operationID := domain.NewUUIDv7()

	// 1. Validate and fetch raw transactions
	for _, rawID := range input.RawTransactionIDs {
		var status string
		var rawVersion int
		var accountID string
		var dedupHash, currency, rawPayee string
		var extTxID, counterpartyIBAN, reference sql.NullString
		var bookingDate, valueDate sql.NullInt64
		var amountMinor int64

		err := tx.QueryRowContext(ctx, `
			SELECT version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
			       amount_minor, currency, raw_payee, counterparty_iban, reference, status
			FROM raw_transactions_current WHERE id = ?`, rawID).Scan(
			&rawVersion, &accountID, &extTxID, &dedupHash, &bookingDate, &valueDate,
			&amountMinor, &currency, &rawPayee, &counterpartyIBAN, &reference, &status,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("raw transaction %s not found", rawID)
			}
			return "", err
		}
		if status != "PENDING" {
			return "", fmt.Errorf("raw transaction %s is not PENDING (status: %s)", rawID, status)
		}

		newVersion := rawVersion + 1
		_, err = tx.ExecContext(ctx, `
			INSERT INTO raw_transactions (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
				amount_minor, currency, raw_payee, counterparty_iban, reference, status, action,
				agent_id, correlation_id, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'PROCESSED', 'UPDATE', ?, ?, ?)`,
			rawID, newVersion, accountID, extTxID, dedupHash, bookingDate, valueDate,
			amountMinor, currency, rawPayee, counterpartyIBAN, reference,
			input.AgentID, input.CorrelationID, now,
		)
		if err != nil {
			return "", fmt.Errorf("failed updating raw transaction append log: %w", err)
		}

		_, err = tx.ExecContext(ctx, `
			UPDATE raw_transactions_current
			SET version = ?, status = 'PROCESSED', updated_at = ?
			WHERE id = ?`,
			newVersion, now, rawID,
		)
		if err != nil {
			return "", fmt.Errorf("failed updating raw_transactions_current: %w", err)
		}
	}

	// 2. Validate and insert budget transactions
	var createdBudgetIDs []string
	for _, btx := range input.BudgetTransactions {
		if err := domain.ValidateAmountMinor(btx.SourceAmountMinor, false); err != nil {
			return "", fmt.Errorf("invalid source amount: %w", err)
		}
		if err := domain.ValidateAmountMinor(btx.DestAmountMinor, false); err != nil {
			return "", fmt.Errorf("invalid destination amount: %w", err)
		}

		// Invariant 1: No same-account same-currency self-transfer
		if btx.SourceAccountID == btx.DestinationAccountID && btx.SourceCurrency == btx.DestCurrency {
			return "", errors.New("cannot transfer within the same account with same currency")
		}

		// Invariant 2: At least one side owned by user
		var srcUserOwned, dstUserOwned int
		err := tx.QueryRowContext(ctx, `
			SELECT p.is_user_owned FROM accounts_current a 
			JOIN parties_current p ON a.party_id = p.id 
			WHERE a.id = ?`, btx.SourceAccountID).Scan(&srcUserOwned)
		if err != nil {
			return "", fmt.Errorf("source account %s check failed: %w", btx.SourceAccountID, err)
		}

		err = tx.QueryRowContext(ctx, `
			SELECT p.is_user_owned FROM accounts_current a 
			JOIN parties_current p ON a.party_id = p.id 
			WHERE a.id = ?`, btx.DestinationAccountID).Scan(&dstUserOwned)
		if err != nil {
			return "", fmt.Errorf("dest account %s check failed: %w", btx.DestinationAccountID, err)
		}

		if srcUserOwned == 0 && dstUserOwned == 0 {
			return "", errors.New("at least one account in budget transaction must belong to a user-owned party")
		}

		budgetTxID := domain.NewUUIDv7()
		createdBudgetIDs = append(createdBudgetIDs, budgetTxID)

		txDate := btx.TransactionDate
		if txDate == 0 {
			txDate = now
		}

		// Insert historical version 1
		_, err = tx.ExecContext(ctx, `
			INSERT INTO budget_transactions (
				id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
				destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
				action, agent_id, correlation_id, created_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
			budgetTxID, btx.SourceAccountID, btx.DestinationAccountID, btx.SourceAmountMinor, btx.SourceCurrency,
			btx.DestAmountMinor, btx.DestCurrency, btx.BucketID, btx.TargetID, txDate, btx.Notes,
			input.AgentID, input.CorrelationID, now,
		)
		if err != nil {
			return "", fmt.Errorf("failed inserting budget transaction: %w", err)
		}

		// Insert projection current
		_, err = tx.ExecContext(ctx, `
			INSERT INTO budget_transactions_current (
				id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
				destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
				updated_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			budgetTxID, btx.SourceAccountID, btx.DestinationAccountID, btx.SourceAmountMinor, btx.SourceCurrency,
			btx.DestAmountMinor, btx.DestCurrency, btx.BucketID, btx.TargetID, txDate, btx.Notes, now,
		)
		if err != nil {
			return "", fmt.Errorf("failed inserting budget_transactions_current: %w", err)
		}

		// Update account balances:
		// source balance decrements in source_currency
		// dest balance increments in dest_currency
		if err := adjustAccountBalance(ctx, tx, btx.SourceAccountID, btx.SourceCurrency, -btx.SourceAmountMinor, now); err != nil {
			return "", err
		}
		if err := adjustAccountBalance(ctx, tx, btx.DestinationAccountID, btx.DestCurrency, btx.DestAmountMinor, now); err != nil {
			return "", err
		}

		// Update bucket balance if outflow from user account to external
		if btx.BucketID != nil && srcUserOwned == 1 && dstUserOwned == 0 {
			if err := adjustBucketBalance(ctx, tx, *btx.BucketID, btx.SourceCurrency, -btx.SourceAmountMinor); err != nil {
				return "", err
			}
		}
	}

	// 3. Insert transaction links audit records
	for _, rawID := range input.RawTransactionIDs {
		for _, btxID := range createdBudgetIDs {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO transaction_links (
					operation_id, operation_type, raw_transaction_id, budget_transaction_id,
					agent_id, correlation_id, created_at
				) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				operationID, input.OperationType, rawID, btxID,
				input.AgentID, input.CorrelationID, now,
			)
			if err != nil {
				return "", fmt.Errorf("failed writing transaction_links: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return operationID, nil
}

func adjustAccountBalance(ctx context.Context, tx *sql.Tx, accountID, currency string, deltaMinor, now int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO account_currency_balances (account_id, currency, balance_minor, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(account_id, currency) DO UPDATE SET
			balance_minor = balance_minor + excluded.balance_minor,
			updated_at = excluded.updated_at`,
		accountID, currency, deltaMinor, now,
	)
	return err
}

func adjustBucketBalance(ctx context.Context, tx *sql.Tx, bucketID, currency string, deltaMinor int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor)
		VALUES (?, ?, ?)
		ON CONFLICT(bucket_id, currency) DO UPDATE SET
			balance_minor = balance_minor + excluded.balance_minor`,
		bucketID, currency, deltaMinor,
	)
	return err
}

// UpdateBudgetTxInput payload for editing an existing budget transaction.
type UpdateBudgetTxInput struct {
	ID                   string
	SourceAccountID      string
	DestinationAccountID string
	SourceAmountMinor    int64
	SourceCurrency       string
	DestAmountMinor      int64
	DestCurrency         string
	BucketID             *string
	TargetID             *string
	TransactionDate      int64
	Notes                *string
	AgentID              string
	CorrelationID        string
}

// UpdateBudgetTransaction atomically updates an existing budget transaction:
// 1. Reverts previous balance impacts from account_currency_balances & bucket_currency_balances
// 2. Applies new balance impacts
// 3. Appends new version to budget_transactions
// 4. Updates budget_transactions_current projection
func UpdateBudgetTransaction(ctx context.Context, tenantDB *sql.DB, input UpdateBudgetTxInput) error {
	if err := domain.ValidateAmountMinor(input.SourceAmountMinor, false); err != nil {
		return fmt.Errorf("invalid source amount: %w", err)
	}
	if err := domain.ValidateAmountMinor(input.DestAmountMinor, false); err != nil {
		return fmt.Errorf("invalid destination amount: %w", err)
	}
	if input.SourceAccountID == input.DestinationAccountID && input.SourceCurrency == input.DestCurrency {
		return errors.New("cannot transfer within the same account with same currency")
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()

	// 1. Fetch current transaction record
	var curVersion int
	var oldSrcAcctID, oldDstAcctID string
	var oldSrcAmountMinor, oldDstAmountMinor, oldTxDate int64
	var oldSrcCurrency, oldDstCurrency string
	var oldBucketID, oldTargetID, oldNotes sql.NullString

	err = tx.QueryRowContext(ctx, `
		SELECT version, source_account_id, destination_account_id, source_amount_minor, source_currency,
		       destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes
		FROM budget_transactions_current WHERE id = ?`, input.ID).Scan(
		&curVersion, &oldSrcAcctID, &oldDstAcctID, &oldSrcAmountMinor, &oldSrcCurrency,
		&oldDstAmountMinor, &oldDstCurrency, &oldBucketID, &oldTargetID, &oldTxDate, &oldNotes,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("budget transaction %s not found", input.ID)
		}
		return err
	}

	// Revert old account balances
	var oldSrcUserOwned, oldDstUserOwned int
	tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, oldSrcAcctID).Scan(&oldSrcUserOwned)
	tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, oldDstAcctID).Scan(&oldDstUserOwned)

	if err := adjustAccountBalance(ctx, tx, oldSrcAcctID, oldSrcCurrency, oldSrcAmountMinor, now); err != nil {
		return err
	}
	if err := adjustAccountBalance(ctx, tx, oldDstAcctID, oldDstCurrency, -oldDstAmountMinor, now); err != nil {
		return err
	}
	if oldBucketID.Valid && oldBucketID.String != "" && oldSrcUserOwned == 1 && oldDstUserOwned == 0 {
		if err := adjustBucketBalance(ctx, tx, oldBucketID.String, oldSrcCurrency, oldSrcAmountMinor); err != nil {
			return err
		}
	}

	// Check new accounts ownership
	var newSrcUserOwned, newDstUserOwned int
	err = tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, input.SourceAccountID).Scan(&newSrcUserOwned)
	if err != nil {
		return fmt.Errorf("source account check failed: %w", err)
	}
	err = tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, input.DestinationAccountID).Scan(&newDstUserOwned)
	if err != nil {
		return fmt.Errorf("dest account check failed: %w", err)
	}
	if newSrcUserOwned == 0 && newDstUserOwned == 0 {
		return errors.New("at least one account in budget transaction must belong to a user-owned party")
	}

	// Apply new balances
	if err := adjustAccountBalance(ctx, tx, input.SourceAccountID, input.SourceCurrency, -input.SourceAmountMinor, now); err != nil {
		return err
	}
	if err := adjustAccountBalance(ctx, tx, input.DestinationAccountID, input.DestCurrency, input.DestAmountMinor, now); err != nil {
		return err
	}
	if input.BucketID != nil && *input.BucketID != "" && newSrcUserOwned == 1 && newDstUserOwned == 0 {
		if err := adjustBucketBalance(ctx, tx, *input.BucketID, input.SourceCurrency, -input.SourceAmountMinor); err != nil {
			return err
		}
	}

	// Append new version
	newVersion := curVersion + 1
	txDate := input.TransactionDate
	if txDate == 0 {
		txDate = oldTxDate
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO budget_transactions (
			id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
			destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
			action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		input.ID, newVersion, input.SourceAccountID, input.DestinationAccountID, input.SourceAmountMinor, input.SourceCurrency,
		input.DestAmountMinor, input.DestCurrency, input.BucketID, input.TargetID, txDate, input.Notes,
		input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed appending budget_transactions update: %w", err)
	}

	// Update current projection
	_, err = tx.ExecContext(ctx, `
		UPDATE budget_transactions_current
		SET version = ?, source_account_id = ?, destination_account_id = ?,
		    source_amount_minor = ?, source_currency = ?, destination_amount_minor = ?, destination_currency = ?,
		    bucket_id = ?, target_id = ?, transaction_date = ?, notes = ?, updated_at = ?
		WHERE id = ?`,
		newVersion, input.SourceAccountID, input.DestinationAccountID,
		input.SourceAmountMinor, input.SourceCurrency, input.DestAmountMinor, input.DestCurrency,
		input.BucketID, input.TargetID, txDate, input.Notes, now,
		input.ID,
	)
	if err != nil {
		return fmt.Errorf("failed updating budget_transactions_current: %w", err)
	}

	return tx.Commit()
}

// DeleteBudgetTransaction deletes an existing budget transaction by:
// 1. Reverting its balance effects on account_currency_balances & bucket_currency_balances
// 2. Appending SOFT_DELETE record to budget_transactions
// 3. Removing from budget_transactions_current projection
func DeleteBudgetTransaction(ctx context.Context, tenantDB *sql.DB, txID, agentID, correlationID string) error {
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()

	var curVersion int
	var srcAcctID, dstAcctID, srcCurrency, dstCurrency string
	var srcAmountMinor, dstAmountMinor, txDate int64
	var bucketID, targetID, notes sql.NullString

	err = tx.QueryRowContext(ctx, `
		SELECT version, source_account_id, destination_account_id, source_amount_minor, source_currency,
		       destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes
		FROM budget_transactions_current WHERE id = ?`, txID).Scan(
		&curVersion, &srcAcctID, &dstAcctID, &srcAmountMinor, &srcCurrency,
		&dstAmountMinor, &dstCurrency, &bucketID, &targetID, &txDate, &notes,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("budget transaction %s not found", txID)
		}
		return err
	}

	// Revert account & bucket balances
	var srcUserOwned, dstUserOwned int
	tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, srcAcctID).Scan(&srcUserOwned)
	tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, dstAcctID).Scan(&dstUserOwned)

	if err := adjustAccountBalance(ctx, tx, srcAcctID, srcCurrency, srcAmountMinor, now); err != nil {
		return err
	}
	if err := adjustAccountBalance(ctx, tx, dstAcctID, dstCurrency, -dstAmountMinor, now); err != nil {
		return err
	}
	if bucketID.Valid && bucketID.String != "" && srcUserOwned == 1 && dstUserOwned == 0 {
		if err := adjustBucketBalance(ctx, tx, bucketID.String, srcCurrency, srcAmountMinor); err != nil {
			return err
		}
	}

	// Append SOFT_DELETE version
	newVersion := curVersion + 1
	var bID, tID, n *string
	if bucketID.Valid {
		bID = &bucketID.String
	}
	if targetID.Valid {
		tID = &targetID.String
	}
	if notes.Valid {
		n = &notes.String
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO budget_transactions (
			id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
			destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
			action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'SOFT_DELETE', ?, ?, ?)`,
		txID, newVersion, srcAcctID, dstAcctID, srcAmountMinor, srcCurrency,
		dstAmountMinor, dstCurrency, bID, tID, txDate, n,
		agentID, correlationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed appending soft delete to budget_transactions: %w", err)
	}

	// Remove from current projection
	_, err = tx.ExecContext(ctx, `DELETE FROM budget_transactions_current WHERE id = ?`, txID)
	if err != nil {
		return fmt.Errorf("failed deleting from budget_transactions_current: %w", err)
	}

	return tx.Commit()
}

