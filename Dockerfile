# Build stage
FROM golang:1.27-alpine AS builder

# Install build dependencies (CGO required for sqlite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the balance server binary with CGO enabled
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /app/bin/balance ./cmd/server

# Production runtime stage
FROM alpine:3.21

# Install runtime dependencies: ca-certificates for external HTTPS requests (Gemini, GoCardless, ECB), tzdata for timezone support
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Create non-root user and group
RUN addgroup -S balance && adduser -S balance -G balance

# Create data directory structure and assign ownership
RUN mkdir -p /app/data/budgets && chown -R balance:balance /app/data

# Copy compiled binary from builder
COPY --from=builder /app/bin/balance /app/bin/balance

# Expose default HTTP port
EXPOSE 8080

# Environment variables defaults
ENV PORT=8080 \
    DATA_DIR=/app/data

# Switch to non-root user
USER balance:balance

# Define volume mount point for persistent data (main.db and tenant SQLite databases)
VOLUME ["/app/data"]

ENTRYPOINT ["/app/bin/balance"]
