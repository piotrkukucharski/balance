package budget

import (
	"testing"
	"time"
)

func ptr[T any](v T) *T {
	return &v
}

func TestRecurrenceSchedule_MonthlyClamping(t *testing.T) {
	loc := time.UTC
	// Target day is 31st
	sched := RecurrenceSchedule{
		Cadence:    CadenceMonthly,
		DayOfMonth: ptr(31),
	}
	if err := sched.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// 1. On Jan 15, 2026 -> should return Jan 31, 2026
	nowJan15 := time.Date(2026, 1, 15, 12, 0, 0, 0, loc)
	next, ok := sched.NextDueDate(nowJan15, loc)
	if !ok || next != time.Date(2026, 1, 31, 0, 0, 0, 0, loc) {
		t.Fatalf("expected 2026-01-31, got %v (ok=%v)", next, ok)
	}

	// 2. On Jan 31, 2026 (exact day of due date) -> should still return Jan 31, 2026
	nowJan31 := time.Date(2026, 1, 31, 23, 59, 0, 0, loc)
	next, ok = sched.NextDueDate(nowJan31, loc)
	if !ok || next != time.Date(2026, 1, 31, 0, 0, 0, 0, loc) {
		t.Fatalf("expected 2026-01-31 on the day itself, got %v", next)
	}

	// 3. On Feb 1, 2026 -> February 2026 has 28 days -> clamped to Feb 28, 2026
	nowFeb1 := time.Date(2026, 2, 1, 10, 0, 0, 0, loc)
	next, ok = sched.NextDueDate(nowFeb1, loc)
	if !ok || next != time.Date(2026, 2, 28, 0, 0, 0, 0, loc) {
		t.Fatalf("expected 2026-02-28, got %v", next)
	}

	// 4. Leap year test: Feb 1, 2028 (leap year has 29 days) -> clamped to Feb 29, 2028
	nowFeb1Leap := time.Date(2028, 2, 1, 10, 0, 0, 0, loc)
	next, ok = sched.NextDueDate(nowFeb1Leap, loc)
	if !ok || next != time.Date(2028, 2, 29, 0, 0, 0, 0, loc) {
		t.Fatalf("expected 2028-02-29 in leap year, got %v", next)
	}

	// 5. April 2026 has 30 days -> clamped to April 30, 2026
	nowApr5 := time.Date(2026, 4, 5, 0, 0, 0, 0, loc)
	next, ok = sched.NextDueDate(nowApr5, loc)
	if !ok || next != time.Date(2026, 4, 30, 0, 0, 0, 0, loc) {
		t.Fatalf("expected 2026-04-30, got %v", next)
	}
}

func TestRecurrenceSchedule_Weekly(t *testing.T) {
	loc := time.UTC
	// Target day is Friday (ISO 5)
	sched := RecurrenceSchedule{
		Cadence:   CadenceWeekly,
		DayOfWeek: ptr(5),
	}
	if err := sched.Validate(); err != nil {
		t.Fatalf("validation error: %v", err)
	}

	// 2026-11-04 is Wednesday (ISO 3)
	nowWed := time.Date(2026, 11, 4, 15, 0, 0, 0, loc)
	next, ok := sched.NextDueDate(nowWed, loc)
	// Friday is 2026-11-06
	expected := time.Date(2026, 11, 6, 0, 0, 0, 0, loc)
	if !ok || next != expected {
		t.Fatalf("expected %v, got %v", expected, next)
	}

	// On Friday itself (2026-11-06) -> returns 2026-11-06
	nowFri := time.Date(2026, 11, 6, 18, 0, 0, 0, loc)
	next, ok = sched.NextDueDate(nowFri, loc)
	if !ok || next != expected {
		t.Fatalf("expected same day Friday %v, got %v", expected, next)
	}

	// On Saturday (2026-11-07) -> returns next Friday (2026-11-13)
	nowSat := time.Date(2026, 11, 7, 9, 0, 0, 0, loc)
	next, ok = sched.NextDueDate(nowSat, loc)
	expectedNextFri := time.Date(2026, 11, 13, 0, 0, 0, 0, loc)
	if !ok || next != expectedNextFri {
		t.Fatalf("expected next Friday %v, got %v", expectedNextFri, next)
	}
}

func TestRecurrenceSchedule_Annual(t *testing.T) {
	loc := time.UTC
	// Feb 29 on leap year
	sched := RecurrenceSchedule{
		Cadence:     CadenceAnnual,
		DayOfMonth:  ptr(29),
		MonthOfYear: ptr(2),
	}
	if err := sched.Validate(); err != nil {
		t.Fatalf("validation error: %v", err)
	}

	// In 2026 (non-leap year), on Jan 10 -> returns Feb 28, 2026
	now2026 := time.Date(2026, 1, 10, 0, 0, 0, 0, loc)
	next, ok := sched.NextDueDate(now2026, loc)
	if !ok || next != time.Date(2026, 2, 28, 0, 0, 0, 0, loc) {
		t.Fatalf("expected 2026-02-28, got %v", next)
	}

	// In 2028 (leap year), on Jan 10 -> returns Feb 29, 2028
	now2028 := time.Date(2028, 1, 10, 0, 0, 0, 0, loc)
	next, ok = sched.NextDueDate(now2028, loc)
	if !ok || next != time.Date(2028, 2, 29, 0, 0, 0, 0, loc) {
		t.Fatalf("expected 2028-02-29, got %v", next)
	}
}

func TestShouldAdvanceDueDate(t *testing.T) {
	loc := time.UTC
	// Due date: Feb 20, 2026
	dueMicros := time.Date(2026, 2, 20, 0, 0, 0, 0, loc).UnixMicro()

	// 1. Feb 19 (day before) -> false
	if ShouldAdvanceDueDate(dueMicros, time.Date(2026, 2, 19, 23, 59, 0, 0, loc), loc) {
		t.Fatalf("should not advance on day before")
	}

	// 2. Feb 20 (exact day) -> false
	if ShouldAdvanceDueDate(dueMicros, time.Date(2026, 2, 20, 15, 30, 0, 0, loc), loc) {
		t.Fatalf("should not advance on the due day itself")
	}

	// 3. Feb 21 at 00:00:01 (day after) -> true
	if !ShouldAdvanceDueDate(dueMicros, time.Date(2026, 2, 21, 0, 0, 1, 0, loc), loc) {
		t.Fatalf("should advance after midnight of next day")
	}
}
