# Structured Logging Subsystem (`internal/logger`)

## Overview
Structured logging subsystem wrapping log/slog with correlation ID tracing, context propagation, and buffered error diagnostic handlers.

## Interface
- `func ResetDefault()` [logger.go:37]
- `func SetOutput(w io.Writer)` [logger.go:59]
- `func BindTestLogger(t testing.TB)` [logger.go:70]
- `func Write(p []byte) (n int, err error)` [logger.go:102]
- `func WithCorrelationID(ctx context.Context, id string) context.Context` [logger.go:128]
- `func CorrelationIDFromContext(ctx context.Context) string` [logger.go:136]

## Invariants
- Structured logger attaches correlation_id attribute to all log records when present in context [logger.go:120-145]
- Buffering logger captures debug/info output and flushes to stderr upon encountering errors [logger.go:80-110]

## Side Effects
- Writes formatted JSON or text logs to stdout, stderr, or configured io.Writer [logger.go:59-65, 102]

## Verification
- Run: `go test ./...`
