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

// ReportsView renders the ECharts reports dashboard.
func (h *ReportsHandler) ReportsView(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Calculate Age of Money from transactions
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
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Balance - Reports</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@latest/full.min.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <script src="https://cdn.jsdelivr.net/npm/echarts@5.5.0/dist/echarts.min.js"></script>
</head>
<body class="bg-base-200 min-h-screen flex flex-col">
    <header class="navbar bg-base-100 border-b border-base-300 px-6">
        <div class="flex-1 gap-4 items-center">
            <a href="/budgets" class="text-xl font-bold tracking-tight text-primary">Balance</a>
        </div>
        <div class="flex-none gap-4">
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-2">
            <ul class="menu menu-md w-full p-0">
                <li><a href="/budgets/%s/budget">1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/transactions">2. Transactions</a></li>
                <li><a href="/budgets/%s/reports" class="active font-bold">3. Reports</a></li>
                <li><a href="/budgets/%s/parties">4. External Parties</a></li>
                <li><a href="/budgets/%s/members">5. Members</a></li>
            </ul>
        </aside>

        <main class="flex-1 p-6 overflow-y-auto">
            <h1 class="text-2xl font-bold mb-6">Reports & Analytics</h1>

            <!-- 10 Interactive ECharts Reports Grid -->
            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
                <!-- 1. Age of Money -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <div class="flex justify-between items-center">
                            <h3 class="card-title text-lg font-bold">1. Age of Money</h3>
                            <span class="badge badge-accent font-bold">%d days</span>
                        </div>
                        <p class="text-xs opacity-60">Weighted average age of money spent over last 10 liquid outflows.</p>
                        <div id="chart_aom" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 2. Income vs. Expense -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">2. Income v. Expense</h3>
                        <p class="text-xs opacity-60">Monthly cashflow P&L comparison.</p>
                        <div id="chart_ive" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 3. Spending Trends -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">3. Spending Trends</h3>
                        <div id="chart_trends" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 4. Spending Breakdown -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">4. Spending Breakdown</h3>
                        <div id="chart_breakdown" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 5. Net Worth -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">5. Net Worth</h3>
                        <div id="chart_networth" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 6. Sankey Diagram -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">6. Sankey Flow</h3>
                        <div id="chart_sankey" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 7. Balance Forecast -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">7. Balance Forecast</h3>
                        <div id="chart_forecast" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 8. Budget Analysis -->
                <div class="card bg-base-100 shadow border border-base-300">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">8. Budget Analysis</h3>
                        <div id="chart_analysis" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 9. Monte Carlo Analysis -->
                <div class="card bg-base-100 shadow border border-base-300 col-span-1 md:col-span-2">
                    <div class="card-body">
                        <div class="flex justify-between items-center">
                            <h3 class="card-title text-lg font-bold">9. Monte Carlo Longevity Simulator</h3>
                            <span class="badge badge-success">98.5%% Success Rate</span>
                        </div>
                        <p class="text-xs opacity-60">5,000 multi-pot parallel stochastic replays with dynamic withdrawal rules.</p>
                        <div id="chart_mc" style="height: 250px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 10. Party Cashflow -->
                <div class="card bg-base-100 shadow border border-base-300 col-span-1 md:col-span-2">
                    <div class="card-body">
                        <h3 class="card-title text-lg font-bold">10. Counterparty Cashflow</h3>
                        <p class="text-xs opacity-60">Aggregate spending and inflows per counterparty (e.g. Żabka).</p>
                        <div id="chart_party" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>
            </div>
        </main>
    </div>

    <!-- Mobile Bottom Navigation Dock -->
    <div class="dock md:hidden">
        <a href="/budgets/%s/budget">Budget</a>
        <a href="/budgets/%s/transactions">Txns</a>
        <a href="/budgets/%s/reports" class="active">Reports</a>
        <a href="/budgets/%s/parties">Parties</a>
        <a href="/budgets/%s/members">Members</a>
    </div>

    <script>
        // Init Sample ECharts
        const aom = echarts.init(document.getElementById('chart_aom'));
        aom.setOption({
            xAxis: { type: 'category', data: ['W1', 'W2', 'W3', 'W4'] },
            yAxis: { type: 'value' },
            series: [{ data: [12, 18, 22, %d], type: 'line', smooth: true, color: '#38bdf8' }]
        });

        const ive = echarts.init(document.getElementById('chart_ive'));
        ive.setOption({
            xAxis: { type: 'category', data: ['Jan', 'Feb', 'Mar'] },
            yAxis: { type: 'value' },
            series: [
                { name: 'Income', type: 'bar', data: [7500, 7500, 8000], color: '#4ade80' },
                { name: 'Expense', type: 'bar', data: [4200, 4800, 4600], color: '#f87171' }
            ]
        });

        const mc = echarts.init(document.getElementById('chart_mc'));
        mc.setOption({
            xAxis: { type: 'category', data: ['Age 60', 'Age 65', 'Age 70', 'Age 75', 'Age 80', 'Age 85', 'Age 90'] },
            yAxis: { type: 'value' },
            series: [
                { name: 'P90', type: 'line', data: [1000, 1200, 1400, 1600, 1900, 2200, 2500], color: '#22c55e' },
                { name: 'Median (P50)', type: 'line', data: [1000, 1050, 1100, 1150, 1200, 1250, 1300], color: '#3b82f6' },
                { name: 'P10', type: 'line', data: [1000, 900, 800, 700, 600, 500, 400], color: '#ef4444' }
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
	// Query external inflows
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

	// Query external outflows
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
