# Application Configuration (`internal/config`)

## Overview
Centralized application environment configuration resolver providing public base URL parsing, sanitization, port fallback resolution, and absolute URL generation.

## Interface
- `func GetPublicBaseURL(fallbackPort string) string` [config.go:14]
- `func BuildPublicURL(fallbackPort string, subpath string) string` [config.go:56]

## Invariants
- Returned URLs are guaranteed to have no trailing slash [config.go:13-14]
- Defaults scheme to `http://` for `localhost` and `127.0.0.1`, and `https://` otherwise when protocol scheme is missing [config.go:30-37]
- Falls back to `APP_PUBLIC_URL` -> `PUBLIC_BASE_URL` -> `http://localhost:<PORT>` -> `http://localhost:8080` [config.go:14-28]

## Side Effects
- Reads OS environment variables (`APP_PUBLIC_URL`, `PUBLIC_BASE_URL`, `PORT`) [config.go:15-23]

## Verification
- Run: `go test ./...`
