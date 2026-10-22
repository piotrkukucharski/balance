package report

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// SankeyNode represents an entity/category in the cashflow flow.
type SankeyNode struct {
	Name string `json:"name"`
}

// SankeyLink represents a directed monetary link from Source to Target.
type SankeyLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Value  int64  `json:"value"` // Amount in minor units
}

// SankeyFlowResult represents nodes and links ready for an ECharts Sankey chart.
type SankeyFlowResult struct {
	Nodes []SankeyNode `json:"nodes"`
	Links []SankeyLink `json:"links"`
}

// CalculateSankeyFlow generates income-to-bucket flow structure.
// Flow: [Income Sources / Payees] -> [Total Income / Pool] -> [Envelopes / Buckets]
// or [Income Parties] -> [Buckets].
func CalculateSankeyFlow(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, filter ReportFilter) (*SankeyFlowResult, error) {
	rates, err := LoadExchangeRates(ctx, mainDB, filter.TargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("load exchange rates: %w", err)
	}

	targetCurr := filter.TargetCurrency
	if targetCurr == "" {
		targetCurr = "PLN"
	}

	// 1. Inflows from external parties to user accounts
	inflowQuery := `
		SELECT bt.transaction_date, bt.destination_amount_minor, bt.destination_currency,
		       sp.name as party_name
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE sp.is_user_owned = 0 AND dp.is_user_owned = 1`

	var args []any
	if filter.StartDateMicros > 0 {
		inflowQuery += ` AND bt.transaction_date >= ?`
		args = append(args, filter.StartDateMicros)
	}
	if filter.EndDateMicros > 0 {
		inflowQuery += ` AND bt.transaction_date <= ?`
		args = append(args, filter.EndDateMicros)
	}

	inflowRows, err := tenantDB.QueryContext(ctx, inflowQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("query inflow sankey: %w", err)
	}
	defer inflowRows.Close()

	partyIncomeMap := make(map[string]int64)
	var totalIncome int64

	for inflowRows.Next() {
		var txDate, amount int64
		var curr, partyName string
		if err := inflowRows.Scan(&txDate, &amount, &curr, &partyName); err != nil {
			return nil, err
		}
		converted, err := rates.ConvertTo(amount, curr, targetCurr, txDate)
		if err != nil {
			return nil, err
		}
		partyIncomeMap[partyName] += converted
		totalIncome += converted
	}

	// 2. Outflows from user accounts to buckets
	outflowQuery := `
		SELECT bt.transaction_date, bt.source_amount_minor, bt.source_currency,
		       COALESCE(bp.name, 'Unassigned') as bucket_name
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		LEFT JOIN buckets_projection bp ON bt.bucket_id = bp.id
		WHERE sp.is_user_owned = 1 AND dp.is_user_owned = 0`

	var outflowArgs []any
	if filter.StartDateMicros > 0 {
		outflowQuery += ` AND bt.transaction_date >= ?`
		outflowArgs = append(outflowArgs, filter.StartDateMicros)
	}
	if filter.EndDateMicros > 0 {
		outflowQuery += ` AND bt.transaction_date <= ?`
		outflowArgs = append(outflowArgs, filter.EndDateMicros)
	}

	outflowRows, err := tenantDB.QueryContext(ctx, outflowQuery, outflowArgs...)
	if err != nil {
		return nil, fmt.Errorf("query outflow sankey: %w", err)
	}
	defer outflowRows.Close()

	bucketExpenseMap := make(map[string]int64)
	var totalExpense int64

	for outflowRows.Next() {
		var txDate, amount int64
		var curr, bucketName string
		if err := outflowRows.Scan(&txDate, &amount, &curr, &bucketName); err != nil {
			return nil, err
		}
		converted, err := rates.ConvertTo(amount, curr, targetCurr, txDate)
		if err != nil {
			return nil, err
		}
		bucketExpenseMap[bucketName] += converted
		totalExpense += converted
	}

	nodeSet := make(map[string]bool)
	var links []SankeyLink

	poolName := "Available Budget"
	if totalIncome > 0 || totalExpense > 0 {
		nodeSet[poolName] = true
	}

	// Income parties -> Available Budget
	for party, amt := range partyIncomeMap {
		if amt > 0 {
			nodeSet[party] = true
			links = append(links, SankeyLink{
				Source: party,
				Target: poolName,
				Value:  amt,
			})
		}
	}

	// Available Budget -> Buckets
	for bucket, amt := range bucketExpenseMap {
		if amt > 0 {
			nodeSet[bucket] = true
			links = append(links, SankeyLink{
				Source: poolName,
				Target: bucket,
				Value:  amt,
			})
		}
	}

	// If total income exceeded expenses, route remainder to "Savings & Net Growth"
	if totalIncome > totalExpense {
		savingsNode := "Savings & Growth"
		nodeSet[savingsNode] = true
		links = append(links, SankeyLink{
			Source: poolName,
			Target: savingsNode,
			Value:  totalIncome - totalExpense,
		})
	}

	var nodes []SankeyNode
	for n := range nodeSet {
		nodes = append(nodes, SankeyNode{Name: n})
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	return &SankeyFlowResult{
		Nodes: nodes,
		Links: links,
	}, nil
}
