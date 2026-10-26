-- migrations/tenant/00005_optional_target_name.sql
-- Module: Optional Target Name (BALANCE-69)

PRAGMA foreign_keys = OFF;

CREATE TABLE targets_projection_new (
    id TEXT PRIMARY KEY,
    bucket_id TEXT NOT NULL,
    name TEXT,
    target_type TEXT NOT NULL CHECK(target_type IN ('EXPENSE', 'SAVINGS', 'DEBT_PAYMENT')),
    priority INTEGER NOT NULL DEFAULT 2 CHECK(priority >= 1),
    sort_order INTEGER NOT NULL DEFAULT 0,
    amount_mode TEXT NOT NULL DEFAULT 'FIXED' CHECK(amount_mode IN ('FIXED', 'LUA_FORMULA')),
    amount_minor INTEGER NOT NULL DEFAULT 0 CHECK(amount_minor >= 0 AND amount_minor <= 100000000000000),
    lua_script TEXT CHECK(lua_script IS NULL OR length(lua_script) <= 2048),
    last_formula_error TEXT,
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
    last_evaluated_at INTEGER,
    uses_runtime INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (bucket_id) REFERENCES buckets_projection(id) ON DELETE CASCADE
);

INSERT INTO targets_projection_new (
    id, bucket_id, name, target_type, priority, sort_order, amount_mode, amount_minor,
    lua_script, last_formula_error, currency, recurrence_cadence, recurrence_interval,
    due_date, non_spending_strategy, ceiling_amount_minor, current_funded_minor,
    is_archived, version, updated_at, last_evaluated_at, uses_runtime
)
SELECT
    id, bucket_id, name, target_type, priority, sort_order, amount_mode, amount_minor,
    lua_script, last_formula_error, currency, recurrence_cadence, recurrence_interval,
    due_date, non_spending_strategy, ceiling_amount_minor, current_funded_minor,
    is_archived, version, updated_at, last_evaluated_at, uses_runtime
FROM targets_projection;

DROP TABLE targets_projection;
ALTER TABLE targets_projection_new RENAME TO targets_projection;

CREATE INDEX IF NOT EXISTS idx_targets_proj_bucket ON targets_projection(bucket_id);
CREATE INDEX IF NOT EXISTS idx_targets_proj_priority ON targets_projection(priority DESC, due_date ASC, sort_order ASC);
CREATE INDEX IF NOT EXISTS idx_targets_runtime_eval ON targets_projection(uses_runtime, last_evaluated_at);

PRAGMA foreign_keys = ON;
