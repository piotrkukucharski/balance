package budget

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/logger"
)

// AdvanceAllBudgetsOverdueTargets iterates over all budgets in mainDB, loads their tenantDBs and timezones,
// and advances overdue target due dates.
func AdvanceAllBudgetsOverdueTargets(ctx context.Context, mainDB *sql.DB, tenantMgr *db.TenantDBManager) (int, int, error) {
	if mainDB == nil || tenantMgr == nil {
		return 0, 0, nil
	}

	rows, err := mainDB.QueryContext(ctx, `SELECT id, timezone FROM budgets`)
	if err != nil {
		return 0, 0, fmt.Errorf("failed querying budgets: %w", err)
	}
	defer rows.Close()

	totalChecked := 0
	totalAdvanced := 0

	for rows.Next() {
		var budgetID, timezone string
		if err := rows.Scan(&budgetID, &timezone); err != nil {
			continue
		}

		tenantDB, err := tenantMgr.GetTenantDB(budgetID, false)
		if err != nil {
			logger.Error(ctx, "Failed getting tenant DB for budget target advance", "budget_id", budgetID, "error", err.Error())
			continue
		}

		res, err := AdvanceOverdueTargetDueDatesForTenant(ctx, tenantDB, timezone)
		if err != nil {
			logger.Error(ctx, "Failed advancing targets for budget", "budget_id", budgetID, "error", err.Error())
			continue
		}
		totalChecked += res.TargetsChecked
		totalAdvanced += res.TargetsAdvanced
	}

	return totalChecked, totalAdvanced, nil
}

// RunAdvanceTargetsCLI executes the worker:advance-targets CLI command with argument parsing.
func RunAdvanceTargetsCLI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("worker:advance-targets", flag.ContinueOnError)
	dataDir := fs.String("data-dir", os.Getenv("DATA_DIR"), "Path to data directory containing main.db and tenant DBs")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	dir := *dataDir
	if dir == "" {
		dir = "./data"
	}

	mainDBPath := filepath.Join(dir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		return fmt.Errorf("failed opening main DB at %s: %w", mainDBPath, err)
	}
	defer mainDB.Close()

	tenantMgr, err := db.NewTenantDBManager(dir, mainDB, 16)
	if err != nil {
		return fmt.Errorf("failed creating tenant manager: %w", err)
	}
	logger.Info(ctx, "Starting advance targets worker", "db", mainDBPath)

	checked, advanced, err := AdvanceAllBudgetsOverdueTargets(ctx, mainDB, tenantMgr)
	if err != nil {
		logger.Error(ctx, "Advance targets worker failed", "error", err.Error())
		return err
	}

	logger.Info(ctx, "Target due date advancement completed", "checked", checked, "advanced", advanced)
	fmt.Printf("Successfully processed %d targets, advanced %d overdue targets to next cycle\n", checked, advanced)
	return nil
}
