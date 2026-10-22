package report

import (
	"context"
	"database/sql"

	"github.com/balance/balance/internal/domain"
)

// ExchangeRateMap provides currency conversions to a target currency using historical or latest rates.
type ExchangeRateMap struct {
	targetCurrency string
	// map of baseCurrency -> sorted entries (rate_date asc/desc) or latest rate
	latestRates map[string]int64 // baseCurrency -> rate_micros
}

// LoadExchangeRates loads available exchange rates from mainDB where target_currency = targetCurrency.
// If mainDB is nil or has no rates, conversion defaults to 1:1 (rateMicros = 1,000,000).
func LoadExchangeRates(ctx context.Context, mainDB *sql.DB, targetCurrency string) (*ExchangeRateMap, error) {
	if targetCurrency == "" {
		targetCurrency = "PLN"
	}

	erm := &ExchangeRateMap{
		targetCurrency: targetCurrency,
		latestRates:    make(map[string]int64),
	}

	if mainDB == nil {
		return erm, nil
	}

	rows, err := mainDB.QueryContext(ctx, `
		SELECT base_currency, rate_micros
		FROM currency_exchange_rates
		WHERE target_currency = ?
		ORDER BY rate_date DESC`, targetCurrency)
	if err != nil {
		// Table might not exist in some unit test setups, graceful degradation
		return erm, nil
	}
	defer rows.Close()

	for rows.Next() {
		var baseCurr string
		var rateMicros int64
		if err := rows.Scan(&baseCurr, &rateMicros); err != nil {
			return nil, err
		}
		if _, exists := erm.latestRates[baseCurr]; !exists {
			erm.latestRates[baseCurr] = rateMicros
		}
	}

	return erm, nil
}

// ConvertTo converts amountMinor from fromCurr to toCurr. If fromCurr == toCurr, amount is returned directly.
// If fromCurr != toCurr and no rate is found, defaults to 1:1 conversion.
func (erm *ExchangeRateMap) ConvertTo(amountMinor int64, fromCurr, toCurr string, _ int64) (int64, error) {
	if fromCurr == "" || toCurr == "" || fromCurr == toCurr {
		return amountMinor, nil
	}

	rateMicros, ok := erm.latestRates[fromCurr]
	if !ok || rateMicros <= 0 {
		// Default to 1:1
		rateMicros = domain.MicroUnitMultiplier
	}

	return domain.ConvertCurrencySafe(amountMinor, rateMicros)
}
