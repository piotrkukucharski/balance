# Background Job Scheduler (`internal/scheduler`)

## Overview
Background job scheduler orchestrating recurring maintenance tasks, FX rate updates, target date advances, and report cache warming.

## Interface
- `type Mode string` [scheduler.go:19]
- `func ParseMode(val string) (Mode, error)` [scheduler.go:29]
- `type Config struct` [scheduler.go:38]
- `type Scheduler struct` [scheduler.go:47]
- `func New(cfg Config) *Scheduler` [scheduler.go:55]
- `func (s *Scheduler) Start(ctx context.Context) error` [scheduler.go:70]
- `func (s *Scheduler) Stop()` [scheduler.go:95]

## Invariants
- Scheduler runs only in enabled modes (SCHEDULER_MODE=worker or all) [scheduler.go:25-35]
- Periodic jobs acquire distributed or local execution locks to prevent overlapping runs [scheduler.go:75-90]

## Side Effects
- Spawns background goroutines running periodic cron tickers [scheduler.go:70-85]
- Triggers recurring database sync routines (FX rates, target renewals) [scheduler.go:80-95]

## Verification
- Run: `go test ./...`
