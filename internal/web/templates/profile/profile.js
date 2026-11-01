function updateLivePreview() {
    const localeEl = document.getElementById('pref_locale');
    const dFormatEl = document.getElementById('pref_date_format');
    const tFormatEl = document.getElementById('pref_time_format');
    const nFormatEl = document.getElementById('pref_number_format');
    if (!dFormatEl || !tFormatEl) return;

    const locale = localeEl ? localeEl.value : 'en-US';
    let dFormat = dFormatEl.value;
    let tFormat = tFormatEl.value;
    let nFormat = nFormatEl ? nFormatEl.value : 'auto';

    // Resolve auto formats based on locale
    if (dFormat === 'auto') {
        dFormat = locale === 'pl-PL' ? 'DD.MM.YYYY' : 'YYYY-MM-DD';
    }
    if (tFormat === 'auto') {
        tFormat = '24h';
    }

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

    // Number & Currency preview (1234567.89)
    let numGrouping = ',';
    let numDecimal = '.';
    if (nFormat === 'auto') {
        if (locale === 'pl-PL') {
            numGrouping = ' ';
            numDecimal = ',';
        } else {
            numGrouping = ',';
            numDecimal = '.';
        }
    } else {
        switch(nFormat) {
            case '1.000,99': numGrouping = '.'; numDecimal = ','; break;
            case '1 000,99': numGrouping = ' '; numDecimal = ','; break;
            case '1 000.99': numGrouping = ' '; numDecimal = '.'; break;
            case '1_000.99': numGrouping = '_'; numDecimal = '.'; break;
            case '1000.99': numGrouping = ''; numDecimal = '.'; break;
            default: numGrouping = ','; numDecimal = '.';
        }
    }

    const majorParts = ['1', '234', '567'];
    const formattedNum = majorParts.join(numGrouping) + numDecimal + '89';
    const currencyStr = locale === 'pl-PL' ? formattedNum + ' PLN' : formattedNum + ' USD';

    const previewDateEl = document.getElementById('preview_date');
    const previewTimeEl = document.getElementById('preview_time');
    const previewNumberEl = document.getElementById('preview_number');
    const previewCurrencyEl = document.getElementById('preview_currency');

    if (previewDateEl) previewDateEl.textContent = dStr;
    if (previewTimeEl) previewTimeEl.textContent = dStr + ' ' + tStr;
    if (previewNumberEl) previewNumberEl.textContent = formattedNum;
    if (previewCurrencyEl) previewCurrencyEl.textContent = currencyStr;
}

window.updateLivePreview = updateLivePreview;
updateLivePreview();
