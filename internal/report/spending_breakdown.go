package report

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// SpendingBreakdownItem represents the share and sum of spending in a bucket.
type SpendingBreakdownItem struct {
	BucketID         string  `json:"bucket_id"`
	BucketName       string  `json:"bucket_name"`
	GroupName        string  `json:"group_name"`
	TotalSpentMinor  int64   `json:"total_spent_minor"`
	PercentageShare  float64 `json:"percentage_share"` // 0.0 to 100.0
}

// SpendingBreakdownResult holds the full breakdown and total expenditure.
type SpendingBreakdownResult struct {
	Items           []SpendingBreakdownItem `json:"items"`
	TotalSpentMinor int64                   `json:"total_spent_minor"`
}

// CalculateSpendingBreakdown calculates envelope spending share and percentages.
func CalculateSpendingBreakdown(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, filter ReportFilter) (*SpendingBreakdownResult, error) {
	rates, err := LoadExchangeRates(ctx, mainDB, filter.TargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("load exchange rates: %w", err)
	}

	query := `
		SELECT bt.transaction_date, bt.source_amount_minor, bt.source_currency,
		       COALESCE(bt.bucket_id, '') as b_id,
		       COALESCE(bp.name, 'Unassigned') as b_name,
		       COALESCE(bp.group_name, 'Default') as g_name
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		LEFT JOIN buckets_projection bp ON bt.bucket_id = bp.id
		WHERE sp.is_user_owned = 1 AND dp.is_user_owned = 0`

	var args []any
	if filter.StartDateMicros > 0 {
		query += ` AND bt.transaction_date >= ?`
		args = append(args, filter.StartDateMicros)
	}
	if filter.EndDateMicros > 0 {
		query += ` AND bt.transaction_date <= ?`
		args = append(args, filter.EndDateMicros)
	}

	rows, err := tenantDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query spending breakdown: %w", err)
	}
	defer rows.Close()

	targetCurr := filter.TargetCurrency
	if targetCurr == "" {
		targetCurr = "PLN"
	}

	itemMap := make(map[string]*SpendingBreakdownItem)
	var totalSpent int64

	for rows.Next() {
		var txDate int64
		var amount int64
		var curr, bID, bName, gName string

		if err := rows.Scan(&txDate, &amount, &curr, &bID, &bName, &gName); err != nil {
			return nil, fmt.Errorf("scan breakdown row: %w", err)
		}

		converted, err := rates.ConvertTo(amount, curr, targetCurr, txDate)
		if err != nil {
			return nil, err
		}

		key := bID
		if key == "" {
			key = "unassigned"
		}

		item, exists := itemMap[key]
		if !exists {
			item = &SpendingBreakdownItem{
				BucketID:   bID,
				BucketName: bName,
				GroupName:  gName,
			}
			itemMap[key] = item
		}
		item.TotalSpentMinor += converted
		totalSpent += converted
	}

	var items []SpendingBreakdownItem
	for _, it := range itemMap {
		if totalSpent > 0 {
			it.PercentageShare = (float64(it.TotalSpentMinor) / float64(totalSpent)) * 100.0
		}
		items = append(items, *it)
	}

	// Sort items descending by spent amount
	sort.Slice(items, func(i, j int) bool {
		return items[i].TotalSpentMinor > items[j].TotalSpentMinor
	})

	return &SpendingBreakdownResult{
		Items:           items,
		TotalSpentMinor: totalSpent,
	}, nil
}
