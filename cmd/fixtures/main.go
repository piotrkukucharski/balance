package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/balance/balance/internal/clock"
	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/service"
)

// LogBuffer captures log records into an in-memory buffer so they are silent unless an error occurs.
type LogBuffer struct {
	buf *bytes.Buffer
}

func setupBufferingLogger() *LogBuffer {
	buf := &bytes.Buffer{}
	logger.SetOutput(buf, slog.LevelInfo)
	return &LogBuffer{buf: buf}
}

func (l *LogBuffer) DumpToStderr() {
	if l.buf.Len() > 0 {
		fmt.Fprintf(os.Stderr, "\n--- Diagnostic Logs Captured During Execution ---\n%s-------------------------------------------------\n", l.buf.String())
	}
}

// TerminalProgressReporter renders clean progress bars and step summaries to stderr/stdout.
type TerminalProgressReporter struct {
	totalSteps int
	current    int
	stepName   string
}

func (r *TerminalProgressReporter) StartStep(name string, totalItems int) {
	r.stepName = name
	r.totalSteps = totalItems
	r.current = 0
	fmt.Printf("  ▶ %s...\n", name)
	r.renderBar()
}

func (r *TerminalProgressReporter) Advance(n int) {
	r.current += n
	r.renderBar()
}

func (r *TerminalProgressReporter) renderBar() {
	if r.totalSteps <= 0 {
		return
	}
	width := 30
	pct := float64(r.current) / float64(r.totalSteps)
	if pct > 1.0 {
		pct = 1.0
	}
	filled := int(pct * float64(width))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	fmt.Printf("\r    [%s] %3.0f%% (%d/%d)", bar, pct*100, r.current, r.totalSteps)
	if r.current >= r.totalSteps {
		fmt.Print("\n")
	}
}

func (r *TerminalProgressReporter) CompleteStep(summary string) {
	if r.current < r.totalSteps {
		fmt.Print("\n")
	}
	fmt.Printf("  ✓ %s\n\n", summary)
}

func parseDateOrTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("date string is empty")
	}
	// Try YYYY-MM-DD
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), nil
	}
	// Try RFC3339
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid date format '%s', expected YYYY-MM-DD or RFC3339", raw)
}

func initService(dataDir string) (*service.AppService, func(), error) {
	if dataDir == "" {
		dataDir = os.Getenv("DATA_DIR")
	}
	if dataDir == "" {
		dataDir = "./data"
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("create data dir: %w", err)
	}

	mainDBPath := filepath.Join(dataDir, "main.db")
	mainDB, err := db.OpenDB(mainDBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open main db: %w", err)
	}

	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		mainDB.Close()
		return nil, nil, fmt.Errorf("apply main migrations: %w", err)
	}

	budgetsDir := filepath.Join(dataDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 64)
	if err != nil {
		mainDB.Close()
		return nil, nil, fmt.Errorf("init tenant mgr: %w", err)
	}

	clk := clock.NewSystemClock(time.UTC)
	svc := service.NewAppService(mainDB, tenantMgr, clk)

	cleanup := func() {
		tenantMgr.CloseAll()
		mainDB.Close()
	}
	return svc, cleanup, nil
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	args := os.Args[2:]

	logBuf := setupBufferingLogger()

	var err error
	switch command {
	case "create-user":
		err = handleCreateUser(args)
	case "create-bucket":
		err = handleCreateBucket(args)
	case "load":
		err = handleLoadFixtures(args)
	case "remove-bucket":
		err = handleRemoveBucket(args)
	case "remove-user":
		err = handleRemoveUser(args)
	case "all":
		err = handleRunAll(args)
	case "help", "-h", "--help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "Unknown command '%s'\n\n", command)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ ERROR: %v\n", err)
		logBuf.DumpToStderr()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`Balance Fixtures CLI Utility

Usage:
  fixtures <command> [flags]

Commands:
  create-user     Create user account with login, password, userId and firstActionAt
  create-bucket   Create budget tenant in main.db with bucketId, owner, timezone and firstActionAt
  load            Load rich fixtures (4 accounts, >=15 buckets, multi-currency transactions)
  remove-bucket   Remove budget and tenant sqlite database
  remove-user     Remove user account
  all             Run entire fixture lifecycle sequentially with progress indicators
`)
}

func handleCreateUser(args []string) error {
	fs := flag.NewFlagSet("create-user", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultDataDir := os.Getenv("DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "./data"
	}

	login := fs.String("login", "", "User login / email (required)")
	password := fs.String("password", "", "User password min 8 characters (required)")
	userID := fs.String("user-id", "", "UUIDv7 user identifier (required)")
	firstActionAt := fs.String("first-action-at", "", "Initial creation date YYYY-MM-DD (required)")
	displayName := fs.String("display-name", "", "Display name (optional)")
	dataDir := fs.String("data-dir", defaultDataDir, "Data directory")
	corrID := fs.String("correlation-id", "", "Correlation ID")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Validate required fields
	if *login == "" {
		return fmt.Errorf("flag --login is required")
	}
	if *password == "" {
		return fmt.Errorf("flag --password is required")
	}
	if *userID == "" {
		return fmt.Errorf("flag --user-id is required")
	}
	if *firstActionAt == "" {
		return fmt.Errorf("flag --first-action-at is required")
	}

	actionDate, err := parseDateOrTime(*firstActionAt)
	if err != nil {
		return fmt.Errorf("invalid --first-action-at: %w", err)
	}

	if *corrID == "" {
		*corrID = domain.NewUUIDv7()
	}

	svc, cleanup, err := initService(*dataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, _ := logger.WithCorrelationID(context.Background(), *corrID)

	fmt.Printf("▶ Creating user '%s' (%s)...\n", *login, *userID)
	id, err := svc.CreateUser(ctx, service.CreateUserInput{
		ID:            *userID,
		Email:         *login,
		Password:      *password,
		DisplayName:   *displayName,
		FirstActionAt: &actionDate,
	})
	if err != nil {
		return err
	}

	fmt.Printf("✓ User created successfully (userId: %s)\n", id)
	return nil
}

func handleCreateBucket(args []string) error {
	fs := flag.NewFlagSet("create-bucket", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultDataDir := os.Getenv("DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "./data"
	}

	bucketID := fs.String("bucket-id", "", "UUIDv7 budget / bucket identifier (required)")
	name := fs.String("name", "", "Budget name (required)")
	ownerUserID := fs.String("owner-user-id", "", "Owner user ID (required)")
	timezone := fs.String("timezone", "", "Timezone e.g. Europe/Warsaw (required)")
	firstActionAt := fs.String("first-action-at", "", "Creation date YYYY-MM-DD (required)")
	currency := fs.String("currency", "PLN", "Default currency")
	dataDir := fs.String("data-dir", defaultDataDir, "Data directory")
	corrID := fs.String("correlation-id", "", "Correlation ID")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *bucketID == "" {
		return fmt.Errorf("flag --bucket-id is required")
	}
	if *name == "" {
		return fmt.Errorf("flag --name is required")
	}
	if *ownerUserID == "" {
		return fmt.Errorf("flag --owner-user-id is required")
	}
	if *timezone == "" {
		return fmt.Errorf("flag --timezone is required")
	}
	if *firstActionAt == "" {
		return fmt.Errorf("flag --first-action-at is required")
	}

	actionDate, err := parseDateOrTime(*firstActionAt)
	if err != nil {
		return fmt.Errorf("invalid --first-action-at: %w", err)
	}

	if *corrID == "" {
		*corrID = domain.NewUUIDv7()
	}

	svc, cleanup, err := initService(*dataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, _ := logger.WithCorrelationID(context.Background(), *corrID)

	fmt.Printf("▶ Provisioning budget bucket '%s' (%s) for owner %s...\n", *name, *bucketID, *ownerUserID)
	id, err := svc.CreateBudget(ctx, service.CreateBudgetInput{
		ID:              *bucketID,
		Name:            *name,
		OwnerUserID:     *ownerUserID,
		DefaultCurrency: *currency,
		Timezone:        *timezone,
		FirstActionAt:   &actionDate,
		CorrelationID:   *corrID,
	})
	if err != nil {
		return err
	}

	fmt.Printf("✓ Budget bucket provisioned successfully (bucketId: %s)\n", id)
	return nil
}

func handleLoadFixtures(args []string) error {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultDataDir := os.Getenv("DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "./data"
	}

	bucketID := fs.String("bucket-id", "", "Budget ID (required)")
	userID := fs.String("user-id", "", "User ID with OWNER role (required)")
	lastTxForm := fs.String("last-transaction", "", "Date of last transaction YYYY-MM-DD (required)")
	firstActionAt := fs.String("first-action-at", "", "First action date YYYY-MM-DD (required)")
	dataDir := fs.String("data-dir", defaultDataDir, "Data directory")
	corrID := fs.String("correlation-id", "", "Correlation ID")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *bucketID == "" {
		return fmt.Errorf("flag --bucket-id is required")
	}
	if *userID == "" {
		return fmt.Errorf("flag --user-id is required")
	}
	if *lastTxForm == "" {
		return fmt.Errorf("flag --last-transaction is required")
	}
	if *firstActionAt == "" {
		return fmt.Errorf("flag --first-action-at is required")
	}

	lastTxDate, err := parseDateOrTime(*lastTxForm)
	if err != nil {
		return fmt.Errorf("invalid --last-transaction: %w", err)
	}

	actionDate, err := parseDateOrTime(*firstActionAt)
	if err != nil {
		return fmt.Errorf("invalid --first-action-at: %w", err)
	}

	if *corrID == "" {
		*corrID = domain.NewUUIDv7()
	}

	svc, cleanup, err := initService(*dataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, _ := logger.WithCorrelationID(context.Background(), *corrID)
	reporter := &TerminalProgressReporter{}

	fmt.Printf("▶ Loading fixtures into budget %s (owner: %s)...\n", *bucketID, *userID)
	err = svc.LoadFixtures(ctx, service.LoadFixturesInput{
		BudgetID:        *bucketID,
		UserID:          *userID,
		LastTransaction: lastTxDate,
		FirstActionAt:   actionDate,
		CorrelationID:   *corrID,
		Reporter:        reporter,
	})
	if err != nil {
		return err
	}

	fmt.Println("✓ Fixtures loaded successfully!")
	return nil
}

func handleRemoveBucket(args []string) error {
	fs := flag.NewFlagSet("remove-bucket", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultDataDir := os.Getenv("DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "./data"
	}

	bucketID := fs.String("bucket-id", "", "Budget ID to delete (required)")
	dataDir := fs.String("data-dir", defaultDataDir, "Data directory")
	corrID := fs.String("correlation-id", "", "Correlation ID")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bucketID == "" {
		return fmt.Errorf("flag --bucket-id is required")
	}

	if *corrID == "" {
		*corrID = domain.NewUUIDv7()
	}

	svc, cleanup, err := initService(*dataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, _ := logger.WithCorrelationID(context.Background(), *corrID)

	fmt.Printf("▶ Removing budget bucket %s...\n", *bucketID)
	if err := svc.RemoveBudget(ctx, *bucketID); err != nil {
		return err
	}
	fmt.Printf("✓ Budget bucket %s removed successfully\n", *bucketID)
	return nil
}

func handleRemoveUser(args []string) error {
	fs := flag.NewFlagSet("remove-user", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultDataDir := os.Getenv("DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "./data"
	}

	userID := fs.String("user-id", "", "User ID to delete (required)")
	dataDir := fs.String("data-dir", defaultDataDir, "Data directory")
	corrID := fs.String("correlation-id", "", "Correlation ID")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *userID == "" {
		return fmt.Errorf("flag --user-id is required")
	}

	if *corrID == "" {
		*corrID = domain.NewUUIDv7()
	}

	svc, cleanup, err := initService(*dataDir)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, _ := logger.WithCorrelationID(context.Background(), *corrID)

	fmt.Printf("▶ Removing user %s...\n", *userID)
	if err := svc.RemoveUser(ctx, *userID); err != nil {
		return err
	}
	fmt.Printf("✓ User %s removed successfully\n", *userID)
	return nil
}

func handleRunAll(args []string) error {
	fs := flag.NewFlagSet("all", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultDataDir := os.Getenv("DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "./data"
	}

	login := fs.String("login", "", "User login / email (required)")
	password := fs.String("password", "", "User password min 8 characters (required)")
	userID := fs.String("user-id", "", "UUIDv7 user identifier (required)")
	budgetName := fs.String("name", "", "Budget name (required)")
	bucketID := fs.String("bucket-id", "", "UUIDv7 budget / bucket identifier (required)")
	timezone := fs.String("timezone", "", "Timezone e.g. Europe/Warsaw (required)")
	lastTxForm := fs.String("last-transaction", "", "Date of last transaction YYYY-MM-DD (required)")
	firstActionAt := fs.String("first-action-at", "", "First action date YYYY-MM-DD (required)")
	dataDir := fs.String("data-dir", defaultDataDir, "Data directory")
	corrID := fs.String("correlation-id", "", "Shared correlation ID across all steps")
	clean := fs.Bool("clean", false, "Clean up user and budget if they already exist before creating")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Validate required fields
	if *login == "" {
		return fmt.Errorf("flag --login is required")
	}
	if *password == "" {
		return fmt.Errorf("flag --password is required")
	}
	if *userID == "" {
		return fmt.Errorf("flag --user-id is required")
	}
	if *budgetName == "" {
		return fmt.Errorf("flag --name is required")
	}
	if *bucketID == "" {
		return fmt.Errorf("flag --bucket-id is required")
	}
	if *timezone == "" {
		return fmt.Errorf("flag --timezone is required")
	}
	if *firstActionAt == "" {
		return fmt.Errorf("flag --first-action-at is required")
	}
	if *lastTxForm == "" {
		return fmt.Errorf("flag --last-transaction is required")
	}

	firstDate, err := parseDateOrTime(*firstActionAt)
	if err != nil {
		return fmt.Errorf("invalid --first-action-at: %w", err)
	}

	lastDate, err := parseDateOrTime(*lastTxForm)
	if err != nil {
		return fmt.Errorf("invalid --last-transaction: %w", err)
	}

	if *corrID == "" {
		*corrID = domain.NewUUIDv7()
	}

	// If --clean is enabled, remove existing user and budget first
	if *clean {
		svc, cleanup, err := initService(*dataDir)
		if err != nil {
			return err
		}
		ctx, _ := logger.WithCorrelationID(context.Background(), *corrID)
		fmt.Printf("▶ [--clean] Cleaning up existing user (%s) and budget (%s) if present...\n", *login, *bucketID)
		_ = svc.CleanExistingUserAndBudgets(ctx, *login, *bucketID)
		cleanup()
		fmt.Println("✓ Cleanup completed.")
	}

	fmt.Println("==================================================================")
	fmt.Println("              BALANCE FIXTURES PROVISIONING PIPELINE              ")
	fmt.Println("==================================================================")
	fmt.Printf("Correlation ID:  %s\n", *corrID)
	fmt.Printf("User ID:         %s (%s)\n", *userID, *login)
	fmt.Printf("Budget ID:       %s ('%s', %s)\n", *bucketID, *budgetName, *timezone)
	fmt.Printf("Date Range:      %s → %s (%d days)\n",
		firstDate.Format("2006-01-02"), lastDate.Format("2006-01-02"), int(lastDate.Sub(firstDate).Hours()/24))
	fmt.Println("------------------------------------------------------------------")

	// Step 1: Create User
	fmt.Println("\n[1/3] Step 1: Create User")
	userArgs := []string{
		"--login=" + *login,
		"--password=" + *password,
		"--user-id=" + *userID,
		"--first-action-at=" + firstDate.Format("2006-01-02"),
		"--correlation-id=" + *corrID,
		"--data-dir=" + *dataDir,
	}
	if err := handleCreateUser(userArgs); err != nil {
		return fmt.Errorf("step 1 failed (create-user): %w", err)
	}

	// Step 2: Create Budget
	fmt.Println("\n[2/3] Step 2: Create Budget")
	budgetArgs := []string{
		"--bucket-id=" + *bucketID,
		"--name=" + *budgetName,
		"--owner-user-id=" + *userID,
		"--timezone=" + *timezone,
		"--first-action-at=" + firstDate.Format("2006-01-02"),
		"--correlation-id=" + *corrID,
		"--data-dir=" + *dataDir,
	}
	if err := handleCreateBucket(budgetArgs); err != nil {
		return fmt.Errorf("step 2 failed (create-bucket): %w", err)
	}

	// Step 3: Load Fixtures
	fmt.Println("\n[3/3] Step 3: Load Fixtures")
	loadArgs := []string{
		"--bucket-id=" + *bucketID,
		"--user-id=" + *userID,
		"--first-action-at=" + firstDate.Format("2006-01-02"),
		"--last-transaction=" + lastDate.Format("2006-01-02"),
		"--correlation-id=" + *corrID,
		"--data-dir=" + *dataDir,
	}
	if err := handleLoadFixtures(loadArgs); err != nil {
		return fmt.Errorf("step 3 failed (load fixtures): %w", err)
	}

	fmt.Println("==================================================================")
	fmt.Println("✓ Entire fixture pipeline executed successfully with 0 errors!")
	fmt.Println("==================================================================")
	return nil
}
