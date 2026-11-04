package budget

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/balance/balance/internal/logger"
)

// TargetDueAdvanceResult summarises targets updated by AdvanceOverdueTargetDueDates.
type TargetDueAdvanceResult struct {
	TargetsChecked  int
	TargetsAdvanced int
}

// AdvanceOverdueTargetDueDatesForTenant advances any active targets in tenantDB whose due_date
// is strictly in the past (yesterday or earlier relative to midnight in the budget's timezone).
func AdvanceOverdueTargetDueDatesForTenant(ctx context.Context, tenantDB *sql.DB, timezone string, now ...time.Time) (TargetDueAdvanceResult, error) {
	var res TargetDueAdvanceResult
	if tenantDB == nil {
		return res, nil
	}

	loc := time.UTC
	if timezone != "" {
		if l, err := time.LoadLocation(timezone); err == nil {
			loc = l
		}
	}

	currentTime := time.Now().In(loc)
	if len(now) > 0 && !now[0].IsZero() {
		currentTime = now[0].In(loc)
	}

	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, recurrence_cadence, recurrence_interval,
		       recurrence_day_of_week, recurrence_day_of_month, recurrence_month_of_year,
		       due_date
		FROM targets_projection
		WHERE is_archived = 0
		  AND recurrence_cadence != 'ONE_OFF'
		  AND due_date IS NOT NULL AND due_date > 0
	`)
	if err != nil {
		return res, fmt.Errorf("failed querying recurring targets for due date advance: %w", err)
	}
	defer rows.Close()

	type pendingUpdate struct {
		id         string
		nextDueMic int64
	}
	var updates []pendingUpdate

	for rows.Next() {
		var id, cadence string
		var rInterval, dow, dom, moy sql.NullInt64
		var dueDateMicros int64
		if err := rows.Scan(&id, &cadence, &rInterval, &dow, &dom, &moy, &dueDateMicros); err != nil {
			continue
		}
		res.TargetsChecked++

		if !ShouldAdvanceDueDate(dueDateMicros, currentTime, loc) {
			continue
		}

		sched := RecurrenceSchedule{
			Cadence:  cadence,
			Interval: int(rInterval.Int64),
		}
		if dow.Valid {
			v := int(dow.Int64)
			sched.DayOfWeek = &v
		}
		if dom.Valid {
			v := int(dom.Int64)
			sched.DayOfMonth = &v
		}
		if moy.Valid {
			v := int(moy.Int64)
			sched.MonthOfYear = &v
		}
		if dueDateMicros > 0 {
			tVal := time.UnixMicro(dueDateMicros).In(loc)
			sched.AnchorDate = &tVal
		}

		nextDate, ok := sched.NextDueDate(currentTime, loc)
		if ok && !nextDate.IsZero() {
			updates = append(updates, pendingUpdate{
				id:         id,
				nextDueMic: nextDate.UnixMicro(),
			})
		}
	}

	if len(updates) == 0 {
		return res, nil
	}

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	nowMicros := time.Now().UTC().UnixMicro()
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE targets_projection
		SET due_date = ?, updated_at = ?
		WHERE id = ?
	`)
	if err != nil {
		return res, err
	}
	defer stmt.Close()

	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.nextDueMic, nowMicros, u.id); err != nil {
			return res, fmt.Errorf("failed advancing due_date for target %s: %w", u.id, err)
		}
		res.TargetsAdvanced++
	}

	if err := tx.Commit(); err != nil {
		return res, err
	}

	logger.Info(ctx, "Advanced overdue targets to next cycle",
		"checked", res.TargetsChecked,
		"advanced", res.TargetsAdvanced,
		"timezone", loc.String(),
	)

	return res, nil
}
