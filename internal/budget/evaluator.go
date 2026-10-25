package budget

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/balance/balance/internal/lua"
)

type evalTarget struct {
	ID                 string
	Name               string
	BucketID           string
	Priority           int
	SortOrder          int
	AmountMinor        int64
	LuaScript          string
	Currency           string
	DueDate            *int64
	UsesRuntime        int
	LastEvaluatedAt    *int64
	LastFormulaError   *string
	CurrentFundedMinor int64
	DepRefs            []string
}

// EnsureDailyTargetsEvaluated checks if any active targets with amount_mode = 'LUA_FORMULA'
// and uses_runtime = 1 have not yet been evaluated for the current calendar day in the budget's timezone.
// It evaluates targets in topological dependency order (DAG), updating targets_projection and snapshot in-memory.
// Returns the count of recalculated targets and any error encountered.
func EnsureDailyTargetsEvaluated(ctx context.Context, tenantDB *sql.DB, timezone string, now ...time.Time) (int, error) {
	if tenantDB == nil {
		return 0, nil
	}

	loc := time.UTC
	if timezone != "" {
		if l, err := time.LoadLocation(timezone); err == nil {
			loc = l
		}
	}

	currentTime := time.Now().In(loc)
	if len(now) > 0 && !now[0].IsZero() {
		currentTime = now[0].In(loc)
	}

	todayMidnight := time.Date(currentTime.Year(), currentTime.Month(), currentTime.Day(), 0, 0, 0, 0, loc)
	todayStartMicros := todayMidnight.UnixMicro()

	// Fast check: are there any active targets using runtime needing evaluation?
	var pendingCount int
	err := tenantDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM targets_projection
		WHERE is_archived = 0
		  AND amount_mode = 'LUA_FORMULA'
		  AND uses_runtime = 1
		  AND (last_evaluated_at IS NULL OR last_evaluated_at < ?)`,
		todayStartMicros,
	).Scan(&pendingCount)
	if err != nil || pendingCount == 0 {
		return 0, err
	}

	// Fetch all active formula targets to build dependency graph
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, bucket_id, name, priority, sort_order, amount_minor, lua_script,
		       currency, due_date, uses_runtime, last_evaluated_at, last_formula_error, current_funded_minor
		FROM targets_projection
		WHERE is_archived = 0
		  AND amount_mode = 'LUA_FORMULA'
		  AND lua_script IS NOT NULL AND length(trim(lua_script)) > 0`,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var allTargets []*evalTarget
	targetByID := make(map[string]*evalTarget)
	targetByName := make(map[string]*evalTarget)

	for rows.Next() {
		var t evalTarget
		var dueDateNull sql.NullInt64
		var lastEvalNull sql.NullInt64
		var lastErrNull sql.NullString
		if err := rows.Scan(
			&t.ID, &t.BucketID, &t.Name, &t.Priority, &t.SortOrder, &t.AmountMinor,
			&t.LuaScript, &t.Currency, &dueDateNull, &t.UsesRuntime, &lastEvalNull,
			&lastErrNull, &t.CurrentFundedMinor,
		); err != nil {
			continue
		}
		if dueDateNull.Valid {
			t.DueDate = &dueDateNull.Int64
		}
		if lastEvalNull.Valid {
			t.LastEvaluatedAt = &lastEvalNull.Int64
		}
		if lastErrNull.Valid {
			t.LastFormulaError = &lastErrNull.String
		}

		deps, _ := lua.ExtractTargetDependencies(t.LuaScript)
		t.DepRefs = deps

		allTargets = append(allTargets, &t)
		targetByID[t.ID] = &t
		targetByName[strings.ToLower(t.Name)] = &t
	}

	// Find the seed targets: uses_runtime = 1 and (last_evaluated_at IS NULL OR < todayStartMicros)
	toEvaluateSet := make(map[string]*evalTarget)
	for _, t := range allTargets {
		if t.UsesRuntime == 1 && (t.LastEvaluatedAt == nil || *t.LastEvaluatedAt < todayStartMicros) {
			toEvaluateSet[t.ID] = t
		}
	}

	if len(toEvaluateSet) == 0 {
		return 0, nil
	}

	// Transitive closure: any formula target that directly or indirectly depends on a target in toEvaluateSet
	// must also be re-evaluated
	changed := true
	for changed {
		changed = false
		for _, t := range allTargets {
			if _, already := toEvaluateSet[t.ID]; already {
				continue
			}
			for _, ref := range t.DepRefs {
				refTarget := targetByID[ref]
				if refTarget == nil {
					refTarget = targetByName[strings.ToLower(ref)]
				}
				if refTarget != nil {
					if _, inEval := toEvaluateSet[refTarget.ID]; inEval {
						toEvaluateSet[t.ID] = t
						changed = true
						break
					}
				}
			}
		}
	}

	// Build DAG among toEvaluateSet
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // targetID -> slice of dependent target IDs

	for _, t := range toEvaluateSet {
		inDegree[t.ID] = 0
	}

	for _, t := range toEvaluateSet {
		for _, ref := range t.DepRefs {
			refTarget := targetByID[ref]
			if refTarget == nil {
				refTarget = targetByName[strings.ToLower(ref)]
			}
			if refTarget != nil {
				if _, inEval := toEvaluateSet[refTarget.ID]; inEval && refTarget.ID != t.ID {
					inDegree[t.ID]++
					dependents[refTarget.ID] = append(dependents[refTarget.ID], t.ID)
				}
			}
		}
	}

	// Sort helper: priority DESC, sort_order ASC, ID ASC
	compareTargets := func(a, b *evalTarget) bool {
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return a.ID < b.ID
	}

	// Initial candidates: in-degree == 0
	var candidates []*evalTarget
	for _, t := range toEvaluateSet {
		if inDegree[t.ID] == 0 {
			candidates = append(candidates, t)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return compareTargets(candidates[i], candidates[j])
	})

	var evalOrder []*evalTarget
	visited := make(map[string]bool)

	for len(candidates) > 0 {
		curr := candidates[0]
		candidates = candidates[1:]
		evalOrder = append(evalOrder, curr)
		visited[curr.ID] = true

		for _, depID := range dependents[curr.ID] {
			inDegree[depID]--
			if inDegree[depID] == 0 {
				depTarget := toEvaluateSet[depID]
				candidates = append(candidates, depTarget)
				sort.Slice(candidates, func(i, j int) bool {
					return compareTargets(candidates[i], candidates[j])
				})
			}
		}
	}

	// Fallback for cycles or disconnected targets
	if len(evalOrder) < len(toEvaluateSet) {
		var remaining []*evalTarget
		for id, t := range toEvaluateSet {
			if !visited[id] {
				remaining = append(remaining, t)
			}
		}
		sort.Slice(remaining, func(i, j int) bool {
			return compareTargets(remaining[i], remaining[j])
		})
		evalOrder = append(evalOrder, remaining...)
	}

	// Build financial snapshot for Lua sandbox
	defaultCurr := "PLN"
	if len(evalOrder) > 0 && evalOrder[0].Currency != "" {
		defaultCurr = evalOrder[0].Currency
	}
	snap, _ := BuildBudgetSnapshot(ctx, tenantDB, defaultCurr, 30, loc.String())
	if snap == nil {
		snap = &lua.InMemoryBudgetSnapshot{
			Targets:        make(map[string]*lua.TargetMeta),
			BucketBalances: make(map[string]int64),
			FXRates:        make(map[string]int64),
			Timezone:       loc.String(),
		}
	}
	snap.NowTime = &currentTime
	snap.Timezone = loc.String()

	nowMicros := currentTime.UTC().UnixMicro()
	recalculated := 0

	for _, t := range evalOrder {
		var anchorDueDateSec *int64
		if t.DueDate != nil && *t.DueDate > 0 {
			sec := *t.DueDate / 1_000_000
			anchorDueDateSec = &sec
		}

		targetCtx := &lua.TargetFormulaContext{
			TargetID:      t.ID,
			Name:          t.Name,
			Planned:       t.AmountMinor,
			Required:      t.AmountMinor,
			Funded:        t.CurrentFundedMinor,
			Spent:         0,
			Priority:      t.Priority,
			Currency:      t.Currency,
			AnchorDueDate: anchorDueDateSec,
		}

		evalVal, evalErr := lua.EvaluateFormulaWithContext(ctx, t.LuaScript, snap, targetCtx)
		if evalErr != nil {
			errStr := evalErr.Error()
			_, _ = tenantDB.ExecContext(ctx, `
				UPDATE targets_projection
				SET last_evaluated_at = ?, last_formula_error = ?
				WHERE id = ?`,
				nowMicros, errStr, t.ID,
			)
			t.LastFormulaError = &errStr
			t.LastEvaluatedAt = &nowMicros
		} else {
			_, _ = tenantDB.ExecContext(ctx, `
				UPDATE targets_projection
				SET amount_minor = ?, last_evaluated_at = ?, last_formula_error = NULL
				WHERE id = ?`,
				evalVal, nowMicros, t.ID,
			)
			t.AmountMinor = evalVal
			t.LastFormulaError = nil
			t.LastEvaluatedAt = &nowMicros

			// Update in-memory snapshot so subsequent targets see the new value
			if snap.Targets == nil {
				snap.Targets = make(map[string]*lua.TargetMeta)
			}
			meta := snap.Targets[t.ID]
			if meta == nil {
				meta = &lua.TargetMeta{
					ID:            t.ID,
					Name:          t.Name,
					Priority:      t.Priority,
					Currency:      t.Currency,
					AnchorDueDate: anchorDueDateSec,
				}
				snap.Targets[t.ID] = meta
				snap.Targets[t.Name] = meta
				snap.Targets[strings.ToLower(t.Name)] = meta
			}
			meta.Planned = evalVal
			meta.Required = evalVal
		}
		recalculated++
	}

	return recalculated, nil
}
