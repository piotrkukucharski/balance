package budget

import (
	"context"
	"testing"

	"github.com/balance/balance/internal/ai"
	"github.com/balance/balance/internal/domain"
)

func TestApplyBudgetStructure_CustomDaysCadence(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()

	days14 := 14
	structure := &ai.BudgetStructure{
		Groups: []ai.ProposedGroup{
			{
				GroupName: "Transport & Living",
				Buckets: []ai.ProposedBucket{
					{
						Name: "Public Transit",
						Targets: []ai.ProposedTarget{
							{
								Name:                "Karta miejska (kwartalna)",
								TargetType:          "EXPENSE",
								Priority:            2,
								AmountMinor:         12500,
								Currency:            "PLN",
								RecurrenceCadence:   "CUSTOM_DAYS",
								RecurrenceDays:      nil, // nil to test fallback/inference
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
							{
								Name:                "Bilety dwutygodniowe",
								TargetType:          "EXPENSE",
								Priority:            1,
								AmountMinor:         5000,
								Currency:            "PLN",
								RecurrenceCadence:   "CUSTOM_DAYS",
								RecurrenceDays:      &days14,
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
						},
					},
				},
			},
		},
	}

	bCreated, tCreated, err := ApplyBudgetStructure(ctx, tenantDB, structure, agentID, corrID)
	if err != nil {
		t.Fatalf("ApplyBudgetStructure failed on CUSTOM_DAYS: %v", err)
	}
	if bCreated != 1 || tCreated != 2 {
		t.Fatalf("expected 1 bucket and 2 targets, got %d buckets and %d targets", bCreated, tCreated)
	}

	// Verify the targets in database
	var rCadence1 string
	var rDays1 int
	err = tenantDB.QueryRowContext(ctx, `SELECT recurrence_cadence, recurrence_interval FROM targets_projection WHERE name = ?`, "Karta miejska (kwartalna)").Scan(&rCadence1, &rDays1)
	if err != nil {
		t.Fatalf("failed querying target 1: %v", err)
	}
	if rCadence1 != "CUSTOM_DAYS" || rDays1 != 90 {
		t.Errorf("expected CUSTOM_DAYS with 90 days for quarterly ticket, got %s / %d", rCadence1, rDays1)
	}

	var rCadence2 string
	var rDays2 int
	err = tenantDB.QueryRowContext(ctx, `SELECT recurrence_cadence, recurrence_interval FROM targets_projection WHERE name = ?`, "Bilety dwutygodniowe").Scan(&rCadence2, &rDays2)
	if err != nil {
		t.Fatalf("failed querying target 2: %v", err)
	}
	if rCadence2 != "CUSTOM_DAYS" || rDays2 != 14 {
		t.Errorf("expected CUSTOM_DAYS with 14 days, got %s / %d", rCadence2, rDays2)
	}

	// Verify ExportBudgetStructure
	exported, err := ExportBudgetStructure(ctx, tenantDB)
	if err != nil {
		t.Fatalf("ExportBudgetStructure failed: %v", err)
	}
	if len(exported.Groups) != 1 || len(exported.Groups[0].Buckets) != 1 || len(exported.Groups[0].Buckets[0].Targets) != 2 {
		t.Fatalf("unexpected exported structure: %+v", exported)
	}
	exportedTargets := exported.Groups[0].Buckets[0].Targets
	if exportedTargets[0].RecurrenceDays == nil || *exportedTargets[0].RecurrenceDays != 90 {
		t.Errorf("expected exported target 1 recurrence_days to be 90, got: %v", exportedTargets[0].RecurrenceDays)
	}
	if exportedTargets[1].RecurrenceDays == nil || *exportedTargets[1].RecurrenceDays != 14 {
		t.Errorf("expected exported target 2 recurrence_days to be 14, got: %v", exportedTargets[1].RecurrenceDays)
	}
}

func TestApplyBudgetStructure_AtomicityRollback(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()

	// Structure with a valid first bucket + target, followed by an invalid target in a second bucket
	structure := &ai.BudgetStructure{
		Groups: []ai.ProposedGroup{
			{
				GroupName: "Living",
				Buckets: []ai.ProposedBucket{
					{
						Name: "Groceries",
						Targets: []ai.ProposedTarget{
							{
								Name:                "Weekly Groceries",
								TargetType:          "EXPENSE",
								Priority:            1,
								AmountMinor:         20000,
								Currency:            "PLN",
								RecurrenceCadence:   "WEEKLY",
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
						},
					},
					{
						Name: "Utilities",
						Targets: []ai.ProposedTarget{
							{
								Name:                "Faulty Target",
								TargetType:          "EXPENSE",
								Priority:            2,
								AmountMinor:         -500, // Invalid negative amount causing failure
								Currency:            "PLN",
								RecurrenceCadence:   "MONTHLY",
								NonSpendingStrategy: "RETURN_TO_POOL",
							},
						},
					},
				},
			},
		},
	}

	bCreated, tCreated, err := ApplyBudgetStructure(ctx, tenantDB, structure, agentID, corrID)
	if err == nil {
		t.Fatalf("expected error from ApplyBudgetStructure due to invalid target, got nil (buckets=%d, targets=%d)", bCreated, tCreated)
	}
	if bCreated != 0 || tCreated != 0 {
		t.Errorf("expected 0 buckets and 0 targets returned on failure, got buckets=%d, targets=%d", bCreated, tCreated)
	}

	// Verify complete rollback: no buckets, targets, or events should exist
	var bucketCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM buckets_projection`).Scan(&bucketCount); err != nil {
		t.Fatalf("failed querying buckets count: %v", err)
	}
	if bucketCount != 0 {
		t.Errorf("expected 0 buckets in projection after rollback, got %d", bucketCount)
	}

	var targetCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection`).Scan(&targetCount); err != nil {
		t.Fatalf("failed querying targets count: %v", err)
	}
	if targetCount != 0 {
		t.Errorf("expected 0 targets in projection after rollback, got %d", targetCount)
	}

	var bucketEventsCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM bucket_events`).Scan(&bucketEventsCount); err != nil {
		t.Fatalf("failed querying bucket_events count: %v", err)
	}
	if bucketEventsCount != 0 {
		t.Errorf("expected 0 events in bucket_events after rollback, got %d", bucketEventsCount)
	}

	var targetEventsCount int
	if err := tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM target_events`).Scan(&targetEventsCount); err != nil {
		t.Fatalf("failed querying target_events count: %v", err)
	}
	if targetEventsCount != 0 {
		t.Errorf("expected 0 events in target_events after rollback, got %d", targetEventsCount)
	}
}

func TestApplyBudgetStructure_ConflictResolution(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()

	// Initial setup: create buckets "Rent" and "Internet"
	bRentID, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Old Housing",
		Name:          "Rent",
		AgentID:       agentID,
		CorrelationID: corrID,
	})
	if err != nil {
		t.Fatalf("failed creating Rent bucket: %v", err)
	}

	_, err = CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:            bRentID,
		Name:                "Monthly Rent",
		TargetType:          "EXPENSE",
		Priority:            3,
		AmountMinor:         200000,
		Currency:            "PLN",
		RecurrenceCadence:   "MONTHLY",
		NonSpendingStrategy: "RETURN_TO_POOL",
		AgentID:             agentID,
		CorrelationID:       corrID,
	})
	if err != nil {
		t.Fatalf("failed creating target for Rent: %v", err)
	}

	bNetID, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Utilities",
		Name:          "Internet",
		AgentID:       agentID,
		CorrelationID: corrID,
	})
	if err != nil {
		t.Fatalf("failed creating Internet bucket: %v", err)
	}

	_, err = CreateTarget(ctx, tenantDB, CreateTargetInput{
		BucketID:            bNetID,
		Name:                "Old Fiber",
		TargetType:          "EXPENSE",
		Priority:            2,
		AmountMinor:         8000,
		Currency:            "PLN",
		RecurrenceCadence:   "MONTHLY",
		NonSpendingStrategy: "RETURN_TO_POOL",
		AgentID:             agentID,
		CorrelationID:       corrID,
	})
	if err != nil {
		t.Fatalf("failed creating target for Internet: %v", err)
	}

	bSkipID, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Lifestyle",
		Name:          "Entertainment",
		AgentID:       agentID,
		CorrelationID: corrID,
	})
	if err != nil {
		t.Fatalf("failed creating Entertainment bucket: %v", err)
	}

	t.Run("ConflictResolution MERGE and Group Update", func(t *testing.T) {
		structure := &ai.BudgetStructure{
			Groups: []ai.ProposedGroup{
				{
					GroupName: "New Housing Group", // group updated from "Old Housing"
					Buckets: []ai.ProposedBucket{
						{
							Name:               "Rent",
							ConflictResolution: "MERGE",
							Targets: []ai.ProposedTarget{
								{
									Name:                "Monthly Rent", // duplicate, should be skipped
									TargetType:          "EXPENSE",
									Priority:            3,
									AmountMinor:         200000,
									Currency:            "PLN",
									RecurrenceCadence:   "MONTHLY",
									NonSpendingStrategy: "RETURN_TO_POOL",
								},
								{
									Name:                "Water & Heating", // new target, should be added
									TargetType:          "EXPENSE",
									Priority:            2,
									AmountMinor:         25000,
									Currency:            "PLN",
									RecurrenceCadence:   "MONTHLY",
									NonSpendingStrategy: "RETURN_TO_POOL",
								},
							},
						},
					},
				},
			},
		}

		bCount, tCount, err := ApplyBudgetStructure(ctx, tenantDB, structure, agentID, corrID)
		if err != nil {
			t.Fatalf("ApplyBudgetStructure failed on MERGE: %v", err)
		}
		if bCount != 0 {
			t.Errorf("expected 0 new buckets created (reused existing), got %d", bCount)
		}
		if tCount != 1 {
			t.Errorf("expected 1 new target created, got %d", tCount)
		}

		// Verify group name was updated
		var updatedGroup string
		err = tenantDB.QueryRowContext(ctx, `SELECT group_name FROM buckets_projection WHERE id = ?`, bRentID).Scan(&updatedGroup)
		if err != nil || updatedGroup != "New Housing Group" {
			t.Errorf("expected group to be updated to 'New Housing Group', got %q", updatedGroup)
		}

		// Verify target count for Rent is 2
		var activeTargets int
		err = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection WHERE bucket_id = ? AND is_archived = 0`, bRentID).Scan(&activeTargets)
		if err != nil || activeTargets != 2 {
			t.Errorf("expected 2 active targets in Rent, got %d", activeTargets)
		}
	})

	t.Run("ConflictResolution REPLACE", func(t *testing.T) {
		structure := &ai.BudgetStructure{
			Groups: []ai.ProposedGroup{
				{
					GroupName: "Utilities",
					Buckets: []ai.ProposedBucket{
						{
							Name:               "Internet",
							ConflictResolution: "REPLACE",
							Targets: []ai.ProposedTarget{
								{
									Name:                "New Fiber 1Gbps",
									TargetType:          "EXPENSE",
									Priority:            3,
									AmountMinor:         11000,
									Currency:            "PLN",
									RecurrenceCadence:   "MONTHLY",
									NonSpendingStrategy: "RETURN_TO_POOL",
								},
							},
						},
					},
				},
			},
		}

		bCount, tCount, err := ApplyBudgetStructure(ctx, tenantDB, structure, agentID, corrID)
		if err != nil {
			t.Fatalf("ApplyBudgetStructure failed on REPLACE: %v", err)
		}
		if bCount != 0 {
			t.Errorf("expected 0 new buckets, got %d", bCount)
		}
		if tCount != 1 {
			t.Errorf("expected 1 target created, got %d", tCount)
		}

		// Check old target is archived
		var oldArchived int
		err = tenantDB.QueryRowContext(ctx, `SELECT is_archived FROM targets_projection WHERE bucket_id = ? AND name = 'Old Fiber'`, bNetID).Scan(&oldArchived)
		if err != nil || oldArchived != 1 {
			t.Errorf("expected Old Fiber to be archived (1), got %d (err: %v)", oldArchived, err)
		}

		// Check new target is active
		var newActive int
		err = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection WHERE bucket_id = ? AND name = 'New Fiber 1Gbps' AND is_archived = 0`, bNetID).Scan(&newActive)
		if err != nil || newActive != 1 {
			t.Errorf("expected New Fiber 1Gbps to be active, got %d", newActive)
		}
	})

	t.Run("ConflictResolution SKIP", func(t *testing.T) {
		structure := &ai.BudgetStructure{
			Groups: []ai.ProposedGroup{
				{
					GroupName: "Lifestyle",
					Buckets: []ai.ProposedBucket{
						{
							Name:               "Entertainment",
							ConflictResolution: "SKIP",
							Targets: []ai.ProposedTarget{
								{
									Name:                "Cinema & Theater",
									TargetType:          "EXPENSE",
									Priority:            1,
									AmountMinor:         15000,
									Currency:            "PLN",
									RecurrenceCadence:   "MONTHLY",
									NonSpendingStrategy: "RETURN_TO_POOL",
								},
							},
						},
					},
				},
			},
		}

		bCount, tCount, err := ApplyBudgetStructure(ctx, tenantDB, structure, agentID, corrID)
		if err != nil {
			t.Fatalf("ApplyBudgetStructure failed on SKIP: %v", err)
		}
		if bCount != 0 || tCount != 0 {
			t.Errorf("expected 0 buckets and 0 targets created for SKIP, got b=%d, t=%d", bCount, tCount)
		}

		var skipTargets int
		err = tenantDB.QueryRowContext(ctx, `SELECT count(*) FROM targets_projection WHERE bucket_id = ? AND is_archived = 0`, bSkipID).Scan(&skipTargets)
		if err != nil || skipTargets != 0 {
			t.Errorf("expected 0 targets in Entertainment, got %d", skipTargets)
		}
	})
}

func TestDetectAndEnrichStructureConflicts(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDB(t)
	defer tenantDB.Close()

	agentID := domain.NewUUIDv7()
	corrID := domain.NewUUIDv7()

	// Create buckets in different groups
	_, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Housing",
		Name:          "Rent",
		AgentID:       agentID,
		CorrelationID: corrID,
	})
	if err != nil {
		t.Fatalf("failed creating Rent bucket: %v", err)
	}

	_, err = CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Living",
		Name:          "Groceries",
		AgentID:       agentID,
		CorrelationID: corrID,
	})
	if err != nil {
		t.Fatalf("failed creating Groceries bucket: %v", err)
	}

	structure := &ai.BudgetStructure{
		Groups: []ai.ProposedGroup{
			{
				GroupName: "Fixed",
				Buckets: []ai.ProposedBucket{
					{Name: "rent"}, // lowercase to test case-insensitivity
					{Name: "Gym Pass"},
				},
			},
			{
				GroupName: "Variable",
				Buckets: []ai.ProposedBucket{
					{Name: " GROCERIES "}, // whitespace to test trimming
				},
			},
		},
	}

	err = DetectAndEnrichStructureConflicts(ctx, tenantDB, structure)
	if err != nil {
		t.Fatalf("DetectAndEnrichStructureConflicts failed: %v", err)
	}

	if len(structure.ExistingGroups) != 2 {
		t.Errorf("expected 2 existing groups, got: %v", structure.ExistingGroups)
	}

	bRent := structure.Groups[0].Buckets[0]
	if !bRent.IsExisting || bRent.ExistingGroup != "Housing" {
		t.Errorf("expected rent to be flagged as existing in 'Housing', got IsExisting=%v, Group=%s", bRent.IsExisting, bRent.ExistingGroup)
	}

	bGym := structure.Groups[0].Buckets[1]
	if bGym.IsExisting {
		t.Errorf("expected Gym Pass to not be existing, got IsExisting=true")
	}

	bGroc := structure.Groups[1].Buckets[0]
	if !bGroc.IsExisting || bGroc.ExistingGroup != "Living" {
		t.Errorf("expected GROCERIES to be flagged as existing in 'Living', got IsExisting=%v, Group=%s", bGroc.IsExisting, bGroc.ExistingGroup)
	}
}

