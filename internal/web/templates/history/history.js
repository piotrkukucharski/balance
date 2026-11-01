function handleSquashCheckChange() {
    const checks = Array.from(document.querySelectorAll('.squash-check:checked')).map(c => parseInt(c.value)).sort((a,b) => a - b);
    const badge = document.getElementById('squash_selection_badge');
    const btnAi = document.getElementById('btn_ai_squash_comment');
    const btnSubmit = document.getElementById('btn_submit_squash');
    const startInp = document.getElementById('squash_start_ver');
    const endInp = document.getElementById('squash_end_ver');

    if (checks.length < 2) {
        if (badge) {
            badge.innerText = 'Select 2+ versions';
            badge.className = 'badge badge-sm badge-neutral';
        }
        if (btnAi) btnAi.disabled = true;
        if (btnSubmit) btnSubmit.disabled = true;
        if (startInp) startInp.value = '';
        if (endInp) endInp.value = '';
        return;
    }

    const min = checks[0];
    const max = checks[checks.length - 1];
    const isContiguous = (max - min + 1) === checks.length;

    if (!isContiguous) {
        if (badge) {
            badge.innerText = '⚠️ Range must be contiguous';
            badge.className = 'badge badge-sm badge-warning';
        }
        if (btnAi) btnAi.disabled = true;
        if (btnSubmit) btnSubmit.disabled = true;
        if (startInp) startInp.value = '';
        if (endInp) endInp.value = '';
        return;
    }

    if (badge) {
        badge.innerText = 'v' + min + ' .. v' + max + ' (' + checks.length + ' versions)';
        badge.className = 'badge badge-sm badge-primary font-bold';
    }
    if (btnAi) btnAi.disabled = false;
    if (btnSubmit) btnSubmit.disabled = false;
    if (startInp) startInp.value = min;
    if (endInp) endInp.value = max;
}

function generateAISquashComment(bID, aID) {
    const startVer = document.getElementById('squash_start_ver').value;
    const endVer = document.getElementById('squash_end_ver').value;
    if (!startVer || !endVer) return;

    const btn = document.getElementById('btn_ai_squash_comment');
    const origText = btn.innerHTML;
    btn.innerHTML = '⏳ Generating...';
    btn.disabled = true;

    const formData = new FormData();
    formData.append('start_version', startVer);
    formData.append('end_version', endVer);

    const csrfToken = window.HISTORY_CSRF_TOKEN || (typeof csrfToken !== 'undefined' ? csrfToken : '');

    fetch('/budgets/' + bID + '/automations/' + aID + '/ai/squash-comment', {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrfToken },
        body: formData
    })
    .then(r => r.text())
    .then(comment => {
        document.getElementById('squash_comment_input').value = comment;
    })
    .catch(err => alert('AI comment error: ' + err))
    .finally(() => {
        btn.innerHTML = origText;
        btn.disabled = false;
    });
}

window.handleSquashCheckChange = handleSquashCheckChange;
window.generateAISquashComment = generateAISquashComment;
