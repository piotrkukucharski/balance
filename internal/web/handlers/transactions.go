package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/integration/gocardless"
	"github.com/balance/balance/internal/integration/mock"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/rule"
	"github.com/balance/balance/internal/service"
	"github.com/balance/balance/internal/transaction"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

type TransactionsHandler struct {
	mainDB           *sql.DB
	tenantMgr        *db.TenantDBManager
	mockAdapter      *mock.MockAdapter
	gocardlessClient *gocardless.Client
	syncMu           sync.Mutex
	activeSyncs      map[string]bool
	syncListeners    map[string][]chan string
	appService       *service.AppService
}

func NewTransactionsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *TransactionsHandler {
	secretID := os.Getenv("GOCARDLESS_SECRET_ID")
	secretKey := os.Getenv("GOCARDLESS_SECRET_KEY")
	redirectURL := os.Getenv("GOCARDLESS_REDIRECT_URL")
	var gcClient *gocardless.Client
	if secretID != "" && secretKey != "" {
		gcClient = gocardless.NewClient(secretID, secretKey, redirectURL)
	}

	return &TransactionsHandler{
		mainDB:           mainDB,
		tenantMgr:        tenantMgr,
		mockAdapter:      mock.NewMockAdapter(),
		gocardlessClient: gcClient,
		activeSyncs:      make(map[string]bool),
		syncListeners:    make(map[string][]chan string),
	}
}

func (h *TransactionsHandler) SetGoCardlessClient(gc *gocardless.Client) {
	h.gocardlessClient = gc
}

func (h *TransactionsHandler) SetService(svc *service.AppService) {
	h.appService = svc
}

type txBucketOption struct {
	ID   string
	Name string
}

type txAccountItem struct {
	ID               string
	Name             string
	Currency         string
	BalanceFormatted string
	IsActiveFilter   bool
	IsBankLinked     bool
	HasHistory       bool
}

type txLedgerRow struct {
	ID                string
	AmountMinor       int64
	Currency          string
	DateTag           template.HTML
	BucketName        string
	TargetName        string
	Notes             string
	SourceAccountName string
	DestAccountName   string
	IsIncome          bool
	IsTransfer        bool
	AmountClass       string
	AmountPrefix      string
	AmountFormatted   string
	ActionMenuHTML    template.HTML
}

type txRawRow struct {
	ID                string
	AmountMinor       int64
	Currency          string
	DateTag           template.HTML
	AccountName       string
	Payee             string
	Reference         string
	IsPendingHold     bool
	IsMatchedRule     bool
	SuggestedBucketID string
	AmountBadgeClass  string
	AmountPrefix      string
	AmountFormatted   string
	ActionMenuHTML    template.HTML
}

type transactionsViewData struct {
	DateFormat              string
	TimeFormat              string
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	HasLinkedBankAccounts   bool
	NeedsInitialSyncModal   bool
	IsAllAccountsActive     bool
	FilterPartyID           string
	FilteredPartyName       string
	PendingRawCount         int
	MatchedRulesCount       int
	TodayDateStr            string
	PartySuggestions        []string
	Buckets                 []txBucketOption
	Accounts                []txAccountItem
	RawTransactions         []txRawRow
	LedgerTransactions      []txLedgerRow
	PageConfigJSON          template.JS
	AllTargetsJSON          template.JS
}

// TransactionsView renders the unified transactions ledger, triage workbench, and rapid entry toolbar.
func (h *TransactionsHandler) TransactionsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	role := c.Get(ContextRoleKey).(string)
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var budgetName, defaultCurr string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr)
	if defaultCurr == "" {
		defaultCurr = "PLN"
	}

	filterAcctID := c.QueryParam("account_id")
	filterPartyID := c.QueryParam("party_id")

	// 1. Fetch buckets and targets for dropdowns
	var buckets []txBucketOption
	bucketRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name FROM buckets_projection
		WHERE is_archived = 0
		ORDER BY name ASC`)
	if err == nil {
		defer bucketRows.Close()
		for bucketRows.Next() {
			var bID, bName string
			bucketRows.Scan(&bID, &bName)
			buckets = append(buckets, txBucketOption{ID: bID, Name: bName})
		}
	}

	// Fetch targets map grouped by bucket for client-side selection
	type clientTarget struct {
		ID       string `json:"id"`
		BucketID string `json:"bucket_id"`
		Name     string `json:"name"`
		Currency string `json:"currency"`
	}
	targetsList := make([]clientTarget, 0)
	targetRows, tErr := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, bucket_id, COALESCE(name, 'Priority ' || priority), currency
		FROM targets_projection
		WHERE is_archived = 0
		ORDER BY priority DESC, sort_order ASC`)
	if tErr == nil {
		defer targetRows.Close()
		for targetRows.Next() {
			var ct clientTarget
			if err := targetRows.Scan(&ct.ID, &ct.BucketID, &ct.Name, &ct.Currency); err == nil {
				targetsList = append(targetsList, ct)
			}
		}
	}
	targetsJSONBytes, _ := json.Marshal(targetsList)
	pageConfigBytes, _ := json.Marshal(map[string]any{
		"activeBudgetId": budgetID,
		"allTargets":     targetsList,
	})

	// 2. Fetch user checking/cash accounts with balances for pills & dropdowns
	acctRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT a.id, a.name, a.primary_currency, COALESCE(acb.balance_minor, 0),
		       COALESCE(ail.id, '')
		FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		LEFT JOIN account_currency_balances acb ON a.id = acb.account_id AND a.primary_currency = acb.currency
		LEFT JOIN account_integration_links ail ON a.id = ail.account_id
		WHERE p.is_user_owned = 1 AND a.is_closed = 0
		ORDER BY a.name ASC`)
	var accounts []txAccountItem
	linkedBankAcctCount := 0
	uninitializedBankAcctCount := 0

	var filteredPartyName string
	if filterPartyID != "" {
		tenantDB.QueryRowContext(c.Request().Context(), `SELECT name FROM parties_current WHERE id = ?`, filterPartyID).Scan(&filteredPartyName)
	}

	if err == nil {
		defer acctRows.Close()
		for acctRows.Next() {
			var aID, aName, aCurr, ailID string
			var aBalMinor int64
			acctRows.Scan(&aID, &aName, &aCurr, &aBalMinor, &ailID)

			aBalMajor := float64(aBalMinor) / 100.0
			isBankLinked := ailID != ""
			hasHistory := false
			if isBankLinked {
				linkedBankAcctCount++
				// Check if this account already has any raw or budget transactions
				var rawCnt, btxCnt int
				_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT COUNT(*) FROM raw_transactions_current WHERE account_id = ?`, aID).Scan(&rawCnt)
				_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT COUNT(*) FROM budget_transactions_current WHERE source_account_id = ? OR destination_account_id = ?`, aID, aID).Scan(&btxCnt)
				hasHistory = (rawCnt + btxCnt) > 0
				if !hasHistory {
					uninitializedBankAcctCount++
				}
			}

			accounts = append(accounts, txAccountItem{
				ID:               aID,
				Name:             aName,
				Currency:         aCurr,
				BalanceFormatted: fmt.Sprintf("%.2f", aBalMajor),
				IsActiveFilter:   filterAcctID != "" && filterAcctID == aID,
				IsBankLinked:     isBankLinked,
				HasHistory:       hasHistory,
			})
		}
	}

	// 3. Fetch external parties for autocomplete suggestions
	var partySuggestions []string
	partyRows, pErr := tenantDB.QueryContext(c.Request().Context(), `
		SELECT DISTINCT name FROM parties_current
		WHERE is_user_owned = 0 AND name != ''
		ORDER BY name ASC
		LIMIT 100`)
	if pErr == nil {
		defer partyRows.Close()
		for partyRows.Next() {
			var pName string
			partyRows.Scan(&pName)
			partySuggestions = append(partySuggestions, pName)
		}
	}

	// 4. Fetch budget transactions (filtered by account or party if specified)
	query := `
		SELECT bt.id, bt.source_amount_minor, bt.source_currency, bt.transaction_date,
		       COALESCE(bt.notes, ''), COALESCE(bp.name, 'Unassigned'), COALESCE(bt.bucket_id, ''),
		       COALESCE(bt.target_id, ''), COALESCE(tp.name, ''),
		       bt.source_account_id, bt.destination_account_id,
		       sa.name, da.name, sp.is_user_owned, dp.is_user_owned
		FROM budget_transactions_current bt
		LEFT JOIN buckets_projection bp ON bt.bucket_id = bp.id
		LEFT JOIN targets_projection tp ON bt.target_id = tp.id
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN parties_current dp ON da.party_id = dp.id`
	var queryArgs []any
	var whereClauses []string
	if filterAcctID != "" {
		whereClauses = append(whereClauses, `(bt.source_account_id = ? OR bt.destination_account_id = ?)`)
		queryArgs = append(queryArgs, filterAcctID, filterAcctID)
	}
	if filterPartyID != "" {
		whereClauses = append(whereClauses, `(sa.party_id = ? OR da.party_id = ?)`)
		queryArgs = append(queryArgs, filterPartyID, filterPartyID)
	}
	if len(whereClauses) > 0 {
		query += ` WHERE ` + strings.Join(whereClauses, " AND ")
	}
	query += ` ORDER BY bt.transaction_date DESC LIMIT 150`

	rows, err := tenantDB.QueryContext(c.Request().Context(), query, queryArgs...)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed loading transactions: "+err.Error())
	}
	defer rows.Close()

	var ledgerRows []txLedgerRow
	csrfToken, _ := c.Get("csrfToken").(string)

	for rows.Next() {
		var id, curr, notes, bucketName, bucketIDVal, targetIDVal, targetName, srcAcctID, dstAcctID, srcAcctName, dstAcctName string
		var amountMinor, txDate int64
		var srcUserOwned, dstUserOwned int
		rows.Scan(&id, &amountMinor, &curr, &txDate, &notes, &bucketName, &bucketIDVal,
			&targetIDVal, &targetName,
			&srcAcctID, &dstAcctID, &srcAcctName, &dstAcctName, &srcUserOwned, &dstUserOwned)

		dateStr := time.UnixMicro(txDate).Format("2006-01-02")
		dateTag := RenderTimeTagMicro(txDate, false, c)
		amountMajor := float64(amountMinor) / 100.0

		escapedNotes := strings.ReplaceAll(notes, "'", "\\'")
		escapedNotes = strings.ReplaceAll(escapedNotes, "\"", "&quot;")

		amountClass := "text-error"
		amountPrefix := "-"
		isIncome := false
		isTransfer := false

		if srcUserOwned == 0 && dstUserOwned == 1 {
			// Income: external party -> user account
			amountPrefix = "+"
			amountClass = "text-success"
			isIncome = true
		} else if srcUserOwned == 1 && dstUserOwned == 1 {
			// Internal transfer
			amountPrefix = "⇄ "
			amountClass = "text-info"
			isTransfer = true
		}

		actionMenu := RenderPopoverActionMenu("tx", id, []TableActionItem{
			{
				Label: "🕒 History",
				HXGet: fmt.Sprintf("/budgets/%s/history/budget_transactions/%s", budgetID, id),
			},
			{
				Label:   "✏️ Edit",
				OnClick: fmt.Sprintf("openEditTxModal('%s', '%.2f', '%s', '%s', '%s', '%s', '%s')", id, amountMajor, dateStr, escapedNotes, bucketIDVal, targetIDVal, srcAcctID),
			},
			{
				Label:      "🗑️ Delete",
				FormAction: fmt.Sprintf("/budgets/%s/transactions/%s/delete", budgetID, id),
				CSRFToken:  csrfToken,
				IsDanger:   true,
				ConfirmMsg: "Are you sure you want to delete this transaction?",
			},
		})

		ledgerRows = append(ledgerRows, txLedgerRow{
			ID:                id,
			AmountMinor:       amountMinor,
			Currency:          curr,
			DateTag:           template.HTML(dateTag),
			BucketName:        bucketName,
			TargetName:        targetName,
			Notes:             notes,
			SourceAccountName: srcAcctName,
			DestAccountName:   dstAcctName,
			IsIncome:          isIncome,
			IsTransfer:        isTransfer,
			AmountClass:       amountClass,
			AmountPrefix:      amountPrefix,
			AmountFormatted:   fmt.Sprintf("%.2f", amountMajor),
			ActionMenuHTML:    template.HTML(actionMenu),
		})
	}

	// 5. Fetch pending raw transactions (unresolved bank feeds)
	rawRows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT r.id, r.account_id, r.booking_date, r.amount_minor, r.currency,
		       COALESCE(r.raw_payee, ''), COALESCE(r.counterparty_iban, ''), COALESCE(r.reference, ''),
		       a.name, COALESCE(r.booking_status, 'BOOKED')
		FROM raw_transactions_current r
		JOIN accounts_current a ON r.account_id = a.id
		WHERE r.status = 'PENDING'
		ORDER BY r.booking_date DESC
		LIMIT 100`)
	var rawTxRows []txRawRow
	pendingRawCount := 0
	matchedRulesCount := 0

	if err == nil {
		defer rawRows.Close()

		rulesEngine, _ := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)

		for rawRows.Next() {
			pendingRawCount++
			var rID, rAcctID, rCurrency, rPayee, rIBAN, rRef, rAcctName, rBookingStatus string
			var rDate, rAmountMinor int64
			rawRows.Scan(&rID, &rAcctID, &rDate, &rAmountMinor, &rCurrency, &rPayee, &rIBAN, &rRef, &rAcctName, &rBookingStatus)

			rDateTag := RenderTimeTagMicro(rDate, false, c)
			rAmountMajor := float64(rAmountMinor) / 100.0

			amountBadgeClass := "text-error"
			amountPrefix := ""
			if rAmountMinor > 0 {
				amountBadgeClass = "text-success"
				amountPrefix = "+"
			}

			// Suggest rule match if available
			suggestedBucketID := ""
			isPendingHold := rBookingStatus == "PENDING"
			isMatchedRule := false
			if !isPendingHold && rulesEngine != nil {
				if match := rulesEngine.Match(rPayee, rIBAN, rRef); match != nil && match.DefaultBucketID != nil {
					suggestedBucketID = *match.DefaultBucketID
					isMatchedRule = true
					matchedRulesCount++
				}
			}

			escapedPayee := strings.ReplaceAll(rPayee, "'", "\\'")
			escapedPayee = strings.ReplaceAll(escapedPayee, "\"", "&quot;")

			var rawActionItems []TableActionItem
			if isPendingHold {
				rawActionItems = []TableActionItem{
					{
						Label: "🕒 History",
						HXGet: fmt.Sprintf("/budgets/%s/history/raw_transactions/%s", budgetID, rID),
					},
					{
						Label:      "🗑️ Delete",
						FormAction: fmt.Sprintf("/budgets/%s/transactions/raw/%s/delete", budgetID, rID),
						CSRFToken:  csrfToken,
						IsDanger:   true,
						ConfirmMsg: "Permanently delete this bank transaction feed?",
					},
				}
			} else {
				rawActionItems = []TableActionItem{
					{
						Label: "🕒 History",
						HXGet: fmt.Sprintf("/budgets/%s/history/raw_transactions/%s", budgetID, rID),
					},
					{
						Label:   "➡️ Detailed Resolve Modal",
						OnClick: fmt.Sprintf("openResolveRawModal('%s', '%s', '%.2f', '%s', '%s', '%s')", rID, rAcctID, rAmountMajor, rCurrency, escapedPayee, suggestedBucketID),
					},
					{
						Label:      "🗑️ Delete",
						FormAction: fmt.Sprintf("/budgets/%s/transactions/raw/%s/delete", budgetID, rID),
						CSRFToken:  csrfToken,
						IsDanger:   true,
						ConfirmMsg: "Permanently delete this bank transaction feed?",
					},
				}
			}
			rawActionMenu := RenderPopoverActionMenu("raw", rID, rawActionItems)

			rawTxRows = append(rawTxRows, txRawRow{
				ID:                rID,
				AmountMinor:       rAmountMinor,
				Currency:          rCurrency,
				DateTag:           template.HTML(rDateTag),
				AccountName:       rAcctName,
				Payee:             rPayee,
				Reference:         rRef,
				IsPendingHold:     isPendingHold,
				IsMatchedRule:     isMatchedRule,
				SuggestedBucketID: suggestedBucketID,
				AmountBadgeClass:  amountBadgeClass,
				AmountPrefix:      amountPrefix,
				AmountFormatted:   fmt.Sprintf("%.2f", rAmountMajor),
				ActionMenuHTML:    template.HTML(rawActionMenu),
			})
		}
	}

	todayDateStr := time.Now().UTC().Format("2006-01-02")
	dFormat, tFormat := GetUserDateTimePreferences(c)

	viewData := transactionsViewData{
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		BudgetID:                budgetID,
		BudgetName:              budgetName,
		CSRFToken:               csrfToken,
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "transactions")),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "transactions")),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
		HasLinkedBankAccounts:   linkedBankAcctCount > 0,
		NeedsInitialSyncModal:   uninitializedBankAcctCount > 0,
		IsAllAccountsActive:     filterAcctID == "" && filterPartyID == "",
		FilterPartyID:           filterPartyID,
		FilteredPartyName:       filteredPartyName,
		PendingRawCount:         pendingRawCount,
		MatchedRulesCount:       matchedRulesCount,
		TodayDateStr:            todayDateStr,
		PartySuggestions:        partySuggestions,
		Buckets:                 buckets,
		Accounts:                accounts,
		RawTransactions:         rawTxRows,
		LedgerTransactions:      ledgerRows,
		PageConfigJSON:          template.JS(pageConfigBytes),
		AllTargetsJSON:          template.JS(targetsJSONBytes),
	}

	renderedHTML, renderErr := templates.RenderString("transactions/view", viewData)
	if renderErr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering transactions template: "+renderErr.Error())
	}

	_ = role
	return c.HTML(http.StatusOK, renderedHTML)
}

// SyncBankEvents streams real-time sync progress and completion events via SSE.
func (h *TransactionsHandler) SyncBankEvents(c echo.Context) error {
	budgetID := c.Param("budgetId")
	corrID, _ := c.Get("correlationID").(string)

	reqCtx := c.Request().Context()
	if corrID != "" {
		reqCtx, _ = logger.WithCorrelationID(reqCtx, corrID)
	}
	logCtx := logger.WithComponent(reqCtx, "biz:transaction:bank_sync:sse")
	logger.Info(logCtx, "Client connected to bank sync SSE stream", "budget_id", budgetID)

	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	msgChan := make(chan string, 10)
	h.syncMu.Lock()
	h.syncListeners[budgetID] = append(h.syncListeners[budgetID], msgChan)
	h.syncMu.Unlock()

	defer func() {
		h.syncMu.Lock()
		defer h.syncMu.Unlock()
		listeners := h.syncListeners[budgetID]
		for i, ch := range listeners {
			if ch == msgChan {
				h.syncListeners[budgetID] = append(listeners[:i], listeners[i+1:]...)
				break
			}
		}
		close(msgChan)
		logger.Info(logCtx, "Client disconnected from bank sync SSE stream", "budget_id", budgetID)
	}()

	// Send initial ping
	fmt.Fprintf(w, "event: ping\ndata: connected\n\n")
	w.Flush()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-msgChan:
			if !ok {
				return nil
			}
			fmt.Fprint(w, msg)
			w.Flush()
		}
	}
}

func (h *TransactionsHandler) broadcastSyncEvent(ctx context.Context, budgetID, corrID, event, data string) {
	logCtx := ctx
	if corrID != "" {
		logCtx, _ = logger.WithCorrelationID(logCtx, corrID)
	}
	logCtx = logger.WithComponent(logCtx, "biz:transaction:bank_sync:event")

	h.syncMu.Lock()
	defer h.syncMu.Unlock()

	listeners := h.syncListeners[budgetID]
	logger.Info(logCtx, "Broadcasting sync SSE event to clients",
		"budget_id", budgetID,
		"event", event,
		"data", data,
		"listeners_count", len(listeners),
	)

	payload := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	for _, ch := range listeners {
		select {
		case ch <- payload:
		default:
		}
	}
}

type accountSyncResult struct {
	AccountID     string
	ImportedCount int
	Error         error
}

func (h *TransactionsHandler) executeAccountSync(ctx context.Context, tenantDB *sql.DB, userAcctID, userID, corrID string, daysWindow int) (int, error) {
	logCtx := logger.WithComponent(ctx, "biz:transaction:bank_sync:account")
	now := time.Now().UTC().UnixMicro()

	// Ensure account_integration_sync_logs table exists
	_, err := tenantDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS account_integration_sync_logs (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			synced_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_sync_logs_acct_time ON account_integration_sync_logs(account_id, synced_at DESC);
	`)
	if err != nil {
		logger.Error(logCtx, "Failed to ensure sync logs table", "error", err.Error())
	}

	// Check if this account is linked to an integration
	var integrationID, extAcctID string
	err = tenantDB.QueryRowContext(ctx, `
		SELECT COALESCE(integration_id, ''), COALESCE(external_account_id, '') FROM account_integration_links
		WHERE account_id = ? LIMIT 1`, userAcctID).Scan(&integrationID, &extAcctID)
	if err != nil || integrationID == "" {
		logger.Warn(logCtx, "Bank sync rejected: account is not linked to any bank integration", "account_id", userAcctID)
		return 0, fmt.Errorf("account is not linked to any bank integration")
	}

	var provider, institutionID string
	if integrationID != "" {
		_ = h.mainDB.QueryRowContext(ctx, `
			SELECT COALESCE(provider, ''), COALESCE(institution_id, '') FROM user_integrations
			WHERE id = ?`, integrationID).Scan(&provider, &institutionID)
	}

	isMockAccount := false
	if provider == "MOCK" || strings.HasPrefix(integrationID, "mock_") || strings.HasPrefix(institutionID, "MOCK_") || strings.Contains(strings.ToUpper(institutionID), "SANDBOX") {
		isMockAccount = true
	}

	if isMockAccount && !isDevMode() {
		logger.Warn(logCtx, "Bank sync rejected: mock bank integration not allowed in production", "account_id", userAcctID)
		return 0, fmt.Errorf("mock bank integrations are disabled in production environment")
	}

	isGoCardless := provider == "GOCARDLESS" && h.gocardlessClient != nil && extAcctID != "" && !strings.HasPrefix(institutionID, "MOCK_")

	// Rate limit check: max 4 imports per account in the last 24 hours (86400 seconds) - exempt for mock/sandbox accounts
	if !isMockAccount {
		oneDayAgo := now - 24*3600*1_000_000
		var syncCount24h int
		err = tenantDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM account_integration_sync_logs
			WHERE account_id = ? AND synced_at >= ?`, userAcctID, oneDayAgo).Scan(&syncCount24h)
		if err == nil && syncCount24h >= 4 {
			logger.Warn(logCtx, "Sync rejected: rate limit reached (max 4 per day)",
				"account_id", userAcctID,
				"sync_count", syncCount24h,
			)
			return 0, fmt.Errorf("RATE_LIMIT: Daily bank synchronization limit reached for account (maximum 4 imports per 24 hours)")
		}
	}

	// Fetch primary currency for this account
	var acctCurrency string
	err = tenantDB.QueryRowContext(ctx, `
		SELECT primary_currency FROM accounts_current WHERE id = ?`, userAcctID).Scan(&acctCurrency)
	if err != nil {
		acctCurrency = "PLN"
	}

	// Check if this account already has any booked transactions
	var latestRawDate, latestBudgetTxDate sql.NullInt64
	_ = tenantDB.QueryRowContext(ctx, `
		SELECT MAX(booking_date) FROM raw_transactions_current
		WHERE account_id = ? AND booking_status = 'BOOKED'`, userAcctID).Scan(&latestRawDate)
	_ = tenantDB.QueryRowContext(ctx, `
		SELECT MAX(transaction_date) FROM budget_transactions_current
		WHERE source_account_id = ? OR destination_account_id = ?`, userAcctID, userAcctID).Scan(&latestBudgetTxDate)

	var latestKnownDate int64
	if latestRawDate.Valid && latestRawDate.Int64 > latestKnownDate {
		latestKnownDate = latestRawDate.Int64
	}
	if latestBudgetTxDate.Valid && latestBudgetTxDate.Int64 > latestKnownDate {
		latestKnownDate = latestBudgetTxDate.Int64
	}

	isFirstSync := latestKnownDate == 0

	// Determine sinceMicro
	var sinceMicro int64
	if !isFirstSync {
		// Incremental sync: only transactions newer than latest imported booking date
		sinceMicro = latestKnownDate
	} else {
		// First sync: calculate window from user input (clamp between 1 and 90 days)
		days := daysWindow
		if days < 1 {
			days = 1
		} else if days > 90 {
			days = 90
		}
		sinceMicro = now - int64(days)*24*3600*1_000_000
	}

	type incomingTx struct {
		ExternalID       string
		BookingDateMicro int64
		AmountMinor      int64
		Currency         string
		Payee            string
		CounterpartyIBAN string
		Reference        string
		BookingStatus    string // "BOOKED" or "PENDING"
	}

	var incoming []incomingTx
	var bankBalanceMinor int64

	if isGoCardless {
		dateFrom := time.UnixMicro(sinceMicro).Format("2006-01-02")
		txResp, txErr := h.gocardlessClient.GetAccountTransactions(ctx, extAcctID, dateFrom, "")
		if txErr != nil {
			logger.Error(logCtx, "GoCardless GetAccountTransactions failed",
				"account_id", userAcctID,
				"external_account_id", extAcctID,
				"error", txErr.Error(),
			)
			return 0, fmt.Errorf("bank transaction fetch failed: %w", txErr)
		}

		for _, b := range txResp.Transactions.Booked {
			amt, aErr := b.GetAmountMinor()
			if aErr != nil {
				continue
			}
			curr := b.TransactionAmount.Currency
			if curr == "" {
				curr = acctCurrency
			}
			incoming = append(incoming, incomingTx{
				ExternalID:       b.GetExternalID(),
				BookingDateMicro: b.GetBookingDateMicro(),
				AmountMinor:      amt,
				Currency:         curr,
				Payee:            b.GetPayee(),
				CounterpartyIBAN: b.GetCounterpartyIBAN(),
				Reference:        b.GetReference(),
				BookingStatus:    "BOOKED",
			})
		}

		for _, p := range txResp.Transactions.Pending {
			amt, aErr := p.GetAmountMinor()
			if aErr != nil {
				continue
			}
			curr := p.TransactionAmount.Currency
			if curr == "" {
				curr = acctCurrency
			}
			incoming = append(incoming, incomingTx{
				ExternalID:       p.GetExternalID(),
				BookingDateMicro: p.GetBookingDateMicro(),
				AmountMinor:      amt,
				Currency:         curr,
				Payee:            p.GetPayee(),
				CounterpartyIBAN: p.GetCounterpartyIBAN(),
				Reference:        p.GetReference(),
				BookingStatus:    "PENDING",
			})
		}

		// Fetch available balance from GoCardless
		balResp, balErr := h.gocardlessClient.GetAccountBalances(ctx, extAcctID)
		if balErr == nil && balResp != nil && len(balResp.Balances) > 0 {
			var chosenBal *gocardless.AccountBalance
			findBal := func(types ...string) *gocardless.AccountBalance {
				for _, t := range types {
					for i := range balResp.Balances {
						if strings.EqualFold(balResp.Balances[i].BalanceType, t) {
							return &balResp.Balances[i]
						}
					}
				}
				return nil
			}
			// User choice: Available balance preferred (interimAvailable, authorised, forwardAvailable)
			// fallback to closingBooked, interimBooked, or first balance
			chosenBal = findBal("interimAvailable", "authorised", "forwardAvailable")
			if chosenBal == nil {
				chosenBal = findBal("closingBooked", "interimBooked")
			}
			if chosenBal == nil && len(balResp.Balances) > 0 {
				chosenBal = &balResp.Balances[0]
			}
			if chosenBal != nil {
				var f float64
				if _, err := fmt.Sscanf(strings.TrimSpace(chosenBal.BalanceAmount.Amount), "%f", &f); err == nil {
					bankBalanceMinor = int64(math.Round(f * 100))
				}
			}
		}
	} else {
		// Mock adapter
		extTxs, err := h.mockAdapter.FetchTransactions(ctx, userAcctID, sinceMicro)
		if err != nil {
			logger.Error(logCtx, "Bank sync failed fetching transactions from adapter", "account_id", userAcctID, "error", err.Error())
			return 0, err
		}
		for _, tx := range extTxs {
			bStatus := tx.BookingStatus
			if bStatus == "" {
				bStatus = "BOOKED"
			}
			incoming = append(incoming, incomingTx{
				ExternalID:       tx.ExternalTxID,
				BookingDateMicro: tx.BookingDateMicro,
				AmountMinor:      tx.AmountMinor,
				Currency:         tx.Currency,
				Payee:            tx.Payee,
				CounterpartyIBAN: tx.CounterpartyIBAN,
				Reference:        tx.Reference,
				BookingStatus:    bStatus,
			})
		}

		extAccounts, _ := h.mockAdapter.FetchAccounts(ctx, userID)
		for _, ea := range extAccounts {
			if ea.Currency == acctCurrency {
				bankBalanceMinor = ea.BalanceMinor
				break
			}
		}
		if bankBalanceMinor == 0 && len(extAccounts) > 0 {
			bankBalanceMinor = extAccounts[0].BalanceMinor
		}
	}

	// Fetch existing pending holds for this account to reconcile or remove expired holds
	type oldPending struct {
		id         string
		externalID string
		amount     int64
		payee      string
		date       int64
	}
	var existingPending []oldPending
	pRows, pErr := tenantDB.QueryContext(ctx, `
		SELECT id, COALESCE(external_transaction_id, ''), amount_minor, raw_payee, booking_date
		FROM raw_transactions_current
		WHERE account_id = ? AND status = 'PENDING' AND booking_status = 'PENDING'`, userAcctID)
	if pErr == nil && pRows != nil {
		for pRows.Next() {
			var op oldPending
			pRows.Scan(&op.id, &op.externalID, &op.amount, &op.payee, &op.date)
			existingPending = append(existingPending, op)
		}
		pRows.Close()
	}

	retainedPendingIDs := make(map[string]bool)
	importedCount := 0
	var sumImportedDelta int64
	var oldestTxDate int64 = now

	for _, inc := range incoming {
		if inc.BookingStatus == "BOOKED" {
			// Check if this booked transaction matches any existing pending hold
			var matchedPendingID string
			for _, op := range existingPending {
				if !retainedPendingIDs[op.id] {
					if (op.externalID != "" && op.externalID == inc.ExternalID) ||
						(op.amount == inc.AmountMinor && op.payee == inc.Payee && math.Abs(float64(op.date-inc.BookingDateMicro)) < 7*86400*1_000_000) {
						matchedPendingID = op.id
						break
					}
				}
			}

			if matchedPendingID != "" {
				// Transition pending hold to BOOKED
				dedup := transaction.ComputeCanonicalDedupHash(userAcctID, inc.ExternalID, inc.BookingDateMicro, inc.AmountMinor, inc.Currency, inc.Payee, inc.Reference)
				res, err := tenantDB.ExecContext(ctx, `
					UPDATE raw_transactions_current
					SET booking_status = 'BOOKED',
					    external_transaction_id = ?,
					    dedup_hash = ?,
					    booking_date = ?,
					    amount_minor = ?,
					    currency = ?,
					    raw_payee = ?,
					    counterparty_iban = ?,
					    reference = ?,
					    updated_at = ?
					WHERE id = ?`,
					inc.ExternalID, dedup, inc.BookingDateMicro, inc.AmountMinor, inc.Currency, inc.Payee, inc.CounterpartyIBAN, inc.Reference, now, matchedPendingID,
				)
				if err == nil {
					if rows, _ := res.RowsAffected(); rows > 0 {
						retainedPendingIDs[matchedPendingID] = true
						importedCount++
						sumImportedDelta += inc.AmountMinor
						if inc.BookingDateMicro < oldestTxDate {
							oldestTxDate = inc.BookingDateMicro
						}
						continue
					}
				}
			}
		}

		// Insert transaction
		dedup := transaction.ComputeCanonicalDedupHash(userAcctID, inc.ExternalID, inc.BookingDateMicro, inc.AmountMinor, inc.Currency, inc.Payee, inc.Reference)
		rawID := domain.NewUUIDv7()

		res, err := tenantDB.ExecContext(ctx, `
			INSERT INTO raw_transactions_current (
				id, version, account_id, external_transaction_id, dedup_hash, booking_date,
				amount_minor, currency, raw_payee, counterparty_iban, reference, status, booking_status, updated_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?, ?)
			ON CONFLICT(account_id, dedup_hash) DO NOTHING`,
			rawID, userAcctID, inc.ExternalID, dedup, inc.BookingDateMicro,
			inc.AmountMinor, inc.Currency, inc.Payee, inc.CounterpartyIBAN, inc.Reference, inc.BookingStatus, now,
		)
		if err == nil {
			if rows, _ := res.RowsAffected(); rows > 0 {
				importedCount++
				if inc.BookingStatus == "BOOKED" {
					sumImportedDelta += inc.AmountMinor
					if inc.BookingDateMicro < oldestTxDate {
						oldestTxDate = inc.BookingDateMicro
					}
				} else {
					retainedPendingIDs[rawID] = true
				}
			} else if inc.BookingStatus == "PENDING" {
				var existingID string
				_ = tenantDB.QueryRowContext(ctx, `SELECT id FROM raw_transactions_current WHERE account_id = ? AND dedup_hash = ?`, userAcctID, dedup).Scan(&existingID)
				if existingID != "" {
					retainedPendingIDs[existingID] = true
				}
			}
		}
	}

	// Remove stale pending holds that were neither booked nor returned in bank's current pending list
	for _, op := range existingPending {
		if !retainedPendingIDs[op.id] {
			_, _ = tenantDB.ExecContext(ctx, `DELETE FROM raw_transactions_current WHERE id = ? AND booking_status = 'PENDING'`, op.id)
			logger.Info(logCtx, "Removed expired or dropped bank pending hold", "raw_id", op.id, "payee", op.payee, "amount", op.amount)
		}
	}

	// If it's the first sync, ensure 'Set Balance' party and virtual external account exist,
	// then record an initial Set Balance transaction so starting balance + pending transactions = real bank balance.
	if isFirstSync && bankBalanceMinor != 0 {
		var setBalancePartyID, setBalanceAcctID string
		err := tenantDB.QueryRowContext(ctx, `
			SELECT p.id, a.id FROM parties_current p
			JOIN accounts_current a ON a.party_id = p.id
			WHERE p.name = 'Set Balance' AND p.is_user_owned = 0
			LIMIT 1`).Scan(&setBalancePartyID, &setBalanceAcctID)
		if err != nil {
			setBalancePartyID = domain.NewUUIDv7()
			setBalanceAcctID = domain.NewUUIDv7()
			tenantDB.ExecContext(ctx, `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'Set Balance', 'ORGANIZATION', 0, ?, ?)`, setBalancePartyID, userID, now)
			tenantDB.ExecContext(ctx, `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, 'Set Balance Offset', 'EXTERNAL', ?, 0, ?, ?)`, setBalanceAcctID, setBalancePartyID, acctCurrency, userID, now)
		}

		// Initial offset = Bank Balance - Net sum of imported transactions in window
		startingBalanceMinor := bankBalanceMinor - sumImportedDelta
		if startingBalanceMinor > 0 {
			notes := "Starting Balance (Set Balance Offset)"
			_, err = transaction.ExecuteResolution(ctx, tenantDB, transaction.ResolveResolutionInput{
				OperationType: transaction.OpDirectMove,
				BudgetTransactions: []transaction.BudgetTxInput{
					{
						SourceAccountID:      setBalanceAcctID,
						DestinationAccountID: userAcctID,
						SourceAmountMinor:    startingBalanceMinor,
						SourceCurrency:       acctCurrency,
						DestAmountMinor:      startingBalanceMinor,
						DestCurrency:         acctCurrency,
						BucketID:             nil, // Ready to assign
						Notes:                &notes,
						TransactionDate:      oldestTxDate,
					},
				},
				AgentID:       userID,
				CorrelationID: corrID,
			})
			if err != nil {
				logger.Error(logCtx, "Failed creating initial Set Balance transaction", "error", err.Error())
			} else {
				logger.Info(logCtx, "Created Set Balance starting transaction",
					"account_id", userAcctID,
					"bank_balance", bankBalanceMinor,
					"sum_imported_delta", sumImportedDelta,
					"starting_balance", startingBalanceMinor,
					"tx_date", oldestTxDate,
				)
			}
		}
	}

	// Update last_synced_at on account_integration_links
	_, _ = tenantDB.ExecContext(ctx, `
		UPDATE account_integration_links
		SET last_synced_at = ?
		WHERE account_id = ?`, now, userAcctID)

	// Log successful sync for rate limit accounting
	logID := domain.NewUUIDv7()
	_, _ = tenantDB.ExecContext(ctx, `
		INSERT INTO account_integration_sync_logs (id, account_id, synced_at)
		VALUES (?, ?, ?)`, logID, userAcctID, now)

	logger.Info(logCtx, "Account bank sync succeeded",
		"account_id", userAcctID,
		"is_first_sync", isFirstSync,
		"imported_count", importedCount,
	)

	return importedCount, nil
}

// SyncWithBank triggers bank integration synchronization, imports raw transactions and runs auto-matching.
func (h *TransactionsHandler) SyncWithBank(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:bank_sync")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	logger.Info(ctx, "Starting bank transaction synchronization",
		"budget_id", budgetID,
		"user_id", userID,
	)

	// Concurrency check: Reject if a bank sync is already in progress for this budget
	h.syncMu.Lock()
	if h.activeSyncs[budgetID] {
		h.syncMu.Unlock()
		logger.Warn(ctx, "Concurrent bank sync rejected", "budget_id", budgetID)
		if c.Request().Header.Get("HX-Request") == "true" {
			alertHTML, _ := templates.RenderString("transactions/sync_conflict_alert", nil)
			return c.HTML(http.StatusConflict, alertHTML)
		}
		return echo.NewHTTPError(http.StatusConflict, "Bank synchronization is already running for this budget. Please wait.")
	}
	h.activeSyncs[budgetID] = true
	h.syncMu.Unlock()

	// Parse days_window (default 30)
	daysWindow := 30
	if dStr := c.FormValue("days_window"); dStr != "" {
		if parsedDays, err := strconv.Atoi(dStr); err == nil {
			daysWindow = parsedDays
		}
	}

	// Parse accounts to sync (support multi-select "account_ids" or single "account_id")
	var accountIDs []string
	formParams, err := c.FormParams()
	if err == nil {
		accountIDs = formParams["account_ids"]
	}
	if len(accountIDs) == 0 {
		if singleID := c.FormValue("account_id"); singleID != "" {
			accountIDs = append(accountIDs, singleID)
		}
	}

	// If still empty, fetch all user-owned accounts linked to bank integrations
	if len(accountIDs) == 0 {
		rows, qErr := tenantDB.QueryContext(c.Request().Context(), `
			SELECT a.id FROM accounts_current a
			JOIN parties_current p ON a.party_id = p.id
			JOIN account_integration_links ail ON a.id = ail.account_id
			WHERE p.is_user_owned = 1 AND a.is_closed = 0
			ORDER BY a.name ASC`)
		if qErr == nil {
			defer rows.Close()
			for rows.Next() {
				var acctID string
				if err := rows.Scan(&acctID); err == nil {
					accountIDs = append(accountIDs, acctID)
				}
			}
		}
		if len(accountIDs) == 0 {
			logger.Warn(ctx, "Bank sync rejected: no bank-linked accounts found for synchronization", "budget_id", budgetID)
			return echo.NewHTTPError(http.StatusBadRequest, "No bank-linked accounts available for synchronization. Please link a bank account first.")
		}
	}

	isAsync := c.Request().Header.Get("HX-Request") == "true" || c.FormValue("async") == "true"

	syncFunc := func(opCtx context.Context) ([]accountSyncResult, error) {
		defer func() {
			h.syncMu.Lock()
			delete(h.activeSyncs, budgetID)
			h.syncMu.Unlock()
		}()

		var results []accountSyncResult
		totalImported := 0
		var firstErr error

		for i, acctID := range accountIDs {
			h.broadcastSyncEvent(opCtx, budgetID, corrID, "sync-progress", fmt.Sprintf(`{"account_id":"%s","index":%d,"total":%d,"correlation_id":"%s"}`, acctID, i+1, len(accountIDs), corrID))
			imported, sErr := h.executeAccountSync(opCtx, tenantDB, acctID, userID, corrID, daysWindow)
			results = append(results, accountSyncResult{
				AccountID:     acctID,
				ImportedCount: imported,
				Error:         sErr,
			})
			if sErr != nil && firstErr == nil {
				firstErr = sErr
			}
			totalImported += imported
		}

		if firstErr != nil {
			h.broadcastSyncEvent(opCtx, budgetID, corrID, "sync-error", fmt.Sprintf(`{"error":"%s","correlation_id":"%s"}`, firstErr.Error(), corrID))
		} else {
			h.broadcastSyncEvent(opCtx, budgetID, corrID, "sync-complete", fmt.Sprintf(`{"imported_count":%d,"accounts_count":%d,"correlation_id":"%s"}`, totalImported, len(accountIDs), corrID))
		}

		return results, firstErr
	}

	if isAsync {
		bgCtx := context.WithoutCancel(c.Request().Context())
		go func() {
			defer logger.RecoverGoroutine(bgCtx, "async_bank_sync")
			_, _ = syncFunc(bgCtx)
		}()
		alertHTML, _ := templates.RenderString("transactions/sync_accepted_alert", nil)
		return c.HTML(http.StatusAccepted, alertHTML)
	}

	// Synchronous execution
	results, syncErr := syncFunc(c.Request().Context())
	if syncErr != nil {
		if strings.HasPrefix(syncErr.Error(), "RATE_LIMIT:") {
			return echo.NewHTTPError(http.StatusTooManyRequests, syncErr.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, syncErr.Error())
	}

	_ = results
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// AddManualTransaction handles direct cash transaction entry.
func parseFlexibleAmount(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty amount")
	}
	// Support simple addition like 15+4.50
	if strings.Contains(raw, "+") {
		parts := strings.Split(raw, "+")
		var total float64
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			var f float64
			if _, err := fmt.Sscanf(p, "%f", &f); err == nil {
				total += f
			}
		}
		return int64(total * 100), nil
	}
	var f float64
	if _, err := fmt.Sscanf(raw, "%f", &f); err != nil {
		return 0, err
	}
	return int64(f * 100), nil
}

func findOrCreateExternalAccount(ctx context.Context, tenantDB *sql.DB, partyName, currency, userID string, now int64) (string, error) {
	cleanName := strings.TrimSpace(partyName)
	if cleanName == "" {
		cleanName = "General Merchant"
	}
	var counterpartyAcctID string
	err := tenantDB.QueryRowContext(ctx, `
		SELECT a.id FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.name = ? AND p.is_user_owned = 0
		LIMIT 1`, cleanName).Scan(&counterpartyAcctID)
	if err == nil {
		return counterpartyAcctID, nil
	}

	pID := domain.NewUUIDv7()
	counterpartyAcctID = domain.NewUUIDv7()
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
		VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`, pID, cleanName, userID, now)
	if err != nil {
		return "", err
	}
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
		VALUES (?, 1, ?, ?, 'EXTERNAL', ?, 0, ?, ?)`, counterpartyAcctID, pID, cleanName, currency, userID, now)
	if err != nil {
		return "", err
	}
	return counterpartyAcctID, nil
}

// AddManualTransaction handles direct cash transaction entry (Expense, Income, or Transfer).
func (h *TransactionsHandler) AddManualTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:manual")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	amountMinor, err := parseFlexibleAmount(c.FormValue("amount"))
	if err != nil || amountMinor <= 0 {
		logger.Warn(ctx, "Manual transaction rejected: invalid amount", "raw_amount", c.FormValue("amount"))
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid transaction amount")
	}

	// Parse custom transaction date if provided (YYYY-MM-DD)
	txDate := now
	dateForm := c.FormValue("transaction_date")
	if dateForm != "" {
		if parsedDate, err := time.Parse("2006-01-02", dateForm); err == nil {
			txDate = parsedDate.UTC().UnixMicro()
		}
	}

	txType := strings.ToUpper(strings.TrimSpace(c.FormValue("tx_type")))
	if txType == "" {
		txType = "EXPENSE"
	}

	if h.appService != nil {
		tType := service.TxExpense
		switch txType {
		case "INCOME":
			tType = service.TxIncome
		case "TRANSFER":
			tType = service.TxTransfer
		}

		var bIDPtr *string
		bVal := c.FormValue("bucket_id")
		if bVal != "" {
			bIDPtr = &bVal
		}

		var tIDPtr *string
		tVal := c.FormValue("target_id")
		if tVal != "" && bIDPtr != nil {
			tIDPtr = &tVal
		}

		var dMinor int64
		if dAmt := c.FormValue("dest_amount"); dAmt != "" {
			if parsedDAmt, err := parseFlexibleAmount(dAmt); err == nil && parsedDAmt > 0 {
				dMinor = parsedDAmt
			}
		}

		opID, err := h.appService.AddTransaction(c.Request().Context(), service.AddTransactionInput{
			BudgetID:          budgetID,
			UserID:            userID,
			CorrelationID:     corrID,
			TxType:            tType,
			SourceAccountID:   c.FormValue("account_id"),
			DestinationAcctID: c.FormValue("dest_account_id"),
			AmountMinor:       amountMinor,
			DestAmountMinor:   dMinor,
			BucketID:          bIDPtr,
			TargetID:          tIDPtr,
			Notes:             strings.TrimSpace(c.FormValue("notes")),
			TransactionDate:   time.UnixMicro(txDate),
		})
		if err != nil {
			logger.Error(ctx, "Failed adding transaction via service", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		logger.Info(ctx, "Manual transaction recorded successfully", "budget_id", budgetID, "operation_id", opID)
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
	}

	userAcctID := c.FormValue("account_id")
	if userAcctID == "" {
		err := tenantDB.QueryRowContext(c.Request().Context(), `
			SELECT a.id FROM accounts_current a JOIN parties_current p ON a.party_id = p.id WHERE p.is_user_owned = 1 LIMIT 1`).Scan(&userAcctID)
		if err != nil {
			pID := domain.NewUUIDv7()
			userAcctID = domain.NewUUIDv7()
			tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
				VALUES (?, 1, 'My Family', 'INDIVIDUAL', 1, ?, ?)`, pID, userID, now)
			tenantDB.ExecContext(c.Request().Context(), `
				INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
				VALUES (?, 1, ?, 'Checking Account', 'CHECKING', 'PLN', 0, ?, ?)`, userAcctID, pID, userID, now)
		}
	}

	var userAcctCurr string
	_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT primary_currency FROM accounts_current WHERE id = ?`, userAcctID).Scan(&userAcctCurr)
	if userAcctCurr == "" {
		userAcctCurr = "PLN"
	}

	notes := strings.TrimSpace(c.FormValue("notes"))
	var bucketIDPtr *string
	bucketIDVal := c.FormValue("bucket_id")
	if bucketIDVal != "" {
		bucketIDPtr = &bucketIDVal
	}

	var targetIDPtr *string
	targetIDVal := c.FormValue("target_id")
	if targetIDVal != "" && bucketIDPtr != nil {
		targetIDPtr = &targetIDVal
	}

	var srcAcct, dstAcct string
	var srcCurr, dstCurr string
	var destAmountMinor int64 = amountMinor

	switch txType {
	case "INCOME":
		dstAcct = userAcctID
		dstCurr = userAcctCurr
		srcCurr = userAcctCurr
		payeeName := notes
		if payeeName == "" {
			payeeName = "External Payer / Income"
		}
		extAcct, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, payeeName, srcCurr, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external payer account")
		}
		srcAcct = extAcct

	case "TRANSFER":
		srcAcct = userAcctID
		srcCurr = userAcctCurr
		dstAcct = c.FormValue("dest_account_id")
		if dstAcct == "" || dstAcct == srcAcct {
			return echo.NewHTTPError(http.StatusBadRequest, "Transfer requires distinct source and destination accounts")
		}
		_ = tenantDB.QueryRowContext(c.Request().Context(), `SELECT primary_currency FROM accounts_current WHERE id = ?`, dstAcct).Scan(&dstCurr)
		if dstCurr == "" {
			dstCurr = srcCurr
		}
		if dAmt := c.FormValue("dest_amount"); dAmt != "" {
			if parsedDAmt, err := parseFlexibleAmount(dAmt); err == nil && parsedDAmt > 0 {
				destAmountMinor = parsedDAmt
			}
		}
		bucketIDPtr = nil // Internal transfers do not use buckets

	default: // EXPENSE
		srcAcct = userAcctID
		srcCurr = userAcctCurr
		dstCurr = userAcctCurr
		payeeName := notes
		if payeeName == "" {
			payeeName = "General Merchant"
		}
		extAcct, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, payeeName, dstCurr, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external merchant account")
		}
		dstAcct = extAcct
	}

	opID, err := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
		OperationType: transaction.OpDirectMove,
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      srcAcct,
				DestinationAccountID: dstAcct,
				SourceAmountMinor:    amountMinor,
				SourceCurrency:       srcCurr,
				DestAmountMinor:      destAmountMinor,
				DestCurrency:         dstCurr,
				BucketID:             bucketIDPtr,
				TargetID:             targetIDPtr,
				Notes:                &notes,
				TransactionDate:      txDate,
			},
		},
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		logger.Error(ctx, "Failed executing resolution for manual transaction", "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to save transaction: %v", err))
	}

	logger.Info(ctx, "Manual transaction recorded successfully",
		"budget_id", budgetID,
		"operation_id", opID,
		"tx_type", txType,
		"source_account_id", srcAcct,
		"dest_account_id", dstAcct,
		"amount_minor", amountMinor,
	)

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// UpdateTransaction handles editing an existing transaction.
func (h *TransactionsHandler) UpdateTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:update")

	budgetID := c.Param("budgetId")
	txID := c.Param("txId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	var amountFloat float64
	fmt.Sscanf(c.FormValue("amount"), "%f", &amountFloat)
	amountMinor := int64(amountFloat * 100)

	if amountMinor <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid transaction amount")
	}

	// Fetch existing transaction details
	var curSrcAcctID, curDstAcctID, curSrcCurr, curDstCurr string
	var curTxDate int64
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT source_account_id, destination_account_id, source_currency, destination_currency, transaction_date
		FROM budget_transactions_current WHERE id = ?`, txID).Scan(
		&curSrcAcctID, &curDstAcctID, &curSrcCurr, &curDstCurr, &curTxDate,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Transaction not found")
	}

	newSrcAcctID := c.FormValue("account_id")
	if newSrcAcctID == "" {
		newSrcAcctID = curSrcAcctID
	}

	txDate := curTxDate
	dateForm := c.FormValue("transaction_date")
	if dateForm != "" {
		if parsedDate, err := time.Parse("2006-01-02", dateForm); err == nil {
			txDate = parsedDate.UTC().UnixMicro()
		}
	}

	notes := c.FormValue("notes")
	var bucketIDPtr *string
	bucketIDVal := c.FormValue("bucket_id")
	if bucketIDVal != "" {
		bucketIDPtr = &bucketIDVal
	}

	var targetIDPtr *string
	targetIDVal := c.FormValue("target_id")
	if targetIDVal != "" && bucketIDPtr != nil {
		targetIDPtr = &targetIDVal
	}

	err = transaction.UpdateBudgetTransaction(c.Request().Context(), tenantDB, transaction.UpdateBudgetTxInput{
		ID:                   txID,
		SourceAccountID:      newSrcAcctID,
		DestinationAccountID: curDstAcctID,
		SourceAmountMinor:    amountMinor,
		SourceCurrency:       curSrcCurr,
		DestAmountMinor:      amountMinor,
		DestCurrency:         curDstCurr,
		BucketID:             bucketIDPtr,
		TargetID:             targetIDPtr,
		TransactionDate:      txDate,
		Notes:                &notes,
		AgentID:              userID,
		CorrelationID:        corrID,
	})
	if err != nil {
		logger.Error(ctx, "Failed updating transaction", "tx_id", txID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to update transaction: %v", err))
	}

	logger.Info(ctx, "Transaction updated successfully", "budget_id", budgetID, "tx_id", txID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// DeleteTransaction handles deleting an existing transaction.
func (h *TransactionsHandler) DeleteTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:delete")

	budgetID := c.Param("budgetId")
	txID := c.Param("txId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	err := transaction.DeleteBudgetTransaction(c.Request().Context(), tenantDB, txID, userID, corrID)
	if err != nil {
		logger.Error(ctx, "Failed deleting transaction", "tx_id", txID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to delete transaction: %v", err))
	}

	logger.Info(ctx, "Transaction deleted successfully", "budget_id", budgetID, "tx_id", txID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// BatchDeleteTransactions handles bulk deletion of multiple budget transactions.
func (h *TransactionsHandler) BatchDeleteTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:batch_delete")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)

	formParams, _ := c.FormParams()
	txIDs := formParams["transaction_ids"]
	if len(txIDs) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
	}

	err := transaction.BatchDeleteBudgetTransactions(c.Request().Context(), tenantDB, txIDs, userID, corrID)
	if err != nil {
		logger.Error(ctx, "Failed batch deleting transactions", "count", len(txIDs), "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to delete transactions: %v", err))
	}

	logger.Info(ctx, "Transactions batch deleted successfully", "budget_id", budgetID, "count", len(txIDs))
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// ResolveRawTransaction moves a single raw bank transaction into a budget transaction.
func (h *TransactionsHandler) ResolveRawTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:resolve")

	budgetID := c.Param("budgetId")
	rawID := c.Param("rawId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	// 1. Fetch raw transaction details
	var userAcctID, currency, rawPayee string
	var extTxID, counterpartyIBAN, ref sql.NullString
	var bookingDate sql.NullInt64
	var amountMinor int64
	var status, bStatus string
	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT account_id, external_transaction_id, booking_date, amount_minor, currency,
		       raw_payee, counterparty_iban, reference, status, COALESCE(booking_status, 'BOOKED')
		FROM raw_transactions_current WHERE id = ?`, rawID).Scan(
		&userAcctID, &extTxID, &bookingDate, &amountMinor, &currency,
		&rawPayee, &counterpartyIBAN, &ref, &status, &bStatus,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Raw transaction not found")
	}
	if status != "PENDING" {
		return echo.NewHTTPError(http.StatusBadRequest, "Raw transaction is not in PENDING status")
	}
	if bStatus == "PENDING" {
		return echo.NewHTTPError(http.StatusBadRequest, "Pending bank holds cannot be resolved into budget transactions until cleared by the bank")
	}

	partyName := strings.TrimSpace(c.FormValue("party_name"))
	if partyName == "" {
		partyName = strings.TrimSpace(rawPayee)
		if partyName == "" {
			partyName = "External Merchant"
		}
	}

	// 2. Find or create counterparty party & external account on-the-fly
	var counterpartyAcctID string
	err = tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT a.id FROM accounts_current a
		JOIN parties_current p ON a.party_id = p.id
		WHERE p.name = ? AND p.is_user_owned = 0
		LIMIT 1`, partyName).Scan(&counterpartyAcctID)
	if err != nil {
		pID := domain.NewUUIDv7()
		counterpartyAcctID = domain.NewUUIDv7()
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO parties_current (id, version, name, party_type, is_user_owned, agent_id, updated_at)
			VALUES (?, 1, ?, 'ORGANIZATION', 0, ?, ?)`, pID, partyName, userID, now)
		tenantDB.ExecContext(c.Request().Context(), `
			INSERT INTO accounts_current (id, version, party_id, name, account_type, primary_currency, is_closed, agent_id, updated_at)
			VALUES (?, 1, ?, ?, 'EXTERNAL', ?, 0, ?, ?)`, counterpartyAcctID, pID, partyName, currency, userID, now)
	}

	var bucketIDPtr *string
	bucketIDVal := c.FormValue("bucket_id")
	if bucketIDVal != "" {
		bucketIDPtr = &bucketIDVal
	}

	var targetIDPtr *string
	targetIDVal := c.FormValue("target_id")
	if targetIDVal != "" && bucketIDPtr != nil {
		targetIDPtr = &targetIDVal
	}

	notes := strings.TrimSpace(c.FormValue("notes"))
	if notes == "" {
		notes = rawPayee
	}

	txDate := now
	if bookingDate.Valid {
		txDate = bookingDate.Int64
	}

	// 3. Setup Double-Entry source & destination
	var srcAcct, dstAcct string
	var absAmountMinor int64
	if amountMinor < 0 {
		// Outflow / Expense: User Account -> External Counterparty
		srcAcct = userAcctID
		dstAcct = counterpartyAcctID
		absAmountMinor = -amountMinor
	} else {
		// Inflow / Income: External Counterparty -> User Account
		srcAcct = counterpartyAcctID
		dstAcct = userAcctID
		absAmountMinor = amountMinor
	}

	opID, err := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
		OperationType:     transaction.OpDirectMove,
		RawTransactionIDs: []string{rawID},
		BudgetTransactions: []transaction.BudgetTxInput{
			{
				SourceAccountID:      srcAcct,
				DestinationAccountID: dstAcct,
				SourceAmountMinor:    absAmountMinor,
				SourceCurrency:       currency,
				DestAmountMinor:      absAmountMinor,
				DestCurrency:         currency,
				BucketID:             bucketIDPtr,
				TargetID:             targetIDPtr,
				Notes:                &notes,
				TransactionDate:      txDate,
			},
		},
		AgentID:       userID,
		CorrelationID: corrID,
	})
	if err != nil {
		logger.Error(ctx, "Failed resolving raw transaction", "raw_id", rawID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to resolve transaction: %v", err))
	}

	logger.Info(ctx, "Raw transaction resolved successfully",
		"budget_id", budgetID,
		"raw_id", rawID,
		"operation_id", opID,
	)

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// DeleteRawTransaction permanently hard deletes a single raw bank transaction.
func (h *TransactionsHandler) DeleteRawTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:delete")

	budgetID := c.Param("budgetId")
	rawID := c.Param("rawId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	err := transaction.DeleteRawTransaction(c.Request().Context(), tenantDB, rawID)
	if err != nil {
		logger.Error(ctx, "Failed deleting raw transaction", "raw_id", rawID, "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to delete raw transaction: %v", err))
	}

	logger.Info(ctx, "Raw transaction deleted permanently", "budget_id", budgetID, "raw_id", rawID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// IgnoreRawTransaction marks a raw bank transaction as IGNORED.
func (h *TransactionsHandler) IgnoreRawTransaction(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:ignore")

	budgetID := c.Param("budgetId")
	rawID := c.Param("rawId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	var rawVersion int
	var accountID, dedupHash, currency, rawPayee string
	var extTxID, counterpartyIBAN, reference sql.NullString
	var bookingDate, valueDate sql.NullInt64
	var amountMinor int64

	err := tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
		       amount_minor, currency, raw_payee, counterparty_iban, reference
		FROM raw_transactions_current WHERE id = ? AND status = 'PENDING'`, rawID).Scan(
		&rawVersion, &accountID, &extTxID, &dedupHash, &bookingDate, &valueDate,
		&amountMinor, &currency, &rawPayee, &counterpartyIBAN, &reference,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Pending raw transaction not found")
	}

	newVersion := rawVersion + 1
	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO raw_transactions (
			id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
			amount_minor, currency, raw_payee, counterparty_iban, reference, status, booking_status, action,
			agent_id, correlation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'IGNORED', 'BOOKED', 'UPDATE', ?, ?, ?)`,
		rawID, newVersion, accountID, extTxID, dedupHash, bookingDate, valueDate,
		amountMinor, currency, rawPayee, counterpartyIBAN, reference,
		userID, corrID, now,
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE raw_transactions_current
		SET version = ?, status = 'IGNORED', updated_at = ?
		WHERE id = ?`, newVersion, now, rawID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	logger.Info(ctx, "Raw transaction ignored successfully", "budget_id", budgetID, "raw_id", rawID)
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// AutoMatchRawTransactions runs auto-matching rules across all pending raw transactions.
func (h *TransactionsHandler) AutoMatchRawTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:reconcile:auto_match")

	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	rulesEngine, err := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)
	if err != nil || rulesEngine == nil {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
	}

	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, account_id, booking_date, amount_minor, currency,
		       COALESCE(raw_payee, ''), COALESCE(counterparty_iban, ''), COALESCE(reference, '')
		FROM raw_transactions_current
		WHERE status = 'PENDING' AND booking_status = 'BOOKED'
		ORDER BY booking_date ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type rawItem struct {
		id          string
		accountID   string
		date        int64
		amountMinor int64
		currency    string
		payee       string
		iban        string
		ref         string
	}
	var items []rawItem
	for rows.Next() {
		var it rawItem
		rows.Scan(&it.id, &it.accountID, &it.date, &it.amountMinor, &it.currency, &it.payee, &it.iban, &it.ref)
		items = append(items, it)
	}

	matchedCount := 0
	for _, it := range items {
		match := rulesEngine.Match(it.payee, it.iban, it.ref)
		if match == nil {
			continue
		}

		counterpartyAcctID := match.TargetCounterpartyAcctID
		if counterpartyAcctID == "" {
			// Create counterparty account if rule had party
			continue
		}

		var srcAcct, dstAcct string
		var absAmountMinor int64
		if it.amountMinor < 0 {
			srcAcct = it.accountID
			dstAcct = counterpartyAcctID
			absAmountMinor = -it.amountMinor
		} else {
			srcAcct = counterpartyAcctID
			dstAcct = it.accountID
			absAmountMinor = it.amountMinor
		}

		notes := it.payee
		_, sErr := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
			OperationType:     transaction.OpDirectMove,
			RawTransactionIDs: []string{it.id},
			BudgetTransactions: []transaction.BudgetTxInput{
				{
					SourceAccountID:      srcAcct,
					DestinationAccountID: dstAcct,
					SourceAmountMinor:    absAmountMinor,
					SourceCurrency:       it.currency,
					DestAmountMinor:      absAmountMinor,
					DestCurrency:         it.currency,
					BucketID:             match.DefaultBucketID,
					Notes:                &notes,
					TransactionDate:      it.date,
				},
			},
			AgentID:       userID,
			CorrelationID: corrID,
		})
		if sErr == nil {
			matchedCount++
		}
	}

	logger.Info(ctx, "Auto-match completed", "budget_id", budgetID, "matched_count", matchedCount, "evaluated", len(items))
	_ = now
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// BatchResolveRawTransactions handles MERGE and SPLIT operations from the Floating Action Bar.
func (h *TransactionsHandler) BatchResolveRawTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:batch_resolve")
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	opType := c.FormValue("operation_type")
	formParams, _ := c.FormParams()
	rawIDs := formParams["raw_ids"]
	if len(rawIDs) == 0 {
		if single := c.FormValue("raw_id"); single != "" {
			rawIDs = append(rawIDs, single)
		}
	}

	if len(rawIDs) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "No raw transactions selected")
	}

	if opType == transaction.OpMerge {
		// N-to-1 Merge
		var totalNetDelta int64
		var currency string
		var userAcctID string
		var latestDate int64

		for _, rID := range rawIDs {
			var amt int64
			var curr, acctID string
			var bDate sql.NullInt64
			err := tenantDB.QueryRowContext(c.Request().Context(), `
				SELECT amount_minor, currency, account_id, booking_date
				FROM raw_transactions_current WHERE id = ? AND status = 'PENDING' AND booking_status = 'BOOKED'`, rID).
				Scan(&amt, &curr, &acctID, &bDate)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Pending raw transaction %s not found", rID))
			}
			totalNetDelta += amt
			currency = curr
			userAcctID = acctID
			if bDate.Valid && bDate.Int64 > latestDate {
				latestDate = bDate.Int64
			}
		}

		if latestDate == 0 {
			latestDate = now
		}

		if fAcct := c.FormValue("account_id"); fAcct != "" {
			userAcctID = fAcct
		}

		partyName := strings.TrimSpace(c.FormValue("party_name"))
		if partyName == "" {
			partyName = "Merged Counterparty"
		}
		extAcctID, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, partyName, currency, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external party")
		}

		var bucketIDPtr *string
		if bID := c.FormValue("bucket_id"); bID != "" {
			bucketIDPtr = &bID
		}

		notes := strings.TrimSpace(c.FormValue("notes"))
		if notes == "" {
			notes = fmt.Sprintf("Merged %d bank transactions", len(rawIDs))
		}

		var srcAcct, dstAcct string
		var absAmountMinor int64
		if totalNetDelta < 0 {
			srcAcct = userAcctID
			dstAcct = extAcctID
			absAmountMinor = -totalNetDelta
		} else {
			srcAcct = extAcctID
			dstAcct = userAcctID
			absAmountMinor = totalNetDelta
		}

		if absAmountMinor == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "Net amount of merged transactions is 0. Cannot create 0-amount budget transaction.")
		}

		_, err = transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
			OperationType:     transaction.OpMerge,
			RawTransactionIDs: rawIDs,
			BudgetTransactions: []transaction.BudgetTxInput{
				{
					SourceAccountID:      srcAcct,
					DestinationAccountID: dstAcct,
					SourceAmountMinor:    absAmountMinor,
					SourceCurrency:       currency,
					DestAmountMinor:      absAmountMinor,
					DestCurrency:         currency,
					BucketID:             bucketIDPtr,
					Notes:                &notes,
					TransactionDate:      latestDate,
				},
			},
			AgentID:       userID,
			CorrelationID: corrID,
		})
		if err != nil {
			logger.Error(ctx, "Failed executing merge resolution", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to merge transactions: %v", err))
		}

	} else if opType == transaction.OpSplit {
		// 1-to-N Split
		if len(rawIDs) != 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "Split operation requires exactly 1 raw transaction")
		}
		rawID := rawIDs[0]

		var userAcctID, currency, rawPayee string
		var bookingDate sql.NullInt64
		var amountMinor int64
		err := tenantDB.QueryRowContext(c.Request().Context(), `
			SELECT account_id, booking_date, amount_minor, currency, raw_payee
			FROM raw_transactions_current WHERE id = ? AND status = 'PENDING' AND booking_status = 'BOOKED'`, rawID).
			Scan(&userAcctID, &bookingDate, &amountMinor, &currency, &rawPayee)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Pending raw transaction not found")
		}

		txDate := now
		if bookingDate.Valid {
			txDate = bookingDate.Int64
		}

		partyName := strings.TrimSpace(c.FormValue("party_name"))
		if partyName == "" {
			partyName = rawPayee
		}
		extAcctID, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, partyName, currency, userID, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed creating external party")
		}

		splitBucketIDs := formParams["split_bucket_id"]
		splitAmounts := formParams["split_amount"]
		splitNotes := formParams["split_note"]

		if len(splitBucketIDs) == 0 || len(splitAmounts) != len(splitBucketIDs) {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid split configuration")
		}

		var btxInputs []transaction.BudgetTxInput
		var sumSplitMinor int64

		for i, bID := range splitBucketIDs {
			amtMinor, aErr := parseFlexibleAmount(splitAmounts[i])
			if aErr != nil || amtMinor <= 0 {
				return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid split amount for entry %d", i+1))
			}
			sumSplitMinor += amtMinor

			var bIDPtr *string
			if bID != "" {
				bIDPtr = &bID
			}
			note := rawPayee
			if i < len(splitNotes) && strings.TrimSpace(splitNotes[i]) != "" {
				note = strings.TrimSpace(splitNotes[i])
			}

			var srcAcct, dstAcct string
			if amountMinor < 0 {
				srcAcct = userAcctID
				dstAcct = extAcctID
			} else {
				srcAcct = extAcctID
				dstAcct = userAcctID
			}

			btxInputs = append(btxInputs, transaction.BudgetTxInput{
				SourceAccountID:      srcAcct,
				DestinationAccountID: dstAcct,
				SourceAmountMinor:    amtMinor,
				SourceCurrency:       currency,
				DestAmountMinor:      amtMinor,
				DestCurrency:         currency,
				BucketID:             bIDPtr,
				Notes:                &note,
				TransactionDate:      txDate,
			})
		}

		expectedAbsMinor := amountMinor
		if expectedAbsMinor < 0 {
			expectedAbsMinor = -expectedAbsMinor
		}
		if sumSplitMinor != expectedAbsMinor {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Split amounts sum (%d minor) does not match original transaction amount (%d minor)", sumSplitMinor, expectedAbsMinor))
		}

		_, err = transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
			OperationType:      transaction.OpSplit,
			RawTransactionIDs:  []string{rawID},
			BudgetTransactions: btxInputs,
			AgentID:            userID,
			CorrelationID:      corrID,
		})
		if err != nil {
			logger.Error(ctx, "Failed executing split resolution", "error", err.Error())
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to split transaction: %v", err))
		}
	} else if opType == "APPROVE" {
		// Batch 1-to-1 Direct Move (Approve) for all selected raw transactions
		rulesEngine, _ := rule.LoadRulesFromDB(c.Request().Context(), tenantDB)
		approvedCount := 0

		for _, rID := range rawIDs {
			var rawVersion int
			var accountID, dedupHash, currency, rawPayee string
			var extTxID, counterpartyIBAN, reference sql.NullString
			var bookingDate, valueDate sql.NullInt64
			var amountMinor int64

			err := tenantDB.QueryRowContext(c.Request().Context(), `
				SELECT version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
				       amount_minor, currency, raw_payee, counterparty_iban, reference
				FROM raw_transactions_current WHERE id = ? AND status = 'PENDING'`, rID).Scan(
				&rawVersion, &accountID, &extTxID, &dedupHash, &bookingDate, &valueDate,
				&amountMinor, &currency, &rawPayee, &counterpartyIBAN, &reference,
			)
			if err != nil {
				continue
			}

			txDate := now
			if bookingDate.Valid {
				txDate = bookingDate.Int64
			}

			// Clean payee and lookup auto-match rule
			cleanPayee := strings.TrimSpace(rawPayee)
			if cleanPayee == "" {
				cleanPayee = "External Merchant"
			}
			refStr := ""
			if reference.Valid {
				refStr = reference.String
			}
			ibanStr := ""
			if counterpartyIBAN.Valid {
				ibanStr = counterpartyIBAN.String
			}

			var targetBucketID *string
			var targetCounterpartyAcctID string

			if rulesEngine != nil {
				match := rulesEngine.Match(cleanPayee, ibanStr, refStr)
				if match != nil {
					targetBucketID = match.DefaultBucketID
					targetCounterpartyAcctID = match.TargetCounterpartyAcctID
				}
			}

			if targetCounterpartyAcctID == "" {
				extAcct, err := findOrCreateExternalAccount(c.Request().Context(), tenantDB, cleanPayee, currency, userID, now)
				if err != nil {
					continue
				}
				targetCounterpartyAcctID = extAcct
			}

			var srcAcct, dstAcct string
			var absAmountMinor int64
			if amountMinor < 0 {
				srcAcct = accountID
				dstAcct = targetCounterpartyAcctID
				absAmountMinor = -amountMinor
			} else {
				srcAcct = targetCounterpartyAcctID
				dstAcct = accountID
				absAmountMinor = amountMinor
			}

			note := cleanPayee
			_, sErr := transaction.ExecuteResolution(c.Request().Context(), tenantDB, transaction.ResolveResolutionInput{
				OperationType:     transaction.OpDirectMove,
				RawTransactionIDs: []string{rID},
				BudgetTransactions: []transaction.BudgetTxInput{
					{
						SourceAccountID:      srcAcct,
						DestinationAccountID: dstAcct,
						SourceAmountMinor:    absAmountMinor,
						SourceCurrency:       currency,
						DestAmountMinor:      absAmountMinor,
						DestCurrency:         currency,
						BucketID:             targetBucketID,
						Notes:                &note,
						TransactionDate:      txDate,
					},
				},
				AgentID:       userID,
				CorrelationID: corrID,
			})
			if sErr == nil {
				approvedCount++
			}
		}

		logger.Info(ctx, "Batch approved raw transactions", "count", approvedCount, "total_requested", len(rawIDs))
	} else {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unsupported operation type: %s", opType))
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// BatchDeleteRawTransactions permanently deletes multiple raw bank transactions.
func (h *TransactionsHandler) BatchDeleteRawTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:batch_delete")
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	formParams, _ := c.FormParams()
	rawIDs := formParams["raw_ids"]
	if len(rawIDs) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
	}

	err := transaction.BatchDeleteRawTransactions(c.Request().Context(), tenantDB, rawIDs)
	if err != nil {
		logger.Error(ctx, "Failed batch deleting raw transactions", "count", len(rawIDs), "error", err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to delete raw transactions: %v", err))
	}

	logger.Info(ctx, "Batch deleted raw transactions", "count", len(rawIDs))
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// BatchIgnoreRawTransactions marks multiple raw transactions as IGNORED.
func (h *TransactionsHandler) BatchIgnoreRawTransactions(c echo.Context) error {
	ctx := logger.WithComponent(c.Request().Context(), "biz:transaction:raw:batch_ignore")
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID := c.Get(ContextUserIDKey).(string)
	corrID := c.Get("correlationID").(string)
	now := time.Now().UTC().UnixMicro()

	formParams, _ := c.FormParams()
	rawIDs := formParams["raw_ids"]
	if len(rawIDs) == 0 {
		return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
	}

	for _, rawID := range rawIDs {
		var rawVersion int
		var accountID, dedupHash, currency, rawPayee string
		var extTxID, counterpartyIBAN, reference sql.NullString
		var bookingDate, valueDate sql.NullInt64
		var amountMinor int64

		err := tenantDB.QueryRowContext(c.Request().Context(), `
			SELECT version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
			       amount_minor, currency, raw_payee, counterparty_iban, reference
			FROM raw_transactions_current WHERE id = ? AND status = 'PENDING'`, rawID).Scan(
			&rawVersion, &accountID, &extTxID, &dedupHash, &bookingDate, &valueDate,
			&amountMinor, &currency, &rawPayee, &counterpartyIBAN, &reference,
		)
		if err == nil {
			newVersion := rawVersion + 1
			tx, tErr := tenantDB.BeginTx(c.Request().Context(), nil)
			if tErr == nil {
				_, _ = tx.ExecContext(c.Request().Context(), `
					INSERT INTO raw_transactions (
						id, version, account_id, external_transaction_id, dedup_hash, booking_date, value_date,
						amount_minor, currency, raw_payee, counterparty_iban, reference, status, booking_status, action,
						agent_id, correlation_id, created_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'IGNORED', 'BOOKED', 'UPDATE', ?, ?, ?)`,
					rawID, newVersion, accountID, extTxID, dedupHash, bookingDate, valueDate,
					amountMinor, currency, rawPayee, counterpartyIBAN, reference,
					userID, corrID, now,
				)
				_, _ = tx.ExecContext(c.Request().Context(), `
					UPDATE raw_transactions_current
					SET version = ?, status = 'IGNORED', updated_at = ?
					WHERE id = ?`, newVersion, now, rawID)
				_ = tx.Commit()
			}
		}
	}

	logger.Info(ctx, "Batch ignored raw transactions", "count", len(rawIDs))
	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}
