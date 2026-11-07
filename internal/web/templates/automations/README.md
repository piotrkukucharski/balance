# Automations View Templates (`internal/web/templates/automations`)

## Overview
Server-side HTML templates for automation rule lists, trigger builder modals, and execution history.

## Interface
- `automations.html`

## Invariants
- Embeds CSRF token into form submission actions.

## Side Effects
- None. Static HTML template compilation.

## Verification
- Run: `go test ./...`
