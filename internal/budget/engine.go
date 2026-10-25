package budget

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Non-spending strategies
const (
	StrategyRollOver          = "ROLL_OVER"
	StrategyReturnToPool      = "RETURN_TO_POOL"
	StrategyArrearsCumulative = "ARREARS_CUMULATIVE"
	StrategyCapAndHold        = "CAP_AND_HOLD"
	StrategyProrateFuture     = "PRORATE_FUTURE"
)

// Recurrence cadences
const (
	CadenceOneOff       = "ONE_OFF"
	CadenceWeekly       = "WEEKLY"
	CadenceMonthly      = "MONTHLY"
	CadenceAnnual       = "ANNUAL"
	CadenceCustomDays   = "CUSTOM_DAYS"
	CadenceCustomMonths = "CUSTOM_MONTHS"
	CadenceCustomYears  = "CUSTOM_YEARS"
)

// TargetDefinition represents a target evaluated within a rolling window.
type TargetDefinition struct {
	ID                  string
	BucketID            string
	Name                string
	Priority            int // higher = more important (1=Optional, 2=Recommended, 3=Mandatory)
	SortOrder           int
	AmountMinor         int64 // Evaluated or static required cycle/target amount
	Currency            string
	RecurrenceCadence   string
	RecurrenceInterval  int   // >= 1 for CUSTOM_DAYS, CUSTOM_MONTHS, CUSTOM_YEARS
	DueDate             int64 // UTC microsecond timestamp
	NonSpendingStrategy string
	CeilingAmountMinor  *int64
	CurrentFundedMinor  int64
	CurrentBalanceMinor int64 // Current unspent balance attributed to this target
	ArrearsMinor        int64 // Cumulative unpaid debt from previous periods for ARREARS_CUMULATIVE
}

// CalendarDaysBetween returns the number of calendar days from t1 to t2 in the given location.
// Times are converted to the location and truncated to midnight.
func CalendarDaysBetween(t1, t2 time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	t1Loc := t1.In(loc)
	t2Loc := t2.In(loc)
	d1 := time.Date(t1Loc.Year(), t1Loc.Month(), t1Loc.Day(), 0, 0, 0, 0, loc)
	d2 := time.Date(t2Loc.Year(), t2Loc.Month(), t2Loc.Day(), 0, 0, 0, 0, loc)
	return int(math.Round(d2.Sub(d1).Hours() / 24))
}

// DaysRemainingInCurrentMonth returns the number of days remaining until the end of the current month
// in the given timezone (from today's calendar date to the last calendar day of the month).
// Minimum return value is 1 day (on the last day of the month).
func DaysRemainingInCurrentMonth(now time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	nowLoc := now.In(loc)
	d1 := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), 0, 0, 0, 0, loc)
	firstOfNext := time.Date(nowLoc.Year(), nowLoc.Month()+1, 1, 0, 0, 0, 0, loc)
	lastDay := firstOfNext.AddDate(0, 0, -1)
	days := int(math.Round(lastDay.Sub(d1).Hours() / 24))
	if days < 1 {
		return 1
	}
	return days
}

// DaysRemainingInCurrentYear returns the number of days remaining until the end of the current year
// in the given timezone (from today's calendar date to Dec 31).
// Minimum return value is 1 day (on the last day of the year).
func DaysRemainingInCurrentYear(now time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	nowLoc := now.In(loc)
	d1 := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), 0, 0, 0, 0, loc)
	lastDay := time.Date(nowLoc.Year(), 12, 31, 0, 0, 0, 0, loc)
	days := int(math.Round(lastDay.Sub(d1).Hours() / 24))
	if days < 1 {
		return 1
	}
	return days
}

// CalculateHorizonDays parses a horizon identifier and calculates the effective requirement window in days.
// Supported horizons: "monthly", "30d", "90d", "annual", "365d". Default is "monthly".
func CalculateHorizonDays(horizon string, now time.Time, loc *time.Location) (cleanHorizon string, windowDays int) {
	if loc == nil {
		loc = time.UTC
	}
	switch strings.ToLower(strings.TrimSpace(horizon)) {
	case "90d":
		return "90d", 90
	case "30d":
		return "30d", 30
	case "365d":
		return "365d", 365
	case "annual":
		return "annual", DaysRemainingInCurrentYear(now, loc)
	case "monthly":
		return "monthly", DaysRemainingInCurrentMonth(now, loc)
	default:
		return "monthly", DaysRemainingInCurrentMonth(now, loc)
	}
}

// ComputeTargetRequirement computes the target requirement in minor units for windowDays assuming UTC.
func ComputeTargetRequirement(t *TargetDefinition, windowDays int, nowUTC time.Time) int64 {
	return ComputeTargetRequirementInLoc(t, windowDays, nowUTC, time.UTC)
}

// ComputeTargetRequirementInLoc computes the exact funding requirement in minor units for windowDays in the given timezone.
// For targets with a due date:
//   - If due date is in the past: 100% required immediately.
//   - If due date <= windowDays: 100% required in this window.
//   - If due date > windowDays: prorated by ceil(AmountMinor * (windowDays / daysUntilDue)).
// For recurring targets: evaluated based on occurrences in the window.
// For undated one-off targets: 0 required (open-ended piggybank/buffer).
func ComputeTargetRequirementInLoc(t *TargetDefinition, windowDays int, now time.Time, loc *time.Location) int64 {
	if windowDays < 1 {
		windowDays = 1
	}
	if loc == nil {
		loc = time.UTC
	}

	switch t.RecurrenceCadence {
	case CadenceOneOff:
		if t.DueDate == 0 {
			// Undated one-off targets have 0 requirement (open-ended piggybank/buffer)
			return 0
		}
		dueTime := time.UnixMicro(t.DueDate).In(loc)
		daysUntilDue := CalendarDaysBetween(now, dueTime, loc)
		if daysUntilDue <= 0 {
			// Overdue: 100% required immediately
			return t.AmountMinor
		}
		if daysUntilDue <= windowDays {
			// Due within this window: 100% required
			return t.AmountMinor
		}
		// Due beyond window: proportional
		prorated := float64(t.AmountMinor) * (float64(windowDays) / float64(daysUntilDue))
		return int64(math.Ceil(prorated))

	case CadenceWeekly:
		return computeRecurring(t, windowDays, 7)
	case CadenceMonthly:
		if t.DueDate > 0 {
			return computeCalendarRecurring(t, windowDays, now, loc, 1, 0)
		}
		return computeRecurring(t, windowDays, 30)
	case CadenceAnnual:
		if t.DueDate > 0 {
			return computeCalendarRecurring(t, windowDays, now, loc, 0, 1)
		}
		return computeRecurring(t, windowDays, 365)
	case CadenceCustomDays:
		n := t.RecurrenceInterval
		if n < 1 {
			n = 1
		}
		return computeRecurring(t, windowDays, n)
	case CadenceCustomMonths:
		interval := t.RecurrenceInterval
		if interval < 1 {
			interval = 1
		}
		return computeCalendarRecurring(t, windowDays, now, loc, interval, 0)
	case CadenceCustomYears:
		interval := t.RecurrenceInterval
		if interval < 1 {
			interval = 1
		}
		return computeCalendarRecurring(t, windowDays, now, loc, 0, interval)
	default:
		return t.AmountMinor
	}
}

func computeCalendarRecurring(t *TargetDefinition, windowDays int, now time.Time, loc *time.Location, monthsStep, yearsStep int) int64 {
	if monthsStep < 0 {
		monthsStep = 0
	}
	if yearsStep < 0 {
		yearsStep = 0
	}
	if monthsStep == 0 && yearsStep == 0 {
		monthsStep = 1
	}
	if loc == nil {
		loc = time.UTC
	}

	if t.DueDate == 0 {
		cycleDays := monthsStep*30 + yearsStep*365
		if cycleDays < 1 {
			cycleDays = 30
		}
		return computeRecurring(t, windowDays, cycleDays)
	}

	anchor := time.UnixMicro(t.DueDate).In(loc)
	nowInLoc := now.In(loc)
	windowEnd := nowInLoc.AddDate(0, 0, windowDays)

	// Step forward or backward to align with now
	curr := anchor
	if curr.Before(nowInLoc) {
		for curr.Before(nowInLoc) {
			curr = curr.AddDate(yearsStep, monthsStep, 0)
		}
	} else {
		for {
			prev := curr.AddDate(-yearsStep, -monthsStep, 0)
			if !prev.After(nowInLoc) {
				break
			}
			curr = prev
		}
	}

	// Count occurrences within [nowInLoc, windowEnd]
	occurrences := 0
	lastOccurrence := time.Time{}
	iter := curr
	for !iter.After(windowEnd) {
		occurrences++
		lastOccurrence = iter
		iter = iter.AddDate(yearsStep, monthsStep, 0)
	}

	var baseRequired int64
	if occurrences > 0 {
		baseRequired = int64(occurrences) * t.AmountMinor
		// Tail pro-rating: if there are remaining days between lastOccurrence and windowEnd,
		// pro-rate towards the next cycle.
		remDays := CalendarDaysBetween(lastOccurrence, windowEnd, loc)
		if remDays > 0 {
			nextCycleDays := CalendarDaysBetween(lastOccurrence, iter, loc)
			if nextCycleDays > 0 {
				tailProrated := float64(t.AmountMinor) * (float64(remDays) / float64(nextCycleDays))
				baseRequired += int64(math.Ceil(tailProrated))
			}
		}
	} else {
		// Target due date is beyond windowEnd: prorate proportional to windowDays / daysUntilNext
		daysUntilNext := CalendarDaysBetween(nowInLoc, curr, loc)
		if daysUntilNext <= windowDays || daysUntilNext <= 0 {
			baseRequired = t.AmountMinor
		} else {
			prorated := float64(t.AmountMinor) * (float64(windowDays) / float64(daysUntilNext))
			baseRequired = int64(math.Ceil(prorated))
		}
	}

	// Strategy specific adjustments
	switch t.NonSpendingStrategy {
	case StrategyCapAndHold:
		if t.CeilingAmountMinor != nil {
			ceiling := *t.CeilingAmountMinor
			if t.CurrentBalanceMinor >= ceiling {
				return 0
			}
			needed := ceiling - t.CurrentBalanceMinor
			if baseRequired > needed {
				return needed
			}
		}
	case StrategyRollOver:
		if t.CurrentBalanceMinor > 0 {
			baseRequired -= t.CurrentBalanceMinor
			if baseRequired < 0 {
				baseRequired = 0
			}
		}
	case StrategyArrearsCumulative:
		baseRequired += t.ArrearsMinor
	}

	if baseRequired < 0 {
		return 0
	}
	return baseRequired
}

func computeRecurring(t *TargetDefinition, windowDays, cycleDays int) int64 {
	if cycleDays < 1 {
		cycleDays = 1
	}
	occurrences := windowDays / cycleDays
	var baseRequired int64
	if occurrences < 1 {
		baseRequired = t.AmountMinor
	} else {
		baseRequired = int64(occurrences) * t.AmountMinor
		remDays := windowDays % cycleDays
		if remDays > 0 {
			tailProrated := float64(t.AmountMinor) * (float64(remDays) / float64(cycleDays))
			baseRequired += int64(math.Ceil(tailProrated))
		}
	}

	// Strategy specific adjustments
	switch t.NonSpendingStrategy {
	case StrategyCapAndHold:
		if t.CeilingAmountMinor != nil {
			ceiling := *t.CeilingAmountMinor
			if t.CurrentBalanceMinor >= ceiling {
				return 0
			}
			needed := ceiling - t.CurrentBalanceMinor
			if baseRequired > needed {
				return needed
			}
		}
	case StrategyRollOver:
		if t.CurrentBalanceMinor > 0 {
			baseRequired -= t.CurrentBalanceMinor
			if baseRequired < 0 {
				baseRequired = 0
			}
		}
	case StrategyArrearsCumulative:
		baseRequired += t.ArrearsMinor
	}

	if baseRequired < 0 {
		return 0
	}
	return baseRequired
}

// WaterfallSort sorts targets by: priority DESC, due_date ASC (nulls last), sort_order ASC.
func WaterfallSort(targets []*TargetDefinition) {
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority // higher priority first
		}
		if a.DueDate != b.DueDate {
			if a.DueDate == 0 {
				return false // nulls last
			}
			if b.DueDate == 0 {
				return true
			}
			return a.DueDate < b.DueDate // earlier due date first
		}
		return a.SortOrder < b.SortOrder
	})
}

// AutoAssignResult records assigned minor funds per target.
type AutoAssignAllocation struct {
	TargetID string
	Assigned int64
}

// PriorityAutoAssign allocates available pool across targets in waterfall order.
func PriorityAutoAssign(targets []*TargetDefinition, windowDays int, nowUTC time.Time, availablePool int64) ([]AutoAssignAllocation, int64) {
	sorted := make([]*TargetDefinition, len(targets))
	copy(sorted, targets)
	WaterfallSort(sorted)

	var allocations []AutoAssignAllocation
	remainingPool := availablePool

	for _, t := range sorted {
		if remainingPool <= 0 {
			break
		}
		required := ComputeTargetRequirement(t, windowDays, nowUTC)
		needed := required - t.CurrentFundedMinor
		if needed <= 0 {
			continue
		}

		assign := needed
		if assign > remainingPool {
			assign = remainingPool
		}

		allocations = append(allocations, AutoAssignAllocation{
			TargetID: t.ID,
			Assigned: assign,
		})
		t.CurrentFundedMinor += assign
		remainingPool -= assign
	}

	return allocations, remainingPool
}
