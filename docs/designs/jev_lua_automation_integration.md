# Technical Design: Jev Integration in Sandboxed Lua Automations

* **Author:** Piotr Kucharski
* **Date:** 2026-11-07
* **Status:** Finalized (Remediated Post-Review)

## 1. The Problem & Business Context

Personal finance management in Balance relies on automation rules to sanitize payee names, categorize transactions, split expenses, and enforce envelope budgeting invariants. Rules are divided into two distinct execution phases:
- **Judge Script**: A boolean condition script returning `true` or `false` to decide whether a transaction matches the rule.
- **Executor Script**: A mutation script executed when the Judge passes, applying updates (e.g. assigning a bucket, reassigning counterparties, or executing splits via `tx.split`).

Currently, automation matching in Judge scripts relies on rigid deterministic conditions (equality, prefixes, or linear RE2 regex patterns). When users encounter polymorphic transaction strings (e.g., `"UBER *TRIP 1234 HELP.UBER.COM"`, `"UBER EATS AMSTERDAM"`, or ambiguously labeled bank transfers), traditional regex rules require constant manual maintenance and brittle combinatorial rule trees.

Standard generative LLMs (like OpenAI or Gemini) are too slow (1,000–4,000ms), costly, non-deterministic, and prone to hallucinations or malformed JSON when evaluated in transaction pipelines. Balance requires a fast, calibrated, probabilistic classification engine directly callable from transaction automation scripts, without sacrificing the security boundaries of the sandboxed Lua environment.

## 2. Overview & Terminology

This feature integrates the **TypeSafe Jev System One decision engine** into Balance's embedded Lua automation runtime (`internal/lua` and `internal/automation`).

### Terminology & Concepts
- **System One Model (Jev)**: Drawing an analogy to Daniel Kahneman's cognitive framework, a "System One" AI model is optimized for fast, reflex-like, structured classifications rather than conversational text generation. It accepts an input state and specific questions, returning schema-enforced, calibrated probability outputs within 70–500ms (see [TypeSafe AI Documentation](https://docs.typesafe.ai)).
- **`jev.choice` Primitive**: Selects the best match from an explicit list of candidates (e.g., categorizing an expense to one of several buckets).
- **`jev.noul` Primitive**: A binary/probabilistic assertion primitive (from "nous" / boolean truth assertion) evaluating a yes/no hypothesis and returning a calibrated probability score ($0.0 \dots 1.0$) alongside a confidence metric.

Instead of exposing an open HTTP client or raw network sockets (which would introduce severe SSRF and data exfiltration vectors), Balance exposes a dedicated, opinionated `jev.*` Lua module. All requests execute synchronously through a hardened Go HTTP client with an instance-wide API key, strict timeouts, quota protection (maximum 3 calls per script run), error sanitization (preventing secret leaks in error payloads), and universal loop bans.

## 3. Goals & Non-Goals

### Goals
- **Deterministic & Safe Decision Primitives**: Provide `jev.choice` and `jev.noul` in Lua returning `(result, confidence, err)` tuples with zero VM panics.
- **Strict Sandbox Integrity**: Keep GopherLua isolated from raw network access, `os.getenv`, and filesystem I/O.
- **Leverage Existing Lexer Loop Bans**: Rely on Balance's existing `ValidateLuaScript` lexer token ban (`ErrForbiddenToken`) which strictly forbids all loops (`for`, `while`, `repeat`, `goto`) and user functions (`function`), guaranteeing no Jev calls can ever execute in a loop.
- **Resource & Quota Protection**: Enforce a strict 1.0s timeout per call and a hard quota of max 3 calls per script run (enforced via package constant `MaxJevCalls = 3`).
- **Secret Sanitization**: Securely inject `JEV_API_KEY` in the Go runtime while redacting authorization headers, tokens, and sensitive query params from all user-facing errors.
- **Dependency Injection for Testing**: Expose a mockable `JevClient` interface in Go so engine tests can run deterministically without external network sockets.
- **BDD & Unit Test Coverage**: Implement Godog BDD scenarios verifying correct decisions, token ban enforcement, quota exhaustion, and secret redaction.

### Non-Goals
- **General-Purpose HTTP in Lua**: No `lua-http` or raw socket access for user scripts (reserved for future evaluation under task [BALANCE-109](https://todo.ptr.ovh/tasks/187)).
- **Dynamic AST Loop Parsers**: No redundant AST walker since all loops are already banned at the token scanner level.
- **Speculative Configuration Flags**: No `BALANCE_LUA_JEV_MAX_CALLS` environment variable; max calls is a constant (`MaxJevCalls = 3`).
- **Per-Tenant Jev API Keys**: The API key is managed at the server/instance configuration level.
- **Generative Text / Free-form Chat**: Jev is strictly a decision classifier; conversational LLM interactions remain handled by existing AI assistant services.
- **Asynchronous Lua Execution**: Script evaluation remains synchronous within transaction processing pipelines.

## 4. Technical Plan

### 4.1 System Architecture & Data Flow

```mermaid
flowchart TD
    subgraph AutomationPipeline ["Automation Execution (internal/automation)"]
        Tx[Transaction Event / Batch Import] --> PreCheck[ValidateLuaScript Lexer Check]
        PreCheck -- "Token Error (Loop/Function/Concat)" --> RejectTx[Reject Script at Save/Compile Time]
        PreCheck -- "Valid Linear Script" --> VMInit[Initialize GopherLua VM]
    end

    subgraph LuaSandbox ["Hardened Lua VM (internal/lua)"]
        VMInit --> ExecJudge[Execute Judge or Executor Script]
        ExecJudge --> LuaJevCall["jev.choice(...) / jev.noul(...)"]
    end

    subgraph HostBridge ["Go Host Module Bridge (internal/lua)"]
        LuaJevCall --> CallQuotaCheck{Calls <= MaxJevCalls (3)?}
        CallQuotaCheck -- "> 3 Calls" --> QuotaErr[Return ErrJevCallLimitExceeded]
        CallQuotaCheck -- "Allowed" --> SanitizeArgs[Validate & Serialize JSON]
        SanitizeArgs --> GoClient[JevClient Interface]
    end

    subgraph ExternalService ["TypeSafe AI Network Service"]
        GoClient --> HTTPReq["POST /v1/systemone (Per-Call Timeout: 1.0s)"]
        HTTPReq --> JevAPI[Jev Inference Endpoint]
        JevAPI --> HTTPResp[HTTP 200 / 4xx / 5xx]
    end

    HTTPResp --> RespSanitizer[Secret & Error Sanitizer]
    RespSanitizer --> LuaReturn[Push Result Tuple to Lua]
    LuaReturn --> ExecJudge
```

### 4.2 Component Design & Responsibilities

1. **Jev HTTP Client & Interface (`internal/jev/client.go`)**:
   - Defines the core interface:
     ```go
     type JevClient interface {
         Choice(ctx context.Context, question string, candidates []string, contextData map[string]any) (string, float64, error)
         Noul(ctx context.Context, question string, contextData map[string]any) (float64, float64, error)
     }
     ```
   - Standard implementation `HTTPClient`:
     - Manages communication with `https://gateway-us.pydantic.dev/proxy/typesafe/v1/systemone` (or configured endpoint).
     - Resolves instance-wide API key from environment `JEV_API_KEY`.
     - Uses dedicated `http.Client` with connection pooling (`MaxIdleConns: 50`, `IdleConnTimeout: 90s`, `KeepAlive: true`) and per-request timeout of 1.0s.
     - Implements error classification: maps raw HTTP/network errors into structured domain errors (`ErrUnauthorized`, `ErrRateLimited`, `ErrJevTimeout`, `ErrJevServiceDown`), explicitly scrubbing authorization tokens, headers, and secret URL components.

2. **Loop Prevention via Existing Lexer (`internal/lua/engine.go`)**:
   - `ValidateLuaScript` already inspects tokens and rejects `TWhile`, `TRepeat`, `TFor`, `TGoto`, and `TFunction`.
   - Because scripts are strictly linear expressions without loops, functions, or string concatenation, no nested loops can exist. Scripts violating this policy are rejected at compile/save time.

3. **Lua Module Binding & Quota Enforcement (`internal/lua/jev_lib.go`)**:
   - Registered into GopherLua (`glua.LState`) during automation evaluation when a `JevClient` is configured.
   - Tracks invocation count using a simple local integer `jevCallCount` in the execution context:
     ```go
     const MaxJevCalls = 3
     ```
   - Calling more than 3 times immediately returns `nil, 0.0, "jev call quota exceeded (max 3 calls)"`.
   - Tuple Return Signatures in Lua:
     - On success: `(result, confidence, nil)` (where `result` is string for `choice`, or float 0.0..1.0 for `noul`).
     - On error: `(nil, 0.0, "sanitized error message")`.

4. **Execution & Validation Timeout Differentiation**:
   - Callers control execution timeouts by passing an appropriate `context.Context`:
     - **Dry-run / Script Validation (UI Save Handler)**: The web handler passes `context.WithTimeout(ctx, 1000*time.Millisecond)` to guarantee snappy sub-second UI responsiveness.
     - **Full Automation Pipeline Execution (Transaction Worker)**: The automation worker passes `context.WithTimeout(ctx, 3500*time.Millisecond)` (providing adequate margin for up to 3 sequential 1.0s HTTP rounds plus network overhead).

## 5. Alternatives Considered

1. **Exposing Generic `lua-http` / Raw Socket Client to Lua Scripts**:
   - *Description*: Provide an embedded HTTP client library (such as `lua-http` or Go socket bindings) directly inside the Lua environment.
   - *Status*: Rejected by author.
   - *Technical Rationale*: Violates the foundational security sandbox of Balance. A generic HTTP client in user-authored scripts introduces critical Server-Side Request Forgery (SSRF) vulnerabilities against internal network services, databases, and cloud metadata endpoints. Furthermore, it exposes the system to data exfiltration risks and makes API key credential isolation impossible without complex egress filtering. (Tracked separately under [BALANCE-109](https://todo.ptr.ovh/tasks/187)).

2. **Custom AST Loop Parser (`internal/lua/validator.go`)**:
   - *Description*: Implement a custom AST walker traversing statement blocks to find `jev.*` calls inside loops.
   - *Status*: Rejected post-review (Zoom-Out Blocker).
   - *Technical Rationale*: `ValidateLuaScript` in `internal/lua/engine.go` already bans `TWhile`, `TRepeat`, `TFor`, and `TGoto` for all scripts. Adding an AST parser introduces redundant code and maintenance overhead for an invariant already strictly enforced at the tokenizer level.

3. **Configurable Environment Variable for Call Quota (`BALANCE_LUA_JEV_MAX_CALLS`)**:
   - *Description*: Provide an environment variable to adjust the maximum number of Jev calls per script.
   - *Status*: Rejected post-review (YAGNI).
   - *Technical Rationale*: Scripts are linear, limited to 50 lines, and strictly bounded by a 3.5s execution deadline. A fixed constant of 3 calls per script provides predictable SLA guarantees during banking transaction ingestion without speculative configuration knobs.

4. **Generative LLM Chat Completion (OpenAI / Gemini Prompting)**:
   - *Description*: Invoke generative chat completions from within the Lua script to decide transaction categories.
   - *Status*: Rejected by author.
   - *Technical Rationale*: Generative chat models exhibit excessive latency (1,000–4,000ms+), which severely blocks transaction ingestion pipelines. They are non-deterministic, cost significantly more per invocation, and suffer from schema drift or invalid JSON markdown wrapping.

## 6. Detailed Implementation Plan

The implementation modifies and adds files across configuration, Jev client communication, Lua runtime bindings, AI prompt generation, and test suites.

### File Modifications & Creations

1. **`internal/config/config.go`** (Modified)
   - *Rationale*: Add helper functions `GetJevAPIKey() string` and `GetJevEndpoint() string`. Centralizes environment variable resolution and defaults.

2. **`internal/jev/client.go`** (New File)
   - *Rationale*: Core HTTP client and `JevClient` interface implementation communicating with TypeSafe Jev System One endpoint (`POST /v1/systemone` or gateway). Manages keep-alive connection pooling, Bearer token injection, request serialization for `choice` and `noul`, response deserialization, 1.0s per-request context timeouts, and secret/header sanitization on error objects.

3. **`internal/jev/client_test.go`** (New File)
   - *Rationale*: Unit tests using `httptest.Server` verifying correct payload formats, 1.0s timeout cancellations, handling of HTTP 400/401/429/500 responses, and asserting that the `Authorization` header and secret tokens never appear in formatted error strings.

4. **`internal/lua/jev_lib.go`** (New File)
   - *Rationale*: Register the `jev` table into GopherLua (`glua.LState`). Implement Lua bridge wrappers for `jev.choice` and `jev.noul`, convert Lua tables to Go structs, enforce execution quota (`MaxJevCalls = 3`), execute calls via `JevClient`, and return `(result, confidence, err)` tuples to Lua.

5. **`internal/lua/engine.go`** (Modified)
   - *Rationale*: Update `EvaluateAutomationJudge` and `EvaluateAutomationExecutor` to accept an optional `JevClient` (or options struct). When present, register `jev_lib` into the VM.

6. **`internal/lua/engine_test.go`** (Modified)
   - *Rationale*: Unit tests for `EvaluateAutomationJudge` and `EvaluateAutomationExecutor` with a mock `JevClient`, verifying successful evaluations, handling of upstream errors, and rejection when quota exceeds 3 calls.

7. **`internal/ai/automation.go`** (Modified)
   - *Rationale*: Update system prompt instructions for the AI rule generator (`sysPrompt`): document `jev.choice` and `jev.noul` availability, explain their signatures and parameters, and explicitly instruct the AI that loops are banned and to limit total calls to a maximum of 3.

8. **`features/lua_jev.feature`** (New File)
   - *Rationale*: Godog BDD test specifications covering:
     - Evaluating `jev.choice` for payee categorization.
     - Evaluating `jev.noul` for recurring transaction checks.
     - Verifying loop syntax rejection via existing tokenizer.
     - Bounding script execution to max 3 calls.
     - Verifying error message sanitization when upstream Jev returns HTTP 500.

9. **`test/bdd/lua_jev_steps_test.go`** (New File)
   - *Rationale*: Godog BDD step definitions implementing test scenarios defined in `features/lua_jev.feature`.

10. **`internal/lua/README.md`** and **`internal/automation/README.md`** (Modified)
    - *Rationale*: Update package documentation to record `jev.*` module interfaces, invariants (1s per-call timeout, max 3 calls, no loops), and security rules.
