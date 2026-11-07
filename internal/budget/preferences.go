package budget

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const PrefFirstDayOfWeek = "first_day_of_week"

// GetUserFirstDayOfWeek retrieves user's preferred first day of week for the budget.
// If optional defaultDay is provided and valid, it is used as fallback when preference is not found.
// Otherwise, defaults to "MONDAY".
func GetUserFirstDayOfWeek(ctx context.Context, tenantDB *sql.DB, userID string, defaultDay ...string) (string, error) {
	fallback := FirstDayMonday
	if len(defaultDay) > 0 && IsValidFirstDayOfWeek(defaultDay[0]) {
		fallback = strings.ToUpper(strings.TrimSpace(defaultDay[0]))
	}

	if tenantDB == nil || strings.TrimSpace(userID) == "" {
		return fallback, nil
	}

	var val string
	err := tenantDB.QueryRowContext(ctx, `SELECT value FROM budget_preferences WHERE user_id = ? AND key = ?`, userID, PrefFirstDayOfWeek).Scan(&val)
	if err == nil {
		val = strings.ToUpper(strings.TrimSpace(val))
		if IsValidFirstDayOfWeek(val) {
			return val, nil
		}
	}
	return fallback, nil
}

// SetUserFirstDayOfWeek stores user's preferred first day of week (one of 7 weekdays).
func SetUserFirstDayOfWeek(ctx context.Context, tenantDB *sql.DB, userID, firstDay string) error {
	if tenantDB == nil {
		return errors.New("tenantDB is required")
	}
	if strings.TrimSpace(userID) == "" {
		return errors.New("userID is required")
	}
	val := strings.ToUpper(strings.TrimSpace(firstDay))
	if !IsValidFirstDayOfWeek(val) {
		return errors.New("first_day_of_week must be a valid weekday (MONDAY..SUNDAY)")
	}

	now := time.Now().UTC().UnixMicro()
	_, err := tenantDB.ExecContext(ctx, `
		INSERT INTO budget_preferences (user_id, key, value, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, userID, PrefFirstDayOfWeek, val, now)
	return err
}
