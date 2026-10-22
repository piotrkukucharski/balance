package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

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

	aomResult, _ := h.CalculateAgeOfMoneyForBudget(c.Request().Context(), tenantDB)
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
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
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
            %s
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
                            <a href="/budgets/%s/reports/age-of-money" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>1. Age of Money</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                            <span id="badge_aom" class="badge badge-accent badge-soft font-bold">%d days</span>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Weighted average age of money spent across liquid outflows.</p>
                        <div id="chart_aom" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 2. Income vs. Expense -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/income-expense" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>2. Income v. Expense</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                            <span id="badge_ive" class="badge badge-success badge-soft font-bold">--</span>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Monthly cashflow P&L comparison.</p>
                        <div id="chart_ive" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 3. Spending Trends -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/spending-trends" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>3. Spending Trends</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Category drift and monthly moving average.</p>
                        <div id="chart_trends" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 4. Spending Breakdown -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/spending-breakdown" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>4. Spending Breakdown</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Proportional category composition.</p>
                        <div id="chart_breakdown" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 5. Net Worth -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/net-worth" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>5. Net Worth</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Assets minus liabilities trajectory over time.</p>
                        <div id="chart_networth" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 6. Sankey Diagram -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/sankey" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>6. Sankey Flow</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Visual multi-layer cash movement from income to envelopes.</p>
                        <div id="chart_sankey" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 7. Balance Forecast -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/forecast" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>7. Balance Forecast</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">365-day cashflow runway with scheduled recurrences.</p>
                        <div id="chart_forecast" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 8. Budget Analysis -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/budget-analysis" class="card-title text-base font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>8. Budget Analysis</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Envelope rollover mechanics and strategy tracking.</p>
                        <div id="chart_analysis" style="height: 200px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 9. Monte Carlo Analysis -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md col-span-1 md:col-span-2">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <div>
                                <a href="/budgets/%s/reports/monte-carlo" class="card-title text-lg font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                    <span>9. Monte Carlo Simulator</span>
                                    <span class="text-xs text-base-content/40">↗</span>
                                </a>
                                <p class="text-xs text-base-content/60">5,000 multi-pot stochastic runs with Guyton-Klinger dynamic withdrawal rules.</p>
                            </div>
                            <span id="badge_mc" class="badge badge-success badge-soft font-bold text-sm py-3 px-4">--%% Success Rate</span>
                        </div>
                        <div id="chart_mc" style="height: 260px; width: 100%%;"></div>
                    </div>
                </div>

                <!-- 10. Party Cashflow -->
                <div class="card card-border bg-base-100 border-base-300 shadow-md col-span-1 md:col-span-2">
                    <div class="card-body p-6">
                        <div class="flex justify-between items-center mb-2">
                            <a href="/budgets/%s/reports/party-cashflow" class="card-title text-lg font-bold hover:text-primary transition-colors flex items-center gap-1.5">
                                <span>10. Counterparty Cashflow</span>
                                <span class="text-xs text-base-content/40">↗</span>
                            </a>
                        </div>
                        <p class="text-xs text-base-content/60 mb-4">Net cashflow and volume aggregated per merchant (Żabka, Employer, Landlord).</p>
                        <div id="chart_party" style="height: 220px; width: 100%%;"></div>
                    </div>
                </div>
                </div>
            </div>
        </main>
    </div>

    %s

    <script>
        const chartBg = 'transparent';
        const textStyle = { color: 'rgba(255, 255, 255, 0.7)', fontSize: 11 };
        const budgetId = '%s';

        // Helper to format minor amounts (cents) to 2 decimals
        function formatMoney(minor) {
            return (minor / 100).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
        }

        async function fetchJSON(url) {
            try {
                const res = await fetch(url);
                if (!res.ok) return null;
                return await res.json();
            } catch (e) {
                console.error("fetch error:", url, e);
                return null;
            }
        }

        // 1. Age of Money
        (async () => {
            const chart = echarts.init(document.getElementById('chart_aom'));
            chart.showLoading({ color: '#06b6d4' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/age-of-money/data');
            chart.hideLoading();

            const history = (data && data.history && data.history.length > 0) ? data.history : [];
            const xData = history.map((h, i) => 'P' + (i + 1));
            const yData = history.map(h => h.age_days);

            if (data && typeof data.headline_days === 'number') {
                const b = document.getElementById('badge_aom');
                if (b) b.innerText = data.headline_days + ' days';
            }

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis', formatter: '{b}: {c} days' },
                grid: { top: 20, right: 20, bottom: 25, left: 35 },
                xAxis: { type: 'category', data: xData.length ? xData : ['Start'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                series: [{ data: yData.length ? yData : [0], type: 'line', smooth: true, color: '#06b6d4', areaStyle: { opacity: 0.1 } }]
            });
        })();

        // 2. Income vs Expense
        (async () => {
            const chart = echarts.init(document.getElementById('chart_ive'));
            chart.showLoading({ color: '#22c55e' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/income-expense/data');
            chart.hideLoading();

            const months = (data && data.months) ? data.months : [];
            const xData = months.map(m => m.month_key);
            const incomeData = months.map(m => m.income_minor / 100);
            const expenseData = months.map(m => m.expense_minor / 100);

            if (data && typeof data.total_savings_minor === 'number') {
                const b = document.getElementById('badge_ive');
                if (b) {
                    const sign = data.total_savings_minor >= 0 ? '+' : '';
                    b.innerText = sign + formatMoney(data.total_savings_minor);
                    b.className = 'badge font-bold ' + (data.total_savings_minor >= 0 ? 'badge-success badge-soft' : 'badge-error badge-soft');
                }
            }

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis' },
                grid: { top: 20, right: 20, bottom: 25, left: 45 },
                xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                series: [
                    { name: 'Income', type: 'bar', data: incomeData, color: '#22c55e', borderRadius: [4, 4, 0, 0] },
                    { name: 'Expense', type: 'bar', data: expenseData, color: '#ef4444', borderRadius: [4, 4, 0, 0] }
                ]
            });
        })();

        // 3. Spending Trends
        (async () => {
            const chart = echarts.init(document.getElementById('chart_trends'));
            chart.showLoading({ color: '#a855f7' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/spending-trends/data');
            chart.hideLoading();

            const pts = (data && data.points) ? data.points : [];
            const xData = pts.map(p => p.month_key);
            const totalData = pts.map(p => p.total_spent_minor / 100);
            const avgData = pts.map(p => p.moving_avg_minor / 100);

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis' },
                legend: { textStyle: textStyle, data: ['Total Spent', '3M Avg'], top: 0 },
                grid: { top: 30, right: 20, bottom: 25, left: 45 },
                xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                series: [
                    { name: 'Total Spent', type: 'bar', data: totalData, color: '#ec4899', borderRadius: [4, 4, 0, 0] },
                    { name: '3M Avg', type: 'line', smooth: true, data: avgData, color: '#eab308' }
                ]
            });
        })();

        // 4. Spending Breakdown
        (async () => {
            const chart = echarts.init(document.getElementById('chart_breakdown'));
            chart.showLoading({ color: '#f97316' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/spending-breakdown/data');
            chart.hideLoading();

            const items = (data && data.items) ? data.items : [];
            const pieData = items.map(it => ({
                name: it.bucket_name || 'Unassigned',
                value: it.total_spent_minor / 100
            }));

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%%)' },
                series: [{
                    type: 'pie',
                    radius: ['40%%', '70%%'],
                    avoidLabelOverlap: false,
                    itemStyle: { borderRadius: 6, borderColor: '#1f2937', borderWidth: 2 },
                    label: { show: false },
                    emphasis: { label: { show: true, fontSize: 12, fontWeight: 'bold' } },
                    data: pieData.length ? pieData : [{ name: 'No Spending', value: 0 }]
                }]
            });
        })();

        // 5. Net Worth
        (async () => {
            const chart = echarts.init(document.getElementById('chart_networth'));
            chart.showLoading({ color: '#3b82f6' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/net-worth/data');
            chart.hideLoading();

            const pts = (data && data.points) ? data.points : [];
            const xData = pts.map(p => p.month_key);
            const netData = pts.map(p => p.net_worth_minor / 100);
            const assetData = pts.map(p => p.assets_minor / 100);
            const liabData = pts.map(p => p.liabilities_minor / 100);

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis' },
                grid: { top: 20, right: 20, bottom: 25, left: 55 },
                xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                series: [
                    { name: 'Net Worth', type: 'line', smooth: true, data: netData, color: '#3b82f6', areaStyle: { opacity: 0.15 } },
                    { name: 'Assets', type: 'line', smooth: true, data: assetData, color: '#22c55e', lineStyle: { type: 'dashed' } },
                    { name: 'Liabilities', type: 'line', smooth: true, data: liabData, color: '#ef4444', lineStyle: { type: 'dashed' } }
                ]
            });
        })();

        // 6. Sankey Flow
        (async () => {
            const chart = echarts.init(document.getElementById('chart_sankey'));
            chart.showLoading({ color: '#10b981' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/sankey/data');
            chart.hideLoading();

            const nodes = (data && data.nodes) ? data.nodes.map(n => ({ name: n.name })) : [];
            const links = (data && data.links) ? data.links.map(l => ({
                source: l.source,
                target: l.target,
                value: l.value_minor / 100
            })) : [];

            if (nodes.length === 0) {
                nodes.push({ name: 'Income' }, { name: 'Envelopes' });
                links.push({ source: 'Income', target: 'Envelopes', value: 0 });
            }

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'item', triggerOn: 'mousemove' },
                series: [{
                    type: 'sankey',
                    layout: 'none',
                    emphasis: { focus: 'adjacency' },
                    lineStyle: { color: 'gradient', curveness: 0.5 },
                    data: nodes,
                    links: links
                }]
            });
        })();

        // 7. Balance Forecast
        (async () => {
            const chart = echarts.init(document.getElementById('chart_forecast'));
            chart.showLoading({ color: '#6366f1' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/forecast/data');
            chart.hideLoading();

            const pts = (data && data.points) ? data.points : [];
            const xData = pts.map(p => p.date);
            const yData = pts.map(p => p.projected_balance_minor / 100);

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis' },
                grid: { top: 20, right: 20, bottom: 25, left: 55 },
                xAxis: { type: 'category', data: xData.length ? xData : ['Today'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                series: [{ data: yData.length ? yData : [0], type: 'line', smooth: true, color: '#6366f1', areaStyle: { opacity: 0.1 } }]
            });
        })();

        // 8. Budget Analysis
        (async () => {
            const chart = echarts.init(document.getElementById('chart_analysis'));
            chart.showLoading({ color: '#14b8a6' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/budget-analysis/data');
            chart.hideLoading();

            const pts = (data && data.points) ? data.points : [];
            const xData = pts.map(p => p.period_key);
            const budgetedData = pts.map(p => p.budgeted_minor / 100);
            const spentData = pts.map(p => p.spent_minor / 100);
            const cumData = pts.map(p => p.cumulative_balance_minor / 100);

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis' },
                legend: { textStyle: textStyle, data: ['Budgeted', 'Spent', 'Cumulative Balance'], top: 0 },
                grid: { top: 30, right: 20, bottom: 25, left: 45 },
                xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                series: [
                    { name: 'Budgeted', type: 'bar', data: budgetedData, color: '#38bdf8', borderRadius: [4, 4, 0, 0] },
                    { name: 'Spent', type: 'bar', data: spentData, color: '#f87171', borderRadius: [4, 4, 0, 0] },
                    { name: 'Cumulative Balance', type: 'line', smooth: true, data: cumData, color: '#10b981' }
                ]
            });
        })();

        // 9. Monte Carlo
        (async () => {
            const chart = echarts.init(document.getElementById('chart_mc'));
            chart.showLoading({ color: '#22c55e' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/monte-carlo/data');
            chart.hideLoading();

            const p10 = (data && data.p10_trajectory) ? data.p10_trajectory.map(v => v / 100) : [];
            const p50 = (data && data.p50_trajectory) ? data.p50_trajectory.map(v => v / 100) : [];
            const p90 = (data && data.p90_trajectory) ? data.p90_trajectory.map(v => v / 100) : [];

            if (data && typeof data.success_rate_pct === 'number') {
                const b = document.getElementById('badge_mc');
                if (b) b.innerText = data.success_rate_pct.toFixed(1) + '%% Success Rate';
            }

            const xData = p50.map((_, i) => 'Age ' + (40 + i));

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis' },
                legend: { textStyle: textStyle, data: ['P90 Optimistic', 'P50 Median', 'P10 Conservative'], top: 0 },
                grid: { top: 35, right: 30, bottom: 25, left: 60 },
                xAxis: { type: 'category', data: xData.length ? xData : ['Age 40', 'Age 65'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                series: [
                    { name: 'P90 Optimistic', type: 'line', data: p90, color: '#22c55e', smooth: true },
                    { name: 'P50 Median', type: 'line', data: p50, color: '#3b82f6', smooth: true },
                    { name: 'P10 Conservative', type: 'line', data: p10, color: '#f59e0b', smooth: true }
                ]
            });
        })();

        // 10. Counterparty Cashflow
        (async () => {
            const chart = echarts.init(document.getElementById('chart_party'));
            chart.showLoading({ color: '#8b5cf6' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/party-cashflow/data');
            chart.hideLoading();

            const parties = (data && data.parties) ? data.parties.slice(0, 10) : [];
            const yAxisData = parties.map(p => p.party_name);
            const netData = parties.map(p => p.net_flow_minor / 100);

            chart.setOption({
                backgroundColor: chartBg,
                textStyle: textStyle,
                tooltip: { trigger: 'axis' },
                grid: { top: 20, right: 30, bottom: 25, left: 100 },
                xAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                yAxis: { type: 'category', data: yAxisData.length ? yAxisData : ['No Counterparties'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                series: [{
                    name: 'Net Cashflow',
                    type: 'bar',
                    data: netData,
                    itemStyle: {
                        color: function(params) {
                            return params.value >= 0 ? '#22c55e' : '#ef4444';
                        },
                        borderRadius: [0, 4, 4, 0]
                    }
                }]
            });
        })();

        window.addEventListener('resize', () => {
            ['chart_aom', 'chart_ive', 'chart_trends', 'chart_breakdown', 'chart_networth', 'chart_sankey', 'chart_forecast', 'chart_analysis', 'chart_mc', 'chart_party'].forEach(id => {
                const el = document.getElementById(id);
                if (el) {
                    const inst = echarts.getInstanceByDom(el);
                    if (inst) inst.resize();
                }
            });
        });
    </script>
</body>
</html>`, csrfToken,
		RenderSidebarNavigation(budgetID, "reports"),
		budgetID,
		aomHeadline,
		budgetID,
		budgetID,
		budgetID,
		budgetID,
		budgetID,
		budgetID,
		budgetID,
		budgetID,
		budgetID,
		RenderBottomDock(budgetID, "reports"),
		budgetID)

	return c.HTML(http.StatusOK, html)
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
		TargetCurrency: defaultCurrency,
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
	if curr := c.QueryParam("currency"); curr != "" {
		filter.TargetCurrency = curr
	}
	return filter
}

// 1. Age of Money Data
func (h *ReportsHandler) GetAgeOfMoneyData(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	res, err := h.CalculateAgeOfMoneyForBudget(c.Request().Context(), tenantDB)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 2. Income vs. Expense Data
func (h *ReportsHandler) GetIncomeExpenseData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)

	res, err := report.CalculateIncomeVsExpense(c.Request().Context(), tenantDB, h.mainDB, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 3. Spending Trends Data
func (h *ReportsHandler) GetSpendingTrendsData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)

	res, err := report.CalculateSpendingTrends(c.Request().Context(), tenantDB, h.mainDB, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 4. Spending Breakdown Data
func (h *ReportsHandler) GetSpendingBreakdownData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)

	res, err := report.CalculateSpendingBreakdown(c.Request().Context(), tenantDB, h.mainDB, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 5. Net Worth Data
func (h *ReportsHandler) GetNetWorthData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)

	res, err := report.CalculateNetWorth(c.Request().Context(), tenantDB, h.mainDB, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 6. Sankey Flow Data
func (h *ReportsHandler) GetSankeyData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)

	res, err := report.CalculateSankeyFlow(c.Request().Context(), tenantDB, h.mainDB, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
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

	res, err := report.CalculateBalanceForecast(c.Request().Context(), tenantDB, days)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 8. Budget Analysis Data
func (h *ReportsHandler) GetBudgetAnalysisData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)

	res, err := report.CalculateBudgetAnalysis(c.Request().Context(), tenantDB, h.mainDB, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 9. Monte Carlo Data
func (h *ReportsHandler) GetMonteCarloData(c echo.Context) error {
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)

	// Pull active account balances to seed starting balance
	var totalBalanceMinor int64
	_ = tenantDB.QueryRowContext(c.Request().Context(), `
		SELECT COALESCE(SUM(balance_minor), 0)
		FROM account_currency_balances`).Scan(&totalBalanceMinor)

	startBalance := totalBalanceMinor
	if startBalance < 10000000 { // Min 100,000 PLN seed if zero or negative
		startBalance = 100000000 // 1,000,000 PLN default retirement pot
	}

	cfg := report.MonteCarloConfig{
		CurrentAge:       40,
		TargetAge:        65,
		SimulationsCount: 1000,
		ReturnModel:      "RANDOM_NORMAL",
		Pots: []report.MontePot{
			{
				Name:                 "Liquid Assets & Portfolio",
				StartingBalanceMinor: startBalance,
				ExpectedReturnPct:    7.0,
				VolatilityStdDevPct:  12.0,
				AccessibleFromAge:    40,
			},
		},
		SpendingPhases: []report.SpendPhase{
			{
				FromAge:           40,
				ToAge:             65,
				AnnualTargetMinor: 4000000, // 40,000 PLN / year
			},
		},
	}

	res, err := report.RunMonteCarloSimulation(c.Request().Context(), cfg)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// 10. Counterparty Cashflow Data
func (h *ReportsHandler) GetPartyCashflowData(c echo.Context) error {
	budgetID := c.Param("budgetId")
	tenantDB := c.Get(ContextBudgetDBKey).(*sql.DB)
	currency := h.getBudgetCurrency(c.Request().Context(), budgetID)
	filter := parseReportFilter(c, currency)

	res, err := report.CalculatePartyCashflow(c.Request().Context(), tenantDB, h.mainDB, filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// CalculateAgeOfMoneyForBudget calculates headline and history Age of Money for tenant DB.
func (h *ReportsHandler) CalculateAgeOfMoneyForBudget(ctx context.Context, tenantDB *sql.DB) (*report.AgeOfMoneyResult, error) {
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

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
    <title>Balance — %s</title>
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
    <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
    <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <script src="https://cdn.jsdelivr.net/npm/echarts@5.5.0/dist/echarts.min.js"></script>
    <link rel="icon" type="image/x-icon" href="/favicon.ico">
    <link rel="manifest" href="/manifest.webmanifest">
</head>
<body class="bg-base-200 text-base-content min-h-screen flex flex-col">
    <header class="navbar bg-base-100 border-b border-base-300 px-6 sticky top-0 z-30 shadow-xs">
        <div class="navbar-start gap-4 items-center">
            <a href="/budgets" class="text-2xl font-black tracking-tight text-primary flex items-center gap-2">
                <span>⚖️</span>
                <span>Balance</span>
            </a>
            <div class="breadcrumbs text-sm font-medium">
                <ul>
                    <li><a href="/budgets/%s/reports" class="text-base-content/70 hover:text-primary">Reports</a></li>
                    <li class="font-bold text-primary">%s</li>
                </ul>
            </div>
        </div>
        <div class="navbar-end gap-3">
            <a href="/budgets/%s/reports" class="btn btn-ghost btn-sm gap-1">
                <span>←</span>
                <span>Back to Dashboard</span>
            </a>
            <form method="POST" action="/logout">
                <input type="hidden" name="csrf_token" value="%s" />
                <button type="submit" class="btn btn-ghost btn-sm font-medium">Log Out</button>
            </form>
        </div>
    </header>

    <div class="flex-1 flex overflow-hidden">
        <aside class="w-64 bg-base-100 border-r border-base-300 p-4 hidden md:flex flex-col gap-4">
            %s
        </aside>

        <main class="flex-1 p-6 md:p-8 overflow-y-auto flex flex-col gap-6">
            <!-- Header & Filter Controls Bar -->
            <div class="flex flex-col md:flex-row md:items-center justify-between gap-4 bg-base-100 p-6 rounded-2xl border border-base-300 shadow-sm">
                <div>
                    <h1 class="text-2xl md:text-3xl font-black tracking-tight">%s</h1>
                    <p class="text-sm text-base-content/60 mt-1">%s</p>
                </div>

                <!-- Filters Form -->
                <form id="report-filters-form" method="GET" class="flex flex-wrap items-center gap-3">
                    <!-- Date Range Selector -->
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs font-semibold">Range</span></label>
                        <select name="range" class="select select-bordered select-sm font-medium" onchange="this.form.submit()">
                            <option value="30d" %s>Last 30 Days</option>
                            <option value="90d" %s>Last 90 Days</option>
                            <option value="180d" %s>Last 180 Days</option>
                            <option value="YTD" %s>Year to Date (YTD)</option>
                            <option value="1y" %s>Last 1 Year</option>
                            <option value="all" %s>All Time</option>
                        </select>
                    </div>

                    <!-- Display Currency Selector -->
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs font-semibold">Currency</span></label>
                        <select name="currency" class="select select-bordered select-sm font-medium" onchange="this.form.submit()">
                            <option value="PLN" %s>PLN</option>
                            <option value="EUR" %s>EUR</option>
                            <option value="USD" %s>USD</option>
                            <option value="GBP" %s>GBP</option>
                        </select>
                    </div>

                    <!-- Exclude Internal Transfers Toggle -->
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs font-semibold">Transfers</span></label>
                        <label class="label cursor-pointer gap-2 border border-base-300 px-3 py-1.5 rounded-lg bg-base-200">
                            <span class="label-text text-xs">Exclude Internal</span>
                            <input type="checkbox" name="exclude_internal" value="true" class="checkbox checkbox-sm checkbox-primary" %s onchange="this.form.submit()" />
                        </label>
                    </div>
                </form>
            </div>

            <!-- Full-Screen Interactive Chart Container -->
            <div class="card card-border bg-base-100 border-base-300 shadow-md flex-1 min-h-[500px]">
                <div class="card-body p-6 flex flex-col">
                    <div id="%s" class="w-full flex-1 min-h-[460px]"></div>
                </div>
            </div>
        </main>
    </div>

    %s

    <script>
        const chartBg = 'transparent';
        const textStyle = { color: 'rgba(255, 255, 255, 0.7)', fontSize: 12 };
        const budgetId = '%s';
        const endpoint = '%s';
        const chartId = '%s';
        const curr = '%s';
        const excludeInternal = %t;

        function formatMoney(minor) {
            return (minor / 100).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + ' ' + curr;
        }

        async function fetchJSON(url) {
            try {
                const res = await fetch(url);
                if (!res.ok) return null;
                return await res.json();
            } catch (e) {
                console.error("fetch error:", url, e);
                return null;
            }
        }

        (async () => {
            const chartDom = document.getElementById(chartId);
            if (!chartDom) return;
            const chart = echarts.init(chartDom);
            chart.showLoading({ color: '#3b82f6' });

            const dataUrl = '/budgets/' + budgetId + '/reports/' + endpoint + '?currency=' + curr + '&exclude_internal=' + excludeInternal;
            const data = await fetchJSON(dataUrl);
            chart.hideLoading();

            const type = '%s';
            if (type === 'age-of-money') {
                const history = (data && data.history && data.history.length > 0) ? data.history : [];
                const xData = history.map((h, i) => 'P' + (i + 1));
                const yData = history.map(h => h.age_days);

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis', formatter: '{b}: {c} days' },
                    grid: { top: 40, right: 30, bottom: 40, left: 50 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['Start'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    yAxis: { type: 'value', name: 'Days', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    series: [{ data: yData.length ? yData : [0], type: 'line', smooth: true, color: '#06b6d4', areaStyle: { opacity: 0.15 } }]
                });
            } else if (type === 'income-expense') {
                const months = (data && data.months) ? data.months : [];
                const xData = months.map(m => m.month_key);
                const incomeData = months.map(m => m.income_minor / 100);
                const expenseData = months.map(m => m.expense_minor / 100);
                const savingsData = months.map(m => m.net_savings_minor / 100);

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    legend: { textStyle: textStyle, top: 0, data: ['Income', 'Expense', 'Net Savings'] },
                    grid: { top: 50, right: 30, bottom: 40, left: 60 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    series: [
                        { name: 'Income', type: 'bar', data: incomeData, color: '#22c55e', borderRadius: [6, 6, 0, 0] },
                        { name: 'Expense', type: 'bar', data: expenseData, color: '#ef4444', borderRadius: [6, 6, 0, 0] },
                        { name: 'Net Savings', type: 'line', smooth: true, data: savingsData, color: '#3b82f6', lineStyle: { width: 3 } }
                    ]
                });
            } else if (type === 'spending-trends') {
                const pts = (data && data.points) ? data.points : [];
                const xData = pts.map(p => p.month_key);
                const totalData = pts.map(p => p.total_spent_minor / 100);
                const avgData = pts.map(p => p.moving_avg_minor / 100);

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    legend: { textStyle: textStyle, data: ['Total Spent', '3-Month Moving Average'], top: 0 },
                    grid: { top: 50, right: 30, bottom: 40, left: 60 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    series: [
                        { name: 'Total Spent', type: 'bar', data: totalData, color: '#ec4899', borderRadius: [6, 6, 0, 0] },
                        { name: '3-Month Moving Average', type: 'line', smooth: true, data: avgData, color: '#eab308', lineStyle: { width: 3 } }
                    ]
                });
            } else if (type === 'spending-breakdown') {
                const items = (data && data.items) ? data.items : [];
                const pieData = items.map(it => ({
                    name: it.bucket_name || 'Unassigned',
                    value: it.total_spent_minor / 100
                }));

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'item', formatter: '{b}: {c} ' + curr + ' ({d}%%)' },
                    legend: { type: 'scroll', orient: 'vertical', right: 20, top: 40, bottom: 20, textStyle: textStyle },
                    series: [{
                        type: 'pie',
                        radius: ['45%%', '75%%'],
                        center: ['40%%', '50%%'],
                        avoidLabelOverlap: false,
                        itemStyle: { borderRadius: 8, borderColor: '#1f2937', borderWidth: 2 },
                        label: { show: false },
                        emphasis: { label: { show: true, fontSize: 14, fontWeight: 'bold' } },
                        data: pieData.length ? pieData : [{ name: 'No Spending', value: 0 }]
                    }]
                });
            } else if (type === 'net-worth') {
                const pts = (data && data.points) ? data.points : [];
                const xData = pts.map(p => p.month_key);
                const netData = pts.map(p => p.net_worth_minor / 100);
                const assetData = pts.map(p => p.assets_minor / 100);
                const liabData = pts.map(p => p.liabilities_minor / 100);

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    legend: { textStyle: textStyle, data: ['Net Worth', 'Assets', 'Liabilities'], top: 0 },
                    grid: { top: 50, right: 30, bottom: 40, left: 70 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    series: [
                        { name: 'Net Worth', type: 'line', smooth: true, data: netData, color: '#3b82f6', areaStyle: { opacity: 0.2 }, lineStyle: { width: 3 } },
                        { name: 'Assets', type: 'line', smooth: true, data: assetData, color: '#22c55e', lineStyle: { type: 'dashed' } },
                        { name: 'Liabilities', type: 'line', smooth: true, data: liabData, color: '#ef4444', lineStyle: { type: 'dashed' } }
                    ]
                });
            } else if (type === 'sankey') {
                const nodes = (data && data.nodes) ? data.nodes.map(n => ({ name: n.name })) : [];
                const links = (data && data.links) ? data.links.map(l => ({
                    source: l.source,
                    target: l.target,
                    value: l.value_minor / 100
                })) : [];

                if (nodes.length === 0) {
                    nodes.push({ name: 'Income' }, { name: 'Envelopes' });
                    links.push({ source: 'Income', target: 'Envelopes', value: 0 });
                }

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'item', triggerOn: 'mousemove' },
                    series: [{
                        type: 'sankey',
                        layout: 'none',
                        top: 40,
                        bottom: 40,
                        left: 50,
                        right: 50,
                        emphasis: { focus: 'adjacency' },
                        lineStyle: { color: 'gradient', curveness: 0.5 },
                        data: nodes,
                        links: links
                    }]
                });
            } else if (type === 'forecast') {
                const pts = (data && data.points) ? data.points : [];
                const xData = pts.map(p => p.date);
                const yData = pts.map(p => p.projected_balance_minor / 100);

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    grid: { top: 40, right: 30, bottom: 40, left: 70 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['Today'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    series: [{ name: 'Projected Balance', data: yData.length ? yData : [0], type: 'line', smooth: true, color: '#6366f1', areaStyle: { opacity: 0.15 } }]
                });
            } else if (type === 'budget-analysis') {
                const pts = (data && data.points) ? data.points : [];
                const xData = pts.map(p => p.period_key);
                const budgetedData = pts.map(p => p.budgeted_minor / 100);
                const spentData = pts.map(p => p.spent_minor / 100);
                const cumData = pts.map(p => p.cumulative_balance_minor / 100);

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    legend: { textStyle: textStyle, data: ['Budgeted', 'Spent', 'Cumulative Balance'], top: 0 },
                    grid: { top: 50, right: 30, bottom: 40, left: 60 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['No Data'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    series: [
                        { name: 'Budgeted', type: 'bar', data: budgetedData, color: '#38bdf8', borderRadius: [6, 6, 0, 0] },
                        { name: 'Spent', type: 'bar', data: spentData, color: '#f87171', borderRadius: [6, 6, 0, 0] },
                        { name: 'Cumulative Balance', type: 'line', smooth: true, data: cumData, color: '#10b981', lineStyle: { width: 3 } }
                    ]
                });
            } else if (type === 'monte-carlo') {
                const p10 = (data && data.p10_trajectory) ? data.p10_trajectory.map(v => v / 100) : [];
                const p50 = (data && data.p50_trajectory) ? data.p50_trajectory.map(v => v / 100) : [];
                const p90 = (data && data.p90_trajectory) ? data.p90_trajectory.map(v => v / 100) : [];
                const xData = p50.map((_, i) => 'Age ' + (40 + i));

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    legend: { textStyle: textStyle, data: ['P90 Optimistic', 'P50 Median', 'P10 Conservative'], top: 0 },
                    grid: { top: 50, right: 40, bottom: 40, left: 70 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['Age 40', 'Age 65'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    series: [
                        { name: 'P90 Optimistic', type: 'line', data: p90, color: '#22c55e', smooth: true },
                        { name: 'P50 Median', type: 'line', data: p50, color: '#3b82f6', smooth: true },
                        { name: 'P10 Conservative', type: 'line', data: p10, color: '#f59e0b', smooth: true }
                    ]
                });
            } else if (type === 'party-cashflow') {
                const parties = (data && data.parties) ? data.parties.slice(0, 15) : [];
                const yAxisData = parties.map(p => p.party_name);
                const netData = parties.map(p => p.net_flow_minor / 100);

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    grid: { top: 30, right: 40, bottom: 40, left: 140 },
                    xAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } } },
                    yAxis: { type: 'category', data: yAxisData.length ? yAxisData : ['No Counterparties'], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
                    series: [{
                        name: 'Net Cashflow',
                        type: 'bar',
                        data: netData,
                        itemStyle: {
                            color: function(params) {
                                return params.value >= 0 ? '#22c55e' : '#ef4444';
                            },
                            borderRadius: [0, 6, 6, 0]
                        }
                    }]
                });
            }

            window.addEventListener('resize', () => chart.resize());
        })();
    </script>
</body>
</html>`,
		meta.Title,
		budgetID,
		meta.Title,
		budgetID,
		csrfToken,
		RenderSidebarNavigation(budgetID, "reports"),
		meta.Title,
		meta.Description,
		selectedAttr(selectedRange == "30d"),
		selectedAttr(selectedRange == "90d"),
		selectedAttr(selectedRange == "180d"),
		selectedAttr(selectedRange == "YTD"),
		selectedAttr(selectedRange == "1y"),
		selectedAttr(selectedRange == "all"),
		selectedAttr(selectedCurrency == "PLN"),
		selectedAttr(selectedCurrency == "EUR"),
		selectedAttr(selectedCurrency == "USD"),
		selectedAttr(selectedCurrency == "GBP"),
		checkedAttr(excludeInternal),
		meta.ChartID,
		RenderBottomDock(budgetID, "reports"),
		budgetID,
		meta.Endpoint,
		meta.ChartID,
		selectedCurrency,
		excludeInternal,
		meta.Type)

	return c.HTML(http.StatusOK, html)
}

func selectedAttr(b bool) string {
	if b {
		return "selected"
	}
	return ""
}

func checkedAttr(b bool) string {
	if b {
		return "checked"
	}
	return ""
}
