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
