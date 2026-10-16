package heavyfixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/balance/balance/internal/db"
)

func TestGenerate10YearHeavyDataset(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "balance_heavy_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "heavy.db")
	tenantDB, err := db.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tenantDB.Close()

	if err := db.ApplyMigrations(tenantDB, "migrations/tenant"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	err = Generate10YearHeavyDataset(ctx, tenantDB, 123456789)
	if err != nil {
		t.Fatalf("Generate10YearHeavyDataset failed: %v", err)
	}

	var partyCount, acctCount, bucketCount, targetCount, ruleCount, txCount int
	tenantDB.QueryRow("SELECT count(*) FROM parties_current").Scan(&partyCount)
	tenantDB.QueryRow("SELECT count(*) FROM accounts_current").Scan(&acctCount)
	tenantDB.QueryRow("SELECT count(*) FROM buckets_projection").Scan(&bucketCount)
	tenantDB.QueryRow("SELECT count(*) FROM targets_projection").Scan(&targetCount)
	tenantDB.QueryRow("SELECT count(*) FROM matching_rules_current").Scan(&ruleCount)
	tenantDB.QueryRow("SELECT count(*) FROM budget_transactions_current").Scan(&txCount)

	if partyCount != 1001 { // 1 user-owned + 1000 external
		t.Fatalf("expected 1001 parties, got %d", partyCount)
	}
	if acctCount != 1215 { // 15 user-owned + 1200 external
		t.Fatalf("expected 1215 accounts, got %d", acctCount)
	}
	if bucketCount != 60 {
		t.Fatalf("expected 60 buckets, got %d", bucketCount)
	}
	if targetCount != 150 {
		t.Fatalf("expected 150 targets, got %d", targetCount)
	}
	if ruleCount != 250 {
		t.Fatalf("expected 250 matching rules, got %d", ruleCount)
	}
	if txCount != 500 {
		t.Fatalf("expected 500 sample transactions, got %d", txCount)
	}
}
