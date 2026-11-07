# OpenAPI Specifications & REST API Bindings (`internal/api`)

## Overview
OpenAPI 3.1 HTTP specification generator, Swagger UI / Scalar documentation handlers, and generated REST API v1 server route bindings.

## Interface
- `func GetRawSpecYAML() ([]byte, error)` [docs.go:64]
- `func GetSpecJSON() ([]byte, error)` [docs.go:77]
- `func ServeAPIDocs(c echo.Context) error` [docs.go:82]
- `func ServeOpenAPIYAML(c echo.Context) error` [docs.go:87]
- `func ServeOpenAPIJSON(c echo.Context) error` [docs.go:96]
- `func RegisterDocsRoutes(e *echo.Echo)` [docs.go:105]
- `type ServerInterface` [openapi.gen.go:120]
- `func RegisterHandlers(router echo.Echo, si ServerInterface)` [openapi.gen.go:210]

## Invariants
- API specification accurately models REST API v1 routes, parameters, and schemas [docs.go:25-50]
- Documentation endpoints serve immutable cached OpenAPI JSON/YAML specifications [docs.go:60-100]

## Side Effects
- Mounts HTTP route handlers on Echo server router at /api/docs and /api/v1 [docs.go:105-115, server.go:35]

## Verification
- Run: `go test ./...`
