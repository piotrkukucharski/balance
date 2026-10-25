package budget

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/lua"
)

// Event Types for Buckets and Targets
const (
	EventTypeBucketCreated  = "BUCKET_CREATED"
	EventTypeBucketUpdated  = "BUCKET_UPDATED"
	EventTypeBucketArchived = "BUCKET_ARCHIVED"
	EventTypeFundsAssigned  = "FUNDS_ASSIGNED"
	EventTypeFundsMoved     = "FUNDS_MOVED"

	EventTypeTargetCreated  = "TARGET_CREATED"
	EventTypeTargetUpdated  = "TARGET_UPDATED"
	EventTypeTargetArchived = "TARGET_ARCHIVED"
)

// CreateBucketInput payload
type CreateBucketInput struct {
	GroupName     string
	Name          string
	AgentID       string
	CorrelationID string
}

// UpdateBucketInput payload
type UpdateBucketInput struct {
	ID            string
	GroupName     string
	Name          string
	AgentID       string
	CorrelationID string
}

// CreateTargetInput payload
type CreateTargetInput struct {
	BucketID            string
	Name                string
	TargetType          string // 'EXPENSE', 'SAVINGS', 'DEBT_PAYMENT'
	Priority            int    // >= 1 (1=Optional, 2=Recommended, 3=Mandatory)
	SortOrder           int
	AmountMode          string // 'FIXED', 'LUA_FORMULA'
	AmountMinor         int64
	LuaScript           *string
	Currency            string
	RecurrenceCadence   string // 'ONE_OFF', 'WEEKLY', 'MONTHLY', 'ANNUAL', 'CUSTOM_DAYS', 'CUSTOM_MONTHS', 'CUSTOM_YEARS'
	RecurrenceInterval  int
	DueDate             *int64
	NonSpendingStrategy string // 'ROLL_OVER', 'RETURN_TO_POOL', 'ARREARS_CUMULATIVE', 'CAP_AND_HOLD', 'PRORATE_FUTURE'
	CeilingAmountMinor  *int64
	AgentID             string
	CorrelationID       string
}

// UpdateTargetInput payload
type UpdateTargetInput struct {
	ID                  string
	Name                string
	TargetType          string
	Priority            int
	SortOrder           int
	AmountMode          string // 'FIXED', 'LUA_FORMULA'
	AmountMinor         int64
	LuaScript           *string
	RecurrenceCadence   string
	RecurrenceInterval  int
	DueDate             *int64
	NonSpendingStrategy string
	CeilingAmountMinor  *int64
	AgentID             string
	CorrelationID       string
}

// AssignFundsInput payload for assigning funds from Ready to Assign to a bucket/target
type AssignFundsInput struct {
	BucketID      string
	TargetID      *string
	Currency      string
	AmountMinor   int64 // positive: add funds, negative: return funds to pool
	AgentID       string
	CorrelationID string
}

// MoveFundsInput payload for moving funds between buckets
type MoveFundsInput struct {
	SourceBucketID string
	DestBucketID   string
	Currency       string
	AmountMinor    int64 // strictly positive
	AgentID        string
	CorrelationID  string
}

// CreateBucket creates a new bucket in event store and synchronous projection.
func CreateBucket(ctx context.Context, tenantDB *sql.DB, input CreateBucketInput) (string, error) {
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	bucketID, err := CreateBucketTx(ctx, tx, input)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return bucketID, nil
}

// CreateBucketTx creates a new bucket within an existing transaction.
func CreateBucketTx(ctx context.Context, tx *sql.Tx, input CreateBucketInput) (string, error) {
	if input.Name == "" {
		return "", errors.New("bucket name cannot be empty")
	}
	if input.GroupName == "" {
		input.GroupName = "Default"
	}

	now := time.Now().UTC().UnixMicro()
	bucketID := domain.NewUUIDv7()
	eventID := domain.NewUUIDv7()

	payload, err := json.Marshal(map[string]any{
		"bucket_id":  bucketID,
		"group_name": input.GroupName,
		"name":       input.Name,
	})
	if err != nil {
		return "", err
	}

	// 1. Record event
	_, err = tx.ExecContext(ctx, `
		INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?)`,
		eventID, bucketID, EventTypeBucketCreated, string(payload), input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return "", fmt.Errorf("failed writing bucket event: %w", err)
	}

	// 2. Synchronous projection
	_, err = tx.ExecContext(ctx, `
		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, ?, ?, 0, 1, ?)`,
		bucketID, input.GroupName, input.Name, now,
	)
	if err != nil {
		return "", fmt.Errorf("failed writing buckets_projection: %w", err)
	}

	return bucketID, nil
}

// UpdateBucket updates bucket name or group name.
func UpdateBucket(ctx context.Context, tenantDB *sql.DB, input UpdateBucketInput) error {
	if input.ID == "" || input.Name == "" {
		return errors.New("bucket id and name are required")
	}
	if input.GroupName == "" {
		input.GroupName = "Default"
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentVersion int
	err = tx.QueryRowContext(ctx, `SELECT version FROM buckets_projection WHERE id = ? AND is_archived = 0`, input.ID).Scan(&currentVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("bucket not found or archived")
		}
		return err
	}

	now := time.Now().UTC().UnixMicro()
	newVersion := currentVersion + 1
	eventID := domain.NewUUIDv7()

	payload, err := json.Marshal(map[string]any{
		"bucket_id":  input.ID,
		"group_name": input.GroupName,
		"name":       input.Name,
	})
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, input.ID, newVersion, EventTypeBucketUpdated, string(payload), input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed writing bucket event: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE buckets_projection
		SET group_name = ?, name = ?, version = ?, updated_at = ?
		WHERE id = ?`,
		input.GroupName, input.Name, newVersion, now, input.ID,
	)
	if err != nil {
		return fmt.Errorf("failed updating buckets_projection: %w", err)
	}

	return tx.Commit()
}

// ArchiveBucket archives a bucket if and only if it has zero active targets and zero balance.
func ArchiveBucket(ctx context.Context, tenantDB *sql.DB, bucketID, agentID, correlationID string) error {
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentVersion int
	err = tx.QueryRowContext(ctx, `SELECT version FROM buckets_projection WHERE id = ? AND is_archived = 0`, bucketID).Scan(&currentVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("bucket not found or already archived")
		}
		return err
	}

	// Invariant: bucket cannot be archived if it has non-zero balances in any currency
	var nonZeroBalances int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM bucket_currency_balances WHERE bucket_id = ? AND balance_minor != 0`, bucketID).Scan(&nonZeroBalances)
	if err != nil {
		return err
	}
	if nonZeroBalances > 0 {
		return errors.New("cannot archive bucket with non-zero balance; move funds to Ready to Assign first")
	}

	// Invariant: bucket cannot be archived if it has non-archived targets
	var activeTargets int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection WHERE bucket_id = ? AND is_archived = 0`, bucketID).Scan(&activeTargets)
	if err != nil {
		return err
	}
	if activeTargets > 0 {
		return errors.New("cannot archive bucket with active targets; delete or move targets first")
	}

	now := time.Now().UTC().UnixMicro()
	newVersion := currentVersion + 1
	eventID := domain.NewUUIDv7()

	payload, _ := json.Marshal(map[string]any{"bucket_id": bucketID})

	_, err = tx.ExecContext(ctx, `
		INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, bucketID, newVersion, EventTypeBucketArchived, string(payload), agentID, correlationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed writing archive bucket event: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE buckets_projection
		SET is_archived = 1, version = ?, updated_at = ?
		WHERE id = ?`,
		newVersion, now, bucketID,
	)
	if err != nil {
		return fmt.Errorf("failed archiving buckets_projection: %w", err)
	}

	return tx.Commit()
}

// CreateTarget creates a new target within a bucket.
func CreateTarget(ctx context.Context, tenantDB *sql.DB, input CreateTargetInput) (string, error) {
	if input.AmountMode == "LUA_FORMULA" && input.LuaScript != nil && *input.LuaScript != "" {
		if err := lua.ValidateLuaScript(*input.LuaScript); err != nil {
			return "", fmt.Errorf("invalid lua script: %w", err)
		}
		if input.AmountMinor == 0 {
			snap, _ := BuildBudgetSnapshot(ctx, tenantDB, input.Currency, 30)
			if snap != nil {
				evalVal, err := lua.EvaluateFormula(ctx, *input.LuaScript, snap)
				if err == nil {
					input.AmountMinor = evalVal
				}
			}
		}
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	targetID, err := CreateTargetTx(ctx, tx, input)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return targetID, nil
}

// CreateTargetTx creates a new target within an existing transaction.
func CreateTargetTx(ctx context.Context, tx *sql.Tx, input CreateTargetInput) (string, error) {
	if input.BucketID == "" || input.Name == "" {
		return "", errors.New("bucket id and target name are required")
	}
	if input.TargetType == "" {
		input.TargetType = "EXPENSE"
	}
	if input.Priority < 1 {
		input.Priority = 2
	}
	if input.Currency == "" {
		input.Currency = "PLN"
	}
	if input.RecurrenceCadence == "" {
		input.RecurrenceCadence = CadenceMonthly
	}
	isCustom := input.RecurrenceCadence == CadenceCustomDays ||
		input.RecurrenceCadence == CadenceCustomMonths ||
		input.RecurrenceCadence == CadenceCustomYears

	if isCustom && input.RecurrenceInterval < 1 {
		switch input.RecurrenceCadence {
		case CadenceCustomMonths:
			input.RecurrenceInterval = 3
		case CadenceCustomYears:
			input.RecurrenceInterval = 1
		default:
			input.RecurrenceInterval = 90
		}
	} else if !isCustom {
		input.RecurrenceInterval = 0
	}

	if (input.RecurrenceCadence == CadenceCustomMonths || input.RecurrenceCadence == CadenceCustomYears) && (input.DueDate == nil || *input.DueDate == 0) {
		return "", errors.New("due date is required for custom months and custom years cadence")
	}

	var rInterval *int
	if isCustom {
		rInterval = &input.RecurrenceInterval
	}
	if input.NonSpendingStrategy == "" {
		input.NonSpendingStrategy = StrategyRollOver
	}
	if input.AmountMode == "" {
		input.AmountMode = "FIXED"
	}

	if err := domain.ValidateAmountMinor(input.AmountMinor, true); err != nil {
		return "", fmt.Errorf("invalid target amount: %w", err)
	}

	// Verify bucket exists and is not archived in this transaction
	var bucketExists int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM buckets_projection WHERE id = ? AND is_archived = 0`, input.BucketID).Scan(&bucketExists)
	if err != nil || bucketExists == 0 {
		return "", errors.New("bucket not found or archived")
	}

	now := time.Now().UTC().UnixMicro()
	targetID := domain.NewUUIDv7()
	eventID := domain.NewUUIDv7()

	payload, err := json.Marshal(map[string]any{
		"target_id":             targetID,
		"bucket_id":             input.BucketID,
		"name":                  input.Name,
		"target_type":           input.TargetType,
		"priority":              input.Priority,
		"sort_order":            input.SortOrder,
		"amount_mode":           input.AmountMode,
		"amount_minor":          input.AmountMinor,
		"lua_script":            input.LuaScript,
		"currency":              input.Currency,
		"recurrence_cadence":    input.RecurrenceCadence,
		"recurrence_interval":   rInterval,
		"due_date":              input.DueDate,
		"non_spending_strategy": input.NonSpendingStrategy,
		"ceiling_amount_minor":  input.CeilingAmountMinor,
	})
	if err != nil {
		return "", err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO target_events (event_id, aggregate_id, bucket_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		eventID, targetID, input.BucketID, EventTypeTargetCreated, string(payload), input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return "", fmt.Errorf("failed writing target event: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO targets_projection (
			id, bucket_id, name, target_type, priority, sort_order, amount_mode, amount_minor,
			lua_script, currency, recurrence_cadence, recurrence_interval, due_date, non_spending_strategy,
			ceiling_amount_minor, current_funded_minor, is_archived, version, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 1, ?)`,
		targetID, input.BucketID, input.Name, input.TargetType, input.Priority, input.SortOrder,
		input.AmountMode, input.AmountMinor, input.LuaScript, input.Currency, input.RecurrenceCadence,
		rInterval, input.DueDate, input.NonSpendingStrategy, input.CeilingAmountMinor, now,
	)
	if err != nil {
		return "", fmt.Errorf("failed inserting targets_projection: %w", err)
	}

	return targetID, nil
}

// UpdateTarget updates an existing target.
func UpdateTarget(ctx context.Context, tenantDB *sql.DB, input UpdateTargetInput) error {
	if input.ID == "" || input.Name == "" {
		return errors.New("target id and name are required")
	}
	if input.Priority < 1 {
		input.Priority = 2
	}
	if input.AmountMode == "" {
		input.AmountMode = "FIXED"
	}

	if input.AmountMode == "LUA_FORMULA" && input.LuaScript != nil && *input.LuaScript != "" {
		if err := lua.ValidateLuaScript(*input.LuaScript); err != nil {
			return fmt.Errorf("invalid lua script: %w", err)
		}
		if input.AmountMinor == 0 {
			snap, _ := BuildBudgetSnapshot(ctx, tenantDB, "PLN", 30)
			if snap != nil {
				evalVal, err := lua.EvaluateFormula(ctx, *input.LuaScript, snap)
				if err == nil {
					input.AmountMinor = evalVal
				}
			}
		}
	}

	if err := domain.ValidateAmountMinor(input.AmountMinor, true); err != nil {
		return fmt.Errorf("invalid target amount: %w", err)
	}

	isCustom := input.RecurrenceCadence == CadenceCustomDays ||
		input.RecurrenceCadence == CadenceCustomMonths ||
		input.RecurrenceCadence == CadenceCustomYears

	if isCustom && input.RecurrenceInterval < 1 {
		switch input.RecurrenceCadence {
		case CadenceCustomMonths:
			input.RecurrenceInterval = 3
		case CadenceCustomYears:
			input.RecurrenceInterval = 1
		default:
			input.RecurrenceInterval = 90
		}
	} else if input.RecurrenceCadence != "" && !isCustom {
		input.RecurrenceInterval = 0
	}

	if (input.RecurrenceCadence == CadenceCustomMonths || input.RecurrenceCadence == CadenceCustomYears) && (input.DueDate == nil || *input.DueDate == 0) {
		return errors.New("due date is required for custom months and custom years cadence")
	}

	var rInterval *int
	if isCustom {
		rInterval = &input.RecurrenceInterval
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var bucketID string
	var currentVersion int
	err = tx.QueryRowContext(ctx, `SELECT bucket_id, version FROM targets_projection WHERE id = ? AND is_archived = 0`, input.ID).Scan(&bucketID, &currentVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("target not found or archived")
		}
		return err
	}

	now := time.Now().UTC().UnixMicro()
	newVersion := currentVersion + 1
	eventID := domain.NewUUIDv7()

	payload, err := json.Marshal(map[string]any{
		"target_id":             input.ID,
		"name":                  input.Name,
		"target_type":           input.TargetType,
		"priority":              input.Priority,
		"sort_order":            input.SortOrder,
		"amount_mode":           input.AmountMode,
		"amount_minor":          input.AmountMinor,
		"lua_script":            input.LuaScript,
		"recurrence_cadence":    input.RecurrenceCadence,
		"recurrence_interval":   rInterval,
		"due_date":              input.DueDate,
		"non_spending_strategy": input.NonSpendingStrategy,
		"ceiling_amount_minor":  input.CeilingAmountMinor,
	})
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO target_events (event_id, aggregate_id, bucket_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, input.ID, bucketID, newVersion, EventTypeTargetUpdated, string(payload), input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed writing target event: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE targets_projection
		SET name = ?, target_type = ?, priority = ?, sort_order = ?, amount_mode = ?, amount_minor = ?,
		    lua_script = ?, recurrence_cadence = ?, recurrence_interval = ?, due_date = ?, non_spending_strategy = ?,
		    ceiling_amount_minor = ?, version = ?, updated_at = ?
		WHERE id = ?`,
		input.Name, input.TargetType, input.Priority, input.SortOrder, input.AmountMode, input.AmountMinor,
		input.LuaScript, input.RecurrenceCadence, rInterval, input.DueDate, input.NonSpendingStrategy,
		input.CeilingAmountMinor, newVersion, now, input.ID,
	)
	if err != nil {
		return fmt.Errorf("failed updating targets_projection: %w", err)
	}

	return tx.Commit()
}

// DeleteTarget archives a target.
func DeleteTarget(ctx context.Context, tenantDB *sql.DB, targetID, agentID, correlationID string) error {
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var bucketID string
	var currentVersion int
	err = tx.QueryRowContext(ctx, `SELECT bucket_id, version FROM targets_projection WHERE id = ? AND is_archived = 0`, targetID).Scan(&bucketID, &currentVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("target not found or already archived")
		}
		return err
	}

	now := time.Now().UTC().UnixMicro()
	newVersion := currentVersion + 1
	eventID := domain.NewUUIDv7()

	payload, _ := json.Marshal(map[string]any{"target_id": targetID})

	_, err = tx.ExecContext(ctx, `
		INSERT INTO target_events (event_id, aggregate_id, bucket_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, targetID, bucketID, newVersion, EventTypeTargetArchived, string(payload), agentID, correlationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed writing archive target event: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE targets_projection
		SET is_archived = 1, version = ?, updated_at = ?
		WHERE id = ?`,
		newVersion, now, targetID,
	)
	if err != nil {
		return fmt.Errorf("failed archiving targets_projection: %w", err)
	}

	return tx.Commit()
}

// AssignFunds assigns funds from the ready to assign pool to a bucket (and optionally specific target).
// A negative amount returns funds from the bucket to ready to assign.
func AssignFunds(ctx context.Context, tenantDB *sql.DB, input AssignFundsInput) error {
	if input.BucketID == "" {
		return errors.New("bucket id is required")
	}
	if input.Currency == "" {
		input.Currency = "PLN"
	}
	if input.AmountMinor == 0 {
		return errors.New("assignment amount cannot be zero")
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()
	eventID := domain.NewUUIDv7()

	// Update bucket balance
	_, err = tx.ExecContext(ctx, `
		INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor)
		VALUES (?, ?, ?)
		ON CONFLICT(bucket_id, currency) DO UPDATE SET
			balance_minor = balance_minor + excluded.balance_minor`,
		input.BucketID, input.Currency, input.AmountMinor,
	)
	if err != nil {
		return fmt.Errorf("failed updating bucket_currency_balances: %w", err)
	}

	// If assigned to a specific target, update its current_funded_minor
	if input.TargetID != nil && *input.TargetID != "" {
		_, err = tx.ExecContext(ctx, `
			UPDATE targets_projection
			SET current_funded_minor = max(0, current_funded_minor + ?), updated_at = ?
			WHERE id = ? AND bucket_id = ?`,
			input.AmountMinor, now, *input.TargetID, input.BucketID,
		)
		if err != nil {
			return fmt.Errorf("failed updating target current_funded_minor: %w", err)
		}
	} else if input.AmountMinor > 0 {
		if err := waterfallAssignBucketTargetsTx(ctx, tx, input.BucketID, input.Currency, input.AmountMinor, now); err != nil {
			return fmt.Errorf("failed waterfall assigning targets: %w", err)
		}
	} else if input.AmountMinor < 0 {
		if err := unassignBucketTargetsTx(ctx, tx, input.BucketID, input.Currency, now); err != nil {
			return fmt.Errorf("failed unassigning targets: %w", err)
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"bucket_id":    input.BucketID,
		"target_id":    input.TargetID,
		"currency":     input.Currency,
		"amount_minor": input.AmountMinor,
	})

	var bucketVer int
	_ = tx.QueryRowContext(ctx, `SELECT version FROM buckets_projection WHERE id = ?`, input.BucketID).Scan(&bucketVer)
	nextVer := bucketVer + 1

	_, err = tx.ExecContext(ctx, `
		INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, input.BucketID, nextVer, EventTypeFundsAssigned, string(payload), input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed recording funds assigned event: %w", err)
	}

	_, _ = tx.ExecContext(ctx, `UPDATE buckets_projection SET version = ?, updated_at = ? WHERE id = ?`, nextVer, now, input.BucketID)

	return tx.Commit()
}

// unassignBucketTargetsTx reduces current_funded_minor of targets in a bucket so that total funded <= max(0, bucketBalance).
// Targets are defunded in reverse priority order (lowest priority / furthest due date first).
func unassignBucketTargetsTx(ctx context.Context, tx *sql.Tx, bucketID, currency string, now int64) error {
	var bal int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(balance_minor, 0)
		FROM bucket_currency_balances
		WHERE bucket_id = ? AND currency = ?`, bucketID, currency).Scan(&bal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if bal < 0 {
		bal = 0
	}

	var totalFunded int64
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(current_funded_minor), 0)
		FROM targets_projection
		WHERE bucket_id = ? AND currency = ? AND is_archived = 0`, bucketID, currency).Scan(&totalFunded)
	if err != nil {
		return err
	}

	if totalFunded <= bal {
		return nil
	}

	toDefund := totalFunded - bal
	rows, err := tx.QueryContext(ctx, `
		SELECT id, current_funded_minor
		FROM targets_projection
		WHERE bucket_id = ? AND currency = ? AND is_archived = 0 AND current_funded_minor > 0
		ORDER BY priority ASC, due_date DESC, sort_order DESC`, bucketID, currency)
	if err != nil {
		return err
	}
	defer rows.Close()

	type targetItem struct {
		id     string
		funded int64
	}
	var items []targetItem
	for rows.Next() {
		var item targetItem
		if err := rows.Scan(&item.id, &item.funded); err == nil {
			items = append(items, item)
		}
	}
	rows.Close()

	for _, t := range items {
		if toDefund <= 0 {
			break
		}
		deduct := t.funded
		if deduct > toDefund {
			deduct = toDefund
		}
		if deduct > 0 {
			_, err = tx.ExecContext(ctx, `
				UPDATE targets_projection
				SET current_funded_minor = current_funded_minor - ?, updated_at = ?
				WHERE id = ?`, deduct, now, t.id)
			if err != nil {
				return err
			}
			toDefund -= deduct
		}
	}
	return nil
}

// waterfallAssignBucketTargetsTx waterfall-funds active targets in a bucket up to amountMinor.
func waterfallAssignBucketTargetsTx(ctx context.Context, tx *sql.Tx, bucketID, currency string, amountMinor int64, now int64) error {
	if amountMinor <= 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, priority, sort_order, amount_minor, recurrence_cadence, recurrence_interval,
		       due_date, non_spending_strategy, current_funded_minor
		FROM targets_projection
		WHERE bucket_id = ? AND currency = ? AND is_archived = 0
		ORDER BY priority DESC, due_date ASC, sort_order ASC`, bucketID, currency)
	if err != nil {
		return err
	}
	defer rows.Close()

	var targets []*TargetDefinition
	for rows.Next() {
		var t TargetDefinition
		var dueDate sql.NullInt64
		var rInterval sql.NullInt64
		rows.Scan(&t.ID, &t.Priority, &t.SortOrder, &t.AmountMinor, &t.RecurrenceCadence,
			&rInterval, &dueDate, &t.NonSpendingStrategy, &t.CurrentFundedMinor)
		if rInterval.Valid {
			t.RecurrenceInterval = int(rInterval.Int64)
		}
		if dueDate.Valid {
			t.DueDate = dueDate.Int64
		}
		targets = append(targets, &t)
	}
	rows.Close()

	if len(targets) == 0 {
		return nil
	}

	allocs, _ := PriorityAutoAssign(targets, 30, time.Now().UTC(), amountMinor)
	for _, alloc := range allocs {
		if alloc.Assigned <= 0 {
			continue
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE targets_projection
			SET current_funded_minor = current_funded_minor + ?, updated_at = ?
			WHERE id = ?`, alloc.Assigned, now, alloc.TargetID)
		if err != nil {
			return err
		}
	}
	return nil
}

// MoveFunds transfers funds directly between two envelopes.
func MoveFunds(ctx context.Context, tenantDB *sql.DB, input MoveFundsInput) error {
	if input.SourceBucketID == "" || input.DestBucketID == "" {
		return errors.New("source and destination bucket IDs are required")
	}
	if input.SourceBucketID == input.DestBucketID {
		return errors.New("cannot move funds within the same bucket")
	}
	if input.AmountMinor <= 0 {
		return errors.New("amount must be strictly positive")
	}
	if input.Currency == "" {
		input.Currency = "PLN"
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Verify source has sufficient balance
	var srcBal int64
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(balance_minor, 0)
		FROM bucket_currency_balances
		WHERE bucket_id = ? AND currency = ?`, input.SourceBucketID, input.Currency).Scan(&srcBal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if srcBal < input.AmountMinor {
		return fmt.Errorf("insufficient funds in source bucket (has %d, needs %d)", srcBal, input.AmountMinor)
	}

	now := time.Now().UTC().UnixMicro()
	eventID := domain.NewUUIDv7()

	// Decrement source
	_, err = tx.ExecContext(ctx, `
		UPDATE bucket_currency_balances
		SET balance_minor = balance_minor - ?
		WHERE bucket_id = ? AND currency = ?`, input.AmountMinor, input.SourceBucketID, input.Currency)
	if err != nil {
		return err
	}

	// Increment destination
	_, err = tx.ExecContext(ctx, `
		INSERT INTO bucket_currency_balances (bucket_id, currency, balance_minor)
		VALUES (?, ?, ?)
		ON CONFLICT(bucket_id, currency) DO UPDATE SET
			balance_minor = balance_minor + excluded.balance_minor`,
		input.DestBucketID, input.Currency, input.AmountMinor,
	)
	if err != nil {
		return err
	}

	// Defund source targets if source bucket balance fell below total target funding
	if err := unassignBucketTargetsTx(ctx, tx, input.SourceBucketID, input.Currency, now); err != nil {
		return fmt.Errorf("failed unassigning source bucket targets: %w", err)
	}

	// Waterfall fund destination targets with the incoming money
	if err := waterfallAssignBucketTargetsTx(ctx, tx, input.DestBucketID, input.Currency, input.AmountMinor, now); err != nil {
		return fmt.Errorf("failed assigning dest bucket targets: %w", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"source_bucket_id": input.SourceBucketID,
		"dest_bucket_id":   input.DestBucketID,
		"currency":         input.Currency,
		"amount_minor":     input.AmountMinor,
	})

	var srcVer int
	_ = tx.QueryRowContext(ctx, `SELECT version FROM buckets_projection WHERE id = ?`, input.SourceBucketID).Scan(&srcVer)
	nextVer := srcVer + 1

	_, err = tx.ExecContext(ctx, `
		INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, input.SourceBucketID, nextVer, EventTypeFundsMoved, string(payload), input.AgentID, input.CorrelationID, now,
	)
	if err != nil {
		return fmt.Errorf("failed recording funds moved event: %w", err)
	}

	_, _ = tx.ExecContext(ctx, `UPDATE buckets_projection SET version = ?, updated_at = ? WHERE id = ?`, nextVer, now, input.SourceBucketID)

	var dstVer int
	_ = tx.QueryRowContext(ctx, `SELECT version FROM buckets_projection WHERE id = ?`, input.DestBucketID).Scan(&dstVer)
	_, _ = tx.ExecContext(ctx, `UPDATE buckets_projection SET version = ?, updated_at = ? WHERE id = ?`, dstVer+1, now, input.DestBucketID)

	return tx.Commit()
}

// CalculateReadyToAssign calculates available unallocated funds per currency.
// Formula: SUM(account_currency_balances of user_owned accounts) - SUM(bucket_currency_balances).
func CalculateReadyToAssign(ctx context.Context, tenantDB *sql.DB) (map[string]int64, error) {
	// 1. Total liquid cash on user accounts per currency
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT acb.currency, COALESCE(SUM(acb.balance_minor), 0)
		FROM account_currency_balances acb
		JOIN accounts_current a ON acb.account_id = a.id
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.is_user_owned = 1 AND a.is_closed = 0
		GROUP BY acb.currency`)
	if err != nil {
		return nil, fmt.Errorf("failed querying account cash: %w", err)
	}
	defer rows.Close()

	totalCash := make(map[string]int64)
	for rows.Next() {
		var curr string
		var amt int64
		rows.Scan(&curr, &amt)
		totalCash[curr] = amt
	}

	// 2. Total allocated in buckets per currency
	bRows, err := tenantDB.QueryContext(ctx, `
		SELECT bcb.currency, COALESCE(SUM(bcb.balance_minor), 0)
		FROM bucket_currency_balances bcb
		JOIN buckets_projection b ON bcb.bucket_id = b.id
		WHERE b.is_archived = 0
		GROUP BY bcb.currency`)
	if err != nil {
		return nil, fmt.Errorf("failed querying bucket balances: %w", err)
	}
	defer bRows.Close()

	totalAllocated := make(map[string]int64)
	for bRows.Next() {
		var curr string
		var amt int64
		bRows.Scan(&curr, &amt)
		totalAllocated[curr] = amt
	}

	readyToAssign := make(map[string]int64)
	for curr, cash := range totalCash {
		readyToAssign[curr] = cash - totalAllocated[curr]
	}
	// Also include any currencies that might have bucket balances but zero cash (negative ready to assign)
	for curr, alloc := range totalAllocated {
		if _, exists := readyToAssign[curr]; !exists {
			readyToAssign[curr] = -alloc
		}
	}

	return readyToAssign, nil
}
