package report

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// BudgetAnalysisPoint holds the budget vs actual spending, strategy adjustments, and running balance for a period.
type BudgetAnalysisPoint struct {
	PeriodKey               string `json:"period_key"`                  // "YYYY-MM"
	BudgetedMinor           int64  `json:"budgeted_minor"`              // Funds assigned to buckets
	SpentMinor              int64  `json:"spent_minor"`                 // Actual net outflows from buckets
	AdjustmentMinor         int64  `json:"adjustment_minor"`            // Strategy / overspending adjustments
	CumulativeBalanceMinor  int64  `json:"cumulative_balance_minor"`     // Running balance carried forward
}

// BudgetAnalysisResult holds the time-series points of the budget analysis.
type BudgetAnalysisResult struct {
	Points []BudgetAnalysisPoint `json:"points"`
}

type fundsAssignedPayload struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// CalculateBudgetAnalysis aggregates monthly budgeted allocations vs actual spending and running balances.
func CalculateBudgetAnalysis(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, filter ReportFilter) (*BudgetAnalysisResult, error) {
	rates, err := LoadExchangeRates(ctx, mainDB, filter.TargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("load exchange rates: %w", err)
	}

	targetCurr := filter.TargetCurrency
	if targetCurr == "" {
		targetCurr = "PLN"
	}

	// 1. Fetch allocated funds from bucket_events (FUNDS_ASSIGNED)
	eventQuery := `
		SELECT occurred_at, payload
		FROM bucket_events
		WHERE event_type = 'FUNDS_ASSIGNED'`
	var eventArgs []any
	if filter.StartDateMicros > 0 {
		eventQuery += ` AND occurred_at >= ?`
		eventArgs = append(eventArgs, filter.StartDateMicros)
	}
	if filter.EndDateMicros > 0 {
		eventQuery += ` AND occurred_at <= ?`
		eventArgs = append(eventArgs, filter.EndDateMicros)
	}
	eventQuery += ` ORDER BY occurred_at ASC`

	eventRows, err := tenantDB.QueryContext(ctx, eventQuery, eventArgs...)
	if err != nil {
		return nil, fmt.Errorf("query bucket events: %w", err)
	}
	defer eventRows.Close()

	monthMap := make(map[string]*BudgetAnalysisPoint)

	for eventRows.Next() {
		var occurredAt int64
		var payloadJSON string
		if err := eventRows.Scan(&occurredAt, &payloadJSON); err != nil {
			return nil, fmt.Errorf("scan bucket event: %w", err)
		}

		var p fundsAssignedPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			continue
		}

		converted, err := rates.ConvertTo(p.AmountMinor, p.Currency, targetCurr, occurredAt)
		if err != nil {
			return nil, err
		}

		monthKey := time.UnixMicro(occurredAt).UTC().Format("2006-01")
		point, exists := monthMap[monthKey]
		if !exists {
			point = &BudgetAnalysisPoint{PeriodKey: monthKey}
			monthMap[monthKey] = point
		}
		point.BudgetedMinor += converted
	}

	// 2. Fetch actual envelope spending from budget_transactions_current
	txQuery := `
		SELECT bt.transaction_date, bt.source_amount_minor, bt.source_currency
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE sp.is_user_owned = 1 AND dp.is_user_owned = 0 AND bt.bucket_id IS NOT NULL`

	var txArgs []any
	if filter.StartDateMicros > 0 {
		txQuery += ` AND bt.transaction_date >= ?`
		txArgs = append(txArgs, filter.StartDateMicros)
	}
	if filter.EndDateMicros > 0 {
		txQuery += ` AND bt.transaction_date <= ?`
		txArgs = append(txArgs, filter.EndDateMicros)
	}
	txQuery += ` ORDER BY bt.transaction_date ASC`

	txRows, err := tenantDB.QueryContext(ctx, txQuery, txArgs...)
	if err != nil {
		return nil, fmt.Errorf("query spending for budget analysis: %w", err)
	}
	defer txRows.Close()

	for txRows.Next() {
		var txDate int64
		var amount int64
		var curr string
		if err := txRows.Scan(&txDate, &amount, &curr); err != nil {
			return nil, fmt.Errorf("scan spending row: %w", err)
		}

		converted, err := rates.ConvertTo(amount, curr, targetCurr, txDate)
		if err != nil {
			return nil, err
		}

		monthKey := time.UnixMicro(txDate).UTC().Format("2006-01")
		point, exists := monthMap[monthKey]
		if !exists {
			point = &BudgetAnalysisPoint{PeriodKey: monthKey}
			monthMap[monthKey] = point
		}
		point.SpentMinor += converted
	}

	// Collect and sort period keys
	var keys []string
	for k := range monthMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var runningBalance int64
	points := make([]BudgetAnalysisPoint, 0, len(keys))

	for _, k := range keys {
		pt := *monthMap[k]
		netPeriod := pt.BudgetedMinor - pt.SpentMinor

		// If spent > budgeted, adjustment records negative spread
		if pt.SpentMinor > pt.BudgetedMinor {
			pt.AdjustmentMinor = pt.BudgetedMinor - pt.SpentMinor
		}

		runningBalance += netPeriod
		pt.CumulativeBalanceMinor = runningBalance
		points = append(points, pt)
	}

	return &BudgetAnalysisResult{
		Points: points,
	}, nil
}
