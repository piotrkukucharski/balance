-- migrations/tenant/00004_targets_runtime_eval.sql
-- Module: Targets Daily Runtime Evaluation

CREATE INDEX IF NOT EXISTS idx_targets_runtime_eval ON targets_projection(uses_runtime, last_evaluated_at);

UPDATE targets_projection
SET uses_runtime = 1
WHERE (lua_script LIKE '%runtime.%' OR lua_script LIKE '%runtime%')
  AND uses_runtime = 0;
