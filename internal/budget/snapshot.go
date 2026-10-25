package budget

import (
	"context"
	"database/sql"
	"strings"

	"github.com/balance/balance/internal/lua"
)

// BuildBudgetSnapshot constructs an in-memory financial snapshot from tenantDB for the Lua sandbox.
func BuildBudgetSnapshot(ctx context.Context, tenantDB *sql.DB, targetCurrency string, windowDays int, timezone ...string) (*lua.InMemoryBudgetSnapshot, error) {
	if targetCurrency == "" {
		targetCurrency = "PLN"
	}
	if windowDays <= 0 {
		windowDays = 30
	}
	tz := "UTC"
	if len(timezone) > 0 && timezone[0] != "" {
		tz = timezone[0]
	}

	snap := &lua.InMemoryBudgetSnapshot{
		Targets:        make(map[string]*lua.TargetMeta),
		BucketBalances: make(map[string]int64),
		FXRates:        make(map[string]int64),
		InflowsPast:    make(map[string][]int64),
		OutflowsPast:   make(map[string][]int64),
		WindowDays:     windowDays,
		TargetCurrency: targetCurrency,
		Timezone:       tz,
	}

	if tenantDB == nil {
		return snap, nil
	}

	// 1. Fetch active targets
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, name, priority, amount_minor, current_funded_minor, currency, due_date
		FROM targets_projection
		WHERE is_archived = 0`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name, curr string
			var priority int
			var amt, funded int64
			var dueDate sql.NullInt64
			if err := rows.Scan(&id, &name, &priority, &amt, &funded, &curr, &dueDate); err == nil {
				var anchorDueDateSec *int64
				if dueDate.Valid && dueDate.Int64 > 0 {
					sec := dueDate.Int64 / 1_000_000
					anchorDueDateSec = &sec
				}
				meta := &lua.TargetMeta{
					ID:            id,
					Name:          name,
					Planned:       amt,
					Required:      amt,
					Funded:        funded,
					Spent:         0,
					Priority:      priority,
					Currency:      curr,
					AnchorDueDate: anchorDueDateSec,
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
