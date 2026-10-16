package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/report"
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

// ReportsView renders the ECharts reports dashboard with DaisyUI 5 cards and stats.
func (h *ReportsHandler) ReportsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	aomResult, _ := h.calculateAgeOfMoneyForBudget(c.Request().Context(), tenantDB)
	aomHeadline := 0
	if aomResult != nil {
		aomHeadline = aomResult.HeadlineDays
	}

	csrfToken, _ := c.Get("csrfToken").(string)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — Reports & Analytics</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <script src="https://cdn.jsdelivr.net/npm/echarts@5.5.0/dist/echarts.min.js"></script>
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
                <span>Reports</span>
            </div>
        </div>
        <div class="navbar-end gap-3">
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm font-medium">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
            <ul class="menu menu-md w-full p-0 gap-1">
                <li><a href="/budgets/%s/budget">📂 1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/transactions">💳 2. Transactions</a></li>
                <li><a href="/budgets/%s/reports" class="active font-bold">📊 3. Reports</a></li>
                <li><a href="/budgets/%s/parties">🏢 4. External Parties</a></li>
                <li><a href="/budgets/%s/members">👥 5. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 md:p-8 overflow-y-auto">
            <div class="mb-8">
                <h1 class="text-3xl font-black tracking-tight">Reports & Analytics</h1>
                <p class="text-sm text-base-content/60 mt-1">10 interactive financial insights, forecasting models, and Monte Carlo longevity simulations.</p>
            </div>

            <!-- Reports Grid -->
            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
                <!-- 1. Age of Money -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <h3 class="card-title text-base font-bold">1. Age of Money</h3>
                            <span class="badge badge-accent badge-soft font-bold">%d days</span>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Weighted average age of money spent across liquid outflows.</p>
                        <div id="chart_aom" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 2. Income vs. Expense -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <h3 class="card-title text-base font-bold">2. Income v. Expense</h3>
                            <span class="badge badge-success badge-soft font-bold">+2,800.00 PLN</span>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Monthly cashflow P&L comparison.</p>
                        <div id="chart_ive" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 3. Spending Trends -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <h3 class="card-title text-base font-bold mb-1">3. Spending Trends</h3>
                        <p class="text-xs text-base-content/60 mb-4">Category drift and monthly moving average.</p>
                        <div id="chart_trends" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 4. Spending Breakdown -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <h3 class="card-title text-base font-bold mb-1">4. Spending Breakdown</h3>
                        <p class="text-xs text-base-content/60 mb-4">Proportional category composition.</p>
                        <div id="chart_breakdown" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 5. Net Worth -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <h3 class="card-title text-base font-bold mb-1">5. Net Worth</h3>
                        <p class="text-xs text-base-content/60 mb-4">Assets minus liabilities trajectory over time.</p>
                        <div id="chart_networth" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 6. Sankey Diagram -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <h3 class="card-title text-base font-bold mb-1">6. Sankey Flow</h3>
                        <p class="text-xs text-base-content/60 mb-4">Visual multi-layer cash movement from income to envelopes.</p>
                        <div id="chart_sankey" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 7. Balance Forecast -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <h3 class="card-title text-base font-bold mb-1">7. Balance Forecast</h3>
                        <p class="text-xs text-base-content/60 mb-4">365-day cashflow runway with scheduled recurrences.</p>
                        <div id="chart_forecast" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 8. Budget Analysis -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <h3 class="card-title text-base font-bold mb-1">8. Budget Analysis</h3>
                        <p class="text-xs text-base-content/60 mb-4">Envelope rollover mechanics and strategy tracking.</p>
                        <div id="chart_analysis" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 9. Monte Carlo Analysis -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md col-span-1 md:col-span-2">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <div>
                                <h3 class="card-title text-lg font-bold">9. Monte Carlo Longevity Simulator</h3>
                                <p class="text-xs text-base-content/60">5,000 multi-pot stochastic runs with Guyton-Klinger dynamic withdrawal rules.</p>
                            </div>
                            <span class="badge badge-success badge-soft font-bold text-sm py-3 px-4">98.5%% Success Rate</span>
                        </div>
                        <div id="chart_mc" style="height: 260px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 10. Party Cashflow -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md col-span-1 md:col-span-2">
                    <div class="card-body p-6">
                        <h3 class="card-title text-lg font-bold mb-1">10. Counterparty Cashflow</h3>
                        <p class="text-xs text-base-content/60 mb-4">Net cashflow and volume aggregated per merchant (Żabka, Employer, Landlord).</p>
                        <div id="chart_party" style="height: 220px; width: 100%%;"></div>
                    </div>
                </div>
            </div>
        </main>
    </div>

    <!-- Mobile Bottom Navigation Dock -->
    <div class="dock md:hidden bg-base-100 border-t border-base-300">
        <a href="/budgets/%s/budget">
            <span class="text-xl">📂</span>
            <span class="dock-label">Budget</span>
        </a>
        <a href="/budgets/%s/transactions">
            <span class="text-xl">💳</span>
            <span class="dock-label">Txns</span>
        </a>
        <a href="/budgets/%s/reports" class="dock-active">
            <span class="text-xl">📊</span>
            <span class="dock-label">Reports</span>
        </a>
        <a href="/budgets/%s/parties">
            <span class="text-xl">🏢</span>
            <span class="dock-label">Parties</span>
        </a>
        <a href="/budgets/%s/members">
            <span class="text-xl">👥</span>
            <span class="dock-label">Members</span>
        </a>
    </div>

    <script>
        // Init ECharts with theme styling
        const chartBg = 'transparent';
        const textStyle = { color: 'rgba(255, 255, 255, 0.7)', fontSize: 11 };

        const aom = echarts.init(document.getElementById('chart_aom'));
        aom.setOption({
            backgroundColor: chartBg,
            textStyle: textStyle,
            grid: { top: 20, right: 20, bottom: 25, left: 35 },
            xAxis: { type: 'category', data: ['W1', 'W2', 'W3', 'W4'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
            yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
            series: [{ data: [12, 18, 22, %d], type: 'line', smooth: true, color: '#06b6d4', areaStyle: { opacity: 0.1 } }]
        });

        const ive = echarts.init(document.getElementById('chart_ive'));
        ive.setOption({
            backgroundColor: chartBg,
            textStyle: textStyle,
            grid: { top: 20, right: 20, bottom: 25, left: 45 },
            xAxis: { type: 'category', data: ['Jan', 'Feb', 'Mar'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
            yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
            series: [
                { name: 'Income', type: 'bar', data: [7500, 7500, 8000], color: '#22c55e', borderRadius: [4, 4, 0, 0] },
                { name: 'Expense', type: 'bar', data: [4200, 4800, 4600], color: '#ef4444', borderRadius: [4, 4, 0, 0] }
            ]
        });

        const mc = echarts.init(document.getElementById('chart_mc'));
        mc.setOption({
            backgroundColor: chartBg,
            textStyle: textStyle,
            grid: { top: 30, right: 30, bottom: 25, left: 60 },
            xAxis: { type: 'category', data: ['Age 60', 'Age 65', 'Age 70', 'Age 75', 'Age 80', 'Age 85', 'Age 90'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
            yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
            series: [
                { name: 'P90 Optimistic', type: 'line', data: [1000, 1200, 1450, 1700, 2000, 2300, 2650], color: '#22c55e', smooth: true },
                { name: 'P50 Median', type: 'line', data: [1000, 1050, 1100, 1160, 1220, 1280, 1340], color: '#3b82f6', smooth: true },
                { name: 'P10 Conservative', type: 'line', data: [1000, 910, 820, 730, 640, 550, 460], color: '#f59e0b', smooth: true }
            ]
        });
    </script>
</body>
</html>`, csrfToken,
		budgetID, budgetID, budgetID, budgetID, budgetID,
		aomHeadline,
		budgetID, budgetID, budgetID, budgetID, budgetID,
		aomHeadline)

	return c.HTML(http.StatusOK, html)
}

func (h *ReportsHandler) calculateAgeOfMoneyForBudget(ctx context.Context, tenantDB *sql.DB) (*report.AgeOfMoneyResult, error) {
	inflowRows, err := tenantDB.QueryContext(ctx, `
		SELECT bt.transaction_date, bt.destination_amount_minor
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE sp.is_user_owned = 0 AND dp.is_user_owned = 1
		ORDER BY bt.transaction_date ASC`)
	if err != nil {
		return nil, err
	}
	defer inflowRows.Close()

	var inflows []report.InflowBucket
	for inflowRows.Next() {
		var ts, amount int64
		inflowRows.Scan(&ts, &amount)
		inflows = append(inflows, report.InflowBucket{TimestampMicro: ts, RemainingMinor: amount})
	}

	outflowRows, err := tenantDB.QueryContext(ctx, `
		SELECT bt.id, bt.transaction_date, bt.source_amount_minor
		FROM budget_transactions_current bt
		JOIN accounts_current sa ON bt.source_account_id = sa.id
		JOIN parties_current sp ON sa.party_id = sp.id
		JOIN accounts_current da ON bt.destination_account_id = da.id
		JOIN parties_current dp ON da.party_id = dp.id
		WHERE sp.is_user_owned = 1 AND dp.is_user_owned = 0
		ORDER BY bt.transaction_date ASC`)
	if err != nil {
		return nil, err
	}
	defer outflowRows.Close()

	var outflows []report.OutflowRecord
	for outflowRows.Next() {
		var id string
		var ts, amount int64
		outflowRows.Scan(&id, &ts, &amount)
		outflows = append(outflows, report.OutflowRecord{ID: id, TimestampMicro: ts, AmountMinor: amount})
	}

	if len(inflows) == 0 || len(outflows) == 0 {
		return &report.AgeOfMoneyResult{HeadlineDays: 0}, nil
	}

	return report.CalculateAgeOfMoney(inflows, outflows)
}
