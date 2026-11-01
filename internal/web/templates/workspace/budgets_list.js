function openEditBudgetModal(budgetId, budgetName) {
    const form = document.getElementById('edit_budget_form');
    if (form) form.action = '/budgets/' + budgetId + '/update';
    const nameInput = document.getElementById('edit_budget_name');
    if (nameInput) nameInput.value = budgetName;
    const modal = document.getElementById('edit_budget_modal');
    if (modal) modal.showModal();
}

function openCloneBudgetModal(budgetId, budgetName) {
    const form = document.getElementById('clone_budget_form');
    if (form) form.action = '/budgets/' + budgetId + '/clone';
    const nameInput = document.getElementById('clone_budget_name');
    if (nameInput) nameInput.value = budgetName + ' (Copy)';
    const members = document.getElementById('clone_include_members');
    if (members) members.checked = true;
    const accounts = document.getElementById('clone_include_accounts');
    if (accounts) accounts.checked = true;
    const buckets = document.getElementById('clone_include_buckets');
    if (buckets) buckets.checked = true;
    const targets = document.getElementById('clone_include_targets');
    if (targets) targets.checked = true;
    const txs = document.getElementById('clone_include_transactions');
    if (txs) txs.checked = true;
    syncCloneDependencies();
    const modal = document.getElementById('clone_budget_modal');
    if (modal) modal.showModal();
}

function syncCloneDependencies() {
    const accounts = document.getElementById('clone_include_accounts');
    const buckets = document.getElementById('clone_include_buckets');
    const targets = document.getElementById('clone_include_targets');
    const txs = document.getElementById('clone_include_transactions');
    if (!accounts || !buckets || !targets || !txs) return;

    // If user explicitly checks transactions, accounts and buckets must be checked
    if (txs.checked) {
        accounts.checked = true;
        buckets.checked = true;
    }
    // If user explicitly checks targets, buckets must be checked
    if (targets.checked) {
        buckets.checked = true;
    }
    // If buckets is unchecked, uncheck targets and transactions
    if (!buckets.checked) {
        targets.checked = false;
        txs.checked = false;
    }
    // If accounts is unchecked, uncheck transactions
    if (!accounts.checked) {
        txs.checked = false;
    }
}

function openNewBudgetModal(mode) {
    if (mode === 'ai') {
        const aiRadio = document.getElementById('creation_mode_ai');
        if (aiRadio) { aiRadio.checked = true; toggleCreationMode('ai'); }
    } else if (mode === 'copy') {
        const copyRadio = document.getElementById('creation_mode_copy');
        if (copyRadio && !copyRadio.disabled) { copyRadio.checked = true; toggleCreationMode('copy'); }
    } else {
        const emptyRadio = document.querySelector('input[name="creation_mode"][value="empty"]');
        if (emptyRadio) { emptyRadio.checked = true; toggleCreationMode('empty'); }
    }
    const modal = document.getElementById('new_budget_modal');
    if (modal) modal.showModal();
}

function toggleCreationMode(mode) {
    const aiCont = document.getElementById('ai_prompt_container');
    const copyCont = document.getElementById('copy_budget_container');
    if (aiCont) aiCont.classList.toggle('hidden', mode !== 'ai');
    if (copyCont) copyCont.classList.toggle('hidden', mode !== 'copy');
}

window.openEditBudgetModal = openEditBudgetModal;
window.openCloneBudgetModal = openCloneBudgetModal;
window.syncCloneDependencies = syncCloneDependencies;
window.openNewBudgetModal = openNewBudgetModal;
window.toggleCreationMode = toggleCreationMode;
