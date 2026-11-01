package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/balance/balance/internal/fx"
	"github.com/balance/balance/internal/logger"
	"github.com/go-co-op/gocron/v2"
)

// Mode represents the scheduling strategy configured for the application.
type Mode string

const (
	// ModeExternal (default) delegates cron tasks to an external orchestrator (e.g. host cron, k8s CronJob).
	ModeExternal Mode = "external"
	// ModeInternal runs recurring tasks inside the server process using gocron/v2.
	ModeInternal Mode = "internal"
)

// ParseMode parses a string into a scheduler Mode, defaulting to ModeExternal.
func ParseMode(val string) Mode {
	v := strings.ToLower(strings.TrimSpace(val))
	if v == string(ModeInternal) {
		return ModeInternal
	}
	return ModeExternal
}

// Config holds configuration for the internal scheduler.
type Config struct {
	Mode         Mode
	FXSyncPeriod time.Duration
	MainDB       *sql.DB
}

// Scheduler wraps gocron.Scheduler and manages background recurring jobs.
type Scheduler struct {
	cfg       Config
	scheduler gocron.Scheduler
	mu        sync.Mutex
	started   bool
}

// New creates a new Scheduler instance.
func New(cfg Config) (*Scheduler, error) {
	if cfg.FXSyncPeriod <= 0 {
		cfg.FXSyncPeriod = 12 * time.Hour
	}

	return &Scheduler{
		cfg: cfg,
	}, nil
}

// Start boots up the background scheduler if Mode is ModeInternal.
// If Mode is ModeExternal, Start is a no-op and returns nil.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cfg.Mode != ModeInternal {
		logger.Info(ctx, "Scheduler mode is external; internal cron is disabled", "mode", s.cfg.Mode)
		return nil
	}

	if s.started {
		return nil
	}

	sch, err := gocron.NewScheduler()
	if err != nil {
		return fmt.Errorf("failed creating gocron scheduler: %w", err)
	}
	s.scheduler = sch

	// Register worker:sync-fx
	if s.cfg.MainDB != nil {
		syncTask := func() {
			jobCtx := logger.WithComponent(context.Background(), "worker:sync-fx")
			logger.Info(jobCtx, "Internal scheduler executing worker:sync-fx")
			fxService := fx.NewService(s.cfg.MainDB, nil, nil)
			synced, err := fxService.Sync(jobCtx, fx.SyncOptions{
				Days:   0, // auto-detect: 90 if empty, 1 if routine
				Source: "ALL",
			})
			if err != nil {
				logger.Error(jobCtx, "Internal scheduler worker:sync-fx failed", "error", err.Error())
				return
			}
			logger.Info(jobCtx, "Internal scheduler worker:sync-fx completed successfully", "synced_count", synced)
		}

		// Register job: runs periodically every FXSyncPeriod with singleton mode and immediate first execution
		_, err = s.scheduler.NewJob(
			gocron.DurationJob(s.cfg.FXSyncPeriod),
			gocron.NewTask(syncTask),
			gocron.WithSingletonMode(gocron.LimitModeReschedule),
			gocron.WithStartAt(gocron.WithStartImmediately()),
		)
		if err != nil {
			return fmt.Errorf("failed registering worker:sync-fx job: %w", err)
		}
	}

	s.scheduler.Start()
	s.started = true
	logger.Info(ctx, "Internal scheduler started successfully",
		"mode", s.cfg.Mode,
		"fx_sync_period", s.cfg.FXSyncPeriod.String(),
	)
	return nil
}

// Shutdown gracefully shuts down the running scheduler.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started || s.scheduler == nil {
		return nil
	}

	logger.Info(ctx, "Shutting down internal scheduler")
	err := s.scheduler.Shutdown()
	s.started = false
	s.scheduler = nil
	return err
}

// IsRunning reports whether the scheduler is currently active.
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}
