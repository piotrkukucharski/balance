# Developer Scripts (`scripts`)

## Overview
Developer and operational workflow automation scripts, including worktree isolation and environment orchestration.

## Interface
- `go run ./scripts/worktree.go create <task-id-or-name>` (worktree.go:349-479)
- `go run ./scripts/worktree.go clear <name> [--delete-branch] [--force]` (worktree.go:501-569)
- `go run ./scripts/worktree.go update-maintree <name>` (worktree.go:571-596)
- `go run ./scripts/worktree.go update-branch` (worktree.go:598-619)
- `go run ./scripts/worktree.go finish [--force]` (worktree.go:666-807)

## Invariants
- Allocates free ephemeral server port in the 10000-11000 range for new worktrees (worktree.go:99-118, 392)
- Automatically queries Vikunja task API (Project 29) to resolve task IDs (e.g. BALANCE-2) into structured branch/directory slugs (worktree.go:276-347)
- Safely backs up SQLite databases using sqlite3 .backup or file copy to prevent corruption across worktree data copies (worktree.go:160-209)
- Propagates .env, .agents, and skills-lock.json across worktrees (worktree.go:397-474)
- Verifies port utilization and server activity before allowing worktree removal or finish (worktree.go:532-540, 743-752)

## Side Effects
- Creates or deletes Git worktrees in ../development/<name> and branches
- Copies and backs up SQLite databases and environment configurations
- Checks TCP network ports and queries external Vikunja REST API

## Verification
- Run: `go test ./...`
