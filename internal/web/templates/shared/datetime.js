(function() {
    function formatTimeElements(root) {
        const rootEl = root || document;
        const htmlEl = document.documentElement;
        const globalDateFormat = htmlEl.getAttribute('data-date-format') || 'YYYY-MM-DD';
        const globalTimeFormat = htmlEl.getAttribute('data-time-format') || '24h';

        rootEl.querySelectorAll('time[datetime]').forEach(el => {
            const dtAttr = el.getAttribute('datetime');
            if (!dtAttr) return;
            const date = new Date(dtAttr);
            if (isNaN(date.getTime())) return;

            const dFormat = el.getAttribute('data-format-date') || globalDateFormat;
            const tFormat = el.getAttribute('data-format-time') || globalTimeFormat;
            const hasTime = el.getAttribute('data-has-time') !== 'false' && (dtAttr.includes('T') || dtAttr.includes(':'));

            const year = date.getFullYear();
            const month = String(date.getMonth() + 1).padStart(2, '0');
            const day = String(date.getDate()).padStart(2, '0');
            let hours = date.getHours();
            const minutes = String(date.getMinutes()).padStart(2, '0');
            const seconds = String(date.getSeconds()).padStart(2, '0');

            let dStr = '';
            switch(dFormat) {
                case 'DD.MM.YYYY': dStr = day + '.' + month + '.' + year; break;
                case 'DD/MM/YYYY': dStr = day + '/' + month + '/' + year; break;
                case 'MM/DD/YYYY': dStr = month + '/' + day + '/' + year; break;
                default: dStr = year + '-' + month + '-' + day;
            }

            if (!hasTime) {
                el.textContent = dStr;
                return;
            }

            let tStr = '';
            if (tFormat === '12h') {
                const ampm = hours >= 12 ? 'PM' : 'AM';
                const h12 = hours % 12 || 12;
                const hStr = String(h12).padStart(2, '0');
                tStr = hStr + ':' + minutes + ' ' + ampm;
            } else {
                tStr = String(hours).padStart(2, '0') + ':' + minutes;
            }

            el.textContent = dStr + ' ' + tStr;
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', () => formatTimeElements());
    } else {
        formatTimeElements();
    }

    // Support HTMX content injection
    document.addEventListener('htmx:afterSwap', function(evt) {
        formatTimeElements(evt.detail.target);
    });
})();
