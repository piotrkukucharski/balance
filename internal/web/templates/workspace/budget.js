const currentBudgetID = window.currentBudgetID || '';
const csrfTokenVal = window.csrfTokenVal || '';
const currentCurrency = window.currentCurrency || 'PLN';
const currentHorizon = window.currentHorizon || 'monthly';
const currentFilter = window.currentFilter || 'all';
const bucketsData = window.bucketsData || [];

window.addEventListener('DOMContentLoaded', () => {
    const toast = document.getElementById('budget-toast');
    if (toast && !toast.classList.contains('hidden')) {
        setTimeout(() => {
            toast.style.opacity = '0';
            setTimeout(() => toast.classList.add('hidden'), 300);
        }, 4000);
    }

    if (window.location.hash) {
        const el = document.querySelector(window.location.hash);
        if (el) {
            el.scrollIntoView({ behavior: 'smooth', block: 'center' });
            el.classList.add('bg-primary/10');
            setTimeout(() => el.classList.remove('bg-primary/10'), 2500);
        }
    }

    document.querySelectorAll('.target-subrows').forEach(row => {
        const bID = row.getAttribute('data-bucket-id');
        const stored = localStorage.getItem('balance_bucket_expanded_' + currentBudgetID + '_' + bID);
        if (stored !== null) {
            const isExp = stored === 'true';
            row.classList.toggle('hidden', !isExp);
            const icon = document.getElementById('bucket-toggle-icon-' + bID);
            if (icon) icon.innerText = isExp ? '▼' : '▶';
            const btn = document.querySelector('.target-toggle-btn[aria-controls="targets-' + bID + '"]');
            if (btn) btn.setAttribute('aria-expanded', isExp ? 'true' : 'false');
        }
    });

    initBucketDragAndDrop();
});

function showAppToast(message, isSuccess = true) {
    let toast = document.getElementById('budget-toast');
    if (!toast) return;
    const alertBox = toast.querySelector('.alert');
    const msgEl = document.getElementById('budget-toast-msg');
    if (msgEl) msgEl.innerText = message;
    if (alertBox) {
        alertBox.className = 'alert text-xs shadow-lg flex items-center gap-2 ' + (isSuccess ? 'alert-success' : 'alert-error');
    }
    toast.classList.remove('hidden');
    toast.style.opacity = '1';
    clearTimeout(window.__toastTimeout);
    window.__toastTimeout = setTimeout(() => {
        toast.style.opacity = '0';
        setTimeout(() => toast.classList.add('hidden'), 300);
    }, 4000);
}

function initBucketDragAndDrop() {
    let draggedBucket = null;

    // Desktop Drag Handles
    document.querySelectorAll('.bucket-drag-handle').forEach(handle => {
        handle.addEventListener('dragstart', (e) => {
            const row = handle.closest('.bucket-row');
            if (!row) return;
            draggedBucket = {
                type: 'desktop',
                id: row.getAttribute('data-bucket-id'),
                name: row.getAttribute('data-bucket-name'),
                groupId: row.getAttribute('data-group-id'),
                groupName: row.getAttribute('data-group-name'),
                rowEl: row,
                subrowEl: document.getElementById('targets-' + row.getAttribute('data-bucket-id'))
            };
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', draggedBucket.id);
            row.classList.add('opacity-40');
        });

        handle.addEventListener('dragend', () => {
            if (draggedBucket && draggedBucket.rowEl) {
                draggedBucket.rowEl.classList.remove('opacity-40');
            }
            document.querySelectorAll('.group-drop-zone').forEach(z => {
                z.classList.remove('bg-primary/20', 'border-primary');
            });
            draggedBucket = null;
        });
    });

    // Desktop Drop Zones (Category Group Headers)
    document.querySelectorAll('.group-drop-zone').forEach(zone => {
        zone.addEventListener('dragover', (e) => {
            if (!draggedBucket || draggedBucket.type !== 'desktop') return;
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            zone.classList.add('bg-primary/20', 'border-primary');
        });

        zone.addEventListener('dragleave', (e) => {
            if (!zone.contains(e.relatedTarget)) {
                zone.classList.remove('bg-primary/20', 'border-primary');
            }
        });

        zone.addEventListener('drop', (e) => {
            e.preventDefault();
            zone.classList.remove('bg-primary/20', 'border-primary');
            if (!draggedBucket || draggedBucket.type !== 'desktop') return;

            const targetGroupId = zone.getAttribute('data-group-id');
            const targetGroupName = zone.getAttribute('data-group-name');
            if (targetGroupId === draggedBucket.groupId) return;

            moveBucketToGroupDOM(draggedBucket, targetGroupId, targetGroupName);
        });
    });

    // Mobile Drag Handles
    document.querySelectorAll('.mobile-bucket-drag-handle').forEach(handle => {
        handle.addEventListener('dragstart', (e) => {
            const card = handle.closest('.mobile-bucket-card');
            if (!card) return;
            draggedBucket = {
                type: 'mobile',
                id: card.getAttribute('data-bucket-id'),
                name: card.getAttribute('data-bucket-name'),
                groupId: card.getAttribute('data-group-id'),
                groupName: card.getAttribute('data-group-name'),
                cardEl: card
            };
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', draggedBucket.id);
            card.classList.add('opacity-40');
        });

        handle.addEventListener('dragend', () => {
            if (draggedBucket && draggedBucket.cardEl) {
                draggedBucket.cardEl.classList.remove('opacity-40');
            }
            document.querySelectorAll('.mobile-group-drop-zone').forEach(z => {
                z.classList.remove('bg-primary/20', 'border-primary');
            });
            draggedBucket = null;
        });
    });

    // Mobile Drop Zones (Category Headers)
    document.querySelectorAll('.mobile-group-drop-zone').forEach(zone => {
        zone.addEventListener('dragover', (e) => {
            if (!draggedBucket || draggedBucket.type !== 'mobile') return;
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            zone.classList.add('bg-primary/20', 'border-primary');
        });

        zone.addEventListener('dragleave', (e) => {
            if (!zone.contains(e.relatedTarget)) {
                zone.classList.remove('bg-primary/20', 'border-primary');
            }
        });

        zone.addEventListener('drop', (e) => {
            e.preventDefault();
            zone.classList.remove('bg-primary/20', 'border-primary');
            if (!draggedBucket || draggedBucket.type !== 'mobile') return;

            const targetGroupId = zone.getAttribute('data-group-id');
            const targetGroupName = zone.getAttribute('data-group-name');
            if (targetGroupId === draggedBucket.groupId) return;

            moveMobileBucketToGroupDOM(draggedBucket, targetGroupId, targetGroupName);
        });
    });
}

function moveBucketToGroupDOM(item, targetGroupId, targetGroupName) {
    const oldGroupId = item.groupId;
    const oldGroupName = item.groupName;
    const row = item.rowEl;
    const subrow = item.subrowEl;
    const header = document.getElementById('group-header-' + targetGroupId);
    if (!header) return;

    // Save old location for rollback
    const prevSibling = row.previousElementSibling;

    // Optimistic DOM update
    row.classList.remove('group-item-' + oldGroupId);
    row.classList.add('group-item-' + targetGroupId);
    row.setAttribute('data-group-id', targetGroupId);
    row.setAttribute('data-group-name', targetGroupName);

    if (subrow) {
        subrow.classList.remove('group-item-' + oldGroupId);
        subrow.classList.add('group-item-' + targetGroupId);
    }

    header.after(row);
    if (subrow) {
        row.after(subrow);
    }

    // Also update search attribute tokens
    const searchAttr = row.getAttribute('data-search') || '';
    row.setAttribute('data-search', searchAttr.replace(oldGroupName.toLowerCase(), targetGroupName.toLowerCase()));

    // Send request to server
    fetch('/budgets/' + currentBudgetID + '/buckets/' + item.id + '/move-group', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
            'Accept': 'application/json',
            'X-Requested-With': 'XMLHttpRequest'
        },
        body: new URLSearchParams({
            'csrf_token': csrfTokenVal,
            'group_name': targetGroupName
        })
    })
    .then(res => {
        if (!res.ok) throw new Error('Server returned status ' + res.status);
        return res.json();
    })
    .then(() => {
        showAppToast('Bucket moved to ' + targetGroupName, true);
    })
    .catch(err => {
        // Rollback DOM
        row.classList.remove('group-item-' + targetGroupId);
        row.classList.add('group-item-' + oldGroupId);
        row.setAttribute('data-group-id', oldGroupId);
        row.setAttribute('data-group-name', oldGroupName);
        if (subrow) {
            subrow.classList.remove('group-item-' + targetGroupId);
            subrow.classList.add('group-item-' + oldGroupId);
        }
        if (prevSibling) {
            prevSibling.after(row);
        }
        if (subrow) {
            row.after(subrow);
        }
        showAppToast('Failed to move bucket: ' + err.message, false);
    });
}

function moveMobileBucketToGroupDOM(item, targetGroupId, targetGroupName) {
    const oldGroupId = item.groupId;
    const oldGroupName = item.groupName;
    const card = item.cardEl;
    const header = document.getElementById('mobile-group-' + targetGroupId);
    if (!header) return;

    const prevSibling = card.previousElementSibling;

    // Optimistic DOM update
    card.classList.remove('mobile-group-item-' + oldGroupId);
    card.classList.add('mobile-group-item-' + targetGroupId);
    card.setAttribute('data-group-id', targetGroupId);
    card.setAttribute('data-group-name', targetGroupName);

    header.after(card);

    fetch('/budgets/' + currentBudgetID + '/buckets/' + item.id + '/move-group', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
            'Accept': 'application/json',
            'X-Requested-With': 'XMLHttpRequest'
        },
        body: new URLSearchParams({
            'csrf_token': csrfTokenVal,
            'group_name': targetGroupName
        })
    })
    .then(res => {
        if (!res.ok) throw new Error('Server returned status ' + res.status);
        return res.json();
    })
    .then(() => {
        showAppToast('Bucket moved to ' + targetGroupName, true);
    })
    .catch(err => {
        card.classList.remove('mobile-group-item-' + targetGroupId);
        card.classList.add('mobile-group-item-' + oldGroupId);
        card.setAttribute('data-group-id', oldGroupId);
        card.setAttribute('data-group-name', oldGroupName);
        if (prevSibling) {
            prevSibling.after(card);
        }
        showAppToast('Failed to move bucket: ' + err.message, false);
    });
}

function toggleBucketTargets(bID) {
    const row = document.getElementById('targets-' + bID);
    if (!row) return;
    const isNowHidden = !row.classList.contains('hidden');
    row.classList.toggle('hidden', isNowHidden);
    const icon = document.getElementById('bucket-toggle-icon-' + bID);
    if (icon) icon.innerText = isNowHidden ? '▶' : '▼';
    const btn = document.querySelector('.target-toggle-btn[aria-controls="targets-' + bID + '"]');
    if (btn) btn.setAttribute('aria-expanded', isNowHidden ? 'false' : 'true');
    localStorage.setItem('balance_bucket_expanded_' + currentBudgetID + '_' + bID, isNowHidden ? 'false' : 'true');
}

function toggleMobileTargets(bID) {
    const el = document.getElementById('mobile-targets-' + bID);
    if (!el) return;
    const isNowHidden = !el.classList.contains('hidden');
    el.classList.toggle('hidden', isNowHidden);
    const icon = document.getElementById('mobile-toggle-icon-' + bID);
    if (icon) icon.innerText = isNowHidden ? '▶' : '▼';
}

function toggleGroup(groupID) {
    const items = document.querySelectorAll('.group-item-' + groupID);
    let anyHidden = false;
    items.forEach(el => {
        if (el.classList.contains('hidden')) anyHidden = true;
    });
    const shouldHide = !anyHidden;
    items.forEach(el => el.classList.toggle('hidden', shouldHide));
    const icon = document.getElementById('group-icon-' + groupID);
    if (icon) icon.innerText = shouldHide ? '▶' : '▼';
}

function toggleMobileGroup(groupID) {
    const items = document.querySelectorAll('.mobile-group-item-' + groupID);
    let anyHidden = false;
    items.forEach(el => {
        if (el.classList.contains('hidden')) anyHidden = true;
    });
    const shouldHide = !anyHidden;
    items.forEach(el => el.classList.toggle('hidden', shouldHide));
    const icon = document.getElementById('mobile-group-icon-' + groupID);
    if (icon) icon.innerText = shouldHide ? '▶' : '▼';
}

function expandAllEnvelopes() {
    document.querySelectorAll('.target-subrows').forEach(row => {
        row.classList.remove('hidden');
        const bID = row.getAttribute('data-bucket-id');
        const icon = document.getElementById('bucket-toggle-icon-' + bID);
        if (icon) icon.innerText = '▼';
        const btn = document.querySelector('.target-toggle-btn[aria-controls="targets-' + bID + '"]');
        if (btn) btn.setAttribute('aria-expanded', 'true');
        localStorage.setItem('balance_bucket_expanded_' + currentBudgetID + '_' + bID, 'true');
    });
    document.querySelectorAll('.mobile-bucket-card').forEach(card => {
        card.classList.remove('hidden');
    });
    document.querySelectorAll('.mobile-group-header').forEach(hdr => {
        const gID = hdr.getAttribute('data-group-id');
        const icon = document.getElementById('mobile-group-icon-' + gID);
        if (icon) icon.innerText = '▼';
    });
    document.querySelectorAll('.mobile-target-sublist').forEach(el => {
        el.classList.remove('hidden');
        const bID = el.getAttribute('data-bucket-id');
        const icon = document.getElementById('mobile-toggle-icon-' + bID);
        if (icon) icon.innerText = '▼';
    });
}

function collapseAllEnvelopes() {
    document.querySelectorAll('.target-subrows').forEach(row => {
        row.classList.add('hidden');
        const bID = row.getAttribute('data-bucket-id');
        const icon = document.getElementById('bucket-toggle-icon-' + bID);
        if (icon) icon.innerText = '▶';
        const btn = document.querySelector('.target-toggle-btn[aria-controls="targets-' + bID + '"]');
        if (btn) btn.setAttribute('aria-expanded', 'false');
        localStorage.setItem('balance_bucket_expanded_' + currentBudgetID + '_' + bID, 'false');
    });
    document.querySelectorAll('.mobile-target-sublist').forEach(el => {
        el.classList.add('hidden');
        const bID = el.getAttribute('data-bucket-id');
        const icon = document.getElementById('mobile-toggle-icon-' + bID);
        if (icon) icon.innerText = '▶';
    });
}

function filterEnvelopeSearch(query) {
    const q = (query || '').toLowerCase().trim();
    document.querySelectorAll('.bucket-row').forEach(row => {
        const text = (row.getAttribute('data-search') || '').toLowerCase();
        const matches = !q || text.includes(q);
        row.classList.toggle('hidden', !matches);
        const bID = row.getAttribute('data-bucket-id');
        const targetRow = document.getElementById('targets-' + bID);
        if (targetRow && !matches) {
            targetRow.classList.add('hidden');
        }
    });
    document.querySelectorAll('.mobile-bucket-card').forEach(card => {
        const text = (card.getAttribute('data-search') || '').toLowerCase();
        const matches = !q || text.includes(q);
        card.classList.toggle('hidden', !matches);
    });
    document.querySelectorAll('.mobile-group-header').forEach(hdr => {
        const gID = hdr.getAttribute('data-group-id');
        const visibleCards = document.querySelectorAll('.mobile-group-item-' + gID + ':not(.hidden)');
        hdr.classList.toggle('hidden', q.length > 0 && visibleCards.length === 0);
    });
}

function openNewBucketModal() {
    new_bucket_modal.showModal();
    const inp = document.getElementById('new_bucket_name');
    if (inp) setTimeout(() => inp.focus(), 50);
}

function openRenameGroupModal(groupName) {
    document.getElementById('rename_group_old_name').value = groupName;
    const inp = document.getElementById('rename_group_new_name');
    inp.value = groupName;
    rename_group_modal.showModal();
    setTimeout(() => inp.focus(), 50);
}

function openEditBucketModal(id, name, group) {
    document.getElementById('edit_bucket_form').action = '/budgets/' + currentBudgetID + '/buckets/' + id + '/update';
    const inp = document.getElementById('edit_bucket_name');
    inp.value = name;
    document.getElementById('edit_bucket_group').value = group;
    edit_bucket_modal.showModal();
    setTimeout(() => inp.focus(), 50);
}

function openAssignModal(id, name, curr) {
    document.getElementById('assign_funds_form').action = '/budgets/' + currentBudgetID + '/buckets/' + id + '/assign';
    document.getElementById('assign_bucket_name').innerText = name;
    document.getElementById('assign_currency').value = curr;
    document.getElementById('assign_currency_label').innerText = curr;
    assign_funds_modal.showModal();
    const inp = document.getElementById('assign_amount_input');
    if (inp) setTimeout(() => inp.focus(), 50);
}

function openMoveModal(id, name, curr) {
    document.getElementById('move_source_id').value = id;
    document.getElementById('move_source_name').innerText = name;
    document.getElementById('move_currency').value = curr;
    document.getElementById('move_currency_label').innerText = curr;
    move_funds_modal.showModal();
    const inp = document.getElementById('move_amount_input');
    if (inp) setTimeout(() => inp.focus(), 50);
}

let activeDrawerBucketID = '';

function openBucketDrawer(id, name, group, bal, curr) {
    activeDrawerBucketID = id;
    document.getElementById('drawer_bucket_name').innerText = name;
    document.getElementById('drawer_bucket_meta').innerText = 'Group: ' + group + ' • ID: ' + id;
    document.getElementById('drawer_bucket_balance').innerText = bal + ' ' + curr;

    const b = bucketsData.find(x => x.ID === id);
    if (b) {
        const reqStr = (b.TotalRequiredMinor / 100).toFixed(2);
        const fundStr = (b.TotalFundedMinor / 100).toFixed(2);
        const bufStr = (b.UnassignedBufferMinor / 100).toFixed(2);
        const reqEl = document.getElementById('drawer_bucket_funded_req');
        if (reqEl) reqEl.innerText = fundStr + ' / ' + reqStr + ' ' + curr;
        const bufEl = document.getElementById('drawer_bucket_buffer');
        if (bufEl) {
            bufEl.innerText = (b.UnassignedBufferMinor > 0 ? '+' : '') + bufStr + ' ' + curr;
            bufEl.className = 'stat-value text-base font-mono font-bold ' + 
                (b.UnassignedBufferMinor < 0 ? 'text-error' : (b.UnassignedBufferMinor > 0 ? 'text-success' : 'text-base-content/60'));
        }
    }

    const container = document.getElementById('drawer_targets_container');
    container.innerHTML = '';

    if (b && b.Targets && b.Targets.length > 0) {
        b.Targets.forEach(t => {
            const card = document.createElement('div');
            card.className = 'card card-border bg-base-100 border-base-300 shadow-xs p-4 flex flex-col gap-2';
            const priorityText = t.Priority === 3 ? 'Mandatory' : (t.Priority === 2 ? 'Recommended' : 'Optional');
            const ceilingHtml = t.CeilingAmountMinor ? ' • <span class="font-semibold text-primary">Goal: ' + (t.CeilingAmountMinor/100).toFixed(2) + ' ' + t.Currency + '</span>' : '';
            const luaBadge = t.AmountMode === 'LUA_FORMULA' ? ' <span class="badge badge-outline badge-xs font-mono text-secondary border-secondary">LUA</span>' : '';
            const formulaErrHtml = (t.LastFormulaError && t.LastFormulaError.length > 0) ? '<div class="text-[11px] text-error font-mono mt-1">⚠️ ' + escapeHtml(t.LastFormulaError) + '</div>' : '';
            const rInt = t.RecurrenceInterval || t.RecurrenceDays;
            const cadenceDisplay = t.RecurrenceCadence === 'CUSTOM_DAYS' ? ((rInt || 90) + 'd') :
                (t.RecurrenceCadence === 'CUSTOM_MONTHS' ? ((rInt || 3) + 'm') :
                (t.RecurrenceCadence === 'CUSTOM_YEARS' ? ((rInt || 1) + 'y') : t.RecurrenceCadence));

            let dueHtml = '';
            if (t.DueDate && t.DueDate > 0) {
                const d = new Date(t.DueDate / 1000);
                dueHtml = ' • Due: ' + d.toISOString().split('T')[0];
            }

            const targetTitle = (t.Name && t.Name.trim().length > 0) ? (escapeHtml(t.Name) + luaBadge) : ('<span class="badge ' + (t.Priority === 3 ? 'badge-error' : (t.Priority === 2 ? 'badge-primary' : 'badge-ghost')) + ' badge-sm font-semibold">' + priorityText + '</span>' + luaBadge);

            card.innerHTML = 
                '<div class="flex justify-between items-start">' +
                    '<div>' +
                        '<div class="font-bold text-sm tracking-tight flex items-center gap-1">' +
                            targetTitle +
                        '</div>' +
                        '<div class="text-[11px] text-base-content/60">Priority: ' + priorityText + ' • Cadence: ' + cadenceDisplay + dueHtml + ' • Strategy: ' + t.NonSpendingStrategy + ceilingHtml + '</div>' +
                    '</div>' +
                    '<span class="badge ' + t.HealthBadgeClass + ' badge-sm font-semibold">' + t.HealthBadge + '</span>' +
                '</div>' +
                formulaErrHtml +
                '<div class="flex justify-between items-center text-xs mt-1">' +
                    '<span class="font-mono">Funded: ' + (t.CurrentFundedMinor/100).toFixed(2) + ' / Required: ' + (t.RequiredMinor/100).toFixed(2) + ' ' + t.Currency + '</span>' +
                    '<div class="flex items-center gap-1">' +
                        '<button type="button" class="btn btn-ghost btn-sm sm:btn-xs text-secondary font-semibold touch-manipulation" onclick="openMoveTargetFundsModal(\'' + t.ID + '\')">⇄ Move</button>' +
                        '<button type="button" class="btn btn-ghost btn-sm sm:btn-xs text-primary font-semibold target-edit-btn touch-manipulation" onclick="openEditTargetModal(\'' + t.ID + '\')">Edit</button>' +
                        '<form method="POST" action="/budgets/' + currentBudgetID + '/targets/' + t.ID + '/delete" onsubmit="return confirm(\'Delete this target?\')">' +
                            '<input type="hidden" name="csrf_token" value="' + csrfTokenVal + '" />' +
                            '<input type="hidden" name="horizon" value="' + currentHorizon + '" />' +
                            '<input type="hidden" name="filter" value="' + currentFilter + '" />' +
                            '<button type="submit" class="btn btn-ghost btn-sm sm:btn-xs text-error font-semibold touch-manipulation">Delete</button>' +
                        '</form>' +
                    '</div>' +
                '</div>';
            container.appendChild(card);
        });
    } else {
        container.innerHTML = '<div class="text-xs text-base-content/50 py-3 text-center border border-dashed border-base-300 rounded-lg">No targets added to this envelope yet.</div>';
    }

    bucket_drawer.showModal();
}

function openEditTargetModal(id) {
    const b = bucketsData.find(x => x.ID === activeDrawerBucketID);
    if (!b || !b.Targets) return;
    const t = b.Targets.find(x => x.ID === id);
    if (!t) return;

    document.getElementById('target_modal_title').innerText = 'Edit Target';
    document.getElementById('target_form').action = '/budgets/' + currentBudgetID + '/targets/' + t.ID + '/update';
    document.getElementById('target_bucket_id').value = t.BucketID;
    document.getElementById('target_name').value = t.Name;
    document.getElementById('target_type').value = t.TargetType;
    document.getElementById('target_priority').value = t.Priority;
    document.getElementById('target_cadence').value = t.RecurrenceCadence;
    const rIntVal = t.RecurrenceInterval || t.RecurrenceDays || 90;
    if (document.getElementById('target_recurrence_days')) {
        document.getElementById('target_recurrence_days').value = rIntVal;
    }
    if (document.getElementById('target_due_date')) {
        if (t.DueDate && t.DueDate > 0) {
            const d = new Date(t.DueDate / 1000);
            document.getElementById('target_due_date').value = d.toISOString().split('T')[0];
        } else {
            document.getElementById('target_due_date').value = '';
        }
    }
    document.getElementById('target_strategy').value = t.NonSpendingStrategy;
    document.getElementById('target_amount').value = (t.AmountMinor / 100).toFixed(2);
    document.getElementById('target_ceiling').value = t.CeilingAmountMinor ? (t.CeilingAmountMinor / 100).toFixed(2) : '';
    const currSelect = document.getElementById('target_currency');
    if (currSelect) {
        let hasOpt = false;
        for (let i = 0; i < currSelect.options.length; i++) {
            if (currSelect.options[i].value === t.Currency) {
                hasOpt = true;
                break;
            }
        }
        if (!hasOpt && t.Currency) {
            const opt = document.createElement('option');
            opt.value = t.Currency;
            opt.innerText = t.Currency;
            currSelect.appendChild(opt);
        }
        currSelect.value = t.Currency;
        currSelect.disabled = true;
    }
    const currHelp = document.getElementById('target_currency_help');
    if (currHelp) {
        currHelp.classList.remove('hidden');
    }
    document.getElementById('target_lua_script').value = t.LuaScript || '';

    toggleCustomDays();
    hideFormulaPreview();
    hideAIPanel();

    const mode = t.AmountMode === 'LUA_FORMULA' ? 'LUA_FORMULA' : 'FIXED';
    switchAmountMode(mode);

    target_modal.showModal();
}

function openMoveTargetFundsModal(preselectedSrcID, preselectedDstID) {
    const b = bucketsData.find(x => x.ID === activeDrawerBucketID);
    if (!b) return;

    document.getElementById('move_target_bucket_id').value = b.ID;
    document.getElementById('move_target_bucket_name').innerText = b.Name;
    document.getElementById('move_target_currency').value = b.Currency;
    document.getElementById('move_target_curr_label').innerText = b.Currency;

    const srcSelect = document.getElementById('move_source_target_id');
    const dstSelect = document.getElementById('move_dest_target_id');

    srcSelect.innerHTML = '';
    dstSelect.innerHTML = '';

    // Add unassigned buffer option
    const bufferOpt = document.createElement('option');
    bufferOpt.value = 'BUFFER';
    bufferOpt.innerText = '📂 Envelope Buffer (' + (b.UnassignedBufferMinor / 100).toFixed(2) + ' ' + b.Currency + ')';
    srcSelect.appendChild(bufferOpt);

    const dstBufferOpt = document.createElement('option');
    dstBufferOpt.value = 'BUFFER';
    dstBufferOpt.innerText = '📂 Envelope Buffer';
    dstSelect.appendChild(dstBufferOpt);

    if (b.Targets) {
        b.Targets.forEach(t => {
            const opt1 = document.createElement('option');
            opt1.value = t.ID;
            opt1.innerText = (t.Name || 'Priority ' + t.Priority) + ' (Funded: ' + (t.CurrentFundedMinor / 100).toFixed(2) + ' ' + t.Currency + ')';
            srcSelect.appendChild(opt1);

            const opt2 = document.createElement('option');
            opt2.value = t.ID;
            opt2.innerText = (t.Name || 'Priority ' + t.Priority) + ' (Needs: ' + (t.NeededMinor / 100).toFixed(2) + ' ' + t.Currency + ')';
            dstSelect.appendChild(opt2);
        });
    }

    if (preselectedSrcID) {
        srcSelect.value = preselectedSrcID;
    } else if (b.Targets && b.Targets.length > 0) {
        srcSelect.value = b.Targets[0].ID;
    }

    if (preselectedDstID) {
        dstSelect.value = preselectedDstID;
    } else if (b.Targets && b.Targets.length > 1) {
        dstSelect.value = b.Targets[1].ID;
    } else {
        dstSelect.value = 'BUFFER';
    }

    move_target_funds_modal.showModal();
}

function updateMoveTargetOptions() {
    // Optional sync logic between selects
}

function openNewTargetModal() {
    document.getElementById('target_modal_title').innerText = 'Add Target';
    document.getElementById('target_form').action = '/budgets/' + currentBudgetID + '/targets';
    document.getElementById('target_bucket_id').value = activeDrawerBucketID;
    document.getElementById('target_name').value = '';
    document.getElementById('target_type').value = 'EXPENSE';
    document.getElementById('target_priority').value = '2';
    document.getElementById('target_cadence').value = 'MONTHLY';
    if (document.getElementById('target_recurrence_days')) {
        document.getElementById('target_recurrence_days').value = '90';
    }
    if (document.getElementById('target_due_date')) {
        document.getElementById('target_due_date').value = '';
    }
    document.getElementById('target_strategy').value = 'ROLL_OVER';
    document.getElementById('target_amount').value = '';
    document.getElementById('target_ceiling').value = '';
    const newCurrSelect = document.getElementById('target_currency');
    if (newCurrSelect) {
        newCurrSelect.value = currentCurrency;
        newCurrSelect.disabled = false;
    }
    const newCurrHelp = document.getElementById('target_currency_help');
    if (newCurrHelp) {
        newCurrHelp.classList.add('hidden');
    }
    document.getElementById('target_lua_script').value = '';

    toggleCustomDays();
    hideFormulaPreview();
    hideAIPanel();
    switchAmountMode('FIXED');

    target_modal.showModal();
}

function toggleCustomDays() {
    const cadenceSelect = document.getElementById('target_cadence');
    const customContainer = document.getElementById('custom_days_container');
    const rInput = document.getElementById('target_recurrence_days');
    const rLabel = document.getElementById('target_recurrence_interval_label');
    const dueDateContainer = document.getElementById('target_due_date_container');
    const dueDateInput = document.getElementById('target_due_date');
    const dueDateLabel = document.getElementById('target_due_date_label');
    if (!cadenceSelect || !customContainer) return;

    const val = cadenceSelect.value;
    const isCustomDays = val === 'CUSTOM_DAYS';
    const isCustomMonths = val === 'CUSTOM_MONTHS';
    const isCustomYears = val === 'CUSTOM_YEARS';
    const isCustom = isCustomDays || isCustomMonths || isCustomYears;
    const isOneOff = val === 'ONE_OFF';
    const isWeeklyMonthlyAnnual = val === 'WEEKLY' || val === 'MONTHLY' || val === 'ANNUAL';

    if (isCustom) {
        customContainer.classList.remove('hidden');
        if (isCustomDays) {
            if (rLabel) rLabel.innerText = 'Recurrence Interval (Days)';
            if (rInput && (!rInput.value || parseInt(rInput.value) < 1)) rInput.value = '90';
        } else if (isCustomMonths) {
            if (rLabel) rLabel.innerText = 'Recurrence Interval (Months)';
            if (rInput && (!rInput.value || parseInt(rInput.value) < 1)) rInput.value = '3';
        } else if (isCustomYears) {
            if (rLabel) rLabel.innerText = 'Recurrence Interval (Years)';
            if (rInput && (!rInput.value || parseInt(rInput.value) < 1)) rInput.value = '1';
        }
    } else {
        customContainer.classList.add('hidden');
    }

    if (dueDateContainer && dueDateInput) {
        if (isCustomMonths || isCustomYears) {
            dueDateContainer.classList.remove('hidden');
            dueDateInput.required = true;
            if (dueDateLabel) dueDateLabel.innerHTML = 'Anchor Due Date <span class="text-error">*</span>';
            if (!dueDateInput.value) {
                const d = new Date();
                dueDateInput.value = d.toISOString().split('T')[0];
            }
        } else if (isOneOff) {
            dueDateContainer.classList.remove('hidden');
            dueDateInput.required = false;
            if (dueDateLabel) dueDateLabel.innerText = 'Target Due Date (Optional)';
        } else if (isWeeklyMonthlyAnnual) {
            dueDateContainer.classList.remove('hidden');
            dueDateInput.required = false;
            if (dueDateLabel) dueDateLabel.innerText = 'Anchor Due Date (Optional)';
        } else {
            dueDateContainer.classList.add('hidden');
            dueDateInput.required = false;
        }
    }
}

function switchAmountMode(mode) {
    const isLua = mode === 'LUA_FORMULA';
    const rFixed = document.getElementById('mode_fixed');
    const rLua = document.getElementById('mode_lua');
    if (rFixed) rFixed.checked = !isLua;
    if (rLua) rLua.checked = isLua;

    const fixedContainer = document.getElementById('fixed_amount_container');
    const luaContainer = document.getElementById('lua_formula_container');
    const targetAmt = document.getElementById('target_amount');

    if (isLua) {
        if (fixedContainer) fixedContainer.classList.add('hidden');
        if (luaContainer) luaContainer.classList.remove('hidden');
        if (targetAmt) targetAmt.removeAttribute('required');
    } else {
        if (fixedContainer) fixedContainer.classList.remove('hidden');
        if (luaContainer) luaContainer.classList.add('hidden');
        if (targetAmt) targetAmt.setAttribute('required', 'required');
    }
}

function toggleAIPanel() {
    const panel = document.getElementById('ai_assistant_panel');
    if (panel) panel.classList.toggle('hidden');
}

function hideAIPanel() {
    const panel = document.getElementById('ai_assistant_panel');
    if (panel) panel.classList.add('hidden');
    const statusMsg = document.getElementById('ai_status_msg');
    if (statusMsg) {
        statusMsg.innerText = '';
        statusMsg.classList.add('hidden');
    }
}

function showFormulaPreview(type, htmlContent) {
    const box = document.getElementById('formula_preview_box');
    if (!box) return;
    box.className = 'text-xs p-3 rounded-lg border flex flex-col gap-1 ' + 
        (type === 'success' ? 'bg-success/10 border-success/30 text-success' : 
        (type === 'error' ? 'bg-error/10 border-error/30 text-error' : 'bg-base-200 border-base-300'));
    box.innerHTML = htmlContent;
    box.classList.remove('hidden');
}

function hideFormulaPreview() {
    const box = document.getElementById('formula_preview_box');
    if (box) {
        box.innerHTML = '';
        box.classList.add('hidden');
    }
}

function generateFormulaAI() {
    const promptInput = document.getElementById('ai_formula_prompt');
    const prompt = promptInput ? promptInput.value.trim() : '';
    const statusMsg = document.getElementById('ai_status_msg');
    const btnGen = document.getElementById('btn_generate_ai');

    if (!prompt) {
        if (statusMsg) {
            statusMsg.className = 'text-xs text-error mt-1';
            statusMsg.innerText = 'Please describe the formula in natural language.';
            statusMsg.classList.remove('hidden');
        }
        return;
    }

    if (statusMsg) {
        statusMsg.className = 'text-xs text-base-content/70 mt-1 flex items-center gap-1';
        statusMsg.innerHTML = '<span class="loading loading-spinner loading-xs"></span> Generating formula with AI...';
        statusMsg.classList.remove('hidden');
    }
    if (btnGen) btnGen.disabled = true;

    const dueDateInput = document.getElementById('target_due_date');
    const dueDateVal = dueDateInput ? dueDateInput.value : '';
    const targetNameInput = document.getElementById('target_name');
    const targetNameVal = targetNameInput ? targetNameInput.value.trim() : '';

    fetch('/api/v1/budgets/' + currentBudgetID + '/targets/generate-formula', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'X-CSRF-Token': csrfTokenVal
        },
        body: JSON.stringify({
            current_bucket_name: document.getElementById('drawer_bucket_name').innerText.trim() || 'General',
            target_currency: (document.getElementById('target_currency') ? document.getElementById('target_currency').value : '') || currentCurrency || 'PLN',
            user_prompt: prompt,
            window_days: 30,
            due_date: dueDateVal,
            target_name: targetNameVal
        })
    })
    .then(res => res.json())
    .then(data => {
        if (btnGen) btnGen.disabled = false;
        if (data.error || !data.lua_script) {
            if (statusMsg) {
                statusMsg.className = 'text-xs text-error mt-1';
                statusMsg.innerText = data.error || 'Failed generating formula';
            }
        } else {
            if (statusMsg) {
                statusMsg.className = 'text-xs text-success mt-1';
                statusMsg.innerHTML = '✨ <strong>Generated:</strong> ' + escapeHtml(data.explanation || '');
            }
            document.getElementById('target_lua_script').value = data.lua_script;
            validateFormulaScript();
        }
    })
    .catch(err => {
        if (btnGen) btnGen.disabled = false;
        if (statusMsg) {
            statusMsg.className = 'text-xs text-error mt-1';
            statusMsg.innerText = 'Network error: ' + err.message;
        }
    });
}

function validateFormulaScript() {
    const script = document.getElementById('target_lua_script').value.trim();
    if (!script) {
        showFormulaPreview('error', '⚠️ Please enter a Lua script to validate.');
        return;
    }

    const dueDateInput = document.getElementById('target_due_date');
    const dueDateVal = dueDateInput ? dueDateInput.value : '';

    showFormulaPreview('info', '<div class="flex items-center gap-1"><span class="loading loading-spinner loading-xs"></span> Validating and running dry-run evaluation...</div>');

    fetch('/api/v1/budgets/' + currentBudgetID + '/targets/validate-formula', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'X-CSRF-Token': csrfTokenVal
        },
        body: JSON.stringify({
            lua_script: script,
            due_date: dueDateVal
        })
    })
    .then(res => res.json())
    .then(data => {
        if (data.valid) {
            let depsHtml = (data.dependencies && data.dependencies.length > 0) 
                ? 'Dependencies: <span class="font-mono">' + escapeHtml(data.dependencies.join(', ')) + '</span>' 
                : 'No external target dependencies';
            let previewHtml = '';
            if (data.evaluated_minor !== undefined && data.evaluated_minor !== null) {
                const evalAmt = (data.evaluated_minor / 100).toFixed(2);
                previewHtml = '<div class="font-bold text-sm text-primary mt-1">Calculated Preview: ' + evalAmt + ' ' + currentCurrency + '</div>';
                document.getElementById('target_amount').value = evalAmt;
            }
            showFormulaPreview('success', 
                '<div class="flex items-center gap-1 font-bold text-success">✓ Valid Sandboxed Formula</div>' +
                '<div class="text-[11px] text-base-content/70">' + depsHtml + '</div>' +
                previewHtml
            );
        } else {
            showFormulaPreview('error', 
                '<div class="font-bold text-error">✕ Validation Failed</div>' +
                '<div class="font-mono text-[11px]">' + escapeHtml(data.error || 'Invalid script') + '</div>'
            );
        }
    })
    .catch(err => {
        showFormulaPreview('error', '✕ Network error: ' + escapeHtml(err.message));
    });
}

function prepareTargetSubmit() {
    const rLua = document.getElementById('mode_lua');
    const targetAmt = document.getElementById('target_amount');
    if (rLua && rLua.checked) {
        if (!targetAmt.value || targetAmt.value.trim() === '') {
            targetAmt.value = '0.00';
        }
    }
}

function openStructureModal(tab) {
    document.getElementById('structure_error').classList.add('hidden');
    document.getElementById('structure_preview_wrapper').classList.add('hidden');
    document.getElementById('btn_apply_structure').classList.add('hidden');
    switchStructureTab(tab || 'ai');
    structure_modal.showModal();
}

function switchStructureTab(tab) {
    const isAI = tab === 'ai';
    document.getElementById('tab_btn_ai').className = isAI ? 'tab tab-active font-semibold text-xs' : 'tab font-semibold text-xs';
    document.getElementById('tab_btn_import').className = !isAI ? 'tab tab-active font-semibold text-xs' : 'tab font-semibold text-xs';
    document.getElementById('structure_tab_ai').classList.toggle('hidden', !isAI);
    document.getElementById('structure_tab_import').classList.toggle('hidden', isAI);
}

let loadedStructureData = null;

function runAIGeneration() {
    const prompt = document.getElementById('structure_ai_prompt').value.trim();
    if (!prompt) {
        showStructureError('Please enter a budget description.');
        return;
    }
    showStructureLoading(true, 'Analyzing description and generating budget buckets with AI...');
    fetch('/budgets/' + currentBudgetID + '/structure/generate-ai-preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfTokenVal },
        body: JSON.stringify({ prompt: prompt, currency: currentCurrency })
    })
    .then(res => res.json())
    .then(data => {
        showStructureLoading(false);
        if (data.error) {
            showStructureError(data.error);
        } else {
            renderPreviewData(data);
        }
    })
    .catch(err => {
        showStructureLoading(false);
        showStructureError(err.message || 'Generation failed');
    });
}

function runSourceImport() {
    const sel = document.getElementById('source_budget_select');
    if (!sel || !sel.value) {
        showStructureError('Please select a source budget.');
        return;
    }
    showStructureLoading(true, 'Loading buckets and targets from source budget...');
    fetch('/budgets/' + currentBudgetID + '/structure/preview-source?source_budget_id=' + encodeURIComponent(sel.value))
    .then(res => res.json())
    .then(data => {
        showStructureLoading(false);
        if (data.error) {
            showStructureError(data.error);
        } else {
            renderPreviewData(data);
        }
    })
    .catch(err => {
        showStructureLoading(false);
        showStructureError(err.message || 'Import failed');
    });
}

function showStructureLoading(show, text) {
    document.getElementById('structure_loading').classList.toggle('hidden', !show);
    document.getElementById('structure_error').classList.add('hidden');
    if (show) {
        document.getElementById('structure_preview_wrapper').classList.add('hidden');
        document.getElementById('btn_apply_structure').classList.add('hidden');
        if (text) document.getElementById('structure_loading_text').innerText = text;
    }
}

function showStructureError(msg) {
    const el = document.getElementById('structure_error');
    document.getElementById('structure_error_text').innerText = msg;
    el.classList.remove('hidden');
}

function onBucketGroupChange(gIdx, bIdx, val) {
    const customInput = document.querySelector('.bucket-custom-group-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
    if (customInput) {
        const isNew = (val === '__NEW__');
        customInput.classList.toggle('hidden', !isNew);
        if (isNew) {
            customInput.focus();
        }
    }
}

function onBucketConflictChange(gIdx, bIdx, action) {
    const bucketCard = document.querySelector('.bucket-card[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
    if (bucketCard) {
        bucketCard.classList.toggle('opacity-50', action === 'SKIP');
    }
    const bChk = document.querySelector('.bucket-chk[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
    if (bChk) {
        bChk.checked = (action !== 'SKIP');
        onBucketCheckboxToggle(gIdx, bIdx, bChk.checked);
    }
    updateCountsBadge();
}

function renderPreviewData(data) {
    loadedStructureData = data;
    const container = document.getElementById('structure_preview_list');
    container.innerHTML = '';

    let totalBuckets = 0;
    let totalTargets = 0;

    if (!data.groups || data.groups.length === 0) {
        showStructureError('No buckets or categories could be generated.');
        return;
    }

    // Gather all distinct group names (from existing groups in budget + proposed groups)
    const groupSet = new Set();
    if (data.existing_groups && Array.isArray(data.existing_groups)) {
        data.existing_groups.forEach(g => { if (g && g.trim()) groupSet.add(g.trim()); });
    }
    if (data.groups && Array.isArray(data.groups)) {
        data.groups.forEach(g => { if (g && g.group_name && g.group_name.trim()) groupSet.add(g.group_name.trim()); });
    }
    const allKnownGroups = Array.from(groupSet);

    data.groups.forEach((g, gIdx) => {
        const groupEl = document.createElement('div');
        groupEl.className = 'border border-base-300 rounded-lg p-3 bg-base-100 shadow-xs flex flex-col gap-2';

        let bucketsHtml = '';
        if (g.buckets) {
            g.buckets.forEach((b, bIdx) => {
                totalBuckets++;
                let targetsHtml = '';
                if (b.targets) {
                    b.targets.forEach((t, tIdx) => {
                        totalTargets++;
                        const pVal = t.priority || 2;
                        const amtVal = (t.amount_minor / 100).toFixed(2);

                        targetsHtml += 
                            '<div class="flex items-center gap-2 pl-6 py-1 border-t border-base-200">' +
                                '<input type="checkbox" class="checkbox checkbox-primary checkbox-xs target-chk" checked data-group="' + gIdx + '" data-bucket="' + bIdx + '" data-target="' + tIdx + '" onchange="updateCountsBadge()">' +
                                '<input type="text" class="input input-bordered input-xs flex-1 target-name-input" value="' + escapeHtml(t.name) + '" data-group="' + gIdx + '" data-bucket="' + bIdx + '" data-target="' + tIdx + '">' +
                                '<div class="flex items-center gap-1 shrink-0">' +
                                    '<input type="number" step="0.01" class="input input-bordered input-xs w-20 text-right font-mono target-amount-input" value="' + amtVal + '" data-group="' + gIdx + '" data-bucket="' + bIdx + '" data-target="' + tIdx + '">' +
                                    '<span class="text-[10px] font-bold text-base-content/60">' + (t.currency || currentCurrency) + '</span>' +
                                '</div>' +
                                '<select class="select select-bordered select-xs target-prio-select shrink-0 font-medium" data-group="' + gIdx + '" data-bucket="' + bIdx + '" data-target="' + tIdx + '">' +
                                    '<option value="3"' + (pVal === 3 ? ' selected' : '') + ' class="text-error font-bold">P3 - Mandatory</option>' +
                                    '<option value="2"' + (pVal === 2 ? ' selected' : '') + ' class="text-primary font-bold">P2 - Recommended</option>' +
                                    '<option value="1"' + (pVal === 1 ? ' selected' : '') + ' class="text-base-content/60 font-medium">P1 - Optional</option>' +
                                '</select>' +
                            '</div>';
                    });
                }

                // Group selector options
                const currentBucketGroup = g.group_name || 'General';
                let groupOptionsHtml = '';
                allKnownGroups.forEach(grpName => {
                    const isSelected = (grpName === currentBucketGroup);
                    groupOptionsHtml += '<option value="' + escapeHtml(grpName) + '"' + (isSelected ? ' selected' : '') + '>' + escapeHtml(grpName) + '</option>';
                });
                groupOptionsHtml += '<option value="__NEW__">+ New Group...</option>';

                // Conflict resolution UI if existing bucket detected
                let conflictHtml = '';
                if (b.is_existing) {
                    conflictHtml = 
                        '<div class="flex flex-wrap items-center gap-1.5 bg-warning/10 border border-warning/30 rounded px-2 py-1 text-[11px] mt-1">' +
                            '<span class="badge badge-warning badge-xs font-bold gap-1">⚠️ Existing Bucket</span>' +
                            (b.existing_group ? '<span class="text-[10px] text-base-content/60">(currently in: ' + escapeHtml(b.existing_group) + ')</span>' : '') +
                            '<span class="font-semibold text-base-content/80 ml-auto">Conflict:</span>' +
                            '<select class="select select-bordered select-xs bucket-conflict-select font-semibold" data-group="' + gIdx + '" data-bucket="' + bIdx + '" onchange="onBucketConflictChange(' + gIdx + ', ' + bIdx + ', this.value)">' +
                                '<option value="MERGE" selected>Add new targets (Merge)</option>' +
                                '<option value="REPLACE">Replace existing targets (Overwrite)</option>' +
                                '<option value="SKIP">Skip bucket (Ignore)</option>' +
                            '</select>' +
                        '</div>';
                }

                bucketsHtml += 
                    '<div class="border border-base-200 rounded p-2 bg-base-200/30 flex flex-col gap-1.5 bucket-card" data-group="' + gIdx + '" data-bucket="' + bIdx + '">' +
                        '<div class="flex flex-wrap items-center gap-2">' +
                            '<input type="checkbox" class="checkbox checkbox-primary checkbox-xs bucket-chk" checked data-group="' + gIdx + '" data-bucket="' + bIdx + '" onchange="onBucketCheckboxToggle(' + gIdx + ', ' + bIdx + ', this.checked)">' +
                            '<span class="text-xs font-bold text-primary">📁</span>' +
                            '<input type="text" class="input input-bordered input-xs font-bold flex-1 min-w-[120px] bucket-name-input" value="' + escapeHtml(b.name) + '" data-group="' + gIdx + '" data-bucket="' + bIdx + '">' +
                            '<div class="flex items-center gap-1 shrink-0">' +
                                '<span class="text-[11px] text-base-content/60 font-medium">Group:</span>' +
                                '<select class="select select-bordered select-xs bucket-group-select" data-group="' + gIdx + '" data-bucket="' + bIdx + '" onchange="onBucketGroupChange(' + gIdx + ', ' + bIdx + ', this.value)">' +
                                    groupOptionsHtml +
                                '</select>' +
                                '<input type="text" class="input input-bordered input-xs hidden w-28 bucket-custom-group-input" placeholder="New group..." data-group="' + gIdx + '" data-bucket="' + bIdx + '">' +
                            '</div>' +
                        '</div>' +
                        conflictHtml +
                        (targetsHtml ? '<div class="flex flex-col">' + targetsHtml + '</div>' : '') +
                    '</div>';
            });
        }

        groupEl.innerHTML = 
            '<div class="flex items-center justify-between pb-1">' +
                '<span class="font-extrabold text-xs uppercase tracking-wider text-base-content/70">' + escapeHtml(g.group_name || 'General') + '</span>' +
            '</div>' +
            '<div class="flex flex-col gap-2">' + bucketsHtml + '</div>';

        container.appendChild(groupEl);
    });

    document.getElementById('structure_stats_badge').innerText = totalBuckets + ' buckets, ' + totalTargets + ' targets';
    document.getElementById('structure_select_all').checked = true;
    document.getElementById('structure_preview_wrapper').classList.remove('hidden');
    document.getElementById('btn_apply_structure').classList.remove('hidden');
}

function escapeHtml(str) {
    return (str || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function onBucketCheckboxToggle(gIdx, bIdx, checked) {
    const targets = document.querySelectorAll('.target-chk[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
    targets.forEach(t => t.checked = checked);
    updateCountsBadge();
}

function toggleSelectAllStructure(checked) {
    document.querySelectorAll('.bucket-chk, .target-chk').forEach(c => c.checked = checked);
    updateCountsBadge();
}

function updateCountsBadge() {
    const bChecked = document.querySelectorAll('.bucket-chk:checked').length;
    const tChecked = document.querySelectorAll('.target-chk:checked').length;
    document.getElementById('structure_stats_badge').innerText = bChecked + ' buckets, ' + tChecked + ' targets selected';
}

function applyStructureConfirmed() {
    if (!loadedStructureData) return;

    const groupBucketsMap = new Map();

    loadedStructureData.groups.forEach((g, gIdx) => {
        if (!g.buckets) return;
        g.buckets.forEach((b, bIdx) => {
            const bChk = document.querySelector('.bucket-chk[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
            if (bChk && bChk.checked) {
                const nameInput = document.querySelector('.bucket-name-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
                const bucketName = nameInput ? nameInput.value.trim() : b.name;

                // Target group resolution
                let targetGroup = g.group_name || 'Default';
                const grpSelect = document.querySelector('.bucket-group-select[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
                if (grpSelect) {
                    if (grpSelect.value === '__NEW__') {
                        const customInput = document.querySelector('.bucket-custom-group-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
                        if (customInput && customInput.value.trim()) {
                            targetGroup = customInput.value.trim();
                        }
                    } else if (grpSelect.value) {
                        targetGroup = grpSelect.value.trim();
                    }
                }

                // Conflict resolution
                let conflictRes = 'MERGE';
                const conflictSelect = document.querySelector('.bucket-conflict-select[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"]');
                if (conflictSelect) {
                    conflictRes = conflictSelect.value;
                }
                if (conflictRes === 'SKIP') {
                    return; // User opted to skip this bucket entirely
                }

                const finalTargets = [];
                if (b.targets) {
                    b.targets.forEach((t, tIdx) => {
                        const tChk = document.querySelector('.target-chk[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"][data-target="' + tIdx + '"]');
                        if (tChk && tChk.checked) {
                            const tNameInput = document.querySelector('.target-name-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"][data-target="' + tIdx + '"]');
                            const tAmtInput = document.querySelector('.target-amount-input[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"][data-target="' + tIdx + '"]');
                            const tPrioSelect = document.querySelector('.target-prio-select[data-group="' + gIdx + '"][data-bucket="' + bIdx + '"][data-target="' + tIdx + '"]');

                            const tName = tNameInput ? tNameInput.value.trim() : t.name;
                            let amtMinor = t.amount_minor;
                            if (tAmtInput) {
                                const parsed = parseFloat(tAmtInput.value);
                                if (!isNaN(parsed) && parsed >= 0) {
                                    amtMinor = Math.round(parsed * 100);
                                }
                            }
                            let priority = t.priority || 2;
                            if (tPrioSelect) {
                                const pVal = parseInt(tPrioSelect.value, 10);
                                if (!isNaN(pVal) && pVal >= 1 && pVal <= 3) {
                                    priority = pVal;
                                }
                            }

                            finalTargets.push({
                                name: tName,
                                target_type: t.target_type || 'EXPENSE',
                                priority: priority,
                                amount_minor: amtMinor,
                                currency: t.currency || currentCurrency,
                                recurrence_cadence: t.recurrence_cadence || 'MONTHLY',
                                recurrence_days: t.recurrence_days || null,
                                non_spending_strategy: t.non_spending_strategy || 'RETURN_TO_POOL'
                            });
                        }
                    });
                }

                const bucketData = {
                    name: bucketName,
                    conflict_resolution: conflictRes,
                    targets: finalTargets
                };

                if (!groupBucketsMap.has(targetGroup)) {
                    groupBucketsMap.set(targetGroup, []);
                }
                groupBucketsMap.get(targetGroup).push(bucketData);
            }
        });
    });

    const finalGroups = [];
    groupBucketsMap.forEach((buckets, grpName) => {
        if (buckets.length > 0) {
            finalGroups.push({
                group_name: grpName,
                buckets: buckets
            });
        }
    });

    if (finalGroups.length === 0) {
        showStructureError('No buckets or targets selected.');
        return;
    }

    const btn = document.getElementById('btn_apply_structure');
    btn.disabled = true;
    btn.innerHTML = '<span class="loading loading-spinner loading-xs"></span> Applying...';

    fetch('/budgets/' + currentBudgetID + '/structure/apply', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'X-CSRF-Token': csrfTokenVal
        },
        body: JSON.stringify({ groups: finalGroups })
    })
    .then(res => res.json())
    .then(data => {
        if (data.error) {
            btn.disabled = false;
            btn.innerHTML = '<span>✓</span><span>Apply Selected Structure</span>';
            showStructureError(data.error);
        } else {
            window.location.reload();
        }
    })
    .catch(err => {
        btn.disabled = false;
        btn.innerHTML = '<span>✓</span><span>Apply Selected Structure</span>';
        showStructureError(err.message || 'Failed applying structure');
    });
}

window.showAppToast = showAppToast;
window.toggleBucketTargets = toggleBucketTargets;
window.toggleMobileTargets = toggleMobileTargets;
window.toggleGroup = toggleGroup;
window.toggleMobileGroup = toggleMobileGroup;
window.expandAllEnvelopes = expandAllEnvelopes;
window.collapseAllEnvelopes = collapseAllEnvelopes;
window.filterEnvelopeSearch = filterEnvelopeSearch;
window.openNewBucketModal = openNewBucketModal;
window.openRenameGroupModal = openRenameGroupModal;
window.openEditBucketModal = openEditBucketModal;
window.openAssignModal = openAssignModal;
window.openMoveModal = openMoveModal;
window.openBucketDrawer = openBucketDrawer;
window.openEditTargetModal = openEditTargetModal;
window.openMoveTargetFundsModal = openMoveTargetFundsModal;
window.updateMoveTargetOptions = updateMoveTargetOptions;
window.openNewTargetModal = openNewTargetModal;
window.toggleCustomDays = toggleCustomDays;
window.switchAmountMode = switchAmountMode;
window.toggleAIPanel = toggleAIPanel;
window.hideAIPanel = hideAIPanel;
window.showFormulaPreview = showFormulaPreview;
window.hideFormulaPreview = hideFormulaPreview;
window.generateFormulaAI = generateFormulaAI;
window.validateFormulaScript = validateFormulaScript;
window.prepareTargetSubmit = prepareTargetSubmit;
window.openStructureModal = openStructureModal;
window.switchStructureTab = switchStructureTab;
window.runAIGeneration = runAIGeneration;
window.runSourceImport = runSourceImport;
window.onBucketGroupChange = onBucketGroupChange;
window.onBucketConflictChange = onBucketConflictChange;
window.renderPreviewData = renderPreviewData;
window.onBucketCheckboxToggle = onBucketCheckboxToggle;
window.toggleSelectAllStructure = toggleSelectAllStructure;
window.updateCountsBadge = updateCountsBadge;
window.applyStructureConfirmed = applyStructureConfirmed;
