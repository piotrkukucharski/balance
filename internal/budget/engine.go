package budget

import (
	"math"
	"sort"
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
	CadenceOneOff     = "ONE_OFF"
	CadenceWeekly     = "WEEKLY"
	CadenceMonthly    = "MONTHLY"
	CadenceAnnual     = "ANNUAL"
	CadenceCustomDays = "CUSTOM_DAYS"
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
	RecurrenceDays      int   // >= 1 for CUSTOM_DAYS
	DueDate             int64 // UTC microsecond timestamp
	NonSpendingStrategy string
	CeilingAmountMinor  *int64
	CurrentFundedMinor  int64
	CurrentBalanceMinor int64 // Current unspent balance attributed to this target
	ArrearsMinor        int64 // Cumulative unpaid debt from previous periods for ARREARS_CUMULATIVE
}

// TargetRequirement computes the exact funding requirement in minor units for windowDays.
// Given horizon H days starting at Tstart:
// 1. One-off target:
//   - If Tdue <= Tstart + H: full remaining amount U (AmountMinor - CurrentFundedMinor)
//   - If Tdue > Tstart + H: Prorated: U * (H / (Tdue - Tstart))
//
// 2. Recurring target:
//   - Number of cycles in window H: floor(H / N) (with N >= 1)
func ComputeTargetRequirement(t *TargetDefinition, windowDays int, nowUTC time.Time) int64 {
	if windowDays < 1 {
		windowDays = 1
	}

	remainingGoal := t.AmountMinor - t.CurrentFundedMinor
	if remainingGoal < 0 {
		remainingGoal = 0
	}

	switch t.RecurrenceCadence {
	case CadenceOneOff:
		if t.DueDate == 0 {
			return remainingGoal
		}
		dueTime := time.UnixMicro(t.DueDate)
		windowEnd := nowUTC.AddDate(0, 0, windowDays)
		if !dueTime.After(windowEnd) {
			// Due within this window
			return remainingGoal
		}
		// Due beyond window: prorate
		totalDaysUntilDue := int(math.Ceil(dueTime.Sub(nowUTC).Hours() / 24))
		if totalDaysUntilDue <= windowDays || totalDaysUntilDue <= 0 {
			return remainingGoal
		}
		prorated := float64(remainingGoal) * (float64(windowDays) / float64(totalDaysUntilDue))
		return int64(math.Round(prorated))

	case CadenceWeekly:
		return computeRecurring(t, windowDays, 7)
	case CadenceMonthly:
		return computeRecurring(t, windowDays, 30)
	case CadenceAnnual:
		return computeRecurring(t, windowDays, 365)
	case CadenceCustomDays:
		n := t.RecurrenceDays
		if n < 1 {
			n = 1
		}
		return computeRecurring(t, windowDays, n)
	default:
		return remainingGoal
	}
}

func computeRecurring(t *TargetDefinition, windowDays, cycleDays int) int64 {
	occurrences := windowDays / cycleDays
	if occurrences < 1 {
		occurrences = 1
	}
	baseRequired := int64(occurrences) * t.AmountMinor

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

	unfunded := baseRequired - t.CurrentFundedMinor
	if unfunded < 0 {
		return 0
	}
	return unfunded
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
		needed := ComputeTargetRequirement(t, windowDays, nowUTC)
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
		remainingPool -= assign
	}

	return allocations, remainingPool
}
