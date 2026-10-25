package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
)

type logEntry struct {
	Time          string `json:"time"`
	Level         string `json:"level"`
	Msg           string `json:"msg"`
	CorrelationID string `json:"correlation_id"`
	Component     string `json:"component"`
	WorkerID      int    `json:"worker_id,omitempty"`
	Async         bool   `json:"async,omitempty"`
}

func TestStructuredLoggerInvariants(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf, slog.LevelDebug)
	defer logger.ResetDefault()

	ctx := context.Background()
	testCorrID := domain.NewUUIDv7()
	ctx, _ = logger.WithCorrelationID(ctx, testCorrID)
	ctx = logger.WithComponent(ctx, "biz:test")

	logger.Info(ctx, "Structured test event", "user_id", "user_123")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	var entry logEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("failed unmarshaling log JSON: %v", err)
	}

	if entry.CorrelationID != testCorrID {
		t.Errorf("expected correlation_id %q, got %q", testCorrID, entry.CorrelationID)
	}
	if entry.Component != "biz:test" {
		t.Errorf("expected component 'biz:test', got %q", entry.Component)
	}
	if entry.Level != "INFO" {
		t.Errorf("expected level 'INFO', got %q", entry.Level)
	}
	if entry.Time == "" {
		t.Errorf("expected timestamp, got empty")
	}
}

func TestAsyncGoroutineCorrelationPropagation(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	syncWriter := &threadSafeWriter{buf: &buf, mu: &mu}

	logger.SetOutput(syncWriter, slog.LevelDebug)
	defer logger.ResetDefault()

	originCorrID := domain.NewUUIDv7()
	ctx, _ := logger.WithCorrelationID(context.Background(), originCorrID)
	ctx = logger.WithComponent(ctx, "biz:async_test")

	var wg sync.WaitGroup
	workers := 5
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			workerCtx := logger.WithComponent(ctx, "worker:async_pool")
			logger.Info(workerCtx, "Async worker started", "worker_id", workerID, "async", true)
		}(w)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != workers {
		t.Fatalf("expected %d log lines, got %d", workers, len(lines))
	}

	for _, line := range lines {
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("failed unmarshaling worker log JSON: %v", err)
		}
		if entry.CorrelationID != originCorrID {
			t.Errorf("worker log lost origin correlation ID: got %q, expected %q", entry.CorrelationID, originCorrID)
		}
		if entry.Component != "worker:async_pool" {
			t.Errorf("unexpected component: %q", entry.Component)
		}
		if !entry.Async {
			t.Errorf("expected async: true")
		}
	}
}

type threadSafeWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w *threadSafeWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// fakeTB simulates testing.TB to verify log buffering behavior on success and failure.
type fakeTB struct {
	testing.TB
	failed   bool
	logs     []string
	cleanups []func()
}

func (f *fakeTB) Helper()      {}
func (f *fakeTB) Failed() bool { return f.failed }
func (f *fakeTB) Log(args ...any) {
	var sb strings.Builder
	for i, arg := range args {
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(arg.(string))
	}
	f.logs = append(f.logs, sb.String())
}
func (f *fakeTB) Cleanup(fn func()) {
	f.cleanups = append(f.cleanups, fn)
}
func (f *fakeTB) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

func TestBindTestLoggerSuccessMuted(t *testing.T) {
	fake := &fakeTB{failed: false}
	logger.BindTestLogger(fake)

	ctx := context.Background()
	logger.Info(ctx, "This log should be silenced because test passed")

	fake.runCleanups()

	if len(fake.logs) != 0 {
		t.Fatalf("expected 0 logs emitted for passing test, got %d: %v", len(fake.logs), fake.logs)
	}
}

func TestBindTestLoggerFailureFlushed(t *testing.T) {
	fake := &fakeTB{failed: true} // Simulates test failure
	logger.BindTestLogger(fake)

	ctx := context.Background()
	testMsg := "Critical error during failed test run"
	logger.Error(ctx, testMsg)

	fake.runCleanups()

	if len(fake.logs) != 1 {
		t.Fatalf("expected 1 log line flushed on test failure, got %d", len(fake.logs))
	}
	if !strings.Contains(fake.logs[0], testMsg) {
		t.Errorf("flushed log missing expected message: %s", fake.logs[0])
	}
}

func TestRecoverGoroutine(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	syncWriter := &threadSafeWriter{buf: &buf, mu: &mu}

	logger.SetOutput(syncWriter, slog.LevelDebug)
	defer logger.ResetDefault()

	ctx, _ := logger.WithCorrelationID(context.Background(), "")
	ctx = logger.WithComponent(ctx, "biz:panic_test")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer logger.RecoverGoroutine(ctx, "test_panic_worker")
		panic("simulated background worker crash")
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	output := buf.String()
	if !strings.Contains(output, "Recovered from panic in background goroutine") {
		t.Errorf("expected panic recovery log message, got: %s", output)
	}
	if !strings.Contains(output, "simulated background worker crash") {
		t.Errorf("expected panic reason in log output, got: %s", output)
	}
}
