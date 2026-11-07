# Web HTML Templates & Static Assets (`internal/web/templates`)

## Overview
HTML template registry and static asset distribution engine using embedded files, SHA-256 content hashing, and localized formatters.

## Interface
- `var FS embed.FS` (templates.go:22)
- `func StaticPath(logicalPath string) string` (templates.go:227)
- `func ReadStaticFile(logicalPath string) ([]byte, error)` (templates.go:236)
- `func Render(w io.Writer, name string, data any) error` (templates.go:241)
- `func RenderString(name string, data any) (string, error)` (templates.go:255)
- `func RegisterStaticRoutes(e *echo.Echo)` (templates.go:266)
- `func ServeStaticAsset(c echo.Context) error` (templates.go:270)

## Invariants
- Module templates clone the shared base template so that block definitions and overrides do not collide across different files (templates.go:172-222)
- Static asset URLs contain a 12-character hex SHA-256 fingerprint allowing immutable caching headers (templates.go:58-66, 309-313)

## Side Effects
- Registers GET /static/* on the Echo router (templates.go:266)
- Emits HTTP responses with ETag and Cache-Control headers from embedded assets (templates.go:302-315)

## Verification
- Run: `go test ./...`
