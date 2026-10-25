package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/transaction"
)

var (
	ErrNotFound       = errors.New("entity or target version not found")
	ErrInvalidEntity  = errors.New("invalid entity type for history")
	ErrAlreadyCurrent = errors.New("target version is already current")
	ErrSoftDelete     = errors.New("cannot restore a soft-delete version; select an active version to restore")
)

// FieldDiff captures the change of a specific field between versions.
type FieldDiff struct {
	FieldName string `json:"field_name"`
	Label     string `json:"label"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
}

// HistoryItem represents an immutable version entry from an append-only table.
type HistoryItem struct {
	ID            string         `json:"id"`
	Version       int            `json:"version"`
	Action        string         `json:"action"` // CREATE, UPDATE, RESTORE, SOFT_DELETE
	AgentID       string         `json:"agent_id"`
	CorrelationID string         `json:"correlation_id"`
	CreatedAt     int64          `json:"created_at"` // UTC microseconds
	CreatedAtTime time.Time      `json:"created_at_time"`
	FieldDiffs    []FieldDiff    `json:"field_diffs"`
	IsCurrent     bool           `json:"is_current"`
	Payload       map[string]any `json:"payload"`
}

// EntityHistoryResult encapsulates the full history timeline for an entity.
type EntityHistoryResult struct {
	EntityType     string        `json:"entity_type"`
	EntityID       string        `json:"entity_id"`
	EntityName     string        `json:"entity_name"`
	CurrentVersion int           `json:"current_version"`
	IsDeleted      bool          `json:"is_deleted"`
	Items          []HistoryItem `json:"items"` // Sorted DESC (latest version first)
}

// RestoreResult returns the outcome of a temporal restore operation.
type RestoreResult struct {
	EntityID            string `json:"id"`
	NewVersion          int    `json:"new_version"`
	RestoredFromVersion int    `json:"restored_from_version"`
	Action              string `json:"action"`
}

// NormalizeEntityType maps user-provided or URL entity identifiers to canonical table names.
func NormalizeEntityType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "party", "parties":
		return "parties", nil
	case "account", "accounts":
		return "accounts", nil
	case "matching_rule", "matching_rules", "matching-rule", "matching-rules":
		return "matching_rules", nil
	case "raw_transaction", "raw_transactions", "raw-transaction", "raw-transactions":
		return "raw_transactions", nil
	case "budget_transaction", "budget_transactions", "budget-transaction", "budget-transactions", "transaction", "transactions":
		return "budget_transactions", nil
	case "saved_report", "saved_reports", "saved-report", "saved-reports":
		return "saved_reports", nil
	case "automation", "automations":
		return "automations", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalidEntity, raw)
	}
}

// FormatEpochMicro formats microsecond epoch into human-readable UTC timestamp.
func FormatEpochMicro(micros int64) string {
	if micros <= 0 {
		return "-"
	}
	t := time.UnixMicro(micros).UTC()
	return t.Format("2006-01-02 15:04:05 UTC")
}

// FormatMinor formats integer minor units into currency string.
func FormatMinor(amountMinor int64, currency string) string {
	sign := ""
	val := amountMinor
	if val < 0 {
		sign = "-"
		val = -val
	}
	return fmt.Sprintf("%s%.2f %s", sign, float64(val)/100.0, currency)
}

// GetEntityHistory loads the chronological history of an entity and computes diffs.
func GetEntityHistory(ctx context.Context, tenantDB *sql.DB, entityTypeRaw, entityID string) (*EntityHistoryResult, error) {
	table, err := NormalizeEntityType(entityTypeRaw)
	if err != nil {
		return nil, err
	}
	if !domain.IsValidUUIDv7(entityID) {
		return nil, fmt.Errorf("invalid entity ID %q: must be UUIDv7", entityID)
	}

	res := &EntityHistoryResult{
		EntityType: table,
		EntityID:   entityID,
		Items:      make([]HistoryItem, 0),
	}

	switch table {
	case "parties":
		return loadPartiesHistory(ctx, tenantDB, res)
	case "accounts":
		return loadAccountsHistory(ctx, tenantDB, res)
	case "matching_rules":
		return loadMatchingRulesHistory(ctx, tenantDB, res)
	case "raw_transactions":
		return loadRawTransactionsHistory(ctx, tenantDB, res)
	case "budget_transactions":
		return loadBudgetTransactionsHistory(ctx, tenantDB, res)
	case "saved_reports":
		return loadSavedReportsHistory(ctx, tenantDB, res)
	case "automations":
		return loadAutomationsHistory(ctx, tenantDB, res)
	default:
		return nil, ErrInvalidEntity
	}
}

// RestoreEntityVersion resurrects or rolls back an entity to the target version.
func RestoreEntityVersion(ctx context.Context, tenantDB *sql.DB, entityTypeRaw, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	table, err := NormalizeEntityType(entityTypeRaw)
	if err != nil {
		return nil, err
	}
	if !domain.IsValidUUIDv7(entityID) {
		return nil, fmt.Errorf("invalid entity ID %q: must be UUIDv7", entityID)
	}
	if targetVersion <= 0 {
		return nil, errors.New("target version must be greater than 0")
	}
	if correlationID == "" {
		correlationID = domain.NewUUIDv7()
	}
	if now <= 0 {
		now = time.Now().UTC().UnixMicro()
	}

	switch table {
	case "parties":
		return restoreParty(ctx, tenantDB, entityID, targetVersion, agentID, correlationID, now)
	case "accounts":
		return restoreAccount(ctx, tenantDB, entityID, targetVersion, agentID, correlationID, now)
	case "matching_rules":
		return restoreMatchingRule(ctx, tenantDB, entityID, targetVersion, agentID, correlationID, now)
	case "raw_transactions":
		return restoreRawTransaction(ctx, tenantDB, entityID, targetVersion, agentID, correlationID, now)
	case "budget_transactions":
		return restoreBudgetTransaction(ctx, tenantDB, entityID, targetVersion, agentID, correlationID, now)
	case "saved_reports":
		return restoreSavedReport(ctx, tenantDB, entityID, targetVersion, agentID, correlationID, now)
	case "automations":
		return restoreAutomation(ctx, tenantDB, entityID, targetVersion, agentID, correlationID, now)
	default:
		return nil, ErrInvalidEntity
	}
}

// -------------------------------------------------------------
// Parties
// -------------------------------------------------------------

type partyRecord struct {
	id            string
	version       int
	name          string
	partyType     string
	isUserOwned   int
	notes         sql.NullString
	action        string
	agentID       string
	correlationID string
	createdAt     int64
}

func loadPartiesHistory(ctx context.Context, db *sql.DB, res *EntityHistoryResult) (*EntityHistoryResult, error) {
	// Check current projection
	var curVer int
	var curName string
	err := db.QueryRowContext(ctx, `SELECT version, name FROM parties_current WHERE id = ?`, res.EntityID).Scan(&curVer, &curName)
	if err == nil {
		res.CurrentVersion = curVer
		res.EntityName = curName
	} else {
		res.IsDeleted = true
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at
		FROM parties WHERE id = ? ORDER BY version ASC`, res.EntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []partyRecord
	for rows.Next() {
		var r partyRecord
		if err := rows.Scan(&r.id, &r.version, &r.name, &r.partyType, &r.isUserOwned, &r.notes, &r.action, &r.agentID, &r.correlationID, &r.createdAt); err != nil {
			return nil, err
		}
		records = append(records, r)
		if res.EntityName == "" {
			res.EntityName = r.name
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	// Compute diffs from version to version
	var items []HistoryItem
	for i, r := range records {
		item := HistoryItem{
			ID:            r.id,
			Version:       r.version,
			Action:        r.action,
			AgentID:       r.agentID,
			CorrelationID: r.correlationID,
			CreatedAt:     r.createdAt,
			CreatedAtTime: time.UnixMicro(r.createdAt).UTC(),
			IsCurrent:     r.version == res.CurrentVersion && !res.IsDeleted,
			Payload: map[string]any{
				"name":          r.name,
				"party_type":    r.partyType,
				"is_user_owned": r.isUserOwned,
				"notes":         r.notes.String,
			},
		}

		if i == 0 {
			item.FieldDiffs = append(item.FieldDiffs,
				FieldDiff{FieldName: "name", Label: "Name", NewValue: r.name},
				FieldDiff{FieldName: "party_type", Label: "Party Type", NewValue: r.partyType},
				FieldDiff{FieldName: "is_user_owned", Label: "User Owned", NewValue: boolToStr(r.isUserOwned == 1)},
			)
			if r.notes.Valid && r.notes.String != "" {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "notes", Label: "Notes", NewValue: r.notes.String})
			}
		} else {
			prev := records[i-1]
			if prev.name != r.name {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "name", Label: "Name", OldValue: prev.name, NewValue: r.name})
			}
			if prev.partyType != r.partyType {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "party_type", Label: "Party Type", OldValue: prev.partyType, NewValue: r.partyType})
			}
			if prev.isUserOwned != r.isUserOwned {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "is_user_owned", Label: "User Owned", OldValue: boolToStr(prev.isUserOwned == 1), NewValue: boolToStr(r.isUserOwned == 1)})
			}
			if prev.notes.String != r.notes.String {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "notes", Label: "Notes", OldValue: nullStrVal(prev.notes), NewValue: nullStrVal(r.notes)})
			}
		}

		items = append(items, item)
	}

	// Reverse to DESC order for UI timeline
	res.Items = reverseItems(items)
	return res, nil
}

func restoreParty(ctx context.Context, db *sql.DB, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	var name, pType string
	var owned int
	var notes sql.NullString
	var act string
	err := db.QueryRowContext(ctx, `
		SELECT name, party_type, is_user_owned, notes, action
		FROM parties WHERE id = ? AND version = ?`, entityID, targetVersion).Scan(&name, &pType, &owned, &notes, &act)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if act == "SOFT_DELETE" {
		return nil, ErrSoftDelete
	}

	var maxVer int
	db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM parties WHERE id = ?`, entityID).Scan(&maxVer)
	nextVer := maxVer + 1

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO parties (id, version, name, party_type, is_user_owned, notes, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'RESTORE', ?, ?, ?)`,
		entityID, nextVer, name, pType, owned, notes, agentID, correlationID, now)
	if err != nil {
		return nil, fmt.Errorf("append party restore: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, notes, agent_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			name = excluded.name,
			party_type = excluded.party_type,
			is_user_owned = excluded.is_user_owned,
			notes = excluded.notes,
			agent_id = excluded.agent_id,
			updated_at = excluded.updated_at`,
		entityID, nextVer, name, pType, owned, notes, agentID, now)
	if err != nil {
		return nil, fmt.Errorf("upsert parties_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &RestoreResult{
		EntityID:            entityID,
		NewVersion:          nextVer,
		RestoredFromVersion: targetVersion,
		Action:              "RESTORE",
	}, nil
}

// -------------------------------------------------------------
// Accounts
// -------------------------------------------------------------

type accountRecord struct {
	id              string
	version         int
	partyID         string
	name            string
	accountType     string
	primaryCurrency string
	iban            sql.NullString
	faviconType     string
	faviconValue    sql.NullString
	isClosed        int
	action          string
	agentID         string
	correlationID   string
	createdAt       int64
}

func loadAccountsHistory(ctx context.Context, db *sql.DB, res *EntityHistoryResult) (*EntityHistoryResult, error) {
	var curVer int
	var curName string
	err := db.QueryRowContext(ctx, `SELECT version, name FROM accounts_current WHERE id = ?`, res.EntityID).Scan(&curVer, &curName)
	if err == nil {
		res.CurrentVersion = curVer
		res.EntityName = curName
	} else {
		res.IsDeleted = true
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at
		FROM accounts WHERE id = ? ORDER BY version ASC`, res.EntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []accountRecord
	for rows.Next() {
		var r accountRecord
		if err := rows.Scan(&r.id, &r.version, &r.partyID, &r.name, &r.accountType, &r.primaryCurrency, &r.iban, &r.faviconType, &r.faviconValue, &r.isClosed, &r.action, &r.agentID, &r.correlationID, &r.createdAt); err != nil {
			return nil, err
		}
		records = append(records, r)
		if res.EntityName == "" {
			res.EntityName = r.name
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	var items []HistoryItem
	for i, r := range records {
		item := HistoryItem{
			ID:            r.id,
			Version:       r.version,
			Action:        r.action,
			AgentID:       r.agentID,
			CorrelationID: r.correlationID,
			CreatedAt:     r.createdAt,
			CreatedAtTime: time.UnixMicro(r.createdAt).UTC(),
			IsCurrent:     r.version == res.CurrentVersion && !res.IsDeleted,
			Payload: map[string]any{
				"name":             r.name,
				"account_type":     r.accountType,
				"primary_currency": r.primaryCurrency,
				"iban":             r.iban.String,
				"is_closed":        r.isClosed,
				"favicon_value":    r.faviconValue.String,
			},
		}

		if i == 0 {
			item.FieldDiffs = append(item.FieldDiffs,
				FieldDiff{FieldName: "name", Label: "Account Name", NewValue: r.name},
				FieldDiff{FieldName: "account_type", Label: "Account Type", NewValue: r.accountType},
				FieldDiff{FieldName: "primary_currency", Label: "Currency", NewValue: r.primaryCurrency},
			)
			if r.iban.Valid && r.iban.String != "" {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "iban", Label: "IBAN", NewValue: r.iban.String})
			}
			if r.faviconValue.Valid && r.faviconValue.String != "" {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "favicon_value", Label: "Icon", NewValue: r.faviconValue.String})
			}
		} else {
			prev := records[i-1]
			if prev.name != r.name {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "name", Label: "Account Name", OldValue: prev.name, NewValue: r.name})
			}
			if prev.accountType != r.accountType {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "account_type", Label: "Account Type", OldValue: prev.accountType, NewValue: r.accountType})
			}
			if prev.primaryCurrency != r.primaryCurrency {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "primary_currency", Label: "Currency", OldValue: prev.primaryCurrency, NewValue: r.primaryCurrency})
			}
			if prev.iban.String != r.iban.String {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "iban", Label: "IBAN", OldValue: nullStrVal(prev.iban), NewValue: nullStrVal(r.iban)})
			}
			if prev.isClosed != r.isClosed {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "is_closed", Label: "Status", OldValue: closedToStr(prev.isClosed), NewValue: closedToStr(r.isClosed)})
			}
			if prev.faviconValue.String != r.faviconValue.String {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "favicon_value", Label: "Icon", OldValue: nullStrVal(prev.faviconValue), NewValue: nullStrVal(r.faviconValue)})
			}
		}

		items = append(items, item)
	}

	res.Items = reverseItems(items)
	return res, nil
}

func restoreAccount(ctx context.Context, db *sql.DB, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	var partyID, name, aType, curr, favType string
	var iban, favVal sql.NullString
	var isClosed int
	var act string
	err := db.QueryRowContext(ctx, `
		SELECT party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action
		FROM accounts WHERE id = ? AND version = ?`, entityID, targetVersion).Scan(&partyID, &name, &aType, &curr, &iban, &favType, &favVal, &isClosed, &act)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if act == "SOFT_DELETE" {
		return nil, ErrSoftDelete
	}

	var maxVer int
	db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM accounts WHERE id = ?`, entityID).Scan(&maxVer)
	nextVer := maxVer + 1

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO accounts (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'RESTORE', ?, ?, ?)`,
		entityID, nextVer, partyID, name, aType, curr, iban, favType, favVal, isClosed, agentID, correlationID, now)
	if err != nil {
		return nil, fmt.Errorf("append account restore: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, iban, favicon_type, favicon_value, is_closed, agent_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			party_id = excluded.party_id,
			name = excluded.name,
			account_type = excluded.account_type,
			primary_currency = excluded.primary_currency,
			iban = excluded.iban,
			favicon_type = excluded.favicon_type,
			favicon_value = excluded.favicon_value,
			is_closed = excluded.is_closed,
			agent_id = excluded.agent_id,
			updated_at = excluded.updated_at`,
		entityID, nextVer, partyID, name, aType, curr, iban, favType, favVal, isClosed, agentID, now)
	if err != nil {
		return nil, fmt.Errorf("upsert accounts_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &RestoreResult{
		EntityID:            entityID,
		NewVersion:          nextVer,
		RestoredFromVersion: targetVersion,
		Action:              "RESTORE",
	}, nil
}

// -------------------------------------------------------------
// Matching Rules
// -------------------------------------------------------------

type ruleRecord struct {
	id             string
	version        int
	name           string
	priority       int
	matchField     string
	matchOperator  string
	matchPattern   string
	targetAcctID   string
	defaultBucket  sql.NullString
	action         string
	agentID        string
	correlationID  string
	createdAt      int64
}

func loadMatchingRulesHistory(ctx context.Context, db *sql.DB, res *EntityHistoryResult) (*EntityHistoryResult, error) {
	var curVer int
	var curName string
	err := db.QueryRowContext(ctx, `SELECT version, name FROM matching_rules_current WHERE id = ?`, res.EntityID).Scan(&curVer, &curName)
	if err == nil {
		res.CurrentVersion = curVer
		res.EntityName = curName
	} else {
		res.IsDeleted = true
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, version, name, priority, match_field, match_operator, match_pattern, target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at
		FROM matching_rules WHERE id = ? ORDER BY version ASC`, res.EntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ruleRecord
	for rows.Next() {
		var r ruleRecord
		if err := rows.Scan(&r.id, &r.version, &r.name, &r.priority, &r.matchField, &r.matchOperator, &r.matchPattern, &r.targetAcctID, &r.defaultBucket, &r.action, &r.agentID, &r.correlationID, &r.createdAt); err != nil {
			return nil, err
		}
		records = append(records, r)
		if res.EntityName == "" {
			res.EntityName = r.name
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	var items []HistoryItem
	for i, r := range records {
		item := HistoryItem{
			ID:            r.id,
			Version:       r.version,
			Action:        r.action,
			AgentID:       r.agentID,
			CorrelationID: r.correlationID,
			CreatedAt:     r.createdAt,
			CreatedAtTime: time.UnixMicro(r.createdAt).UTC(),
			IsCurrent:     r.version == res.CurrentVersion && !res.IsDeleted,
			Payload: map[string]any{
				"name":              r.name,
				"priority":          r.priority,
				"match_field":       r.matchField,
				"match_operator":    r.matchOperator,
				"match_pattern":     r.matchPattern,
				"default_bucket_id": r.defaultBucket.String,
			},
		}

		if i == 0 {
			item.FieldDiffs = append(item.FieldDiffs,
				FieldDiff{FieldName: "name", Label: "Rule Name", NewValue: r.name},
				FieldDiff{FieldName: "priority", Label: "Priority", NewValue: fmt.Sprint(r.priority)},
				FieldDiff{FieldName: "match_field", Label: "Match Field", NewValue: r.matchField},
				FieldDiff{FieldName: "match_operator", Label: "Operator", NewValue: r.matchOperator},
				FieldDiff{FieldName: "match_pattern", Label: "Pattern", NewValue: r.matchPattern},
			)
		} else {
			prev := records[i-1]
			if prev.name != r.name {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "name", Label: "Rule Name", OldValue: prev.name, NewValue: r.name})
			}
			if prev.priority != r.priority {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "priority", Label: "Priority", OldValue: fmt.Sprint(prev.priority), NewValue: fmt.Sprint(r.priority)})
			}
			if prev.matchField != r.matchField {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "match_field", Label: "Match Field", OldValue: prev.matchField, NewValue: r.matchField})
			}
			if prev.matchOperator != r.matchOperator {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "match_operator", Label: "Operator", OldValue: prev.matchOperator, NewValue: r.matchOperator})
			}
			if prev.matchPattern != r.matchPattern {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "match_pattern", Label: "Pattern", OldValue: prev.matchPattern, NewValue: r.matchPattern})
			}
			if prev.defaultBucket.String != r.defaultBucket.String {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "default_bucket_id", Label: "Default Bucket", OldValue: nullStrVal(prev.defaultBucket), NewValue: nullStrVal(r.defaultBucket)})
			}
		}

		items = append(items, item)
	}

	res.Items = reverseItems(items)
	return res, nil
}

func restoreMatchingRule(ctx context.Context, db *sql.DB, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	var name, mField, mOp, mPat, tAcct string
	var priority int
	var dBucket sql.NullString
	var act string
	err := db.QueryRowContext(ctx, `
		SELECT name, priority, match_field, match_operator, match_pattern, target_counterparty_account_id, default_bucket_id, action
		FROM matching_rules WHERE id = ? AND version = ?`, entityID, targetVersion).Scan(&name, &priority, &mField, &mOp, &mPat, &tAcct, &dBucket, &act)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if act == "SOFT_DELETE" {
		return nil, ErrSoftDelete
	}

	var maxVer int
	db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM matching_rules WHERE id = ?`, entityID).Scan(&maxVer)
	nextVer := maxVer + 1

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO matching_rules (id, version, name, priority, match_field, match_operator, match_pattern, target_counterparty_account_id, default_bucket_id, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'RESTORE', ?, ?, ?)`,
		entityID, nextVer, name, priority, mField, mOp, mPat, tAcct, dBucket, agentID, correlationID, now)
	if err != nil {
		return nil, fmt.Errorf("append matching_rules restore: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO matching_rules_current (id, version, name, priority, match_field, match_operator, match_pattern, target_counterparty_account_id, default_bucket_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			name = excluded.name,
			priority = excluded.priority,
			match_field = excluded.match_field,
			match_operator = excluded.match_operator,
			match_pattern = excluded.match_pattern,
			target_counterparty_account_id = excluded.target_counterparty_account_id,
			default_bucket_id = excluded.default_bucket_id,
			updated_at = excluded.updated_at`,
		entityID, nextVer, name, priority, mField, mOp, mPat, tAcct, dBucket, now)
	if err != nil {
		return nil, fmt.Errorf("upsert matching_rules_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &RestoreResult{
		EntityID:            entityID,
		NewVersion:          nextVer,
		RestoredFromVersion: targetVersion,
		Action:              "RESTORE",
	}, nil
}

// -------------------------------------------------------------
// Raw Transactions
// -------------------------------------------------------------

type rawTxRecord struct {
	id            string
	version       int
	accountID     string
	extTxID       sql.NullString
	dedupHash     string
	bookingDate   int64
	valueDate     sql.NullInt64
	amountMinor   int64
	currency      string
	rawPayee      string
	iban          sql.NullString
	reference     sql.NullString
	status        string
	bookingStatus string
	action        string
	agentID       string
	correlationID string
	createdAt     int64
}

func loadRawTransactionsHistory(ctx context.Context, db *sql.DB, res *EntityHistoryResult) (*EntityHistoryResult, error) {
	var curVer int
	var curPayee string
	err := db.QueryRowContext(ctx, `SELECT version, raw_payee FROM raw_transactions_current WHERE id = ?`, res.EntityID).Scan(&curVer, &curPayee)
	if err == nil {
		res.CurrentVersion = curVer
		res.EntityName = curPayee
	} else {
		res.IsDeleted = true
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date, amount_minor, currency, raw_payee, counterparty_iban, reference, status, booking_status, action, agent_id, correlation_id, created_at
		FROM raw_transactions WHERE id = ? ORDER BY version ASC`, res.EntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []rawTxRecord
	for rows.Next() {
		var r rawTxRecord
		if err := rows.Scan(&r.id, &r.version, &r.accountID, &r.extTxID, &r.dedupHash, &r.bookingDate, &r.valueDate, &r.amountMinor, &r.currency, &r.rawPayee, &r.iban, &r.reference, &r.status, &r.bookingStatus, &r.action, &r.agentID, &r.correlationID, &r.createdAt); err != nil {
			return nil, err
		}
		records = append(records, r)
		if res.EntityName == "" {
			res.EntityName = r.rawPayee
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	var items []HistoryItem
	for i, r := range records {
		item := HistoryItem{
			ID:            r.id,
			Version:       r.version,
			Action:        r.action,
			AgentID:       r.agentID,
			CorrelationID: r.correlationID,
			CreatedAt:     r.createdAt,
			CreatedAtTime: time.UnixMicro(r.createdAt).UTC(),
			IsCurrent:     r.version == res.CurrentVersion && !res.IsDeleted,
			Payload: map[string]any{
				"raw_payee":      r.rawPayee,
				"amount_minor":   r.amountMinor,
				"currency":       r.currency,
				"status":         r.status,
				"booking_status": r.bookingStatus,
				"reference":      r.reference.String,
			},
		}

		if i == 0 {
			item.FieldDiffs = append(item.FieldDiffs,
				FieldDiff{FieldName: "raw_payee", Label: "Payee", NewValue: r.rawPayee},
				FieldDiff{FieldName: "amount", Label: "Amount", NewValue: FormatMinor(r.amountMinor, r.currency)},
				FieldDiff{FieldName: "status", Label: "Status", NewValue: r.status},
				FieldDiff{FieldName: "booking_status", Label: "Booking Status", NewValue: r.bookingStatus},
			)
		} else {
			prev := records[i-1]
			if prev.rawPayee != r.rawPayee {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "raw_payee", Label: "Payee", OldValue: prev.rawPayee, NewValue: r.rawPayee})
			}
			if prev.amountMinor != r.amountMinor || prev.currency != r.currency {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "amount", Label: "Amount", OldValue: FormatMinor(prev.amountMinor, prev.currency), NewValue: FormatMinor(r.amountMinor, r.currency)})
			}
			if prev.status != r.status {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "status", Label: "Status", OldValue: prev.status, NewValue: r.status})
			}
			if prev.bookingStatus != r.bookingStatus {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "booking_status", Label: "Booking Status", OldValue: prev.bookingStatus, NewValue: r.bookingStatus})
			}
		}

		items = append(items, item)
	}

	res.Items = reverseItems(items)
	return res, nil
}

func restoreRawTransaction(ctx context.Context, db *sql.DB, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	var acctID, dedup, curr, payee, status, bStatus, act string
	var extTxID, iban, ref sql.NullString
	var bDate int64
	var vDate sql.NullInt64
	var amt int64
	err := db.QueryRowContext(ctx, `
		SELECT account_id, external_transaction_id, dedup_hash, booking_date, value_date, amount_minor, currency, raw_payee, counterparty_iban, reference, status, booking_status, action
		FROM raw_transactions WHERE id = ? AND version = ?`, entityID, targetVersion).Scan(
		&acctID, &extTxID, &dedup, &bDate, &vDate, &amt, &curr, &payee, &iban, &ref, &status, &bStatus, &act,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if act == "SOFT_DELETE" {
		return nil, ErrSoftDelete
	}

	var maxVer int
	db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM raw_transactions WHERE id = ?`, entityID).Scan(&maxVer)
	nextVer := maxVer + 1

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO raw_transactions (
			id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
			amount_minor, currency, raw_payee, counterparty_iban, reference, status, booking_status,
			action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'RESTORE', ?, ?, ?)`,
		entityID, nextVer, acctID, extTxID, dedup, bDate, vDate,
		amt, curr, payee, iban, ref, status, bStatus,
		agentID, correlationID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("append raw_transactions restore: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO raw_transactions_current (
			id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
			amount_minor, currency, raw_payee, counterparty_iban, reference, status, booking_status, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			account_id = excluded.account_id,
			external_transaction_id = excluded.external_transaction_id,
			dedup_hash = excluded.dedup_hash,
			booking_date = excluded.booking_date,
			value_date = excluded.value_date,
			amount_minor = excluded.amount_minor,
			currency = excluded.currency,
			raw_payee = excluded.raw_payee,
			counterparty_iban = excluded.counterparty_iban,
			reference = excluded.reference,
			status = excluded.status,
			booking_status = excluded.booking_status,
			updated_at = excluded.updated_at`,
		entityID, nextVer, acctID, extTxID, dedup, bDate, vDate,
		amt, curr, payee, iban, ref, status, bStatus, now,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert raw_transactions_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &RestoreResult{
		EntityID:            entityID,
		NewVersion:          nextVer,
		RestoredFromVersion: targetVersion,
		Action:              "RESTORE",
	}, nil
}

// -------------------------------------------------------------
// Budget Transactions
// -------------------------------------------------------------

type budgetTxRecord struct {
	id            string
	version       int
	sourceAcctID  string
	destAcctID    string
	sourceAmount  int64
	sourceCurr    string
	destAmount    int64
	destCurr      string
	bucketID      sql.NullString
	targetID      sql.NullString
	txDate        int64
	notes         sql.NullString
	action        string
	agentID       string
	correlationID string
	createdAt     int64
}

func loadBudgetTransactionsHistory(ctx context.Context, db *sql.DB, res *EntityHistoryResult) (*EntityHistoryResult, error) {
	var curVer int
	var curNotes sql.NullString
	err := db.QueryRowContext(ctx, `SELECT version, notes FROM budget_transactions_current WHERE id = ?`, res.EntityID).Scan(&curVer, &curNotes)
	if err == nil {
		res.CurrentVersion = curVer
		if curNotes.Valid && curNotes.String != "" {
			res.EntityName = curNotes.String
		} else {
			res.EntityName = "Budget Transaction " + res.EntityID[:8]
		}
	} else {
		res.IsDeleted = true
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
		       destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
		       action, agent_id, correlation_id, created_at
		FROM budget_transactions WHERE id = ? ORDER BY version ASC`, res.EntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []budgetTxRecord
	for rows.Next() {
		var r budgetTxRecord
		if err := rows.Scan(&r.id, &r.version, &r.sourceAcctID, &r.destAcctID, &r.sourceAmount, &r.sourceCurr, &r.destAmount, &r.destCurr, &r.bucketID, &r.targetID, &r.txDate, &r.notes, &r.action, &r.agentID, &r.correlationID, &r.createdAt); err != nil {
			return nil, err
		}
		records = append(records, r)
		if res.EntityName == "" {
			if r.notes.Valid && r.notes.String != "" {
				res.EntityName = r.notes.String
			} else {
				res.EntityName = "Budget Transaction " + res.EntityID[:8]
			}
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	var items []HistoryItem
	for i, r := range records {
		item := HistoryItem{
			ID:            r.id,
			Version:       r.version,
			Action:        r.action,
			AgentID:       r.agentID,
			CorrelationID: r.correlationID,
			CreatedAt:     r.createdAt,
			CreatedAtTime: time.UnixMicro(r.createdAt).UTC(),
			IsCurrent:     r.version == res.CurrentVersion && !res.IsDeleted,
			Payload: map[string]any{
				"source_amount_minor": r.sourceAmount,
				"source_currency":     r.sourceCurr,
				"dest_amount_minor":   r.destAmount,
				"dest_currency":       r.destCurr,
				"bucket_id":           r.bucketID.String,
				"transaction_date":    r.txDate,
				"notes":               r.notes.String,
			},
		}

		if i == 0 {
			item.FieldDiffs = append(item.FieldDiffs,
				FieldDiff{FieldName: "source_amount", Label: "Amount", NewValue: FormatMinor(r.sourceAmount, r.sourceCurr)},
				FieldDiff{FieldName: "transaction_date", Label: "Date", NewValue: FormatEpochMicro(r.txDate)},
			)
			if r.notes.Valid && r.notes.String != "" {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "notes", Label: "Notes", NewValue: r.notes.String})
			}
			if r.bucketID.Valid && r.bucketID.String != "" {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "bucket_id", Label: "Bucket", NewValue: r.bucketID.String})
			}
		} else {
			prev := records[i-1]
			if prev.sourceAmount != r.sourceAmount || prev.sourceCurr != r.sourceCurr {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "source_amount", Label: "Amount", OldValue: FormatMinor(prev.sourceAmount, prev.sourceCurr), NewValue: FormatMinor(r.sourceAmount, r.sourceCurr)})
			}
			if prev.notes.String != r.notes.String {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "notes", Label: "Notes", OldValue: nullStrVal(prev.notes), NewValue: nullStrVal(r.notes)})
			}
			if prev.bucketID.String != r.bucketID.String {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "bucket_id", Label: "Bucket", OldValue: nullStrVal(prev.bucketID), NewValue: nullStrVal(r.bucketID)})
			}
			if prev.txDate != r.txDate {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "transaction_date", Label: "Date", OldValue: FormatEpochMicro(prev.txDate), NewValue: FormatEpochMicro(r.txDate)})
			}
		}

		items = append(items, item)
	}

	res.Items = reverseItems(items)
	return res, nil
}

func restoreBudgetTransaction(ctx context.Context, db *sql.DB, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	// Fetch target version data
	var srcAcct, dstAcct, srcCurr, dstCurr, act string
	var srcAmt, dstAmt, txDate int64
	var bucketID, targetID, notes sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT source_account_id, destination_account_id, source_amount_minor, source_currency,
		       destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes, action
		FROM budget_transactions WHERE id = ? AND version = ?`, entityID, targetVersion).Scan(
		&srcAcct, &dstAcct, &srcAmt, &srcCurr, &dstAmt, &dstCurr, &bucketID, &targetID, &txDate, &notes, &act,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if act == "SOFT_DELETE" {
		return nil, ErrSoftDelete
	}

	var maxVer int
	db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM budget_transactions WHERE id = ?`, entityID).Scan(&maxVer)
	nextVer := maxVer + 1

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. If there is currently an active version in budget_transactions_current, revert its balance effects
	var curSrcAcct, curDstAcct, curSrcCurr, curDstCurr string
	var curSrcAmt, curDstAmt int64
	var curBucketID sql.NullString
	hasActive := false
	err = tx.QueryRowContext(ctx, `
		SELECT source_account_id, destination_account_id, source_amount_minor, source_currency,
		       destination_amount_minor, destination_currency, bucket_id
		FROM budget_transactions_current WHERE id = ?`, entityID).Scan(
		&curSrcAcct, &curDstAcct, &curSrcAmt, &curSrcCurr, &curDstAmt, &curDstCurr, &curBucketID,
	)
	if err == nil {
		hasActive = true
		var oldSrcUserOwned, oldDstUserOwned int
		tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, curSrcAcct).Scan(&oldSrcUserOwned)
		tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, curDstAcct).Scan(&oldDstUserOwned)

		// Revert account balances (add back source, subtract dest)
		if err := transaction.AdjustAccountBalance(ctx, tx, curSrcAcct, curSrcCurr, curSrcAmt, now); err != nil {
			return nil, fmt.Errorf("revert old src balance: %w", err)
		}
		if err := transaction.AdjustAccountBalance(ctx, tx, curDstAcct, curDstCurr, -curDstAmt, now); err != nil {
			return nil, fmt.Errorf("revert old dst balance: %w", err)
		}
		if curBucketID.Valid && curBucketID.String != "" && oldSrcUserOwned == 1 && oldDstUserOwned == 0 {
			if err := transaction.AdjustBucketBalance(ctx, tx, curBucketID.String, curSrcCurr, curSrcAmt); err != nil {
				return nil, fmt.Errorf("revert old bucket balance: %w", err)
			}
		}
	}

	_ = hasActive

	// 2. Apply target version balance effects
	var newSrcUserOwned, newDstUserOwned int
	tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, srcAcct).Scan(&newSrcUserOwned)
	tx.QueryRowContext(ctx, `SELECT p.is_user_owned FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE a.id = ?`, dstAcct).Scan(&newDstUserOwned)

	if err := transaction.AdjustAccountBalance(ctx, tx, srcAcct, srcCurr, -srcAmt, now); err != nil {
		return nil, fmt.Errorf("apply restored src balance: %w", err)
	}
	if err := transaction.AdjustAccountBalance(ctx, tx, dstAcct, dstCurr, dstAmt, now); err != nil {
		return nil, fmt.Errorf("apply restored dst balance: %w", err)
	}
	if bucketID.Valid && bucketID.String != "" && newSrcUserOwned == 1 && newDstUserOwned == 0 {
		if err := transaction.AdjustBucketBalance(ctx, tx, bucketID.String, srcCurr, -srcAmt); err != nil {
			return nil, fmt.Errorf("apply restored bucket balance: %w", err)
		}
	}

	// 3. Append to historical append-only table
	_, err = tx.ExecContext(ctx, `
		INSERT INTO budget_transactions (
			id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
			destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes,
			action, agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'RESTORE', ?, ?, ?)`,
		entityID, nextVer, srcAcct, dstAcct, srcAmt, srcCurr, dstAmt, dstCurr, bucketID, targetID, txDate, notes,
		agentID, correlationID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("append budget_transactions restore: %w", err)
	}

	// 4. Upsert current projection
	_, err = tx.ExecContext(ctx, `
		INSERT INTO budget_transactions_current (
			id, version, source_account_id, destination_account_id, source_amount_minor, source_currency,
			destination_amount_minor, destination_currency, bucket_id, target_id, transaction_date, notes, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			source_account_id = excluded.source_account_id,
			destination_account_id = excluded.destination_account_id,
			source_amount_minor = excluded.source_amount_minor,
			source_currency = excluded.source_currency,
			destination_amount_minor = excluded.destination_amount_minor,
			destination_currency = excluded.destination_currency,
			bucket_id = excluded.bucket_id,
			target_id = excluded.target_id,
			transaction_date = excluded.transaction_date,
			notes = excluded.notes,
			updated_at = excluded.updated_at`,
		entityID, nextVer, srcAcct, dstAcct, srcAmt, srcCurr, dstAmt, dstCurr, bucketID, targetID, txDate, notes, now,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert budget_transactions_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &RestoreResult{
		EntityID:            entityID,
		NewVersion:          nextVer,
		RestoredFromVersion: targetVersion,
		Action:              "RESTORE",
	}, nil
}

// -------------------------------------------------------------
// Saved Reports
// -------------------------------------------------------------

type savedReportRecord struct {
	id            string
	version       int
	name          string
	reportType    string
	configJSON    string
	isPinned      int
	action        string
	agentID       string
	correlationID string
	createdAt     int64
}

func loadSavedReportsHistory(ctx context.Context, db *sql.DB, res *EntityHistoryResult) (*EntityHistoryResult, error) {
	var curVer int
	var curName string
	err := db.QueryRowContext(ctx, `SELECT version, name FROM saved_reports_current WHERE id = ?`, res.EntityID).Scan(&curVer, &curName)
	if err == nil {
		res.CurrentVersion = curVer
		res.EntityName = curName
	} else {
		res.IsDeleted = true
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, version, name, report_type, config_json, is_pinned_widget, action, agent_id, correlation_id, created_at
		FROM saved_reports WHERE id = ? ORDER BY version ASC`, res.EntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []savedReportRecord
	for rows.Next() {
		var r savedReportRecord
		if err := rows.Scan(&r.id, &r.version, &r.name, &r.reportType, &r.configJSON, &r.isPinned, &r.action, &r.agentID, &r.correlationID, &r.createdAt); err != nil {
			return nil, err
		}
		records = append(records, r)
		if res.EntityName == "" {
			res.EntityName = r.name
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	var items []HistoryItem
	for i, r := range records {
		item := HistoryItem{
			ID:            r.id,
			Version:       r.version,
			Action:        r.action,
			AgentID:       r.agentID,
			CorrelationID: r.correlationID,
			CreatedAt:     r.createdAt,
			CreatedAtTime: time.UnixMicro(r.createdAt).UTC(),
			IsCurrent:     r.version == res.CurrentVersion && !res.IsDeleted,
			Payload: map[string]any{
				"name":             r.name,
				"report_type":      r.reportType,
				"is_pinned_widget": r.isPinned,
			},
		}

		if i == 0 {
			item.FieldDiffs = append(item.FieldDiffs,
				FieldDiff{FieldName: "name", Label: "Report Name", NewValue: r.name},
				FieldDiff{FieldName: "report_type", Label: "Report Type", NewValue: r.reportType},
			)
		} else {
			prev := records[i-1]
			if prev.name != r.name {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "name", Label: "Report Name", OldValue: prev.name, NewValue: r.name})
			}
			if prev.isPinned != r.isPinned {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "is_pinned_widget", Label: "Pinned Widget", OldValue: boolToStr(prev.isPinned == 1), NewValue: boolToStr(r.isPinned == 1)})
			}
			if prev.configJSON != r.configJSON {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "config_json", Label: "Configuration", OldValue: "(modified)", NewValue: "(updated)"})
			}
		}

		items = append(items, item)
	}

	res.Items = reverseItems(items)
	return res, nil
}

func restoreSavedReport(ctx context.Context, db *sql.DB, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	var name, rType, cfg string
	var isPinned int
	var act string
	err := db.QueryRowContext(ctx, `
		SELECT name, report_type, config_json, is_pinned_widget, action
		FROM saved_reports WHERE id = ? AND version = ?`, entityID, targetVersion).Scan(&name, &rType, &cfg, &isPinned, &act)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if act == "SOFT_DELETE" {
		return nil, ErrSoftDelete
	}

	var maxVer int
	db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM saved_reports WHERE id = ?`, entityID).Scan(&maxVer)
	nextVer := maxVer + 1

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO saved_reports (id, version, name, report_type, config_json, is_pinned_widget, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'RESTORE', ?, ?, ?)`,
		entityID, nextVer, name, rType, cfg, isPinned, agentID, correlationID, now)
	if err != nil {
		return nil, fmt.Errorf("append saved_reports restore: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO saved_reports_current (id, version, name, report_type, config_json, is_pinned_widget, agent_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			name = excluded.name,
			report_type = excluded.report_type,
			config_json = excluded.config_json,
			is_pinned_widget = excluded.is_pinned_widget,
			agent_id = excluded.agent_id,
			updated_at = excluded.updated_at`,
		entityID, nextVer, name, rType, cfg, isPinned, agentID, now)
	if err != nil {
		return nil, fmt.Errorf("upsert saved_reports_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &RestoreResult{
		EntityID:            entityID,
		NewVersion:          nextVer,
		RestoredFromVersion: targetVersion,
		Action:              "RESTORE",
	}, nil
}

// -------------------------------------------------------------
// Automations
// -------------------------------------------------------------

type automationRecord struct {
	id             string
	version        int
	name           string
	judgeScript    string
	executorScript string
	comment        string
	action         string
	agentID        string
	correlationID  string
	createdAt      int64
}

func loadAutomationsHistory(ctx context.Context, db *sql.DB, res *EntityHistoryResult) (*EntityHistoryResult, error) {
	var curVer int
	var curName string
	err := db.QueryRowContext(ctx, `SELECT version, name FROM automations_current WHERE id = ?`, res.EntityID).Scan(&curVer, &curName)
	if err == nil {
		res.CurrentVersion = curVer
		res.EntityName = curName
	} else {
		res.IsDeleted = true
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, version, name, judge_script, executor_script, comment, action, agent_id, correlation_id, created_at
		FROM automations WHERE id = ? ORDER BY version ASC`, res.EntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []automationRecord
	for rows.Next() {
		var r automationRecord
		if err := rows.Scan(&r.id, &r.version, &r.name, &r.judgeScript, &r.executorScript, &r.comment, &r.action, &r.agentID, &r.correlationID, &r.createdAt); err != nil {
			return nil, err
		}
		records = append(records, r)
		if res.EntityName == "" {
			res.EntityName = r.name
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	var items []HistoryItem
	for i, r := range records {
		item := HistoryItem{
			ID:            r.id,
			Version:       r.version,
			Action:        r.action,
			AgentID:       r.agentID,
			CorrelationID: r.correlationID,
			CreatedAt:     r.createdAt,
			CreatedAtTime: time.UnixMicro(r.createdAt).UTC(),
			IsCurrent:     r.version == res.CurrentVersion && !res.IsDeleted,
			Payload: map[string]any{
				"name":            r.name,
				"judge_script":    r.judgeScript,
				"executor_script": r.executorScript,
				"comment":         r.comment,
			},
		}

		if i == 0 {
			item.FieldDiffs = append(item.FieldDiffs,
				FieldDiff{FieldName: "name", Label: "Rule Name", NewValue: r.name},
				FieldDiff{FieldName: "judge_script", Label: "Judge Script", NewValue: r.judgeScript},
				FieldDiff{FieldName: "executor_script", Label: "Executor Script", NewValue: r.executorScript},
			)
			if r.comment != "" {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "comment", Label: "Comment", NewValue: r.comment})
			}
		} else {
			prev := records[i-1]
			if prev.name != r.name {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "name", Label: "Rule Name", OldValue: prev.name, NewValue: r.name})
			}
			if prev.judgeScript != r.judgeScript {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "judge_script", Label: "Judge Script", OldValue: prev.judgeScript, NewValue: r.judgeScript})
			}
			if prev.executorScript != r.executorScript {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "executor_script", Label: "Executor Script", OldValue: prev.executorScript, NewValue: r.executorScript})
			}
			if prev.comment != r.comment {
				item.FieldDiffs = append(item.FieldDiffs, FieldDiff{FieldName: "comment", Label: "Comment", OldValue: prev.comment, NewValue: r.comment})
			}
		}

		items = append(items, item)
	}

	res.Items = reverseItems(items)
	return res, nil
}

func restoreAutomation(ctx context.Context, db *sql.DB, entityID string, targetVersion int, agentID, correlationID string, now int64) (*RestoreResult, error) {
	var name, judgeScript, executorScript, comment, act string
	err := db.QueryRowContext(ctx, `
		SELECT name, judge_script, executor_script, comment, action
		FROM automations WHERE id = ? AND version = ?`, entityID, targetVersion).Scan(&name, &judgeScript, &executorScript, &comment, &act)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if act == "SOFT_DELETE" {
		return nil, ErrSoftDelete
	}

	var maxVer int
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM automations WHERE id = ?`, entityID).Scan(&maxVer)
	nextVer := maxVer + 1

	var isActive, sortOrder int = 1, 0
	_ = db.QueryRowContext(ctx, `SELECT is_active, sort_order FROM automations_current WHERE id = ?`, entityID).Scan(&isActive, &sortOrder)

	restoreComment := fmt.Sprintf("Restored from version %d", targetVersion)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO automations (id, version, name, judge_script, executor_script, comment, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'RESTORE', ?, ?, ?)`,
		entityID, nextVer, name, judgeScript, executorScript, restoreComment, agentID, correlationID, now)
	if err != nil {
		return nil, fmt.Errorf("append automations restore: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO automations_current (id, version, name, judge_script, executor_script, comment, is_active, sort_order, agent_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			name = excluded.name,
			judge_script = excluded.judge_script,
			executor_script = excluded.executor_script,
			comment = excluded.comment,
			is_active = excluded.is_active,
			sort_order = excluded.sort_order,
			agent_id = excluded.agent_id,
			updated_at = excluded.updated_at`,
		entityID, nextVer, name, judgeScript, executorScript, restoreComment, isActive, sortOrder, agentID, now)
	if err != nil {
		return nil, fmt.Errorf("upsert automations_current: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &RestoreResult{
		EntityID:            entityID,
		NewVersion:          nextVer,
		RestoredFromVersion: targetVersion,
		Action:              "RESTORE",
	}, nil
}

// -------------------------------------------------------------
// Helpers
// -------------------------------------------------------------

func reverseItems(items []HistoryItem) []HistoryItem {
	n := len(items)
	res := make([]HistoryItem, n)
	for i, item := range items {
		res[n-1-i] = item
	}
	return res
}

func boolToStr(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func closedToStr(closed int) string {
	if closed == 1 {
		return "Closed"
	}
	return "Active"
}

func nullStrVal(ns sql.NullString) string {
	if ns.Valid && ns.String != "" {
		return ns.String
	}
	return "-"
}
