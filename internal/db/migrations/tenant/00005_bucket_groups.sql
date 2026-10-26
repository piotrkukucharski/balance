-- migrations/tenant/00005_bucket_groups.sql
-- Module: Bucket Groups Reordering & Rename

CREATE TABLE IF NOT EXISTS bucket_groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bucket_groups_sort ON bucket_groups(sort_order ASC, name ASC);

-- Populate bucket_groups with existing distinct groups from buckets_projection if any
INSERT OR IGNORE INTO bucket_groups (id, name, sort_order, created_at, updated_at)
SELECT 
    lower(hex(randomblob(16))),
    trim(group_name),
    row_number() OVER (ORDER BY group_name ASC) - 1,
    cast(strftime('%s', 'now') as integer) * 1000000,
    cast(strftime('%s', 'now') as integer) * 1000000
FROM (
    SELECT DISTINCT group_name 
    FROM buckets_projection 
    WHERE trim(group_name) != ''
)
ORDER BY group_name ASC;
