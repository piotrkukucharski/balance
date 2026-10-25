# Architecture & Domain Design Document: Balance

**Status:** Approved / Working Specification  
**Version:** 0.2.0  
**Backend Stack:** Golang (Echo framework), SQLite (Multi-tenant: `main.db` + Per-Budget `{budget_id}.db`), UUIDv7, CQRS & Event Sourcing / Immutable Append-Only Ledger Models.  
**Frontend & API Stack:** Server-Side Rendered HTML (`a-h/templ`), **HTMX (v4)**, **Tailwind CSS (v4.3)**, **DaisyUI (v5.7)**, Database-backed Browser Sessions (`main.db`), and **OpenAPI 3.1** (Schema-First via `oapi-codegen`).  
**Language Standards:** All code, comments, database schemas, tables, fields, and API endpoints are strictly in **English**.

---

## 1. Executive Summary & Vision

**Balance** is an envelope-style personal and multi-entity budgeting system inspired by YNAB, Actual Budget, and Firefly III, built for multi-currency tracking, strict account-to-account cashflow visibility, real-world reconciliation, flexible time horizons (not locked into calendar months), and extensible bank synchronization (GoCardless, CSV, Mock, and future adapters).

### Key Differentiators & Principles
1. **Multi-Currency at its Core (Buckets & Accounts)**:
   - Both `Bucket`s (envelopes) and `Account`s can hold balances in multiple currencies simultaneously without forced global conversion loss.
   - `Target`s are single-currency, while `BudgetTransaction`s support both same-currency and cross-currency transfers (`source_amount_minor` + `source_currency` $\rightarrow$ `destination_amount_minor` + `destination_currency`).
2. **Strict Account-to-Account Double-Entry Model (Firefly III Inspired)**:
   - Every `Account` belongs to a `Party` (an individual or organization, either user-owned or external counterparty like `"Żabka"` or `"Employer"`).
   - Every `BudgetTransaction` strictly links a `source_account_id` and a `destination_account_id`.
   - External merchants without a known IBAN (e.g., card payments at `"Żabka"`) use a virtual/external `Account` owned by the `"Żabka"` `Party`, enabling granular cashflow reporting per `Party` and/or per `Account` (with optional exclusion of internal user-owned transfers).
3. **Flexible Time Horizons (Rolling Windows)**:
   - Unlike conventional tools locked to calendar months (e.g. 1st–31st), Balance supports arbitrary rolling windows (e.g., next 30 days, 90 days, or custom spans) alongside classic monthly views.
4. **Two-Tier Transaction Architecture (Raw vs. Budget Transactions)**:
   - `raw_transactions` store external bank/CSV truth.
   - `budget_transactions` store budgetary truth (e.g., splitting a dinner bill where 1 raw expense of `-300 PLN` + 2 friend reimbursements of `+100 PLN` are merged into a single net expense of `100 PLN`).
   - Traceability is maintained via an N-to-M junction/audit table (`transaction_links` with `operation_id`, `raw_transaction_id`, `budget_transaction_id`).
5. **Multi-Tenant SQLite Architecture**:
   - `main.db`: Users, browser sessions, authentication, budget registry, and user-level bank integration credentials/tokens.
   - `budget_{id}.db`: Isolated SQLite database for each budget containing accounts, parties, matching rules, buckets, targets, transactions, and event streams.
6. **CQRS, Event Sourcing & Append-Only Versioning**:
   - `Bucket`s and `Target`s use Event Sourcing + synchronous inline CQRS projections.
   - `RawTransaction`, `BudgetTransaction`, `Account`, `Party`, and `MatchingRule` entities use immutable append-only versioning (`id`, `version`, `action`, `created_at`, `agent_id`, `correlation_id`).
   - Every HTTP/HTMX/API request is assigned a strictly **server-generated** `correlation_id` (UUIDv7) for database audit columns (`correlation_id`), preventing client-side audit trail spoofing or log injection (an optional client-supplied `X-Request-ID` header is validated as a canonical UUID and logged separately). All primary keys are UUIDv7 generated in application code (never null/nil).
7. **Timezones & Localization**:
   - Each budget specifies a canonical Timezone (configurable and modifiable).
   - All database timestamps are strictly stored in UTC+0 Unix microsecond format (`INTEGER`). Calculations relative to days/periods evaluate in the budget's configured timezone.
   - Full i18n support with user-configurable date and currency/number formats.
8. **Monetary Representation (Minor Currency Units & Overflow Protection)**:
   - All amounts in database and Go domain structs are stored strictly as 64-bit integers (`int64`) representing minor units (cents, grosze, etc., e.g. `100.50 PLN` = `10050`). Floats are strictly forbidden.
   - Every user/API input amount is strictly bounded by `MaxSafeAmountMinor = 100_000_000_000_00` ($10^{14}$ minor units, safely inside IEEE-754 exact integer range $2^{53}-1$ and immune to `int64` overflow during ledger summation).
   - All currency conversions (`amount_minor * rate_micros / 1_000_000`) use 128-bit intermediate integer arithmetic (`math/big` or `math/bits.Mul64` / `Div64`) to prevent 64-bit multiplication overflow before division.

---

## 2. Domain Terminology

| Domain Concept | English Domain Term | Definition & Rules |
|---|---|---|
| **Envelope** / Basket | **Bucket** | Spending/saving envelope capable of holding sub-balances in multiple currencies. Can be organized into `BucketGroup`s. |
| **Party** (Individuals & Organizations) | **Party** | An `INDIVIDUAL` or `ORGANIZATION`. Flagged with `is_user_owned` (`true` for the user/family members, `false` for external counterparties like `"Żabka"`, `"Netflix"`, `"Employer"`). |
| **Financial Account** | **Account** | Multi-currency financial account owned by a `Party`. Includes user accounts (`CHECKING`, `SAVINGS`, `CREDIT_CARD`, `CASH`, `INVESTMENT`) and counterparty accounts (`EXTERNAL`). |
| **Matching Rule** | **MatchingRule** | Rule matching incoming `RawTransaction` attributes (`counterparty_iban`, `raw_payee` pattern) to a counterparty `Account` / `Party` and default `Bucket`. |
| **Raw Transaction** | **RawTransaction** | Immutable raw incoming transactions from bank integrations or CSV imports. |
| **Budget Transaction** | **BudgetTransaction** | Budgetary ledger entries moving money from `source_account_id` to `destination_account_id`, optionally assigned to a `Bucket` and `Target`. |
| **Transaction Link** | **TransactionLink** | Tracks 1-to-1 (`DIRECT_MOVE`), N-to-1 (`MERGE`), 1-to-N (`SPLIT`), or N-to-M (`COMPOSITE`) links between `RawTransaction` and `BudgetTransaction` via `operation_id`. |
| **Bucket Goal / Target** | **Target** | Single-currency goal inside a `Bucket` (one-off or recurring, with configurable non-spending strategy). |
| **User / Actor** | **Agent** (`agent_id`) | Identifies who performed the mutation (authenticated user ID or background sync worker ID). |

---

## 3. UI, Frontend, UX Flows & Dual-Interface Architecture

### 3.1 Hypermedia UI Stack & Browser Session Hardening
- **Templates**: Type-safe Go HTML components using **`a-h/templ`** (all user/bank/AI strings strictly HTML-escaped by default; `@templ.Raw` is forbidden on untrusted content).
- **Interactivity**: **HTMX (v4)** for partial DOM swaps, inline editing, transaction Move/Merge/Split drawers, and reactive horizon switching without heavy SPA client state. Configured with `htmx.config.selfRequestsOnly = true` and `htmx.config.allowEval = false`.
- **Styling**: **Tailwind CSS (v4.3)** paired with **DaisyUI (v5.7)** component primitives.
- **Password Hashing**: Passwords are hashed using **Argon2id** (`memory = 64 MiB`, `iterations = 3`, `parallelism = 2`, `salt = 16 bytes CSPRNG`, `keyLen = 32 bytes`).
- **Browser Authentication & Session Token Security**:
  - Stateful HTTP-only, `SameSite=Lax`, `Secure` session cookies (`__Host-balance_session` in TLS production) backed by the `sessions` table in `main.db`.
  - **CSPRNG Session Tokens & Hashed Storage**: In accordance with RFC 9562 §6.9, UUIDv7 is **never** used as a session secret. Session tokens are generated using 32 bytes (256 bits) of `crypto/rand` encoded as Base64URL. Only `SHA-256(raw_token)` is stored in `sessions.token_hash` (while `sessions.id` is an internal UUIDv7 primary key), ensuring a read-only database leak cannot be used to hijack active sessions.
  - **Session Fixation & Lifecycle**: Logging in, registering, or changing passwords immediately rotates the session token. Changing a password invalidates all other active sessions for that user.
  - **CSRF Protection**: Enforced on **all** state-mutating requests (`POST`, `PUT`, `PATCH`, `DELETE`) authenticated via session cookies (across both HTMX UI routes and `/api/v1/...` endpoints) using a per-session 256-bit `csrf_token` verified via the `X-CSRF-Token` header or form field.

### 3.2 OpenAPI 3.1 REST API & Personal Access Tokens (`Bearer`)
- Alongside the HTMX views, the Echo server exposes a complete JSON REST API under `/api/v1/...`.
- **Schema-First with `oapi-codegen`**: `api/openapi.yaml` is the single source of truth, generating strict Go server interfaces and DTOs, with interactive documentation served at `/api/docs` (protected behind authentication in production).
- **Shared Application Layer**: Both `templ`/HTMX handlers and OpenAPI JSON handlers invoke the exact same CQRS Command and Query services.
- **Dual Auth Support (`SessionOrBearerAuthMiddleware`)**:
  - API endpoints accept either a valid Browser Session cookie (which **requires** a valid `X-CSRF-Token` on mutating methods) OR an `Authorization: Bearer <pat_token>` header (exempt from CSRF because it is not automatically sent by browsers).
  - Bearer tokens are stateful **Personal Access Tokens (PATs)** with prefix `bal_pat_` + 32 bytes of `crypto/rand` Base64URL, stored as `SHA-256` hashes in `main.db` (`api_tokens` table) with explicit expiration (`expires_at`), revocation support, and `last_used_at` tracking.

### 3.3 YNAB-First UX Philosophy, User Journey & RBAC Permission Matrix
The primary UX benchmark for **Balance** is **YNAB (You Need A Budget)** — prioritizing zero-friction envelope allocation, immediate visual feedback on whether every bucket is healthy, and rapid transaction entry/reconciliation:

1. **Authentication (`/login` & `/register`)**:
   - Dedicated **Login** (`GET /login`, `POST /login`) and **Registration** (`GET /register`, `POST /register`) screens with server-side validation, IP/account rate-limiting (token bucket throttling against brute-force attacks), and automatic session initialization in `main.db`.
   - Public user registration can be disabled via environment variable `ALLOW_REGISTRATION=false`. When disabled, registration endpoints (`GET/POST /register` and `POST /api/v1/auth/register`) return `403 Forbidden`, and registration links/callouts are hidden on the login page.
2. **Post-Login Landing: Budget Selector (`/budgets`) & Top Navigation Bar**:
   - Immediately after logging in, the user lands on the **Budget List (`/budgets`)** screen displaying cards for all budgets the user owns or is an accepted member of (showing budget name, timezone, member count, and last activity), plus a **"+ New Budget"** button and pending **Budget Invitations** banner.
   - **Global Top Bar (Header)**: Always visible across `/budgets` and inside active budget views. Contains:
     - Active budget switcher dropdown (when inside a budget),
     - **Age of Money** metric badge (when inside a budget),
     - User profile / locale settings link, and a prominent **"Log Out" (`POST /logout`)** action.
3. **Active Budget Layout & Left Sidebar Navigation**:
   - Selecting a budget from `/budgets` opens the main **Budget (Buckets)** view (`/budgets/:budgetId/budget`).
   - **Left Sidebar Menu**: Provides persistent one-click navigation (`hx-boost="true"`) across the 5 core workspace sections:
     1. **Budget (Buckets / Envelopes)** (`/budgets/:budgetId/budget`) — Default view upon selecting a budget.
     2. **Transactions** (`/budgets/:budgetId/transactions`) — Unified ledger & Raw-to-Budget reconciliation workbench.
     3. **Reports** (`/budgets/:budgetId/reports`) — 10 YNAB + Actual Budget interactive ECharts reports.
     4. **External Parties** (`/budgets/:budgetId/parties`) — Counterparties directory (e.g., `"Żabka"`, `"Netflix"`), their virtual/external accounts, matching rules, and counterparty cashflow.
     5. **Members** (`/budgets/:budgetId/members`) — Multi-user budget membership management (`OWNER`, `ADMIN`, `EDITOR`, `VIEWER`), token-based invitations (`budget_invitations`), and shared bank integration links.
   - Below the 5 main menu items, the left sidebar also displays a YNAB-style collapsible summary of **User-Owned Accounts** (`CHECKING`, `SAVINGS`, `CREDIT_CARD`, `CASH`) with their current multi-currency balances.
   - **Unified RBAC Roles & Permission Matrix (Locked across UI, API, and SQL)**:
     - **`OWNER`**: Full control over the budget, including budget deletion, timezone/settings modification, managing all member roles (`OWNER`, `ADMIN`, `EDITOR`, `VIEWER`), and all `ADMIN`/`EDITOR` permissions.
     - **`ADMIN`**: Can invite/remove `EDITOR` and `VIEWER` members, link/unlink their own bank integrations, restore historical entity versions (`/restore`), and perform all `EDITOR` actions.
     - **`EDITOR`**: Day-to-day budgeting: create/edit/archive `Bucket`s, `Target`s (including AI Formula Assistant), `Party`/`Account` records, `MatchingRule`s, manual `BudgetTransaction`s, Move/Merge/Split reconciliation, trigger bank sync, and save/run reports (including Monte Carlo simulations).
     - **`VIEWER`**: Strictly read-only access (`GET` views and read-only report queries). **Cannot** mutate any budget data, **cannot** invoke paid external AI generation (`/targets/generate-formula`), **cannot** trigger bank syncs, and **cannot** save report configurations.

### 3.4 YNAB-Style Bucket View & Health Status Indicators ("Is Everything All Right")
When a budget is opened (`/budgets/:budgetId/budget`), the center workspace displays:
- **Top Horizon & Pool Banner**:
  - **Unallocated Pool ("Ready to Assign")** per currency + **One-Click Auto-Assign ("Auto-Assign by Priority")** button.
  - **Rolling Window Selector** (`Monthly`, `30d`, `90d`, `Custom`).
- **Bucket Groups & Bucket Rows with Health Status Pill**:
  - Every `Bucket` row shows its name, assigned amount in the active horizon, activity (spent), available multi-currency balance, and an unmistakable **YNAB-style Health Status Indicator** telling the user at a glance whether everything is in order:
    - 🟢 **`Funded / On Track` (Green Badge)**: Available balance >= 0 AND all required `Mandatory` (`priority = 3`) and `Recommended` (`priority = 2`) targets in the active time window are 100% funded.
    - 🟡 **`Underfunded` (Amber/Yellow Badge + Progress Bar)**: Available balance >= 0, but one or more targets in the active rolling window still need funding. Displays the exact missing amount (e.g. `Underfunded by 450.00 PLN due Nov 15`) and highlights whether a `Mandatory` (`priority = 3`) target is at risk.
    - 🔴 **`Overspent` (Red Alert Badge)**: Available balance in any currency is negative (`< 0`), requiring immediate coverage from the Unallocated Pool or another Bucket.
    - ⚪ **`On Hold / Optional Remaining` (Neutral Badge)**: All required targets are met; only `Optional` (`priority = 1`) wish-list targets remain unfunded.
- **Right-Hand Inspector Drawer (YNAB Target & Funding Inspector)**:
  - Clicking any `Bucket` opens the right-hand inspector panel showing its individual `Target`s (ordered by priority), non-spending strategy selector, the **AI Formula Assistant** (`LUA_FORMULA`), and quick-fund buttons.

### 3.5 Transactions View & Primary Toolbar Actions
In the **Transactions (`/budgets/:budgetId/transactions`)** section, the top action toolbar prominently provides two primary buttons:
1. **🔄 "Sync with Bank"** (`hx-post="/budgets/:budgetId/transactions/sync"`):
   - Immediately triggers synchronization across all connected bank integrations (`GoCardless`, `MockAdapter`, etc.) for the budget's accounts, deduplicates incoming `RawTransaction`s, runs the `MatchingRule` engine, and swaps in newly fetched transactions with a toast summary (e.g. *"Fetched 14 new transactions, 11 automatically matched"*).
2. **➕ "Add Manual Transaction"** (`hx-get="/budgets/:budgetId/transactions/new"` / `hx-post="/budgets/:budgetId/transactions"`):
   - Opens an inline YNAB-style rapid entry row (or mobile bottom sheet) allowing the user to manually record a `BudgetTransaction` (selecting `source_account_id`, `destination_account_id` or creating a new `Party`/`Account` on the fly, currency, amount, `Bucket`, optional `Target`, date, and notes).
- Alongside these buttons, users can toggle the **Reconcile / Move-Merge-Split Workbench** to resolve unmatched `RawTransaction`s into `BudgetTransaction`s.

### 3.6 Mobile-Responsive UX & Online-Only PWA (Progressive Web App)
- **Mobile-First Responsive Design (`< 768px` / `md:` breakpoints)**:
  - **Navigation Adaptation**: On mobile screens, the desktop left sidebar automatically transforms into a fixed bottom navigation bar (**DaisyUI 5 `dock`**) for the primary views (`Budget`, `Transactions`, `Reports`, `Parties`, `Members`) plus a compact top navbar with the budget switcher and logout menu.
  - **Touch-Optimized Bucket & Transaction Cards**: Wide desktop tables adapt into stacked, scannable mobile cards with clear status badges (🟢/🟡/🔴), tap-to-assign bottom sheets (`modal-bottom`), and swipe/tap reconciliation actions.
- **Installable PWA — Strictly Online-Only (No Offline Mode)**:
  - **Web App Manifest (`GET /manifest.webmanifest`)**: Configures `name: "Balance"`, `short_name: "Balance"`, `display: "standalone"`, `start_url: "/budgets"`, `theme_color`, `background_color`, and multi-resolution app icons (`192x192`, `512x512`, maskable) + Apple iOS meta tags (`apple-mobile-web-app-capable`), allowing installation to the home screen on Android, iOS, and Desktop.
  - **Pass-Through Service Worker (`GET /sw.js`) — No Offline Data Caching**:
    - Registered solely to satisfy browser PWA installability criteria (`beforeinstallprompt`).
    - All HTTP/HTMX/API requests use a strict **`NetworkOnly`** strategy (zero caching of HTML/JSON financial state in `CacheStorage` or `IndexedDB`), guaranteeing that the server's SQLite database (`main.db` + `budget_{id}.db`) is always the single source of truth.
    - If the device loses internet connectivity, the Service Worker renders a clean, lightweight static **"No Connection — Balance requires an active server connection"** screen with an automatic retry button when back online.

---

## 4. Database Architecture, PRAGMAs & Multi-Tenancy

### 4.1 SQLite Connection Configuration & Tenant Pool Hardening (Locked)
Every SQLite connection opened by the application (both `main.db` and tenant `budget_{id}.db` files) **must** immediately execute the following initialization PRAGMAs:

```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
PRAGMA cache_size = -16000; -- 16 MiB page cache cap per connection (negative value = KiB)
PRAGMA foreign_keys = true;
PRAGMA temp_store = memory;
```

- **Tenant Connection Pool & Path Traversal Guard**:
  1. **Strict Pre-Open Authorization**: The tenant middleware **must**:
     - Validate that `:budgetId` is a canonical 36-character UUIDv7 string (`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).
     - Query `main.db` (`budget_members`) to verify that the authenticated `user_id` is an active member of `:budgetId` and holds the required RBAC role **before** touching the filesystem or calling `sql.Open`.
  2. **Filesystem Safety**: Construct the database path strictly via `filepath.Join(budgetsDir, budgetID + ".db")`, verify `filepath.Dir(resolvedPath) == budgetsDir`, and for existing budgets open SQLite with `mode=rw` (failing if the file does not exist rather than creating arbitrary empty `.db` files on disk).
  3. **Bounded LRU Tenant Pool**: An in-memory `TenantDBManager` maintains an LRU cache of open `*sql.DB` handles (default max `64` active tenant DBs, each configured with `SetMaxOpenConns(4)` and `SetMaxIdleConns(2)`), automatically closing idle handles to prevent file descriptor (`ulimit -n`) and RAM exhaustion. Per-user budget creation is capped (default max `20` owned budgets per user).

### 4.2 Migration Strategy
- Plain-SQL versioned migrations managed via `goose` or `golang-migrate` (embedded in the Go binary via `embed.FS`).
- Separate migration directories:
  - `migrations/main/*.sql` $\rightarrow$ applied to `main.db` on startup.
  - `migrations/tenant/*.sql` $\rightarrow$ applied to each `budget_{id}.db` on budget creation and lazy tenant open.

### 4.3 Database Split
1. **`main.db` (Global Registry)**:
   - `users`: User credentials (`Argon2id` hash), locale, date/number formatting preferences.
   - `sessions`: Browser session state (`id` UUIDv7 PK, `token_hash` SHA-256 of CSPRNG cookie token, `user_id`, `active_budget_id`, `csrf_token`, `expires_at`, etc.).
   - `api_tokens`: Personal Access Tokens for REST API `Bearer` authentication (`id`, `user_id`, `name`, `token_hash` SHA-256, `expires_at`, `last_used_at`, `revoked_at`).
   - `budgets`: Budget registry, timezone, owner metadata.
   - `budget_members`: Multi-user access control per budget (`OWNER`, `ADMIN`, `EDITOR`, `VIEWER`).
   - `budget_invitations`: Token-based pending invitations (`id`, `budget_id`, `invited_email`, `role`, `token_hash`, `invited_by_user_id`, `expires_at`, `accepted_at`) preventing forced membership and email enumeration.
   - `user_integrations`: Integration instances per user (`provider`, AEAD-encrypted credentials with AAD binding, sync status).
   - `integration_oauth_states`: Ephemeral CSRF/session-bound state tokens for bank OAuth/requisition callbacks (`state_hash`, `user_id`, `session_id`, `provider`, `expires_at`).
   - `currency_exchange_rates`: Global historical and current daily exchange rates shared across all tenant budgets (populated via background NBP/ECB sync workers or deterministic test fixtures, and cached in-memory by the Go service).
2. **`budget_{id}.db` (Per-Budget Isolated Tenant DB)**:
   - `parties`, `accounts`, `account_currency_balances`, `account_integration_links`, `matching_rules`.
   - `raw_transactions`, `budget_transactions`, `transaction_links`.
   - `bucket_events`, `target_events`, `buckets_projection`, `bucket_currency_balances`, `targets_projection`, `saved_reports`.

---

## 5. Strict Account-to-Account Model, External Accounts & Matching Engine

### 5.1 Multi-Currency Accounts & Parties
- Every `Account` is owned by a `Party` (`party_id NOT NULL`).
- **Multi-Currency Support**: An `Account` is not restricted to a single currency. While an account defines a `primary_currency` for UI defaults, it can hold balances and record transactions in any number of currencies (e.g., legacy multi-currency bank accounts or multi-currency wallets). Balances are maintained per `(account_id, currency)` in `account_currency_balances`.
- **Internal vs. External Accounts**:
  - **User-Owned Accounts**: Owned by a `Party` with `is_user_owned = 1` (e.g., User's Checking, Savings, Cash wallet, Credit Card, or joint family account).
  - **External / Counterparty Accounts**: Owned by a `Party` with `is_user_owned = 0` (e.g., `"Żabka"`, `"Biedronka"`, `"Employer Inc."`, `"Friend Marek"`).
  - A single external `Party` (like `"Żabka"` or a telecom provider) can have one virtual external `Account` (e.g., `"Żabka POS"`) or multiple known IBAN accounts.

### 5.2 Double-Entry Budget Transactions (`source_account_id` $\rightarrow$ `destination_account_id`) & Ledger Invariants
Every `BudgetTransaction` moves value from `source_account_id` to `destination_account_id`:
- **Expense (Outflow)**:
  - `source_account_id`: User's Checking/Cash/Card Account (`is_user_owned = 1`).
  - `destination_account_id`: Counterparty's External Account, e.g., `"Żabka POS"` (`is_user_owned = 0`).
  - Decreases `source_account_id` balance in `source_currency` and deducts from the assigned `Bucket` (and `Target`).
- **Income (Inflow)**:
  - `source_account_id`: Employer/Counterparty External Account (`is_user_owned = 0`).
  - `destination_account_id`: User's Checking/Cash Account (`is_user_owned = 1`).
- **Internal Transfer & Currency Exchange**:
  - Both `source_account_id` and `destination_account_id` belong to user-owned Parties (`is_user_owned = 1`).
  - For a currency exchange within the **same** multi-currency account, `source_account_id == destination_account_id`, while `source_currency = 'PLN'` (`source_amount_minor = 43000`) and `destination_currency = 'EUR'` (`destination_amount_minor = 10000`).
- **Mandatory Domain Invariants (Enforced in Application & SQL)**:
  1. **No Same-Account Same-Currency Self-Transfer**: `source_account_id != destination_account_id OR source_currency != destination_currency` (prevents artificial bucket depletion without real balance movement).
  2. **User-Owned Participation**: At least one of `source_account_id` or `destination_account_id` **must** belong to a `Party` with `is_user_owned = 1` (pure external-to-external `0 -> 0` transactions are forbidden).
  3. **Safe Amount Bounds**: `0 < source_amount_minor <= MaxSafeAmountMinor` ($10^{14}$) and `0 < destination_amount_minor <= MaxSafeAmountMinor` ($10^{14}$).
  4. **Target-to-Bucket Consistency**: If `target_id` is non-null, `bucket_id` must be non-null and equal `target.bucket_id`, and `target.currency` must match the relevant transaction currency.

### 5.3 Auto-Matching Rules & On-the-Fly Creation ("Żabka" Use Case)
1. **Import**: When `RawTransaction` is imported (via GoCardless, CSV, or Mock), it records `account_id` (the user's account), `raw_payee` (e.g. `"ZABKA Z1234 WARSZAWA"`), `counterparty_iban` (if present), and `reference`.
2. **Matching Rule Engine & ReDoS Protection**:
   - Evaluates `matching_rules` ordered by `priority`:
     - Match by exact `counterparty_iban`, OR
     - Match `raw_payee` / `reference` by `CONTAINS`, `PREFIX`, `EXACT`, or `REGEX` (e.g., pattern `"ZABKA"` $\rightarrow$ maps to `Party: "Żabka"`, `counterparty_account_id: "Żabka External Account"`, default `Bucket: "Groceries"`).
   - **ReDoS Hardening**: `REGEX` rules are evaluated strictly using Go's standard library `regexp` package (RE2 linear-time engine $O(N)$, never backtracking PCRE). `match_pattern` length is capped at `256` characters, pre-compiled and validated upon rule creation/update, and total active matching rules per budget are capped at `500`.
3. **UI Resolution (Move / Merge / Split)**:
   - When the user selects `RawTransaction`(s) in the UI to move into `BudgetTransaction`(s), the rule engine pre-fills the suggested Counterparty `Party` / `Account` and `Bucket`.
   - **On-the-Fly Creation**: If a counterparty does not exist yet, the user can type a name (e.g. `"Żabka"`) in the HTMX modal to create the `Party`, its virtual external `Account`, and an optional `MatchingRule` in a single atomic action.
4. **Party & Account Reporting**:
   - Reports aggregate inflows, outflows, and net flow per `Party` (summing across all accounts belonging to that `Party`, e.g., total spent at `"Żabka"`) or per individual `Account`.
   - Includes a toggle to **exclude internal transfers** where both source and destination parties have `is_user_owned = 1`.

---

## 6. Two-Tier Transaction Resolution (Move, Merge, Split)

- **N-to-1 Merge Example (Dinner with Friends)**:
  - Raw 1: `-300.00 PLN` (Restaurant paid by user).
  - Raw 2: `+100.00 PLN` (Friend A transfer).
  - Raw 3: `+100.00 PLN` (Friend B transfer).
  - User selects Raw 1, 2, 3 $\rightarrow$ clicks **Move & Merge** $\rightarrow$ creates a single `BudgetTransaction` of `100.00 PLN` (`source_account_id = User Checking`, `destination_account_id = Restaurant External Account`, `bucket_id = Dining Out`).
- **1-to-N Split Example (Supermarket Receipt)**:
  - Raw 1: `-400.00 PLN` $\rightarrow$ split into two `BudgetTransaction`s: `300.00 PLN` (`Bucket: Groceries`) and `100.00 PLN` (`Bucket: Household`).
- **Arbitrary N-to-M Cardinality & Double-Resolve Prevention**:
  - Recorded in `transaction_links` under a shared `operation_id` (UUIDv7) with `operation_type` (`DIRECT_MOVE`, `MERGE`, `SPLIT`, `COMPOSITE`).
  - **Idempotency & Integrity**: Every `raw_transaction_id` passed to a Move/Merge/Split operation must currently exist in `raw_transactions_current` with `status = 'PENDING'` (not already `PROCESSED` or `IGNORED`). Within a single atomic SQLite transaction, the selected `RawTransaction`s are transitioned to `status = 'PROCESSED'`, the `BudgetTransaction`s are inserted, and the `transaction_links` rows are recorded.
- **Accounts Without Bank Integration (Cash, Manual Cards)**:
  - Users can create `BudgetTransaction`s directly on manual/cash accounts without requiring a prior `RawTransaction`.

---

## 7. Bucket & Target Engine (CQRS & Rolling Windows)

### 7.1 Multi-Currency Balances & Direct Target Attribution
- Each `Bucket` maintains independent sub-balances per currency (`bucket_currency_balances`).
- Each `Target` has a single currency and belongs to a `Bucket`. A `Bucket` can have multiple `Target`s in different currencies (e.g., Bucket `"Trip to Japan"` with Target 1: Flights `5000 PLN`, Target 2: Hotel `150,000 JPY`, Target 3: Cash `100,000 JPY`).
- Funds allocated to a bucket can be attributed directly to a specific `Target` (`target_id`), and `BudgetTransaction`s can optionally reference `target_id` to consume that target's allocation.

### 7.2 Rolling Window Horizon Math (e.g., 30-Day or 90-Day View)
Given a selected horizon of $H$ days ($1 \le H \le 3650$) starting at $T_{\text{start}}$ (evaluated in the budget's timezone):
1. **One-Off Target** (Due at $T_{\text{due}}$, remaining unfunded amount $U$):
   - If $T_{\text{due}} \le T_{\text{start}} + H$: Full remaining amount $U$ is required within this window.
   - If $T_{\text{due}} > T_{\text{start}} + H$: Prorated requirement for the window is:
     $$A_{\text{window}} = U \times \frac{H}{T_{\text{due}} - T_{\text{start}}}$$
     *(Example: Flight `5000 PLN` due in 30d + Hotel `3000 PLN` due in 180d viewed in a 90d window $\implies 5000 + 3000 \times \frac{90}{180} = 6500\text{ PLN}$).*
2. **Recurring Target** (Every $N$ days, where $1 \le N \le 36500$, cycle amount $C$):
   - Number of occurrences in window $H$: $\left\lfloor \frac{H}{N} \right\rfloor$ (or exact scheduled dates falling within $[T_{\text{start}}, T_{\text{start}} + H]$). `recurrence_days` ($N$) is strictly constrained to $N \ge 1$ at both the domain and SQL `CHECK` levels to prevent division-by-zero panics and infinite date-expansion loops.
   - *(Example: English lessons `100 PLN` every `10 days` in a `90-day` window $\implies 9 \times 100 = 900\text{ PLN}$).*

### 7.3 Non-Spending / Under-Spending Strategies (Locked)
1. **`ROLL_OVER` (Carry-Over Surplus)**: Unspent funds stay on the target and reduce the next period's required contribution.
2. **`RETURN_TO_POOL` (Sweep to Ready-to-Assign)**: Unspent funds return to the global unassigned pool at period end.
3. **`ARREARS_CUMULATIVE` (Must-Pay / Cumulative Debt)**: Unpaid target amount carries over as mandatory arrears added to the next period ($Req_{\text{next}} = C + \text{Arrears}$), e.g., rent or loan payments.
4. **`CAP_AND_HOLD` (Ceiling Buffer)**: Accumulates until `ceiling_amount_minor` is reached; required funding stays at 0 until spent.
5. **`PRORATE_FUTURE` (Cascade to Future Milestones)**: Surplus reduces required funding across subsequent targets in the same bucket.

### 7.4 Target Priority Hierarchy & Waterfall Engine (Locked)
Every `Target` has an integer `priority` (`priority >= 1`) where **a higher value means higher importance**, combined with a deterministic tie-breaking order:
$$\text{Evaluation Order}: \quad \texttt{priority DESC}, \quad \texttt{due\_date ASC NULLS LAST}, \quad \texttt{sort\_order ASC}$$

- **Initial UI Priority Tiers (Extensible in DB)**:
  - `1` — **Optional / Nice-to-Have**
  - `2` — **Recommended / Should-Have** (default)
  - `3` — **Mandatory / Must-Have**
  - *(Because `priority` is stored as an open `INTEGER CHECK(priority >= 1)`, higher custom priority levels `4, 5, ...` can be enabled in the UI in the future without database migrations).*
- **Three Operational Roles of Target Priority**:
  1. **Global Budget Auto-Fund (`Auto-Fund Underfunded` in Active Horizon)**:
     - When distributing available `Ready to Assign` funds in a given currency across the active window (e.g., 30-day or 90-day view), the engine funds all `priority = 3` targets first (earliest `due_date` first), then `priority = 2`, then `priority = 1`, stopping as soon as available funds are exhausted.
  2. **Intra-Bucket Waterfall & `PRORATE_FUTURE` Cascade**:
     - When a user allocates a lump sum to a `Bucket` as a whole (without selecting a specific `target_id`), or when a completed target cascades its surplus via `PRORATE_FUTURE`, funds flow sequentially through the bucket's targets ordered by `priority DESC, due_date ASC, sort_order ASC`.
  3. **Default Expense Target Consumption**:
     - When an outflow `BudgetTransaction` is assigned to a `Bucket` without an explicit `target_id`, it automatically consumes funded balance from the highest-priority active target in the matching currency (`priority DESC, due_date ASC, sort_order ASC`).

### 7.5 Programmable Target Formulas via Sandboxed Lua Engine (Locked)
While most targets have a static amount (`amount_mode = 'FIXED'`) that simply sums toward the `Bucket`'s total requirement, Balance also supports dynamic, dependent targets via an embedded **Lua Formula Engine (`amount_mode = 'LUA_FORMULA'`)**.

#### Motivation: The Mortgage Installment + Overpayment Example
Suppose a `"Mortgage"` Bucket has two targets in `PLN`:
1. **Target 1 (`"Required Mortgage Installment"`)**: `amount_mode = 'FIXED'`, `amount_minor = 90000` (`900.00 PLN`), `priority = 3` (*Mandatory*), strategy = `ARREARS_CUMULATIVE`.
2. **Target 2 (`"Mortgage Overpayment"`)**: `amount_mode = 'LUA_FORMULA'`, `priority = 1` (*Optional*), strategy = `RETURN_TO_POOL`.
   - The user wants total monthly mortgage spending to equal `2,000.00 PLN`, meaning the overpayment target should dynamically equal `2,000 PLN - Required Mortgage Installment`:
     ```lua
     return math.max(0, money(2000) - target("Required Mortgage Installment").required)
     ```
   - When the required installment is `900 PLN`, the Lua formula evaluates to `1,100 PLN` (`110000` minor units), and the Bucket's total requirement sums to `900 + 1100 = 2000 PLN`.
   - If interest rates change and the user updates `"Required Mortgage Installment"` to `840 PLN`, `"Mortgage Overpayment"` automatically evaluates to `1,160 PLN` without manual editing.
   - If the user missed last month's installment (`ARREARS_CUMULATIVE` increases `.required` to `1,800 PLN`), the overpayment automatically shrinks to `200 PLN` (`2000 - 1800`) so mandatory arrears take precedence within the `2,000 PLN` budget.

#### Sandboxed Lua VM Architecture & 6-Pillar Security Hardening (`github.com/yuin/gopher-lua`)
To guarantee that user-supplied Lua formulas can never escape the sandbox, exhaust memory/CPU, execute SQL queries, or corrupt financial ledger math, the engine enforces 6 mandatory security and reliability controls:

1. **Pillar 1 — Strict Function Whitelist (`SkipOpenLibs: true`), AST Grammar Ban & Bytecode Ban**:
   - The VM is initialized with `SkipOpenLibs: true` (never calling `lua.OpenBase`, `lua.OpenString`, `lua.OpenTable`, `lua.OpenIo`, `lua.OpenOs`, `lua.OpenDebug`, `lua.OpenPackage`, or `lua.OpenCoroutine`).
   - Only a manually constructed, read-only `math` table (`abs`, `ceil`, `floor`, `fmod`, `max`, `min`, `pow`, `sqrt`), `tonumber`, and the read-only domain API (`money`, `target`, `bucket`, `fx`, `budget`, `window`) are registered.
   - All metaprogramming, environment manipulation, and bytecode loading functions (`loadstring`, `load`, `dofile`, `loadfile`, `getfenv`, `setfenv`, `getmetatable`, `setmetatable`, `rawget`, `rawset`, `rawequal`, `collectgarbage`, `newproxy`, `string.dump`) are completely absent.
   - **AST-Level Grammar Restrictions (`gopher-lua/ast` Visitor before Compilation)**:
     - **No Loops or Jumps**: Rejects `WhileStmt`, `RepeatStmt`, `NumberForStmt`, `GenericForStmt`, and `GotoStmt`.
     - **No User-Defined Functions**: Rejects `FunctionStmt` and `FuncExpr` (preventing recursive calls and tail-call elimination infinite loops that would bypass `CallStackSize`).
     - **No String Concatenation Bomb (`..`)**: Rejects `ConcatOp` (`OP_CONCAT`) in the AST so scripts cannot exponentially allocate multi-gigabyte strings on the Go heap.
   - **Source Input Validation**: Rejects any script containing control bytes (specifically the `\x1bLua` precompiled bytecode header), scripts exceeding **2 KB** (2,048 bytes), or scripts exceeding **50 lines**.
2. **Pillar 2 — Hard Memory, Stack, Instruction Quota & Emergency Timeout**:
   - Every `lua.NewState` is configured with strict bounds:
     ```go
     lua.Options{
         CallStackSize:   16,  // Prevents deep call nesting
         RegistrySize:    128, // Initial VM register allocation
         RegistryMaxSize: 256, // Hard ceiling on VM memory/register growth
         SkipOpenLibs:    true,
     }
     ```
   - **Deterministic Instruction Quota**: Enforces a hard limit of `MaxVMInstructions = 500` executed bytecode instructions per evaluation (so formula termination is 100% deterministic regardless of host CPU load), backed by a `context.WithTimeout(ctx, 50*time.Millisecond)` emergency safety net.
3. **Pillar 3 — `NaN` / `±Inf` / `int64` Overflow Sanitizer**:
   - Because Lua 5.1 `LNumber` is `float64`, casting `NaN` or `+Inf` (e.g. `100 / 0`) directly to `int64` in Go would produce `math.MinInt64` (`-9223372036854775808`), corrupting bucket sums.
   - The Go output validator enforces:
     1. Return value must be a single `lua.LNumber`.
     2. `!math.IsNaN(val) && !math.IsInf(val, 0)`.
     3. Clamped and validated within `[0, MaxSafeTargetMinor]` where `MaxSafeTargetMinor = 100_000_000_000_00` ($10^{14}$ minor units, safely inside IEEE 754's exact integer boundary $2^{53}-1$ and immune to `int64` overflow during bucket summation).
     4. Rounded deterministically via `int64(math.Round(val))`.
4. **Pillar 4 — 100% Pure Function Determinism**:
   - `math.random`, `math.randomseed`, and system clock access are excluded. Because termination is governed by the deterministic instruction limit (`MaxVMInstructions = 500`) rather than CPU-sensitive wall-clock races, for any given budget snapshot and horizon window a Lua formula is a pure mathematical function `f(snapshot, window) -> int64`, preserving CQRS and Event Sourcing replay determinism.
5. **Pillar 5 — Zero Database I/O Inside Lua (`InMemoryBudgetSnapshot`) & Call Quotas**:
   - The Lua VM never holds a `*sql.DB` reference and cannot trigger SQL queries.
   - Prior to formula evaluation, Go loads an immutable, tenant-scoped `InMemoryBudgetSnapshot` for the active `budget_{id}.db` (including pre-computed daily prefix-sum arrays for inflows/outflows over the last 3,650 days). All calls to `target()`, `bucket.balance()`, `budget.income()`, `budget.spent()`, and `fx.rate()` validate their arguments (e.g. `1 <= days <= 3650`, valid ISO-4217 currency) and read from this in-memory struct in $O(1)$ time.
   - Enforces a per-script host call counter (`MaxHostAPICalls = 32`), a maximum DAG dependency depth (`MaxDAGDepth = 8`), and circular dependency tracking (`ErrCircularTargetDependency` if `Target A -> Target B -> Target A`).
6. **Pillar 6 — Ephemeral Isolated `LState`, Read-Only Tables & Graceful Degradation**:
   - Pre-compiled Lua AST (`*lua.FunctionProto`) is cached per target version, while each evaluation runs in a fresh, isolated `lua.LState` with a locked global environment (`__newindex` raises an error on any global write or mutation of returned tables, and `__metatable = "locked"`).
   - **Graceful Degradation**: If a formula fails at runtime (e.g., division by zero or referencing a deleted target), it **never breaks the dashboard or returns HTTP 500**. Instead, the evaluator resolves the target's required amount to `0` (or its cached fallback), records the diagnostic message in `targets_projection.last_formula_error`, renders a localized warning badge next to the target in the HTMX UI, and logs the warning with `correlation_id`.

#### Rich Read-Only Domain API Exposed to Lua
All monetary values returned and expected by Lua formulas are integer **minor units** (`int64`, e.g., grosze/cents):

| Lua Function / Object | Signature & Return Type | Description |
|---|---|---|
| `money(val)` | `money(number \| string) -> int64` | Converts major currency units into integer minor units for the target's currency (e.g. `money(2000)` $\rightarrow$ `200000`, `money("19.99")` $\rightarrow$ `1999`). |
| `target(ref)` | `target(id_or_name) -> table` | Looks up another target by UUIDv7 or by name within the same Bucket from the in-memory snapshot. Returns read-only `{ planned = int64, required = int64, funded = int64, spent = int64, priority = int }`. |
| `bucket.balance` | `bucket.balance(currency?) -> int64` | Returns the current Bucket's balance in minor units for the given currency (defaults to the target's currency). |
| `fx.rate` | `fx.rate(base, target, ts?) -> number` | Returns the historical or latest exchange rate from `main.db`'s `currency_exchange_rates` cache (e.g. `fx.rate("EUR", "PLN")`). |
| `fx.convert` | `fx.convert(amount_minor, from, to, ts?) -> int64` | Converts `amount_minor` from `from` currency to `to` currency using 128-bit safe integer micro-rate math. |
| `budget.income` | `budget.income(days, currency?) -> int64` | Total external inflows into user-owned accounts over the last `days` (`1 <= days <= 3650`, excluding internal transfers). |
| `budget.spent` | `budget.spent(days, currency?) -> int64` | Total external outflows from user-owned accounts over the last `days` (`1 <= days <= 3650`, excluding internal transfers). |
| `window.days` | `int` | Length in days of the currently evaluated rolling horizon (e.g. `30`, `90`). |

#### Example Lua Target Formulas
```lua
-- 1. Mortgage Overpayment: complement up to 2000 PLN minus required installment
return math.max(0, money(2000) - target("Required Mortgage Installment").required)

-- 2. Save 15% of external income from the last 30 days, capped at 5000 PLN
return math.min(money(5000), math.floor(budget.income(30, "PLN") * 0.15))

-- 3. FX-hedged target: need 1000 EUR worth of PLN in a PLN target
return fx.convert(100000, "EUR", "PLN")
```

### 7.6 AI Formula Assistant (Google Sheets-Style Natural Language to Lua)
When a user with `OWNER`, `ADMIN`, or `EDITOR` role selects `calculation_mode = 'LUA_FORMULA'` in the Target creation/editing UI, an interactive **"Generate Formula with AI"** assistant panel appears (inspired by Google Sheets formula generation):
- The user describes their goal in natural language (capped at `UserPrompt <= 500` characters, e.g., *"I want to allocate 2000 PLN total to mortgage each month, so this target should equal 2000 PLN minus my required mortgage installment"*).
- Clicking **Generate** triggers a rate-limited HTMX request (`hx-post="/budgets/:budgetId/targets/generate-formula"`, max `10` requests/minute per user), which calls the backend AI service, validates and dry-runs the generated Lua formula in the 6-Pillar Sandbox against the live budget state, and returns the ready-to-save Lua code along with a live calculated preview (e.g., `Live preview now: 1,100.00 PLN`).

#### 1. Provider-Agnostic Abstraction (`FormulaAIProvider`)
To ensure the AI model or vendor can be swapped in the future without touching domain or HTTP handler logic, the AI client is hidden behind a clean Go interface in `internal/ai/formula.go`:

```go
type FormulaGenerationRequest struct {
    UserPrompt        string              // User's natural language goal description (max 500 chars)
    CurrentBucketName string              // Name of the bucket being edited
    TargetCurrency    string              // ISO-4217 currency of the target being edited (e.g. "PLN")
    WindowDays        int                 // Active rolling horizon (e.g. 30, 90)
    AvailableBuckets  []BucketContextMeta // Names, IDs, and currencies of buckets in the budget
    AvailableTargets  []TargetContextMeta // Names, IDs, bucket names, priorities, currencies, and current required/funded amounts
    PreviousAttempt   string              // Populated only on automatic self-correction retry
    ValidationError   string              // Sandbox compilation/runtime error from PreviousAttempt
}

type FormulaGenerationResult struct {
    LuaScript   string `json:"lua_script"`
    Explanation string `json:"explanation"`
}

type FormulaAIProvider interface {
    ProviderName() string
    GenerateLuaFormula(ctx context.Context, req FormulaGenerationRequest) (*FormulaGenerationResult, error)
}
```

#### 2. Pure Direct HTTP Implementation (No External AI SDKs)
- **Zero External AI SDK Dependencies**: The concrete provider (`GeminiFormulaProvider` in `internal/ai/gemini_http.go`) is implemented strictly using Go's standard library (`net/http`, `encoding/json`, `context.WithTimeout`) with direct HTTPS `POST` requests to the Google Gemini REST API (`https://generativelanguage.googleapis.com/v1beta/models/gemini-3.8-flash:generateContent`).
- **Model & Environment Configuration**:
  - Default model: `gemini-3.8-flash` (configurable via `AI_GEMINI_MODEL`, defaulting to `gemini-3.8-flash`).
  - API Key: Read strictly from the environment variable `GEMINI_API_KEY` (passed via the `x-goog-api-key` HTTP header; never logged or exposed to the browser).
  - Uses Gemini Structured Outputs (`responseMimeType: "application/json"` with `responseSchema` enforcing `{ "lua_script": "string", "explanation": "string" }`) and low temperature (`0.1`) for deterministic code generation.

#### 3. System Prompt Specification & Indirect Prompt Injection Defenses
Every request to `gemini-3.8-flash` includes a structured `systemInstruction` containing:
1. **Role & Output Contract**: Instructs the model that it is a Lua 5.1 formula generator for the **Balance** budgeting engine and must return a single deterministic expression or short script ending with `return <int64_minor_units>`.
2. **Strict 6-Pillar Sandbox Constraints (What is Forbidden)**:
   - Explicitly lists the ONLY allowed standard functions: `math.max`, `math.min`, `math.floor`, `math.ceil`, `math.abs`, `math.fmod`, `math.pow`, `math.sqrt`, `tonumber`.
   - Explicitly warns that `string`, `table`, `io`, `os`, `debug`, `package`, `coroutine`, `dofile`, `loadstring`, `require`, `pcall`, `xpcall`, `setmetatable`, `getmetatable`, string concatenation (`..`), function declarations (`function`), and **all loop constructs** (`while`, `repeat`, `for`, `goto`) are blocked at the AST/compiler level and will be rejected.
   - Script length must be $\le 2048$ bytes, execute in $\le 500$ VM instructions, and return a non-negative integer in minor units (cents/grosze).
3. **Read-Only Domain API Reference**:
   - Full signatures and semantics of `money(val)`, `target(id_or_name).required / .funded / .planned / .spent / .priority`, `bucket.balance(currency?)`, `fx.rate(base, target)`, `fx.convert(amount_minor, from, to)`, `budget.income(days, currency?)`, `budget.spent(days, currency?)`, and `window.days`.
4. **Live Budget Metadata Snapshot & Prompt-Injection Isolation**:
   - Serializes the current budget's `AvailableBuckets` and `AvailableTargets` inside a dedicated, escaped `<untrusted_budget_metadata_json>` delimiter block, explicitly instructing the model to treat all bucket/target names strictly as data identifiers and never as instructions.
   - Raw bank transactions (`raw_transactions`), IBANs, and counterparty `Party` names are never sent to the AI. The UI displays a clear privacy notice indicating that bucket/target names are shared with the configured AI provider when clicking **Generate Formula with AI**.

#### 4. Auto-Validation, Live Dry-Run Preview & Self-Correction Retry Loop (Locked)
Before returning any AI-generated formula to the user, the backend executes the `FormulaAssistantService` pipeline:
1. **Generate (Attempt 1)**: Calls `FormulaAIProvider.GenerateLuaFormula(ctx, req)`.
2. **Sandbox Validation & Dry-Run**:
   - Runs the 6-Pillar AST validator (size check, forbidden AST nodes/loops/functions/`..` check, bytecode compilation) and extracts referenced `target(...)` dependencies to verify the DAG has no circular references (`ErrCircularTargetDependency`).
   - Executes the compiled bytecode inside the isolated `gopher-lua` sandbox against the current `InMemoryBudgetSnapshot`.
3. **Automatic Self-Correction Retry (Max 1 Retry)**:
   - If AST validation, DAG cycle detection, or sandbox execution fails (e.g., syntax error, unknown target name, or non-numeric return value), the backend automatically invokes `GenerateLuaFormula` **one more time** with `PreviousAttempt` and `ValidationError` populated so `gemini-3.8-flash` can self-correct.
4. **UI Response (Strict HTML Escaping)**:
   - Returns an HTMX partial (or OpenAPI JSON response) populating the Lua code editor, a concise plain-text explanation of the formula (rendered strictly via escaped `a-h/templ` text expressions, never `@templ.Raw`), and a live badge showing the exact evaluated value for the current horizon (e.g., `Evaluated amount now: 1,100.00 PLN`).

---

## 8. Versioning, Event Sourcing & CQRS at Scale

### 8.1 Immutable Append-Only Versioned Entities + Synchronous `*_current` Read Projections
Applies to: `Party`, `Account`, `MatchingRule`, `RawTransaction`, `BudgetTransaction`.
- **100% Immutable Write Tables**: The historical tables (`parties`, `accounts`, `matching_rules`, `raw_transactions`, `budget_transactions`) are strictly append-only (`INSERT` only; never `UPDATE` or `DELETE`).
- Primary Key: `(id, version)` where `id` is UUIDv7 and `version` is a monotonically increasing integer (`1, 2, 3...`).
- Audit metadata on every row:
  - `action`: `'CREATE' | 'UPDATE' | 'RESTORE' | 'SOFT_DELETE'`
  - `agent_id`: UUIDv7 of the acting user or integration worker.
  - `correlation_id`: Server-generated UUIDv7 of the originating request/job.
  - `created_at`: UTC timestamp in microseconds.
- **Synchronous `*_current` Read Projections (Locked for 10-Year Scale)**:
  - To guarantee `< 5 ms` query times across `36,500+` transactions and `1,000+` accounts without expensive `ROW_NUMBER() OVER (PARTITION BY id ORDER BY version DESC)` window scans, each append-only table is paired with a synchronous `*_current` projection table (`parties_current`, `accounts_current`, `matching_rules_current`, `raw_transactions_current`, `budget_transactions_current`).
  - Maintained automatically within the same SQLite transaction (via SQLite `AFTER INSERT` triggers or inline Go projectors):
    - When `action IN ('CREATE', 'UPDATE', 'RESTORE')`, the row is upserted (`INSERT OR REPLACE`) into `*_current`.
    - When `action = 'SOFT_DELETE'`, the row is removed from `*_current` (while remaining permanently in the historical append-only table).
- **History & Rollback**: Opening the **History** drawer queries the append-only table (`SELECT * FROM accounts WHERE id = ? ORDER BY version DESC`). Restoring version $k$ when latest is $n$ inserts version $n+1$ with a copy of version $k$'s payload, `action = 'RESTORE'`, current `created_at`, and the restoring user's `agent_id` (which automatically updates `*_current`).

### 8.2 Event Sourcing + Synchronous Inline CQRS & Cross-Model Balance Consistency
- Mutations to `Bucket`s and `Target`s emit domain events into `bucket_events` and `target_events`.
- Within the **same SQLite transaction**, the synchronous projector updates `buckets_projection`, `bucket_currency_balances`, and `targets_projection`, guaranteeing immediate `read-your-own-writes` consistency for HTMX partial swaps.
- **Unified Balance Invariant Across Event-Sourced Buckets and Versioned Transactions (Locked)**:
  - Whenever a `BudgetTransaction` is created, updated, restored (`RESTORE`), or soft-deleted (`SOFT_DELETE`), the inline transaction projector atomically computes the net delta between the previous active version (if any) and the new version, and updates `account_currency_balances`, `bucket_currency_balances`, and `targets_projection.current_funded_minor` within the **same SQLite transaction**.
  - During a full CQRS projection rebuild from scratch, the rebuilder deterministically replays `bucket_events` and `target_events` (allocations, transfers between buckets, strategy rollovers) AND applies all active rows from `budget_transactions_current` (outflows/inflows), guaranteeing that materialized balances in `account_currency_balances`, `bucket_currency_balances`, and `targets_projection` never drift after edits or version rollbacks.

---

## 9. Extensible Bank Integrations, Cryptography & Deduplication

```
                  +-------------------------------------------------+
                  |           IntegrationProvider (Interface)       |
                  +-------------------------------------------------+
                  | ProviderCode() string                           |
                  | InitiateAuth(ctx, req) (*AuthRedirect, error)   |
                  | HandleCallback(ctx, params) (*Credentials, err) |
                  | FetchAccounts(ctx, creds) ([]ExtAccount, error) |
                  | FetchTransactions(ctx, creds, acct, since)      |
                  +-------------------------------------------------+
                                           ^
            +------------------+-----------+-----------+------------------+
            |                  |                       |                  |
  +-------------------+ +--------------+     +-------------------+ +---------------+
  | GoCardlessAdapter | |  CSVAdapter  |     |    MockAdapter    | | FutureAdapter |
  +-------------------+ +--------------+     | (E2E / Int Tests) | | (Plaid/FinTS) |
                                             +-------------------+ +---------------+
```

- **Extensible Provider Registry & Production Mock Guard**:
  - `user_integrations.provider` is an open `TEXT` identifier validated against the application's registered providers (`GOCARDLESS`, `CSV`, `MOCK`, and future providers like `ENABLE_BANKING`, `PLAID`, `POLISH_API`, `FINTS`) without restrictive SQL enum locks.
  - `MockAdapter` (`MOCK`) is registered **only** when explicitly enabled via `ALLOW_MOCK_PROVIDERS=true` (in dev/test/E2E environments) and is strictly disabled by default in production builds.
- **AEAD Credential Encryption with Context Binding (AAD)**:
  - `user_integrations.encrypted_credentials` is encrypted at rest using **AES-256-GCM** (or **XChaCha20-Poly1305**) with a 256-bit master key loaded from `ENCRYPTION_MASTER_KEY` and a fresh CSPRNG nonce per write.
  - Every encryption/decryption operation binds **Additional Authenticated Data (`AAD`)** set to `user_id + ":" + integration_id + ":" + provider`, cryptographically preventing ciphertext substitution across users or rows.
- **OAuth / Requisition State CSRF Protection**:
  - `InitiateAuth` generates a 256-bit CSPRNG `state` token, stores its SHA-256 hash in `integration_oauth_states` (`main.db`) bound to `(user_id, session_id, provider)` with a 10-minute TTL, and verifies/consumes it atomically in `HandleCallback` (`GET /api/v1/integrations/:provider/callback`).
- **Multi-User Shared Account Sync & Cross-DB Ownership Enforcement**:
  - Multiple users in a family budget can connect their own bank credentials (`user_integrations` in `main.db`) to a shared `Account` in `budget_{id}.db`.
  - **IDOR Prevention**: Linking an integration (`POST /budgets/:budgetId/integrations/:integrationId/link-account`) strictly verifies that `user_integrations.user_id == authenticated_user.id`.
  - **Automatic Cascade Unlinking on Member Removal**: Removing a user from `budget_members` (or downgrading them to `VIEWER`) in `main.db` immediately deletes all `account_integration_links` in `budget_{id}.db` referencing that user's `user_integrations`. Furthermore, every sync execution verifies that the integration owner is still an active non-viewer member of the budget before decrypting credentials.
- **CSV Import Bounds**:
  - `POST /api/v1/budgets/:budgetId/accounts/:accountId/import-csv` enforces `http.MaxBytesReader` at **10 MiB**, a maximum of **50,000 rows** per upload, strict UTF-8 validation, and strips control characters from `raw_payee` and `reference`.
- **Canonical Deterministic Deduplication**:
  - To prevent canonical concatenation collisions AND avoid dropping legitimate identical same-day purchases (e.g., buying two identical coffees at `"Żabka"` on the same day), `dedup_hash` is computed over length-prefixed / null-delimited fields including the provider's `external_transaction_id` (when provided by bank APIs) or the 1-based `same_day_sequence` index within the imported statement batch:
    $$\text{dedup\_hash} = \text{SHA256}(\text{len\_prefixed}(\text{account\_id}, \text{external\_tx\_id\_or\_seq}, \text{booking\_date}, \text{amount\_minor}, \text{currency}, \text{clean\_payee}, \text{clean\_reference}))$$
  - Active uniqueness on `(account_id, dedup_hash)` is enforced on `raw_transactions_current` (so soft-deleted raw transactions can be cleanly re-imported if needed), while collisions on active records are safely skipped during sync/import.

### 9.2 Central Bank Exchange Rate Synchronization (ECB & NBP)

```
       +-------------------------------------------------------+
       |           Central Bank Exchange Rate Engine           |
       +-------------------------------------------------------+
       | Priority: European Central Bank (ECB) -> Primary      |
       | Fallback: Narodowy Bank Polski (NBP)   -> Fallback    |
       +-------------------------------------------------------+
                                   |
         +-------------------------+-------------------------+
         |                                                   |
+---------------------------------+         +---------------------------------+
|      ECB Feed Client            |         |      NBP Feed Client            |
+---------------------------------+         +---------------------------------+
| Base: EUR                       |         | Target: PLN                     |
| Daily: eurofxref-daily.xml      |         | Table A: Daily major (~35 curr) |
| History: eurofxref-hist-90d.xml |         | Table B: Weekly exotic          |
+---------------------------------+         +---------------------------------+
                                   |
                                   v
             +-------------------------------------------+
             |         main.db Storage Invariants        |
             +-------------------------------------------+
             | Table: currency_exchange_rates            |
             | - Stores ONLY original published quotes   |
             | - Source & publication date preserved     |
             | - NO inverse or cross pairs in DB         |
             +-------------------------------------------+
                                   |
                                   v
             +-------------------------------------------+
             |    In-Memory Dynamic Calculation Engine   |
             +-------------------------------------------+
             | - Dynamic Inversion: 10^12 / rate_micros  |
             | - Cross-rate Triangulation (via EUR/PLN)  |
             | - Linear Interpolation (weekends/holidays)|
             | - In-progress weekend uses Friday rate    |
             | - Dates older than history -> Strict Error|
             +-------------------------------------------+
```

1. **Central Bank Sourcing & Global Priority Hierarchy**:
   - **European Central Bank (ECB) — Global Primary**: Sourced from official ECB XML reference feeds (`eurofxref-daily.xml` for current rates and `eurofxref-hist-90d.xml` for 90-day history). All ECB quotes have `base_currency = 'EUR'`.
   - **Narodowy Bank Polski (NBP) — Fallback**: Sourced from the official NBP REST API (`https://api.nbp.pl/api/exchangerates/tables/{table}/`). Table A provides daily mid rates for ~35 major currencies; Table B provides weekly mid rates for exotic currencies. All NBP quotes have `target_currency = 'PLN'`.
   - **Conflict Resolution**: If both ECB and NBP publish a rate for the same pair and date, ECB takes global precedence while NBP serves as fallback.
   - **Audit Preservation**: Both `source` (`'ECB'`, `'NBP'`, `'MANUAL'`, `'MOCK'`) and canonical UTC publication timestamp (`rate_date`) are strictly recorded for every rate.

2. **Storage Invariants (`currency_exchange_rates` in `main.db`)**:
   - Rates are stored strictly in the central **`main.db`** (shared across all tenant budgets without duplication).
   - **Original Quotes Only**: The database stores **only** the original published rates from the central banks. Reciprocal inverse pairs (e.g. `PLN -> EUR`) and cross-rate permutations (e.g. `USD -> CHF`) are **never** generated or stored as duplicate rows in SQLite, maintaining zero data redundancy.
   - `rate_micros` is stored as `INTEGER NOT NULL CHECK(rate_micros > 0)` scaled by $1,000,000$ (e.g., $4.321500 \implies 4321500$).

3. **In-Memory Dynamic Calculation, Triangulation & Interpolation**:
   - **Dynamic Inversion**: An inverse rate ($B \to A$) is derived dynamically in Go using 128-bit integer arithmetic:
     $$\text{rate\_micros}(B \to A) = \left\lfloor \frac{10^{12}}{\text{rate\_micros}(A \to B)} \right\rfloor$$
   - **Cross-Rate Triangulation**: When converting between two currencies where neither direct nor inverse pair exists, the engine triangulates via common pivot currencies (EUR or PLN) using 128-bit safe integer math with Round Half Up.
   - **Linear Interpolation for Non-Business Days**: For weekends and bank holidays between two published business days $T_1$ and $T_2$, the rate at date $T$ is computed via deterministic linear interpolation:
     $$\text{Rate}(T) = \text{Rate}(T_1) + \frac{T - T_1}{T_2 - T_1} \cdot (\text{Rate}(T_2) - \text{Rate}(T_1))$$
   - **In-Progress Weekend Rule**: For dates during the current in-progress weekend where Monday's rate has not yet been published by the central banks, the engine uses the latest known business day rate (Friday) until Monday's rate is published.
   - **Strict Error on Missing Past History**: If a transaction date is older than the earliest available historical rate in `currency_exchange_rates` (older than 90 days), the conversion **must immediately fail with an error** (`ErrExchangeRateNotFound`). Silent 1:1 fallbacks for historical financial transactions are strictly prohibited.

4. **Standalone CLI Worker & Scheduled Sync (`balance worker:sync-fx`)**:
   - Executed as a standalone CLI subcommand: `balance worker:sync-fx` (or via `task worker:sync-fx`).
   - Supported CLI flags:
     - `--days=N`: Number of days of history to sync (defaults to 1 day if data exists, or 90 days if `currency_exchange_rates` is empty).
     - `--source=ALL|ECB|NBP`: Sourcing filter (defaults to `ALL` with ECB priority).
     - `--data-dir=PATH`: Path to directory containing `main.db`.
   - Intended for execution once daily via host Linux cron or container scheduler.

5. **REST API Triggers**:
   - `POST /api/v1/exchange-rates/sync`: Allows authenticated clients / admins to trigger synchronization of central bank feeds on demand, returning `{"synced_count": N}`.
   - `GET /api/v1/exchange-rates`: Queries historical time-series from `main.db` with parameters `base`, `target`, `from`, `to`.

---

## 10. Testing Strategy, Heavy-Load Fixtures & Quality Standards

### 10.1 Unit Tests (Algorithmic & Domain Value)
Unit tests (`*_test.go`) focus strictly on domain logic and complex algorithms where they provide high value:
- **Rolling Window Horizon Calculator**: 30d/90d/custom window proration for one-off targets, recurring $N$-day targets, and timezone boundary transitions.
- **Sandboxed Lua Target Formula Engine & AI Assistant**: Evaluation of `LUA_FORMULA` scripts, read-only domain API bindings (`money()`, `target()`, `bucket.balance()`, `fx.rate()`, `budget.income()`), DAG circular dependency detection (`ErrCircularTargetDependency`), VM timeout/sandbox enforcement, and `FormulaAssistantService` validation + 1-retry self-correction pipeline.
- **Target Non-Spending Strategies & Priority Waterfall**: State transitions for `ROLL_OVER`, `RETURN_TO_POOL`, `ARREARS_CUMULATIVE`, `CAP_AND_HOLD`, `PRORATE_FUTURE`, and priority ordering (`priority DESC, due_date ASC, sort_order ASC`).
- **Transaction Resolution Engine**: `DIRECT_MOVE`, `MERGE` (N-to-1), `SPLIT` (1-to-N), and `COMPOSITE` (N-to-M) sum invariants and cross-currency handling.
- **Deduplication & Matching Rules Engine**: SHA256 normalization and priority-based regex/IBAN rule evaluation.
- **CQRS Event Projectors**: Replaying `bucket_events` and `target_events` into deterministic read models.
- **Reporting & Forecasting Algorithms**:
  - **Age of Money FIFO Queue**: Multi-bucket inflow depletion and last-10-outflow weighted day-age calculation.
  - **Monte Carlo Simulation Engine**: Normal distribution vs. Historical Shuffled / Block Bootstrap / Historical Sequences, Guyton-Klinger Guardrails, Kitces Ratcheting, Bengen Floor & Ceiling, progressive tax bands, and bridge-gap pot access age locks.
  - **Balance Forecast & Sankey Graph Builders**: Target/schedule recurrence expansion, lowest-balance trough detection, and Top-$N$ `"Other"` node aggregation.

### 10.2 Mock Integration & AI Providers (`MockAdapter` & `MockFormulaAIProvider`)
- A built-in, deterministic `MOCK` bank integration provider (`internal/integration/mock`) is always compiled and registered in test and dev modes, simulating OAuth/requisition flows, multi-currency bank accounts, and configurable batches of incoming `RawTransaction`s (including split bills, merchant payments like `"ZABKA"`, and duplicate records).
- A deterministic `MockFormulaAIProvider` (`internal/ai/mock_provider.go`) is registered in test/E2E environments when `GEMINI_API_KEY` is not set or `AI_PROVIDER=mock`, allowing full E2E verification of natural-language-to-Lua generation and the automatic self-correction retry flow without external network calls.

### 10.3 End-to-End (E2E) & Integration Tests
- **Playwright E2E Suite**:
  - Runs a real headless browser (across both **Desktop** `1440x900` and **Mobile** `390x844` viewports) against an ephemeral instance of the Echo server with isolated SQLite databases (`main.db` + tenant DBs), `MockAdapter`, and `MockFormulaAIProvider`.
  - Verifies **all happy paths** end-to-end:
    1. User registration (`/register`), login (`/login`), landing on the Budget Selector list (`/budgets`), top navigation bar with logout (`POST /logout`), database session persistence, and locale/format settings.
    2. Budget creation with timezone configuration, selecting a budget to open the YNAB-style **Budget (Buckets)** view (`/budgets/:budgetId/budget`), and verifying the 5-item Left Sidebar Menu (`Budget`, `Transactions`, `Reports`, `External Parties`, `Members`) on desktop and bottom navigation `dock` on mobile.
    3. Managing budget members (`/budgets/:budgetId/members`), connecting the `MOCK` bank integration, and using the **"Sync with Bank"** button and **"Add Manual Transaction"** button in the Transactions view (`/budgets/:budgetId/transactions`).
    4. Creating `Party` and `Account` records in `/budgets/:budgetId/parties` (including multi-currency accounts and external counterparty `"Żabka"` with auto-matching rules).
    5. Executing **Move**, **Merge** (e.g. `-300 + 100 + 100 = -100`), and **Split** operations via HTMX 4 UI.
    6. Configuring `Bucket`s and multi-target goals (including generating a `LUA_FORMULA` via the AI Formula Assistant and verifying its live preview), verifying YNAB-style Bucket Health Status badges (🟢 `Funded`, 🟡 `Underfunded`, 🔴 `Overspent`), allocating funds (manual + Priority Auto-Assign), and switching between Monthly, 30-day, and 90-day rolling views.
    7. Viewing version history of an edited `Account`/`Party`/`Transaction` and restoring a previous version.
    8. Generating all 10 reports (`Age of Money`, `Income v. Expense`, `Spending Trends`, `Spending Breakdown`, `Net Worth`, `Sankey`, `Balance Forecast`, `Budget Analysis`, `Monte Carlo Analysis`, and `Party Cashflow`), saving report widget configurations, and toggling internal transfer exclusion & display currencies.
    9. Verifying Online-Only PWA installability (`/manifest.webmanifest` and pass-through `/sw.js` with `NetworkOnly` strategy and connection-required fallback).
- **Isolated External Integration Tests**:
  - Live/Sandbox tests against real external APIs (e.g., GoCardless Sandbox and live `gemini-3.8-flash` HTTP calls using `GEMINI_API_KEY`) are isolated behind a Go build tag (`//go:build external_integration`) or explicit CLI flag (`go test -tags=external_integration ./...`) so standard CI and local E2E runs remain fast, self-contained, and deterministic.

### 10.4 Heavy-Load Test Fixtures & Performance Benchmarking (10-Year Simulation)
To verify and continuously protect performance as the application evolves, the project includes a dedicated **Heavy Account Fixture Generator** (`internal/testutil/heavyfixture`):

- **Deterministic Seeded Generator + Cached SQLite Snapshot (Locked)**:
  - Uses a fixed PRNG seed (`math/rand/v2` with deterministic ChaCha8/PCG seed) to generate reproducible UUIDv7 sequences, timestamps, amounts, and domain events.
  - Builds a cached SQLite database file (`.cache/fixtures/budget_heavy_10y_{migration_hash}.db`) in batched transactions. Tests copy this pre-built snapshot in milliseconds via filesystem copy-on-write, and automatically regenerate it only when SQL migrations or fixture version constants change.
- **10-Year Heavy Dataset Profile**:
  - **Time Horizon**: 10 years (`3,650` days) of continuous multi-currency activity (`PLN`, `EUR`, `USD`, `JPY`, `GBP`) with daily `currency_exchange_rates`.
  - **Transactions**:
    - `36,500+` `budget_transactions` (~10 transactions/day average, plus historical edits producing `45,000+` total version rows).
    - `40,000+` `raw_transactions` linked via `transaction_links` (`DIRECT_MOVE`, `MERGE`, `SPLIT`, `COMPOSITE`).
  - **Counterparties & Accounts**:
    - `1,000+` external `Party` entities and `1,200+` `Account`s (retail chains like `"Żabka"`, utilities, landlords, employers, contractors, plus `15` user-owned multi-currency accounts).
    - `250+` active `MatchingRule`s (regex, substring, prefix, IBAN).
  - **Buckets, Targets & Event Streams**:
    - `60+` `Bucket`s across `10` `BucketGroup`s.
    - `150+` `Target`s covering one-off milestones, weekly/monthly/annual/custom $N$-day cadences, and all 5 non-spending strategies (`ROLL_OVER`, `RETURN_TO_POOL`, `ARREARS_CUMULATIVE`, `CAP_AND_HOLD`, `PRORATE_FUTURE`).
    - `60,000+` `bucket_events` and `target_events` representing 10 years of allocations and policy adjustments.
- **Continuous Performance Benchmarks (`go test -bench=. ./internal/...` & E2E Load Suite)**:
  - **Rolling Window Horizon Calculation** (30d / 90d / 365d across 60 buckets & 150 targets): target `< 15 ms` P95.
  - **Party Cashflow, Income v. Expense, Spending Trends & Breakdown, Net Worth & Sankey Reports** (over 36,500 transactions and 1,000 parties): target `< 20 ms` P95.
  - **Age of Money FIFO Calculation** (over 36,500 transactions): target `< 15 ms` P95.
  - **Balance Forecast & Budget Analysis Reports** (365-day daily projection across 150 recurring targets): target `< 20 ms` P95.
  - **Monte Carlo Simulation** (`5,000` 50-year multi-pot replays executed in parallel Go goroutines): target `< 50 ms` P95.
  - **Paginated Transaction & Reconciliation Views** (filtering by bucket, party, account, date range): target `< 5 ms` P95.
  - **Auto-Matching Rule Evaluation** (matching batch of 500 imported `RawTransaction`s against 250 rules): target `< 10 ms`.
  - **Full CQRS Event Replay** (rebuilding all projections from 60,000+ events from scratch): target `< 500 ms`.

---

## 11. Comprehensive SQL DDL Schemas

All UUIDs are generated in Golang as UUIDv7 strings (`TEXT NOT NULL`). All timestamps are Unix UTC microseconds (`INTEGER NOT NULL`).

### 11.1 Main Database (`main.db`)

```sql
-- migrations/main/00001_init_main.sql

-- Users
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY, -- UUIDv7
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL, -- Argon2id encoded hash
    display_name TEXT NOT NULL,
    locale TEXT NOT NULL DEFAULT 'en-US',
    date_format TEXT NOT NULL DEFAULT 'YYYY-MM-DD',
    number_format TEXT NOT NULL DEFAULT '1,234.56',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

-- Browser Sessions (Stateful UI Auth - Hashed CSPRNG Token)
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY, -- Internal UUIDv7 row identifier (never sent in cookie)
    token_hash TEXT NOT NULL UNIQUE, -- SHA-256 hex of 256-bit CSPRNG session cookie token
    user_id TEXT NOT NULL,
    active_budget_id TEXT, -- Currently selected budget in UI
    csrf_token TEXT NOT NULL, -- 256-bit CSPRNG token bound to session
    ip_address TEXT,
    user_agent TEXT,
    created_at INTEGER NOT NULL,
    last_active_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

-- Personal Access Tokens for OpenAPI Bearer Authentication
CREATE TABLE IF NOT EXISTS api_tokens (
    id TEXT PRIMARY KEY, -- UUIDv7
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE, -- SHA-256 hex of 'bal_pat_<256-bit-csprng>'
    expires_at INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at INTEGER,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);

-- Budgets Registry
CREATE TABLE IF NOT EXISTS budgets (
    id TEXT PRIMARY KEY, -- UUIDv7
    name TEXT NOT NULL,
    default_currency TEXT NOT NULL, -- ISO 4217 e.g. 'PLN'
    timezone TEXT NOT NULL DEFAULT 'UTC', -- IANA timezone e.g. 'Europe/Warsaw'
    created_by_agent_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (created_by_agent_id) REFERENCES users(id) ON DELETE RESTRICT
);

-- Budget Memberships (Multi-user access with unified RBAC roles)
CREATE TABLE IF NOT EXISTS budget_members (
    budget_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('OWNER', 'ADMIN', 'EDITOR', 'VIEWER')),
    joined_at INTEGER NOT NULL,
    PRIMARY KEY (budget_id, user_id),
    FOREIGN KEY (budget_id) REFERENCES budgets(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Budget Invitations (Opt-in token-based invitation workflow)
CREATE TABLE IF NOT EXISTS budget_invitations (
    id TEXT PRIMARY KEY, -- UUIDv7
    budget_id TEXT NOT NULL,
    invited_email TEXT NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('ADMIN', 'EDITOR', 'VIEWER')),
    token_hash TEXT NOT NULL UNIQUE, -- SHA-256 hex of 256-bit CSPRNG invite token
    invited_by_user_id TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    accepted_at INTEGER,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (budget_id) REFERENCES budgets(id) ON DELETE CASCADE,
    FOREIGN KEY (invited_by_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_budget_invitations_budget ON budget_invitations(budget_id);
CREATE INDEX IF NOT EXISTS idx_budget_invitations_email ON budget_invitations(invited_email);

-- Extensible Bank Integrations (Per User)
CREATE TABLE IF NOT EXISTS user_integrations (
    id TEXT PRIMARY KEY, -- UUIDv7
    user_id TEXT NOT NULL,
    provider TEXT NOT NULL, -- Extensible: 'GOCARDLESS', 'CSV', 'MOCK', 'PLAID', 'ENABLE_BANKING', etc.
    institution_id TEXT,
    institution_name TEXT,
    external_reference_id TEXT, -- e.g. GoCardless requisition ID
    encrypted_credentials TEXT, -- AES-256-GCM / XChaCha20-Poly1305 payload with AAD=(user_id:id:provider)
    status TEXT NOT NULL CHECK(status IN ('ACTIVE', 'EXPIRED', 'ERROR', 'PENDING')),
    expires_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_user_integrations_user ON user_integrations(user_id);
CREATE INDEX IF NOT EXISTS idx_user_integrations_provider ON user_integrations(provider);

-- Ephemeral OAuth / Bank Requisition State Tokens (CSRF Protection on Callbacks)
CREATE TABLE IF NOT EXISTS integration_oauth_states (
    state_hash TEXT PRIMARY KEY, -- SHA-256 hex of 256-bit CSPRNG state parameter
    user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_oauth_states_expires ON integration_oauth_states(expires_at);

-- Global Currency Exchange Rates (Historical & Current Time-Series)
CREATE TABLE IF NOT EXISTS currency_exchange_rates (
    id TEXT PRIMARY KEY, -- UUIDv7
    base_currency TEXT NOT NULL, -- ISO 4217 e.g. 'EUR'
    target_currency TEXT NOT NULL, -- ISO 4217 e.g. 'PLN'
    rate_date INTEGER NOT NULL, -- UTC day boundary timestamp (microseconds)
    rate_micros INTEGER NOT NULL CHECK(rate_micros > 0), -- Rate scaled by 1,000,000 (e.g. 4.321500 = 4321500)
    source TEXT NOT NULL, -- 'NBP', 'ECB', 'MANUAL', 'MOCK'
    created_at INTEGER NOT NULL,
    UNIQUE (base_currency, target_currency, rate_date)
);

CREATE INDEX IF NOT EXISTS idx_fx_rates_lookup 
    ON currency_exchange_rates(base_currency, target_currency, rate_date DESC);
```

### 11.2 Tenant Database (`budget_{id}.db`)

```sql
-- migrations/tenant/00001_init_tenant.sql

-- 1. Parties (Versioned, Append-Only: Individuals & Organizations, User-Owned & External)
CREATE TABLE IF NOT EXISTS parties (
    id TEXT NOT NULL, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL, -- e.g. 'Jan Kowalski', 'Żabka', 'Employer LLC'
    party_type TEXT NOT NULL CHECK(party_type IN ('INDIVIDUAL', 'ORGANIZATION')),
    is_user_owned INTEGER NOT NULL DEFAULT 0, -- 1 = belongs to budget member(s), 0 = external counterparty
    notes TEXT,
    action TEXT NOT NULL CHECK(action IN ('CREATE', 'UPDATE', 'RESTORE', 'SOFT_DELETE')),
    agent_id TEXT NOT NULL, -- UUIDv7
    correlation_id TEXT NOT NULL, -- Server-generated UUIDv7
    created_at INTEGER NOT NULL, -- UTC microseconds
    PRIMARY KEY (id, version)
);

CREATE INDEX IF NOT EXISTS idx_parties_id_version ON parties(id, version DESC);
CREATE INDEX IF NOT EXISTS idx_parties_user_owned ON parties(is_user_owned);

-- 2. Accounts (Versioned, Append-Only, Multi-Currency Capable, Linked to Party)
CREATE TABLE IF NOT EXISTS accounts (
    id TEXT NOT NULL, -- UUIDv7
    version INTEGER NOT NULL,
    party_id TEXT NOT NULL, -- Owning Party UUIDv7 (every account belongs to a Party)
    name TEXT NOT NULL, -- e.g. 'mBank Multi-Currency', 'Wallet Cash', 'Żabka POS'
    account_type TEXT NOT NULL CHECK(account_type IN (
        'CHECKING', 
        'SAVINGS', 
        'CREDIT_CARD', 
        'CASH', 
        'INVESTMENT', 
        'EXTERNAL' -- Counterparty account (IBAN or virtual merchant account)
    )),
    primary_currency TEXT NOT NULL DEFAULT 'PLN', -- Default UI currency; account can hold any currency
    iban TEXT, -- Optional IBAN or external identifier
    is_closed INTEGER NOT NULL DEFAULT 0,
    action TEXT NOT NULL CHECK(action IN ('CREATE', 'UPDATE', 'RESTORE', 'SOFT_DELETE')),
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (id, version)
);

CREATE INDEX IF NOT EXISTS idx_accounts_id_version ON accounts(id, version DESC);
CREATE INDEX IF NOT EXISTS idx_accounts_party ON accounts(party_id);
CREATE INDEX IF NOT EXISTS idx_accounts_iban ON accounts(iban);

-- Multi-Currency Sub-Balances per Account (Read Projection / Materialized Ledger Balance)
CREATE TABLE IF NOT EXISTS account_currency_balances (
    account_id TEXT NOT NULL,
    currency TEXT NOT NULL, -- ISO 4217 ('PLN', 'EUR', 'USD', 'JPY')
    balance_minor INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (account_id, currency)
);

-- Account to Integration Mappings (Multiple users can link integrations to the same shared account)
CREATE TABLE IF NOT EXISTS account_integration_links (
    id TEXT PRIMARY KEY, -- UUIDv7
    account_id TEXT NOT NULL,
    integration_id TEXT NOT NULL, -- References main.db user_integrations.id (verified against user_id & budget_members)
    external_account_id TEXT NOT NULL, -- Provider's account ID or IBAN
    sync_cursor TEXT,
    last_synced_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (account_id, integration_id, external_account_id)
);

-- 3. Matching Rules (Versioned, Append-Only: Auto-assigning External Account/Party & Bucket)
CREATE TABLE IF NOT EXISTS matching_rules (
    id TEXT NOT NULL, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL, -- e.g. 'Żabka Stores'
    priority INTEGER NOT NULL DEFAULT 100,
    match_field TEXT NOT NULL CHECK(match_field IN ('RAW_PAYEE', 'COUNTERPARTY_IBAN', 'REFERENCE', 'ANY')),
    match_operator TEXT NOT NULL CHECK(match_operator IN ('CONTAINS', 'PREFIX', 'EXACT', 'REGEX')),
    match_pattern TEXT NOT NULL CHECK(length(match_pattern) >= 1 AND length(match_pattern) <= 256), -- RE2 validated, max 256 chars
    target_counterparty_account_id TEXT NOT NULL, -- Maps to the external Account (and its Party)
    default_bucket_id TEXT, -- Optional suggested Bucket
    action TEXT NOT NULL CHECK(action IN ('CREATE', 'UPDATE', 'RESTORE', 'SOFT_DELETE')),
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (id, version)
);

CREATE INDEX IF NOT EXISTS idx_matching_rules_id_ver ON matching_rules(id, version DESC);

-- 4. Raw Transactions (External Truth from Integrations/CSV, Append-Only)
CREATE TABLE IF NOT EXISTS raw_transactions (
    id TEXT NOT NULL, -- UUIDv7
    version INTEGER NOT NULL,
    account_id TEXT NOT NULL, -- The user's account where the transaction was imported
    external_transaction_id TEXT, -- Optional provider-assigned unique transaction ID
    dedup_hash TEXT NOT NULL, -- Canonical SHA256 over length-prefixed fields
    booking_date INTEGER NOT NULL, -- UTC microseconds
    value_date INTEGER,
    amount_minor INTEGER NOT NULL CHECK(amount_minor >= -100000000000000 AND amount_minor <= 100000000000000 AND amount_minor != 0),
    currency TEXT NOT NULL,
    raw_payee TEXT NOT NULL,
    counterparty_iban TEXT,
    reference TEXT,
    status TEXT NOT NULL CHECK(status IN ('PENDING', 'PROCESSED', 'IGNORED')),
    action TEXT NOT NULL CHECK(action IN ('CREATE', 'UPDATE', 'RESTORE', 'SOFT_DELETE')),
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (id, version)
);

CREATE INDEX IF NOT EXISTS idx_raw_transactions_dedup 
    ON raw_transactions(account_id, dedup_hash);
CREATE INDEX IF NOT EXISTS idx_raw_transactions_id_ver 
    ON raw_transactions(id, version DESC);
CREATE INDEX IF NOT EXISTS idx_raw_transactions_status 
    ON raw_transactions(status);

-- 5. Budget Transactions (Budgetary Truth, Strict Double-Entry Account-to-Account, Append-Only)
CREATE TABLE IF NOT EXISTS budget_transactions (
    id TEXT NOT NULL, -- UUIDv7
    version INTEGER NOT NULL,
    source_account_id TEXT NOT NULL, -- Account money leaves (e.g. User Checking for expense, Employer for income)
    destination_account_id TEXT NOT NULL, -- Account money enters (e.g. 'Żabka POS' for expense, User Checking for income)
    source_amount_minor INTEGER NOT NULL CHECK(source_amount_minor > 0 AND source_amount_minor <= 100000000000000),
    source_currency TEXT NOT NULL,
    destination_amount_minor INTEGER NOT NULL CHECK(destination_amount_minor > 0 AND destination_amount_minor <= 100000000000000),
    destination_currency TEXT NOT NULL,
    bucket_id TEXT, -- Assigned Bucket (NULL for pure internal transfers)
    target_id TEXT, -- Optional direct Target attribution within the Bucket
    transaction_date INTEGER NOT NULL, -- UTC microseconds
    notes TEXT,
    action TEXT NOT NULL CHECK(action IN ('CREATE', 'UPDATE', 'RESTORE', 'SOFT_DELETE')),
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (id, version),
    CHECK (source_account_id != destination_account_id OR source_currency != destination_currency)
);

CREATE INDEX IF NOT EXISTS idx_budget_tx_id_ver ON budget_transactions(id, version DESC);
CREATE INDEX IF NOT EXISTS idx_budget_tx_source ON budget_transactions(source_account_id, transaction_date);
CREATE INDEX IF NOT EXISTS idx_budget_tx_dest ON budget_transactions(destination_account_id, transaction_date);
CREATE INDEX IF NOT EXISTS idx_budget_tx_bucket ON budget_transactions(bucket_id, transaction_date);

-- 6. Transaction Links (N-to-M Audit Mapping between Raw and Budget Transactions)
CREATE TABLE IF NOT EXISTS transaction_links (
    operation_id TEXT NOT NULL, -- UUIDv7 grouping this Move / Merge / Split operation
    operation_type TEXT NOT NULL CHECK(operation_type IN ('DIRECT_MOVE', 'MERGE', 'SPLIT', 'COMPOSITE')),
    raw_transaction_id TEXT NOT NULL,
    budget_transaction_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (operation_id, raw_transaction_id, budget_transaction_id)
);

CREATE INDEX IF NOT EXISTS idx_tx_links_raw ON transaction_links(raw_transaction_id);
CREATE INDEX IF NOT EXISTS idx_tx_links_budget ON transaction_links(budget_transaction_id);

-- 7. Event Store: Buckets & Targets (Event Sourcing)
CREATE TABLE IF NOT EXISTS bucket_events (
    event_id TEXT PRIMARY KEY, -- UUIDv7
    aggregate_id TEXT NOT NULL, -- Bucket UUIDv7
    version INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL, -- JSON
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    UNIQUE (aggregate_id, version)
);

CREATE INDEX IF NOT EXISTS idx_bucket_events_agg ON bucket_events(aggregate_id, version ASC);

CREATE TABLE IF NOT EXISTS target_events (
    event_id TEXT PRIMARY KEY, -- UUIDv7
    aggregate_id TEXT NOT NULL, -- Target UUIDv7
    bucket_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL, -- JSON
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    UNIQUE (aggregate_id, version)
);

CREATE INDEX IF NOT EXISTS idx_target_events_agg ON target_events(aggregate_id, version ASC);
CREATE INDEX IF NOT EXISTS idx_target_events_bucket ON target_events(bucket_id);

-- 8. CQRS Projections (Synchronously Updated Read Models)
CREATE TABLE IF NOT EXISTS buckets_projection (
    id TEXT PRIMARY KEY, -- Bucket UUIDv7
    group_name TEXT NOT NULL DEFAULT 'Default',
    name TEXT NOT NULL,
    is_archived INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS bucket_currency_balances (
    bucket_id TEXT NOT NULL,
    currency TEXT NOT NULL,
    balance_minor INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_id, currency),
    FOREIGN KEY (bucket_id) REFERENCES buckets_projection(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS targets_projection (
    id TEXT PRIMARY KEY, -- Target UUIDv7
    bucket_id TEXT NOT NULL,
    name TEXT NOT NULL,
    target_type TEXT NOT NULL CHECK(target_type IN ('EXPENSE', 'SAVINGS', 'DEBT_PAYMENT')),
    priority INTEGER NOT NULL DEFAULT 2 CHECK(priority >= 1), -- 1 = Optional, 2 = Recommended, 3 = Mandatory, higher = more important
    sort_order INTEGER NOT NULL DEFAULT 0, -- Tie-breaker order within bucket
    amount_mode TEXT NOT NULL DEFAULT 'FIXED' CHECK(amount_mode IN ('FIXED', 'LUA_FORMULA')),
    amount_minor INTEGER NOT NULL DEFAULT 0 CHECK(amount_minor >= 0 AND amount_minor <= 100000000000000), -- Static amount for FIXED, or cached last-evaluated minor amount for LUA_FORMULA
    lua_script TEXT CHECK(lua_script IS NULL OR length(lua_script) <= 2048), -- Sandboxed Lua script when amount_mode = 'LUA_FORMULA'
    last_formula_error TEXT, -- Populated on runtime formula error (Graceful Degradation)
    currency TEXT NOT NULL,
    recurrence_cadence TEXT NOT NULL CHECK(recurrence_cadence IN ('ONE_OFF', 'WEEKLY', 'MONTHLY', 'ANNUAL', 'CUSTOM_DAYS')),
    recurrence_days INTEGER CHECK(recurrence_cadence != 'CUSTOM_DAYS' OR (recurrence_days IS NOT NULL AND recurrence_days >= 1 AND recurrence_days <= 36500)),
    due_date INTEGER,
    non_spending_strategy TEXT NOT NULL CHECK(non_spending_strategy IN (
        'ROLL_OVER', 
        'RETURN_TO_POOL', 
        'ARREARS_CUMULATIVE', 
        'CAP_AND_HOLD', 
        'PRORATE_FUTURE'
    )),
    ceiling_amount_minor INTEGER CHECK(ceiling_amount_minor IS NULL OR (ceiling_amount_minor >= 0 AND ceiling_amount_minor <= 100000000000000)),
    current_funded_minor INTEGER NOT NULL DEFAULT 0,
    is_archived INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (bucket_id) REFERENCES buckets_projection(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_targets_proj_bucket ON targets_projection(bucket_id);
CREATE INDEX IF NOT EXISTS idx_targets_proj_priority ON targets_projection(priority DESC, due_date ASC, sort_order ASC);

-- 9. Synchronous *_current Read Projections for Append-Only Tables (10-Year Scale Optimization)
CREATE TABLE IF NOT EXISTS parties_current (
    id TEXT PRIMARY KEY, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL,
    party_type TEXT NOT NULL,
    is_user_owned INTEGER NOT NULL,
    notes TEXT,
    agent_id TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_parties_curr_owned ON parties_current(is_user_owned);

CREATE TABLE IF NOT EXISTS accounts_current (
    id TEXT PRIMARY KEY, -- UUIDv7
    version INTEGER NOT NULL,
    party_id TEXT NOT NULL,
    name TEXT NOT NULL,
    account_type TEXT NOT NULL,
    primary_currency TEXT NOT NULL,
    iban TEXT,
    is_closed INTEGER NOT NULL,
    agent_id TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (party_id) REFERENCES parties_current(id)
);

CREATE INDEX IF NOT EXISTS idx_accounts_curr_party ON accounts_current(party_id);

CREATE TABLE IF NOT EXISTS matching_rules_current (
    id TEXT PRIMARY KEY, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL,
    priority INTEGER NOT NULL,
    match_field TEXT NOT NULL,
    match_operator TEXT NOT NULL,
    match_pattern TEXT NOT NULL,
    target_counterparty_account_id TEXT NOT NULL,
    default_bucket_id TEXT,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_matching_rules_curr_prio ON matching_rules_current(priority ASC);

CREATE TABLE IF NOT EXISTS raw_transactions_current (
    id TEXT PRIMARY KEY, -- UUIDv7
    version INTEGER NOT NULL,
    account_id TEXT NOT NULL,
    external_transaction_id TEXT,
    dedup_hash TEXT NOT NULL,
    booking_date INTEGER NOT NULL,
    value_date INTEGER,
    amount_minor INTEGER NOT NULL,
    currency TEXT NOT NULL,
    raw_payee TEXT NOT NULL,
    counterparty_iban TEXT,
    reference TEXT,
    status TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_raw_tx_curr_dedup ON raw_transactions_current(account_id, dedup_hash);
CREATE INDEX IF NOT EXISTS idx_raw_tx_curr_status_date ON raw_transactions_current(status, booking_date DESC);
CREATE INDEX IF NOT EXISTS idx_raw_tx_curr_account ON raw_transactions_current(account_id, booking_date DESC);

CREATE TABLE IF NOT EXISTS budget_transactions_current (
    id TEXT PRIMARY KEY, -- UUIDv7
    version INTEGER NOT NULL,
    source_account_id TEXT NOT NULL,
    destination_account_id TEXT NOT NULL,
    source_amount_minor INTEGER NOT NULL,
    source_currency TEXT NOT NULL,
    destination_amount_minor INTEGER NOT NULL,
    destination_currency TEXT NOT NULL,
    bucket_id TEXT,
    target_id TEXT,
    transaction_date INTEGER NOT NULL,
    notes TEXT,
    updated_at INTEGER NOT NULL,
    CHECK (source_account_id != destination_account_id OR source_currency != destination_currency)
);

CREATE INDEX IF NOT EXISTS idx_budget_tx_curr_date ON budget_transactions_current(transaction_date DESC);
CREATE INDEX IF NOT EXISTS idx_budget_tx_curr_source ON budget_transactions_current(source_account_id, transaction_date DESC);
CREATE INDEX IF NOT EXISTS idx_budget_tx_curr_dest ON budget_transactions_current(destination_account_id, transaction_date DESC);
CREATE INDEX IF NOT EXISTS idx_budget_tx_curr_bucket ON budget_transactions_current(bucket_id, transaction_date DESC);

-- 10. Saved Reports & Monte Carlo Plans (Append-Only Versioned + Current Projection)
CREATE TABLE IF NOT EXISTS saved_reports (
    id TEXT NOT NULL, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL,
    report_type TEXT NOT NULL CHECK(report_type IN (
        'AGE_OF_MONEY',
        'INCOME_VS_EXPENSE',
        'SPENDING_TRENDS',
        'SPENDING_BREAKDOWN',
        'NET_WORTH',
        'SANKEY',
        'BALANCE_FORECAST',
        'BUDGET_ANALYSIS',
        'MONTE_CARLO',
        'PARTY_CASHFLOW'
    )),
    config_json TEXT NOT NULL CHECK(length(config_json) <= 65536), -- Filters, date range mode, Monte Carlo pots/phases/tax config (max 64 KiB)
    is_pinned_widget INTEGER NOT NULL DEFAULT 0,
    action TEXT NOT NULL CHECK(action IN ('CREATE', 'UPDATE', 'RESTORE', 'SOFT_DELETE')),
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (id, version)
);

CREATE INDEX IF NOT EXISTS idx_saved_reports_id_ver ON saved_reports(id, version DESC);

CREATE TABLE IF NOT EXISTS saved_reports_current (
    id TEXT PRIMARY KEY, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL,
    report_type TEXT NOT NULL,
    config_json TEXT NOT NULL CHECK(length(config_json) <= 65536),
    is_pinned_widget INTEGER NOT NULL,
    agent_id TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
```

---

## 12. OpenAPI 3.1 REST & HTMX Web Routes

Every request passes through:
1. `CorrelationIDMiddleware` (assigning a strictly server-generated UUIDv7 `correlation_id` for database audit records and setting the `X-Correlation-ID` response header),
2. `SessionOrBearerAuthMiddleware` (verifying either a hashed browser session cookie + `X-CSRF-Token` on mutating requests, OR a hashed `Authorization: Bearer bal_pat_...` Personal Access Token),
3. `TenantAuthzMiddleware` (for `/budgets/:budgetId/...` and `/api/v1/budgets/:budgetId/...`, validating `:budgetId` as UUIDv7 and enforcing the user's `budget_members.role` against the route's minimum required role before opening `budget_{id}.db`).

### 12.1 HTMX 4 / Templ Web UI & PWA Routes (Session Auth + CSRF)
- **Authentication & Top Bar**:
  - `GET /login`, `POST /login` - Login screen & rate-limited session creation in `main.db`.
  - `GET /register`, `POST /register` - User registration screen & automatic session login.
  - `POST /logout` - Top-bar logout action (invalidates session in `main.db` and redirects to `/login`).
- **Post-Login Budget Selector & Invitations**:
  - `GET /budgets`, `POST /budgets` - Post-login landing page listing all user budgets with a top bar (user menu + logout), pending invitations banner, and modal/form to create a new budget.
  - `POST /invitations/:token/accept` & `POST /invitations/:token/decline` - Explicit opt-in acceptance or rejection of a budget invitation.
- **Active Budget Workspace (Left Sidebar on Desktop / Bottom `dock` on Mobile)**:
  1. **Budget (Buckets / Envelopes)**:
     - `GET /budgets/:budgetId/budget` (aliased from `/budgets/:budgetId` and `/budgets/:budgetId/dashboard`) - YNAB-style Bucket & Group view with **Unallocated Pool ("Ready to Assign")**, Bucket Health Status indicators (🟢 `Funded`, 🟡 `Underfunded`, 🔴 `Overspent`), Rolling Horizon switcher (`Monthly` / `30d` / `90d` / `Custom`), Priority Auto-Assign (`POST /budgets/:budgetId/budget/auto-assign`), and right-hand Bucket/Target inspector drawer.
     - `POST /budgets/:budgetId/targets/generate-formula` - Rate-limited HTMX endpoint (`EDITOR`+ role required) for the Google Sheets-style AI Formula Assistant (`gemini-3.8-flash` + 6-Pillar Sandbox dry-run + 1 retry).
     - `POST /budgets/:budgetId/targets/validate-formula` - HTMX live dry-run endpoint (`EDITOR`+ role required) for manual Lua formula edits.
  2. **Transactions (Transactions & Reconciliation)**:
     - `GET /budgets/:budgetId/transactions` - Unified transactions ledger with the two primary toolbar buttons:
       - **🔄 Sync with Bank**: `POST /budgets/:budgetId/transactions/sync` (`EDITOR`+ role required; triggers bank integration sync + auto-matching rules).
       - **➕ Add Manual Transaction**: `GET /budgets/:budgetId/transactions/new` & `POST /budgets/:budgetId/transactions` (`EDITOR`+ role required; inline YNAB-style rapid transaction entry / mobile bottom sheet).
     - `GET /budgets/:budgetId/reconcile` - Two-pane Move / Merge / Split workbench (`RawTransaction`s $\rightarrow$ `BudgetTransaction`s with live rule matching and on-the-fly Party/Account creation modal).
  3. **Reports**:
     - `GET /budgets/:budgetId/reports` - Customizable Reports Dashboard with widget cards and full-screen interactive views powered by `a-h/templ`, HTMX 4, and **Apache ECharts**.
     - `GET /budgets/:budgetId/reports/:reportType` - Full interactive report view (`age-of-money`, `income-vs-expense`, `spending-trends`, `spending-breakdown`, `net-worth`, `sankey`, `balance-forecast`, `budget-analysis`, `monte-carlo`, `party-cashflow`).
  4. **External Parties (External Parties & Counterparty Accounts)**:
     - `GET /budgets/:budgetId/parties`, `POST /budgets/:budgetId/parties` - External Parties & Counterparty Accounts directory, Matching Rules management, and Party Cashflow summary (e.g. total spent at `"Żabka"`).
  5. **Members (Members, Invitations & Integrations)**:
     - `GET /budgets/:budgetId/members`, `POST /budgets/:budgetId/members/invite` - Budget members list, role management (`OWNER`, `ADMIN`, `EDITOR`, `VIEWER`), token-based user invitations (`budget_invitations`), and connected bank integration accounts.
- **Audit History Drawer**:
  - `GET /budgets/:budgetId/history/:entityType/:entityId` - Version history drawer with one-click `hx-post` version restoration (`ADMIN`+ role required).
- **Online-Only PWA Endpoints**:
  - `GET /manifest.webmanifest` - Web App Manifest (`display: "standalone"`, `start_url: "/budgets"`, icons).
  - `GET /sw.js` - Pass-through `NetworkOnly` Service Worker enabling PWA installation on mobile/desktop without offline financial data caching (renders a static connection-required banner when offline).

### 12.2 OpenAPI 3.1 Endpoints (`/api/v1/...`, Documented at `/api/docs`)
- **Auth, Sessions & Personal Access Tokens**:
  - `POST /api/v1/auth/register`
  - `POST /api/v1/auth/login`
  - `POST /api/v1/auth/logout`
  - `GET /api/v1/auth/tokens`, `POST /api/v1/auth/tokens`, `DELETE /api/v1/auth/tokens/:id` (Manage Personal Access Tokens for `Bearer` API access)
  - `GET /api/v1/users/me` & `PUT /api/v1/users/me`
- **Global Exchange Rates (`main.db`)**:
  - `GET /api/v1/exchange-rates` (Query historical time-series by `base`, `target`, `from`, `to`)
  - `POST /api/v1/exchange-rates/sync` (Rate-limited trigger of NBP/ECB feed sync into `main.db`; only fetches from trusted upstream central bank feeds)
- **Budgets & Invitations**:
  - `GET /api/v1/budgets` & `POST /api/v1/budgets`
  - `PATCH /api/v1/budgets/:budgetId` (`OWNER` role required: update timezone, name, member roles)
  - `POST /api/v1/budgets/:budgetId/invitations` (`ADMIN`+ role required) & `POST /api/v1/invitations/:token/accept`
- **Integrations (Pluggable Providers: GoCardless, CSV, Mock, etc.)**:
  - `GET /api/v1/integrations/providers`
  - `POST /api/v1/integrations/:provider/connect` (Generates session-bound `state` in `integration_oauth_states`)
  - `GET /api/v1/integrations/:provider/callback` (Verifies & consumes `state` before saving AEAD-encrypted credentials)
  - `POST /api/v1/budgets/:budgetId/integrations/:integrationId/link-account` (`EDITOR`+ role AND `user_integrations.user_id == current_user.id` required)
  - `POST /api/v1/budgets/:budgetId/integrations/:integrationId/sync` (`EDITOR`+ role required; verifies integration owner is an active budget member)
  - `POST /api/v1/budgets/:budgetId/accounts/:accountId/import-csv` (`EDITOR`+ role required; max 10 MiB / 50,000 rows)
- **Parties, Accounts & Matching Rules**:
  - `GET /api/v1/budgets/:budgetId/parties` & `POST /api/v1/budgets/:budgetId/parties`
  - `PUT /api/v1/budgets/:budgetId/parties/:id`
  - `GET /api/v1/budgets/:budgetId/parties/:id/cashflow` (Query params: `from`, `to`, `exclude_internal=true`)
  - `GET /api/v1/budgets/:budgetId/accounts` & `POST /api/v1/budgets/:budgetId/accounts`
  - `GET /api/v1/budgets/:budgetId/matching-rules` & `POST /api/v1/budgets/:budgetId/matching-rules`
  - `GET /api/v1/budgets/:budgetId/:entityType/:id/history` & `POST /api/v1/budgets/:budgetId/:entityType/:id/restore` (`ADMIN`+ role required)
- **Transactions & Resolution (Move, Merge, Split)**:
  - `GET /api/v1/budgets/:budgetId/raw-transactions`
  - `GET /api/v1/budgets/:budgetId/transactions`
  - `POST /api/v1/budgets/:budgetId/transactions` (Direct creation for Cash / non-integrated accounts)
  - `POST /api/v1/budgets/:budgetId/transactions/resolve`
    ```json
    {
      "operation_type": "MERGE",
      "raw_transaction_ids": [
        "018d0000-0000-7000-8000-000000000001",
        "018d0000-0000-7000-8000-000000000002",
        "018d0000-0000-7000-8000-000000000003"
      ],
      "budget_transactions": [
        {
          "source_account_id": "018d0000-0000-7000-8000-000000000010",
          "destination_account_id": "018d0000-0000-7000-8000-000000000030",
          "source_amount_minor": 10000,
          "source_currency": "PLN",
          "destination_amount_minor": 10000,
          "destination_currency": "PLN",
          "bucket_id": "018d0000-0000-7000-8000-000000000020",
          "target_id": null,
          "transaction_date": 1773705600000000,
          "notes": "Dinner split net expense"
        }
      ]
    }
    ```
- **Buckets, Targets, AI Formula Assistant & Rolling Horizon View**:
  - `GET /api/v1/budgets/:budgetId/buckets` & `POST /api/v1/budgets/:budgetId/buckets`
  - `POST /api/v1/budgets/:budgetId/buckets/:id/targets`
  - `POST /api/v1/budgets/:budgetId/targets/generate-formula` (`EDITOR`+ role required, rate-limited: Natural language $\rightarrow$ validated Lua formula + live preview via `FormulaAIProvider`)
  - `POST /api/v1/budgets/:budgetId/targets/validate-formula` (`EDITOR`+ role required: AST/DAG validation + sandbox dry-run evaluation)
  - `POST /api/v1/budgets/:budgetId/targets/:id/fund`
  - `GET /api/v1/budgets/:budgetId/view/horizon?days=90`
- **Reporting, Analytics & Forecasting Endpoints**:
  - `GET /api/v1/budgets/:budgetId/reports/saved` & `POST /api/v1/budgets/:budgetId/reports/saved`
  - `PUT /api/v1/budgets/:budgetId/reports/saved/:id`
  - `GET /api/v1/budgets/:budgetId/reports/age-of-money`
  - `GET /api/v1/budgets/:budgetId/reports/income-vs-expense`
  - `GET /api/v1/budgets/:budgetId/reports/spending-trends`
  - `GET /api/v1/budgets/:budgetId/reports/spending-breakdown`
  - `GET /api/v1/budgets/:budgetId/reports/net-worth`
  - `GET /api/v1/budgets/:budgetId/reports/sankey`
  - `GET /api/v1/budgets/:budgetId/reports/balance-forecast`
  - `GET /api/v1/budgets/:budgetId/reports/budget-analysis`
  - `POST /api/v1/budgets/:budgetId/reports/monte-carlo/simulate`
  - `GET /api/v1/budgets/:budgetId/reports/party-cashflow`

---

## 13. Comprehensive Reporting, Analytics & Forecasting Engine

All reports are computed on the **Go backend** against the `*_current` and CQRS read-model tables, exposed via OpenAPI 3.1 JSON endpoints, and rendered in the browser using `a-h/templ` + **HTMX 4** + **Apache ECharts**.

### 13.1 Cross-Cutting Report Capabilities
1. **Multi-Currency Slicing & Global Historical Exchange Rates in `main.db` (Locked)**:
   - **Centralized Historical Storage**: All current and historical currency exchange rates (`currency_exchange_rates`) are stored in **`main.db`** so they are shared across all tenant budgets without data duplication and cached in-memory by the Go application for $O(1)$ nanosecond lookups.
   - **Single-Currency Slice Mode**: Filter report strictly to one currency (e.g., `currency=PLN` or `currency=EUR`) without conversion.
   - **Aggregated Display Currency Mode**: Convert and sum all multi-currency balances and transactions into a single selected `display_currency` (defaulting to the budget's `default_currency`) using 128-bit safe integer micro-rates (`rate_micros`):
     - **Historical Rate Mode (Default)**: Flow reports (`Income v. Expense`, `Spending Trends`, `Spending Breakdown`, `Sankey`, `Party Cashflow`) convert each transaction using the historical rate from its `transaction_date` (evaluated via linear interpolation between bounding business days for weekends/holidays, or the latest Friday rate for the in-progress weekend; returning a strict error if the date precedes available history), while balance reports (`Net Worth`, `Budget Analysis`) value each period's ending balance using the rate at the end of that period.
     - **Latest Rate Mode (Toggleable)**: Optionally evaluates all historical series using the latest current exchange rate to eliminate FX fluctuation noise when analyzing pure spending volume trends.
2. **Unified Filter Engine**:
   - Every report supports filtering by:
     - **Date Range**: Static (`start_date`..`end_date`, bounded to max `100` years) or Live Rolling Window (`30d`, `90d`, `180d`, `YTD`, `1y`, `all`).
     - **Dimensions**: `account_ids[]`, `party_ids[]`, `bucket_group_names[]`, `bucket_ids[]`, `target_ids[]`.
     - **Internal Transfer Exclusion (`exclude_internal=true` by default)**: Automatically excludes transactions where both `source_account_id` and `destination_account_id` belong to Parties with `is_user_owned = 1`.
3. **Saved Report Widgets (`saved_reports`)**:
   - Users can save configured filters, view modes, and complex Monte Carlo retirement plans as reusable widgets on the Reports Dashboard (append-only versioned with full history, `config_json <= 64 KiB`).

---

### 13.2 YNAB-Inspired Core Reports

#### 1. Age of Money (`AGE_OF_MONEY`)
- **Concept**: Measures how many days ago the money you are spending today was earned (inspired by YNAB's Age of Money).
- **FIFO Queue Algorithm**:
  - Scope: Liquid user-owned accounts (`account_type IN ('CHECKING', 'SAVINGS', 'CASH')` where owning Party has `is_user_owned = 1`).
  - All inflows from external parties (`source_account.is_user_owned = 0`) are pushed into a chronological **FIFO Queue** of income buckets: $(T_{\text{inflow}}, \text{RemainingAmount})$.
  - Each outflow to an external party (`destination_account.is_user_owned = 0`) consumes money from the oldest unspent income bucket(s) at the head of the queue.
  - The age of an outflow transaction at $T_{\text{outflow}}$ is the weighted average of $(T_{\text{outflow}} - T_{\text{inflow}})$ in days across the income bucket slice(s) it depleted.
  - **Headline Metric**: The arithmetic mean of the ages of the **last 10 liquid outflow transactions** (in days), accompanied by an ECharts time-series line graph showing how Age of Money evolved day-by-day/month-by-month.

#### 2. Income v. Expense (`INCOME_VS_EXPENSE`)
- **Concept**: Detailed P&L matrix across time intervals (monthly, weekly, or custom rolling intervals).
- **Structure**:
  - **Income Section**: Grouped by external `Party` (e.g., `"Employer Inc."`, `"B2B Client"`), showing inflows per period, row `Average`, and row `Total`.
  - **Expense Section**: Grouped by `BucketGroup` $\rightarrow$ `Bucket` (expandable down to `Party`), showing outflows per period, row `Average`, and row `Total`.
  - **Summary Row**: `Total Income`, `Total Expenses`, and `Net Income (Savings)` (`Income - Expenses`) per column and overall.

#### 3. Spending Trends (`SPENDING_TRENDS`)
- **Concept**: Multi-period visual comparison of spending patterns to spot seasonal changes and long-term drift.
- **Visualization**:
  - **Stacked Bar Chart** (Apache ECharts) over the selected intervals (grouped by `BucketGroup`, `Bucket`, `Party`, or `Account`).
  - Overlaid with a dashed **Average Spending Line** and an optional **Linear Regression Trend Line**.
  - Interactive drill-down clicking a bar or legend item to inspect underlying `BudgetTransaction`s via HTMX drawer.

#### 4. Spending Breakdown (`SPENDING_BREAKDOWN`)
- **Concept**: Proportional composition of spending within a chosen window.
- **Visualization**:
  - Interactive **Donut Chart / Treemap** (Apache ECharts) paired with a ranked summary table (`Bucket` or `Party`, `Total Spent`, `% of Total`, `Transaction Count`, `Average Transaction`).

#### 5. Net Worth (`NET_WORTH`)
- **Concept**: Total financial worth across all user-owned accounts (`is_user_owned = 1`) over time: $\text{Net Worth} = \text{Assets} - \text{Liabilities}$.
- **View Modes**:
  - **Trend Mode**: Positive-balance accounts plotted as `Assets` (bars above zero), negative-balance / credit card / debt accounts plotted as `Liabilities` (bars below zero), overlaid with a bold **Net Worth Line** and summary cards (`Starting Net Worth`, `Ending Net Worth`, `Absolute Change`, `% Change`).
  - **Stacked Account Mode**: Stacked area/bar view separating individual account balances over time (e.g. Checking + Savings + Investment accounts).

---

### 13.3 Actual Budget-Inspired Advanced & Experimental Reports

#### 6. Sankey Report (`SANKEY`)
- **Concept**: Visualizes proportional flow of money through the budget using a multi-layer Sankey diagram (inspired by Actual Budget's `sankey-spreadsheet.ts`).
- **View Modes**:
  1. **Spent Mode**: `Income Party` $\rightarrow$ `Account` (optional/groupable via *"Group accounts"* toggle so credit-card spending connects smoothly to salary inflows) $\rightarrow$ `BucketGroup` $\rightarrow$ `Bucket` (+ `Unspent / Saved` node when `Income > Expenses`).
  2. **Budgeted Mode**: `Available Funds / Income` $\rightarrow$ `BucketGroup` $\rightarrow$ `Bucket` $\rightarrow$ `Target`.
- **Display & Layout Controls**:
  - **Top-$N$ Node Cap**: Limits visible nodes per layer (`Show up to N` or `All`) and automatically rolls smaller categories into an `"Other"` node with a rich hover tooltip listing all aggregated items and their exact amounts.
  - **Sorting**: `per-group`, `global` (largest flow first), or `budget-order`.
  - **Labels**: Toggle between absolute monetary values and percentage labels (`%`).

#### 7. Balance Forecast Report (`BALANCE_FORECAST`)
- **Concept**: Projects future account balances day-by-day or month-by-month to identify upcoming cashflow shortfalls before they occur (inspired by Actual Budget's `forecast-projection.ts`).
- **Forecasting Algorithm**:
  - Resolves starting balance from `account_currency_balances` / `budget_transactions_current` up to $T_{\text{start}}$.
  - **Two Forecast Sources**:
    1. **Targets & Scheduled Recurrences Mode**: Expands all active `Target`s (one-off due dates, weekly/monthly/annual/custom $N$-day cadences where $N \ge 1$, plus `ARREARS_CUMULATIVE` unpaid obligations) into simulated daily occurrences across $[T_{\text{start}}, T_{\text{end}}]$ (bounded to a maximum projection horizon of `3,650` days / 10 years).
    2. **Budget Plan Mode**: Projects monthly trajectory by adding planned income targets and subtracting planned bucket expense allocations.
  - Computes daily/monthly running balance per account and in aggregate, explicitly returning the **Lowest Projected Balance** `(date, balance_minor, account_id, account_name)` and highlighting any negative balance troughs in red.

#### 8. Budget Analysis Report (`BUDGET_ANALYSIS`)
- **Concept**: Tracks budget execution and envelope rollover mechanics over time (inspired by Actual Budget's `budget-analysis-spreadsheet.ts`).
- **Four Tracked Series per Interval**:
  1. **`Budgeted / Allocated`**: Total funds assigned to selected Buckets/Targets in the interval.
  2. **`Spent`**: Total net outflows from those Buckets in the interval.
  3. **`Strategy / Overspending Adjustment`**: Funds swept back to the pool (`RETURN_TO_POOL`) or mandatory debt carried forward (`ARREARS_CUMULATIVE`).
  4. **`Cumulative Running Balance`**: End-of-period available balance carried forward (`ROLL_OVER` and `CAP_AND_HOLD`).
- **Controls**: Toggle between Bar and Line chart, show/hide cumulative balance line, Live rolling window vs. Static range.

#### 9. Monte Carlo Analysis (`MONTE_CARLO`)
- **Concept**: Full-featured probabilistic financial independence and retirement longevity simulator running `1,000` to `10,000` replays (default `5,000`) across a bounded Go worker pool (`runtime.NumCPU()` workers per simulation, guarded by a global server semaphore of max `4` concurrent simulations and a `2s` context timeout to prevent CPU/goroutine exhaustion, modeled on Actual Budget's `monteCarloSimulation.ts`).
- **Six-Tab Configuration Schema & Validation Bounds (Stored in `saved_reports.config_json`, max `64 KiB`)**:
  1. **Plan Details**:
     - `current_age` & `target_age` (strictly validated: `0 <= current_age < target_age <= 120`, maximum simulation horizon `1 <= (target_age - current_age) <= 100` years).
     - `simulations_count` (`1000`..`10000`, default `5000`).
     - `return_model`:
       - `RANDOM_NORMAL`: Draws annual return from normal distribution $(\mu, \sigma)$ per pot.
       - `HISTORICAL_SHUFFLED`: Samples real historical market years (1928–present stocks/bonds/bills + paired actual inflation) in random order.
       - `HISTORICAL_BLOCK_BOOTSTRAP`: Stitches consecutive runs of real historical years (configurable `block_length_years` `1`..`30`, default `10`) to preserve multi-year crash-and-recovery sequences.
       - `HISTORICAL_SEQUENCES`: Replays exact historical sequences starting from every historical year since 1928.
     - `inflation`: `mean_pct` & `std_dev_pct` (or paired historical inflation), plus `manage_surplus` (`SURPLUS_CASH_POT` vs `ASSUME_UNSPENT_IS_SPENT`).
  2. **Investment Pots** (max `20` pots per plan):
     - Multiple pots, each with: `name`, `starting_balance_minor` (or live `linked_account_id` to pull real-time balance from `accounts_current`), asset allocation (`STOCKS_BONDS_CASH_MIX` or `CUSTOM` with `expected_return_pct` & `volatility_std_dev_pct`), `accessible_from_age` (enabling **bridge-gap retirement** modeling where a pension is locked until age 57/60/65 while a taxable bridge pot funds early years), `tax_rate_pct` / `taxable_portion_pct`, and yearly `fixed_fee_minor` + `percentage_fee_pct`.
  3. **Income Streams** (max `20` streams per plan):
     - Non-portfolio income (state pension, rental income, part-time work) with `from_age`, `to_age`, `annual_gross_minor`, tax treatment, and `adjust_by_inflation` flag. Covers spending first before pots are drawn down.
  4. **Contributions** (max `20` contribution rules per plan):
     - Pre-retirement savings paid into specific pots (`into_pot_id`, `paid_from` outside plan or specific income stream, `before_tax` salary sacrifice flag, `from_age`, `to_age`, `annual_amount_minor`, `adjust_by_inflation`).
  5. **Spending Phases & Dynamic Withdrawal Rules** (max `10` spending phases per plan):
     - **Spending Phases**: Age-segmented annual net spending targets (e.g., active early retirement vs. later retirement) plus a `minimum_spending_floor_minor`.
     - **Multi-Pot Withdrawal Order**: `SPLIT_PROPORTIONALLY`, `DRAIN_IN_ORDER`, `BEST_PERFORMER_FIRST`, or `KEEP_TARGET_MIX`.
     - **Dynamic Withdrawal Rules**:
       - `FIXED_REAL`: Constant inflation-adjusted spending.
       - `GUARDRAILS_GUYTON_KLINGER`: Cuts spending when withdrawal rate drifts above upper guardrail; increases when below lower guardrail.
       - `RATCHETING_KITCES`: Increases spending when portfolio exceeds threshold for consecutive years; never cuts.
       - `FLOOR_AND_CEILING_BENGEN`: Withdraws a fixed `%` of current portfolio value bounded by a floor and ceiling around planned spending.
       - `BOUNDARIES`: Direct upper/lower bounds on withdrawal percentage.
  6. **Tax Configuration** (max `15` progressive tax bands):
     - `FLAT_RATE_PER_POT` (effective tax rate per pot/stream) or `PROGRESSIVE_TAX_BANDS` (inflation-indexed tax brackets where income streams fill lower bands first and pot withdrawals are taxed at the marginal slice above).
- **Outputs, Charts & Drill-Downs**:
  - **Real vs. Nominal Toggle**: View all figures in **Today's Money** (discounted by each replay's inflation path) or **Nominal Future Money**.
  - **Headline Metrics**: `Success Rate %`, `Median Ending Balance`, `Median Total Withdrawn`, `Chance of Running Out %`, and `Typical Failure Age`.
  - **Portfolio Performance Fan Chart**: 10th–90th percentile band (80% of replays), 25th–75th percentile band (middle 50%), and 50th percentile Median trajectory, plus selector for `Single Worst Run`, `Pessimistic`, `Median`, and `Optimistic`.
  - **Single-Run Cashflow Chart & Stacked Pot Balances Chart**: Inspect inflows (above zero) vs. spending, taxes, and contributions (below zero, with dimmed shortfall bars on failed years) and per-pot stacked balances for Worst, 25th, Median, 75th, or Best run.
  - **Depletion Age Histogram**: Bar chart of failure counts by age.
  - **Interactive Year-by-Year Run Table & "Year Story"**: Full tabular breakdown of any replay with a plain-English explanation of each year's market return, withdrawal rule adjustments, tax calculation, and pot draws.

#### 10. Party & Counterparty Cashflow Report (`PARTY_CASHFLOW`)
- **Concept**: Dedicated Balance report aggregating inflows, outflows, net balance, transaction count, and average ticket size per `Party` (e.g., total spent at `"Żabka"` across all its POS/external accounts) and per individual `Account`, with one-click toggle to exclude internal transfers between user-owned Parties (`is_user_owned = 1`).

