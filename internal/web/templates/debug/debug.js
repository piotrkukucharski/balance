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
