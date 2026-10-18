package clock

import (
	"sync"
	"time"
)

// Clock provides an interface to access current time and location.
type Clock interface {
	Now() time.Time
	Location() *time.Location
	Today() string // Formatted as YYYY-MM-DD in the clock's location
}

// SystemClock is a real wall-clock implementation locked to a specific timezone.
type SystemClock struct {
	loc *time.Location
}

// NewSystemClock creates a SystemClock with the given location. If loc is nil, UTC is used.
func NewSystemClock(loc *time.Location) *SystemClock {
	if loc == nil {
		loc = time.UTC
	}
	return &SystemClock{loc: loc}
}

// Now returns the current system time in the clock's location.
func (c *SystemClock) Now() time.Time {
	return time.Now().In(c.loc)
}

// Location returns the clock's configured timezone.
func (c *SystemClock) Location() *time.Location {
	return c.loc
}

// Today returns the current date string (YYYY-MM-DD).
func (c *SystemClock) Today() string {
	return c.Now().Format("2006-01-02")
}

// ManualClock is a goroutine-safe controllable clock for simulations and testing.
type ManualClock struct {
	mu  sync.RWMutex
	cur time.Time
	loc *time.Location
}

// NewManualClock creates a ManualClock with an initial date/time and timezone.
func NewManualClock(initial time.Time, loc *time.Location) *ManualClock {
	if loc == nil {
		loc = time.UTC
	}
	return &ManualClock{
		cur: initial.In(loc),
		loc: loc,
	}
}

// Now returns the simulated current time.
func (m *ManualClock) Now() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Location returns the simulated clock's timezone location.
func (m *ManualClock) Location() *time.Location {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loc
}

// Today returns the simulated date string (YYYY-MM-DD).
func (m *ManualClock) Today() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur.Format("2006-01-02")
}

// SetDate sets the year, month, and day while preserving the current time-of-day.
func (m *ManualClock) SetDate(year, month, day int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = time.Date(year, time.Month(month), day, m.cur.Hour(), m.cur.Minute(), m.cur.Second(), m.cur.Nanosecond(), m.loc)
}

// SetDateTime sets the exact current time.
func (m *ManualClock) SetDateTime(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = t.In(m.loc)
}

// IncreaseDateTimeBySecs advances the clock by n seconds.
func (m *ManualClock) IncreaseDateTimeBySecs(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = m.cur.Add(time.Duration(n) * time.Second)
}

// IncreaseDateTimeByMinutes advances the clock by n minutes.
func (m *ManualClock) IncreaseDateTimeByMinutes(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = m.cur.Add(time.Duration(n) * time.Minute)
}

// IncreaseDateTimeByHours advances the clock by n hours.
func (m *ManualClock) IncreaseDateTimeByHours(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = m.cur.Add(time.Duration(n) * time.Hour)
}

// IncreaseDateTimeByDays advances the clock by n days.
func (m *ManualClock) IncreaseDateTimeByDays(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = m.cur.AddDate(0, 0, n)
}

// IncreaseDateTimeByMonths advances the clock by n months.
func (m *ManualClock) IncreaseDateTimeByMonths(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = m.cur.AddDate(0, n, 0)
}

// IncreaseDateTimeByYears advances the clock by n years.
func (m *ManualClock) IncreaseDateTimeByYears(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = m.cur.AddDate(n, 0, 0)
}
