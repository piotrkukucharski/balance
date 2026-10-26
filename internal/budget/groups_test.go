package budget

import (
	"context"
	"database/sql"
	"testing"

	"github.com/balance/balance/internal/db"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestTenantDBForGroups(t *testing.T) *sql.DB {
	t.Helper()
	tenantDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed opening in-memory db: %v", err)
	}

	migrationContent, err := db.MigrationsFS.ReadFile("migrations/tenant/00001_init_tenant.sql")
	if err != nil {
		t.Fatalf("failed reading initial migration: %v", err)
	}
	if _, err := tenantDB.Exec(string(migrationContent)); err != nil {
		t.Fatalf("failed executing migration 00001: %v", err)
	}

	groupsMigration, err := db.MigrationsFS.ReadFile("migrations/tenant/00005_bucket_groups.sql")
	if err != nil {
		t.Fatalf("failed reading groups migration: %v", err)
	}
	if _, err := tenantDB.Exec(string(groupsMigration)); err != nil {
		t.Fatalf("failed executing migration 00005: %v", err)
	}

	return tenantDB
}

func TestBucketGroupsReorderingAndRenaming(t *testing.T) {
	ctx := context.Background()
	tenantDB := setupTestTenantDBForGroups(t)
	defer tenantDB.Close()

	// 1. Create initial buckets with different groups
	b1, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Housing",
		Name:          "Rent",
		AgentID:       "user-1",
		CorrelationID: "c-1",
	})
	if err != nil {
		t.Fatalf("failed creating bucket 1: %v", err)
	}

	b2, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Living",
		Name:          "Groceries",
		AgentID:       "user-1",
		CorrelationID: "c-2",
	})
	if err != nil {
		t.Fatalf("failed creating bucket 2: %v", err)
	}

	b3, err := CreateBucket(ctx, tenantDB, CreateBucketInput{
		GroupName:     "Fun",
		Name:          "Cinema",
		AgentID:       "user-1",
		CorrelationID: "c-3",
	})
	if err != nil {
		t.Fatalf("failed creating bucket 3: %v", err)
	}

	// 2. List bucket groups - should auto-populate from buckets_projection
	groups, err := ListBucketGroups(ctx, tenantDB)
	if err != nil {
		t.Fatalf("failed listing bucket groups: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	// Initial order should be Fun, Housing, Living (alphabetical by row_number)
	if groups[0].Name != "Fun" || groups[1].Name != "Housing" || groups[2].Name != "Living" {
		t.Errorf("unexpected initial group ordering: %v, %v, %v", groups[0].Name, groups[1].Name, groups[2].Name)
	}

	// 3. Move "Living" up
	if err := MoveBucketGroup(ctx, tenantDB, "Living", "up"); err != nil {
		t.Fatalf("failed moving Living up: %v", err)
	}

	groups, err = ListBucketGroups(ctx, tenantDB)
	if err != nil {
		t.Fatalf("failed listing bucket groups: %v", err)
	}
	// Expected: Fun, Living, Housing
	if groups[0].Name != "Fun" || groups[1].Name != "Living" || groups[2].Name != "Housing" {
		t.Errorf("unexpected order after move up: %v, %v, %v", groups[0].Name, groups[1].Name, groups[2].Name)
	}

	// 4. Move "Living" up again to top
	if err := MoveBucketGroup(ctx, tenantDB, "Living", "up"); err != nil {
		t.Fatalf("failed moving Living up to top: %v", err)
	}

	groups, err = ListBucketGroups(ctx, tenantDB)
	if err != nil {
		t.Fatalf("failed listing bucket groups: %v", err)
	}
	// Expected: Living, Fun, Housing
	if groups[0].Name != "Living" || groups[1].Name != "Fun" || groups[2].Name != "Housing" {
		t.Errorf("unexpected order after move up to top: %v, %v, %v", groups[0].Name, groups[1].Name, groups[2].Name)
	}

	// 5. Rename "Living" to "Daily Living"
	if err := RenameBucketGroup(ctx, tenantDB, RenameBucketGroupInput{
		OldName:       "Living",
		NewName:       "Daily Living",
		AgentID:       "user-1",
		CorrelationID: "c-rename",
	}); err != nil {
		t.Fatalf("failed renaming bucket group: %v", err)
	}

	// Check buckets_projection was updated
	var updatedBucketGroup string
	var updatedVersion int
	err = tenantDB.QueryRow(`SELECT group_name, version FROM buckets_projection WHERE id = ?`, b2).Scan(&updatedBucketGroup, &updatedVersion)
	if err != nil {
		t.Fatalf("failed querying updated bucket: %v", err)
	}
	if updatedBucketGroup != "Daily Living" {
		t.Errorf("expected bucket group to be 'Daily Living', got %q", updatedBucketGroup)
	}
	if updatedVersion != 2 {
		t.Errorf("expected bucket version 2, got %d", updatedVersion)
	}

	// Verify bucket_events recorded
	var eventCount int
	err = tenantDB.QueryRow(`SELECT count(*) FROM bucket_events WHERE aggregate_id = ? AND event_type = ?`, b2, EventTypeBucketUpdated).Scan(&eventCount)
	if err != nil || eventCount != 1 {
		t.Errorf("expected 1 BUCKET_UPDATED event, got %d (err: %v)", eventCount, err)
	}

	// Check groups list reflects new name and preserved position
	groups, err = ListBucketGroups(ctx, tenantDB)
	if err != nil {
		t.Fatalf("failed listing bucket groups after rename: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	if groups[0].Name != "Daily Living" || groups[1].Name != "Fun" || groups[2].Name != "Housing" {
		t.Errorf("unexpected order after rename: %v, %v, %v", groups[0].Name, groups[1].Name, groups[2].Name)
	}

	_ = b1
	_ = b3
}
