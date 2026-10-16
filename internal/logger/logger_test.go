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
