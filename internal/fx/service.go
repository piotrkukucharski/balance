package fx

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/balance/balance/internal/domain"
)

// Service manages exchange rate synchronization, storage, and conversions.
type Service struct {
	mainDB    *sql.DB
	ecbClient ECBClient
	nbpClient NBPClient
}

// NewService creates a new exchange rate service.
func NewService(mainDB *sql.DB, ecb ECBClient, nbp NBPClient) *Service {
	if ecb == nil {
		ecb = NewHTTPECBClient("", nil)
	}
	if nbp == nil {
		nbp = NewHTTPNBPClient("", nil)
	}
	return &Service{
		mainDB:    mainDB,
		ecbClient: ecb,
		nbpClient: nbp,
	}
}

// Sync synchronizes exchange rates from configured central banks into mainDB.
func (s *Service) Sync(ctx context.Context, opts SyncOptions) (int, error) {
	if s.mainDB == nil {
		return 0, nil
	}

	days := opts.Days
	if days <= 0 {
		var count int
		_ = s.mainDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM currency_exchange_rates").Scan(&count)
		if count == 0 {
			days = 90
		} else {
			days = 1
		}
	}

	sourceFilter := strings.ToUpper(strings.TrimSpace(opts.Source))
	if sourceFilter == "" {
		sourceFilter = "ALL"
	}

	var allQuotes []RateQuote

	// 1. Fetch from NBP if requested
	if sourceFilter == "ALL" || sourceFilter == "NBP" {
		nbpQuotes, err := s.nbpClient.FetchRates(ctx, days)
		if err == nil {
			allQuotes = append(allQuotes, nbpQuotes...)
		}
	}

	// 2. Fetch from ECB if requested (processed after NBP so ECB takes conflict precedence)
	if sourceFilter == "ALL" || sourceFilter == "ECB" {
		ecbQuotes, err := s.ecbClient.FetchRates(ctx, days)
		if err == nil {
			allQuotes = append(allQuotes, ecbQuotes...)
		}
	}

	if len(allQuotes) == 0 {
		return 0, nil
	}

	tx, err := s.mainDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed starting exchange rates transaction: %w", err)
	}
	defer tx.Rollback()

	// SQLite UPSERT: ECB strictly overwrites NBP on conflict; NBP does not overwrite ECB.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO currency_exchange_rates (
			id, base_currency, target_currency, rate_date, rate_micros, source, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(base_currency, target_currency, rate_date) DO UPDATE SET
			rate_micros = excluded.rate_micros,
			source = excluded.source
		WHERE excluded.source = 'ECB' OR currency_exchange_rates.source = 'NBP'`)
	if err != nil {
		return 0, fmt.Errorf("failed preparing upsert statement: %w", err)
	}
	defer stmt.Close()

	nowMicros := time.Now().UTC().UnixMicro()
	persistedCount := 0

	for _, q := range allQuotes {
		if q.BaseCurrency == "" || q.TargetCurrency == "" || q.RateMicros <= 0 || q.RateDate <= 0 {
			continue
		}
		id := domain.NewUUIDv7()
		res, err := stmt.ExecContext(ctx, id, q.BaseCurrency, q.TargetCurrency, q.RateDate, q.RateMicros, q.Source, nowMicros)
		if err != nil {
			continue
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			persistedCount++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed committing exchange rates: %w", err)
	}

	return persistedCount, nil
}

// Convert converts an amount in minor units from fromCurr to toCurr on the specified dateMicros.
func (s *Service) Convert(ctx context.Context, amountMinor int64, fromCurr, toCurr string, dateMicros int64) (int64, error) {
	fromCurr = strings.ToUpper(strings.TrimSpace(fromCurr))
	toCurr = strings.ToUpper(strings.TrimSpace(toCurr))

	if fromCurr == "" || toCurr == "" {
		return 0, ErrInvalidCurrency
	}
	if fromCurr == toCurr {
		return amountMinor, nil
	}
	if amountMinor == 0 {
		return 0, nil
	}

	rateMicros, _, err := s.GetRate(ctx, fromCurr, toCurr, dateMicros)
	if err != nil {
		return 0, err
	}

	return domain.ConvertCurrencySafe(amountMinor, rateMicros)
}

// GetRate finds or calculates the effective exchange rate for fromCurr -> toCurr at dateMicros.
func (s *Service) GetRate(ctx context.Context, fromCurr, toCurr string, dateMicros int64) (int64, string, error) {
	fromCurr = strings.ToUpper(strings.TrimSpace(fromCurr))
	toCurr = strings.ToUpper(strings.TrimSpace(toCurr))

	if fromCurr == "" || toCurr == "" {
		return 0, "", ErrInvalidCurrency
	}
	if fromCurr == toCurr {
		return domain.MicroUnitMultiplier, "IDENTITY", nil
	}
	if dateMicros <= 0 {
		dateMicros = time.Now().UTC().UnixMicro()
	}

	// 1. Direct pair resolution
	directRate, directSource, err := s.resolveDirectPair(ctx, fromCurr, toCurr, dateMicros)
	if err == nil {
		return directRate, directSource, nil
	}

	// 2. Inverse pair resolution (toCurr -> fromCurr)
	invRate, invSource, err := s.resolveDirectPair(ctx, toCurr, fromCurr, dateMicros)
	if err == nil {
		derivedRate := InvertRateMicros(invRate)
		return derivedRate, invSource, nil
	}

	// 3. Triangulation via EUR
	if fromCurr != "EUR" && toCurr != "EUR" {
		rateFromToEUR, src1, err1 := s.GetRate(ctx, fromCurr, "EUR", dateMicros)
		rateEURToTarget, src2, err2 := s.GetRate(ctx, "EUR", toCurr, dateMicros)
		if err1 == nil && err2 == nil {
			compositeRate, err := MultiplyRatesMicros(rateFromToEUR, rateEURToTarget)
			if err == nil {
				return compositeRate, src1 + "/" + src2, nil
			}
		}
	}

	// 4. Triangulation via PLN
	if fromCurr != "PLN" && toCurr != "PLN" {
		rateFromToPLN, src1, err1 := s.GetRate(ctx, fromCurr, "PLN", dateMicros)
		ratePLNToTarget, src2, err2 := s.GetRate(ctx, "PLN", toCurr, dateMicros)
		if err1 == nil && err2 == nil {
			compositeRate, err := MultiplyRatesMicros(rateFromToPLN, ratePLNToTarget)
			if err == nil {
				return compositeRate, src1 + "/" + src2, nil
			}
		}
	}

	return 0, "", ErrExchangeRateNotFound
}

// resolveDirectPair queries mainDB for the pair (base, target) using exact match, linear interpolation, or Friday fallback.
func (s *Service) resolveDirectPair(ctx context.Context, base, target string, dateMicros int64) (int64, string, error) {
	if s.mainDB == nil {
		return 0, "", ErrExchangeRateNotFound
	}

	targetDay := DayBoundaryMicros(time.UnixMicro(dateMicros))

	// Step 1: Check for exact match on targetDay
	// Ordered by source DESC so 'ECB' takes precedence over 'NBP'
	row := s.mainDB.QueryRowContext(ctx, `
		SELECT rate_micros, source
		FROM currency_exchange_rates
		WHERE base_currency = ? AND target_currency = ? AND rate_date = ?
		ORDER BY CASE source WHEN 'ECB' THEN 1 WHEN 'NBP' THEN 2 ELSE 3 END ASC
		LIMIT 1`, base, target, targetDay)

	var rateMicros int64
	var source string
	if err := row.Scan(&rateMicros, &source); err == nil {
		return rateMicros, source, nil
	}

	// Step 2: Query previous published day (T1 <= targetDay)
	var t1, r1 int64
	var src1 string
	errPrev := s.mainDB.QueryRowContext(ctx, `
		SELECT rate_date, rate_micros, source
		FROM currency_exchange_rates
		WHERE base_currency = ? AND target_currency = ? AND rate_date < ?
		ORDER BY rate_date DESC, CASE source WHEN 'ECB' THEN 1 WHEN 'NBP' THEN 2 ELSE 3 END ASC
		LIMIT 1`, base, target, targetDay).Scan(&t1, &r1, &src1)

	if errPrev != nil {
		// No previous date found -> date precedes available history
		return 0, "", ErrExchangeRateNotFound
	}

	// Step 3: Query next published day (T2 > targetDay)
	var t2, r2 int64
	var src2 string
	errNext := s.mainDB.QueryRowContext(ctx, `
		SELECT rate_date, rate_micros, source
		FROM currency_exchange_rates
		WHERE base_currency = ? AND target_currency = ? AND rate_date > ?
		ORDER BY rate_date ASC, CASE source WHEN 'ECB' THEN 1 WHEN 'NBP' THEN 2 ELSE 3 END ASC
		LIMIT 1`, base, target, targetDay).Scan(&t2, &r2, &src2)

	if errNext != nil {
		// Next published date does not exist yet (e.g. current in-progress weekend before Monday publication).
		// Fall back to latest known business day (Friday) rate.
		return r1, src1, nil
	}

	// Step 4: Both T1 and T2 exist -> perform linear interpolation
	interpolatedRate := InterpolateRate(r1, t1, r2, t2, targetDay)
	return interpolatedRate, src1, nil
}

// InvertRateMicros computes 10^12 / rateMicros using 128-bit integer division with round half up.
func InvertRateMicros(rateMicros int64) int64 {
	if rateMicros <= 0 {
		return 0
	}
	numerator := new(big.Int).Mul(big.NewInt(domain.MicroUnitMultiplier), big.NewInt(domain.MicroUnitMultiplier)) // 10^12
	denom := big.NewInt(rateMicros)
	halfDenom := new(big.Int).Rsh(denom, 1)

	sum := new(big.Int).Add(numerator, halfDenom)
	quo := new(big.Int).Quo(sum, denom)
	return quo.Int64()
}

// MultiplyRatesMicros multiplies two rates scaled by 10^6 and scales back by 10^6: (r1 * r2) / 10^6.
func MultiplyRatesMicros(r1, r2 int64) (int64, error) {
	b1 := big.NewInt(r1)
	b2 := big.NewInt(r2)
	mult := big.NewInt(domain.MicroUnitMultiplier)

	prod := new(big.Int).Mul(b1, b2)
	halfMult := new(big.Int).Rsh(mult, 1)
	sum := new(big.Int).Add(prod, halfMult)
	quo := new(big.Int).Quo(sum, mult)

	if !quo.IsInt64() {
		return 0, fmt.Errorf("composite rate exceeds int64")
	}
	return quo.Int64(), nil
}

// InterpolateRate performs linear interpolation between (t1, r1) and (t2, r2) at timestamp t.
func InterpolateRate(r1, t1, r2, t2, t int64) int64 {
	if t <= t1 {
		return r1
	}
	if t >= t2 {
		return r2
	}
	deltaT := big.NewInt(t - t1)
	diffR := big.NewInt(r2 - r1)
	spanT := big.NewInt(t2 - t1)

	prod := new(big.Int).Mul(deltaT, diffR)
	halfSpan := new(big.Int).Rsh(spanT, 1)

	var num *big.Int
	if prod.Sign() >= 0 {
		num = new(big.Int).Add(prod, halfSpan)
	} else {
		num = new(big.Int).Sub(prod, halfSpan)
	}

	deltaR := new(big.Int).Quo(num, spanT)
	return r1 + deltaR.Int64()
}
