package automation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/lua"
	"github.com/balance/balance/internal/transaction"
)

var (
	ErrAutomationNotFound = errors.New("automation not found")
	ErrInvalidScript      = errors.New("invalid lua automation script")
)

// Automation represents a user-defined automation rule.
type Automation struct {
	ID             string    `json:"id"`
	Version        int       `json:"version"`
	Name           string    `json:"name"`
	JudgeScript    string    `json:"judge_script"`
	ExecutorScript string    `json:"executor_script"`
	Comment        string    `json:"comment"`
	IsActive       bool      `json:"is_active"`
	SortOrder      int       `json:"sort_order"`
	AgentID        string    `json:"agent_id"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateAutomationInput defines the payload for creating a new automation.
type CreateAutomationInput struct {
	Name           string `json:"name"`
	JudgeScript    string `json:"judge_script"`
	ExecutorScript string `json:"executor_script"`
	Comment        string `json:"comment"`
	IsActive       *bool  `json:"is_active,omitempty"`
	AgentID        string `json:"agent_id"`
	CorrelationID  string `json:"correlation_id"`
}

// UpdateAutomationInput defines the payload for updating an existing automation.
type UpdateAutomationInput struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	JudgeScript    string `json:"judge_script"`
	ExecutorScript string `json:"executor_script"`
	Comment        string `json:"comment"`
	Squash         bool   `json:"squash"`
	AgentID        string `json:"agent_id"`
	CorrelationID  string `json:"correlation_id"`
}

// MultiSquashInput defines the input parameters for squashing a continuous range of versions.
type MultiSquashInput struct {
	ID            string `json:"id"`
	StartVersion  int    `json:"start_version"`
	EndVersion    int    `json:"end_version"`
	Comment       string `json:"comment"`
	AgentID       string `json:"agent_id"`
	CorrelationID string `json:"correlation_id"`
}

// TxChange represents the diff of a transaction before and after automations.
type TxChange struct {
	TxID            string `json:"tx_id"`
	OriginalNotes   string `json:"original_notes"`
	NewNotes        string `json:"new_notes"`
	OriginalBucket  string `json:"original_bucket"`
	NewBucket       string `json:"new_bucket"`
	OriginalTarget  string `json:"original_target"`
	NewTarget       string `json:"new_target"`
	OriginalAmount  int64  `json:"original_amount"`
	NewAmount       int64  `json:"new_amount"`
	Action          string `json:"action"` // "MODIFIED", "SPLIT", "DELETED", "NO_CHANGE"
	SplitsCount     int    `json:"splits_count"`
}

// BatchExecutionResult summarizes the outcome of running automations.
type BatchExecutionResult struct {
	ProcessedCount int        `json:"processed_count"`
	ModifiedCount  int        `json:"modified_count"`
	DeletedCount   int        `json:"deleted_count"`
	SplitCount     int        `json:"split_count"`
	Changes        []TxChange `json:"changes"`
}

// ListAutomations returns all automations sorted by sort_order ASC, updated_at DESC.
func ListAutomations(ctx context.Context, tenantDB *sql.DB) ([]Automation, error) {
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, version, name, judge_script, executor_script, comment, is_active, sort_order, agent_id, updated_at
		FROM automations_current
		ORDER BY sort_order ASC, updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query automations_current: %w", err)
	}
	defer rows.Close()

	var list []Automation
	for rows.Next() {
		var a Automation
		var isActiveInt int
		var updatedMicros int64
		if err := rows.Scan(
			&a.ID, &a.Version, &a.Name, &a.JudgeScript, &a.ExecutorScript, &a.Comment,
			&isActiveInt, &a.SortOrder, &a.AgentID, &updatedMicros,
		); err != nil {
			return nil, fmt.Errorf("scan automation: %w", err)
		}
		a.IsActive = isActiveInt == 1
		a.UpdatedAt = time.UnixMicro(updatedMicros).UTC()
		list = append(list, a)
	}
	return list, nil
}

// GetAutomation fetches a single automation by ID.
func GetAutomation(ctx context.Context, tenantDB *sql.DB, id string) (*Automation, error) {
	var a Automation
	var isActiveInt int
	var updatedMicros int64
	err := tenantDB.QueryRowContext(ctx, `
		SELECT id, version, name, judge_script, executor_script, comment, is_active, sort_order, agent_id, updated_at
		FROM automations_current
		WHERE id = ?`, id,
	).Scan(
		&a.ID, &a.Version, &a.Name, &a.JudgeScript, &a.ExecutorScript, &a.Comment,
		&isActiveInt, &a.SortOrder, &a.AgentID, &updatedMicros,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAutomationNotFound
		}
		return nil, fmt.Errorf("query automation: %w", err)
	}
	a.IsActive = isActiveInt == 1
	a.UpdatedAt = time.UnixMicro(updatedMicros).UTC()
	return &a, nil
}

// CreateAutomation creates a new automation and appends version 1.
func CreateAutomation(ctx context.Context, tenantDB *sql.DB, input CreateAutomationInput) (*Automation, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, errors.New("automation name cannot be empty")
	}
	if err := lua.ValidateJudgeScript(input.JudgeScript); err != nil {
		return nil, fmt.Errorf("%w: judge script invalid: %s", ErrInvalidScript, err.Error())
	}
	if err := lua.ValidateLuaScript(input.ExecutorScript); err != nil {
		return nil, fmt.Errorf("%w: executor script invalid: %s", ErrInvalidScript, err.Error())
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	id := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	isActive := 1
	if input.IsActive != nil && !*input.IsActive {
		isActive = 0
	}

	var maxSort int
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) FROM automations_current`).Scan(&maxSort)
	sortOrder := maxSort + 1

	// Insert into append-only table
	_, err = tx.ExecContext(ctx, `
		INSERT INTO automations (
			id, version, name, judge_script, executor_script, comment, action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
		id, input.Name, input.JudgeScript, input.ExecutorScript, input.Comment,
		input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert into automations: %w", err)
	}

	// Insert into projection table
	_, err = tx.ExecContext(ctx, `
		INSERT INTO automations_current (
			id, version, name, judge_script, executor_script, comment, is_active, sort_order, agent_id, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, input.Name, input.JudgeScript, input.ExecutorScript, input.Comment,
		isActive, sortOrder, input.AgentID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert into automations_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Automation{
		ID:             id,
		Version:        1,
		Name:           input.Name,
		JudgeScript:    input.JudgeScript,
		ExecutorScript: input.ExecutorScript,
		Comment:        input.Comment,
		IsActive:       isActive == 1,
		SortOrder:      sortOrder,
		AgentID:        input.AgentID,
		UpdatedAt:      time.UnixMicro(now).UTC(),
	}, nil
}

// UpdateAutomation updates an existing automation, creating version N+1 or squashing N-1.
func UpdateAutomation(ctx context.Context, tenantDB *sql.DB, input UpdateAutomationInput) (*Automation, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, errors.New("automation name cannot be empty")
	}
	if err := lua.ValidateJudgeScript(input.JudgeScript); err != nil {
		return nil, fmt.Errorf("%w: judge script invalid: %s", ErrInvalidScript, err.Error())
	}
	if err := lua.ValidateLuaScript(input.ExecutorScript); err != nil {
		return nil, fmt.Errorf("%w: executor script invalid: %s", ErrInvalidScript, err.Error())
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var curVersion, isActive, sortOrder int
	err = tx.QueryRowContext(ctx, `
		SELECT version, is_active, sort_order FROM automations_current WHERE id = ?`, input.ID,
	).Scan(&curVersion, &isActive, &sortOrder)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAutomationNotFound
		}
		return nil, err
	}

	now := time.Now().UTC().UnixMicro()
	targetVersion := curVersion + 1

	// Squash handling:
	// Decision: "Zastąpienie poprzednika: zaznaczenie „Squash” usuwa wersję N-1 i zapisuje zmiany jako wersję N (redukując liczbę wersji o 1)."
	// "Dla wersji 1 checkbox „Squash” jest zablokowany/ukryty przy wersji 1 (brak poprzednika N-1 do zastąpienia)."
	if input.Squash && curVersion > 1 {
		// Delete version N-1 from append-only table
		_, err = tx.ExecContext(ctx, `DELETE FROM automations WHERE id = ? AND version = ?`, input.ID, curVersion-1)
		if err != nil {
			return nil, fmt.Errorf("delete predecessor version %d for squash: %w", curVersion-1, err)
		}
		// Target version becomes current version
		targetVersion = curVersion
		// Delete the existing current version row so we replace it with updated content
		_, err = tx.ExecContext(ctx, `DELETE FROM automations WHERE id = ? AND version = ?`, input.ID, curVersion)
		if err != nil {
			return nil, fmt.Errorf("replace version %d for squash: %w", curVersion, err)
		}
	}

	// Insert into append-only table
	_, err = tx.ExecContext(ctx, `
		INSERT INTO automations (
			id, version, name, judge_script, executor_script, comment, action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		input.ID, targetVersion, input.Name, input.JudgeScript, input.ExecutorScript, input.Comment,
		input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert version into automations: %w", err)
	}

	// Update projection table
	_, err = tx.ExecContext(ctx, `
		UPDATE automations_current
		SET version = ?, name = ?, judge_script = ?, executor_script = ?, comment = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`,
		targetVersion, input.Name, input.JudgeScript, input.ExecutorScript, input.Comment,
		input.AgentID, now, input.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update automations_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Automation{
		ID:             input.ID,
		Version:        targetVersion,
		Name:           input.Name,
		JudgeScript:    input.JudgeScript,
		ExecutorScript: input.ExecutorScript,
		Comment:        input.Comment,
		IsActive:       isActive == 1,
		SortOrder:      sortOrder,
		AgentID:        input.AgentID,
		UpdatedAt:      time.UnixMicro(now).UTC(),
	}, nil
}

// SquashAutomationVersions squashes a contiguous range of versions (startVersion..endVersion).
// Versions from startVersion up to endVersion - 1 are deleted from append-only table.
// The endVersion row retains its judge and executor scripts, but its comment is updated,
// action is set to 'SQUASH', and automations_current is kept in sync if endVersion was current.
func SquashAutomationVersions(ctx context.Context, tenantDB *sql.DB, input MultiSquashInput) (*Automation, error) {
	if !domain.IsValidUUIDv7(input.ID) {
		return nil, fmt.Errorf("invalid automation id: %s", input.ID)
	}
	if input.StartVersion <= 0 || input.EndVersion <= input.StartVersion {
		return nil, fmt.Errorf("invalid version range: start (%d) must be > 0 and < end (%d)", input.StartVersion, input.EndVersion)
	}
	if input.CorrelationID == "" {
		input.CorrelationID = domain.NewUUIDv7()
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. Verify all versions in range exist and are contiguous
	rows, err := tx.QueryContext(ctx, `
		SELECT version FROM automations WHERE id = ? AND version BETWEEN ? AND ? ORDER BY version ASC`,
		input.ID, input.StartVersion, input.EndVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("query version range: %w", err)
	}
	defer rows.Close()

	var existingVersions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		existingVersions = append(existingVersions, v)
	}

	expectedCount := input.EndVersion - input.StartVersion + 1
	if len(existingVersions) != expectedCount {
		return nil, fmt.Errorf("version range is not contiguous or some versions are missing: expected %d versions, found %d", expectedCount, len(existingVersions))
	}

	for i, v := range existingVersions {
		if v != input.StartVersion+i {
			return nil, fmt.Errorf("non-contiguous version sequence found in range")
		}
	}

	// 2. Fetch endVersion record details
	var endName, endJudge, endExec string
	var endCreatedAt int64
	err = tx.QueryRowContext(ctx, `
		SELECT name, judge_script, executor_script, created_at
		FROM automations WHERE id = ? AND version = ?`, input.ID, input.EndVersion,
	).Scan(&endName, &endJudge, &endExec, &endCreatedAt)
	if err != nil {
		return nil, fmt.Errorf("query target version %d: %w", input.EndVersion, err)
	}

	now := time.Now().UTC().UnixMicro()

	// 3. Delete intermediate versions (startVersion .. endVersion - 1)
	_, err = tx.ExecContext(ctx, `
		DELETE FROM automations WHERE id = ? AND version >= ? AND version < ?`,
		input.ID, input.StartVersion, input.EndVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("delete intermediate versions for squash: %w", err)
	}

	// 4. Update endVersion record in automations
	_, err = tx.ExecContext(ctx, `
		UPDATE automations
		SET comment = ?, action = 'SQUASH', agent_id = ?, correlation_id = ?
		WHERE id = ? AND version = ?`,
		input.Comment, input.AgentID, input.CorrelationID, input.ID, input.EndVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("update target version for squash: %w", err)
	}

	// 5. Update automations_current if endVersion is current version
	var curVersion int
	var isActive, sortOrder int
	err = tx.QueryRowContext(ctx, `
		SELECT version, is_active, sort_order FROM automations_current WHERE id = ?`, input.ID,
	).Scan(&curVersion, &isActive, &sortOrder)
	if err == nil && curVersion == input.EndVersion {
		_, err = tx.ExecContext(ctx, `
			UPDATE automations_current
			SET comment = ?, updated_at = ?
			WHERE id = ?`, input.Comment, now, input.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("update automations_current: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Automation{
		ID:             input.ID,
		Version:        input.EndVersion,
		Name:           endName,
		JudgeScript:    endJudge,
		ExecutorScript: endExec,
		Comment:        input.Comment,
		IsActive:       isActive == 1,
		SortOrder:      sortOrder,
		AgentID:        input.AgentID,
		UpdatedAt:      time.UnixMicro(now).UTC(),
	}, nil
}

// GetAutomationVersion retrieves a specific version from the append-only table.
func GetAutomationVersion(ctx context.Context, tenantDB *sql.DB, id string, version int) (*Automation, error) {
	var a Automation
	var createdMicros int64
	err := tenantDB.QueryRowContext(ctx, `
		SELECT id, version, name, judge_script, executor_script, comment, agent_id, created_at
		FROM automations
		WHERE id = ? AND version = ?`, id, version,
	).Scan(
		&a.ID, &a.Version, &a.Name, &a.JudgeScript, &a.ExecutorScript, &a.Comment,
		&a.AgentID, &createdMicros,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAutomationNotFound
		}
		return nil, fmt.Errorf("query automation version: %w", err)
	}
	a.UpdatedAt = time.UnixMicro(createdMicros).UTC()
	return &a, nil
}

// DeleteAutomation deletes an automation by adding a SOFT_DELETE entry and removing from projection.
func DeleteAutomation(ctx context.Context, tenantDB *sql.DB, id, agentID, correlationID string) error {
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var curVersion int
	var name, judgeScript, executorScript, comment string
	err = tx.QueryRowContext(ctx, `
		SELECT version, name, judge_script, executor_script, comment FROM automations_current WHERE id = ?`, id,
	).Scan(&curVersion, &name, &judgeScript, &executorScript, &comment)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAutomationNotFound
		}
		return err
	}

	now := time.Now().UTC().UnixMicro()
	newVersion := curVersion + 1

	// Insert soft delete into append log
	_, err = tx.ExecContext(ctx, `
		INSERT INTO automations (
			id, version, name, judge_script, executor_script, comment, action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'SOFT_DELETE', ?, ?, ?)`,
		id, newVersion, name, judgeScript, executorScript, comment, agentID, correlationID, now,
	)
	if err != nil {
		return fmt.Errorf("insert soft delete into automations: %w", err)
	}

	// Remove from projection
	_, err = tx.ExecContext(ctx, `DELETE FROM automations_current WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete from automations_current: %w", err)
	}

	return tx.Commit()
}

// ToggleAutomation toggles the is_active status of an automation directly in projection.
func ToggleAutomation(ctx context.Context, tenantDB *sql.DB, id string) (bool, error) {
	now := time.Now().UTC().UnixMicro()
	res, err := tenantDB.ExecContext(ctx, `
		UPDATE automations_current
		SET is_active = 1 - is_active, updated_at = ?
		WHERE id = ?`, now, id,
	)
	if err != nil {
		return false, fmt.Errorf("toggle is_active: %w", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return false, ErrAutomationNotFound
	}

	var isActive int
	_ = tenantDB.QueryRowContext(ctx, `SELECT is_active FROM automations_current WHERE id = ?`, id).Scan(&isActive)
	return isActive == 1, nil
}

// ReorderAutomations updates sort_order for a given ordered slice of automation IDs.
func ReorderAutomations(ctx context.Context, tenantDB *sql.DB, orderedIDs []string) error {
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()
	for i, id := range orderedIDs {
		_, err := tx.ExecContext(ctx, `
			UPDATE automations_current
			SET sort_order = ?, updated_at = ?
			WHERE id = ?`, i+1, now, id,
		)
		if err != nil {
			return fmt.Errorf("update sort order for %s: %w", id, err)
		}
	}
	return tx.Commit()
}

// MoveAutomation moves an automation up or down in sort_order.
func MoveAutomation(ctx context.Context, tenantDB *sql.DB, id, direction string) error {
	automations, err := ListAutomations(ctx, tenantDB)
	if err != nil {
		return err
	}
	idx := -1
	for i, a := range automations {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrAutomationNotFound
	}

	var targetIdx int
	if direction == "up" {
		if idx == 0 {
			return nil // already at top
		}
		targetIdx = idx - 1
	} else if direction == "down" {
		if idx == len(automations)-1 {
			return nil // already at bottom
		}
		targetIdx = idx + 1
	} else {
		return fmt.Errorf("invalid direction: %s", direction)
	}

	// Swap
	automations[idx], automations[targetIdx] = automations[targetIdx], automations[idx]
	var orderedIDs []string
	for _, a := range automations {
		orderedIDs = append(orderedIDs, a.ID)
	}
	return ReorderAutomations(ctx, tenantDB, orderedIDs)
}

func init() {
	transaction.GlobalAutomationHook = ApplyAutomationsInTx
}

// DBTX encapsulates shared DB / Tx query methods.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ApplyAutomationsInTx executes the active automations pipeline on newly created budget transactions
// directly inside the active database transaction. If any automation fails, returning an error causes
// the outer ExecuteResolution transaction to be rolled back.
func ApplyAutomationsInTx(ctx context.Context, tx *sql.Tx, createdTxIDs []string, agentID, correlationID string) error {
	var tableExists int
	_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='automations_current'`).Scan(&tableExists)
	if tableExists == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, judge_script, executor_script
		FROM automations_current
		WHERE is_active = 1
		ORDER BY sort_order ASC, updated_at DESC
	`)
	if err != nil {
		return fmt.Errorf("query active automations in tx: %w", err)
	}
	defer rows.Close()

	var activeAutomations []Automation
	for rows.Next() {
		var a Automation
		if err := rows.Scan(&a.ID, &a.Name, &a.JudgeScript, &a.ExecutorScript); err != nil {
			return fmt.Errorf("scan active automation in tx: %w", err)
		}
		activeAutomations = append(activeAutomations, a)
	}
	if len(activeAutomations) == 0 {
		return nil
	}

	snap, err := loadSnapshotWithDBTX(ctx, tx)
	if err != nil {
		return fmt.Errorf("load snapshot in tx: %w", err)
	}

	now := time.Now().UTC().UnixMicro()

	for _, txID := range createdTxIDs {
		txCtx, err := convertBudgetTxWithDBTX(ctx, tx, txID, snap)
		if err != nil {
			return err
		}

		currentTx := *txCtx
		for _, a := range activeAutomations {
			matched, err := lua.EvaluateAutomationJudge(ctx, a.JudgeScript, currentTx, snap)
			if err != nil {
				return fmt.Errorf("automation %q [%s] judge failed: %w", a.Name, a.ID, err)
			}
			if !matched {
				continue
			}

			outcome, err := lua.EvaluateAutomationExecutor(ctx, a.ExecutorScript, currentTx, snap)
			if err != nil {
				return fmt.Errorf("automation %q [%s] executor failed: %w", a.Name, a.ID, err)
			}

			if outcome.Deleted {
				// Revert balances
				if err := transaction.AdjustAccountBalance(ctx, tx, currentTx.SourceAccountID, currentTx.SourceCurrency, currentTx.SourceAmountMinor, now); err != nil {
					return err
				}
				if err := transaction.AdjustAccountBalance(ctx, tx, currentTx.DestinationAccountID, currentTx.DestCurrency, -currentTx.DestAmountMinor, now); err != nil {
					return err
				}
				if currentTx.BucketID != nil && currentTx.IsUserOwnedSource && !currentTx.IsUserOwnedDest {
					if err := transaction.AdjustBucketBalance(ctx, tx, *currentTx.BucketID, currentTx.SourceCurrency, currentTx.SourceAmountMinor); err != nil {
						return err
					}
				}

				// Append SOFT_DELETE version 2
				_, err = tx.ExecContext(ctx, `
					INSERT INTO budget_transactions (
						id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
						destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
						action, agent_id, correlation_id, created_at
					) VALUES (?, 2, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'SOFT_DELETE', ?, ?, ?)`,
					currentTx.ID, currentTx.SourceAccountID, currentTx.DestinationAccountID,
					currentTx.SourceAmountMinor, currentTx.SourceCurrency, currentTx.DestAmountMinor, currentTx.DestCurrency,
					currentTx.BucketID, currentTx.TargetID, currentTx.TransactionDate, currentTx.Notes,
					agentID, correlationID, now,
				)
				if err != nil {
					return fmt.Errorf("insert soft delete in tx: %w", err)
				}
				_, err = tx.ExecContext(ctx, `DELETE FROM budget_transactions_current WHERE id = ?`, currentTx.ID)
				if err != nil {
					return fmt.Errorf("delete from budget_transactions_current in tx: %w", err)
				}
				break
			}

			if len(outcome.Splits) > 0 {
				splits := outcome.Splits
				// 1. Update part 0 in-place
				// Revert old bucket balance if applicable
				if currentTx.BucketID != nil && currentTx.IsUserOwnedSource && !currentTx.IsUserOwnedDest {
					_ = transaction.AdjustBucketBalance(ctx, tx, *currentTx.BucketID, currentTx.SourceCurrency, currentTx.SourceAmountMinor)
				}
				// Revert old dest account balance difference
				destDiff := splits[0].AmountMinor - currentTx.DestAmountMinor
				srcDiff := splits[0].AmountMinor - currentTx.SourceAmountMinor
				if err := transaction.AdjustAccountBalance(ctx, tx, currentTx.SourceAccountID, currentTx.SourceCurrency, -srcDiff, now); err != nil {
					return err
				}
				if err := transaction.AdjustAccountBalance(ctx, tx, currentTx.DestinationAccountID, currentTx.DestCurrency, destDiff, now); err != nil {
					return err
				}
				if splits[0].BucketID != nil && currentTx.IsUserOwnedSource && !currentTx.IsUserOwnedDest {
					if err := transaction.AdjustBucketBalance(ctx, tx, *splits[0].BucketID, currentTx.SourceCurrency, -splits[0].AmountMinor); err != nil {
						return err
					}
				}

				// Update current projection for part 0
				_, err = tx.ExecContext(ctx, `
					UPDATE budget_transactions_current
					SET source_amount_minor = ?, destination_amount_minor = ?, bucket_id = ?, target_id = ?, notes = ?, updated_at = ?
					WHERE id = ?`,
					splits[0].AmountMinor, splits[0].AmountMinor, splits[0].BucketID, splits[0].TargetID, splits[0].Notes, now, currentTx.ID,
				)
				if err != nil {
					return fmt.Errorf("update split part 1 in tx: %w", err)
				}
				_, err = tx.ExecContext(ctx, `
					INSERT INTO budget_transactions (
						id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
						destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
						action, agent_id, correlation_id, created_at
					) VALUES (?, 2, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
					currentTx.ID, currentTx.SourceAccountID, currentTx.DestinationAccountID,
					splits[0].AmountMinor, currentTx.SourceCurrency, splits[0].AmountMinor, currentTx.DestCurrency,
					splits[0].BucketID, splits[0].TargetID, currentTx.TransactionDate, splits[0].Notes,
					agentID, correlationID, now,
				)
				if err != nil {
					return fmt.Errorf("append split part 1 in tx: %w", err)
				}

				// 2. Insert parts 1..N
				for i := 1; i < len(splits); i++ {
					sp := splits[i]
					subID := domain.NewUUIDv7()
					_, err = tx.ExecContext(ctx, `
						INSERT INTO budget_transactions (
							id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
							destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
							action, agent_id, correlation_id, created_at
						) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
						subID, currentTx.SourceAccountID, currentTx.DestinationAccountID,
						sp.AmountMinor, currentTx.SourceCurrency, sp.AmountMinor, currentTx.DestCurrency,
						sp.BucketID, sp.TargetID, currentTx.TransactionDate, sp.Notes,
						agentID, correlationID, now,
					)
					if err != nil {
						return fmt.Errorf("insert split sub-tx in tx: %w", err)
					}
					_, err = tx.ExecContext(ctx, `
						INSERT INTO budget_transactions_current (
							id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
							destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes, updated_at
						) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						subID, 1, currentTx.SourceAccountID, currentTx.DestinationAccountID,
						sp.AmountMinor, currentTx.SourceCurrency, sp.AmountMinor, currentTx.DestCurrency,
						sp.BucketID, sp.TargetID, currentTx.TransactionDate, sp.Notes, now,
					)
					if err != nil {
						return fmt.Errorf("insert split sub-tx current in tx: %w", err)
					}
					if err := transaction.AdjustAccountBalance(ctx, tx, currentTx.SourceAccountID, currentTx.SourceCurrency, -sp.AmountMinor, now); err != nil {
						return err
					}
					if err := transaction.AdjustAccountBalance(ctx, tx, currentTx.DestinationAccountID, currentTx.DestCurrency, sp.AmountMinor, now); err != nil {
						return err
					}
					if sp.BucketID != nil && currentTx.IsUserOwnedSource && !currentTx.IsUserOwnedDest {
						if err := transaction.AdjustBucketBalance(ctx, tx, *sp.BucketID, currentTx.SourceCurrency, -sp.AmountMinor); err != nil {
							return err
						}
					}
				}
				break
			}

			if outcome.Modified {
				upTx := outcome.UpdatedTx
				// Check if bucket balance adjustment is needed
				if (currentTx.BucketID != upTx.BucketID) && currentTx.IsUserOwnedSource && !currentTx.IsUserOwnedDest {
					if currentTx.BucketID != nil {
						_ = transaction.AdjustBucketBalance(ctx, tx, *currentTx.BucketID, currentTx.SourceCurrency, currentTx.SourceAmountMinor)
					}
					if upTx.BucketID != nil {
						_ = transaction.AdjustBucketBalance(ctx, tx, *upTx.BucketID, upTx.SourceCurrency, -upTx.SourceAmountMinor)
					}
				}

				var curVer int
				_ = tx.QueryRowContext(ctx, `SELECT version FROM budget_transactions_current WHERE id = ?`, currentTx.ID).Scan(&curVer)
				newVer := curVer + 1

				_, err = tx.ExecContext(ctx, `
					INSERT INTO budget_transactions (
						id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
						destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
						action, agent_id, correlation_id, created_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
					currentTx.ID, newVer, upTx.SourceAccountID, upTx.DestinationAccountID,
					upTx.SourceAmountMinor, upTx.SourceCurrency, upTx.DestAmountMinor, upTx.DestCurrency,
					upTx.BucketID, upTx.TargetID, upTx.TransactionDate, upTx.Notes,
					agentID, correlationID, now,
				)
				if err != nil {
					return fmt.Errorf("append update in tx: %w", err)
				}

				_, err = tx.ExecContext(ctx, `
					UPDATE budget_transactions_current
					SET version = ?, source_account_id = ?, destination_account_id = ?,
					    source_amount_minor = ?, source_currency = ?, destination_amount_minor = ?, destination_currency = ?,
					    bucket_id = ?, target_id = ?, transaction_date = ?, notes = ?, updated_at = ?
					WHERE id = ?`,
					newVer, upTx.SourceAccountID, upTx.DestinationAccountID,
					upTx.SourceAmountMinor, upTx.SourceCurrency, upTx.DestAmountMinor, upTx.DestCurrency,
					upTx.BucketID, upTx.TargetID, upTx.TransactionDate, upTx.Notes, now,
					currentTx.ID,
				)
				if err != nil {
					return fmt.Errorf("update current in tx: %w", err)
				}

				currentTx = upTx
			}
		}
	}

	return nil
}

func loadSnapshotWithDBTX(ctx context.Context, dbtx DBTX) (*lua.InMemoryBudgetSnapshot, error) {
	snap := &lua.InMemoryBudgetSnapshot{
		Targets:        make(map[string]*lua.TargetMeta),
		Buckets:        make(map[string]*lua.BucketMeta),
		Accounts:       make(map[string]*lua.AccountMeta),
		Parties:        make(map[string]*lua.PartyMeta),
		BucketBalances: make(map[string]int64),
		FXRates:        make(map[string]int64),
	}

	bRows, err := dbtx.QueryContext(ctx, `SELECT id, name FROM buckets_projection WHERE is_archived = 0`)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var b lua.BucketMeta
			if err := bRows.Scan(&b.ID, &b.Name); err == nil {
				snap.Buckets[b.ID] = &b
				snap.Buckets[strings.ToLower(b.Name)] = &b
			}
		}
	}

	tRows, err := dbtx.QueryContext(ctx, `SELECT id, bucket_id, name, amount_minor, priority, currency FROM targets_projection`)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var t lua.TargetMeta
			var bucketID sql.NullString
			if err := tRows.Scan(&t.ID, &bucketID, &t.Name, &t.Required, &t.Priority, &t.Currency); err == nil {
				if bucketID.Valid {
					t.BucketID = bucketID.String
				}
				snap.Targets[t.ID] = &t
				snap.Targets[strings.ToLower(t.Name)] = &t
			}
		}
	}

	pRows, err := dbtx.QueryContext(ctx, `SELECT id, name, is_user_owned FROM parties_current`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var p lua.PartyMeta
			var isUserOwnedInt int
			if err := pRows.Scan(&p.ID, &p.Name, &isUserOwnedInt); err == nil {
				p.IsUserOwned = isUserOwnedInt == 1
				snap.Parties[p.ID] = &p
				snap.Parties[strings.ToLower(p.Name)] = &p
			}
		}
	}

	aRows, err := dbtx.QueryContext(ctx, `
		SELECT a.id, a.name, a.party_id, a.primary_currency, p.is_user_owned
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
	`)
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var a lua.AccountMeta
			var isUserOwnedInt int
			if err := aRows.Scan(&a.ID, &a.Name, &a.PartyID, &a.Currency, &isUserOwnedInt); err == nil {
				a.IsUserOwned = isUserOwnedInt == 1
				snap.Accounts[a.ID] = &a
				snap.Accounts[strings.ToLower(a.Name)] = &a
			}
		}
	}

	return snap, nil
}

func convertBudgetTxWithDBTX(ctx context.Context, dbtx DBTX, txID string, snap *lua.InMemoryBudgetSnapshot) (*lua.AutomationTxContext, error) {
	var txCtx lua.AutomationTxContext
	var bucketID, targetID, notes sql.NullString

	err := dbtx.QueryRowContext(ctx, `
		SELECT id, source_account_id, destination_account_id, source_amount_minor, source_currency,
		       destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes
		FROM budget_transactions_current WHERE id = ?`, txID,
	).Scan(
		&txCtx.ID, &txCtx.SourceAccountID, &txCtx.DestinationAccountID, &txCtx.SourceAmountMinor, &txCtx.SourceCurrency,
		&txCtx.DestAmountMinor, &txCtx.DestCurrency, &bucketID, &targetID, &txCtx.TransactionDate, &notes,
	)
	if err != nil {
		return nil, fmt.Errorf("read transaction %s: %w", txID, err)
	}

	if bucketID.Valid && bucketID.String != "" {
		txCtx.BucketID = &bucketID.String
		if snap != nil && snap.Buckets != nil {
			if b := snap.Buckets[bucketID.String]; b != nil {
				txCtx.BucketName = b.Name
			}
		}
	}
	if targetID.Valid && targetID.String != "" {
		txCtx.TargetID = &targetID.String
		if snap != nil && snap.Targets != nil {
			if t := snap.Targets[targetID.String]; t != nil {
				txCtx.TargetName = t.Name
			}
		}
	}
	if notes.Valid {
		txCtx.Notes = &notes.String
	}

	if snap != nil {
		if sa := snap.Accounts[txCtx.SourceAccountID]; sa != nil {
			txCtx.SourceAccountName = sa.Name
			txCtx.IsUserOwnedSource = sa.IsUserOwned
			if p := snap.Parties[sa.PartyID]; p != nil && !p.IsUserOwned {
				txCtx.PartyName = p.Name
				txCtx.PartyID = p.ID
			}
		}
		if da := snap.Accounts[txCtx.DestinationAccountID]; da != nil {
			txCtx.DestAccountName = da.Name
			txCtx.IsUserOwnedDest = da.IsUserOwned
			if p := snap.Parties[da.PartyID]; p != nil && !p.IsUserOwned {
				txCtx.PartyName = p.Name
				txCtx.PartyID = p.ID
			}
		}
	}

	return &txCtx, nil
}
// LoadSnapshot loads an in-memory snapshot of buckets, targets, accounts, and parties.
func LoadSnapshot(ctx context.Context, tenantDB *sql.DB) (*lua.InMemoryBudgetSnapshot, error) {
	return loadSnapshotWithDBTX(ctx, tenantDB)
}

// ConvertBudgetTxToContext converts a budget transaction database row into an AutomationTxContext.
func ConvertBudgetTxToContext(txID string, tenantDB *sql.DB, snap *lua.InMemoryBudgetSnapshot) (*lua.AutomationTxContext, error) {
	return convertBudgetTxWithDBTX(context.Background(), tenantDB, txID, snap)
}

// DryRunAutomation executes an automation judge and executor against a transaction without side-effects.
func DryRunAutomation(
	ctx context.Context,
	tenantDB *sql.DB,
	judgeScript, executorScript string,
	targetTxID string,
) (matched bool, outcome *lua.AutomationOutcome, txCtx *lua.AutomationTxContext, err error) {
	snap, err := LoadSnapshot(ctx, tenantDB)
	if err != nil {
		return false, nil, nil, fmt.Errorf("load snapshot: %w", err)
	}

	txCtx, err = ConvertBudgetTxToContext(targetTxID, tenantDB, snap)
	if err != nil {
		return false, nil, nil, fmt.Errorf("convert tx: %w", err)
	}

	matched, err = lua.EvaluateAutomationJudge(ctx, judgeScript, *txCtx, snap)
	if err != nil {
		return false, nil, nil, fmt.Errorf("evaluate judge: %w", err)
	}
	if !matched {
		return false, nil, txCtx, nil
	}

	outcome, err = lua.EvaluateAutomationExecutor(ctx, executorScript, *txCtx, snap)
	if err != nil {
		return true, nil, txCtx, fmt.Errorf("evaluate executor: %w", err)
	}

	return true, outcome, txCtx, nil
}

// RunAutomationsBatch runs all active automations sequentially on a list of transaction IDs.
func RunAutomationsBatch(
	ctx context.Context,
	tenantDB *sql.DB,
	txIDs []string,
	dryRun bool,
	agentID, correlationID string,
) (*BatchExecutionResult, error) {
	automations, err := ListAutomations(ctx, tenantDB)
	if err != nil {
		return nil, fmt.Errorf("list automations: %w", err)
	}

	// Filter active automations only
	var activeAutomations []Automation
	for _, a := range automations {
		if a.IsActive {
			activeAutomations = append(activeAutomations, a)
		}
	}

	if len(activeAutomations) == 0 {
		return &BatchExecutionResult{
			ProcessedCount: len(txIDs),
			Changes:        []TxChange{},
		}, nil
	}

	snap, err := LoadSnapshot(ctx, tenantDB)
	if err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}

	result := &BatchExecutionResult{
		ProcessedCount: len(txIDs),
	}

	for _, txID := range txIDs {
		txCtx, err := ConvertBudgetTxToContext(txID, tenantDB, snap)
		if err != nil {
			return nil, err
		}

		origNotes := ""
		if txCtx.Notes != nil {
			origNotes = *txCtx.Notes
		}
		origBucket := txCtx.BucketName
		origTarget := txCtx.TargetName
		origAmount := txCtx.SourceAmountMinor

		currentTx := *txCtx
		isDeleted := false
		var splits []lua.SplitItem

		for _, a := range activeAutomations {
			matched, err := lua.EvaluateAutomationJudge(ctx, a.JudgeScript, currentTx, snap)
			if err != nil {
				return nil, fmt.Errorf("automation %q [%s] judge failed: %w", a.Name, a.ID, err)
			}
			if !matched {
				continue
			}

			outcome, err := lua.EvaluateAutomationExecutor(ctx, a.ExecutorScript, currentTx, snap)
			if err != nil {
				return nil, fmt.Errorf("automation %q [%s] executor failed: %w", a.Name, a.ID, err)
			}

			if outcome.Deleted {
				isDeleted = true
				break
			}

			if len(outcome.Splits) > 0 {
				splits = outcome.Splits
				// First split updates the current tx representation
				currentTx.SourceAmountMinor = splits[0].AmountMinor
				if currentTx.SourceCurrency == currentTx.DestCurrency {
					currentTx.DestAmountMinor = splits[0].AmountMinor
				}
				currentTx.BucketID = splits[0].BucketID
				if currentTx.BucketID != nil && snap.Buckets[*currentTx.BucketID] != nil {
					currentTx.BucketName = snap.Buckets[*currentTx.BucketID].Name
				}
				currentTx.TargetID = splits[0].TargetID
				if currentTx.TargetID != nil && snap.Targets[*currentTx.TargetID] != nil {
					currentTx.TargetName = snap.Targets[*currentTx.TargetID].Name
				}
				currentTx.Notes = splits[0].Notes
				break
			}

			if outcome.Modified {
				currentTx = outcome.UpdatedTx
			}
		}

		newNotes := ""
		if currentTx.Notes != nil {
			newNotes = *currentTx.Notes
		}
		newBucket := currentTx.BucketName
		newTarget := currentTx.TargetName
		newAmount := currentTx.SourceAmountMinor

		if isDeleted {
			result.DeletedCount++
			result.Changes = append(result.Changes, TxChange{
				TxID:           txID,
				OriginalNotes:  origNotes,
				NewNotes:       "(Deleted)",
				OriginalBucket: origBucket,
				NewBucket:      "",
				OriginalTarget: origTarget,
				NewTarget:      "",
				OriginalAmount: origAmount,
				NewAmount:      0,
				Action:         "DELETED",
			})
			if !dryRun {
				if err := transaction.DeleteBudgetTransaction(ctx, tenantDB, txID, agentID, correlationID); err != nil {
					return nil, fmt.Errorf("delete transaction %s: %w", txID, err)
				}
			}
		} else if len(splits) > 0 {
			result.SplitCount++
			result.Changes = append(result.Changes, TxChange{
				TxID:           txID,
				OriginalNotes:  origNotes,
				NewNotes:       newNotes,
				OriginalBucket: origBucket,
				NewBucket:      newBucket,
				OriginalTarget: origTarget,
				NewTarget:      newTarget,
				OriginalAmount: origAmount,
				NewAmount:      newAmount,
				Action:         "SPLIT",
				SplitsCount:    len(splits),
			})
			if !dryRun {
				// 1. Update part 0 in-place
				updateInput := transaction.UpdateBudgetTxInput{
					ID:                   txID,
					SourceAccountID:      currentTx.SourceAccountID,
					DestinationAccountID: currentTx.DestinationAccountID,
					SourceAmountMinor:    splits[0].AmountMinor,
					SourceCurrency:       currentTx.SourceCurrency,
					DestAmountMinor:      splits[0].AmountMinor,
					DestCurrency:         currentTx.DestCurrency,
					BucketID:             splits[0].BucketID,
					TargetID:             splits[0].TargetID,
					TransactionDate:      currentTx.TransactionDate,
					Notes:                splits[0].Notes,
					AgentID:              agentID,
					CorrelationID:        correlationID,
				}
				if err := transaction.UpdateBudgetTransaction(ctx, tenantDB, updateInput); err != nil {
					return nil, fmt.Errorf("update split part 1 on tx %s: %w", txID, err)
				}

				// 2. Insert parts 1..N
				var newBtxs []transaction.BudgetTxInput
				for i := 1; i < len(splits); i++ {
					sp := splits[i]
					newBtxs = append(newBtxs, transaction.BudgetTxInput{
						SourceAccountID:      currentTx.SourceAccountID,
						DestinationAccountID: currentTx.DestinationAccountID,
						SourceAmountMinor:    sp.AmountMinor,
						SourceCurrency:       currentTx.SourceCurrency,
						DestAmountMinor:      sp.AmountMinor,
						DestCurrency:         currentTx.DestCurrency,
						BucketID:             sp.BucketID,
						TargetID:             sp.TargetID,
						TransactionDate:      currentTx.TransactionDate,
						Notes:                sp.Notes,
					})
				}
				_, err := transaction.ExecuteResolution(ctx, tenantDB, transaction.ResolveResolutionInput{
					OperationType:      transaction.OpSplit,
					BudgetTransactions: newBtxs,
					AgentID:            agentID,
					CorrelationID:      correlationID,
				})
				if err != nil {
					return nil, fmt.Errorf("create split parts on tx %s: %w", txID, err)
				}
			}
		} else if origNotes != newNotes || origBucket != newBucket || origTarget != newTarget || origAmount != newAmount || currentTx.SourceAccountID != txCtx.SourceAccountID || currentTx.DestinationAccountID != txCtx.DestinationAccountID {
			result.ModifiedCount++
			result.Changes = append(result.Changes, TxChange{
				TxID:           txID,
				OriginalNotes:  origNotes,
				NewNotes:       newNotes,
				OriginalBucket: origBucket,
				NewBucket:      newBucket,
				OriginalTarget: origTarget,
				NewTarget:      newTarget,
				OriginalAmount: origAmount,
				NewAmount:      newAmount,
				Action:         "MODIFIED",
			})
			if !dryRun {
				updateInput := transaction.UpdateBudgetTxInput{
					ID:                   txID,
					SourceAccountID:      currentTx.SourceAccountID,
					DestinationAccountID: currentTx.DestinationAccountID,
					SourceAmountMinor:    currentTx.SourceAmountMinor,
					SourceCurrency:       currentTx.SourceCurrency,
					DestAmountMinor:      currentTx.DestAmountMinor,
					DestCurrency:         currentTx.DestCurrency,
					BucketID:             currentTx.BucketID,
					TargetID:             currentTx.TargetID,
					TransactionDate:      currentTx.TransactionDate,
					Notes:                currentTx.Notes,
					AgentID:              agentID,
					CorrelationID:        correlationID,
				}
				if err := transaction.UpdateBudgetTransaction(ctx, tenantDB, updateInput); err != nil {
					return nil, fmt.Errorf("update transaction %s: %w", txID, err)
				}
			}
		}
	}

	return result, nil
}
