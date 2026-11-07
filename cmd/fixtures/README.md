# Fixtures CLI (`cmd/fixtures`)

## Overview
Standalone administrative and fixture provisioning CLI utility for Balance.

## Interface
- `fixtures create-user --login <email> --password <pwd> --user-id <uuidv7> --first-action-at <YYYY-MM-DD> [--display-name <name>] [--data-dir <path>] [--correlation-id <id>]`
- `fixtures create-bucket --bucket-id <uuidv7> --name <name> --owner-user-id <uuidv7> --timezone <tz> --first-action-at <YYYY-MM-DD> [--currency <PLN>] [--data-dir <path>] [--correlation-id <id>]`
- `fixtures load --bucket-id <uuidv7> --user-id <uuidv7> --first-action-at <YYYY-MM-DD> --last-transaction <YYYY-MM-DD> [--data-dir <path>] [--correlation-id <id>]`
- `fixtures remove-bucket --bucket-id <uuidv7> [--data-dir <path>] [--correlation-id <id>]`
- `fixtures remove-user --user-id <uuidv7> [--data-dir <path>] [--correlation-id <id>]`
- `fixtures all --login <email> --password <pwd> --user-id <uuidv7> --name <name> --bucket-id <uuidv7> --timezone <tz> --first-action-at <YYYY-MM-DD> --last-transaction <YYYY-MM-DD> [--clean] [--data-dir <path>] [--correlation-id <id>]`

## Invariants
- Data directory defaults to DATA_DIR environment variable or './data' if unspecified (main.go:101-105, 200-204)
- Pass-through and internal transactions maintain consistent UUIDv7 identifiers and UTC timestamps (main.go:83-97, 237-239, 308-310)
- Buffer logging (setupBufferingLogger) captures detailed execution logs and only flushes to stderr upon encountering terminal errors (main.go:27-37, 148, 173-176)
- When run in 'all' pipeline mode with --clean, any pre-existing user account and tenant SQLite databases are removed before re-provisioning (main.go:556-566)

## Side Effects
- Creates or modifies SQLite database main.db under DATA_DIR
- Creates, populates, or removes tenant databases under DATA_DIR/budgets/<bucket-id>.db
- Writes progress indicators and summaries to stdout and diagnostic error logs to stderr

## Verification
- Run: `go test ./...`
