package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/report"
	"github.com/balance/balance/internal/web/templates"
	"github.com/labstack/echo/v4"
)

type ReportsHandler struct {
	mainDB    *sql.DB
	tenantMgr *db.TenantDBManager
}

func NewReportsHandler(mainDB *sql.DB, tenantMgr *db.TenantDBManager) *ReportsHandler {
	return &ReportsHandler{
		mainDB:    mainDB,
		tenantMgr: tenantMgr,
	}
}

type reportsViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	AOMHeadline             int
	SavedReportsHTML        template.HTML
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
}

// ReportsView renders the ECharts reports dashboard with DaisyUI 5 cards and stats.
func (h *ReportsHandler) ReportsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	aomResult, _ := h.CalculateAgeOfMoneyForBudget(c.Request().Context(), tenantDB)
	aomHeadline := 0
	if aomResult != nil {
		aomHeadline = aomResult.HeadlineDays
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	out, err := templates.RenderString("reports/view", reportsViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		BudgetName:              "Reports",
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		AOMHeadline:             aomHeadline,
		SavedReportsHTML:        template.HTML(h.renderSavedReportsSection(c.Request().Context(), tenantDB, budgetID, csrfToken)),
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "reports", userLocale)),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "reports", userLocale)),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	})
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, out)
}

func (h *ReportsHandler) getBudgetCurrency(ctx context.Context, budgetID string) string {
	var currency string
	err := h.mainDB.QueryRowContext(ctx, `SELECT default_currency FROM budgets WHERE id = ?`, budgetID).Scan(&currency)
	if err != nil || currency == "" {
		return "PLN"
	}
	return currency
}

func parseReportFilter(c echo.Context, defaultCurrency string) report.ReportFilter {
	filter := report.ReportFilter{
		TargetCurrency:  defaultCurrency,
		ExcludeInternal: true,
	}
	if startStr := c.QueryParam("start_date"); startStr != "" {
		if val, err := strconv.ParseInt(startStr, 10, 64); err == nil {
			filter.StartDateMicros = val
		}
	}
	if endStr := c.QueryParam("end_date"); endStr != "" {
		if val, err := strconv.ParseInt(endStr, 10, 64); err == nil {
			filter.EndDateMicros = val
		}
	}
	rangeStr := c.QueryParam("range")
	filter.Range = rangeStr
	if rangeStr != "" && filter.StartDateMicros == 0 {
		now := time.Now().UTC()
		filter.EndDateMicros = now.UnixMicro()
		switch rangeStr {
		case "30d":
			filter.StartDateMicros = now.AddDate(0, 0, -30).UnixMicro()
		case "90d":
			filter.StartDateMicros = now.AddDate(0, 0, -90).UnixMicro()
		case "180d":
			filter.StartDateMicros = now.AddDate(0, 0, -180).UnixMicro()
		case "YTD":
			filter.StartDateMicros = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()
		case "1y":
			filter.StartDateMicros = now.AddDate(-1, 0, 0).UnixMicro()
		case "all":
			filter.StartDateMicros = 0
			filter.EndDateMicros = 0
		}
	}
	if curr := c.QueryParam("currency"); curr != "" {
		filter.TargetCurrency = curr
	}
	if excl := c.QueryParam("exclude_internal"); excl != "" {
		filter.ExcludeInternal = (excl != "false")
	}
	return filter
}

// 1. Age of Money Data
func (h *ReportsHandler) GetAgeOfMoneyData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "age-of-money", filterKey, func() (any, error) {
		return h.CalculateAgeOfMoneyForBudget(c.Request().Context(), tenantDB, filter)
	})
}

// 2. Income vs. Expense Data
func (h *ReportsHandler) GetIncomeExpenseData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "income-expense", filterKey, func() (any, error) {
		return report.CalculateIncomeVsExpense(c.Request().Context(), tenantDB, h.mainDB, filter)
	})
}

// 3. Spending Trends Data
func (h *ReportsHandler) GetSpendingTrendsData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "spending-trends", filterKey, func() (any, error) {
		return report.CalculateSpendingTrends(c.Request().Context(), tenantDB, h.mainDB, filter)
	})
}

// 4. Spending Breakdown Data
func (h *ReportsHandler) GetSpendingBreakdownData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "spending-breakdown", filterKey, func() (any, error) {
		return report.CalculateSpendingBreakdown(c.Request().Context(), tenantDB, h.mainDB, filter)
	})
}

// 5. Net Worth Data
func (h *ReportsHandler) GetNetWorthData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "net-worth", filterKey, func() (any, error) {
		return report.CalculateNetWorth(c.Request().Context(), tenantDB, h.mainDB, filter)
	})
}

// 6. Sankey Flow Data
func (h *ReportsHandler) GetSankeyData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "sankey", filterKey, func() (any, error) {
		return report.CalculateSankeyFlow(c.Request().Context(), tenantDB, h.mainDB, filter)
	})
}

// 7. Balance Forecast Data
func (h *ReportsHandler) GetForecastData(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	days := 30
	if daysStr := c.QueryParam("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 {
			days = d
		}
	}
	filterKey := fmt.Sprintf(`{"days":%d}`, days)

	return h.serveCachedReport(c, "forecast", filterKey, func() (any, error) {
		return report.CalculateBalanceForecast(c.Request().Context(), tenantDB, days)
	})
}

// 8. Budget Analysis Data
func (h *ReportsHandler) GetBudgetAnalysisData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "budget-analysis", filterKey, func() (any, error) {
		return report.CalculateBudgetAnalysis(c.Request().Context(), tenantDB, h.mainDB, filter)
	})
}

// 9. Monte Carlo Data
func (h *ReportsHandler) GetMonteCarloData(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	var cfg report.MonteCarloConfig
	if c.Request().Method == http.MethodPost && strings.Contains(c.Request().Header.Get("Content-Type"), "application/json") {
		_ = c.Bind(&cfg)
	}

	if cfg.CurrentAge <= 0 {
		if age, err := strconv.Atoi(c.QueryParam("current_age")); err == nil && age > 0 {
			cfg.CurrentAge = age
		} else {
			cfg.CurrentAge = 40
		}
	}
	if cfg.TargetAge <= 0 {
		if age, err := strconv.Atoi(c.QueryParam("target_age")); err == nil && age > 0 {
			cfg.TargetAge = age
		} else {
			cfg.TargetAge = 65
		}
	}
	if cfg.SimulationsCount <= 0 {
		if cnt, err := strconv.Atoi(c.QueryParam("simulations_count")); err == nil && cnt > 0 {
			cfg.SimulationsCount = cnt
		} else {
			cfg.SimulationsCount = 1000
		}
	}
	if cfg.ReturnModel == "" {
		cfg.ReturnModel = "RANDOM_NORMAL"
	}
	if cfg.WithdrawalRule == "" {
		cfg.WithdrawalRule = c.QueryParam("withdrawal_rule")
		if cfg.WithdrawalRule == "" {
			cfg.WithdrawalRule = "FIXED_REAL"
		}
	}
	if cfg.InflationMeanPct == 0 {
		if inf, err := strconv.ParseFloat(c.QueryParam("inflation_pct"), 64); err == nil {
			cfg.InflationMeanPct = inf
			cfg.InflationStdDev = 1.5
		}
	}

	var startingBalance int64
	if startStr := c.QueryParam("starting_balance"); startStr != "" {
		if val, err := strconv.ParseInt(startStr, 10, 64); err == nil {
			startingBalance = val
		}
	}
	if startingBalance <= 0 {
		_ = tenantDB.QueryRowContext(c.Request().Context(), `
			SELECT COALESCE(SUM(balance_minor), 0)
			FROM account_currency_balances`).Scan(&startingBalance)
		if startingBalance < 10000000 {
			startingBalance = 100000000 // 1,000,000 PLN default
		}
	}

	expReturn := 7.0
	if retStr := c.QueryParam("expected_return_pct"); retStr != "" {
		if val, err := strconv.ParseFloat(retStr, 64); err == nil {
			expReturn = val
		}
	}
	vol := 12.0
	if volStr := c.QueryParam("volatility_pct"); volStr != "" {
		if val, err := strconv.ParseFloat(volStr, 64); err == nil {
			vol = val
		}
	}

	if len(cfg.Pots) == 0 {
		cfg.Pots = []report.MontePot{
			{
				Name:                 "Liquid Assets & Portfolio",
				StartingBalanceMinor: startingBalance,
				ExpectedReturnPct:    expReturn,
				VolatilityStdDevPct:  vol,
				AccessibleFromAge:    cfg.CurrentAge,
			},
		}
	}

	var annualSpend int64 = 4000000 // 40,000 PLN default
	if spendStr := c.QueryParam("annual_spend"); spendStr != "" {
		if val, err := strconv.ParseInt(spendStr, 10, 64); err == nil {
			annualSpend = val
		}
	}

	if len(cfg.SpendingPhases) == 0 {
		cfg.SpendingPhases = []report.SpendPhase{
			{
				FromAge:           cfg.CurrentAge,
				ToAge:             cfg.TargetAge,
				AnnualTargetMinor: annualSpend,
			},
		}
	}

	if len(cfg.IncomeStreams) == 0 {
		if pAmtStr := c.QueryParam("pension_amount"); pAmtStr != "" {
			if pAmt, err := strconv.ParseInt(pAmtStr, 10, 64); err == nil && pAmt > 0 {
				pFromAge := 65
				if pFromStr := c.QueryParam("pension_from_age"); pFromStr != "" {
					if a, err := strconv.Atoi(pFromStr); err == nil {
						pFromAge = a
					}
				}
				cfg.IncomeStreams = append(cfg.IncomeStreams, report.IncomeStream{
					Name:              "State Pension / External",
					FromAge:           pFromAge,
					ToAge:             cfg.TargetAge,
					AnnualGrossMinor:  pAmt,
					AdjustByInflation: true,
				})
			}
		}
	}

	cfgBytes, _ := json.Marshal(cfg)
	filterKey := string(cfgBytes)

	return h.serveCachedReport(c, "monte-carlo", filterKey, func() (any, error) {
		return report.RunMonteCarloSimulation(c.Request().Context(), cfg)
	})
}

// 10. Counterparty Cashflow Data
func (h *ReportsHandler) GetPartyCashflowData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)
	filterKey := buildReportFilterKey(c, filter)

	return h.serveCachedReport(c, "party-cashflow", filterKey, func() (any, error) {
		return report.CalculatePartyCashflow(c.Request().Context(), tenantDB, h.mainDB, filter)
	})
}

// CalculateAgeOfMoneyForBudget calculates headline and history Age of Money for tenant DB.
func (h *ReportsHandler) CalculateAgeOfMoneyForBudget(ctx context.Context, tenantDB *sql.DB, filters ...report.ReportFilter) (*report.AgeOfMoneyResult, error) {
	var filter report.ReportFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	inflowQuery := `
		SELECT bt.transaction_date, bt.destination_amount_minor
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE dp.is_user_owned = 1 AND da.account_type IN ('CHECKING', 'SAVINGS', 'CASH')
		  AND (sp.is_user_owned = 0 OR (sp.is_user_owned = 1 AND sa.account_type NOT IN ('CHECKING', 'SAVINGS', 'CASH')))`
	var inflowArgs []any
	if filter.EndDateMicros > 0 {
		inflowQuery += ` AND bt.transaction_date <= ?`
		inflowArgs = append(inflowArgs, filter.EndDateMicros)
	}
	inflowQuery += ` ORDER BY bt.transaction_date ASC`

	inflowRows, err := tenantDB.QueryContext(ctx, inflowQuery, inflowArgs...)
	if err != nil {
		return nil, err
	}
	defer inflowRows.Close()

	var inflows []report.InflowBucket
	for inflowRows.Next() {
		var ts, amount int64
		if err := inflowRows.Scan(&ts, &amount); err != nil {
			return nil, err
		}
		inflows = append(inflows, report.InflowBucket{TimestampMicro: ts, RemainingMinor: amount})
	}

	outflowQuery := `
		SELECT bt.id, bt.transaction_date, bt.source_amount_minor
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE sp.is_user_owned = 1 AND sa.account_type IN ('CHECKING', 'SAVINGS', 'CASH')
		  AND (dp.is_user_owned = 0 OR (dp.is_user_owned = 1 AND da.account_type NOT IN ('CHECKING', 'SAVINGS', 'CASH')))`
	var outflowArgs []any
	if filter.EndDateMicros > 0 {
		outflowQuery += ` AND bt.transaction_date <= ?`
		outflowArgs = append(outflowArgs, filter.EndDateMicros)
	}
	outflowQuery += ` ORDER BY bt.transaction_date ASC`

	outflowRows, err := tenantDB.QueryContext(ctx, outflowQuery, outflowArgs...)
	if err != nil {
		return nil, err
	}
	defer outflowRows.Close()

	var outflows []report.OutflowRecord
	for outflowRows.Next() {
		var id string
		var ts, amount int64
		if err := outflowRows.Scan(&id, &ts, &amount); err != nil {
			return nil, err
		}
		outflows = append(outflows, report.OutflowRecord{ID: id, TimestampMicro: ts, AmountMinor: amount})
	}

	if len(inflows) == 0 || len(outflows) == 0 {
		return &report.AgeOfMoneyResult{HeadlineDays: 0, History: []report.AgeOfMoneyPoint{}}, nil
	}

	res, err := report.CalculateAgeOfMoney(inflows, outflows)
	if err != nil {
		return nil, err
	}
	if res.History == nil {
		res.History = []report.AgeOfMoneyPoint{}
	}

	if filter.StartDateMicros > 0 || filter.EndDateMicros > 0 {
		var filteredHistory []report.AgeOfMoneyPoint
		var windowAges []int
		for _, pt := range res.History {
			if filter.StartDateMicros > 0 && pt.TimestampMicro < filter.StartDateMicros {
				continue
			}
			if filter.EndDateMicros > 0 && pt.TimestampMicro > filter.EndDateMicros {
				continue
			}
			filteredHistory = append(filteredHistory, pt)
			windowAges = append(windowAges, pt.AgeDays)
		}
		res.History = filteredHistory
		if len(windowAges) > 0 {
			startIdx := len(windowAges) - 10
			if startIdx < 0 {
				startIdx = 0
			}
			last10 := windowAges[startIdx:]
			var sum int
			for _, a := range last10 {
				sum += a
			}
			res.HeadlineDays = sum / len(last10)
		} else {
			res.HeadlineDays = 0
		}
	}

	return res, nil
}

// ReportDetailMeta holds presentation info for dedicated report views.
type ReportDetailMeta struct {
	Type        string
	Title       string
	Description string
	Endpoint    string
	ChartID     string
}

var reportMetaMap = map[string]ReportDetailMeta{
	"age-of-money": {
		Type:        "age-of-money",
		Title:       "Age of Money",
		Description: "Chronological FIFO tracking of liquid inflows and outflow depletion points.",
		Endpoint:    "age-of-money/data",
		ChartID:     "detail_chart_aom",
	},
	"income-expense": {
		Type:        "income-expense",
		Title:       "Income vs. Expense",
		Description: "Detailed monthly cashflow matrix and savings trajectory.",
		Endpoint:    "income-expense/data",
		ChartID:     "detail_chart_ive",
	},
	"spending-trends": {
		Type:        "spending-trends",
		Title:       "Spending Trends",
		Description: "Category drift and 3-month moving average across envelope buckets.",
		Endpoint:    "spending-trends/data",
		ChartID:     "detail_chart_trends",
	},
	"spending-breakdown": {
		Type:        "spending-breakdown",
		Title:       "Spending Breakdown",
		Description: "Proportional envelope expenditure distribution and category shares.",
		Endpoint:    "spending-breakdown/data",
		ChartID:     "detail_chart_breakdown",
	},
	"net-worth": {
		Type:        "net-worth",
		Title:       "Net Worth",
		Description: "Historical assets minus liabilities trajectory over time.",
		Endpoint:    "net-worth/data",
		ChartID:     "detail_chart_networth",
	},
	"sankey": {
		Type:        "sankey",
		Title:       "Sankey Cashflow",
		Description: "Visual multi-layer cash movement from incoming income to envelopes.",
		Endpoint:    "sankey/data",
		ChartID:     "detail_chart_sankey",
	},
	"forecast": {
		Type:        "forecast",
		Title:       "Balance Forecast",
		Description: "365-day forward cashflow runway with scheduled recurrence projection.",
		Endpoint:    "forecast/data",
		ChartID:     "detail_chart_forecast",
	},
	"budget-analysis": {
		Type:        "budget-analysis",
		Title:       "Budget Analysis",
		Description: "Envelope rollover mechanics, strategy tracking, and cumulative balance.",
		Endpoint:    "budget-analysis/data",
		ChartID:     "detail_chart_analysis",
	},
	"monte-carlo": {
		Type:        "monte-carlo",
		Title:       "Monte Carlo Longevity Simulator",
		Description: "Stochastic multi-pot retirement longevity simulations with dynamic withdrawal rules.",
		Endpoint:    "monte-carlo/data",
		ChartID:     "detail_chart_mc",
	},
	"party-cashflow": {
		Type:        "party-cashflow",
		Title:       "Counterparty Cashflow",
		Description: "Aggregated transaction volume and net cashflow per counterparty merchant.",
		Endpoint:    "party-cashflow/data",
		ChartID:     "detail_chart_party",
	},
}

func reportTypeToEnum(reportType string) string {
	switch reportType {
	case "age-of-money":
		return "AGE_OF_MONEY"
	case "income-expense":
		return "INCOME_VS_EXPENSE"
	case "spending-trends":
		return "SPENDING_TRENDS"
	case "spending-breakdown":
		return "SPENDING_BREAKDOWN"
	case "net-worth":
		return "NET_WORTH"
	case "sankey":
		return "SANKEY"
	case "forecast":
		return "BALANCE_FORECAST"
	case "budget-analysis":
		return "BUDGET_ANALYSIS"
	case "monte-carlo":
		return "MONTE_CARLO"
	case "party-cashflow":
		return "PARTY_CASHFLOW"
	default:
		return strings.ToUpper(strings.ReplaceAll(reportType, "-", "_"))
	}
}

type savedPresetView struct {
	ID         string
	Name       string
	ConfigJSON string
	IsPinned   bool
}

type reportDetailViewData struct {
	Locale                  string
	NumberFormat            string
	BudgetID                string
	BudgetName              string
	CSRFToken               string
	DateFormat              string
	TimeFormat              string
	Meta                    ReportDetailMeta
	ReportTypeEnum          string
	SelectedRange           string
	SelectedCurrency        string
	ExcludeInternal         bool
	SavedPresets            []savedPresetView
	SidebarHTML             template.HTML
	BottomDockHTML          template.HTML
	DateTimeFormatterScript template.HTML
}

// ReportDetailView renders the dedicated full-screen interactive view for a single report.
func (h *ReportsHandler) ReportDetailView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	reportType := c.Param("reportType")

	meta, ok := reportMetaMap[reportType]
	if !ok {
		return c.Redirect(http.StatusSeeOther, "/budgets/"+budgetID+"/reports")
	}

	csrfToken, _ := c.Get("csrfToken").(string)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)

	selectedRange := c.QueryParam("range")
	if selectedRange == "" {
		selectedRange = "30d"
	}
	selectedCurrency := c.QueryParam("currency")
	if selectedCurrency == "" {
		selectedCurrency = currency
	}
	excludeInternal := c.QueryParam("exclude_internal") != "false"

	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	reportTypeEnum := reportTypeToEnum(reportType)
	var savedPresets []savedPresetView
	if rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, name, config_json, is_pinned_widget
		FROM saved_reports_current
		WHERE report_type = ?
		ORDER BY is_pinned_widget DESC, name ASC`, reportTypeEnum); err == nil {
		defer rows.Close()
		for rows.Next() {
			var sID, sName, sCfg string
			var sPinned int
			if err := rows.Scan(&sID, &sName, &sCfg, &sPinned); err == nil {
				savedPresets = append(savedPresets, savedPresetView{
					ID:         sID,
					Name:       sName,
					ConfigJSON: sCfg,
					IsPinned:   sPinned == 1,
				})
			}
		}
	}

	userLocale := GetUserLocale(c)
	userNumberFormat := GetUserNumberFormat(c)
	dFormat, tFormat := GetUserDateTimePreferences(c)

	out, err := templates.RenderString("reports/detail", reportDetailViewData{
		Locale:                  userLocale,
		NumberFormat:            userNumberFormat,
		BudgetID:                budgetID,
		BudgetName:              "",
		CSRFToken:               csrfToken,
		DateFormat:              dFormat,
		TimeFormat:              tFormat,
		Meta:                    meta,
		ReportTypeEnum:          reportTypeEnum,
		SelectedRange:           selectedRange,
		SelectedCurrency:        selectedCurrency,
		ExcludeInternal:         excludeInternal,
		SavedPresets:            savedPresets,
		SidebarHTML:             template.HTML(RenderSidebarNavigation(budgetID, "reports", userLocale)),
		BottomDockHTML:          template.HTML(RenderBottomDock(budgetID, "reports", userLocale)),
		DateTimeFormatterScript: template.HTML(DateTimeFormatterScript()),
	})
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, out)
}

// SavedReportItem represents a stored report configuration.
type SavedReportItem struct {
	ID             string `json:"id"`
	Version        int    `json:"version"`
	Name           string `json:"name"`
	ReportType     string `json:"report_type"`
	ConfigJSON     string `json:"config_json"`
	IsPinnedWidget bool   `json:"is_pinned_widget"`
	AgentID        string `json:"agent_id"`
	UpdatedAt      int64  `json:"updated_at"`
}

// ListSavedReportsJSON returns the active saved reports for the budget.
func (h *ReportsHandler) ListSavedReportsJSON(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	rows, err := tenantDB.QueryContext(c.Request().Context(), `
		SELECT id, version, name, report_type, config_json, is_pinned_widget, agent_id, updated_at
		FROM saved_reports_current
		ORDER BY is_pinned_widget DESC, updated_at DESC`)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer rows.Close()

	var items []SavedReportItem
	for rows.Next() {
		var it SavedReportItem
		var isPinned int
		if err := rows.Scan(&it.ID, &it.Version, &it.Name, &it.ReportType, &it.ConfigJSON, &isPinned, &it.AgentID, &it.UpdatedAt); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		it.IsPinnedWidget = isPinned == 1
		items = append(items, it)
	}
	if items == nil {
		items = []SavedReportItem{}
	}
	return c.JSON(http.StatusOK, items)
}

// SaveReportConfig handles creating or updating a saved report.
func (h *ReportsHandler) SaveReportConfig(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	if userID == "" {
		userID = "anonymous"
	}

	name := c.FormValue("name")
	reportType := c.FormValue("report_type")
	configJSON := c.FormValue("config_json")
	if configJSON == "" {
		configJSON = "{}"
	}
	isPinned := 0
	if c.FormValue("is_pinned_widget") == "true" || c.FormValue("is_pinned_widget") == "1" || c.FormValue("is_pinned_widget") == "on" {
		isPinned = 1
	}

	reportID := c.FormValue("id")
	action := "CREATE"
	version := 1
	now := time.Now().UTC().UnixMicro()
	corrID := domain.NewUUIDv7()

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to start transaction")
	}
	defer tx.Rollback()

	if reportID == "" {
		reportID = domain.NewUUIDv7()
	} else {
		action = "UPDATE"
		_ = tx.QueryRowContext(c.Request().Context(), `SELECT version FROM saved_reports_current WHERE id = ?`, reportID).Scan(&version)
		version++
	}

	// 1. Insert into append-only saved_reports audit log
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO saved_reports (id, version, name, report_type, config_json, is_pinned_widget, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reportID, version, name, reportType, configJSON, isPinned, action, userID, corrID, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Failed to save report audit log: %v", err))
	}

	// 2. Upsert into saved_reports_current
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO saved_reports_current (id, version, name, report_type, config_json, is_pinned_widget, agent_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version = excluded.version,
			name = excluded.name,
			report_type = excluded.report_type,
			config_json = excluded.config_json,
			is_pinned_widget = excluded.is_pinned_widget,
			agent_id = excluded.agent_id,
			updated_at = excluded.updated_at`,
		reportID, version, name, reportType, configJSON, isPinned, userID, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to upsert saved report projection: %v", err))
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to commit saved report")
	}

	if c.Request().Header.Get("Accept") == "application/json" {
		return c.JSON(http.StatusOK, map[string]any{"id": reportID, "status": "saved"})
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/reports", budgetID))
}

// TogglePinReport toggles the is_pinned_widget state.
func (h *ReportsHandler) TogglePinReport(c echo.Context) error {
	budgetID := c.Param("budgetId")
	reportID := c.Param("reportId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	if userID == "" {
		userID = "anonymous"
	}

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to start transaction")
	}
	defer tx.Rollback()

	var version, isPinned int
	var name, reportType, configJSON string
	err = tx.QueryRowContext(c.Request().Context(), `
		SELECT version, name, report_type, config_json, is_pinned_widget
		FROM saved_reports_current WHERE id = ?`, reportID).Scan(&version, &name, &reportType, &configJSON, &isPinned)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Saved report not found")
	}

	newPinned := 1
	if isPinned == 1 {
		newPinned = 0
	}
	newVersion := version + 1
	now := time.Now().UTC().UnixMicro()
	corrID := domain.NewUUIDv7()

	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO saved_reports (id, version, name, report_type, config_json, is_pinned_widget, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'UPDATE', ?, ?, ?)`,
		reportID, newVersion, name, reportType, configJSON, newPinned, userID, corrID, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to log pin update")
	}

	_, err = tx.ExecContext(c.Request().Context(), `
		UPDATE saved_reports_current
		SET version = ?, is_pinned_widget = ?, agent_id = ?, updated_at = ?
		WHERE id = ?`, newVersion, newPinned, userID, now, reportID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update pin status")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to commit pin toggle")
	}

	if c.Request().Header.Get("Accept") == "application/json" {
		return c.JSON(http.StatusOK, map[string]any{"id": reportID, "is_pinned_widget": newPinned == 1})
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/reports", budgetID))
}

// DeleteSavedReport soft deletes the saved report and removes it from projection.
func (h *ReportsHandler) DeleteSavedReport(c echo.Context) error {
	budgetID := c.Param("budgetId")
	reportID := c.Param("reportId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	userID, _ := c.Get(ContextUserIDKey).(string)
	if userID == "" {
		userID = "anonymous"
	}

	tx, err := tenantDB.BeginTx(c.Request().Context(), nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to start transaction")
	}
	defer tx.Rollback()

	var version, isPinned int
	var name, reportType, configJSON string
	err = tx.QueryRowContext(c.Request().Context(), `
		SELECT version, name, report_type, config_json, is_pinned_widget
		FROM saved_reports_current WHERE id = ?`, reportID).Scan(&version, &name, &reportType, &configJSON, &isPinned)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Saved report not found")
	}

	newVersion := version + 1
	now := time.Now().UTC().UnixMicro()
	corrID := domain.NewUUIDv7()

	// Append SOFT_DELETE action
	_, err = tx.ExecContext(c.Request().Context(), `
		INSERT INTO saved_reports (id, version, name, report_type, config_json, is_pinned_widget, action, agent_id, correlation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'SOFT_DELETE', ?, ?, ?)`,
		reportID, newVersion, name, reportType, configJSON, isPinned, userID, corrID, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to record soft delete")
	}

	// Remove from current projection
	_, err = tx.ExecContext(c.Request().Context(), `DELETE FROM saved_reports_current WHERE id = ?`, reportID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete from projection")
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to commit deletion")
	}

	if c.Request().Header.Get("Accept") == "application/json" {
		return c.JSON(http.StatusOK, map[string]any{"id": reportID, "status": "deleted"})
	}

	return c.Redirect(http.StatusSeeOther, fmt.Sprintf("/budgets/%s/reports", budgetID))
}

type savedReportCardView struct {
	ID         string
	Name       string
	ReportType string
	IsPinned   bool
}

type savedReportsSectionData struct {
	BudgetID  string
	CSRFToken string
	Items     []savedReportCardView
}

func (h *ReportsHandler) renderSavedReportsSection(ctx context.Context, tenantDB *sql.DB, budgetID string, csrfToken string) string {
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, name, report_type, is_pinned_widget, updated_at
		FROM saved_reports_current
		ORDER BY is_pinned_widget DESC, updated_at DESC`)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var items []savedReportCardView
	for rows.Next() {
		var id, name, reportType string
		var isPinned int
		var updatedAt int64
		if err := rows.Scan(&id, &name, &reportType, &isPinned, &updatedAt); err != nil {
			continue
		}
		items = append(items, savedReportCardView{
			ID:         id,
			Name:       name,
			ReportType: reportType,
			IsPinned:   isPinned == 1,
		})
	}

	out, err := templates.RenderString("reports/saved_section", savedReportsSectionData{
		BudgetID:  budgetID,
		CSRFToken: csrfToken,
		Items:     items,
	})
	if err != nil {
		return ""
	}
	return out
}
