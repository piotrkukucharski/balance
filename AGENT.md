# Balance — Agent Instructions & Repository Rules (`AGENT.md`)

This document defines the mandatory workflows, architectural invariants, coding standards, and tooling rules for AI agents (`agy` / Antigravity) working in the **Balance** repository.

Always consult [`docs/architecture_and_domain_spec.md`](docs/architecture_and_domain_spec.md) as the authoritative source of truth for domain logic, database schemas, and security constraints.

---

## 1. Mandatory Development Workflow (BDD Gherkin-First)

Every new feature, change request, or task **MUST** strictly follow a two-step **Behavior-Driven Development (BDD)** workflow:

### Step 1: Gherkin Specification & Approval (Before Writing Code)
1. Write or update the relevant Gherkin scenario(s) in `features/*.feature` covering the happy path and key domain invariants for the task.
2. Present the Gherkin scenario(s) to the user and **wait for explicit approval** before implementing application code.
3. Do **not** skip straight to implementation when a new functional task is requested.

### Step 2: Implementation, Verification & Staged Commits
1. Once the user approves the Gherkin specification, implement:
   - Domain/application code in `internal/` and/or `cmd/`.
   - Corresponding Godog step definitions and assertions in `internal/bdd/bdd_test.go`.
   - Unit/integration tests where applicable.
2. Run verification via `task test:bdd` and `task test:all`.
3. **Commit after every completed stage** (`git commit`) with a clear, descriptive English commit message.

---

## 2. Language & Naming Policy

- **Codebase & Web UI (100% English)**: All Go source code, package names, identifiers, comments, SQL migrations, table/column names, API routes, Gherkin `.feature` scenarios, commit messages, and **all Web Application UI labels/views/buttons/templates must be written strictly in English**.
- **User Communication**: Respond to the user in Polish (or the language they communicate in).

---

## 3. UI & Frontend Standards (`daisyui` Skill Mandatory)

Whenever creating or modifying HTML views or JSX/templates (`internal/web/handlers/*.go`):

1. **Mandatory daisyUI Skill First**: You **MUST ALWAYS** consult and read the daisyUI skill instructions in [`.agents/skills/daisyui/SKILL.md`](.agents/skills/daisyui/SKILL.md) **BEFORE** writing, modifying, or proposing any UI, HTML, or template code. Never write UI components from memory or guess daisyUI classes without consulting the skill first.
2. **DaisyUI v5 + Tailwind CSS v4 CDN**:
   - Use exclusively the official DaisyUI v5 + Tailwind v4 browser CDN tags (never use the broken `daisyui@5.0.0/dist/full.min.css` URL):
     ```html
     <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
     <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
     <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
     ```
3. **Semantic DaisyUI v5 Components & Colors**:
   - Use DaisyUI v5 semantic colors (`bg-base-100`, `bg-base-200`, `bg-base-300`, `text-base-content`, `primary`, `secondary`, `accent`, `success`, `warning`, `error`) rather than hardcoded Tailwind palette colors (`bg-gray-*`, `text-slate-*`).
   - Follow DaisyUI v5 form patterns (`fieldset`, `fieldset-legend`, `input w-full`, `select w-full`, `btn`, `card`, `stats`, `table`, `badge`, `drawer`, `alert`).
4. **Online-Only PWA & HTMX**:
   - Server-rendered HTML with HTMX (`hx-get`, `hx-post`, `hx-target`, `hx-swap`).
   - Never cache `/api/*`, `/budgets/*`, or HTML views containing financial data in `sw.js`.

---

## 4. Core Architectural & Domain Invariants

All changes must preserve these non-negotiable invariants from [`docs/architecture_and_domain_spec.md`](docs/architecture_and_domain_spec.md):

1. **Database-per-Budget Multi-Tenancy (SQLite)**:
   - `main.db`: Users, sessions, PATs, budgets registry, memberships, invitations, OAuth states, system audit logs (`migrations/main/`).
   - `budget_{budget_id}.db`: Isolated per-budget financial ledger (`migrations/tenant/`).
   - **Mandatory SQLite PRAGMAs on every connection**:
     - `PRAGMA journal_mode = WAL;`
     - `PRAGMA synchronous = NORMAL;`
     - `PRAGMA busy_timeout = 5000;`
     - `PRAGMA foreign_keys = true;`
     - `PRAGMA temp_store = MEMORY;`
     - `PRAGMA cache_size = -16000;`
   - **Path Traversal Protection**: Validate `budget_id` against strict UUIDv7 regex and verify `filepath.Clean` stays inside the tenant data directory before opening any tenant DB file.
2. **Monetary Precision (`int64` Minor Units + 128-Bit FX Math)**:
   - Never use `float32` or `float64` for money storage or ledger calculations.
   - Store all monetary amounts as signed 64-bit integers (`int64`) in minor units (e.g., `1050` = `10.50 PLN`), bounded by `MaxSafeAmountMinor = 100_000_000_000_000` ($10^{14}$).
   - Store FX rates as scaled `int64` integers (`FXRateScale = 100_000_000`, $10^8$).
   - Perform currency conversions using 128-bit intermediate integer arithmetic (`math/big` or `bits.Mul64`/`bits.Div64`) with deterministic **Round Half Up**.
3. **Primary Keys (UUIDv7)**:
   - Every primary key (`id`) across both `main.db` and `budget_{id}.db` must be a **UUIDv7** (`019...`) generated in Go (`domain.NewUUIDv7()`), never `AUTOINCREMENT` or random UUIDv4.
4. **CQRS & Event Projections**:
   - Domain mutations append immutable events to `*_events` tables (`account_events`, `bucket_events`, `transaction_events`) and atomically update materialized `*_current` tables (`accounts_current`, `buckets_current`, `transactions_current`, `bucket_Search_index_current`, `ready_to_assign_current`) within the same SQLite transaction.
   - Zero-sum double-entry invariant: `SUM(Budgeted in buckets) + ReadyToAssign == Total Cash in Budget Accounts`.
5. **6-Pillar Sandboxed Lua Formula Engine (`internal/lua`)**:
   - Execute user/AI formulas only inside an ephemeral `gopher-lua` state (`SkipOpenLibs: true`) with:
     1. Static AST/token ban on `while`, `repeat`, `for`, `goto`, `function`, `..` (string concat), and `\x1bLua` bytecode headers.
     2. Whitelisted libraries only (`base` subset, `math`, `table`) with metatables locked.
     3. 50 ms `context.WithTimeout`.
     4. Hard VM call stack and registry caps (`CallStackSize = 64`, `RegistrySize = 512`, `RegistryGrowStep = 0`).
     5. Host function call counter capped at `MaxHostCalls = 50`.
     6. Read-only domain bridge (`money`, `target`, `bucket.balance`, `fx.rate`, `fx.convert`, `budget.income`, `budget.spent`, `window.days`).
6. **Security & Authentication (`internal/auth`)**:
   - Passwords hashed with **Argon2id** (`64 MiB` memory, `3` iterations, `2` lanes, `16B` salt, `32B` key).
   - Sessions and PATs (`bal_pat_...`) use 256-bit CSPRNG tokens stored only as **SHA-256 hashes** in `main.db`.
   - Enforce RBAC (`OWNER`, `EDITOR`, `VIEWER`) and CSRF validation on all state-changing cookie-authenticated requests.

---

## 5. Project Structure

```text
.
├── AGENT.md / AGENTS.md                 # Agent instructions & repository rules
├── Taskfile.yml                         # Task runner commands (dev:* and test:*)
├── cmd/server/main.go                   # Echo HTTP server entrypoint & dependency wiring
├── docs/architecture_and_domain_spec.md # Master architecture & domain specification
├── features/                            # Gherkin BDD feature specifications (01..10)
├── migrations/
│   ├── main/00001_init_main.sql         # Control-plane SQLite schema
│   └── tenant/00001_init_tenant.sql     # Per-budget tenant SQLite schema
└── internal/
    ├── ai/                              # FormulaAIProvider (Gemini HTTP & Mock) + 1-retry self-correction
    ├── auth/                            # Argon2id, CSPRNG tokens, SHA-256 hashing, PATs
    ├── bdd/                             # Godog BDD test runner & step definitions
    ├── budget/                          # Rolling windows, 5 non-spending strategies, waterfall auto-assign
    ├── db/                              # SQLite PRAGMA opener, embedded migrations, LRU TenantDBManager
    ├── domain/                          # UUIDv7, minor units, 128-bit FX conversion, domain primitives
    ├── integration/mock/                # Deterministic MockAdapter for bank sync & import
    ├── lua/                             # 6-Pillar Sandboxed Lua VM formula engine
    ├── report/                          # Age of Money FIFO queue & bounded parallel Monte Carlo simulator
    ├── rule/                            # RE2 linear-time regex auto-matching rules engine
    ├── testutil/heavyfixture/           # 10-year multi-currency heavy dataset generator
    ├── transaction/                     # SHA-256 deduplication & atomic Move/Merge/Split engine
    └── web/handlers/                    # Echo middlewares, PWA handlers, and DaisyUI v5 views
```

---

## 6. Taskfile Commands (`task`)

Always use `task` (defined in [`Taskfile.yml`](Taskfile.yml)) to run development workflows and test suites:

### Development (`dev:*`)
- `task dev:init` — Initialize local directories (`./data/budgets`), `.env`, install agent skills via `npx skills experimental_install`, and download/tidy Go dependencies.
- `task dev:up` — Start the Balance HTTP server in development mode (`http://localhost:8080`).
- `task dev:stop` — Stop any running local Balance development server process.
- `task dev:build` — Compile the production binary to `./bin/balance`.
- `task dev:worktree:create -- <name>` — Create an isolated git worktree in `../development/<name>` with a dedicated port (10000..11000), copy of `.env`, `.agents/`, and copy of data.
- `task dev:worktree:clear -- <name> [--delete-branch]` — Remove worktree (preserves branch by default, pass `--delete-branch` to delete branch).
- `task dev:worktree:update:maintree -- <name>` — Fast-forward maintree branch from specified worktree branch (only runs on maintree).
- `task dev:worktree:update:branch` — Rebase current worktree branch onto main (only runs inside secondary worktree).

---

## 7. Skills & Tooling Management (`skills-lock.json`)

- **`skills-lock.json` is tracked in git**: Defines exact versions/hashes of agent skills (such as `daisyui`).
- **Restoring skills**: Automatically executed via `task dev:init` using `npx skills experimental_install`.
- **Worktree propagation**: `task dev:worktree:create` automatically replicates `.agents/` and `skills-lock.json` into secondary worktrees so agent workflows remain functional across all parallel environments.

---

## 8. Environment & Secrets Configuration (`.env`)

- **`.env` is git-ignored**: Local secrets must never be committed. Always update `.env.example` when adding new environment variables.
- **Taskfile dotenv integration**: `Taskfile.yml` includes `dotenv: ['.env']` so all environment variables are automatically passed to running processes (`dev:up`, `dev:build`, `test:*`).
- **Key Environment Variables**:
  - `PORT`: HTTP server port (default `8080` for maintree, isolated `10000..11000` in worktrees).
  - `DATA_DIR`: Base directory for SQLite databases (`./data`).
  - `GEMINI_API_KEY` & `GEMINI_MODEL`: Google Gemini API configuration for AI formula assistance (default model: `gemini-3.8-flash`).
  - `GOCARDLESS_SECRET_ID`, `GOCARDLESS_SECRET_KEY`, `GOCARDLESS_ENVIRONMENT`: Open Banking integration credentials (default environment: `sandbox`).

### Testing (`test:*`)
- `task test:bdd` — Run all Gherkin BDD scenarios (`features/*.feature`) via Godog (`internal/bdd`).
- `task test:all` — Run all unit, integration, and BDD tests with the `-race` detector enabled.
- `task test:unit` — Run fast domain, auth, Lua sandbox, budget, rule, and report unit tests.
- `task test:db` — Run SQLite connection, PRAGMA, and multi-tenant LRU pool tests.
- `task test:transaction` — Run transaction deduplication and atomic Move/Merge/Split tests.
- `task test:ai` — Run AI Formula Assistant and 1-retry self-correction loop tests.
- `task test:web` — Run HTTP handler, middleware, RBAC, CSRF, and PWA tests.
- `task test:heavy` — Run the 10-year heavy dataset fixture test (`~36,500` transactions).
- `task test:bench` — Run Go performance benchmarks (`-bench=. -benchmem`).
