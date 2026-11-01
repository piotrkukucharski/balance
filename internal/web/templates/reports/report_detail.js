const chartBg = 'transparent';
const textStyle = { color: 'rgba(255, 255, 255, 0.7)', fontSize: 12 };
const budgetId = window.budgetId;
const endpoint = window.endpoint;
const chartId = window.chartId;
const type = window.reportType;

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
        const rate = inc > 0 ? ((net / inc) * 100).toFixed(1) + '%' : '0.0%';
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
        let pct = startNW !== 0 ? ((chg / Math.abs(startNW)) * 100).toFixed(1) + '%' : '0.0%';
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
            { label: 'Adherence', value: bud > 0 ? ((spent / bud) * 100).toFixed(1) + '%' : '100%' }
        ];
    } else if (type === 'monte-carlo') {
        const succ = (data && data.success_rate_pct != null) ? data.success_rate_pct : 0;
        const med = (data && data.median_ending_balance != null) ? data.median_ending_balance : 0;
        const risk = (data && data.chance_of_running_out != null) ? data.chance_of_running_out : 0;
        cards = [
            { label: 'Success Rate', value: succ + '%', color: succ >= 85 ? 'text-success' : (succ >= 70 ? 'text-warning' : 'text-error') },
            { label: 'Median Ending Capital', value: formatMoney(med, curr), color: 'text-primary' },
            { label: 'Risk of Depletion', value: risk + '%', color: risk <= 15 ? 'text-success' : 'text-error' },
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
            tooltip: { trigger: 'item', formatter: '{b}: {c} ' + curr + ' ({d}%)' },
            legend: { type: 'scroll', orient: 'vertical', right: 20, top: 40, bottom: 20, textStyle: textStyle },
            series: [{
                type: 'pie',
                radius: ['45%', '75%'],
                center: ['40%', '50%'],
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

window.updateReport = updateReport;
