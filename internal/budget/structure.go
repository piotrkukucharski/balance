package budget

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/balance/balance/internal/ai"
)

// ApplyBudgetStructure applies a hierarchical budget structure (groups, buckets, targets) to a tenant database.
// Existing buckets with the same name are reused without duplication; existing targets with the same name in a bucket are skipped.
func ApplyBudgetStructure(ctx context.Context, tenantDB *sql.DB, structure *ai.BudgetStructure, agentID, correlationID string) (int, int, error) {
	if structure == nil || len(structure.Groups) == 0 {
		return 0, 0, nil
	}

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

			// Check if bucket with the same name already exists
			var bucketID string
			err := tenantDB.QueryRowContext(ctx, `SELECT id FROM buckets_projection WHERE name = ? AND is_archived = 0 LIMIT 1`, bucketName).Scan(&bucketID)
			if err != nil && err != sql.ErrNoRows {
				return bucketsCreated, targetsCreated, fmt.Errorf("failed checking existing bucket '%s': %w", bucketName, err)
			}

			if bucketID == "" {
				newID, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
					GroupName:     groupName,
					Name:          bucketName,
					AgentID:       agentID,
					CorrelationID: correlationID,
				})
				if err != nil {
					return bucketsCreated, targetsCreated, fmt.Errorf("failed creating bucket '%s': %w", bucketName, err)
				}
				bucketID = newID
				bucketsCreated++
			}

			// Create targets
			for idx, proposedTarget := range proposedBucket.Targets {
				targetName := strings.TrimSpace(proposedTarget.Name)
				if targetName == "" {
					continue
				}

				// Check if target with the same name already exists in this bucket
				var targetCount int
				err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection WHERE bucket_id = ? AND name = ? AND is_archived = 0`, bucketID, targetName).Scan(&targetCount)
				if err != nil {
					return bucketsCreated, targetsCreated, fmt.Errorf("failed checking existing target '%s': %w", targetName, err)
				}
				if targetCount > 0 {
					// Skip existing target
					continue
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

				_, err = CreateTarget(ctx, tenantDB, CreateTargetInput{
					BucketID:            bucketID,
					Name:                targetName,
					TargetType:          tType,
					Priority:            prio,
					SortOrder:           idx,
					AmountMinor:         proposedTarget.AmountMinor,
					Currency:            currency,
					RecurrenceCadence:   cadence,
					NonSpendingStrategy: strategy,
					AgentID:             agentID,
					CorrelationID:       correlationID,
				})
				if err != nil {
					return bucketsCreated, targetsCreated, fmt.Errorf("failed creating target '%s': %w", targetName, err)
				}
				targetsCreated++
			}
		}
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
		SELECT bucket_id, name, target_type, priority, amount_minor, currency, recurrence_cadence, non_spending_strategy
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
		if err := targetRows.Scan(&bucketID, &t.Name, &t.TargetType, &t.Priority, &t.AmountMinor, &t.Currency, &t.RecurrenceCadence, &t.NonSpendingStrategy); err != nil {
			return nil, err
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
