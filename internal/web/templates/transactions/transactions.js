(function() {
    let ACTIVE_BUDGET_ID = '';
    let ALL_TARGETS = [];
    const pageDataEl = document.getElementById('tx-page-data');
    if (pageDataEl) {
        try {
            const parsed = JSON.parse(pageDataEl.textContent || '{}');
            ACTIVE_BUDGET_ID = parsed.activeBudgetId || '';
            ALL_TARGETS = parsed.allTargets || [];
        } catch (e) {
            console.error('[Transactions] Failed parsing tx-page-data:', e);
        }
    }

    const BUCKET_OPTIONS_HTML = document.getElementById('bucket_options_tpl') ? document.getElementById('bucket_options_tpl').innerHTML : '';

    window.focusQuickAdd = function() {
        const el = document.getElementById('qa_amount');
        if (el) {
            el.scrollIntoView({ behavior: 'smooth', block: 'center' });
            el.focus();
        }
    };

    window.setQuickAddType = function(type, btn) {
        document.getElementById('qa_tx_type').value = type;
        document.querySelectorAll('.qa-type-btn').forEach(b => {
            b.classList.remove('btn-active', 'btn-error', 'btn-success', 'btn-info');
            b.classList.add('btn-ghost');
        });
        btn.classList.remove('btn-ghost');
        btn.classList.add('btn-active');

        const payeeInput = document.getElementById('qa_payee');
        const destAcctSelect = document.getElementById('qa_dest_account_id');
        const bucketWrapper = document.getElementById('qa_bucket_wrapper');
        const bucketSelect = document.getElementById('qa_bucket_id');

        if (type === 'TRANSFER') {
            btn.classList.add('btn-info');
            payeeInput.classList.add('hidden');
            destAcctSelect.classList.remove('hidden');
            bucketSelect.disabled = true;
            bucketSelect.value = '';
            bucketWrapper.classList.add('opacity-40');
        } else if (type === 'INCOME') {
            btn.classList.add('btn-success');
            payeeInput.classList.remove('hidden');
            payeeInput.placeholder = 'Payer / Source (e.g. Employer Inc.)';
            destAcctSelect.classList.add('hidden');
            bucketSelect.disabled = false;
            bucketWrapper.classList.remove('opacity-40');
        } else {
            // EXPENSE
            btn.classList.add('btn-error');
            payeeInput.classList.remove('hidden');
            payeeInput.placeholder = 'Payee / Merchant (e.g. Żabka)';
            destAcctSelect.classList.add('hidden');
            bucketSelect.disabled = false;
            bucketWrapper.classList.remove('opacity-40');
        }
    };

    // Global hotkeys (N or A for quick add)
    document.addEventListener('keydown', function(e) {
        const tag = document.activeElement ? document.activeElement.tagName.toLowerCase() : '';
        if (tag === 'input' || tag === 'select' || tag === 'textarea') {
            return;
        }
        if (e.key === 'n' || e.key === 'a' || e.key === 'N' || e.key === 'A') {
            e.preventDefault();
            window.focusQuickAdd();
        }
    });

    // Quick add form Shift+Enter handler
    const qaForm = document.getElementById('quick_add_form');
    if (qaForm) {
        qaForm.addEventListener('keydown', function(e) {
            if (e.key === 'Enter' && e.shiftKey) {
                e.preventDefault();
                qaForm.submit();
            }
        });
    }

    window.updateFloatingActionBar = function() {
        const checkedBoxes = Array.from(document.querySelectorAll('.raw-select-cb:checked'));
        const bar = document.getElementById('floating-action-bar');
        const countLabel = document.getElementById('fab-count');
        const netDisplay = document.getElementById('fab-net-display');
        const splitBtn = document.getElementById('fab-split-btn');
        const deleteInputs = document.getElementById('fab-delete-inputs');
        const approveInputs = document.getElementById('fab-approve-inputs');

        if (!bar) return;

        if (checkedBoxes.length === 0) {
            bar.classList.add('hidden');
            return;
        }

        bar.classList.remove('hidden');
        countLabel.innerText = checkedBoxes.length;

        let netMinor = 0;
        let currency = 'PLN';
        if (deleteInputs) deleteInputs.innerHTML = '';
        if (approveInputs) approveInputs.innerHTML = '';

        checkedBoxes.forEach(cb => {
            const amt = parseInt(cb.dataset.amount || '0', 10);
            currency = cb.dataset.currency || currency;
            netMinor += amt;

            if (deleteInputs) {
                const inp = document.createElement('input');
                inp.type = 'hidden';
                inp.name = 'raw_ids';
                inp.value = cb.value;
                deleteInputs.appendChild(inp);
            }

            if (approveInputs) {
                const inp = document.createElement('input');
                inp.type = 'hidden';
                inp.name = 'raw_ids';
                inp.value = cb.value;
                approveInputs.appendChild(inp);
            }
        });

        const netMajor = Math.abs(netMinor) / 100.0;
        const sign = netMinor < 0 ? '-' : '+';
        netDisplay.innerText = 'Net: ' + sign + netMajor.toFixed(2) + ' ' + currency;

        if (splitBtn) {
            splitBtn.disabled = checkedBoxes.length !== 1;
        }
    };

    window.clearAllSelections = function() {
        document.querySelectorAll('.raw-select-cb').forEach(cb => cb.checked = false);
        window.updateFloatingActionBar();
    };

    window.toggleAllRawCbs = function(master) {
        document.querySelectorAll('.raw-select-cb').forEach(b => b.checked = master.checked);
        window.updateFloatingActionBar();
    };

    window.updateLedgerFloatingActionBar = function() {
        const checkedBoxes = Array.from(document.querySelectorAll('.budget-tx-select-cb:checked'));
        const bar = document.getElementById('ledger-floating-action-bar');
        const countLabel = document.getElementById('ledger-fab-count');
        const totalDisplay = document.getElementById('ledger-fab-total-display');

        if (!bar) return;

        if (checkedBoxes.length === 0) {
            bar.classList.add('hidden');
            const master = document.getElementById('budget-master-cb');
            if (master) master.checked = false;
            return;
        }

        bar.classList.remove('hidden');
        countLabel.innerText = checkedBoxes.length;

        let totalMinor = 0;
        let currency = 'PLN';
        checkedBoxes.forEach(cb => {
            const amt = parseInt(cb.dataset.amount || '0', 10);
            currency = cb.dataset.currency || currency;
            totalMinor += Math.abs(amt);
        });

        const totalMajor = totalMinor / 100.0;
        totalDisplay.innerText = 'Total: ' + totalMajor.toFixed(2) + ' ' + currency;
    };

    window.clearAllLedgerSelections = function() {
        document.querySelectorAll('.budget-tx-select-cb').forEach(cb => cb.checked = false);
        const master = document.getElementById('budget-master-cb');
        if (master) master.checked = false;
        window.updateLedgerFloatingActionBar();
    };

    window.toggleAllBudgetCbs = function(master) {
        document.querySelectorAll('.budget-tx-select-cb').forEach(b => b.checked = master.checked);
        window.updateLedgerFloatingActionBar();
    };

    window.openBatchDeleteConfirmModal = function() {
        const checkedBoxes = Array.from(document.querySelectorAll('.budget-tx-select-cb:checked'));
        if (checkedBoxes.length === 0) return;

        const container = document.getElementById('batch_delete_tx_inputs');
        container.innerHTML = '';
        document.getElementById('batch_delete_modal_count').innerText = checkedBoxes.length;

        checkedBoxes.forEach(cb => {
            const inp = document.createElement('input');
            inp.type = 'hidden';
            inp.name = 'transaction_ids';
            inp.value = cb.value;
            container.appendChild(inp);
        });

        batch_delete_modal.showModal();
    };

    window.openBatchAutomationsModal = function() {
        const checkedBoxes = Array.from(document.querySelectorAll('.budget-tx-select-cb:checked'));
        if (checkedBoxes.length === 0) return;

        document.getElementById('batch_auto_count').innerText = checkedBoxes.length;
        document.getElementById('batch_auto_preview_area').innerHTML = '<div class="flex justify-center p-4"><span class="loading loading-spinner loading-sm"></span></div>';
        batch_automations_modal.showModal();

        const csrf = document.querySelector('input[name="csrf_token"]')?.value || '';
        const txIDs = checkedBoxes.map(cb => cb.value).join(',');
        const formData = new FormData();
        formData.append('tx_ids', txIDs);

        fetch('/budgets/' + ACTIVE_BUDGET_ID + '/automations/batch-dry-run', {
            method: 'POST',
            headers: { 'X-CSRF-Token': csrf },
            body: formData
        })
        .then(r => r.text())
        .then(html => {
            document.getElementById('batch_auto_preview_area').innerHTML = html;
        })
        .catch(err => {
            document.getElementById('batch_auto_preview_area').innerHTML = '<div class="alert alert-error text-xs">Failed loading preview: ' + err + '</div>';
        });
    };

    window.applyBatchAutomations = function() {
        const checkedBoxes = Array.from(document.querySelectorAll('.budget-tx-select-cb:checked'));
        if (checkedBoxes.length === 0) return;

        const btn = document.getElementById('batch_auto_apply_btn');
        btn.disabled = true;
        btn.innerText = 'Applying...';

        const csrf = document.querySelector('input[name="csrf_token"]')?.value || '';
        const txIDs = checkedBoxes.map(cb => cb.value).join(',');
        const formData = new FormData();
        formData.append('tx_ids', txIDs);

        fetch('/budgets/' + ACTIVE_BUDGET_ID + '/automations/batch-run', {
            method: 'POST',
            headers: { 'X-CSRF-Token': csrf },
            body: formData
        })
        .then(r => {
            if (r.ok) {
                window.location.reload();
            } else {
                return r.text().then(t => alert('Error: ' + t));
            }
        })
        .catch(err => alert('Failed: ' + err))
        .finally(() => {
            btn.disabled = false;
            btn.innerText = '⚡ Apply Changes';
        });
    };

    window.openMergeModal = function() {
        const checkedBoxes = Array.from(document.querySelectorAll('.raw-select-cb:checked'));
        if (checkedBoxes.length === 0) return;

        const container = document.getElementById('merge_raw_ids_container');
        container.innerHTML = '';

        let netMinor = 0;
        let currency = 'PLN';
        checkedBoxes.forEach(cb => {
            const amt = parseInt(cb.dataset.amount || '0', 10);
            currency = cb.dataset.currency || currency;
            netMinor += amt;

            const inp = document.createElement('input');
            inp.type = 'hidden';
            inp.name = 'raw_ids';
            inp.value = cb.value;
            container.appendChild(inp);
        });

        const netMajor = Math.abs(netMinor) / 100.0;
        const sign = netMinor < 0 ? '-' : '+';
        document.getElementById('merge_count_label').innerText = checkedBoxes.length + ' transactions selected';
        document.getElementById('merge_net_amount_label').innerText = sign + netMajor.toFixed(2) + ' ' + currency;

        merge_modal.showModal();
    };

    let splitTotalAmountMinor = 0;
    let splitCurrency = 'PLN';

    window.openSplitModal = function() {
        const checked = document.querySelector('.raw-select-cb:checked');
        if (!checked) return;

        const rawId = checked.value;
        const amountMinor = Math.abs(parseInt(checked.dataset.amount || '0', 10));
        splitTotalAmountMinor = amountMinor;
        splitCurrency = checked.dataset.currency || 'PLN';
        const payee = checked.dataset.payee || '';

        document.getElementById('split_raw_id').value = rawId;
        document.getElementById('split_payee_label').innerText = payee;
        document.getElementById('split_total_label').innerText = (amountMinor / 100.0).toFixed(2) + ' ' + splitCurrency;

        const container = document.getElementById('split_rows_container');
        container.innerHTML = '';

        // Add initial 2 split lines
        window.addSplitRow((amountMinor / 200.0).toFixed(2));
        window.addSplitRow((amountMinor / 200.0).toFixed(2));
        window.recalcSplit();

        split_modal.showModal();
    };

    window.addSplitRow = function(defaultAmount) {
        const container = document.getElementById('split_rows_container');
        const row = document.createElement('div');
        row.className = 'grid grid-cols-12 gap-2 items-center split-line-row';
        row.innerHTML =
            '<div class="col-span-5">' +
                '<select name="split_bucket_id" class="select select-sm select-bordered w-full text-xs" required>' +
                    BUCKET_OPTIONS_HTML +
                '</select>' +
            '</div>' +
            '<div class="col-span-4">' +
                '<input type="text" name="split_note" placeholder="Item note" class="input input-sm input-bordered w-full text-xs" />' +
            '</div>' +
            '<div class="col-span-2">' +
                '<input type="number" step="0.01" name="split_amount" value="' + (defaultAmount || '') + '" required placeholder="0.00" class="input input-sm input-bordered w-full font-mono font-bold text-xs split-amt-input" oninput="recalcSplit()" />' +
            '</div>' +
            '<div class="col-span-1 text-right">' +
                '<button type="button" class="btn btn-ghost btn-xs btn-circle text-error" onclick="removeSplitRow(this)">✕</button>' +
            '</div>';
        container.appendChild(row);
        window.recalcSplit();
    };

    window.removeSplitRow = function(btn) {
        const row = btn.closest('.split-line-row');
        if (row && document.querySelectorAll('.split-line-row').length > 1) {
            row.remove();
            window.recalcSplit();
        }
    };

    window.recalcSplit = function() {
        const inputs = document.querySelectorAll('.split-amt-input');
        let sumMinor = 0;
        inputs.forEach(inp => {
            const f = parseFloat(inp.value || '0');
            if (!isNaN(f)) {
                sumMinor += Math.round(f * 100);
            }
        });

        const remainingMinor = splitTotalAmountMinor - sumMinor;
        const remainingLabel = document.getElementById('split_remaining_label');
        const submitBtn = document.getElementById('split_submit_btn');
        const valMsg = document.getElementById('split_validation_msg');

        remainingLabel.innerText = (remainingMinor / 100.0).toFixed(2) + ' ' + splitCurrency;

        if (remainingMinor === 0) {
            remainingLabel.className = 'font-mono font-bold text-success text-base';
            valMsg.innerText = '';
            submitBtn.disabled = false;
        } else {
            remainingLabel.className = 'font-mono font-bold text-error text-base';
            valMsg.innerText = remainingMinor > 0 ? 'Remaining amount must be 0.00' : 'Split amounts exceed total receipt!';
            submitBtn.disabled = true;
        }
    };

    function populateTargetSelect(bucketId, selectEl, selectedTargetId) {
        selectEl.innerHTML = '<option value="">🎯 Unassigned to specific target</option>';
        if (!bucketId) return;
        const filtered = ALL_TARGETS.filter(t => t.bucket_id === bucketId);
        filtered.forEach(t => {
            const opt = document.createElement('option');
            opt.value = t.id;
            opt.innerText = '🎯 ' + t.name + ' (' + t.currency + ')';
            if (selectedTargetId && t.id === selectedTargetId) {
                opt.selected = true;
            }
            selectEl.appendChild(opt);
        });
    }

    window.syncQaTargets = function() {
        const bId = document.getElementById('qa_bucket_id').value;
        const targetSelect = document.getElementById('qa_target_id');
        populateTargetSelect(bId, targetSelect, '');
    };

    window.syncEditTxTargets = function() {
        const bId = document.getElementById('edit_tx_bucket').value;
        const targetSelect = document.getElementById('edit_tx_target');
        populateTargetSelect(bId, targetSelect, '');
    };

    window.syncResolveTargets = function() {
        const bId = document.getElementById('resolve_bucket').value;
        const targetSelect = document.getElementById('resolve_target');
        populateTargetSelect(bId, targetSelect, '');
    };

    window.openEditTxModal = function(id, amount, dateStr, notes, bucketId, targetId, accountId) {
        document.getElementById('edit_tx_amount').value = amount;
        document.getElementById('edit_tx_date').value = dateStr;
        document.getElementById('edit_tx_notes').value = notes;
        if (bucketId) {
            document.getElementById('edit_tx_bucket').value = bucketId;
        } else {
            document.getElementById('edit_tx_bucket').value = '';
        }
        populateTargetSelect(bucketId, document.getElementById('edit_tx_target'), targetId);
        if (accountId) {
            document.getElementById('edit_tx_account').value = accountId;
        }
        document.getElementById('edit_tx_form').action = '/budgets/' + ACTIVE_BUDGET_ID + '/transactions/' + id + '/update';
        edit_tx_modal.showModal();
    };

    window.openResolveRawModal = function(rawId, acctId, amount, currency, rawPayee, suggestedBucketId) {
        document.getElementById('resolve_raw_id').value = rawId;
        document.getElementById('resolve_payee_display').innerText = rawPayee;
        document.getElementById('resolve_amount_display').innerText = amount + ' ' + currency;
        document.getElementById('resolve_notes').value = rawPayee;
        document.getElementById('resolve_party_name').value = rawPayee.split(' ')[0] || rawPayee;
        if (suggestedBucketId) {
            document.getElementById('resolve_bucket').value = suggestedBucketId;
        } else {
            document.getElementById('resolve_bucket').value = '';
        }
        window.syncResolveTargets();
        document.getElementById('resolve_raw_form').action = '/budgets/' + ACTIVE_BUDGET_ID + '/transactions/raw/' + rawId + '/resolve';
        resolve_raw_modal.showModal();
    };

    window.toggleAllAccounts = function(btn) {
        const boxes = document.querySelectorAll('input[name="account_ids"]');
        const allChecked = Array.from(boxes).every(b => b.checked);
        boxes.forEach(b => b.checked = !allChecked);
    };

    // Initialize SSE for bank sync notifications
    if (window.EventSource && ACTIVE_BUDGET_ID) {
        const sseUrl = '/budgets/' + ACTIVE_BUDGET_ID + '/transactions/sync/events';
        console.log('[SSE] Initializing EventSource connection to:', sseUrl);
        const sse = new EventSource(sseUrl);
        sse.onopen = () => console.log('[SSE] Successfully connected to bank sync events');
        sse.onerror = (err) => console.warn('[SSE] EventSource error or reconnection:', err);

        sse.addEventListener('sync-progress', (e) => {
            console.log('[SSE] sync-progress received:', e.data);
            const data = JSON.parse(e.data || '{}');
            const alerts = document.getElementById('sync-alerts');
            if (alerts) {
                alerts.innerHTML = '<div role="alert" class="alert alert-info shadow-lg animate-fade-in flex items-center gap-2">' +
                    '<span class="loading loading-spinner loading-sm text-info"></span>' +
                    '<div>' +
                        '<h4 class="font-bold">Sync in Progress (' + data.index + '/' + data.total + ')</h4>' +
                        '<p class="text-xs">Synchronizing bank accounts in background...</p>' +
                    '</div>' +
                '</div>';
            }
        });

        sse.addEventListener('sync-complete', (e) => {
            console.log('[SSE] sync-complete received:', e.data);
            const data = JSON.parse(e.data || '{}');
            const alerts = document.getElementById('sync-alerts');
            if (alerts) {
                alerts.innerHTML = '<div role="alert" class="alert alert-success shadow-lg animate-fade-in flex justify-between items-center">' +
                    '<div class="flex items-center gap-2">' +
                        '<span>✅</span>' +
                        '<div>' +
                            '<h4 class="font-bold">Bank Sync Complete</h4>' +
                            '<p class="text-xs">Successfully synced ' + (data.imported_count || 0) + ' transaction(s) across ' + (data.accounts_count || 1) + ' account(s).</p>' +
                        '</div>' +
                    '</div>' +
                    '<div class="flex items-center gap-2">' +
                        '<button class="btn btn-primary btn-xs font-bold" onclick="window.location.reload()">Refresh Page</button>' +
                        '<button class="btn btn-ghost btn-xs btn-circle" onclick="document.getElementById(\'sync-alerts\').innerHTML=\'\'">✕</button>' +
                    '</div>' +
                '</div>';
            }
        });

        sse.addEventListener('sync-error', (e) => {
            console.log('[SSE] sync-error received:', e.data);
            const data = JSON.parse(e.data || '{}');
            const alerts = document.getElementById('sync-alerts');
            if (alerts) {
                alerts.innerHTML = '<div role="alert" class="alert alert-error shadow-lg animate-fade-in flex justify-between items-center">' +
                    '<div class="flex items-center gap-2">' +
                        '<span>❌</span>' +
                        '<div>' +
                            '<h4 class="font-bold">Bank Sync Failed</h4>' +
                            '<p class="text-xs">' + (data.error || 'An error occurred during synchronization.') + '</p>' +
                        '</div>' +
                    '</div>' +
                    '<button class="btn btn-ghost btn-xs btn-circle text-error-content" onclick="document.getElementById(\'sync-alerts\').innerHTML=\'\'">✕</button>' +
                '</div>';
            }
        });
    }
})();
