# Internal Packages (`internal`)

## Overview
Intermediate blueprint organizing the core application components, domain logic, database engines, presentation layer, integrations, and background infrastructure for the Balance platform.

## Subdirectories & Boundaries
- [AI Assistance](./ai/README.md): LLM assistance for formula generation, natural language automation translation, smart rules, and budget structure scaffolding.
- [REST API & OpenAPI](./api/README.md): OpenAPI 3.1 specification generation, Swagger/Scalar documentation routes, and generated REST API v1 server bindings.
- [Authentication & Security](./auth/README.md): Password hashing with Argon2id, CSPRNG token generation, and Personal Access Token (PAT) cryptographic verification.
- [Automation Engine](./automation/README.md): Automated transaction rules, multi-condition triggers, bulk actions, and versioned automation histories.
- [Envelope Budgeting](./budget/README.md): Envelope budgeting core domain engine handling bucket CRUD, target evaluations, RTA math, and budget horizons.
- [Clock Abstraction](./clock/README.md): Deterministic time abstraction providing mockable clocks and timezone conversions.
- [Database Management](./db/README.md): Database connection lifecycle, WAL pragma configuration, and multi-tenant SQLite manager with LRU pooling.
- [Domain Primitives](./domain/README.md): Core domain primitives including UUIDv7 generation, minor unit monetary bounds checking, and safe currency math.
- [FX Rates Engine](./fx/README.md): Foreign exchange rate synchronization and caching engine supporting ECB and NBP feeds.
- [Audit History & Time-Travel](./history/README.md): Entity audit history, unified JSON diffs, event inspection, and state restoration.
- [Internationalization](./i18n/README.md): Localization manager handling embedded translation dictionaries and locale currency defaults.
- [Financial Integrations](./integration/README.md): Bank statement CSV ingestion, Open Banking PSD2 feeds via GoCardless, and offline mock banking adapters.
- [Logging Subsystem](./logger/README.md): Structured logging wrapping log/slog with correlation ID tracing and context propagation.
- [Lua Sandboxed Runtime](./lua/README.md): Embedded GopherLua sandboxed runtime for dynamic target calculations and custom automation scripts.
- [Reporting & Analytics](./report/README.md): Analytical reporting and forecasting engine calculating Age of Money, Monte Carlo simulations, Sankey flows, and spending trends.
- [Rule Matching Engine](./rule/README.md): Deterministic rule matching applying prioritized condition sets to transaction streams.
- [Job Scheduler](./scheduler/README.md): Background job scheduler orchestrating recurring maintenance, FX rate sync, and target renewals.
- [Application Services](./service/README.md): High-level service layer coordinating multi-package business workflows and fixture provisioning.
- [Transaction Engine](./transaction/README.md): Transaction lifecycle and double-entry ledger management engine handling deduplication hashing and CQRS projections.
- [Web Presentation](./web/README.md): Web presentation layer orchestrating HTTP routing, controllers, embedded HTML views, and static assets.
