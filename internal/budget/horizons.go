package budget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	rollingHorizonRegex  = regexp.MustCompile(`^([1-9][0-9]{0,3})d$`)
	calendarHorizonRegex = regexp.MustCompile(`^(?:day-([1-9]|1[0-9]|2[0-8])|day:([1-9]|1[0-9]|2[0-8]))$`)
)

// HorizonType represents supported types of time horizons.
type HorizonType string

const (
	HorizonTypeMonthly             HorizonType = "MONTHLY"
	HorizonTypeAnnual              HorizonType = "ANNUAL"
	HorizonTypeRollingDays         HorizonType = "ROLLING_DAYS"
	HorizonTypeCalendarDayOfMonth  HorizonType = "CALENDAR_DAY_OF_MONTH"
)

// BudgetHorizon represents a horizon definition from the tenant DB.
type BudgetHorizon struct {
	ID          string      `json:"id"`
	HorizonType HorizonType `json:"horizon_type"`
	DaysValue   *int        `json:"days_value,omitempty"`
	IsSystem    bool        `json:"is_system"`
	IsHidden    bool        `json:"is_hidden"`
	SortOrder   int         `json:"sort_order"`
	CreatedAt   int64       `json:"created_at"`
	UpdatedAt   int64       `json:"updated_at"`
}

// UserHorizonSummary captures information about users affected by an action.
type UserHorizonSummary struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

// DaysRemainingUntilDayOfMonth calculates days remaining in current or next month until day D (1..28).
// If today's day == D, returns 1 day.
// If today's day < D, returns days remaining until day D in current month.
// If today's day > D, returns days remaining until day D in next month.
func DaysRemainingUntilDayOfMonth(targetDay int, now time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	if targetDay < 1 {
		targetDay = 1
	} else if targetDay > 28 {
		targetDay = 28
	}

	nowLoc := now.In(loc)
	today := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), 0, 0, 0, 0, loc)

	if nowLoc.Day() == targetDay {
		return 1
	}

	var targetDate time.Time
	if nowLoc.Day() < targetDay {
		targetDate = time.Date(nowLoc.Year(), nowLoc.Month(), targetDay, 0, 0, 0, 0, loc)
	} else {
		// Next month
		targetDate = time.Date(nowLoc.Year(), nowLoc.Month()+1, targetDay, 0, 0, 0, 0, loc)
	}

	days := int(math.Round(targetDate.Sub(today).Hours() / 24))
	if days < 1 {
		return 1
	}
	return days
}

// ParseHorizonIdentifier parses and validates any horizon ID or shorthand into canonical ID and window days.
func ParseHorizonIdentifier(horizon string, now time.Time, loc *time.Location) (cleanID string, windowDays int, ok bool) {
	if loc == nil {
		loc = time.UTC
	}
	raw := strings.ToLower(strings.TrimSpace(horizon))
	if raw == "" || raw == "monthly" {
		return "monthly", DaysRemainingInCurrentMonth(now, loc), true
	}
	if raw == "annual" {
		return "annual", DaysRemainingInCurrentYear(now, loc), true
	}

	if m := rollingHorizonRegex.FindStringSubmatch(raw); len(m) > 1 {
		val, err := strconv.Atoi(m[1])
		if err == nil && val >= 1 && val <= 3650 {
			return fmt.Sprintf("%dd", val), val, true
		}
	}

	if m := calendarHorizonRegex.FindStringSubmatch(raw); len(m) > 2 {
		dayStr := m[1]
		if dayStr == "" {
			dayStr = m[2]
		}
		val, err := strconv.Atoi(dayStr)
		if err == nil && val >= 1 && val <= 28 {
			clean := fmt.Sprintf("day-%d", val)
			return clean, DaysRemainingUntilDayOfMonth(val, now, loc), true
		}
	}

	return "monthly", DaysRemainingInCurrentMonth(now, loc), false
}

// GetUserDefaultHorizon retrieves user's default horizon for the budget tenant or falls back to 'monthly' / first visible.
func GetUserDefaultHorizon(ctx context.Context, tenantDB *sql.DB, userID string) (string, error) {
	if tenantDB == nil {
		return "monthly", nil
	}

	var horizonID string
	err := tenantDB.QueryRowContext(ctx, `SELECT value FROM budget_preferences WHERE user_id = ? AND key = 'default_horizon_id'`, userID).Scan(&horizonID)
	if err == nil && horizonID != "" {
		// Verify it exists and is not hidden
		var count int
		_ = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM budget_horizons WHERE id = ? AND is_hidden = 0`, horizonID).Scan(&count)
		if count > 0 {
			return horizonID, nil
		}
	}

	// Fallback 1: 'monthly' if not hidden
	var monthlyHidden int
	err = tenantDB.QueryRowContext(ctx, `SELECT is_hidden FROM budget_horizons WHERE id = 'monthly'`).Scan(&monthlyHidden)
	if err == nil && monthlyHidden == 0 {
		return "monthly", nil
	}

	// Fallback 2: First visible horizon by sort_order
	var firstVisible string
	err = tenantDB.QueryRowContext(ctx, `SELECT id FROM budget_horizons WHERE is_hidden = 0 ORDER BY sort_order ASC, created_at ASC LIMIT 1`).Scan(&firstVisible)
	if err == nil && firstVisible != "" {
		return firstVisible, nil
	}

	return "monthly", nil
}

// SetUserDefaultHorizon updates user's default horizon preference in tenantDB.
func SetUserDefaultHorizon(ctx context.Context, tenantDB *sql.DB, userID, horizonID string) error {
	if strings.TrimSpace(userID) == "" {
		return errors.New("userID is required")
	}
	cleanID := strings.TrimSpace(horizonID)
	if cleanID == "" {
		cleanID = "monthly"
	}

	// Verify horizon exists and is not hidden
	var isHidden int
	err := tenantDB.QueryRowContext(ctx, `SELECT is_hidden FROM budget_horizons WHERE id = ?`, cleanID).Scan(&isHidden)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("horizon '%s' does not exist", cleanID)
		}
		return err
	}
	if isHidden == 1 {
		return fmt.Errorf("cannot set hidden horizon '%s' as default", cleanID)
	}

	now := time.Now().UTC().UnixMicro()
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO budget_preferences (user_id, key, value, updated_at)
		VALUES (?, 'default_horizon_id', ?, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, userID, cleanID, now)
	return err
}

// ListBudgetHorizons returns all horizons for tenantDB, sorted. If visibleOnly is true, excludes hidden.
func ListBudgetHorizons(ctx context.Context, tenantDB *sql.DB, visibleOnly bool) ([]BudgetHorizon, error) {
	query := `SELECT id, horizon_type, days_value, is_system, is_hidden, sort_order, created_at, updated_at
	          FROM budget_horizons `
	if visibleOnly {
		query += `WHERE is_hidden = 0 `
	}
	query += `ORDER BY sort_order ASC, created_at ASC`

	rows, err := tenantDB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var horizons []BudgetHorizon
	for rows.Next() {
		var h BudgetHorizon
		var daysVal sql.NullInt64
		var isSys, isHid int
		if err := rows.Scan(&h.ID, &h.HorizonType, &daysVal, &isSys, &isHid, &h.SortOrder, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		h.IsSystem = isSys == 1
		h.IsHidden = isHid == 1
		if daysVal.Valid {
			v := int(daysVal.Int64)
			h.DaysValue = &v
		}
		horizons = append(horizons, h)
	}
	return horizons, nil
}

// CreateCustomHorizonInput contains parameters for adding a new horizon.
type CreateCustomHorizonInput struct {
	HorizonType HorizonType
	DaysValue   int
}

// CreateCustomHorizon creates a new rolling or calendar horizon. Only OWNER should invoke this.
func CreateCustomHorizon(ctx context.Context, tenantDB *sql.DB, input CreateCustomHorizonInput) (*BudgetHorizon, error) {
	var id string
	switch input.HorizonType {
	case HorizonTypeRollingDays:
		if input.DaysValue < 1 || input.DaysValue > 3650 {
			return nil, errors.New("rolling days must be between 1 and 3650")
		}
		id = fmt.Sprintf("%dd", input.DaysValue)
	case HorizonTypeCalendarDayOfMonth:
		if input.DaysValue < 1 || input.DaysValue > 28 {
			return nil, errors.New("calendar day of month must be between 1 and 28")
		}
		id = fmt.Sprintf("day-%d", input.DaysValue)
	default:
		return nil, errors.New("only ROLLING_DAYS and CALENDAR_DAY_OF_MONTH custom horizons can be created")
	}

	// Check if already exists
	var count int
	_ = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM budget_horizons WHERE id = ?`, id).Scan(&count)
	if count > 0 {
		// If it was hidden, unhide it
		now := time.Now().UTC().UnixMicro()
		_, err := tenantDB.ExecContext(ctx, `UPDATE budget_horizons SET is_hidden = 0, updated_at = ? WHERE id = ?`, now, id)
		if err != nil {
			return nil, err
		}
	} else {
		now := time.Now().UTC().UnixMicro()
		// Get max sort_order
		var maxSort int
		_ = tenantDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) FROM budget_horizons`).Scan(&maxSort)
		newSort := maxSort + 10

		_, err := tenantDB.ExecContext(ctx, `
			INSERT INTO budget_horizons (id, horizon_type, days_value, is_system, is_hidden, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, 0, 0, ?, ?, ?)
		`, id, input.HorizonType, input.DaysValue, newSort, now, now)
		if err != nil {
			return nil, err
		}
	}

	var h BudgetHorizon
	var daysVal sql.NullInt64
	var isSys, isHid int
	err := tenantDB.QueryRowContext(ctx, `
		SELECT id, horizon_type, days_value, is_system, is_hidden, sort_order, created_at, updated_at
		FROM budget_horizons WHERE id = ?`, id).Scan(&h.ID, &h.HorizonType, &daysVal, &isSys, &isHid, &h.SortOrder, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return nil, err
	}
	h.IsSystem = isSys == 1
	h.IsHidden = isHid == 1
	if daysVal.Valid {
		v := int(daysVal.Int64)
		h.DaysValue = &v
	}
	return &h, nil
}

// FindUsersWithDefaultHorizon finds users in the budget who currently have horizonID as their default.
func FindUsersWithDefaultHorizon(ctx context.Context, tenantDB *sql.DB, mainDB *sql.DB, budgetID, horizonID string) ([]UserHorizonSummary, error) {
	rows, err := tenantDB.QueryContext(ctx, `SELECT user_id FROM budget_preferences WHERE key = 'default_horizon_id' AND value = ?`, horizonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var userIDs []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err == nil {
			userIDs = append(userIDs, uid)
		}
	}

	if len(userIDs) == 0 {
		return nil, nil
	}

	// Enrich from mainDB users table
	var summaries []UserHorizonSummary
	for _, uid := range userIDs {
		var name, email string
		_ = mainDB.QueryRowContext(ctx, `SELECT display_name, email FROM users WHERE id = ?`, uid).Scan(&name, &email)
		if name == "" {
			name = email
		}
		summaries = append(summaries, UserHorizonSummary{
			UserID:      uid,
			DisplayName: name,
			Email:       email,
		})
	}
	return summaries, nil
}

// UpdateHorizonVisibilityInput contains parameters for hiding/unhiding a horizon.
type UpdateHorizonVisibilityInput struct {
	HorizonID        string
	Hide             bool
	FallbackTargetID string // Required if users are affected by hiding
}

// SetHorizonHidden toggles the is_hidden flag. If Hide is true and users use it as default, reassigsn them to FallbackTargetID.
func SetHorizonHidden(ctx context.Context, tenantDB *sql.DB, input UpdateHorizonVisibilityInput) error {
	horizonID := strings.TrimSpace(input.HorizonID)
	if horizonID == "" {
		return errors.New("horizonID is required")
	}

	// Count visible horizons
	if input.Hide {
		var visibleCount int
		_ = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM budget_horizons WHERE is_hidden = 0 AND id != ?`, horizonID).Scan(&visibleCount)
		if visibleCount < 1 {
			return errors.New("cannot hide the last visible horizon")
		}

		// Check if any users have this as default
		var affectedCount int
		_ = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM budget_preferences WHERE key = 'default_horizon_id' AND value = ?`, horizonID).Scan(&affectedCount)
		if affectedCount > 0 {
			fallback := strings.TrimSpace(input.FallbackTargetID)
			if fallback == "" || fallback == horizonID {
				return errors.New("fallbackTargetID is required to reassign users of the hidden horizon")
			}
			// Verify fallback is visible
			var fbHidden int
			err := tenantDB.QueryRowContext(ctx, `SELECT is_hidden FROM budget_horizons WHERE id = ?`, fallback).Scan(&fbHidden)
			if err != nil || fbHidden != 0 {
				return errors.New("selected fallback horizon does not exist or is hidden")
			}
			// Reassign users in transaction
			now := time.Now().UTC().UnixMicro()
			tx, err := tenantDB.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()

			_, err = tx.ExecContext(ctx, `UPDATE budget_preferences SET value = ?, updated_at = ? WHERE key = 'default_horizon_id' AND value = ?`, fallback, now, horizonID)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE budget_horizons SET is_hidden = 1, updated_at = ? WHERE id = ?`, now, horizonID)
			if err != nil {
				return err
			}
			return tx.Commit()
		}
	}

	now := time.Now().UTC().UnixMicro()
	hiddenVal := 0
	if input.Hide {
		hiddenVal = 1
	}
	_, err := tenantDB.ExecContext(ctx, `UPDATE budget_horizons SET is_hidden = ?, updated_at = ? WHERE id = ?`, hiddenVal, now, horizonID)
	return err
}

// DeleteCustomHorizon deletes a non-system horizon. If users use it as default, reassigns them to fallbackTargetID.
func DeleteCustomHorizon(ctx context.Context, tenantDB *sql.DB, horizonID, fallbackTargetID string) error {
	cleanID := strings.TrimSpace(horizonID)
	if cleanID == "" {
		return errors.New("horizonID is required")
	}

	var isSystem int
	err := tenantDB.QueryRowContext(ctx, `SELECT is_system FROM budget_horizons WHERE id = ?`, cleanID).Scan(&isSystem)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("horizon not found")
		}
		return err
	}
	if isSystem == 1 {
		return errors.New("system horizons cannot be deleted; they may only be hidden")
	}

	// Check if this was the last visible horizon
	var visibleCount int
	_ = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM budget_horizons WHERE is_hidden = 0 AND id != ?`, cleanID).Scan(&visibleCount)
	if visibleCount < 1 {
		return errors.New("cannot delete the last visible horizon")
	}

	now := time.Now().UTC().UnixMicro()
	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Check if any users have this as default
	var affectedCount int
	_ = tx.QueryRowContext(ctx, `SELECT count(*) FROM budget_preferences WHERE key = 'default_horizon_id' AND value = ?`, cleanID).Scan(&affectedCount)
	if affectedCount > 0 {
		fallback := strings.TrimSpace(fallbackTargetID)
		if fallback == "" || fallback == cleanID {
			return errors.New("fallbackTargetID is required to delete a horizon currently assigned as default")
		}
		var fbHidden int
		err := tx.QueryRowContext(ctx, `SELECT is_hidden FROM budget_horizons WHERE id = ?`, fallback).Scan(&fbHidden)
		if err != nil || fbHidden != 0 {
			return errors.New("selected fallback horizon does not exist or is hidden")
		}
		_, err = tx.ExecContext(ctx, `UPDATE budget_preferences SET value = ?, updated_at = ? WHERE key = 'default_horizon_id' AND value = ?`, fallback, now, cleanID)
		if err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM budget_horizons WHERE id = ?`, cleanID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// CleanUnusedMemberPreferences cleans preferences for users not in memberList during cloning or member removals.
func CleanUnusedMemberPreferences(ctx context.Context, tenantDB *sql.DB, activeUserIDs []string) error {
	if len(activeUserIDs) == 0 {
		_, err := tenantDB.ExecContext(ctx, `DELETE FROM budget_preferences`)
		return err
	}
	placeholders := make([]string, len(activeUserIDs))
	args := make([]any, len(activeUserIDs))
	for i, id := range activeUserIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`DELETE FROM budget_preferences WHERE user_id NOT IN (%s)`, strings.Join(placeholders, ","))
	_, err := tenantDB.ExecContext(ctx, query, args...)
	return err
}
