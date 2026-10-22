package report

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// NetWorthPoint represents financial standing at a point in time.
type NetWorthPoint struct {
	DateKey       string `json:"date_key"`       // "YYYY-MM" or "YYYY-MM-DD"
	AssetsMinor   int64  `json:"assets_minor"`   // Positive balances in checking, savings, cash, investment
	LiabilitiesMinor int64 `json:"liabilities_minor"` // Negative balances, debt, credit cards
	NetWorthMinor int64  `json:"net_worth_minor"` // Assets - Liabilities
}

// NetWorthResult holds chronological net worth progression.
type NetWorthResult struct {
	Points               []NetWorthPoint `json:"points"`
	CurrentNetWorthMinor int64           `json:"current_net_worth_minor"`
}

// CalculateNetWorth computes historical assets, liabilities, and net worth trajectory.
// It reconstructs account balances backward or forward from transactions for user-owned accounts.
func CalculateNetWorth(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, filter ReportFilter) (*NetWorthResult, error) {
	rates, err := LoadExchangeRates(ctx, mainDB, filter.TargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("load exchange rates: %w", err)
	}

	targetCurr := filter.TargetCurrency
	if targetCurr == "" {
		targetCurr = "PLN"
	}

	// 1. Get all user-owned accounts and their types
	acctRows, err := tenantDB.QueryContext(ctx, `
		SELECT a.id, a.account_type, a.primary_currency
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.is_user_owned = 1`)
	if err != nil {
		return nil, fmt.Errorf("query user accounts: %w", err)
	}
	defer acctRows.Close()

	accountTypes := make(map[string]string) // account_id -> account_type
	for acctRows.Next() {
		var id, aType, pCurr string
		if err := acctRows.Scan(&id, &aType, &pCurr); err != nil {
			return nil, err
		}
		accountTypes[id] = aType
	}

	// 2. Fetch all transactions involving user accounts ordered chronologically
	query := `
		SELECT bt.transaction_date, 
		       bt.source_account_id, bt.source_amount_minor, bt.source_currency,
		       bt.destination_account_id, bt.destination_amount_minor, bt.destination_currency,
		       sp.is_user_owned, dp.is_user_owned
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE sp.is_user_owned = 1 OR dp.is_user_owned = 1
		ORDER BY bt.transaction_date ASC`

	rows, err := tenantDB.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query txs for net worth: %w", err)
	}
	defer rows.Close()

	// Track running balance per account in target currency
	accountBalances := make(map[string]int64)
	monthMap := make(map[string]*NetWorthPoint)
	var monthOrder []string

	for rows.Next() {
		var txDate int64
		var srcAcct, dstAcct, srcCurr, dstCurr string
		var srcAmount, dstAmount int64
		var srcOwned, dstOwned int

		if err := rows.Scan(&txDate, &srcAcct, &srcAmount, &srcCurr, &dstAcct, &dstAmount, &dstCurr, &srcOwned, &dstOwned); err != nil {
			return nil, err
		}

		if srcOwned == 1 {
			converted, err := rates.ConvertTo(srcAmount, srcCurr, targetCurr, txDate)
			if err != nil {
				return nil, err
			}
			accountBalances[srcAcct] -= converted
		}

		if dstOwned == 1 {
			converted, err := rates.ConvertTo(dstAmount, dstCurr, targetCurr, txDate)
			if err != nil {
				return nil, err
			}
			accountBalances[dstAcct] += converted
		}

		monthKey := time.UnixMicro(txDate).UTC().Format("2006-01")

		// Recompute net worth at this transaction date
		var assets, liabilities int64
		for aID, bal := range accountBalances {
			aType := accountTypes[aID]
			if aType == "CREDIT_CARD" {
				if bal < 0 {
					liabilities += -bal
				} else {
					assets += bal
				}
			} else {
				if bal >= 0 {
					assets += bal
				} else {
					liabilities += -bal
				}
			}
		}

		point, exists := monthMap[monthKey]
		if !exists {
			point = &NetWorthPoint{DateKey: monthKey}
			monthMap[monthKey] = point
			monthOrder = append(monthOrder, monthKey)
		}
		// Update with latest values for this month
		point.AssetsMinor = assets
		point.LiabilitiesMinor = liabilities
		point.NetWorthMinor = assets - liabilities
	}

	res := &NetWorthResult{
		Points: make([]NetWorthPoint, 0, len(monthOrder)),
	}

	for _, mKey := range monthOrder {
		pt := *monthMap[mKey]
		res.Points = append(res.Points, pt)
		res.CurrentNetWorthMinor = pt.NetWorthMinor
	}

	return res, nil
}
