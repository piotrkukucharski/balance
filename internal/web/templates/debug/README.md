# Debugging View Templates (`internal/web/templates/debug`)

## Overview
Server-side HTML templates for developer introspection and diagnostic views.

## Interface
- `debug.html`

## Invariants
- Accessible only when authorized under administrative or debug modes.

## Side Effects
- None. Static HTML template compilation.

## Verification
- Run: `go test ./...`
