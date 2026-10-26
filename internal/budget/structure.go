package budget

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/domain"
)

// ApplyBudgetStructure applies a hierarchical budget structure (groups, buckets, targets) to a tenant database in a single atomic transaction.
// Supports conflict resolution for existing buckets: MERGE (default), REPLACE, or SKIP.
// If any error occurs, the entire transaction is rolled back so that all items are created atomically or none.
func ApplyBudgetStructure(ctx context.Context, tenantDB *sql.DB, structure *ai.BudgetStructure, agentID, correlationID string) (int, int, error) {
	if structure == nil || len(structure.Groups) == 0 {
		return 0, 0, nil
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed starting transaction for budget structure: %w", err)
	}
	defer tx.Rollback()

	bucketsCreated := 0
	targetsCreated := 0

	for _, group := range structure.Groups {
		groupName := strings.TrimSpace(group.GroupName)
		if groupName == "" {
			groupName = "Default"
		}

		for _, proposedBucket := range group.Buckets {
			bucketName := strings.TrimSpace(proposedBucket.Name)
			if bucketName == "" {
				continue
			}

			// Check if bucket with the same name already exists in this transaction
			var bucketID, existingGroup string
			var bucketVersion int
			err := tx.QueryRowContext(ctx, `SELECT id, group_name, version FROM buckets_projection WHERE lower(trim(name)) = lower(trim(?)) AND is_archived = 0 LIMIT 1`, bucketName).Scan(&bucketID, &existingGroup, &bucketVersion)
			if err != nil && err != sql.ErrNoRows {
				return 0, 0, fmt.Errorf("failed checking existing bucket '%s': %w", bucketName, err)
			}

			conflictRes := strings.ToUpper(strings.TrimSpace(proposedBucket.ConflictResolution))
			if conflictRes == "" {
				conflictRes = "MERGE"
			}

			if bucketID != "" {
				if conflictRes == "SKIP" {
					continue
				}

				// If user changed the group name, update it in buckets_projection
				if groupName != "" && existingGroup != "" && groupName != existingGroup {
					now := time.Now().UTC().UnixMicro()
					newBucketVersion := bucketVersion + 1
					eventID := domain.NewUUIDv7()
					payload, _ := json.Marshal(map[string]any{
						"bucket_id":  bucketID,
						"group_name": groupName,
						"name":       bucketName,
					})
					_, err = tx.ExecContext(ctx, `
						INSERT INTO bucket_events (event_id, aggregate_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						eventID, bucketID, newBucketVersion, EventTypeBucketUpdated, string(payload), agentID, correlationID, now,
					)
					if err != nil {
						return 0, 0, fmt.Errorf("failed writing bucket event: %w", err)
					}
					_, err = tx.ExecContext(ctx, `
						UPDATE buckets_projection
						SET group_name = ?, version = ?, updated_at = ?
						WHERE id = ?`,
						groupName, newBucketVersion, now, bucketID,
					)
					if err != nil {
						return 0, 0, fmt.Errorf("failed updating bucket group: %w", err)
					}
				}

				if conflictRes == "REPLACE" {
					// Archive all active targets for this bucket
					tRows, err := tx.QueryContext(ctx, `SELECT id, version FROM targets_projection WHERE bucket_id = ? AND is_archived = 0`, bucketID)
					if err != nil {
						return 0, 0, fmt.Errorf("failed querying targets for replace in bucket '%s': %w", bucketName, err)
					}
					type oldTarget struct {
						id      string
						version int
					}
					var oldTargets []oldTarget
					for tRows.Next() {
						var ot oldTarget
						if err := tRows.Scan(&ot.id, &ot.version); err != nil {
							tRows.Close()
							return 0, 0, err
						}
						oldTargets = append(oldTargets, ot)
					}
					tRows.Close()

					now := time.Now().UTC().UnixMicro()
					for _, ot := range oldTargets {
						newTVersion := ot.version + 1
						tEventID := domain.NewUUIDv7()
						tPayload, _ := json.Marshal(map[string]any{"target_id": ot.id})
						_, err = tx.ExecContext(ctx, `
							INSERT INTO target_events (event_id, aggregate_id, bucket_id, version, event_type, payload, agent_id, correlation_id, occurred_at)
							VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
							tEventID, ot.id, bucketID, newTVersion, EventTypeTargetArchived, string(tPayload), agentID, correlationID, now,
						)
						if err != nil {
							return 0, 0, fmt.Errorf("failed archiving target event: %w", err)
						}
						_, err = tx.ExecContext(ctx, `
							UPDATE targets_projection
							SET is_archived = 1, version = ?, updated_at = ?
							WHERE id = ?`,
							newTVersion, now, ot.id,
						)
						if err != nil {
							return 0, 0, fmt.Errorf("failed archiving target '%s': %w", ot.id, err)
						}
					}
				}
			} else {
				newID, err := CreateBucketTx(ctx, tx, CreateBucketInput{
					GroupName:     groupName,
					Name:          bucketName,
					AgentID:       agentID,
					CorrelationID: correlationID,
				})
				if err != nil {
					return 0, 0, fmt.Errorf("failed creating bucket '%s': %w", bucketName, err)
				}
				bucketID = newID
				bucketsCreated++
			}

			// Create targets
			for idx, proposedTarget := range proposedBucket.Targets {
				targetName := strings.TrimSpace(proposedTarget.Name)

				if targetName != "" && conflictRes != "REPLACE" {
					// Check if target with the same name already exists in this bucket in this transaction
					var targetCount int
					err := tx.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection WHERE bucket_id = ? AND lower(trim(name)) = lower(trim(?)) AND is_archived = 0`, bucketID, targetName).Scan(&targetCount)
					if err != nil {
						return 0, 0, fmt.Errorf("failed checking existing target '%s': %w", targetName, err)
					}
					if targetCount > 0 {
						// Skip existing target
						continue
					}
				}

				tType := proposedTarget.TargetType
				if tType != "EXPENSE" && tType != "SAVINGS" && tType != "DEBT_PAYMENT" {
					tType = "EXPENSE"
				}

				prio := proposedTarget.Priority
				if prio < 1 {
					prio = 2
				}

				cadence := proposedTarget.RecurrenceCadence
				if cadence == "" {
					cadence = "MONTHLY"
				}

				strategy := proposedTarget.NonSpendingStrategy
				if strategy == "" {
					if tType == "SAVINGS" {
						strategy = "ROLL_OVER"
					} else {
						strategy = "RETURN_TO_POOL"
					}
				}

				currency := strings.ToUpper(strings.TrimSpace(proposedTarget.Currency))
				if currency == "" {
					currency = "PLN"
				}

				rInterval := 0
				if proposedTarget.RecurrenceInterval != nil {
					rInterval = *proposedTarget.RecurrenceInterval
				} else if proposedTarget.RecurrenceDays != nil {
					rInterval = *proposedTarget.RecurrenceDays
				}
				isCustom := cadence == "CUSTOM_DAYS" || cadence == "CUSTOM_MONTHS" || cadence == "CUSTOM_YEARS"
				if isCustom && rInterval < 1 {
					switch cadence {
					case "CUSTOM_MONTHS":
						rInterval = 3
					case "CUSTOM_YEARS":
						rInterval = 1
					default:
						lowerName := strings.ToLower(targetName)
						if strings.Contains(lowerName, "kwartal") || strings.Contains(lowerName, "3 mies") || strings.Contains(lowerName, "quarter") {
							rInterval = 90
						} else if strings.Contains(lowerName, "półrocz") || strings.Contains(lowerName, "polrocz") || strings.Contains(lowerName, "6 mies") || strings.Contains(lowerName, "half") {
							rInterval = 180
						} else if strings.Contains(lowerName, "dwutyg") || strings.Contains(lowerName, "2 tyg") || strings.Contains(lowerName, "bi-week") {
							rInterval = 14
						} else {
							rInterval = 90
						}
					}
				} else if !isCustom {
					rInterval = 0
				}

				var dueDate *int64
				if cadence == "CUSTOM_MONTHS" || cadence == "CUSTOM_YEARS" {
					nowT := time.Now().UTC()
					day := 1
					if proposedTarget.DueDateDay != nil && *proposedTarget.DueDateDay >= 1 && *proposedTarget.DueDateDay <= 31 {
						day = *proposedTarget.DueDateDay
					}
					anchor := time.Date(nowT.Year(), nowT.Month(), day, 0, 0, 0, 0, time.UTC)
					if anchor.Before(nowT) {
						anchor = anchor.AddDate(0, 1, 0)
					}
					u := anchor.UnixMicro()
					dueDate = &u
				}

				_, err = CreateTargetTx(ctx, tx, CreateTargetInput{
					BucketID:            bucketID,
					Name:                targetName,
					TargetType:          tType,
					Priority:            prio,
					SortOrder:           idx,
					AmountMinor:         proposedTarget.AmountMinor,
					Currency:            currency,
					RecurrenceCadence:   cadence,
					RecurrenceInterval:  rInterval,
					DueDate:             dueDate,
					NonSpendingStrategy: strategy,
					AgentID:             agentID,
					CorrelationID:       correlationID,
				})
				if err != nil {
					return 0, 0, fmt.Errorf("failed creating target '%s': %w", targetName, err)
				}
				targetsCreated++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("failed committing budget structure transaction: %w", err)
	}

	return bucketsCreated, targetsCreated, nil
}

// ExportBudgetStructure reads all active groups, buckets, and targets from tenantDB into an ai.BudgetStructure.
func ExportBudgetStructure(ctx context.Context, tenantDB *sql.DB) (*ai.BudgetStructure, error) {
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, group_name, name 
		FROM buckets_projection 
		WHERE is_archived = 0 
		ORDER BY group_name ASC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("failed querying buckets: %w", err)
	}
	defer rows.Close()

	type bucketInfo struct {
		id        string
		groupName string
		name      string
	}
	var buckets []bucketInfo
	for rows.Next() {
		var b bucketInfo
		if err := rows.Scan(&b.id, &b.groupName, &b.name); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}

	// Fetch targets
	targetRows, err := tenantDB.QueryContext(ctx, `
		SELECT bucket_id, COALESCE(name, ''), target_type, priority, amount_minor, currency, recurrence_cadence, recurrence_interval, non_spending_strategy
		FROM targets_projection
		WHERE is_archived = 0
		ORDER BY priority DESC, sort_order ASC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("failed querying targets: %w", err)
	}
	defer targetRows.Close()

	targetsByBucket := make(map[string][]ai.ProposedTarget)
	for targetRows.Next() {
		var bucketID string
		var t ai.ProposedTarget
		var rInterval sql.NullInt64
		if err := targetRows.Scan(&bucketID, &t.Name, &t.TargetType, &t.Priority, &t.AmountMinor, &t.Currency, &t.RecurrenceCadence, &rInterval, &t.NonSpendingStrategy); err != nil {
			return nil, err
		}
		if rInterval.Valid {
			interval := int(rInterval.Int64)
			t.RecurrenceInterval = &interval
			t.RecurrenceDays = &interval
		}
		targetsByBucket[bucketID] = append(targetsByBucket[bucketID], t)
	}

	groupsMap := make(map[string][]ai.ProposedBucket)
	var groupNamesInOrder []string
	seenGroup := make(map[string]bool)

	for _, b := range buckets {
		if !seenGroup[b.groupName] {
			seenGroup[b.groupName] = true
			groupNamesInOrder = append(groupNamesInOrder, b.groupName)
		}
		pBucket := ai.ProposedBucket{
			Name:    b.name,
			Targets: targetsByBucket[b.id],
		}
		if pBucket.Targets == nil {
			pBucket.Targets = []ai.ProposedTarget{}
		}
		groupsMap[b.groupName] = append(groupsMap[b.groupName], pBucket)
	}

	var res ai.BudgetStructure
	for _, gName := range groupNamesInOrder {
		res.Groups = append(res.Groups, ai.ProposedGroup{
			GroupName: gName,
			Buckets:   groupsMap[gName],
		})
	}

	return &res, nil
}

// GetExistingGroupNames returns all distinct active group names from the tenantDB.
func GetExistingGroupNames(ctx context.Context, tenantDB *sql.DB) ([]string, error) {
	groupRows, err := tenantDB.QueryContext(ctx, `SELECT DISTINCT group_name FROM buckets_projection WHERE is_archived = 0 ORDER BY group_name ASC`)
	if err != nil {
		return nil, fmt.Errorf("failed querying groups: %w", err)
	}
	defer groupRows.Close()

	var existingGroups []string
	for groupRows.Next() {
		var g string
		if err := groupRows.Scan(&g); err == nil && strings.TrimSpace(g) != "" {
			existingGroups = append(existingGroups, strings.TrimSpace(g))
		}
	}
	return existingGroups, nil
}

// DetectAndEnrichStructureConflicts checks proposed buckets against existing active buckets in tenantDB,
// marking IsExisting, ExistingGroup, and populating ExistingGroups on structure.
func DetectAndEnrichStructureConflicts(ctx context.Context, tenantDB *sql.DB, structure *ai.BudgetStructure) error {
	if structure == nil {
		return nil
	}

	// 1. Fetch all distinct active group names
	existingGroups, err := GetExistingGroupNames(ctx, tenantDB)
	if err != nil {
		return err
	}
	structure.ExistingGroups = existingGroups

	// 2. Fetch all active buckets
	bRows, err := tenantDB.QueryContext(ctx, `SELECT lower(trim(name)), group_name FROM buckets_projection WHERE is_archived = 0`)
	if err != nil {
		return fmt.Errorf("failed querying buckets: %w", err)
	}
	defer bRows.Close()

	existingBuckets := make(map[string]string) // normalized name -> group_name
	for bRows.Next() {
		var name, grp string
		if err := bRows.Scan(&name, &grp); err == nil {
			existingBuckets[name] = grp
		}
	}

	// 3. Mark existing buckets in structure
	for gIdx := range structure.Groups {
		for bIdx := range structure.Groups[gIdx].Buckets {
			b := &structure.Groups[gIdx].Buckets[bIdx]
			normName := strings.ToLower(strings.TrimSpace(b.Name))
			if origGroup, exists := existingBuckets[normName]; exists {
				b.IsExisting = true
				b.ExistingGroup = origGroup
			}
		}
	}

	return nil
}
