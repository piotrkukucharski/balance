package handlers

import (
	"fmt"
	"html"
	"time"

	"github.com/labstack/echo/v4"
)

// GetUserDateTimePreferences extracts the preferred date and time formats for the current request.
func GetUserDateTimePreferences(c echo.Context) (dateFormat, timeFormat string) {
	if df, ok := c.Get(ContextDateFormatKey).(string); ok && df != "" {
		dateFormat = df
	} else {
		dateFormat = "YYYY-MM-DD"
	}

	if tf, ok := c.Get(ContextTimeFormatKey).(string); ok && tf != "" {
		timeFormat = tf
	} else {
		timeFormat = "24h"
	}
	return dateFormat, timeFormat
}

// RenderTimeTag renders an HTML5 <time> tag containing standard ISO-8601/RFC3339 datetime
// and pre-formatted text according to user preferences, localized dynamically on client JS.
func RenderTimeTag(t time.Time, isDateTime bool, c echo.Context) string {
	dFormat, tFormat := GetUserDateTimePreferences(c)
	isoStr := t.UTC().Format(time.RFC3339)

	var formattedText string
	if isDateTime {
		formattedText = FormatCustomDateTime(t, dFormat, tFormat)
	} else {
		formattedText = FormatCustomDate(t, dFormat)
	}

	return fmt.Sprintf(`<time datetime="%s" data-format-date="%s" data-format-time="%s">%s</time>`,
		isoStr, html.EscapeString(dFormat), html.EscapeString(tFormat), html.EscapeString(formattedText))
}

// RenderTimeTagMicro accepts a microsecond epoch timestamp and renders a localized <time> tag.
func RenderTimeTagMicro(micro int64, isDateTime bool, c echo.Context) string {
	t := time.UnixMicro(micro)
	return RenderTimeTag(t, isDateTime, c)
}

// FormatCustomDate formats a time.Time using custom date format pattern.
func FormatCustomDate(t time.Time, dateFormat string) string {
	switch dateFormat {
	case "DD.MM.YYYY":
		return t.Format("02.01.2006")
	case "DD/MM/YYYY":
		return t.Format("02/01/2006")
	case "MM/DD/YYYY":
		return t.Format("01/02/2006")
	default: // "YYYY-MM-DD"
		return t.Format("2006-01-02")
	}
}

// FormatCustomDateTime formats a time.Time using custom date and time format patterns.
func FormatCustomDateTime(t time.Time, dateFormat, timeFormat string) string {
	dateStr := FormatCustomDate(t, dateFormat)
	var timeStr string
	if timeFormat == "12h" {
		timeStr = t.Format("03:04 PM")
	} else {
		timeStr = t.Format("15:04")
	}
	return dateStr + " " + timeStr
}

// DateTimeFormatterScript returns the client-side JavaScript snippet that
// formats all <time datetime="..."> elements according to html data-date-format / data-time-format.
func DateTimeFormatterScript() string {
	return `
<script>
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
</script>`
}
