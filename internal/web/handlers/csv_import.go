package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
	csvpkg "github.com/balance/balance/internal/integration/csv"
	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/rule"
	"github.com/labstack/echo/v4"
)

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
	if token, ok := c.Get("csrf_token").(string); ok {
		csrfToken = token
	}

	// Fetch active accounts for select dropdown
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name, currency, account_type
		FROM accounts_current
		WHERE is_closed = 0
		ORDER BY name ASC`)
	if err != nil {
		logger.Error(c.Request().Context(), "Failed to query accounts for CSV import", "error", err)
		return err
	}
	defer rows.Close()

	var accountOptions strings.Builder
	for rows.Next() {
		var id, name, currency, acctType string
		if err := rows.Scan(&id, &name, &currency, &acctType); err == nil {
			accountOptions.WriteString(fmt.Sprintf(
				`<option value="%s">%s (%s - %s)</option>`,
				id, name, currency, acctType,
			))
		}
	}

	sidebarHTML := RenderSidebarNavigation(budgetID, "transactions")

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="emerald">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Import Bank Statement CSV - Balance</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col">
    <header class="navbar bg-base-100 border-b border-base-300 px-6 sticky top-0 z-30 shadow-xs">
        <div class="navbar-start gap-4 items-center">
            <a href="/budgets" class="text-2xl font-black tracking-tight text-primary flex items-center gap-2">
                <span>⚖️</span>
                <span>Balance</span>
            </a>
            <div class="badge badge-neutral font-semibold gap-1.5 py-3">
                <span class="w-2 h-2 rounded-full bg-success"></span>
                <span>%s</span>
            </div>
        </div>
        <div class="navbar-end gap-3">
            <a href="/budgets/%s/transactions" class="btn btn-ghost btn-sm font-medium">← Back to Ledger</a>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
            %s
        </aside>

        <main class="flex-1 p-4 md:p-8 overflow-y-auto pb-28">
            <div class="max-w-4xl mx-auto space-y-6">
                <!-- Header -->
                <div class="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4">
                    <div>
                        <h1 class="text-2xl font-black tracking-tight flex items-center gap-2">
                            <span>📄</span>
                            <span>Import Bank Statement CSV</span>
                        </h1>
                        <p class="text-xs text-base-content/60 mt-0.5">Upload transactions directly from CSV files exported from your bank.</p>
                    </div>
                    <a href="/budgets/%s/transactions" class="btn btn-outline btn-sm">Cancel</a>
                </div>

                <!-- Wizard Form -->
                <form id="csv-import-form" method="POST" action="/budgets/%s/transactions/import-csv" enctype="multipart/form-data" class="space-y-6">
                    <input type="hidden" name="csrf_token" value="%s" />

                    <!-- Step 1: Account & File Configuration -->
                    <div class="card bg-base-100 border border-base-300 shadow-sm">
                        <div class="card-body p-6 space-y-4">
                            <h2 class="card-title text-base font-bold flex items-center gap-2 border-b border-base-300 pb-2">
                                <span class="badge badge-primary badge-sm">1</span>
                                <span>Target Account & Statement File</span>
                            </h2>

                            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                                <div class="form-control">
                                    <label class="label font-semibold text-xs"><span class="label-text">Target Account</span></label>
                                    <select name="account_id" id="account_id" required class="select select-bordered w-full select-md focus:select-primary">
                                        <option value="" disabled selected>-- Select an account --</option>
                                        %s
                                    </select>
                                </div>

                                <div class="form-control">
                                    <label class="label font-semibold text-xs"><span class="label-text">Bank Profile</span></label>
                                    <select name="bank_profile" id="bank_profile" class="select select-bordered w-full select-md focus:select-primary" onchange="applyBankProfile(this.value)">
                                        <option value="GENERIC" selected>Standard / Generic CSV (Auto-detect)</option>
                                        <option value="MBANK">mBank (Polska)</option>
                                        <option value="PKOBP">PKO Bank Polski</option>
                                        <option value="SANTANDER">Santander Bank Polska</option>
                                        <option value="ING">ING Bank Śląski</option>
                                        <option value="MILLENNIUM">Bank Millennium</option>
                                    </select>
                                </div>
                            </div>

                            <div class="form-control">
                                <label class="label font-semibold text-xs"><span class="label-text">CSV File (up to 10 MB / 50 000 rows)</span></label>
                                <input type="file" name="csv_file" id="csv_file" required accept=".csv,text/csv,text/plain" class="file-input file-input-bordered w-full file-input-md focus:file-input-primary" onchange="previewCSVFile(this)" />
                            </div>
                        </div>
                    </div>

                    <!-- Step 2: Interactive Column Mapping & Live Preview -->
                    <div id="preview-card" class="card bg-base-100 border border-base-300 shadow-sm hidden">
                        <div class="card-body p-6 space-y-4">
                            <div class="flex justify-between items-center border-b border-base-300 pb-2">
                                <h2 class="card-title text-base font-bold flex items-center gap-2">
                                    <span class="badge badge-primary badge-sm">2</span>
                                    <span>Column Mapping & Statement Preview</span>
                                </h2>
                                <div id="detection-badges" class="flex gap-2">
                                    <span id="encoding-badge" class="badge badge-outline badge-xs font-mono">UTF-8</span>
                                    <span id="delimiter-badge" class="badge badge-outline badge-xs font-mono">Delimiter: ;</span>
                                </div>
                            </div>

                            <!-- Column Mapping Selectors -->
                            <div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 gap-3 bg-base-200/50 p-4 rounded-lg border border-base-300 text-xs">
                                <div>
                                    <label class="font-bold block mb-1">Date Column</label>
                                    <select name="date_col" id="date_col" class="select select-bordered select-xs w-full"></select>
                                </div>
                                <div>
                                    <label class="font-bold block mb-1">Amount Column (+/-)</label>
                                    <select name="amount_col" id="amount_col" class="select select-bordered select-xs w-full"></select>
                                </div>
                                <div>
                                    <label class="font-bold block mb-1">Payee Column</label>
                                    <select name="payee_col" id="payee_col" class="select select-bordered select-xs w-full"></select>
                                </div>
                                <div>
                                    <label class="font-bold block mb-1">Reference / Title</label>
                                    <select name="reference_col" id="reference_col" class="select select-bordered select-xs w-full"></select>
                                </div>
                                <div>
                                    <label class="font-bold block mb-1">Counterparty IBAN</label>
                                    <select name="iban_col" id="iban_col" class="select select-bordered select-xs w-full"></select>
                                </div>
                            </div>

                            <!-- Preview Table -->
                            <div class="overflow-x-auto border border-base-300 rounded-lg">
                                <table class="table table-xs table-zebra w-full" id="preview-table">
                                    <thead class="bg-base-200/80 font-bold" id="preview-thead">
                                        <!-- Dynamic headers -->
                                    </thead>
                                    <tbody id="preview-tbody">
                                        <!-- Dynamic rows -->
                                    </tbody>
                                </table>
                            </div>
                        </div>
                    </div>

                    <!-- Step 3: Confirmation & Auto-Match -->
                    <div class="card bg-base-100 border border-base-300 shadow-sm">
                        <div class="card-body p-6 flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4">
                            <label class="label cursor-pointer gap-3">
                                <input type="checkbox" name="auto_match" value="1" checked="checked" class="checkbox checkbox-primary checkbox-sm" />
                                <span class="label-text text-sm font-semibold">Automatically run Matching Rules on imported transactions</span>
                            </label>

                            <button type="submit" id="submit-btn" class="btn btn-primary font-semibold shadow-md gap-2 w-full sm:w-auto">
                                <span>📥</span>
                                <span>Import Transactions</span>
                            </button>
                        </div>
                    </div>
                </form>
            </div>
        </main>
    </div>

    <script>
    let rawCsvText = '';
    let parsedRows = [];

    const bankProfiles = {
        'GENERIC': { dateCol: 0, amountCol: 1, payeeCol: 2, refCol: 3, ibanCol: -1 },
        'MBANK': { dateCol: 0, amountCol: 6, payeeCol: 3, refCol: 2, ibanCol: -1 },
        'PKOBP': { dateCol: 0, amountCol: 3, payeeCol: 5, refCol: 7, ibanCol: 6 },
        'SANTANDER': { dateCol: 0, amountCol: 5, payeeCol: 2, refCol: 3, ibanCol: 4 },
        'ING': { dateCol: 0, amountCol: 8, payeeCol: 2, refCol: 3, ibanCol: 5 },
        'MILLENNIUM': { dateCol: 1, amountCol: -1, payeeCol: 2, refCol: 6, ibanCol: -1 }
    };

    function previewCSVFile(input) {
        if (!input.files || input.files.length === 0) return;
        const file = input.files[0];
        const reader = new FileReader();

        reader.onload = function(e) {
            rawCsvText = e.target.result;
            processPreview();
        };

        reader.readAsText(file);
    }

    function processPreview() {
        if (!rawCsvText) return;

        // Sniff delimiter
        let delimiter = ';';
        const sample = rawCsvText.substring(0, 4096);
        const commaCount = (sample.match(/,/g) || []).length;
        const semiCount = (sample.match(/;/g) || []).length;
        const tabCount = (sample.match(/\\t/g) || []).length;

        if (commaCount > semiCount && commaCount > tabCount) delimiter = ',';
        else if (tabCount > semiCount && tabCount > commaCount) delimiter = '\\t';

        document.getElementById('delimiter-badge').textContent = 'Delimiter: ' + (delimiter === '\\t' ? 'TAB' : delimiter);

        // Simple row parsing
        const lines = rawCsvText.split(/\\r?\\n/).filter(line => line.trim().length > 0);
        if (lines.length === 0) return;

        parsedRows = lines.slice(0, 15).map(line => {
            // Basic split respecting simple quotes
            return line.split(delimiter).map(cell => cell.trim().replace(/^["']|["']$/g, ''));
        });

        if (parsedRows.length === 0) return;

        const maxCols = Math.max(...parsedRows.map(r => r.length));
        populateColumnDropdowns(maxCols, parsedRows[0]);
        renderPreviewTable(parsedRows);

        document.getElementById('preview-card').classList.remove('hidden');

        // Apply profile defaults if selected
        const currentProfile = document.getElementById('bank_profile').value;
        applyBankProfile(currentProfile);
    }

    function populateColumnDropdowns(colCount, headerRow) {
        const selects = ['date_col', 'amount_col', 'payee_col', 'reference_col', 'iban_col'];
        selects.forEach(id => {
            const el = document.getElementById(id);
            if (!el) return;
            el.innerHTML = '<option value="-1">-- None --</option>';
            for (let i = 0; i < colCount; i++) {
                const colName = (headerRow && headerRow[i]) ? ('Col ' + (i+1) + ': ' + headerRow[i].substring(0, 16)) : ('Column ' + (i+1));
                const opt = document.createElement('option');
                opt.value = i;
                opt.textContent = colName;
                el.appendChild(opt);
            }
        });
    }

    function renderPreviewTable(rows) {
        const thead = document.getElementById('preview-thead');
        const tbody = document.getElementById('preview-tbody');
        thead.innerHTML = '';
        tbody.innerHTML = '';

        if (rows.length === 0) return;

        const trHead = document.createElement('tr');
        const thIndex = document.createElement('th');
        thIndex.textContent = '#';
        thIndex.className = 'w-8 text-center opacity-60';
        trHead.appendChild(thIndex);

        rows[0].forEach((col, idx) => {
            const th = document.createElement('th');
            th.textContent = 'Col ' + (idx + 1);
            trHead.appendChild(th);
        });
        thead.appendChild(trHead);

        rows.forEach((row, rowIdx) => {
            const tr = document.createElement('tr');
            if (rowIdx === 0) tr.className = 'bg-base-200/40 font-semibold';
            const tdIdx = document.createElement('td');
            tdIdx.textContent = (rowIdx + 1);
            tdIdx.className = 'text-center opacity-60';
            tr.appendChild(tdIdx);

            row.forEach(cell => {
                const td = document.createElement('td');
                td.textContent = cell;
                td.className = 'max-w-xs truncate';
                tr.appendChild(td);
            });
            tbody.appendChild(tr);
        });
    }

    function applyBankProfile(profile) {
        const p = bankProfiles[profile] || bankProfiles['GENERIC'];
        const setVal = (id, val) => {
            const el = document.getElementById(id);
            if (el && val >= -1 && val < el.options.length - 1) {
                el.value = val;
            }
        };
        setVal('date_col', p.dateCol);
        setVal('amount_col', p.amountCol);
        setVal('payee_col', p.payeeCol);
        setVal('reference_col', p.refCol);
        setVal('iban_col', p.ibanCol);
    }
    </script>
</body>
</html>`,
		budgetName,
		budgetID,
		sidebarHTML,
		budgetID,
		budgetID,
		csrfToken,
		accountOptions.String(),
	)

	return c.HTML(http.StatusOK, html)
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
	if mapping.Delimiter == 0 {
		mapping.Delimiter = sniff.Delimiter
	}
	if mapping.Encoding == "" {
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
	if mapping.Delimiter == 0 {
		mapping.Delimiter = sniff.Delimiter
	}
	if mapping.Encoding == "" {
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

		tx, err := tenantDB.BeginTx(ctx, nil)
		if err != nil {
			continue
		}

		btID := domain.NewUUIDv7()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO budget_transactions (
				id, version, source_account_id, destination_account_id, bucket_id,
				transaction_date, amount_minor, currency, notes, payee,
				action, agent_id, correlation_id, created_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, 'CREATE', ?, ?, ?)`,
			btID, srcAcct, dstAcct, match.DefaultBucketID,
			it.date, absAmountMinor, it.currency, it.ref, it.payee,
			userID, corrID, now,
		)
		if err != nil {
			tx.Rollback()
			continue
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO budget_transactions_current (
				id, version, source_account_id, destination_account_id, bucket_id,
				transaction_date, amount_minor, currency, notes, payee, updated_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			btID, srcAcct, dstAcct, match.DefaultBucketID,
			it.date, absAmountMinor, it.currency, it.ref, it.payee, now,
		)
		if err != nil {
			tx.Rollback()
			continue
		}

		// Update raw_transactions_current status to PROCESSED
		_, err = tx.ExecContext(ctx, `
			UPDATE raw_transactions_current
			SET status = 'PROCESSED', version = version + 1, updated_at = ?
			WHERE id = ?`, now, it.id)
		if err != nil {
			tx.Rollback()
			continue
		}

		// Append to raw_transactions ledger
		_, err = tx.ExecContext(ctx, `
			INSERT INTO raw_transactions (
				id, version, account_id, external_transaction_id, dedup_hash,
				booking_date, value_date, amount_minor, currency, raw_payee,
				counterparty_iban, reference, status, action, agent_id,
				correlation_id, created_at
			)
			SELECT id, version, account_id, external_transaction_id, dedup_hash,
			       booking_date, value_date, amount_minor, currency, raw_payee,
			       counterparty_iban, reference, 'PROCESSED', 'UPDATE', ?, ?, ?
			FROM raw_transactions_current
			WHERE id = ?`,
			userID, corrID, now, it.id,
		)
		if err != nil {
			tx.Rollback()
			continue
		}

		if err := tx.Commit(); err == nil {
			matchedCount++
		}
	}

	return matchedCount
}
