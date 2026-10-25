-- migrations/tenant/00002_automations.sql
-- Module: Automations (Append-Only Versioned + Current Projection)

CREATE TABLE IF NOT EXISTS automations (
    id TEXT NOT NULL, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL,
    judge_script TEXT NOT NULL,
    executor_script TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL CHECK(action IN ('CREATE', 'UPDATE', 'RESTORE', 'SOFT_DELETE')),
    agent_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (id, version)
);

CREATE INDEX IF NOT EXISTS idx_automations_id_ver ON automations(id, version DESC);

CREATE TABLE IF NOT EXISTS automations_current (
    id TEXT PRIMARY KEY, -- UUIDv7
    version INTEGER NOT NULL,
    name TEXT NOT NULL,
    judge_script TEXT NOT NULL,
    executor_script TEXT NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    is_active INTEGER NOT NULL DEFAULT 1,
    sort_order INTEGER NOT NULL DEFAULT 0,
    agent_id TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_automations_curr_sort ON automations_current(sort_order ASC, updated_at DESC);
