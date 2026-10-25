package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/balance/balance/internal/logger"
	"github.com/balance/balance/internal/report"
	"github.com/labstack/echo/v4"
)

// getBudgetTimezone returns time.Location for budget, or time.UTC if not found or invalid.
func (h *ReportsHandler) getBudgetTimezone(ctx context.Context, budgetID string) *time.Location {
	var tzStr string
	err := h.mainDB.QueryRowContext(ctx, `SELECT timezone FROM budgets WHERE id = ?`, budgetID).Scan(&tzStr)
	if err == nil && tzStr != "" {
		if loc, err := time.LoadLocation(tzStr); err == nil {
			return loc
		}
	}
	return time.UTC
}

// getBudgetTodayDate returns date string "YYYY-MM-DD" for the budget's configured timezone.
func (h *ReportsHandler) getBudgetTodayDate(ctx context.Context, budgetID string) string {
	loc := h.getBudgetTimezone(ctx, budgetID)
	return time.Now().In(loc).Format("2006-01-02")
}

// buildReportFilterKey creates a deterministic JSON key from report filter parameters.
func buildReportFilterKey(c echo.Context, f report.ReportFilter) string {
	hasExplicitStart := c.QueryParam("start_date") != ""
	hasExplicitEnd := c.QueryParam("end_date") != ""

	m := map[string]any{
		"range":            f.Range,
		"currency":         f.TargetCurrency,
		"exclude_internal": f.ExcludeInternal,
	}
	if hasExplicitStart {
		m["start_date"] = f.StartDateMicros
	}
	if hasExplicitEnd {
		m["end_date"] = f.EndDateMicros
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// serveCachedReport handles daily caching and on-demand recalculation for report endpoints.
// If force=true or recalculate=true, or if cache is missing/expired (not from today),
// computeFunc is called and the fresh result is stored in report_cache.
func (h *ReportsHandler) serveCachedReport(
	c echo.Context,
	reportType string,
	filterKey string,
	computeFunc func() (any, error),
) error {
	ctx := c.Request().Context()
	budgetID := c.Param("budgetId")
	tenantDB, ok := c.Get(ContextBudgetDBKey).(*sql.DB)
	if !ok || tenantDB == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "budget database context missing"})
	}

	force := c.QueryParam("force") == "true" || c.QueryParam("recalculate") == "true"
	todayStr := h.getBudgetTodayDate(ctx, budgetID)
	cacheKey := fmt.Sprintf("%s:%s", reportType, filterKey)

	// Check cache if not forcing recalculation
	if !force {
		var dataJSON, calcDate string
		var calculatedAt int64
		err := tenantDB.QueryRowContext(ctx, `
			SELECT data_json, calc_date, calculated_at
			FROM report_cache
			WHERE cache_key = ?`, cacheKey).Scan(&dataJSON, &calcDate, &calculatedAt)

		if err == nil && calcDate == todayStr {
			var m map[string]any
			if err := json.Unmarshal([]byte(dataJSON), &m); err == nil {
				m["is_cached"] = true
				m["calculated_at"] = calculatedAt
				m["calc_date"] = calcDate

				c.Response().Header().Set("X-Report-Cached", "true")
				c.Response().Header().Set("X-Report-Calculated-At", strconv.FormatInt(calculatedAt, 10))
				c.Response().Header().Set("X-Report-Calc-Date", calcDate)

				return c.JSON(http.StatusOK, m)
			}
		}
	}

	// Calculate fresh data on miss or force recalculation
	res, err := computeFunc()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	dataBytes, err := json.Marshal(res)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode report data"})
	}

	var m map[string]any
	if err := json.Unmarshal(dataBytes, &m); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to decode report data"})
	}

	nowMicros := time.Now().UTC().UnixMicro()
	m["is_cached"] = false
	m["calculated_at"] = nowMicros
	m["calc_date"] = todayStr

	persistedBytes, err := json.Marshal(m)
	if err != nil {
		persistedBytes = dataBytes
	}

	// Persist to report_cache
	_, err = tenantDB.ExecContext(ctx, `
		INSERT INTO report_cache (cache_key, report_type, filter_json, calc_date, data_json, calculated_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			filter_json = excluded.filter_json,
			calc_date = excluded.calc_date,
			data_json = excluded.data_json,
			calculated_at = excluded.calculated_at,
			updated_at = excluded.updated_at`,
		cacheKey, reportType, filterKey, todayStr, string(persistedBytes), nowMicros, nowMicros,
	)
	if err != nil {
		logger.Error(ctx, "Failed to persist report cache", "report_type", reportType, "error", err.Error())
	}

	c.Response().Header().Set("X-Report-Cached", "false")
	c.Response().Header().Set("X-Report-Calculated-At", strconv.FormatInt(nowMicros, 10))
	c.Response().Header().Set("X-Report-Calc-Date", todayStr)

	return c.JSON(http.StatusOK, m)
}
