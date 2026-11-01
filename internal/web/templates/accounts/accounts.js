let wizardState = {
    provider: 'MOCK',
    country: 'PL',
    institutions: [],
    selectedInstitutionIDs: new Set()
};

function resetBankWizard() {
    goToStep(1);
    wizardState.selectedInstitutionIDs.clear();
    updateSelectedCount();
}

function goToStep(step) {
    [1, 2, 3, 4].forEach(s => {
        const el = document.getElementById('bank_step_' + s);
        const dot = document.getElementById('step_dot_' + s);
        if (el) {
            if (s === step) el.classList.remove('hidden');
            else el.classList.add('hidden');
        }
        if (dot) {
            if (s <= step) {
                dot.classList.add('bg-primary');
                dot.classList.remove('bg-base-300');
            } else {
                dot.classList.remove('bg-primary');
                dot.classList.add('bg-base-300');
            }
        }
    });

    const subtitle = document.getElementById('bank_wizard_subtitle');
    if (subtitle) {
        if (step === 1) subtitle.innerText = 'Step 1 of 3: Choose Integration Provider';
        else if (step === 2) subtitle.innerText = 'Step 2 of 3: Select Country (' + wizardState.provider + ')';
        else if (step === 3) subtitle.innerText = 'Step 3 of 3: Choose Bank Institutions (' + wizardState.country + ')';
        else if (step === 4) subtitle.innerText = 'Connecting Bank Accounts';
    }
}

function selectProvider(p) {
    wizardState.provider = p;
    if (p === 'GOCARDLESS_SANDBOX') {
        wizardState.country = 'PL';
        wizardState.selectedInstitutionIDs.clear();
        wizardState.selectedInstitutionIDs.add('SANDBOXFINANCE_SFIN0000');
        updateSelectedCount();
        startConnectionQueue();
        return;
    }
    goToStep(2);
}

function selectCountry(c) {
    wizardState.country = c;
    goToStep(3);
    loadInstitutions();
}

async function loadInstitutions() {
    const container = document.getElementById('institutions_list_container');
    container.innerHTML = '<div class="py-8 text-center text-xs text-base-content/50 flex flex-col items-center gap-2"><span class="loading loading-spinner loading-sm text-primary"></span><span>Loading institutions for ' + wizardState.country + '...</span></div>';

    try {
        const res = await fetch('/budgets/' + window.ACTIVE_BUDGET_ID + '/accounts/institutions?provider=' + wizardState.provider + '&country=' + wizardState.country);
        const data = await res.json();
        wizardState.institutions = data.institutions || [];
        renderInstitutions(wizardState.institutions);
    } catch (err) {
        container.innerHTML = '<div class="py-6 text-center text-xs text-error">Failed loading institutions. Please try again.</div>';
    }
}

function renderInstitutions(list) {
    const container = document.getElementById('institutions_list_container');
    if (!list || list.length === 0) {
        container.innerHTML = '<div class="py-6 text-center text-xs text-base-content/50">No banking institutions found for the selected country.</div>';
        return;
    }

    let html = '';
    list.forEach(inst => {
        const isChecked = wizardState.selectedInstitutionIDs.has(inst.id);
        html += '<label class="flex items-center justify-between p-3 rounded-lg bg-base-100 hover:bg-base-200/70 border border-base-300 cursor-pointer transition-colors">' +
            '<div class="flex items-center gap-3">' +
                '<input type="checkbox" value="' + inst.id + '" ' + (isChecked ? 'checked' : '') + ' onchange="toggleInstitutionSelection(\'' + inst.id + '\', this.checked)" class="checkbox checkbox-primary checkbox-sm" />' +
                '<div>' +
                    '<div class="font-bold text-xs text-base-content">' + inst.name + '</div>' +
                    '<div class="text-[10px] text-base-content/60 font-mono">BIC: ' + (inst.bic || 'N/A') + '</div>' +
                '</div>' +
            '</div>' +
            '<span class="badge badge-soft badge-neutral badge-xs">' + inst.id + '</span>' +
        '</label>';
    });
    container.innerHTML = html;
}

function filterInstitutions() {
    const q = document.getElementById('bank_search_input').value.toLowerCase().trim();
    if (!q) {
        renderInstitutions(wizardState.institutions);
        return;
    }
    const filtered = wizardState.institutions.filter(i => (i.name && i.name.toLowerCase().includes(q)) || (i.bic && i.bic.toLowerCase().includes(q)) || (i.id && i.id.toLowerCase().includes(q)));
    renderInstitutions(filtered);
}

function toggleInstitutionSelection(id, checked) {
    if (checked) wizardState.selectedInstitutionIDs.add(id);
    else wizardState.selectedInstitutionIDs.delete(id);
    updateSelectedCount();
}

function updateSelectedCount() {
    const count = wizardState.selectedInstitutionIDs.size;
    document.getElementById('selected_banks_count').innerText = count + ' selected';
    document.getElementById('btn_selected_count').innerText = count;
    document.getElementById('connect_banks_btn').disabled = count === 0;
}

async function startConnectionQueue() {
    const selected = Array.from(wizardState.selectedInstitutionIDs);
    if (selected.length === 0) return;

    goToStep(4);
    const listContainer = document.getElementById('queue_items_list');
    listContainer.innerHTML = '';

    selected.forEach(instId => {
        const row = document.createElement('div');
        row.id = 'queue_item_' + instId;
        row.className = 'flex items-center justify-between p-2.5 rounded border border-base-300 bg-base-100 text-xs';
        row.innerHTML = '<span class="font-bold font-mono">' + instId + '</span><span class="badge badge-neutral badge-xs">Queued</span>';
        listContainer.appendChild(row);
    });

    // Process sequentially one by one
    for (let i = 0; i < selected.length; i++) {
        const instId = selected[i];
        const itemEl = document.getElementById('queue_item_' + instId);
        if (itemEl) itemEl.querySelector('.badge').className = 'badge badge-warning badge-xs animate-pulse';
        if (itemEl) itemEl.querySelector('.badge').innerText = 'Connecting...';

        try {
            const res = await fetch('/budgets/' + window.ACTIVE_BUDGET_ID + '/accounts/connect-queue', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-Requested-With': 'XMLHttpRequest',
                    'X-CSRF-Token': window.CSRF_TOKEN
                },
                body: JSON.stringify({
                    csrf_token: window.CSRF_TOKEN,
                    provider: wizardState.provider,
                    country: wizardState.country,
                    institution_ids: [instId]
                })
            });
            const data = await res.json();
            const r = (data.results && data.results[0]) || {};

            if (itemEl) {
                if (r.status === 'awaiting_authorization' && r.auth_url) {
                    itemEl.querySelector('.badge').className = 'badge badge-info badge-xs';
                    itemEl.querySelector('.badge').innerHTML = '<a href="' + r.auth_url + '" target="_blank" class="underline font-bold">Authorize Bank ➔</a>';
                } else if (r.status === 'connected') {
                    itemEl.querySelector('.badge').className = 'badge badge-success badge-xs font-semibold';
                    itemEl.querySelector('.badge').innerText = 'Connected (' + (r.accounts_count || 1) + ' accounts)';
                } else {
                    itemEl.querySelector('.badge').className = 'badge badge-error badge-xs';
                    itemEl.querySelector('.badge').innerText = r.status || 'Failed';
                }
            }
        } catch (err) {
            if (itemEl) {
                itemEl.querySelector('.badge').className = 'badge badge-error badge-xs';
                itemEl.querySelector('.badge').innerText = 'Network error';
            }
        }
    }

    document.getElementById('queue_spinner').classList.add('hidden');
    document.getElementById('queue_title').innerText = 'All Bank Connections Processed';
    document.getElementById('queue_done_btn').classList.remove('hidden');
}

async function reconnectAccount(acctId, acctName) {
    if (!confirm('Reconnect bank feed for "' + acctName + '"? This will renew your open banking authorization.')) return;
    try {
        const res = await fetch('/budgets/' + window.ACTIVE_BUDGET_ID + '/accounts/' + acctId + '/reconnect', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'X-Requested-With': 'XMLHttpRequest',
                'X-CSRF-Token': window.CSRF_TOKEN
            },
            body: JSON.stringify({ csrf_token: window.CSRF_TOKEN })
        });
        const data = await res.json();
        if (data.auth_url) {
            window.location.href = data.auth_url;
        } else if (data.status === 'ACTIVE' || data.status === 'connected') {
            window.location.reload();
        } else {
            alert('Reconnect response: ' + (data.message || data.status || 'OK'));
            window.location.reload();
        }
    } catch (err) {
        alert('Failed to initiate reconnect: ' + err.message);
    }
}

function switchNewFaviconTab(t) {
    document.getElementById('new_favicon_type').value = t;
    if (t === 'EMOJI') {
        document.getElementById('tab_new_emoji').classList.add('tab-active');
        document.getElementById('tab_new_url').classList.remove('tab-active');
        document.getElementById('panel_new_emoji').classList.remove('hidden');
        document.getElementById('panel_new_url').classList.add('hidden');
        const v = document.getElementById('new_emoji_custom_input').value || '💵';
        setNewEmoji(v);
    } else {
        document.getElementById('tab_new_url').classList.add('tab-active');
        document.getElementById('tab_new_emoji').classList.remove('tab-active');
        document.getElementById('panel_new_url').classList.remove('hidden');
        document.getElementById('panel_new_emoji').classList.add('hidden');
        setNewUrlManual(document.getElementById('new_url_input').value);
    }
}

function setNewEmoji(em) {
    const val = em ? em.trim() : '💵';
    document.getElementById('new_favicon_type').value = 'EMOJI';
    document.getElementById('new_favicon_value').value = val;
    document.getElementById('new_icon_preview').innerHTML = val || '💵';
}

function setNewUrlManual(u) {
    const val = u ? u.trim() : '';
    document.getElementById('new_favicon_type').value = 'URL';
    document.getElementById('new_favicon_value').value = val;
    if (val) {
        document.getElementById('new_icon_preview').innerHTML = '<img src="' + val + '" class="w-full h-full object-contain p-0.5" onerror="this.parentElement.innerText=\'🌐\'" />';
    } else {
        document.getElementById('new_icon_preview').innerHTML = '🌐';
    }
}

async function fetchFaviconForNewAccount() {
    const input = document.getElementById('new_url_input');
    const status = document.getElementById('new_url_status');
    const btn = document.getElementById('new_url_fetch_btn');
    const targetUrl = input.value.trim();
    if (!targetUrl) {
        status.innerText = 'Please enter a valid website URL first.';
        status.className = 'text-[11px] text-error font-medium';
        return;
    }

    btn.disabled = true;
    btn.innerHTML = '<span class="loading loading-spinner loading-xs"></span>';
    status.innerText = 'Fetching and parsing site for highest quality favicon...';
    status.className = 'text-[11px] text-primary font-medium animate-pulse';

    try {
        const res = await fetch('/budgets/' + window.ACTIVE_BUDGET_ID + '/accounts/extract-favicon?url=' + encodeURIComponent(targetUrl));
        const data = await res.json();
        if (res.ok && data.best_favicon) {
            input.value = data.best_favicon;
            setNewUrlManual(data.best_favicon);
            status.innerText = '✓ Resolved icon: ' + data.best_favicon;
            status.className = 'text-[11px] text-success font-medium truncate';
        } else {
            status.innerText = 'Could not find high quality favicon, default icon assigned.';
            status.className = 'text-[11px] text-warning font-medium';
        }
    } catch (err) {
        status.innerText = 'Discovery network error. You can still paste direct icon image URL.';
        status.className = 'text-[11px] text-error font-medium';
    } finally {
        btn.disabled = false;
        btn.innerHTML = 'Fetch';
    }
}

function switchEditFaviconTab(t) {
    document.getElementById('edit_favicon_type').value = t;
    if (t === 'EMOJI') {
        document.getElementById('tab_edit_emoji').classList.add('tab-active');
        document.getElementById('tab_edit_url').classList.remove('tab-active');
        document.getElementById('panel_edit_emoji').classList.remove('hidden');
        document.getElementById('panel_edit_url').classList.add('hidden');
        const v = document.getElementById('edit_emoji_custom_input').value || '💳';
        setEditEmoji(v);
    } else {
        document.getElementById('tab_edit_url').classList.add('tab-active');
        document.getElementById('tab_edit_emoji').classList.remove('tab-active');
        document.getElementById('panel_edit_url').classList.remove('hidden');
        document.getElementById('panel_edit_emoji').classList.add('hidden');
        setEditUrlManual(document.getElementById('edit_url_input').value);
    }
}

function setEditEmoji(em) {
    const val = em ? em.trim() : '💳';
    document.getElementById('edit_favicon_type').value = 'EMOJI';
    document.getElementById('edit_favicon_value').value = val;
    document.getElementById('edit_icon_preview').innerHTML = val || '💳';
}

function setEditUrlManual(u) {
    const val = u ? u.trim() : '';
    document.getElementById('edit_favicon_type').value = 'URL';
    document.getElementById('edit_favicon_value').value = val;
    if (val) {
        document.getElementById('edit_icon_preview').innerHTML = '<img src="' + val + '" class="w-full h-full object-contain p-0.5" onerror="this.parentElement.innerText=\'🌐\'" />';
    } else {
        document.getElementById('edit_icon_preview').innerHTML = '🌐';
    }
}

async function fetchFaviconForEditAccount() {
    const input = document.getElementById('edit_url_input');
    const status = document.getElementById('edit_url_status');
    const btn = document.getElementById('edit_url_fetch_btn');
    const targetUrl = input.value.trim();
    if (!targetUrl) {
        status.innerText = 'Please enter a valid website URL first.';
        status.className = 'text-[11px] text-error font-medium';
        return;
    }

    btn.disabled = true;
    btn.innerHTML = '<span class="loading loading-spinner loading-xs"></span>';
    status.innerText = 'Fetching and parsing site for highest quality favicon...';
    status.className = 'text-[11px] text-primary font-medium animate-pulse';

    try {
        const res = await fetch('/budgets/' + window.ACTIVE_BUDGET_ID + '/accounts/extract-favicon?url=' + encodeURIComponent(targetUrl));
        const data = await res.json();
        if (res.ok && data.best_favicon) {
            input.value = data.best_favicon;
            setEditUrlManual(data.best_favicon);
            status.innerText = '✓ Resolved icon: ' + data.best_favicon;
            status.className = 'text-[11px] text-success font-medium truncate';
        } else {
            status.innerText = 'Could not find high quality favicon, default icon assigned.';
            status.className = 'text-[11px] text-warning font-medium';
        }
    } catch (err) {
        status.innerText = 'Discovery network error. You can still paste direct icon image URL.';
        status.className = 'text-[11px] text-error font-medium';
    } finally {
        btn.disabled = false;
        btn.innerHTML = 'Fetch';
    }
}

function openEditModal(id, name, curr, type, favType, favVal) {
    document.getElementById('edit_name').value = name;
    document.getElementById('edit_account_form').action = '/budgets/' + window.ACTIVE_BUDGET_ID + '/accounts/' + id + '/update';
    if (favType === 'URL') {
        document.getElementById('edit_url_input').value = favVal || '';
        switchEditFaviconTab('URL');
        setEditUrlManual(favVal);
    } else {
        const emoji = favVal || '💳';
        document.getElementById('edit_emoji_custom_input').value = emoji;
        switchEditFaviconTab('EMOJI');
        setEditEmoji(emoji);
    }
    edit_account_modal.showModal();
}

window.wizardState = wizardState;
window.resetBankWizard = resetBankWizard;
window.goToStep = goToStep;
window.selectProvider = selectProvider;
window.selectCountry = selectCountry;
window.loadInstitutions = loadInstitutions;
window.renderInstitutions = renderInstitutions;
window.filterInstitutions = filterInstitutions;
window.toggleInstitutionSelection = toggleInstitutionSelection;
window.updateSelectedCount = updateSelectedCount;
window.startConnectionQueue = startConnectionQueue;
window.reconnectAccount = reconnectAccount;
window.switchNewFaviconTab = switchNewFaviconTab;
window.setNewEmoji = setNewEmoji;
window.setNewUrlManual = setNewUrlManual;
window.fetchFaviconForNewAccount = fetchFaviconForNewAccount;
window.switchEditFaviconTab = switchEditFaviconTab;
window.setEditEmoji = setEditEmoji;
window.setEditUrlManual = setEditUrlManual;
window.fetchFaviconForEditAccount = fetchFaviconForEditAccount;
window.openEditModal = openEditModal;
