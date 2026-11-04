package budget

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const PrefFirstDayOfWeek = "first_day_of_week"

// GetUserFirstDayOfWeek retrieves user's preferred first day of week ("MONDAY" or "SUNDAY") for the budget.
// Defaults to "MONDAY" if not set.
func GetUserFirstDayOfWeek(ctx context.Context, tenantDB *sql.DB, userID string) (string, error) {
	if tenantDB == nil || strings.TrimSpace(userID) == "" {
		return FirstDayMonday, nil
	}

	var val string
	err := tenantDB.QueryRowContext(ctx, `SELECT value FROM budget_preferences WHERE user_id = ? AND key = ?`, userID, PrefFirstDayOfWeek).Scan(&val)
	if err == nil {
		val = strings.ToUpper(strings.TrimSpace(val))
		if val == FirstDaySunday || val == FirstDayMonday {
			return val, nil
		}
	}
	return FirstDayMonday, nil
}

// SetUserFirstDayOfWeek stores user's preferred first day of week ("MONDAY" or "SUNDAY").
func SetUserFirstDayOfWeek(ctx context.Context, tenantDB *sql.DB, userID, firstDay string) error {
	if tenantDB == nil {
		return errors.New("tenantDB is required")
	}
	if strings.TrimSpace(userID) == "" {
		return errors.New("userID is required")
	}
	val := strings.ToUpper(strings.TrimSpace(firstDay))
	if val != FirstDaySunday && val != FirstDayMonday {
		return errors.New("first_day_of_week must be MONDAY or SUNDAY")
	}

	now := time.Now().UTC().UnixMicro()
	_, err := tenantDB.ExecContext(ctx, `
		INSERT INTO budget_preferences (user_id, key, value, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, userID, PrefFirstDayOfWeek, val, now)
	return err
}
