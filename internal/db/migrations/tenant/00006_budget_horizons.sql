-- migrations/tenant/00006_budget_horizons.sql
-- Module: Time Horizons and Per-User Budget Preferences (BALANCE-58)

CREATE TABLE IF NOT EXISTS budget_horizons (
    id TEXT PRIMARY KEY, -- 'monthly', 'annual', '<N>d', 'day-<D>'
    horizon_type TEXT NOT NULL CHECK(horizon_type IN ('MONTHLY', 'ANNUAL', 'ROLLING_DAYS', 'CALENDAR_DAY_OF_MONTH')),
    days_value INTEGER CHECK(
        (horizon_type IN ('MONTHLY', 'ANNUAL') AND days_value IS NULL) OR
        (horizon_type = 'ROLLING_DAYS' AND days_value IS NOT NULL AND days_value >= 1 AND days_value <= 3650) OR
        (horizon_type = 'CALENDAR_DAY_OF_MONTH' AND days_value IS NOT NULL AND days_value >= 1 AND days_value <= 28)
    ),
    is_system INTEGER NOT NULL DEFAULT 0, -- 1 = 'monthly' and 'annual', immutable system items
    is_hidden INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_budget_horizons_sort ON budget_horizons(is_hidden ASC, sort_order ASC, created_at ASC);

-- Seed default initial horizons
INSERT OR IGNORE INTO budget_horizons (id, horizon_type, days_value, is_system, is_hidden, sort_order, created_at, updated_at)
VALUES
    ('monthly', 'MONTHLY', NULL, 1, 0, 10, cast(strftime('%s', 'now') as integer) * 1000000, cast(strftime('%s', 'now') as integer) * 1000000),
    ('30d', 'ROLLING_DAYS', 30, 0, 0, 20, cast(strftime('%s', 'now') as integer) * 1000000, cast(strftime('%s', 'now') as integer) * 1000000),
    ('90d', 'ROLLING_DAYS', 90, 0, 0, 30, cast(strftime('%s', 'now') as integer) * 1000000, cast(strftime('%s', 'now') as integer) * 1000000),
    ('annual', 'ANNUAL', NULL, 1, 0, 40, cast(strftime('%s', 'now') as integer) * 1000000, cast(strftime('%s', 'now') as integer) * 1000000),
    ('365d', 'ROLLING_DAYS', 365, 0, 0, 50, cast(strftime('%s', 'now') as integer) * 1000000, cast(strftime('%s', 'now') as integer) * 1000000);

-- Per-User Budget Preferences
CREATE TABLE IF NOT EXISTS budget_preferences (
    user_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, key)
);

CREATE INDEX IF NOT EXISTS idx_budget_preferences_user ON budget_preferences(user_id);
