# Accounts View Templates (`internal/web/templates/accounts`)

## Overview
Server-side HTML templates for financial account lists, balances, and account management views.

## Interface
- `accounts.html`

## Invariants
- Extends base workspace layout and renders account balances in formatted minor units.

## Side Effects
- None. Static HTML template compilation.

## Verification
- Run: `go test ./...`
