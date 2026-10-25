package budget

import (
	"context"
	"database/sql"
	"strings"

	"github.com/balance/balance/internal/lua"
)

// BuildBudgetSnapshot constructs an in-memory financial snapshot from tenantDB for the Lua sandbox.
func BuildBudgetSnapshot(ctx context.Context, tenantDB *sql.DB, targetCurrency string, windowDays int) (*lua.InMemoryBudgetSnapshot, error) {
	if targetCurrency == "" {
		targetCurrency = "PLN"
	}
	if windowDays <= 0 {
		windowDays = 30
	}

	snap := &lua.InMemoryBudgetSnapshot{
		Targets:        make(map[string]*lua.TargetMeta),
		BucketBalances: make(map[string]int64),
		FXRates:        make(map[string]int64),
		InflowsPast:    make(map[string][]int64),
		OutflowsPast:   make(map[string][]int64),
		WindowDays:     windowDays,
		TargetCurrency: targetCurrency,
	}

	if tenantDB == nil {
		return snap, nil
	}

	// 1. Fetch active targets
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, name, priority, amount_minor, current_funded_minor, currency
		FROM targets_projection
		WHERE is_archived = 0`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name, curr string
			var priority int
			var amt, funded int64
			if err := rows.Scan(&id, &name, &priority, &amt, &funded, &curr); err == nil {
				meta := &lua.TargetMeta{
					ID:       id,
					Name:     name,
					Planned:  amt,
					Required: amt,
					Funded:   funded,
					Spent:    0,
					Priority: priority,
					Currency: curr,
				}
				snap.Targets[id] = meta
				snap.Targets[name] = meta
				snap.Targets[strings.ToLower(name)] = meta
			}
		}
	}

	// 2. Fetch bucket balances
	bRows, err := tenantDB.QueryContext(ctx, `
		SELECT currency, COALESCE(SUM(balance_minor), 0)
		FROM bucket_currency_balances
		GROUP BY currency`)
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var curr string
			var bal int64
			if err := bRows.Scan(&curr, &bal); err == nil {
				snap.BucketBalances[curr] = bal
			}
		}
	}

	return snap, nil
}
