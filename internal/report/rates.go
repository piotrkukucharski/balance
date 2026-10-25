package report

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/fx"
)

type rateCacheKey struct {
	from string
	to   string
	day  int64
}

type cachedRateEntry struct {
	rateMicros int64
	found      bool
}

// ExchangeRateMap provides currency conversions to a target currency using historical or latest rates.
type ExchangeRateMap struct {
	ctx            context.Context
	targetCurrency string
	// map of baseCurrency -> latest rate_micros towards targetCurrency
	latestRates map[string]int64
	fxService   *fx.Service
	hasAnyRates bool
	rateCache   map[rateCacheKey]cachedRateEntry
}

// LoadExchangeRates loads available exchange rates from mainDB where target_currency = targetCurrency.
// If mainDB is nil or has no rates, conversion defaults to 1:1 (rateMicros = 1,000,000).
func LoadExchangeRates(ctx context.Context, mainDB *sql.DB, targetCurrency string) (*ExchangeRateMap, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if targetCurrency == "" {
		targetCurrency = "PLN"
	}

	erm := &ExchangeRateMap{
		ctx:            ctx,
		targetCurrency: targetCurrency,
		latestRates:    make(map[string]int64),
		rateCache:      make(map[rateCacheKey]cachedRateEntry),
	}

	if mainDB == nil {
		return erm, nil
	}
	erm.fxService = fx.NewService(mainDB, nil, nil)

	rows, err := mainDB.QueryContext(ctx, `
		SELECT base_currency, rate_micros
		FROM currency_exchange_rates
		WHERE target_currency = ?
		ORDER BY rate_date DESC`, targetCurrency)
	if err != nil {
		// Table might not exist in some unit test setups, graceful degradation
		return erm, nil
	}

	for rows.Next() {
		var baseCurr string
		var rateMicros int64
		if err := rows.Scan(&baseCurr, &rateMicros); err != nil {
			rows.Close()
			return nil, err
		}
		if _, exists := erm.latestRates[baseCurr]; !exists {
			erm.latestRates[baseCurr] = rateMicros
		}
	}
	rows.Close()

	if len(erm.latestRates) > 0 {
		erm.hasAnyRates = true
	} else {
		var exists int
		if err := mainDB.QueryRowContext(ctx, `SELECT 1 FROM currency_exchange_rates LIMIT 1`).Scan(&exists); err == nil && exists == 1 {
			erm.hasAnyRates = true
		}
	}

	return erm, nil
}

// ConvertTo converts amountMinor from fromCurr to toCurr. If fromCurr == toCurr, amount is returned directly.
// If fromCurr != toCurr, uses fxService with historical linear interpolation if available; otherwise falls back to latest rate or 1:1.
func (erm *ExchangeRateMap) ConvertTo(amountMinor int64, fromCurr, toCurr string, dateMicros int64) (int64, error) {
	fromCurr = strings.ToUpper(strings.TrimSpace(fromCurr))
	toCurr = strings.ToUpper(strings.TrimSpace(toCurr))

	if fromCurr == "" || toCurr == "" || fromCurr == toCurr || amountMinor == 0 {
		return amountMinor, nil
	}

	if erm.fxService != nil && erm.hasAnyRates && dateMicros > 0 {
		dayKey := fx.DayBoundaryMicros(time.UnixMicro(dateMicros))
		key := rateCacheKey{from: fromCurr, to: toCurr, day: dayKey}

		if entry, ok := erm.rateCache[key]; ok {
			if entry.found {
				return domain.ConvertCurrencySafe(amountMinor, entry.rateMicros)
			}
		} else {
			reqCtx := erm.ctx
			if reqCtx == nil {
				reqCtx = context.Background()
			}
			rateMicros, _, err := erm.fxService.GetRate(reqCtx, fromCurr, toCurr, dateMicros)
			if err == nil && rateMicros > 0 {
				erm.rateCache[key] = cachedRateEntry{rateMicros: rateMicros, found: true}
				return domain.ConvertCurrencySafe(amountMinor, rateMicros)
			}
			erm.rateCache[key] = cachedRateEntry{found: false}
		}
	}

	rateMicros, ok := erm.latestRates[fromCurr]
	if !ok || rateMicros <= 0 {
		// Default to 1:1
		rateMicros = domain.MicroUnitMultiplier
	}

	return domain.ConvertCurrencySafe(amountMinor, rateMicros)
}
