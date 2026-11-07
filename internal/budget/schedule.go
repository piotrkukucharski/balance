package budget

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Weekday constants and helpers
const (
	FirstDayMonday    = "MONDAY"
	FirstDayTuesday   = "TUESDAY"
	FirstDayWednesday = "WEDNESDAY"
	FirstDayThursday  = "THURSDAY"
	FirstDayFriday    = "FRIDAY"
	FirstDaySaturday  = "SATURDAY"
	FirstDaySunday    = "SUNDAY"
)

// ValidFirstDaysOfWeek contains all supported first day of week values.
var ValidFirstDaysOfWeek = map[string]int{
	FirstDayMonday:    1,
	FirstDayTuesday:   2,
	FirstDayWednesday: 3,
	FirstDayThursday:  4,
	FirstDayFriday:    5,
	FirstDaySaturday:  6,
	FirstDaySunday:    7,
}

// IsValidFirstDayOfWeek checks if the uppercase string is a valid first day.
func IsValidFirstDayOfWeek(day string) bool {
	_, ok := ValidFirstDaysOfWeek[strings.ToUpper(strings.TrimSpace(day))]
	return ok
}

// RecurrenceSchedule encapsulates the specification of when a target repeats
// and provides pure domain calculation for its next due date and end-of-month clamping.
type RecurrenceSchedule struct {
	Cadence      string // ONE_OFF, WEEKLY, MONTHLY, ANNUAL, CUSTOM_DAYS, CUSTOM_MONTHS, CUSTOM_YEARS
	Interval     int    // >= 1 for CUSTOM_DAYS, CUSTOM_MONTHS, CUSTOM_YEARS
	DayOfWeek    *int   // ISO 1..7 (1=Monday .. 7=Sunday) for WEEKLY
	DayOfMonth   *int   // 1..31 for MONTHLY, ANNUAL, CUSTOM_MONTHS, CUSTOM_YEARS
	MonthOfYear  *int   // 1..12 for ANNUAL, CUSTOM_MONTHS, CUSTOM_YEARS
	AnchorDate   *time.Time // For ONE_OFF or CUSTOM_DAYS
}

// Validate ensures all invariants of RecurrenceSchedule are met.
func (s RecurrenceSchedule) Validate() error {
	switch s.Cadence {
	case CadenceOneOff:
		// Optional AnchorDate (undated targets are allowed)
		return nil

	case CadenceWeekly:
		if s.DayOfWeek == nil || *s.DayOfWeek < 1 || *s.DayOfWeek > 7 {
			return errors.New("weekly cadence requires day_of_week between 1 and 7")
		}
		return nil

	case CadenceMonthly:
		if s.DayOfMonth == nil || *s.DayOfMonth < 1 || *s.DayOfMonth > 31 {
			return errors.New("monthly cadence requires day_of_month between 1 and 31")
		}
		return nil

	case CadenceAnnual:
		if s.DayOfMonth == nil || *s.DayOfMonth < 1 || *s.DayOfMonth > 31 {
			return errors.New("annual cadence requires day_of_month between 1 and 31")
		}
		if s.MonthOfYear == nil || *s.MonthOfYear < 1 || *s.MonthOfYear > 12 {
			return errors.New("annual cadence requires month_of_year between 1 and 12")
		}
		return nil

	case CadenceCustomDays:
		if s.Interval < 1 {
			return errors.New("custom days cadence requires interval >= 1")
		}
		return nil

	case CadenceCustomMonths:
		if s.Interval < 1 {
			return errors.New("custom months cadence requires interval >= 1")
		}
		if s.DayOfMonth == nil || *s.DayOfMonth < 1 || *s.DayOfMonth > 31 {
			return errors.New("custom months cadence requires day_of_month between 1 and 31")
		}
		if s.MonthOfYear == nil || *s.MonthOfYear < 1 || *s.MonthOfYear > 12 {
			return errors.New("custom months cadence requires month_of_year between 1 and 12")
		}
		return nil

	case CadenceCustomYears:
		if s.Interval < 1 {
			return errors.New("custom years cadence requires interval >= 1")
		}
		if s.DayOfMonth == nil || *s.DayOfMonth < 1 || *s.DayOfMonth > 31 {
			return errors.New("custom years cadence requires day_of_month between 1 and 31")
		}
		if s.MonthOfYear == nil || *s.MonthOfYear < 1 || *s.MonthOfYear > 12 {
			return errors.New("custom years cadence requires month_of_year between 1 and 12")
		}
		return nil

	default:
		return fmt.Errorf("unknown recurrence cadence: %q", s.Cadence)
	}
}

// DaysInMonth returns the number of days in the given year and month (1..12).
func DaysInMonth(year int, month int) int {
	// Day 0 of month+1 gives the last day of month
	return time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC).Day()
}

// ClampDayOfMonth clamps targetDay (1..31) to the actual maximum days in the given month and year.
// e.g. Day 31 in February 2026 -> 28; Day 31 in April 2026 -> 30.
func ClampDayOfMonth(year int, month int, targetDay int) int {
	maxDays := DaysInMonth(year, month)
	if targetDay > maxDays {
		return maxDays
	}
	if targetDay < 1 {
		return 1
	}
	return targetDay
}

// NextDueDate calculates the next effective due date at midnight (00:00:00) in the given location.
// Invariant: The returned due date is always strictly on or after `from`'s calendar date.
// If today's date matches the schedule, today is returned (the date only advances after midnight of this day has passed).
func (s RecurrenceSchedule) NextDueDate(from time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	fromInLoc := from.In(loc)
	todayMidnight := time.Date(fromInLoc.Year(), fromInLoc.Month(), fromInLoc.Day(), 0, 0, 0, 0, loc)

	switch s.Cadence {
	case CadenceOneOff:
		if s.AnchorDate == nil || s.AnchorDate.IsZero() {
			return time.Time{}, false
		}
		d := s.AnchorDate.In(loc)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), true

	case CadenceWeekly:
		if s.DayOfWeek == nil {
			return time.Time{}, false
		}
		targetISO := *s.DayOfWeek // 1=Mon .. 7=Sun
		// Go's time.Weekday: 0=Sun, 1=Mon .. 6=Sat
		currentISO := int(todayMidnight.Weekday())
		if currentISO == 0 {
			currentISO = 7
		}
		daysAhead := (targetISO - currentISO + 7) % 7
		return todayMidnight.AddDate(0, 0, daysAhead), true

	case CadenceMonthly:
		if s.DayOfMonth == nil {
			return time.Time{}, false
		}
		targetDay := *s.DayOfMonth
		year := todayMidnight.Year()
		month := int(todayMidnight.Month())

		// Candidate for this month with clamping
		candidateDay := ClampDayOfMonth(year, month, targetDay)
		candidate := time.Date(year, time.Month(month), candidateDay, 0, 0, 0, 0, loc)

		if !candidate.Before(todayMidnight) {
			return candidate, true
		}

		// Next month
		nextMonthDate := time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, loc)
		nextYear := nextMonthDate.Year()
		nextMonth := int(nextMonthDate.Month())
		nextDay := ClampDayOfMonth(nextYear, nextMonth, targetDay)
		return time.Date(nextYear, time.Month(nextMonth), nextDay, 0, 0, 0, 0, loc), true

	case CadenceAnnual:
		if s.DayOfMonth == nil || s.MonthOfYear == nil {
			return time.Time{}, false
		}
		targetDay := *s.DayOfMonth
		targetMonth := *s.MonthOfYear
		year := todayMidnight.Year()

		candidateDay := ClampDayOfMonth(year, targetMonth, targetDay)
		candidate := time.Date(year, time.Month(targetMonth), candidateDay, 0, 0, 0, 0, loc)

		if !candidate.Before(todayMidnight) {
			return candidate, true
		}

		nextYear := year + 1
		nextDay := ClampDayOfMonth(nextYear, targetMonth, targetDay)
		return time.Date(nextYear, time.Month(targetMonth), nextDay, 0, 0, 0, 0, loc), true

	case CadenceCustomDays:
		interval := s.Interval
		if interval < 1 {
			interval = 90
		}
		if s.AnchorDate == nil || s.AnchorDate.IsZero() {
			return todayMidnight.AddDate(0, 0, interval), true
		}
		anchorInLoc := s.AnchorDate.In(loc)
		anchorMidnight := time.Date(anchorInLoc.Year(), anchorInLoc.Month(), anchorInLoc.Day(), 0, 0, 0, 0, loc)

		if !anchorMidnight.Before(todayMidnight) {
			return anchorMidnight, true
		}
		// Step forward by interval days
		curr := anchorMidnight
		for curr.Before(todayMidnight) {
			curr = curr.AddDate(0, 0, interval)
		}
		return curr, true

	case CadenceCustomMonths:
		interval := s.Interval
		if interval < 1 {
			interval = 3
		}
		targetDay := 1
		if s.DayOfMonth != nil {
			targetDay = *s.DayOfMonth
		}
		startMonth := 1
		if s.MonthOfYear != nil {
			startMonth = *s.MonthOfYear
		}

		// Anchor based on current year or start month
		year := todayMidnight.Year()
		clampedDay := ClampDayOfMonth(year, startMonth, targetDay)
		curr := time.Date(year, time.Month(startMonth), clampedDay, 0, 0, 0, 0, loc)

		// If start date is far in future or past, align with todayMidnight
		if curr.Before(todayMidnight) {
			for curr.Before(todayMidnight) {
				// Step month
				nextBase := curr.AddDate(0, interval, 0)
				cDay := ClampDayOfMonth(nextBase.Year(), int(nextBase.Month()), targetDay)
				curr = time.Date(nextBase.Year(), nextBase.Month(), cDay, 0, 0, 0, 0, loc)
			}
		} else {
			for {
				prevBase := curr.AddDate(0, -interval, 0)
				cDay := ClampDayOfMonth(prevBase.Year(), int(prevBase.Month()), targetDay)
				prev := time.Date(prevBase.Year(), prevBase.Month(), cDay, 0, 0, 0, 0, loc)
				if prev.Before(todayMidnight) {
					break
				}
				curr = prev
			}
		}
		return curr, true

	case CadenceCustomYears:
		interval := s.Interval
		if interval < 1 {
			interval = 1
		}
		targetDay := 1
		if s.DayOfMonth != nil {
			targetDay = *s.DayOfMonth
		}
		targetMonth := 1
		if s.MonthOfYear != nil {
			targetMonth = *s.MonthOfYear
		}

		year := todayMidnight.Year()
		clampedDay := ClampDayOfMonth(year, targetMonth, targetDay)
		curr := time.Date(year, time.Month(targetMonth), clampedDay, 0, 0, 0, 0, loc)

		if curr.Before(todayMidnight) {
			for curr.Before(todayMidnight) {
				nextYear := curr.Year() + interval
				cDay := ClampDayOfMonth(nextYear, targetMonth, targetDay)
				curr = time.Date(nextYear, time.Month(targetMonth), cDay, 0, 0, 0, 0, loc)
			}
		} else {
			for {
				prevYear := curr.Year() - interval
				cDay := ClampDayOfMonth(prevYear, targetMonth, targetDay)
				prev := time.Date(prevYear, time.Month(targetMonth), cDay, 0, 0, 0, 0, loc)
				if prev.Before(todayMidnight) {
					break
				}
				curr = prev
			}
		}
		return curr, true
	}

	return time.Time{}, false
}

// ShouldAdvanceDueDate checks if the current due date is strictly in the past (yesterday or earlier).
// Per user decision: throughout the entire due date (e.g. Feb 20), the due date does not advance;
// it only advances after midnight of the next day (Feb 21 00:00:00).
func ShouldAdvanceDueDate(currentDueDateMicros int64, now time.Time, loc *time.Location) bool {
	if currentDueDateMicros <= 0 {
		return false
	}
	if loc == nil {
		loc = time.UTC
	}
	nowInLoc := now.In(loc)
	todayMidnight := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), 0, 0, 0, 0, loc)

	dueDateInLoc := time.UnixMicro(currentDueDateMicros).In(loc)
	dueDateMidnight := time.Date(dueDateInLoc.Year(), dueDateInLoc.Month(), dueDateInLoc.Day(), 0, 0, 0, 0, loc)

	return dueDateMidnight.Before(todayMidnight)
}
