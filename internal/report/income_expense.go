package report

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ReportFilter specifies optional date boundaries and target currency for aggregations.
type ReportFilter struct {
	StartDateMicros int64  // Optional inclusive UTC microsecond start
	EndDateMicros   int64  // Optional inclusive UTC microsecond end
	TargetCurrency  string // Base currency of the budget (defaults to 'PLN' if empty)
	Range           string // "30d", "90d", "180d", "YTD", "1y", "all"
	ExcludeInternal bool   // Default true: exclude transactions between user-owned accounts
}

// IncomeVsExpenseMonthly holds monthly aggregated income and expense numbers.
type IncomeVsExpenseMonthly struct {
	MonthKey          string `json:"month_key"`          // "YYYY-MM"
	IncomeMinor       int64  `json:"income_minor"`       // Sum of inflows to user accounts in minor units
	ExpenseMinor      int64  `json:"expense_minor"`      // Sum of outflows from user accounts in minor units
	NetSavingsMinor   int64  `json:"net_savings_minor"`  // Income - Expense
	SavingsRatePct    float64 `json:"savings_rate_pct"`  // (NetSavings / Income) * 100 or 0
}

// IncomeVsExpenseResult holds the array of monthly aggregates and total summary.
type IncomeVsExpenseResult struct {
	Months            []IncomeVsExpenseMonthly `json:"months"`
	TotalIncomeMinor  int64                    `json:"total_income_minor"`
	TotalExpenseMinor int64                    `json:"total_expense_minor"`
	TotalNetMinor     int64                    `json:"total_net_minor"`
}

// CalculateIncomeVsExpense aggregates monthly income vs expense for the tenant database.
// - Inflow: sp.is_user_owned = 0 AND dp.is_user_owned = 1 (Destination account receives money)
// - Outflow: sp.is_user_owned = 1 AND dp.is_user_owned = 0 (Source account spends money)
// Transfers between user accounts (sp=1 AND dp=1) are neutral and excluded.
func CalculateIncomeVsExpense(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, filter ReportFilter) (*IncomeVsExpenseResult, error) {
	rates, err := LoadExchangeRates(ctx, mainDB, filter.TargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("load exchange rates: %w", err)
	}

	query := `
		SELECT bt.transaction_date, 
		       bt.source_amount_minor, bt.source_currency, 
		       bt.destination_amount_minor, bt.destination_currency,
		       sp.is_user_owned, dp.is_user_owned
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE (sp.is_user_owned != dp.is_user_owned)`

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
		return nil, fmt.Errorf("query income vs expense: %w", err)
	}
	defer rows.Close()

	monthMap := make(map[string]*IncomeVsExpenseMonthly)
	var monthOrder []string

	targetCurr := filter.TargetCurrency
	if targetCurr == "" {
		targetCurr = "PLN"
	}

	res := &IncomeVsExpenseResult{
		Months: []IncomeVsExpenseMonthly{},
	}

	for rows.Next() {
		var txDate int64
		var srcAmount, dstAmount int64
		var srcCurr, dstCurr string
		var srcOwned, dstOwned int

		if err := rows.Scan(&txDate, &srcAmount, &srcCurr, &dstAmount, &dstCurr, &srcOwned, &dstOwned); err != nil {
			return nil, fmt.Errorf("scan income vs expense row: %w", err)
		}

		monthKey := time.UnixMicro(txDate).UTC().Format("2006-01")
		monthly, exists := monthMap[monthKey]
		if !exists {
			monthly = &IncomeVsExpenseMonthly{MonthKey: monthKey}
			monthMap[monthKey] = monthly
			monthOrder = append(monthOrder, monthKey)
		}

		if srcOwned == 0 && dstOwned == 1 {
			// Inflow
			converted, err := rates.ConvertTo(dstAmount, dstCurr, targetCurr, txDate)
			if err != nil {
				return nil, err
			}
			monthly.IncomeMinor += converted
			res.TotalIncomeMinor += converted
		} else if srcOwned == 1 && dstOwned == 0 {
			// Outflow
			converted, err := rates.ConvertTo(srcAmount, srcCurr, targetCurr, txDate)
			if err != nil {
				return nil, err
			}
			monthly.ExpenseMinor += converted
			res.TotalExpenseMinor += converted
		}
	}

	for _, mKey := range monthOrder {
		m := monthMap[mKey]
		m.NetSavingsMinor = m.IncomeMinor - m.ExpenseMinor
		if m.IncomeMinor > 0 {
			m.SavingsRatePct = (float64(m.NetSavingsMinor) / float64(m.IncomeMinor)) * 100.0
		}
		res.Months = append(res.Months, *m)
	}

	res.TotalNetMinor = res.TotalIncomeMinor - res.TotalExpenseMinor
	return res, nil
}
