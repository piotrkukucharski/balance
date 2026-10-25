package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	htmlPkg "html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
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
            <div class="mb-8 flex flex-col md:flex-row md:items-center justify-between gap-4">
                <div>
                    <h1 class="text-3xl font-black tracking-tight">Reports & Analytics</h1>
                    <p class="text-sm text-base-content/60 mt-1">10 interactive financial insights, forecasting models, and Monte Carlo longevity simulations.</p>
                </div>
                <div class="flex items-center gap-2">
                    <button type="button" id="btn-recalculate-all" class="btn btn-sm btn-outline gap-1.5" onclick="recalculateAllReports()">
                        <span id="recalc-all-icon">🔄</span>
                        <span>Przelicz wszystkie</span>
                    </button>
                </div>
            </div>

            %s

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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_aom" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('aom', true)">🔄</button>
                                <span id="badge_aom" class="badge badge-accent badge-soft font-bold">%d days</span>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_ive" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('ive', true)">🔄</button>
                                <span id="badge_ive" class="badge badge-success badge-soft font-bold">--</span>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_trends" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('trends', true)">🔄</button>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_breakdown" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('breakdown', true)">🔄</button>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_nw" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('nw', true)">🔄</button>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_sankey" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('sankey', true)">🔄</button>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_forecast" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('forecast', true)">🔄</button>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_analysis" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('analysis', true)">🔄</button>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_mc" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('mc', true)">🔄</button>
                                <span id="badge_mc" class="badge badge-success badge-soft font-bold text-sm py-3 px-4">--%% Success Rate</span>
                            </div>
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
                            <div class="flex items-center gap-1.5">
                                <span id="cache_status_party" class="badge badge-xs badge-neutral" title="Cache Status">--</span>
                                <button type="button" class="btn btn-ghost btn-xs btn-circle" title="Przelicz" onclick="reloadReport('party', true)">🔄</button>
                            </div>
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

        function updateCacheBadge(id, isCached, calculatedAt) {
            const el = document.getElementById(id);
            if (!el) return;
            if (isCached) {
                el.className = 'badge badge-xs badge-neutral';
                el.innerText = 'Cached';
                if (calculatedAt) {
                    const dt = new Date(calculatedAt / 1000);
                    el.title = 'Zbuforowano: ' + dt.toLocaleTimeString();
                }
            } else {
                el.className = 'badge badge-xs badge-success';
                el.innerText = 'Live';
                if (calculatedAt) {
                    const dt = new Date(calculatedAt / 1000);
                    el.title = 'Przeliczono na żywo: ' + dt.toLocaleTimeString();
                }
            }
        }

        const reportLoaders = {};
        const charts = {};

        async function reloadReport(key, force) {
            if (reportLoaders[key]) {
                await reportLoaders[key](!!force);
            }
        }

        async function recalculateAllReports() {
            const icon = document.getElementById('recalc-all-icon');
            if (icon) icon.classList.add('animate-spin');
            for (const key of Object.keys(reportLoaders)) {
                await reportLoaders[key](true);
            }
            if (icon) icon.classList.remove('animate-spin');
        }

        // 1. Age of Money
        reportLoaders['aom'] = async (force) => {
            const dom = document.getElementById('chart_aom');
            if (!dom) return;
            if (!charts['aom']) charts['aom'] = echarts.init(dom);
            const chart = charts['aom'];
            chart.showLoading({ color: '#06b6d4' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/age-of-money/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_aom', data && data.is_cached, data && data.calculated_at);

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
        };

        // 2. Income vs Expense
        reportLoaders['ive'] = async (force) => {
            const dom = document.getElementById('chart_ive');
            if (!dom) return;
            if (!charts['ive']) charts['ive'] = echarts.init(dom);
            const chart = charts['ive'];
            chart.showLoading({ color: '#22c55e' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/income-expense/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_ive', data && data.is_cached, data && data.calculated_at);

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
        };

        // 3. Spending Trends
        reportLoaders['trends'] = async (force) => {
            const dom = document.getElementById('chart_trends');
            if (!dom) return;
            if (!charts['trends']) charts['trends'] = echarts.init(dom);
            const chart = charts['trends'];
            chart.showLoading({ color: '#a855f7' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/spending-trends/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_trends', data && data.is_cached, data && data.calculated_at);

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
        };

        // 4. Spending Breakdown
        reportLoaders['breakdown'] = async (force) => {
            const dom = document.getElementById('chart_breakdown');
            if (!dom) return;
            if (!charts['breakdown']) charts['breakdown'] = echarts.init(dom);
            const chart = charts['breakdown'];
            chart.showLoading({ color: '#f97316' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/spending-breakdown/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_breakdown', data && data.is_cached, data && data.calculated_at);

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
        };

        // 5. Net Worth
        reportLoaders['nw'] = async (force) => {
            const dom = document.getElementById('chart_networth');
            if (!dom) return;
            if (!charts['nw']) charts['nw'] = echarts.init(dom);
            const chart = charts['nw'];
            chart.showLoading({ color: '#3b82f6' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/net-worth/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_nw', data && data.is_cached, data && data.calculated_at);

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
        };

        // 6. Sankey Flow
        reportLoaders['sankey'] = async (force) => {
            const dom = document.getElementById('chart_sankey');
            if (!dom) return;
            if (!charts['sankey']) charts['sankey'] = echarts.init(dom);
            const chart = charts['sankey'];
            chart.showLoading({ color: '#10b981' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/sankey/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_sankey', data && data.is_cached, data && data.calculated_at);

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
        };

        // 7. Balance Forecast
        reportLoaders['forecast'] = async (force) => {
            const dom = document.getElementById('chart_forecast');
            if (!dom) return;
            if (!charts['forecast']) charts['forecast'] = echarts.init(dom);
            const chart = charts['forecast'];
            chart.showLoading({ color: '#6366f1' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/forecast/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_forecast', data && data.is_cached, data && data.calculated_at);

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
        };

        // 8. Budget Analysis
        reportLoaders['analysis'] = async (force) => {
            const dom = document.getElementById('chart_analysis');
            if (!dom) return;
            if (!charts['analysis']) charts['analysis'] = echarts.init(dom);
            const chart = charts['analysis'];
            chart.showLoading({ color: '#14b8a6' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/budget-analysis/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_analysis', data && data.is_cached, data && data.calculated_at);

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
        };

        // 9. Monte Carlo
        reportLoaders['mc'] = async (force) => {
            const dom = document.getElementById('chart_mc');
            if (!dom) return;
            if (!charts['mc']) charts['mc'] = echarts.init(dom);
            const chart = charts['mc'];
            chart.showLoading({ color: '#22c55e' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/monte-carlo/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_mc', data && data.is_cached, data && data.calculated_at);

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
        };

        // 10. Counterparty Cashflow
        reportLoaders['party'] = async (force) => {
            const dom = document.getElementById('chart_party');
            if (!dom) return;
            if (!charts['party']) charts['party'] = echarts.init(dom);
            const chart = charts['party'];
            chart.showLoading({ color: '#8b5cf6' });
            const data = await fetchJSON('/budgets/' + budgetId + '/reports/party-cashflow/data' + (force ? '?force=true' : ''));
            chart.hideLoading();
            updateCacheBadge('cache_status_party', data && data.is_cached, data && data.calculated_at);

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
        };

        // Initial load of all 10 reports
        (async () => {
            for (const key of Object.keys(reportLoaders)) {
                reportLoaders[key](false);
            }
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
		h.renderSavedReportsSection(c.Request().Context(), tenantDB, budgetID, csrfToken),
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
	savedPresetsOptions := ""
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
				star := ""
				if sPinned == 1 {
					star = " ⭐"
				}
				savedPresetsOptions += fmt.Sprintf(`<option value="%s" data-config="%s">%s%s</option>`, sID, htmlPkg.EscapeString(sCfg), htmlPkg.EscapeString(sName), star)
			}
		}
	}

	monteCarloControls := ""
	if meta.Type == "monte-carlo" {
		monteCarloControls = `
            <div id="monte-carlo-settings" class="card card-border bg-base-100 border-base-300 p-6 rounded-2xl shadow-xs">
                <div class="flex items-center justify-between mb-4">
                    <h2 class="text-base font-bold flex items-center gap-2">
                        <span>🎲</span>
                        <span>Retirement Plan & Longevity Simulation Parameters</span>
                    </h2>
                    <span class="badge badge-primary badge-soft text-xs">Multi-Pot & Dynamic Withdrawals</span>
                </div>
                <div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-6 gap-3">
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs">Current Age</span></label>
                        <input type="number" id="mc_current_age" name="current_age" value="40" min="18" max="100" class="input input-bordered input-sm font-mono" />
                    </div>
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs">Target Age</span></label>
                        <input type="number" id="mc_target_age" name="target_age" value="65" min="30" max="120" class="input input-bordered input-sm font-mono" />
                    </div>
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs">Annual Spend</span></label>
                        <input type="number" id="mc_annual_spend" name="annual_spend" value="50000" min="1000" step="5000" class="input input-bordered input-sm font-mono" />
                    </div>
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs">Return (%%)</span></label>
                        <input type="number" id="mc_expected_return" name="expected_return_pct" value="7.0" step="0.5" class="input input-bordered input-sm font-mono" />
                    </div>
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs">Inflation (%%)</span></label>
                        <input type="number" id="mc_inflation" name="inflation_pct" value="2.5" step="0.5" class="input input-bordered input-sm font-mono" />
                    </div>
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs">Withdrawal Rule</span></label>
                        <select id="mc_withdrawal_rule" name="withdrawal_rule" class="select select-bordered select-sm">
                            <option value="FIXED_REAL">Fixed Real</option>
                            <option value="GUARDRAILS_GUYTON_KLINGER">Guyton-Klinger</option>
                            <option value="RATCHETING_KITCES">Kitces Ratchet</option>
                        </select>
                    </div>
                </div>
                <div class="grid grid-cols-1 md:grid-cols-2 gap-3 mt-3 pt-3 border-t border-base-200">
                    <div class="flex items-center gap-2">
                        <label class="text-xs font-semibold whitespace-nowrap">External Pension (e.g. ZUS):</label>
                        <input type="number" id="mc_pension_amount" placeholder="Annual Amount (e.g. 36000)" class="input input-bordered input-sm font-mono flex-1" />
                        <label class="text-xs whitespace-nowrap">From age:</label>
                        <input type="number" id="mc_pension_from" value="65" min="50" max="100" class="input input-bordered input-sm font-mono w-20" />
                    </div>
                    <div class="flex justify-end gap-2">
                        <button type="button" id="btn-run-simulation" class="btn btn-sm btn-primary gap-1">
                            <span>▶</span>
                            <span>Run Stochastic Simulation</span>
                        </button>
                    </div>
                </div>
            </div>`
	}

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
                    <div class="flex items-center gap-2 mt-2">
                        <span id="cache-status-pill" class="badge badge-xs badge-neutral">Checking...</span>
                        <span id="cache-calc-time" class="text-xs text-base-content/60 font-mono"></span>
                    </div>
                </div>

                <!-- Filters Form -->
                <form id="report-filters-form" method="GET" class="flex flex-wrap items-center gap-3">
                    <!-- Presets Selector -->
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs font-semibold">Preset</span></label>
                        <select id="saved-presets-select" class="select select-bordered select-sm font-medium">
                            <option value="">-- Presets --</option>
                            %s
                        </select>
                    </div>

                    <!-- Date Range Selector -->
                    <div class="form-control">
                        <label class="label py-1"><span class="label-text text-xs font-semibold">Range</span></label>
                        <select name="range" class="select select-bordered select-sm font-medium">
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
                        <select name="currency" class="select select-bordered select-sm font-medium">
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
                            <input type="checkbox" name="exclude_internal" value="true" class="checkbox checkbox-sm checkbox-primary" %s />
                        </label>
                    </div>

                    <!-- Recalculate Button -->
                    <div class="form-control self-end">
                        <button type="button" id="btn-recalculate-report" class="btn btn-outline btn-sm gap-1.5" onclick="updateReport(true)">
                            <span id="recalc-detail-icon">🔄</span>
                            <span>Przelicz</span>
                        </button>
                    </div>

                    <!-- Save Report Modal Trigger -->
                    <div class="form-control self-end">
                        <button type="button" class="btn btn-primary btn-sm gap-1" onclick="document.getElementById('save_report_modal').showModal()">
                            <span>💾</span>
                            <span>Save Configuration</span>
                        </button>
                    </div>
                </form>
            </div>

            %s

            <!-- KPI Metric Cards Grid -->
            <div id="report-kpi-cards" class="grid grid-cols-2 md:grid-cols-4 gap-4"></div>

            <!-- Save Report Modal -->
            <dialog id="save_report_modal" class="modal">
                <div class="modal-box">
                    <form method="dialog">
                        <button class="btn btn-sm btn-circle btn-ghost absolute right-2 top-2">✕</button>
                    </form>
                    <h3 class="font-bold text-lg mb-4">Save Report Configuration</h3>
                    <form method="POST" action="/budgets/%s/reports/saved" class="flex flex-col gap-4">
                        <input type="hidden" name="csrf_token" value="%s" />
                        <input type="hidden" name="report_type" value="%s" />
                        <input type="hidden" id="save_config_json" name="config_json" value='{"range":"%s","currency":"%s","exclude_internal":%t}' />
                        
                        <div class="form-control">
                            <label class="label"><span class="label-text font-semibold">Report Name</span></label>
                            <input type="text" name="name" class="input input-bordered w-full" placeholder="e.g. Monthly Operations" required />
                        </div>

                        <div class="form-control">
                            <label class="label cursor-pointer justify-start gap-3">
                                <input type="checkbox" name="is_pinned_widget" value="true" class="checkbox checkbox-primary" />
                                <span class="label-text">Pin as favorite widget on Reports Dashboard</span>
                            </label>
                        </div>

                        <div class="modal-action">
                            <button type="button" class="btn btn-ghost" onclick="document.getElementById('save_report_modal').close()">Cancel</button>
                            <button type="submit" class="btn btn-primary">Save Widget</button>
                        </div>
                    </form>
                </div>
                <form method="dialog" class="modal-backdrop">
                    <button>close</button>
                </form>
            </dialog>

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
        const type = '%s';

        function formatMoney(minor, c) {
            if (isNaN(minor)) minor = 0;
            return (minor / 100).toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + ' ' + (c || 'PLN');
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

        let chart = null;

        function updateKPICards(type, data, curr) {
            const kpiContainer = document.getElementById('report-kpi-cards');
            if (!kpiContainer) return;
            let cards = [];

            if (type === 'age-of-money') {
                const headline = (data && data.headline_days != null) ? data.headline_days : 0;
                const pts = (data && data.history) ? data.history.length : 0;
                let status = 'Moderate';
                let badgeColor = 'badge-warning';
                if (headline >= 30) { status = 'Healthy (>30d)'; badgeColor = 'badge-success'; }
                else if (headline < 10) { status = 'At Risk (<10d)'; badgeColor = 'badge-error'; }
                cards = [
                    { label: 'Age of Money', value: headline + ' days', color: 'text-accent' },
                    { label: 'Buffer Health', value: status, badge: badgeColor },
                    { label: 'Sample Outflows', value: pts },
                    { label: 'Evaluation Model', value: 'FIFO Liquid Queue' }
                ];
            } else if (type === 'income-expense') {
                const inc = (data && data.total_income_minor) || 0;
                const exp = (data && data.total_expense_minor) || 0;
                const net = (data && data.total_net_minor) || 0;
                const rate = inc > 0 ? ((net / inc) * 100).toFixed(1) + '%%' : '0.0%%';
                cards = [
                    { label: 'Total Inflow', value: formatMoney(inc, curr), color: 'text-success' },
                    { label: 'Total Outflow', value: formatMoney(exp, curr), color: 'text-error' },
                    { label: 'Net Savings', value: formatMoney(net, curr), color: net >= 0 ? 'text-primary' : 'text-error' },
                    { label: 'Savings Rate', value: rate, color: 'text-info' }
                ];
            } else if (type === 'spending-trends') {
                const pts = (data && data.points) || [];
                let total = 0;
                pts.forEach(p => total += (p.total_spent_minor || 0));
                const avg = pts.length > 0 ? Math.round(total / pts.length) : 0;
                cards = [
                    { label: 'Total Spent', value: formatMoney(total, curr), color: 'text-primary' },
                    { label: 'Monthly Average', value: formatMoney(avg, curr), color: 'text-warning' },
                    { label: 'Intervals Analyzed', value: pts.length + ' months' },
                    { label: 'Calculation Mode', value: '3-Mo Moving Avg' }
                ];
            } else if (type === 'spending-breakdown') {
                const items = (data && data.items) || [];
                let total = (data && data.total_spent_minor) || 0;
                let topCat = items.length > 0 ? (items[0].bucket_name || 'Unassigned') : 'None';
                let topAmt = items.length > 0 ? items[0].total_spent_minor : 0;
                cards = [
                    { label: 'Total Outflow', value: formatMoney(total, curr), color: 'text-primary' },
                    { label: 'Top Envelope', value: topCat, color: 'text-secondary' },
                    { label: 'Top Category Amount', value: formatMoney(topAmt, curr) },
                    { label: 'Envelopes Active', value: items.length }
                ];
            } else if (type === 'net-worth') {
                const curNW = (data && data.current_net_worth_minor) || 0;
                const pts = (data && data.points) || [];
                let startNW = pts.length > 0 ? pts[0].net_worth_minor : curNW;
                let chg = curNW - startNW;
                let pct = startNW !== 0 ? ((chg / Math.abs(startNW)) * 100).toFixed(1) + '%%' : '0.0%%';
                cards = [
                    { label: 'Current Net Worth', value: formatMoney(curNW, curr), color: curNW >= 0 ? 'text-success' : 'text-error' },
                    { label: 'Starting Balance', value: formatMoney(startNW, curr) },
                    { label: 'Net Period Change', value: (chg >= 0 ? '+' : '') + formatMoney(chg, curr), color: chg >= 0 ? 'text-success' : 'text-error' },
                    { label: 'Trajectory Shift', value: (chg >= 0 ? '+' : '') + pct }
                ];
            } else if (type === 'sankey') {
                const nodes = (data && data.nodes) || [];
                const links = (data && data.links) || [];
                let totalFlow = 0;
                links.forEach(l => { if (l.source === 'Income' || l.source === 'Total Pool') totalFlow += (l.value_minor || 0); });
                cards = [
                    { label: 'Flow Nodes', value: nodes.length },
                    { label: 'Active Channels', value: links.length },
                    { label: 'Aggregated Inflow', value: formatMoney(totalFlow, curr), color: 'text-success' },
                    { label: 'Topology Engine', value: 'Multi-Tier Sankey' }
                ];
            } else if (type === 'forecast') {
                const cur = (data && data.current_balance_minor) || 0;
                const pts = (data && data.points) || [];
                let lastPt = pts.length > 0 ? pts[pts.length - 1].projected_balance_minor : cur;
                cards = [
                    { label: 'Current Liquidity', value: formatMoney(cur, curr), color: 'text-primary' },
                    { label: '90-Day Projected', value: formatMoney(lastPt, curr), color: lastPt >= 0 ? 'text-success' : 'text-error' },
                    { label: 'Runway Projection', value: (data && data.runway_days) ? data.runway_days + ' days' : '> 90 days' },
                    { label: 'Confidence Band', value: 'Scheduled + Cycles' }
                ];
            } else if (type === 'budget-analysis') {
                const pts = (data && data.points) || [];
                let bud = 0, spent = 0;
                pts.forEach(p => { bud += (p.budgeted_minor || 0); spent += (p.spent_minor || 0); });
                const variance = bud - spent;
                cards = [
                    { label: 'Total Budgeted', value: formatMoney(bud, curr), color: 'text-info' },
                    { label: 'Actual Spent', value: formatMoney(spent, curr), color: 'text-error' },
                    { label: 'Variance / Buffer', value: formatMoney(variance, curr), color: variance >= 0 ? 'text-success' : 'text-warning' },
                    { label: 'Adherence', value: bud > 0 ? ((spent / bud) * 100).toFixed(1) + '%%' : '100%%' }
                ];
            } else if (type === 'monte-carlo') {
                const succ = (data && data.success_rate_pct != null) ? data.success_rate_pct : 0;
                const med = (data && data.median_ending_balance != null) ? data.median_ending_balance : 0;
                const risk = (data && data.chance_of_running_out != null) ? data.chance_of_running_out : 0;
                cards = [
                    { label: 'Success Rate', value: succ + '%%', color: succ >= 85 ? 'text-success' : (succ >= 70 ? 'text-warning' : 'text-error') },
                    { label: 'Median Ending Capital', value: formatMoney(med, curr), color: 'text-primary' },
                    { label: 'Risk of Depletion', value: risk + '%%', color: risk <= 15 ? 'text-success' : 'text-error' },
                    { label: 'Stochastic Iterations', value: '1,000 parallel' }
                ];
            } else if (type === 'party-cashflow') {
                const parties = (data && data.parties) || [];
                const inFlow = (data && data.total_inflow_minor) || 0;
                const outFlow = (data && data.total_outflow_minor) || 0;
                const net = inFlow - outFlow;
                cards = [
                    { label: 'Counterparty Inflow', value: formatMoney(inFlow, curr), color: 'text-success' },
                    { label: 'Counterparty Outflow', value: formatMoney(outFlow, curr), color: 'text-error' },
                    { label: 'Net Counterparty Flow', value: formatMoney(net, curr), color: net >= 0 ? 'text-success' : 'text-error' },
                    { label: 'Active Entities', value: parties.length }
                ];
            }

            kpiContainer.innerHTML = cards.map(function(c) {
                var badgeHtml = c.badge ? '<span class="badge ' + c.badge + ' badge-sm font-semibold">' + c.value + '</span>' : '';
                return '<div class="card card-border bg-base-100 border-base-300 p-4 rounded-xl shadow-xs flex flex-col gap-1">' +
                    '<span class="text-xs font-semibold text-base-content/60 tracking-wide uppercase">' + c.label + '</span>' +
                    '<div class="flex items-center gap-2">' +
                        '<span class="text-xl md:text-2xl font-black tabular-nums tracking-tight ' + (c.color || 'text-base-content') + '">' + c.value + '</span>' +
                        badgeHtml +
                    '</div>' +
                '</div>';
            }).join('');
        }

        function renderChart(type, data, curr) {
            if (!chart) return;

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
                const startAge = (data && data.current_age) ? data.current_age : (parseInt(document.getElementById('mc_current_age') ? document.getElementById('mc_current_age').value : 40) || 40);
                const xData = p50.map((_, i) => 'Age ' + (startAge + i));

                chart.setOption({
                    backgroundColor: chartBg,
                    textStyle: textStyle,
                    tooltip: { trigger: 'axis' },
                    legend: { textStyle: textStyle, data: ['P90 Optimistic', 'P50 Median', 'P10 Conservative'], top: 0 },
                    grid: { top: 50, right: 40, bottom: 40, left: 70 },
                    xAxis: { type: 'category', data: xData.length ? xData : ['Age ' + startAge, 'Age ' + (startAge + 25)], axisLine: { lineStyle: { color: 'rgba(255,255,255,0.2)' } } },
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
        }

        async function updateReport(force) {
            const form = document.getElementById('report-filters-form');
            if (!form) return;
            const range = form.elements['range'] ? form.elements['range'].value : '30d';
            const curr = form.elements['currency'] ? form.elements['currency'].value : 'PLN';
            const excludeInternal = form.elements['exclude_internal'] ? form.elements['exclude_internal'].checked : true;

            const params = new URLSearchParams({ range: range, currency: curr, exclude_internal: excludeInternal });
            if (force) {
                params.set('force', 'true');
            }

            const saveCfg = { range: range, currency: curr, exclude_internal: excludeInternal };
            if (type === 'monte-carlo') {
                const mcCur = document.getElementById('mc_current_age');
                const mcTgt = document.getElementById('mc_target_age');
                const mcSpend = document.getElementById('mc_annual_spend');
                const mcRet = document.getElementById('mc_expected_return');
                const mcInf = document.getElementById('mc_inflation');
                const mcRule = document.getElementById('mc_withdrawal_rule');
                const mcPAmt = document.getElementById('mc_pension_amount');
                const mcPFrom = document.getElementById('mc_pension_from');

                if (mcCur && mcCur.value) { params.set('current_age', mcCur.value); saveCfg.current_age = parseInt(mcCur.value); }
                if (mcTgt && mcTgt.value) { params.set('target_age', mcTgt.value); saveCfg.target_age = parseInt(mcTgt.value); }
                if (mcSpend && mcSpend.value) {
                    const spendMinor = Math.round(parseFloat(mcSpend.value) * 100);
                    params.set('annual_spend', spendMinor);
                    saveCfg.annual_spend = spendMinor;
                }
                if (mcRet && mcRet.value) { params.set('expected_return_pct', mcRet.value); saveCfg.expected_return_pct = parseFloat(mcRet.value); }
                if (mcInf && mcInf.value) { params.set('inflation_pct', mcInf.value); saveCfg.inflation_pct = parseFloat(mcInf.value); }
                if (mcRule && mcRule.value) { params.set('withdrawal_rule', mcRule.value); saveCfg.withdrawal_rule = mcRule.value; }
                if (mcPAmt && mcPAmt.value) {
                    const pMinor = Math.round(parseFloat(mcPAmt.value) * 100);
                    params.set('pension_amount', pMinor);
                    saveCfg.pension_amount = pMinor;
                    if (mcPFrom && mcPFrom.value) {
                        params.set('pension_from_age', mcPFrom.value);
                        saveCfg.pension_from_age = parseInt(mcPFrom.value);
                    }
                }
            }

            window.history.replaceState(null, '', '?' + params.toString());

            const saveCfgInput = document.getElementById('save_config_json');
            if (saveCfgInput) {
                saveCfgInput.value = JSON.stringify(saveCfg);
            }

            const recalcIcon = document.getElementById('recalc-detail-icon');
            if (recalcIcon) recalcIcon.classList.add('animate-spin');
            if (chart) chart.showLoading({ color: '#3b82f6' });
            const dataUrl = '/budgets/' + budgetId + '/reports/' + endpoint + '?' + params.toString();
            const data = await fetchJSON(dataUrl);
            if (chart) chart.hideLoading();
            if (recalcIcon) recalcIcon.classList.remove('animate-spin');

            const pill = document.getElementById('cache-status-pill');
            const timeEl = document.getElementById('cache-calc-time');
            if (pill && timeEl) {
                if (data && data.is_cached) {
                    pill.className = 'badge badge-xs badge-neutral';
                    pill.innerText = 'Cached';
                    const dt = data.calculated_at ? new Date(data.calculated_at / 1000) : new Date();
                    timeEl.innerText = 'Ostatnie przeliczenie: ' + dt.toLocaleTimeString() + ' (z pamięci podręcznej)';
                } else {
                    pill.className = 'badge badge-xs badge-success';
                    pill.innerText = 'Live';
                    const dt = (data && data.calculated_at) ? new Date(data.calculated_at / 1000) : new Date();
                    timeEl.innerText = 'Przeliczono na żywo: ' + dt.toLocaleTimeString();
                }
            }

            renderChart(type, data, curr);
            updateKPICards(type, data, curr);
        }

        const chartDom = document.getElementById(chartId);
        if (chartDom) {
            chart = echarts.init(chartDom);
            updateReport(false);
        }

        const runSimBtn = document.getElementById('btn-run-simulation');
        if (runSimBtn) {
            runSimBtn.addEventListener('click', (e) => {
                e.preventDefault();
                updateReport(true);
            });
        }

        const presetsSelect = document.getElementById('saved-presets-select');
        if (presetsSelect) {
            presetsSelect.addEventListener('change', (e) => {
                const opt = presetsSelect.options[presetsSelect.selectedIndex];
                const cfgStr = opt ? opt.getAttribute('data-config') : null;
                if (!cfgStr) return;
                try {
                    const cfg = JSON.parse(cfgStr);
                    const filterForm = document.getElementById('report-filters-form');
                    if (filterForm) {
                        if (cfg.range && filterForm.elements['range']) filterForm.elements['range'].value = cfg.range;
                        if (cfg.currency && filterForm.elements['currency']) filterForm.elements['currency'].value = cfg.currency;
                        if (cfg.exclude_internal != null && filterForm.elements['exclude_internal']) filterForm.elements['exclude_internal'].checked = !!cfg.exclude_internal;
                    }
                    if (type === 'monte-carlo') {
                        if (cfg.current_age && document.getElementById('mc_current_age')) document.getElementById('mc_current_age').value = cfg.current_age;
                        if (cfg.target_age && document.getElementById('mc_target_age')) document.getElementById('mc_target_age').value = cfg.target_age;
                        if (cfg.annual_spend && document.getElementById('mc_annual_spend')) document.getElementById('mc_annual_spend').value = Math.round(cfg.annual_spend / 100);
                        if (cfg.expected_return_pct && document.getElementById('mc_expected_return')) document.getElementById('mc_expected_return').value = cfg.expected_return_pct;
                        if (cfg.inflation_pct && document.getElementById('mc_inflation')) document.getElementById('mc_inflation').value = cfg.inflation_pct;
                        if (cfg.withdrawal_rule && document.getElementById('mc_withdrawal_rule')) document.getElementById('mc_withdrawal_rule').value = cfg.withdrawal_rule;
                        if (cfg.pension_amount && document.getElementById('mc_pension_amount')) document.getElementById('mc_pension_amount').value = Math.round(cfg.pension_amount / 100);
                        if (cfg.pension_from_age && document.getElementById('mc_pension_from')) document.getElementById('mc_pension_from').value = cfg.pension_from_age;
                    }
                    updateReport();
                } catch (err) {
                    console.error("error parsing preset config:", err);
                }
            });
        }

        const filterForm = document.getElementById('report-filters-form');
        if (filterForm) {
            filterForm.addEventListener('change', (e) => {
                e.preventDefault();
                updateReport();
            });
            filterForm.addEventListener('submit', (e) => {
                e.preventDefault();
                updateReport();
            });
        }

        window.addEventListener('resize', () => {
            if (chart) chart.resize();
        });
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
		savedPresetsOptions,
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
		monteCarloControls,
		budgetID,
		csrfToken,
		reportTypeEnum,
		selectedRange,
		selectedCurrency,
		excludeInternal,
		meta.ChartID,
		RenderBottomDock(budgetID, "reports"),
		budgetID,
		meta.Endpoint,
		meta.ChartID,
		meta.Type)

	return c.HTML(http.StatusOK, html)

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

func (h *ReportsHandler) renderSavedReportsSection(ctx context.Context, tenantDB *sql.DB, budgetID string, csrfToken string) string {
	rows, err := tenantDB.QueryContext(ctx, `
		SELECT id, name, report_type, is_pinned_widget, updated_at
		FROM saved_reports_current
		ORDER BY is_pinned_widget DESC, updated_at DESC`)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var cardsHTML string
	count := 0
	for rows.Next() {
		count++
		var id, name, reportType string
		var isPinned int
		var updatedAt int64
		if err := rows.Scan(&id, &name, &reportType, &isPinned, &updatedAt); err != nil {
			continue
		}

		pinBadge := ""
		pinText := "Pin"
		if isPinned == 1 {
			pinBadge = `<span class="badge badge-primary badge-sm">Pinned</span>`
			pinText = "Unpin"
		}

		cardsHTML += fmt.Sprintf(`
			<div class="card bg-base-100 border border-base-300 shadow-sm p-4 flex flex-row items-center justify-between gap-4">
				<div class="flex items-center gap-3">
					<div class="text-xl">📊</div>
					<div>
						<div class="flex items-center gap-2">
							<span class="font-bold text-sm">%s</span>
							%s
						</div>
						<span class="text-xs text-base-content/60 font-mono">%s</span>
					</div>
				</div>
				<div class="flex items-center gap-2">
					<form method="POST" action="/budgets/%s/reports/saved/%s/toggle-pin">
						<input type="hidden" name="csrf_token" value="%s" />
						<button type="submit" class="btn btn-ghost btn-xs">%s</button>
					</form>
					<form method="POST" action="/budgets/%s/reports/saved/%s/delete" onsubmit="return confirm('Delete this saved report?')">
						<input type="hidden" name="csrf_token" value="%s" />
						<button type="submit" class="btn btn-ghost btn-xs text-error">Delete</button>
					</form>
				</div>
			</div>`, name, pinBadge, reportType, budgetID, id, csrfToken, pinText, budgetID, id, csrfToken)
	}

	if count == 0 {
		return `
			<div class="mb-8 p-6 bg-base-100 rounded-2xl border border-base-300">
				<div class="flex items-center justify-between">
					<div>
						<h2 class="text-lg font-bold">Saved Report Configurations</h2>
						<p class="text-xs text-base-content/60">No saved reports yet. Customize any report and click Save Configuration.</p>
					</div>
				</div>
			</div>`
	}

	return fmt.Sprintf(`
		<div class="mb-8 p-6 bg-base-100 rounded-2xl border border-base-300">
			<div class="flex items-center justify-between mb-4">
				<div>
					<h2 class="text-lg font-bold">Saved Report Configurations</h2>
					<p class="text-xs text-base-content/60">Active saved reports and pinned dashboard widgets.</p>
				</div>
				<span class="badge badge-neutral font-mono">%d saved</span>
			</div>
			<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
				%s
			</div>
		</div>`, count, cardsHTML)
}
