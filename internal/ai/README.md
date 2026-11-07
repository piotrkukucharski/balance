# AI Assistance & Generation Services (`internal/ai`)

## Overview
AI integration services providing LLM assistance for formula generation, natural language automation translation, smart categorization rules, and budget structure scaffolding.

## Interface
- `type AutomationAIPairResult` [automation.go:19]
- `type VersionCommentRequest` [automation.go:26]
- `type AutomationVersionSummary` [automation.go:36]
- `type MultiVersionCommentRequest` [automation.go:44]
- `type TxContextExample` [automation.go:52]
- `type GenerateContext` [automation.go:63]
- `type PromptContext` [formula.go:27]
- `type FormulaAIClient` [formula.go:41]
- `type SmartRuleSuggestion` [smart_rules.go:17]
- `type StructureSuggestion` [structure.go:15]

## Invariants
- AI prompts enforce strict schema validation and token limits [formula.go:45-60]
- Fallbacks provide deterministic error handling when LLM APIs are unreachable [errors.go:12-25]

## Side Effects
- Outbound HTTPS network calls to configured LLM provider endpoints [formula.go:88]

## Verification
- Run: `go test ./...`
