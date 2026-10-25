package budget

import (
	"context"
	"testing"
	"time"

	"github.com/balance/balance/internal/domain"
)

func TestEnsureDailyTargetsEvaluated_DAGAndIdempotency(t *testing.T) {
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()
	ctx := context.Background()

	now := time.Now().UTC().UnixMicro()

	// Create bucket
	bucketID := domain.NewUUIDv7()
	_, err := tenantDB.ExecContext(ctx, `
		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, 'Expenses', 'Loans', 0, 1, ?)`,
		bucketID, now,
	)
	if err != nil {
		t.Fatalf("failed to insert bucket: %v", err)
	}

	// Target 1: Base Loan (uses runtime.today and anchor_due_date)
	baseScript := `
		local diff = date.diff_months(self.anchor_due_date, runtime.today)
		return money(diff * 100)
	`
	dueDate := time.Date(2027, 10, 25, 0, 0, 0, 0, time.UTC).UnixMicro()
	baseID, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:   bucketID,
		Name:       "Base Loan",
		Priority:   2,
		AmountMode: "LUA_FORMULA",
		LuaScript:  &baseScript,
		DueDate:    &dueDate,
		Currency:   "PLN",
	})
	if err != nil {
		t.Fatalf("failed to create Base Loan: %v", err)
	}

	// Verify uses_runtime detected
	var usesRuntime int
	err = tenantDB.QueryRowContext(ctx, `SELECT uses_runtime FROM targets_projection WHERE id = ?`, baseID).Scan(&usesRuntime)
	if err != nil || usesRuntime != 1 {
		t.Fatalf("expected uses_runtime=1, got %d (err: %v)", usesRuntime, err)
	}

	// Target 2: Loan Surcharge (depends on target('Base Loan'))
	surchargeScript := `
		local base = target("Base Loan").required
		return math.floor(base * 0.10)
	`
	surchargeID, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:   bucketID,
		Name:       "Loan Surcharge",
		Priority:   1, // lower priority than Base Loan
		AmountMode: "LUA_FORMULA",
		LuaScript:  &surchargeScript,
		Currency:   "PLN",
	})
	if err != nil {
		t.Fatalf("failed to create Loan Surcharge: %v", err)
	}

	// Evaluate on date 2026-10-25
	simDate1 := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	count, err := EnsureDailyTargetsEvaluated(ctx, tenantDB, "Europe/Warsaw", simDate1)
	if err != nil {
		t.Fatalf("EnsureDailyTargetsEvaluated failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 targets recalculated, got %d", count)
	}

	// Check Base Loan: 12 months * 100 = 1200.00 PLN = 120000 minor
	var baseAmt int64
	var baseLastEval *int64
	err = tenantDB.QueryRowContext(ctx, `SELECT amount_minor, last_evaluated_at FROM targets_projection WHERE id = ?`, baseID).Scan(&baseAmt, &baseLastEval)
	if err != nil || baseAmt != 120000 {
		t.Fatalf("expected Base Loan amount 120000, got %d (err: %v)", baseAmt, err)
	}
	if baseLastEval == nil || *baseLastEval == 0 {
		t.Fatalf("expected Base Loan last_evaluated_at to be populated")
	}

	// Check Loan Surcharge: 10% of 120000 = 12000 minor
	var surchargeAmt int64
	err = tenantDB.QueryRowContext(ctx, `SELECT amount_minor FROM targets_projection WHERE id = ?`, surchargeID).Scan(&surchargeAmt)
	if err != nil || surchargeAmt != 12000 {
		t.Fatalf("expected Loan Surcharge amount 12000, got %d (err: %v)", surchargeAmt, err)
	}

	// Idempotency: re-evaluating on same day returns 0
	simDateSameDay := time.Date(2026, 10, 25, 15, 30, 0, 0, time.UTC)
	count2, err := EnsureDailyTargetsEvaluated(ctx, tenantDB, "Europe/Warsaw", simDateSameDay)
	if err != nil {
		t.Fatalf("same-day re-evaluation failed: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("expected 0 targets recalculated on same day, got %d", count2)
	}

	// New calendar day: 2026-11-25 (11 months remaining -> 1100.00 PLN, surcharge 110.00 PLN)
	simDate2 := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	count3, err := EnsureDailyTargetsEvaluated(ctx, tenantDB, "Europe/Warsaw", simDate2)
	if err != nil {
		t.Fatalf("new-day evaluation failed: %v", err)
	}
	if count3 != 2 {
		t.Fatalf("expected 2 targets recalculated on new day, got %d", count3)
	}

	err = tenantDB.QueryRowContext(ctx, `SELECT amount_minor FROM targets_projection WHERE id = ?`, baseID).Scan(&baseAmt)
	if err != nil || baseAmt != 110000 {
		t.Fatalf("expected Base Loan amount 110000 on new day, got %d", baseAmt)
	}

	err = tenantDB.QueryRowContext(ctx, `SELECT amount_minor FROM targets_projection WHERE id = ?`, surchargeID).Scan(&surchargeAmt)
	if err != nil || surchargeAmt != 11000 {
		t.Fatalf("expected Loan Surcharge amount 11000 on new day, got %d", surchargeAmt)
	}
}

func TestEnsureDailyTargetsEvaluated_GracefulError(t *testing.T) {
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()
	ctx := context.Background()

	bucketID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, _ = tenantDB.ExecContext(ctx, `
		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, 'Fixed', 'Utilities', 0, 1, ?)`,
		bucketID, now,
	)

	// Create a faulty target that fails during runtime evaluation
	faultyScript := `
		local t = runtime.today
		error("Calculation error in target")
	`
	tID, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:    bucketID,
		Name:        "Faulty Target",
		Priority:    2,
		AmountMode:  "LUA_FORMULA",
		AmountMinor: 50000, // initial amount: 500.00 PLN
		LuaScript:   &faultyScript,
		Currency:    "PLN",
	})
	if err != nil {
		t.Fatalf("failed to create target: %v", err)
	}

	simDate := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	count, err := EnsureDailyTargetsEvaluated(ctx, tenantDB, "Europe/Warsaw", simDate)
	if err != nil {
		t.Fatalf("expected EnsureDailyTargetsEvaluated not to return fatal error, got: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 target evaluated, got %d", count)
	}

	// Verify target retained previous amount
	var amt int64
	var lastErr *string
	var lastEval *int64
	err = tenantDB.QueryRowContext(ctx, `
		SELECT amount_minor, last_formula_error, last_evaluated_at
		FROM targets_projection
		WHERE id = ?`, tID,
	).Scan(&amt, &lastErr, &lastEval)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if amt != 50000 {
		t.Fatalf("expected amount to retain 50000 minor, got %d", amt)
	}
	if lastErr == nil || *lastErr == "" {
		t.Fatalf("expected last_formula_error to be recorded")
	}
	if lastEval == nil || *lastEval == 0 {
		t.Fatalf("expected last_evaluated_at to be set even on error")
	}

	// Second call on same day should NOT re-attempt evaluation
	countSameDay, err := EnsureDailyTargetsEvaluated(ctx, tenantDB, "Europe/Warsaw", simDate)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if countSameDay != 0 {
		t.Fatalf("expected 0 re-evaluations on same day, got %d", countSameDay)
	}
}

func TestEnsureDailyTargetsEvaluated_CycleFallback(t *testing.T) {
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()
	ctx := context.Background()

	bucketID := domain.NewUUIDv7()
	now := time.Now().UTC().UnixMicro()
	_, _ = tenantDB.ExecContext(ctx, `
		INSERT INTO buckets_projection (id, group_name, name, is_archived, version, updated_at)
		VALUES (?, 'Fixed', 'Cyclic', 0, 1, ?)`,
		bucketID, now,
	)

	// Cycle: Target A depends on B, Target B depends on A
	scriptA := `
		local t = runtime.today
		local b = target("Target B").required
		return b + 100
	`
	scriptB := `
		local t = runtime.today
		local a = target("Target A").required
		return a + 200
	`
	_, err := CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:    bucketID,
		Name:        "Target A",
		Priority:    3,
		AmountMinor: 1000,
		AmountMode:  "LUA_FORMULA",
		LuaScript:   &scriptA,
		Currency:    "PLN",
	})
	if err != nil {
		t.Fatalf("create target A: %v", err)
	}

	_, err = CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:    bucketID,
		Name:        "Target B",
		Priority:    2,
		AmountMinor: 2000,
		AmountMode:  "LUA_FORMULA",
		LuaScript:   &scriptB,
		Currency:    "PLN",
	})
	if err != nil {
		t.Fatalf("create target B: %v", err)
	}

	simDate := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	count, err := EnsureDailyTargetsEvaluated(ctx, tenantDB, "Europe/Warsaw", simDate)
	if err != nil {
		t.Fatalf("evaluation with cycle should not fail fatally, got: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 targets evaluated in fallback order, got %d", count)
	}
}

