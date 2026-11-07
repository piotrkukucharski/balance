# Embedded Lua Sandboxed Runtime (`internal/lua`)

## Overview
Embedded GopherLua sandboxed runtime for dynamic target calculations, user-defined automation scripts, and custom mathematical formulas.

## Interface
- `type TargetMeta struct` [engine.go:35]
- `type TargetFormulaContext struct` [engine.go:49]
- `type BucketMeta struct` [engine.go:62]
- `type AccountMeta struct` [engine.go:69]
- `type PartyMeta struct` [engine.go:78]
- `type AutomationTxContext struct` [engine.go:85]
- `type LuaEngine struct` [engine.go:95]
- `func NewLuaEngine() *LuaEngine` [engine.go:100]
- `func (e *LuaEngine) ExecuteFormula(ctx context.Context, script string, formulaCtx TargetFormulaContext) (int64, error)` [engine.go:120]

## Invariants
- Lua state executes in a restricted sandbox without OS, IO, or network access [engine.go:100-115]
- Script execution is strictly bounded by instruction step counters and execution timeouts [engine.go:125-140]

## Side Effects
- Allocates memory and executes CPU instructions within sandboxed GopherLua VM [engine.go:120-150]

## Verification
- Run: `go test ./...`
