package report

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SpendingTrendsPoint holds the monthly spending per category/bucket along with moving average.
type SpendingTrendsPoint struct {
	MonthKey       string           `json:"month_key"`       // "YYYY-MM"
	TotalSpentMinor int64           `json:"total_spent_minor"` // Total outflows in month
	MovingAvgMinor  int64           `json:"moving_avg_minor"`  // 3-month moving average of total outflows
	BucketBreakdown map[string]int64 `json:"bucket_breakdown"` // Bucket Name -> Minor amount spent
}

// SpendingTrendsResult holds all points and active bucket names.
type SpendingTrendsResult struct {
	Points      []SpendingTrendsPoint `json:"points"`
	BucketNames []string              `json:"bucket_names"`
}

// CalculateSpendingTrends calculates monthly spending trends across envelope buckets with 3-month moving average.
func CalculateSpendingTrends(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, filter ReportFilter) (*SpendingTrendsResult, error) {
	rates, err := LoadExchangeRates(ctx, mainDB, filter.TargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("load exchange rates: %w", err)
	}

	query := `
		SELECT bt.transaction_date, bt.source_amount_minor, bt.source_currency,
		       COALESCE(bp.name, 'Unassigned') as bucket_name
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
	query += ` ORDER BY bt.transaction_date ASC`

	rows, err := tenantDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query spending trends: %w", err)
	}
	defer rows.Close()

	targetCurr := filter.TargetCurrency
	if targetCurr == "" {
		targetCurr = "PLN"
	}

	monthMap := make(map[string]*SpendingTrendsPoint)
	var monthOrder []string
	bucketSet := make(map[string]bool)

	for rows.Next() {
		var txDate int64
		var amount int64
		var curr string
		var bucketName string

		if err := rows.Scan(&txDate, &amount, &curr, &bucketName); err != nil {
			return nil, fmt.Errorf("scan spending trends row: %w", err)
		}

		monthKey := time.UnixMicro(txDate).UTC().Format("2006-01")
		point, exists := monthMap[monthKey]
		if !exists {
			point = &SpendingTrendsPoint{
				MonthKey:        monthKey,
				BucketBreakdown: make(map[string]int64),
			}
			monthMap[monthKey] = point
			monthOrder = append(monthOrder, monthKey)
		}

		converted, err := rates.ConvertTo(amount, curr, targetCurr, txDate)
		if err != nil {
			return nil, err
		}

		point.TotalSpentMinor += converted
		point.BucketBreakdown[bucketName] += converted
		bucketSet[bucketName] = true
	}

	var bucketNames []string
	for b := range bucketSet {
		bucketNames = append(bucketNames, b)
	}

	res := &SpendingTrendsResult{
		Points:      make([]SpendingTrendsPoint, 0, len(monthOrder)),
		BucketNames: bucketNames,
	}

	// Calculate 3-month moving average
	for i, mKey := range monthOrder {
		pt := *monthMap[mKey]
		var sumLast3 int64
		count := 0
		startIdx := i - 2
		if startIdx < 0 {
			startIdx = 0
		}
		for j := startIdx; j <= i; j++ {
			sumLast3 += monthMap[monthOrder[j]].TotalSpentMinor
			count++
		}
		if count > 0 {
			pt.MovingAvgMinor = sumLast3 / int64(count)
		}
		res.Points = append(res.Points, pt)
	}

	return res, nil
}
