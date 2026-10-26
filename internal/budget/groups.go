package budget

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
)

// BucketGroup represents a user-defined category/group of envelopes with an explicit ordering.
type BucketGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EnsureBucketGroupsSync synchronizes any distinct groups from buckets_projection into bucket_groups.
func EnsureBucketGroupsSync(ctx context.Context, tenantDB *sql.DB) error {
	_, err := tenantDB.ExecContext(ctx, `
		INSERT OR IGNORE INTO bucket_groups (id, name, sort_order, created_at, updated_at)
		SELECT 
			lower(hex(randomblob(16))),
			trim(group_name),
			COALESCE((SELECT MAX(sort_order) + 1 FROM bucket_groups), 0) + row_number() OVER (ORDER BY group_name ASC) - 1,
			cast(strftime('%s', 'now') as integer) * 1000000,
			cast(strftime('%s', 'now') as integer) * 1000000
		FROM (
			SELECT DISTINCT group_name 
			FROM buckets_projection 
			WHERE trim(group_name) != '' 
			  AND trim(group_name) NOT IN (SELECT name FROM bucket_groups)
		)
		ORDER BY group_name ASC;
	`)
	return err
}

// ListBucketGroups returns all bucket groups sorted by sort_order ASC, name ASC.
func ListBucketGroups(ctx context.Context, tenantDB *sql.DB) ([]BucketGroup, error) {
	if err := EnsureBucketGroupsSync(ctx, tenantDB); err != nil {
		return nil, err
	}

	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, name, sort_order, created_at, updated_at
		FROM bucket_groups
		ORDER BY sort_order ASC, name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed querying bucket_groups: %w", err)
	}
	defer rows.Close()

	var groups []BucketGroup
	for rows.Next() {
		var g BucketGroup
		var createdAtMicros, updatedAtMicros int64
		if err := rows.Scan(&g.ID, &g.Name, &g.SortOrder, &createdAtMicros, &updatedAtMicros); err != nil {
			return nil, fmt.Errorf("failed scanning bucket_group: %w", err)
		}
		g.CreatedAt = time.UnixMicro(createdAtMicros).UTC()
		g.UpdatedAt = time.UnixMicro(updatedAtMicros).UTC()
		groups = append(groups, g)
	}
	return groups, nil
}

// RenameBucketGroupInput represents parameters for renaming a bucket group.
type RenameBucketGroupInput struct {
	OldName       string
	NewName       string
	AgentID       string
	CorrelationID string
}

// RenameBucketGroup renames a bucket group and cascades the change to all member envelopes in buckets_projection.
func RenameBucketGroup(ctx context.Context, tenantDB *sql.DB, input RenameBucketGroupInput) error {
	oldName := strings.TrimSpace(input.OldName)
	newName := strings.TrimSpace(input.NewName)

	if oldName == "" || newName == "" {
		return errors.New("both old and new group names are required")
	}
	if oldName == newName {
		return nil
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()

	// 1. Ensure bucket_groups has oldName
	var groupID string
	var groupSortOrder int
	err = tx.QueryRowContext(ctx, `SELECT id, sort_order FROM bucket_groups WHERE name = ?`, oldName).Scan(&groupID, &groupSortOrder)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Insert on the fly if not existing
			groupID = domain.NewUUIDv7()
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM bucket_groups`).Scan(&groupSortOrder)
			_, err = tx.ExecContext(ctx, `
				INSERT INTO bucket_groups (id, name, sort_order, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)`,
				groupID, oldName, groupSortOrder, now, now,
			)
			if err != nil {
				return fmt.Errorf("failed creating initial bucket group: %w", err)
			}
		} else {
			return fmt.Errorf("failed querying bucket_group: %w", err)
		}
	}

	// 2. Check if newName already exists in bucket_groups
	var existingWithNewName int
	_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM bucket_groups WHERE name = ? AND id != ?`, newName, groupID).Scan(&existingWithNewName)
	if existingWithNewName > 0 {
		// If destination group already exists, merge this group into the destination group
		_, err = tx.ExecContext(ctx, `DELETE FROM bucket_groups WHERE id = ?`, groupID)
		if err != nil {
			return fmt.Errorf("failed removing merged group from bucket_groups: %w", err)
		}
	} else {
		// Update bucket_groups name
		_, err = tx.ExecContext(ctx, `
			UPDATE bucket_groups
			SET name = ?, updated_at = ?
			WHERE id = ?`,
			newName, now, groupID,
		)
		if err != nil {
			return fmt.Errorf("failed updating bucket_group name: %w", err)
		}
	}

	// 3. Find all affected buckets in buckets_projection
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, version
		FROM buckets_projection
		WHERE group_name = ? AND is_archived = 0`, oldName)
	if err != nil {
		return fmt.Errorf("failed querying member buckets: %w", err)
	}
	defer rows.Close()

	type bucketToUpdate struct {
		id      string
		name    string
		version int
	}
	var buckets []bucketToUpdate
	for rows.Next() {
		var b bucketToUpdate
		if err := rows.Scan(&b.id, &b.name, &b.version); err != nil {
			return fmt.Errorf("failed scanning member bucket: %w", err)
		}
		buckets = append(buckets, b)
	}
	rows.Close()

	// 4. Update buckets and write events
	for _, b := range buckets {
		newVersion := b.version + 1
		eventID := domain.NewUUIDv7()

		payload, err := json.Marshal(map[string]any{
			"bucket_id":  b.id,
			"group_name": newName,
			"name":       b.name,
		})
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			eventID, b.id, newVersion, EventTypeBucketUpdated, string(payload), input.AgentID, input.CorrelationID, now,
		)
		if err != nil {
			return fmt.Errorf("failed writing bucket event: %w", err)
		}

		_, err = tx.ExecContext(ctx, `
			UPDATE buckets_projection
			SET group_name = ?, version = ?, updated_at = ?
			WHERE id = ?`,
			newName, newVersion, now, b.id,
		)
		if err != nil {
			return fmt.Errorf("failed updating buckets_projection: %w", err)
		}
	}

	return tx.Commit()
}

// ReorderBucketGroups updates sort_order for a given ordered slice of group names.
func ReorderBucketGroups(ctx context.Context, tenantDB *sql.DB, orderedNames []string) error {
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().UnixMicro()
	for idx, name := range orderedNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO bucket_groups (id, name, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET sort_order = excluded.sort_order, updated_at = excluded.updated_at`,
			domain.NewUUIDv7(), trimmed, idx, now, now,
		)
		if err != nil {
			return fmt.Errorf("failed setting sort_order for group %s: %w", trimmed, err)
		}
	}

	return tx.Commit()
}

// MoveBucketGroup moves a group up or down in sort_order.
func MoveBucketGroup(ctx context.Context, tenantDB *sql.DB, groupName, direction string) error {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return errors.New("group name cannot be empty")
	}

	groups, err := ListBucketGroups(ctx, tenantDB)
	if err != nil {
		return err
	}

	idx := -1
	for i, g := range groups {
		if g.Name == groupName {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("group %q not found", groupName)
	}

	var targetIdx int
	if direction == "up" {
		if idx == 0 {
			return nil // already at top
		}
		targetIdx = idx - 1
	} else if direction == "down" {
		if idx == len(groups)-1 {
			return nil // already at bottom
		}
		targetIdx = idx + 1
	} else {
		return fmt.Errorf("invalid direction: %s", direction)
	}

	groups[idx], groups[targetIdx] = groups[targetIdx], groups[idx]
	var orderedNames []string
	for _, g := range groups {
		orderedNames = append(orderedNames, g.Name)
	}

	return ReorderBucketGroups(ctx, tenantDB, orderedNames)
}
