const chartBg = 'transparent';
const textStyle = { color: 'rgba(255, 255, 255, 0.7)', fontSize: 11 };
const budgetId = window.budgetId;

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
        tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
        series: [{
            type: 'pie',
            radius: ['40%', '70%'],
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
        if (b) b.innerText = data.success_rate_pct.toFixed(1) + '% Success Rate';
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

window.reloadReport = reloadReport;
window.recalculateAllReports = recalculateAllReports;
