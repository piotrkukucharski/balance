function showDebugToast(message, isSuccess = true) {
    const toast = document.getElementById('debug-toast');
    if (!toast) return;
    const alertBox = document.getElementById('debug-toast-alert');
    const msgEl = document.getElementById('debug-toast-msg');
    const iconEl = document.getElementById('debug-toast-icon');

    if (msgEl) msgEl.innerText = message;
    if (iconEl) iconEl.innerText = isSuccess ? '✓' : '⚠️';
    if (alertBox) {
        alertBox.className = 'alert text-xs shadow-lg flex items-center gap-2 ' + (isSuccess ? 'alert-success' : 'alert-error');
    }
    toast.classList.remove('hidden');
    toast.style.opacity = '1';
    clearTimeout(window.__debugToastTimeout);
    window.__debugToastTimeout = setTimeout(() => {
        toast.style.opacity = '0';
        setTimeout(() => toast.classList.add('hidden'), 300);
    }, 4000);
}

window.showDebugToast = showDebugToast;

document.addEventListener('debugNotification', function(evt) {
    if (evt.detail && evt.detail.message) {
        showDebugToast(evt.detail.message, evt.detail.success !== false);
    }
});

async function runIntegrationAudit(type) {
    const isAI = type === 'ai';
    const btn = document.getElementById(isAI ? 'btn-audit-ai' : 'btn-audit-gc');
    const badge = document.getElementById(isAI ? 'ai-status-badge' : 'gc-status-badge');
    const output = document.getElementById(isAI ? 'ai-audit-output' : 'gc-audit-output');

    if (btn) btn.classList.add('loading');
    if (badge) {
        badge.className = 'badge badge-info text-xs';
        badge.innerText = 'Sprawdzanie...';
    }
    if (output) {
        output.classList.add('hidden');
        output.innerHTML = '';
    }

    try {
        const budgetIDMatch = window.location.pathname.match(/\/budgets\/([^\/]+)/);
        const bID = budgetIDMatch ? budgetIDMatch[1] : '';
        const endpoint = '/budgets/' + bID + '/debug/integrations/' + type + '/audit';

        const csrfToken = document.querySelector('input[name="csrf_token"]')?.value || '';

        const res = await fetch(endpoint, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'X-CSRF-Token': csrfToken
            }
        });

        const data = await res.json();
        if (output) {
            output.classList.remove('hidden');
            let content = '';
            if (res.ok && data.success) {
                output.className = 'mb-3 p-2.5 rounded-lg text-xs font-mono break-all whitespace-pre-wrap max-h-48 overflow-y-auto bg-success/10 border border-success/30 text-success';
                content = `✓ Status HTTP: ${data.status_code}\n${data.message}\nCorrelationID: ${data.correlation_id}`;
                if (badge) {
                    badge.className = 'badge badge-success text-xs font-bold';
                    badge.innerText = 'Działa poprawnie';
                }
            } else {
                output.className = 'mb-3 p-2.5 rounded-lg text-xs font-mono break-all whitespace-pre-wrap max-h-48 overflow-y-auto bg-error/10 border border-error/30 text-error';
                const status = data.status_code || res.status;
                const msg = data.message || (data.error ? data.error : JSON.stringify(data));
                const corr = data.correlation_id || res.headers.get('X-Correlation-ID') || '-';
                const raw = data.raw_response ? `\n\nOdpowiedź API:\n${data.raw_response}` : '';
                content = `⚠️ Status HTTP: ${status}\nBłąd: ${msg}\nCorrelationID: ${corr}${raw}`;
                if (badge) {
                    badge.className = 'badge badge-error text-xs font-bold';
                    badge.innerText = `Błąd (${status})`;
                }
            }
            output.innerText = content;
        }
    } catch (err) {
        if (output) {
            output.classList.remove('hidden');
            output.className = 'mb-3 p-2.5 rounded-lg text-xs font-mono break-all whitespace-pre-wrap bg-error/10 border border-error/30 text-error';
            output.innerText = 'Błąd wykonania żądania: ' + err.message;
        }
        if (badge) {
            badge.className = 'badge badge-error text-xs font-bold';
            badge.innerText = 'Awaria';
        }
    } finally {
        if (btn) btn.classList.remove('loading');
    }
}

window.runIntegrationAudit = runIntegrationAudit;
