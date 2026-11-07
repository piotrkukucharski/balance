# Deterministic Clock Abstraction (`internal/clock`)

## Overview
Deterministic time abstraction providing mockable clocks and timezone conversions for scheduling, reporting, and event-sourced auditing.

## Interface
- `type Clock interface` [clock.go:9]
- `type SystemClock struct` [clock.go:16]
- `func NewSystemClock(loc *time.Location) *SystemClock` [clock.go:21]
- `func (c *SystemClock) Now() time.Time` [clock.go:29]
- `func (c *SystemClock) Location() *time.Location` [clock.go:34]
- `func (c *SystemClock) Today() time.Time` [clock.go:39]

## Invariants
- All timestamps internally normalize to UTC microsecond epochs unless explicitly formatting for a target timezone [clock.go:25-45]
- SystemClock delegates directly to time.Now() while allowing mock implementations in tests [clock.go:29-35]

## Side Effects
- Reads OS system clock [clock.go:30]

## Verification
- Run: `go test ./...`
