package handlers

import (
	"fmt"
	"time"

	"github.com/balance/balance/internal/web/templates"
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

// GetUserLocale extracts the preferred locale for the current request context.
func GetUserLocale(c echo.Context) string {
	if loc, ok := c.Get(ContextLocaleKey).(string); ok && loc != "" {
		return loc
	}
	return "en-US"
}

// GetUserNumberFormat extracts the preferred number format for the current request context.
func GetUserNumberFormat(c echo.Context) string {
	if nf, ok := c.Get(ContextNumberFormatKey).(string); ok && nf != "" {
		return nf
	}
	return "auto"
}

type timeTagTemplateData struct {
	ISOStr        string
	DateFormat    string
	TimeFormat    string
	FormattedText string
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

	rendered, err := templates.RenderString("shared/time_tag", timeTagTemplateData{
		ISOStr:        isoStr,
		DateFormat:    dFormat,
		TimeFormat:    tFormat,
		FormattedText: formattedText,
	})
	if err != nil {
		return ""
	}
	return rendered
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

// DateTimeFormatterScript returns the client-side JavaScript tag that
// formats all <time datetime="..."> elements according to html data-date-format / data-time-format.
func DateTimeFormatterScript() string {
	return fmt.Sprintf(`<script src="%s" defer></script>`, templates.StaticPath("shared/datetime.js"))
}
