let proposedRules = [];

function openCreateGlobalRuleModal() {
    document.getElementById('rule_modal_title').innerText = "Create Auto-Matching Rule";
    document.getElementById('rule_submit_btn').innerText = "Create Rule";
    const partySel = document.getElementById('rule_party_id');
    partySel.disabled = false;
    const pId = partySel.value;
    const form = document.getElementById('rule_modal_form');
    form.action = '/budgets/' + window.budgetID + '/parties/' + pId + '/rules?redirect=/budgets/' + window.budgetID + '/parties/rules';
    
    document.getElementById('rule_name').value = '';
    document.getElementById('rule_match_field').value = 'RAW_PAYEE';
    document.getElementById('rule_match_operator').value = 'CONTAINS';
    document.getElementById('rule_match_pattern').value = '';
    document.getElementById('rule_default_bucket_id').value = '';
    document.getElementById('rule_priority').value = '100';
    
    partySel.onchange = function() {
        form.action = '/budgets/' + window.budgetID + '/parties/' + partySel.value + '/rules?redirect=/budgets/' + window.budgetID + '/parties/rules';
    };

    rule_modal.showModal();
}

function openCreateRuleModal() {
    document.getElementById('rule_modal_title').innerText = "Create Auto-Matching Rule";
    document.getElementById('rule_submit_btn').innerText = "Create Rule";
    const form = document.getElementById('rule_modal_form');
    form.action = '/budgets/' + window.budgetID + '/parties/' + window.partyID + '/rules';
    
    document.getElementById('rule_name').value = '';
    document.getElementById('rule_match_field').value = 'RAW_PAYEE';
    document.getElementById('rule_match_operator').value = 'CONTAINS';
    document.getElementById('rule_match_pattern').value = '';
    document.getElementById('rule_default_bucket_id').value = '';
    document.getElementById('rule_priority').value = '100';

    rule_modal.showModal();
}

function openEditRuleModal(rule) {
    document.getElementById('rule_modal_title').innerText = "Edit Auto-Matching Rule";
    document.getElementById('rule_submit_btn').innerText = "Save Changes";
    
    const partySel = document.getElementById('rule_party_id');
    const form = document.getElementById('rule_modal_form');
    if (partySel) {
        partySel.value = rule.party_id;
        partySel.disabled = true;
        form.action = '/budgets/' + window.budgetID + '/parties/' + rule.party_id + '/rules/' + rule.id + '/update?redirect=/budgets/' + window.budgetID + '/parties/rules';
    } else {
        form.action = '/budgets/' + window.budgetID + '/parties/' + window.partyID + '/rules/' + rule.id + '/update';
    }

    document.getElementById('rule_name').value = rule.name;
    document.getElementById('rule_match_field').value = rule.match_field;
    document.getElementById('rule_match_operator').value = rule.match_operator;
    document.getElementById('rule_match_pattern').value = rule.match_pattern;
    document.getElementById('rule_default_bucket_id').value = rule.bucket_id || '';
    document.getElementById('rule_priority').value = rule.priority;

    rule_modal.showModal();
}

function openSmartRuleModal() {
    smart_rule_modal.showModal();
    document.getElementById('smart_loading').classList.remove('hidden');
    document.getElementById('smart_content').classList.add('hidden');
    document.getElementById('test_results_banner').classList.add('hidden');

    fetch('/budgets/' + window.budgetID + '/parties/rules/smart-suggest')
        .then(r => r.json())
        .then(data => {
            document.getElementById('smart_loading').classList.add('hidden');
            document.getElementById('smart_content').classList.remove('hidden');

            proposedRules = data.proposed_rules || [];
            document.getElementById('smart_summary').innerText = 'Found ' + data.unassigned_count + ' unclassified transactions. AI proposed ' + proposedRules.length + ' rules.';
            renderProposedRules(proposedRules);
        })
        .catch(err => {
            document.getElementById('smart_loading').innerHTML = '<div class="alert alert-error text-xs">Failed generating smart rules: ' + err + '</div>';
        });
}

function renderProposedRules(rules) {
    const container = document.getElementById('smart_rules_container');
    container.innerHTML = '';
    if (rules.length === 0) {
        container.innerHTML = '<div class="text-center py-6 text-base-content/50">All incoming transactions already match existing rules! No new rules needed.</div>';
        return;
    }

    rules.forEach((r, idx) => {
        const card = document.createElement('div');
        card.className = "card bg-base-200/50 border border-base-300 p-4 rounded-xl flex flex-col gap-3";
        card.innerHTML = "<div class=\"flex items-center justify-between gap-2\">" +
            "<label class=\"flex items-center gap-2 cursor-pointer\">" +
                "<input type=\"checkbox\" name=\"selected_rules\" value=\"" + idx + "\" checked class=\"checkbox checkbox-primary checkbox-sm smart-rule-cb\" />" +
                "<span class=\"font-bold text-sm\">" + (r.name || "") + "</span>" +
            "</label>" +
            "<span class=\"badge badge-sm badge-secondary font-semibold\">" + Math.round((r.confidence || 0.8) * 100) + "% Confidence</span>" +
        "</div>" +
        "<div class=\"grid grid-cols-1 sm:grid-cols-3 gap-2\">" +
            "<input type=\"text\" name=\"rule_" + idx + "_name\" value=\"" + (r.name || "") + "\" class=\"input input-bordered input-sm w-full\" placeholder=\"Rule Name\" />" +
            "<input type=\"text\" name=\"rule_" + idx + "_target_party\" value=\"" + (r.target_party_name || "") + "\" class=\"input input-bordered input-sm w-full\" placeholder=\"Party Name\" />" +
            "<input type=\"text\" name=\"rule_" + idx + "_pattern\" value=\"" + (r.match_pattern || "") + "\" class=\"input input-bordered input-sm w-full font-mono\" placeholder=\"Pattern\" />" +
        "</div>" +
        "<div class=\"flex items-center gap-2 text-xs text-base-content/60\">" +
            "<span>Field: <b>" + (r.match_field || "") + "</b></span> &bull; " +
            "<span>Operator: <b>" + (r.match_operator || "") + "</b></span> &bull; " +
            "<span>Bucket: <b>" + (r.suggested_bucket || "None") + "</b></span>" +
        "</div>" +
        "<input type=\"hidden\" name=\"rule_" + idx + "_field\" value=\"" + (r.match_field || "") + "\" />" +
        "<input type=\"hidden\" name=\"rule_" + idx + "_operator\" value=\"" + (r.match_operator || "") + "\" />" +
        "<input type=\"hidden\" name=\"rule_" + idx + "_bucket\" value=\"" + (r.suggested_bucket || "") + "\" />";
        container.appendChild(card);
    });
}

function testProposedRules() {
    const banner = document.getElementById('test_results_banner');
    banner.className = "p-3 rounded-xl text-xs font-semibold bg-info/10 text-info border border-info/20";
    banner.innerText = "Simulating matching against unclassified transactions...";
    banner.classList.remove('hidden');

    const payload = {
        rules: proposedRules
    };

    fetch('/budgets/' + window.budgetID + '/parties/rules/smart-test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
    })
    .then(r => r.json())
    .then(res => {
        banner.className = "p-3 rounded-xl text-xs font-semibold bg-success/10 text-success border border-success/20";
        banner.innerText = "✓ Test Simulation: Rules will successfully auto-classify " + res.matched_count + " of " + res.total_unassigned + " raw transactions!";
    })
    .catch(err => {
        banner.className = "p-3 rounded-xl text-xs font-semibold bg-error/10 text-error border border-error/20";
        banner.innerText = "Test failed: " + err;
    });
}

window.openCreateGlobalRuleModal = openCreateGlobalRuleModal;
window.openCreateRuleModal = openCreateRuleModal;
window.openEditRuleModal = openEditRuleModal;
window.openSmartRuleModal = openSmartRuleModal;
window.renderProposedRules = renderProposedRules;
window.testProposedRules = testProposedRules;
