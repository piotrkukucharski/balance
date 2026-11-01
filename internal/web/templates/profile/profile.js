function updateLivePreview() {
    const dFormatEl = document.getElementById('pref_date_format');
    const tFormatEl = document.getElementById('pref_time_format');
    if (!dFormatEl || !tFormatEl) return;

    const dFormat = dFormatEl.value;
    const tFormat = tFormatEl.value;
    const now = new Date();

    const year = now.getFullYear();
    const month = String(now.getMonth() + 1).padStart(2, '0');
    const day = String(now.getDate()).padStart(2, '0');
    let hours = now.getHours();
    const minutes = String(now.getMinutes()).padStart(2, '0');
    const seconds = String(now.getSeconds()).padStart(2, '0');

    let dStr = '';
    switch(dFormat) {
        case 'DD.MM.YYYY': dStr = day + '.' + month + '.' + year; break;
        case 'DD/MM/YYYY': dStr = day + '/' + month + '/' + year; break;
        case 'MM/DD/YYYY': dStr = month + '/' + day + '/' + year; break;
        default: dStr = year + '-' + month + '-' + day;
    }

    let tStr = '';
    if (tFormat === '12h') {
        const ampm = hours >= 12 ? 'PM' : 'AM';
        const h12 = hours % 12 || 12;
        const hStr = String(h12).padStart(2, '0');
        tStr = hStr + ':' + minutes + ':' + seconds + ' ' + ampm;
    } else {
        tStr = String(hours).padStart(2, '0') + ':' + minutes + ':' + seconds;
    }

    const previewDateEl = document.getElementById('preview_date');
    const previewTimeEl = document.getElementById('preview_time');
    if (previewDateEl) previewDateEl.textContent = dStr;
    if (previewTimeEl) previewTimeEl.textContent = dStr + ' ' + tStr;
}

window.updateLivePreview = updateLivePreview;
updateLivePreview();
