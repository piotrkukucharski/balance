function getSelectedDryRunTxIDs() {
    const checked = document.querySelectorAll('.dry-run-tx-cb:checked');
    return Array.from(checked).map(cb => cb.value);
}

function updateDryRunSelectedCount() {
    const count = getSelectedDryRunTxIDs().length;
    const el = document.getElementById("dry_run_selected_count");
    if (el) el.innerText = count + " transaction" + (count === 1 ? "" : "s") + " selected";
    const badge = document.getElementById("ai_few_shot_badge");
    if (badge) {
        if (count > 0) {
            badge.innerText = "ℹ️ " + count + " transaction(s) will be included as Few-Shot AI context";
            badge.classList.remove("hidden");
        } else {
            badge.classList.add("hidden");
        }
    }
}

function toggleSelectAllDryRun(select) {
    document.querySelectorAll('.dry-run-tx-cb').forEach(cb => cb.checked = select);
    updateDryRunSelectedCount();
}

document.addEventListener('change', function(e) {
    if (e.target && e.target.classList.contains('dry-run-tx-cb')) {
        updateDryRunSelectedCount();
    }
});

function duplicateAutomation(auto) {
    document.getElementById("modal_title").innerHTML = "<span>⚡</span> <span>New Automation (Copy)</span>";
    document.getElementById("form_auto_id").value = "";
    document.getElementById("form_name").value = "Kopia " + (auto.name || "");
    document.getElementById("form_judge_script").value = auto.judge_script || "";
    document.getElementById("form_executor_script").value = auto.executor_script || "";
    document.getElementById("form_comment").value = "Cloned from " + (auto.name || "existing rule");
    document.getElementById("squash_container").classList.add("hidden");
    document.getElementById("form_squash").checked = false;
    document.getElementById("modal_history_btn").classList.add("hidden");
    document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select one or more transactions and click Run Dry-Run to test Judge & Executor.</div>';

    const form = document.getElementById("automation_form");
    form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations");
    htmx.process(form);

    document.getElementById("automation_modal").showModal();
}

function openNewAutomationModal() {
    document.getElementById("modal_title").innerHTML = "<span>⚡</span> <span>New Automation</span>";
    document.getElementById("form_auto_id").value = "";
    document.getElementById("form_name").value = "";
    document.getElementById("form_judge_script").value = "";
    document.getElementById("form_executor_script").value = "";
    document.getElementById("form_comment").value = "";
    document.getElementById("ai_pair_prompt").value = "";
    document.getElementById("squash_container").classList.add("hidden");
    document.getElementById("form_squash").checked = false;
    document.getElementById("modal_history_btn").classList.add("hidden");
    document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select one or more transactions and click Run Dry-Run to test Judge & Executor.</div>';
    toggleSelectAllDryRun(false);

    const form = document.getElementById("automation_form");
    form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations");
    htmx.process(form);

    document.getElementById("automation_modal").showModal();
}

function openEditAutomationModal(auto) {
    document.getElementById("modal_title").innerHTML = "<span>✏️</span> <span>Edit Automation (v" + auto.version + ")</span>";
    document.getElementById("form_auto_id").value = auto.id;
    document.getElementById("form_name").value = auto.name;
    document.getElementById("form_judge_script").value = auto.judge_script;
    document.getElementById("form_executor_script").value = auto.executor_script;
    document.getElementById("form_comment").value = "";
    document.getElementById("ai_pair_prompt").value = "";
    document.getElementById("modal_history_btn").classList.remove("hidden");
    document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/50 italic">Select one or more transactions and click Run Dry-Run to test Judge & Executor.</div>';
    toggleSelectAllDryRun(false);

    // Squash is enabled only for versions > 1 (per /grill-me decision)
    if (auto.version > 1) {
        document.getElementById("squash_container").classList.remove("hidden");
        document.getElementById("form_squash").checked = false;
    } else {
        document.getElementById("squash_container").classList.add("hidden");
        document.getElementById("form_squash").checked = false;
    }

    const form = document.getElementById("automation_form");
    form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations/" + auto.id + "/update");
    htmx.process(form);

    document.getElementById("automation_modal").showModal();
}

function openHistoryFromModal() {
    const id = document.getElementById("form_auto_id").value;
    if (id) {
        htmx.ajax('GET', '/budgets/' + budgetID + '/automations/' + id + '/history', {target: 'body', swap: 'beforeend'});
    }
}

function loadHistoricalVersionIntoEditor(bID, aID, ver) {
    fetch('/budgets/' + bID + '/automations/' + aID + '/version/' + ver + '/load')
        .then(r => {
            if (!r.ok) throw new Error("Failed loading version " + ver);
            return r.json();
        })
        .then(data => {
            document.getElementById("form_auto_id").value = data.id;
            document.getElementById("form_name").value = data.name;
            document.getElementById("form_judge_script").value = data.judge_script;
            document.getElementById("form_executor_script").value = data.executor_script;
            document.getElementById("form_comment").value = "Based on v" + ver;
            document.getElementById("modal_title").innerHTML = "<span>✏️</span> <span>Edit Automation (loaded v" + ver + ")</span>";
            document.getElementById("squash_container").classList.remove("hidden");
            document.getElementById("form_squash").checked = false;
            document.getElementById("modal_history_btn").classList.remove("hidden");

            const form = document.getElementById("automation_form");
            form.setAttribute("hx-post", "/budgets/" + bID + "/automations/" + data.id + "/update");
            htmx.process(form);

            document.getElementById("history_drawer_backdrop")?.remove();
            document.getElementById("automation_modal").showModal();
        })
        .catch(err => alert(err.message));
}

function closeAutomationModal() {
    document.getElementById("automation_modal").close();
}

document.body.addEventListener("closeAutomationModal", function() {
    closeAutomationModal();
});

function openDeleteAutomationModal(id, name) {
    document.getElementById("delete_auto_name").innerText = name;
    const form = document.getElementById("delete_form");
    form.setAttribute("hx-post", "/budgets/" + budgetID + "/automations/" + id + "/delete");
    htmx.process(form);
    document.getElementById("delete_modal").showModal();
}

async function generatePairWithAI() {
    const prompt = document.getElementById("ai_pair_prompt").value.trim();
    if (!prompt) return;

    const btn = document.getElementById("ai_generate_btn");
    if (btn) btn.classList.add("loading");

    const formData = new FormData();
    formData.append("mode", "pair");
    formData.append("prompt", prompt);

    // Pass existing scripts if present
    const existingJudge = document.getElementById("form_judge_script").value.trim();
    const existingExecutor = document.getElementById("form_executor_script").value.trim();
    if (existingJudge) formData.append("existing_judge", existingJudge);
    if (existingExecutor) formData.append("existing_executor", existingExecutor);

    // Pass selected transactions as context
    const selectedTxIDs = getSelectedDryRunTxIDs();
    if (selectedTxIDs.length > 0) {
        formData.append("tx_ids", selectedTxIDs.join(","));
    }

    // Pass dry-run test results if available
    const resultsEl = document.getElementById("dry_run_results");
    if (resultsEl && resultsEl.innerText.trim() && !resultsEl.innerText.includes("Select one or more transactions")) {
        formData.append("test_results", resultsEl.innerText.trim());
    }

    try {
        const res = await fetch("/budgets/" + budgetID + "/automations/ai/generate", {
            method: "POST",
            headers: { "X-CSRF-Token": csrfToken },
            body: formData
        });
        if (res.ok) {
            const data = await res.json();
            if (data.judge_script) document.getElementById("form_judge_script").value = data.judge_script;
            if (data.executor_script) document.getElementById("form_executor_script").value = data.executor_script;
            if (!document.getElementById("form_name").value) {
                document.getElementById("form_name").value = prompt.length > 50 ? prompt.substring(0, 47) + "..." : prompt;
            }
        } else {
            const errText = await res.text();
            alert("AI Generation failed: " + errText);
        }
    } catch (e) {
        console.error("AI Pair error:", e);
        alert("AI Pair error: " + e.message);
    } finally {
        if (btn) btn.classList.remove("loading");
    }
}

async function generateJudgeWithAI() {
    const prompt = promptUserAI("Enter trigger condition (e.g. When transaction notes mention Lidl):");
    if (!prompt) return;
    const formData = new FormData();
    formData.append("mode", "judge");
    formData.append("prompt", prompt);

    const existingJudge = document.getElementById("form_judge_script").value.trim();
    if (existingJudge) formData.append("existing_judge", existingJudge);

    const selectedTxIDs = getSelectedDryRunTxIDs();
    if (selectedTxIDs.length > 0) {
        formData.append("tx_ids", selectedTxIDs.join(","));
    }

    const res = await fetch("/budgets/" + budgetID + "/automations/ai/generate", {
        method: "POST",
        headers: { "X-CSRF-Token": csrfToken },
        body: formData
    });
    if (res.ok) {
        document.getElementById("form_judge_script").value = await res.text();
    }
}

async function generateExecutorWithAI() {
    const prompt = promptUserAI("Enter action to perform (e.g. Set bucket to Groceries):");
    if (!prompt) return;
    const formData = new FormData();
    formData.append("mode", "executor");
    formData.append("prompt", prompt);

    const existingExecutor = document.getElementById("form_executor_script").value.trim();
    if (existingExecutor) formData.append("existing_executor", existingExecutor);

    const selectedTxIDs = getSelectedDryRunTxIDs();
    if (selectedTxIDs.length > 0) {
        formData.append("tx_ids", selectedTxIDs.join(","));
    }

    const res = await fetch("/budgets/" + budgetID + "/automations/ai/generate", {
        method: "POST",
        headers: { "X-CSRF-Token": csrfToken },
        body: formData
    });
    if (res.ok) {
        document.getElementById("form_executor_script").value = await res.text();
    }
}

async function generateCommentWithAI() {
    const formData = new FormData();
    formData.append("name", document.getElementById("form_name").value);
    formData.append("new_judge_script", document.getElementById("form_judge_script").value);
    formData.append("new_executor_script", document.getElementById("form_executor_script").value);

    const res = await fetch("/budgets/" + budgetID + "/automations/ai/comment", {
        method: "POST",
        headers: { "X-CSRF-Token": csrfToken },
        body: formData
    });
    if (res.ok) {
        document.getElementById("form_comment").value = await res.text();
    }
}

function promptUserAI(msg) {
    return window.prompt(msg);
}

function runDryRunTest() {
    const txIDs = getSelectedDryRunTxIDs();
    const judgeScript = document.getElementById("form_judge_script").value;
    const executorScript = document.getElementById("form_executor_script").value;
    if (txIDs.length === 0) {
        alert("Please select at least one transaction from the list above to test against.");
        return;
    }

    const formData = new FormData();
    txIDs.forEach(id => formData.append("target_tx_ids", id));
    formData.append("judge_script", judgeScript);
    formData.append("executor_script", executorScript);

    document.getElementById("dry_run_results").innerHTML = '<div class="text-[11px] text-base-content/60 flex items-center gap-1"><span class="loading loading-spinner loading-xs"></span> Evaluating rules against selected transactions...</div>';

    fetch("/budgets/" + budgetID + "/automations/dry-run", {
        method: "POST",
        headers: { "X-CSRF-Token": csrfToken },
        body: formData
    })
    .then(res => res.text())
    .then(html => {
        document.getElementById("dry_run_results").innerHTML = html;
    })
    .catch(err => {
        document.getElementById("dry_run_results").innerHTML = '<div class="alert alert-error py-1.5 px-3 text-xs">Error running dry run: ' + err.message + '</div>';
    });
}

window.getSelectedDryRunTxIDs = getSelectedDryRunTxIDs;
window.updateDryRunSelectedCount = updateDryRunSelectedCount;
window.toggleSelectAllDryRun = toggleSelectAllDryRun;
window.duplicateAutomation = duplicateAutomation;
window.openNewAutomationModal = openNewAutomationModal;
window.openEditAutomationModal = openEditAutomationModal;
window.openHistoryFromModal = openHistoryFromModal;
window.loadHistoricalVersionIntoEditor = loadHistoricalVersionIntoEditor;
window.closeAutomationModal = closeAutomationModal;
window.openDeleteAutomationModal = openDeleteAutomationModal;
window.generatePairWithAI = generatePairWithAI;
window.generateJudgeWithAI = generateJudgeWithAI;
window.generateExecutorWithAI = generateExecutorWithAI;
window.generateCommentWithAI = generateCommentWithAI;
window.promptUserAI = promptUserAI;
window.runDryRunTest = runDryRunTest;
