package report

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ForecastPoint represents projected liquid balance at day t.
type ForecastPoint struct {
	DateKey         string `json:"date_key"`          // "YYYY-MM-DD"
	TimestampMicro  int64  `json:"timestamp"`
	ProjectedMinor  int64  `json:"projected_minor"`   // Projected cash balance
	ScheduledInflow int64  `json:"scheduled_inflow"`  // Recurring income arriving
	ScheduledOutflow int64 `json:"scheduled_outflow"` // Targets due on this day
}

// BalanceForecastResult holds the 365-day forward cashflow runway.
type BalanceForecastResult struct {
	CurrentBalanceMinor int64           `json:"current_balance_minor"`
	LowestPointMinor    int64           `json:"lowest_point_minor"`
	DaysUntilZero       int             `json:"days_until_zero"` // -1 if never negative
	Points              []ForecastPoint `json:"points"`
}

// CalculateBalanceForecast projects daily liquid runway forward up to daysForward (e.g. 365 days)
// based on current liquid balances and scheduled target obligations from targets_projection.
func CalculateBalanceForecast(ctx context.Context, tenantDB *sql.DB, daysForward int) (*BalanceForecastResult, error) {
	if daysForward <= 0 || daysForward > 3650 {
		daysForward = 365
	}

	// 1. Current liquid assets (CHECKING, SAVINGS, CASH)
	var initialBalance int64
	balRow := tenantDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(acb.balance_minor), 0)
		FROM account_currency_balances acb
		JOIN accounts_current a ON acb.account_id = a.id
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.is_user_owned = 1 
		  AND a.account_type IN ('CHECKING', 'SAVINGS', 'CASH')
		  AND a.is_closed = 0`)
	if err := balRow.Scan(&initialBalance); err != nil {
		return nil, fmt.Errorf("query initial liquid balance: %w", err)
	}

	// 2. Load active recurring targets from targets_projection
	targetRows, err := tenantDB.QueryContext(ctx, `
		SELECT name, target_type, amount_minor, recurrence_cadence, recurrence_interval, COALESCE(due_date, 0)
		FROM targets_projection
		WHERE is_archived = 0`)
	if err != nil {
		return nil, fmt.Errorf("query targets for forecast: %w", err)
	}
	defer targetRows.Close()

	type targetInfo struct {
		name       string
		targetType string
		amount     int64
		cadence    string
		interval   int
		dueDate    int64
	}
	var targets []targetInfo
	for targetRows.Next() {
		var t targetInfo
		var rInterval sql.NullInt64
		if err := targetRows.Scan(&t.name, &t.targetType, &t.amount, &t.cadence, &rInterval, &t.dueDate); err != nil {
			return nil, err
		}
		if rInterval.Valid {
			t.interval = int(rInterval.Int64)
		}
		targets = append(targets, t)
	}

	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	currentBal := initialBalance
	lowestPoint := initialBalance
	daysUntilZero := -1

	points := make([]ForecastPoint, 0, daysForward)

	for d := 0; d < daysForward; d++ {
		currentDate := startOfDay.AddDate(0, 0, d)
		dateKey := currentDate.Format("2006-01-02")
		dayMicros := currentDate.UnixMicro()

		var dayOutflow int64
		var dayInflow int64

		// Check which targets trigger on this day
		for _, t := range targets {
			triggers := false
			switch t.cadence {
			case "MONTHLY":
				targetDay := 1
				if t.dueDate > 0 {
					targetDay = time.UnixMicro(t.dueDate).UTC().Day()
				}
				if currentDate.Day() == targetDay {
					triggers = true
				}
			case "WEEKLY":
				targetWeekday := time.Monday
				if t.dueDate > 0 {
					targetWeekday = time.UnixMicro(t.dueDate).UTC().Weekday()
				}
				if currentDate.Weekday() == targetWeekday {
					triggers = true
				}
			case "ANNUAL":
				targetMonth := time.January
				targetDay := 1
				if t.dueDate > 0 {
					dueTime := time.UnixMicro(t.dueDate).UTC()
					targetMonth = dueTime.Month()
					targetDay = dueTime.Day()
				}
				if currentDate.Month() == targetMonth && currentDate.Day() == targetDay {
					triggers = true
				}
			case "CUSTOM_DAYS":
				if t.interval > 0 && d%t.interval == 0 {
					triggers = true
				}
			case "CUSTOM_MONTHS":
				if t.dueDate > 0 && t.interval > 0 {
					dueTime := time.UnixMicro(t.dueDate).UTC()
					if currentDate.Day() == dueTime.Day() {
						monthDiff := (currentDate.Year()-dueTime.Year())*12 + int(currentDate.Month()-dueTime.Month())
						if monthDiff >= 0 && monthDiff%t.interval == 0 {
							triggers = true
						}
					}
				}
			case "CUSTOM_YEARS":
				if t.dueDate > 0 && t.interval > 0 {
					dueTime := time.UnixMicro(t.dueDate).UTC()
					if currentDate.Month() == dueTime.Month() && currentDate.Day() == dueTime.Day() {
						yearDiff := currentDate.Year() - dueTime.Year()
						if yearDiff >= 0 && yearDiff%t.interval == 0 {
							triggers = true
						}
					}
				}
			case "ONE_OFF":
				if t.dueDate > 0 {
					dueTime := time.UnixMicro(t.dueDate).UTC()
					if dueTime.Year() == currentDate.Year() && dueTime.YearDay() == currentDate.YearDay() {
						triggers = true
					}
				}
			}

			if triggers {
				dayOutflow += t.amount
			}
		}

		currentBal += dayInflow - dayOutflow
		if currentBal < lowestPoint {
			lowestPoint = currentBal
		}
		if currentBal < 0 && daysUntilZero == -1 {
			daysUntilZero = d
		}

		points = append(points, ForecastPoint{
			DateKey:          dateKey,
			TimestampMicro:   dayMicros,
			ProjectedMinor:   currentBal,
			ScheduledInflow:  dayInflow,
			ScheduledOutflow: dayOutflow,
		})
	}

	return &BalanceForecastResult{
		CurrentBalanceMinor: initialBalance,
		LowestPointMinor:    lowestPoint,
		DaysUntilZero:       daysUntilZero,
		Points:              points,
	}, nil
}
