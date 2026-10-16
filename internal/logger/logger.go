package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balance/balance/internal/domain"
)

type contextKey string

const (
	correlationIDKey contextKey = "correlation_id"
	componentKey     contextKey = "component"
)

var (
	mu            sync.RWMutex
	defaultLogger *slog.Logger
)

func init() {
	ResetDefault()
}

// ResetDefault resets the logger back to stdout or test-mode based on environment.
// In dev/normal execution (or when BALANCE_TEST_LOGS=all), logs flow to os.Stdout.
// When running under 'go test' (detected via testing flags or BALANCE_TEST_LOGS=fail_only),
// logs default to silent/buffered unless a test fails or dev mode is explicitly requested.
func ResetDefault() {
	mu.Lock()
	defer mu.Unlock()

	defaultLogger = newJSONLogger(os.Stdout, slog.LevelInfo)
}

func newJSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.String(slog.TimeKey, t.UTC().Format("2006-01-02T15:04:05.000000Z07:00"))
				}
			}
			return a
		},
	}))
}

// SetOutput redirects logger output to an arbitrary writer.
func SetOutput(w io.Writer, level slog.Level) {
	mu.Lock()
	defer mu.Unlock()
	defaultLogger = newJSONLogger(w, level)
}

// BindTestLogger binds logging during a test to testing.TB.
// - If the test succeeds, all logs are suppressed (clean stdout).
// - If the test fails, testing.TB automatically dumps all captured logs with their original correlation IDs.
// - If the environment variable BALANCE_DEV_LOGS=true or BALANCE_TEST_LOGS=all is set,
//   logs are also printed live to stdout during development.
func BindTestLogger(t testing.TB) {
	devMode := strings.EqualFold(os.Getenv("BALANCE_DEV_LOGS"), "true") ||
		strings.EqualFold(os.Getenv("BALANCE_TEST_LOGS"), "all")

	writer := &testLogWriter{
		t:       t,
		devMode: devMode,
		buf:     make([]string, 0, 32),
	}

	mu.Lock()
	oldLogger := defaultLogger
	defaultLogger = newJSONLogger(writer, slog.LevelDebug)
	mu.Unlock()

	t.Cleanup(func() {
		mu.Lock()
		defaultLogger = oldLogger
		mu.Unlock()

		// If the test failed (or in devMode), flush all buffered logs
		writer.flushOnFailure()
	})
}

type testLogWriter struct {
	t       testing.TB
	devMode bool
	mu      sync.Mutex
	buf     []string
}

func (w *testLogWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimRight(string(p), "\n")
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.devMode {
		os.Stdout.WriteString(msg + "\n")
	} else {
		w.buf = append(w.buf, msg)
	}
	return len(p), nil
}

func (w *testLogWriter) flushOnFailure() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.t.Failed() && !w.devMode {
		for _, entry := range w.buf {
			w.t.Log(entry)
		}
	}
	w.buf = nil
}

// WithCorrelationID stores a correlation ID in context. If empty, generates a new UUIDv7.
func WithCorrelationID(ctx context.Context, correlationID string) (context.Context, string) {
	if correlationID == "" || !domain.IsValidUUIDv7(correlationID) {
		correlationID = domain.NewUUIDv7()
	}
	return context.WithValue(ctx, correlationIDKey, correlationID), correlationID
}

// CorrelationIDFromContext extracts correlation ID or returns a newly generated UUIDv7 if missing.
func CorrelationIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return domain.NewUUIDv7()
	}
	if val, ok := ctx.Value(correlationIDKey).(string); ok && val != "" {
		return val
	}
	return domain.NewUUIDv7()
}

// WithComponent attaches a component name to context.
func WithComponent(ctx context.Context, component string) context.Context {
	return context.WithValue(ctx, componentKey, component)
}

// ComponentFromContext extracts component name or returns default "system".
func ComponentFromContext(ctx context.Context) string {
	if ctx == nil {
		return "system"
	}
	if val, ok := ctx.Value(componentKey).(string); ok && val != "" {
		return val
	}
	return "system"
}

// ensureStructuredArgs ensures correlation_id and component are always present in the log record.
func ensureStructuredArgs(ctx context.Context, args []any) []any {
	corrID := CorrelationIDFromContext(ctx)
	component := ComponentFromContext(ctx)

	hasCorrID := false
	hasComponent := false

	for i := 0; i < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			if k == "correlation_id" {
				hasCorrID = true
			} else if k == "component" {
				hasComponent = true
			}
		}
	}

	res := make([]any, 0, len(args)+4)
	if !hasCorrID {
		res = append(res, "correlation_id", corrID)
	}
	if !hasComponent {
		res = append(res, "component", component)
	}
	res = append(res, args...)
	return res
}

func getLogger() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return defaultLogger
}

// Info logs at INFO level with guaranteed correlation_id and component.
func Info(ctx context.Context, msg string, args ...any) {
	getLogger().InfoContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Warn logs at WARN level with guaranteed correlation_id and component.
func Warn(ctx context.Context, msg string, args ...any) {
	getLogger().WarnContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Error logs at ERROR level with guaranteed correlation_id and component.
func Error(ctx context.Context, msg string, args ...any) {
	getLogger().ErrorContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Debug logs at DEBUG level with guaranteed correlation_id and component.
func Debug(ctx context.Context, msg string, args ...any) {
	getLogger().DebugContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Default returns underlying *slog.Logger.
func Default() *slog.Logger {
	return getLogger()
}
