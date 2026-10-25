-- migrations/tenant/00003_report_cache.sql
-- Module: Report Cache (Daily Caching & On-Demand Recalculate)

CREATE TABLE IF NOT EXISTS report_cache (
    cache_key TEXT PRIMARY KEY,
    report_type TEXT NOT NULL,
    filter_json TEXT NOT NULL,
    calc_date TEXT NOT NULL, -- Format 'YYYY-MM-DD' in budget timezone
    data_json TEXT NOT NULL,
    calculated_at INTEGER NOT NULL, -- Unix timestamp microseconds
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_report_cache_type_date ON report_cache(report_type, calc_date);
