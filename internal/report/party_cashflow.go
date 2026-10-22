package report

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// PartyCashflowItem holds cashflow and volume aggregated per merchant / employer / individual.
type PartyCashflowItem struct {
	PartyID          string `json:"party_id"`
	PartyName        string `json:"party_name"`
	PartyType        string `json:"party_type"` // 'INDIVIDUAL' or 'ORGANIZATION'
	InflowMinor      int64  `json:"inflow_minor"`
	OutflowMinor     int64  `json:"outflow_minor"`
	NetCashflowMinor int64  `json:"net_cashflow_minor"` // Inflow - Outflow
	TransactionCount int    `json:"transaction_count"`
}

// PartyCashflowResult contains all counterparties ordered by total transaction volume or net spend.
type PartyCashflowResult struct {
	Parties          []PartyCashflowItem `json:"parties"`
	TotalInflowMinor int64               `json:"total_inflow_minor"`
	TotalOutflowMinor int64              `json:"total_outflow_minor"`
}

// CalculatePartyCashflow aggregates total volume, inflows, outflows, and net cashflow per counterparty.
func CalculatePartyCashflow(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, filter ReportFilter) (*PartyCashflowResult, error) {
	rates, err := LoadExchangeRates(ctx, mainDB, filter.TargetCurrency)
	if err != nil {
		return nil, fmt.Errorf("load exchange rates: %w", err)
	}

	targetCurr := filter.TargetCurrency
	if targetCurr == "" {
		targetCurr = "PLN"
	}

	query := `
		SELECT bt.transaction_date, 
		       bt.source_amount_minor, bt.source_currency, 
		       bt.destination_amount_minor, bt.destination_currency,
		       sp.id, sp.name, sp.party_type, sp.is_user_owned,
		       dp.id, dp.name, dp.party_type, dp.is_user_owned
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE sp.is_user_owned != dp.is_user_owned`

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
		return nil, fmt.Errorf("query party cashflow: %w", err)
	}
	defer rows.Close()

	partyMap := make(map[string]*PartyCashflowItem)
	var totalInflow, totalOutflow int64

	for rows.Next() {
		var txDate, srcAmount, dstAmount int64
		var srcCurr, dstCurr string
		var spID, spName, spType string
		var dpID, dpName, dpType string
		var spOwned, dpOwned int

		if err := rows.Scan(&txDate, &srcAmount, &srcCurr, &dstAmount, &dstCurr,
			&spID, &spName, &spType, &spOwned,
			&dpID, &dpName, &dpType, &dpOwned); err != nil {
			return nil, err
		}

		if spOwned == 0 && dpOwned == 1 {
			// Inflow from counterparty (sp) to user
			converted, err := rates.ConvertTo(dstAmount, dstCurr, targetCurr, txDate)
			if err != nil {
				return nil, err
			}
			item, exists := partyMap[spID]
			if !exists {
				item = &PartyCashflowItem{
					PartyID:   spID,
					PartyName: spName,
					PartyType: spType,
				}
				partyMap[spID] = item
			}
			item.InflowMinor += converted
			item.TransactionCount++
			totalInflow += converted
		} else if spOwned == 1 && dpOwned == 0 {
			// Outflow to counterparty (dp) from user
			converted, err := rates.ConvertTo(srcAmount, srcCurr, targetCurr, txDate)
			if err != nil {
				return nil, err
			}
			item, exists := partyMap[dpID]
			if !exists {
				item = &PartyCashflowItem{
					PartyID:   dpID,
					PartyName: dpName,
					PartyType: dpType,
				}
				partyMap[dpID] = item
			}
			item.OutflowMinor += converted
			item.TransactionCount++
			totalOutflow += converted
		}
	}

	var parties []PartyCashflowItem
	for _, p := range partyMap {
		p.NetCashflowMinor = p.InflowMinor - p.OutflowMinor
		parties = append(parties, *p)
	}

	// Sort descending by total transaction volume (Outflow + Inflow)
	sort.Slice(parties, func(i, j int) bool {
		volI := parties[i].OutflowMinor + parties[i].InflowMinor
		volJ := parties[j].OutflowMinor + parties[j].InflowMinor
		return volI > volJ
	})

	return &PartyCashflowResult{
		Parties:           parties,
		TotalInflowMinor:  totalInflow,
		TotalOutflowMinor: totalOutflow,
	}, nil
}
