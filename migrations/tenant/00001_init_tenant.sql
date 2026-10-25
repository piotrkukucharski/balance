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
    favicon_type TEXT NOT NULL DEFAULT 'EMOJI' CHECK(favicon_type IN ('EMOJI', 'URL')),
    favicon_value TEXT,
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
    booking_status TEXT NOT NULL DEFAULT 'BOOKED' CHECK(booking_status IN ('BOOKED', 'PENDING')),
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
    recurrence_cadence TEXT NOT NULL CHECK(recurrence_cadence IN ('ONE_OFF', 'WEEKLY', 'MONTHLY', 'ANNUAL', 'CUSTOM_DAYS', 'CUSTOM_MONTHS', 'CUSTOM_YEARS')),
    recurrence_interval INTEGER CHECK(recurrence_cadence NOT IN ('CUSTOM_DAYS', 'CUSTOM_MONTHS', 'CUSTOM_YEARS') OR (recurrence_interval IS NOT NULL AND recurrence_interval >= 1 AND recurrence_interval <= 36500)),
    due_date INTEGER CHECK(recurrence_cadence NOT IN ('CUSTOM_MONTHS', 'CUSTOM_YEARS') OR due_date IS NOT NULL),
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
    favicon_type TEXT NOT NULL DEFAULT 'EMOJI' CHECK(favicon_type IN ('EMOJI', 'URL')),
    favicon_value TEXT,
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
    booking_status TEXT NOT NULL DEFAULT 'BOOKED' CHECK(booking_status IN ('BOOKED', 'PENDING')),
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_raw_tx_curr_dedup ON raw_transactions_current(account_id, dedup_hash);
CREATE INDEX IF NOT EXISTS idx_raw_tx_curr_status_date ON raw_transactions_current(status, booking_date DESC);
CREATE INDEX IF NOT EXISTS idx_raw_tx_curr_booking_status ON raw_transactions_current(booking_status);
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
