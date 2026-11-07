# Profile View Templates (`internal/web/templates/profile`)

## Overview
Server-side HTML templates for user account profile settings, password changes, and Personal Access Tokens (PAT).

## Interface
- `profile.html`

## Invariants
- Protects password change actions with verification checks.

## Side Effects
- None. Static HTML template compilation.

## Verification
- Run: `go test ./...`
