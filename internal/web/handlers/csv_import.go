package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
	csvpkg "github.com/balance/balance/internal/integration/csv"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/rule"
	"github.com/balance/balance/internal/transaction"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

type csvImportAccountOption struct {
	ID          string
	Name        string
	Currency    string
	AccountType string
}

type csvImportPageData struct {
	DateFormat              string
	TimeFormat              string
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
	Accounts                []csvImportAccountOption
}

// CSVImportPage renders the multi-step CSV statement import wizard.
func (h *TransactionsHandler) CSVImportPage(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB, ok := c.Get(ContextBudgetDBKey).(*sql.DB)
	if !ok || tenantDB == nil {
		return c.Redirect(http.StatusSeeOther, "/budgets")
	}

	var budgetName, defaultCurr string
	h.mainDB.QueryRowContext(c.Request().Context(), `SELECT name, default_currency FROM budgets WHERE id = ?`, budgetID).
		Scan(&budgetName, &defaultCurr)
	if defaultCurr == "" {
		defaultCurr = "PLN"
	}

	csrfToken := ""
	if token, ok := c.Get("csrfToken").(string); ok && token != "" {
		csrfToken = token
	} else if token, ok := c.Get("csrf_token").(string); ok {
		csrfToken = token
	}

	// Fetch active accounts for select dropdown
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name, primary_currency, account_type
		FROM accounts_current
		WHERE is_closed = 0
		ORDER BY name ASC`)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed to query accounts for CSV import", "error", err)
		return err
	}
	defer rows.Close()

	var accounts []csvImportAccountOption
	for rows.Next() {
		var id, name, currency, acctType string
		if err := rows.Scan(&id, &name, &currency, &acctType); err == nil {
			accounts = append(accounts, csvImportAccountOption{
				ID:          id,
				Name:        name,
				Currency:    currency,
				AccountType: acctType,
			})
		}
	}

	sidebarHTML := RenderSidebarNavigation(budgetID, "transactions")

	htmlContent, renderErr := templates.RenderString("transactions/csv_import", csvImportPageData{
		BudgetID:    budgetID,
		BudgetName:  budgetName,
		CSRFToken:   csrfToken,
		SidebarHTML: template.HTML(sidebarHTML),
		Accounts:    accounts,
	})
	if renderErr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed rendering CSV import template: "+renderErr.Error())
	}

	return c.HTML(http.StatusOK, htmlContent)
}

// PreviewCSV handles JSON-based CSV parsing preview for API & BDD tests.
func (h *TransactionsHandler) PreviewCSV(c echo.Context) error {
	fileHeader, err := c.FormFile("csv_file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "csv_file is required"})
	}

	src, err := fileHeader.Open()
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed opening file"})
	}
	defer src.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, src); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed reading file"})
	}

	rawBytes := buf.Bytes()
	sniff := csvpkg.SniffEncodingAndDelimiter(rawBytes)

	bankProfile := c.FormValue("bank_profile")
	mapping, _ := csvpkg.GetBankProfile(bankProfile, "PLN")
	if mapping.Delimiter == 0 || bankProfile == "" || strings.EqualFold(bankProfile, "GENERIC") {
		mapping.Delimiter = sniff.Delimiter
	}
	if mapping.Encoding == "" || bankProfile == "" || strings.EqualFold(bankProfile, "GENERIC") {
		mapping.Encoding = sniff.Encoding
	}

	parser := csvpkg.NewStreamParser(mapping)
	txs, err := parser.Parse(bytes.NewReader(rawBytes))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	type previewItem struct {
		Date      string  `json:"date"`
		Amount    float64 `json:"amount"`
		AmountStr string  `json:"amount_str"`
		Payee     string  `json:"payee"`
		Reference string  `json:"reference"`
	}

	items := make([]previewItem, 0, len(txs))
	for _, tx := range txs {
		dateStr := time.UnixMicro(tx.BookingDateMicro).Format("2006-01-02")
		amt := float64(tx.AmountMinor) / 100.0
		items = append(items, previewItem{
			Date:      dateStr,
			Amount:    amt,
			AmountStr: fmt.Sprintf("%.2f", amt),
			Payee:     tx.Payee,
			Reference: tx.Reference,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"encoding":    sniff.Encoding,
		"delimiter":   string(sniff.Delimiter),
		"rows_count":  len(txs),
		"transactions": items,
	})
}

// ImportCSV processes statement upload, persists to raw_transactions_current and raw_transactions,
// and optionally triggers auto-matching rules.
func (h *TransactionsHandler) ImportCSV(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB, ok := c.Get(ContextBudgetDBKey).(*sql.DB)
	if !ok || tenantDB == nil {
		return c.Redirect(http.StatusSeeOther, "/budgets")
	}

	accountID := c.FormValue("account_id")
	if accountID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "account_id is required"})
	}

	fileHeader, err := c.FormFile("csv_file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "csv_file is required"})
	}

	src, err := fileHeader.Open()
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed opening uploaded file"})
	}
	defer src.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, src); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed buffering file"})
	}

	rawBytes := buf.Bytes()
	sniff := csvpkg.SniffEncodingAndDelimiter(rawBytes)

	bankProfile := c.FormValue("bank_profile")
	mapping, _ := csvpkg.GetBankProfile(bankProfile, "PLN")
	if mapping.Delimiter == 0 || bankProfile == "" || strings.EqualFold(bankProfile, "GENERIC") {
		mapping.Delimiter = sniff.Delimiter
	}
	if mapping.Encoding == "" || bankProfile == "" || strings.EqualFold(bankProfile, "GENERIC") {
		mapping.Encoding = sniff.Encoding
	}

	// Override columns if manually specified in form
	if v := c.FormValue("date_col"); v != "" {
		if idx, err := strconv.Atoi(v); err == nil && idx >= -1 {
			mapping.DateCol = idx
		}
	}
	if v := c.FormValue("amount_col"); v != "" {
		if idx, err := strconv.Atoi(v); err == nil && idx >= -1 {
			mapping.AmountCol = idx
		}
	}
	if v := c.FormValue("payee_col"); v != "" {
		if idx, err := strconv.Atoi(v); err == nil && idx >= -1 {
			mapping.PayeeCol = idx
		}
	}
	if v := c.FormValue("reference_col"); v != "" {
		if idx, err := strconv.Atoi(v); err == nil && idx >= -1 {
			mapping.ReferenceCol = idx
		}
	}
	if v := c.FormValue("iban_col"); v != "" {
		if idx, err := strconv.Atoi(v); err == nil && idx >= -1 {
			mapping.CounterpartyIBANCol = idx
		}
	}

	userID, _ := c.Get(ContextUserIDKey).(string)
	if userID == "" {
		userID = "user_system"
	}
	corrID := domain.NewUUIDv7()

	res, err := csvpkg.ImportTransactions(
		c.Request().Context(),
		tenantDB,
		accountID,
		userID,
		corrID,
		bytes.NewReader(rawBytes),
		mapping,
	)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed importing CSV transactions", "error", err)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	autoMatch := c.FormValue("auto_match") == "1" || c.FormValue("auto_match") == "true"
	matchedCount := 0
	if autoMatch {
		matchedCount = h.runAutoMatchInternal(c.Request().Context(), tenantDB, userID, corrID)
	}

	logger.Info(c.Request().Context(), "CSV import completed",
		"budget_id", budgetID,
		"account_id", accountID,
		"imported", res.ImportedCount,
		"duplicates", res.DuplicatesCount,
		"matched", matchedCount,
	)

	if c.Request().Header.Get("Accept") == "application/json" {
		return c.JSON(http.StatusOK, map[string]any{
			"status":           "success",
			"imported_count":   res.ImportedCount,
			"duplicates_count": res.DuplicatesCount,
			"matched_count":    matchedCount,
		})
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/transactions", budgetID))
}

// runAutoMatchInternal runs rule-based auto matching against all pending raw transactions.
func (h *TransactionsHandler) runAutoMatchInternal(ctx context.Context, tenantDB *sql.DB, userID string, corrID string) int {
	rulesEngine, err := rule.LoadRulesFromDB(ctx, tenantDB)
	if err != nil || rulesEngine == nil {
		return 0
	}

	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, account_id, booking_date, amount_minor, currency,
		       COALESCE(raw_payee, ''), COALESCE(counterparty_iban, ''), COALESCE(reference, '')
		FROM raw_transactions_current
		WHERE status = 'PENDING'
		ORDER BY booking_date ASC`)
	if err != nil {
		return 0
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
		if err := rows.Scan(&it.id, &it.accountID, &it.date, &it.amountMinor, &it.currency, &it.payee, &it.iban, &it.ref); err == nil {
			items = append(items, it)
		}
	}

	now := time.Now().UTC().UnixMicro()
	matchedCount := 0

	for _, it := range items {
		match := rulesEngine.Match(it.payee, it.iban, it.ref)
		if match == nil {
			continue
		}

		counterpartyAcctID := match.TargetCounterpartyAcctID
		if counterpartyAcctID == "" {
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
		if it.ref != "" {
			notes = it.payee + " - " + it.ref
		}
		_, sErr := transaction.ExecuteResolution(ctx, tenantDB, transaction.ResolveResolutionInput{
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

	_ = now
	return matchedCount
}
