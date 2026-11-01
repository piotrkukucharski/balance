function updateSelectionBar() {
    const checked = Array.from(document.querySelectorAll('.party-select-cb:checked'));
    const bar = document.getElementById('selection_bar');
    const countLabel = document.getElementById('selected_count');
    const mergeBtn = document.getElementById('merge_btn');

    if (checked.length === 0) {
        bar.classList.add('hidden');
    } else {
        bar.classList.remove('hidden');
        countLabel.innerText = checked.length;
        mergeBtn.disabled = checked.length < 2;
    }
}

function toggleSelectAll(masterCb) {
    document.querySelectorAll('.party-select-cb').forEach(cb => {
        cb.checked = masterCb.checked;
    });
    updateSelectionBar();
}

function clearSelection() {
    document.querySelectorAll('.party-select-cb').forEach(cb => cb.checked = false);
    const master = document.getElementById('select_all_cb');
    if (master) master.checked = false;
    updateSelectionBar();
}

function openRenameModal(partyId, currentName) {
    const form = document.getElementById('rename_form');
    form.action = '/budgets/' + window.ACTIVE_BUDGET_ID + '/parties/' + partyId + '/rename';
    document.getElementById('rename_name_input').value = currentName;
    rename_party_modal.showModal();
}

function openMergeModal() {
    const checked = Array.from(document.querySelectorAll('.party-select-cb:checked'));
    if (checked.length < 2) return;

    const hiddenContainer = document.getElementById('merge_hidden_inputs');
    hiddenContainer.innerHTML = '';
    const targetSelect = document.getElementById('merge_target_party_id');
    targetSelect.innerHTML = '';

    checked.forEach((cb, idx) => {
        const pId = cb.value;
        const pName = cb.getAttribute('data-party-name') || pId;

        const input = document.createElement('input');
        input.type = 'hidden';
        input.name = 'source_party_ids';
        input.value = pId;
        hiddenContainer.appendChild(input);

        const opt = document.createElement('option');
        opt.value = pId;
        opt.text = pName;
        if (idx === 0) {
            opt.selected = true;
            document.getElementById('merge_final_name').value = pName;
        }
        targetSelect.appendChild(opt);
    });

    document.getElementById('merge_count_label').innerText = checked.length + ' counterparties selected';
    merge_parties_modal.showModal();
}

function handleTargetPartyChange() {
    const sel = document.getElementById('merge_target_party_id');
    const text = sel.options[sel.selectedIndex].text;
    document.getElementById('merge_final_name').value = text;
}

window.updateSelectionBar = updateSelectionBar;
window.toggleSelectAll = toggleSelectAll;
window.clearSelection = clearSelection;
window.openRenameModal = openRenameModal;
window.openMergeModal = openMergeModal;
window.handleTargetPartyChange = handleTargetPartyChange;
