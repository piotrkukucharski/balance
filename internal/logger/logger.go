package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/balance/balance/internal/domain"
)

type contextKey string

const (
	correlationIDKey contextKey = "correlation_id"
	componentKey     contextKey = "component"
)

// DefaultLogger wraps slog.Logger with correlation ID and structured metadata invariants.
var defaultLogger *slog.Logger

func init() {
	defaultLogger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				// Deterministic ISO8601 UTC microsecond formatting
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.String(slog.TimeKey, t.UTC().Format("2006-01-02T15:04:05.000000Z07:00"))
				}
			}
			return a
		},
	}))
}

// SetOutput redirects logger output (useful for testing or capturing logs).
func SetOutput(w io.Writer, level slog.Level) {
	defaultLogger = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
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

// ResetDefault resets the logger back to standard stdout JSON handler.
func ResetDefault() {
	SetOutput(os.Stdout, slog.LevelInfo)
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

// Info logs at INFO level with guaranteed correlation_id and component.
func Info(ctx context.Context, msg string, args ...any) {
	defaultLogger.InfoContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Warn logs at WARN level with guaranteed correlation_id and component.
func Warn(ctx context.Context, msg string, args ...any) {
	defaultLogger.WarnContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Error logs at ERROR level with guaranteed correlation_id and component.
func Error(ctx context.Context, msg string, args ...any) {
	defaultLogger.ErrorContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Debug logs at DEBUG level with guaranteed correlation_id and component.
func Debug(ctx context.Context, msg string, args ...any) {
	defaultLogger.DebugContext(ctx, msg, ensureStructuredArgs(ctx, args)...)
}

// Default returns underlying *slog.Logger.
func Default() *slog.Logger {
	return defaultLogger
}
