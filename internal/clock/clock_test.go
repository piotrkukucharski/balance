package clock

import (
	"testing"
	"time"
)

func TestSystemClock(t *testing.T) {
	c := NewSystemClock(time.UTC)
	if c.Location() != time.UTC {
		t.Fatalf("expected UTC location, got %v", c.Location())
	}
	now := c.Now()
	if now.Location() != time.UTC {
		t.Fatalf("expected UTC time, got %v", now.Location())
	}
	today := c.Today()
	expectedToday := now.Format("2006-01-02")
	if today != expectedToday {
		t.Fatalf("expected today %s, got %s", expectedToday, today)
	}
}

func TestManualClock(t *testing.T) {
	initial := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewManualClock(initial, time.UTC)

	if m.Today() != "2026-01-01" {
		t.Fatalf("expected 2026-01-01, got %s", m.Today())
	}

	m.IncreaseDateTimeBySecs(30)
	if m.Now().Second() != 30 {
		t.Fatalf("expected second 30, got %d", m.Now().Second())
	}

	m.IncreaseDateTimeByMinutes(15)
	if m.Now().Minute() != 15 {
		t.Fatalf("expected minute 15, got %d", m.Now().Minute())
	}

	m.IncreaseDateTimeByHours(2)
	if m.Now().Hour() != 14 {
		t.Fatalf("expected hour 14, got %d", m.Now().Hour())
	}

	m.IncreaseDateTimeByDays(5)
	if m.Now().Day() != 6 {
		t.Fatalf("expected day 6, got %d", m.Now().Day())
	}

	m.IncreaseDateTimeByMonths(2)
	if m.Now().Month() != time.March {
		t.Fatalf("expected month March, got %v", m.Now().Month())
	}

	m.IncreaseDateTimeByYears(1)
	if m.Now().Year() != 2027 {
		t.Fatalf("expected year 2027, got %d", m.Now().Year())
	}

	m.SetDate(2025, 12, 25)
	if m.Today() != "2025-12-25" {
		t.Fatalf("expected 2025-12-25, got %s", m.Today())
	}

	newDt := time.Date(2030, 6, 1, 8, 30, 0, 0, time.UTC)
	m.SetDateTime(newDt)
	if !m.Now().Equal(newDt) {
		t.Fatalf("expected %v, got %v", newDt, m.Now())
	}
}
