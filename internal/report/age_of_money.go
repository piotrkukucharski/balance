package report

import (
	"math"
	"sort"
)

// InflowBucket represents an external income slice in the FIFO queue.
type InflowBucket struct {
	TimestampMicro int64
	RemainingMinor int64
}

// OutflowRecord represents a liquid spending transaction from a user account to an external party.
type OutflowRecord struct {
	ID             string
	TimestampMicro int64
	AmountMinor    int64
}

// AgeOfMoneyResult contains the headline metric and historical time-series points.
type AgeOfMoneyResult struct {
	HeadlineDays int               `json:"headline_days"` // Average of last 10 liquid outflows
	History      []AgeOfMoneyPoint `json:"history"`
}

type AgeOfMoneyPoint struct {
	TimestampMicro int64 `json:"timestamp"`
	AgeDays        int   `json:"age_days"`
}

// CalculateAgeOfMoney processes external liquid inflows and outflows via FIFO queue.
// Outflows consume from oldest unspent income bucket(s).
// Age of an outflow at Toutflow is weighted average of (Toutflow - Tinflow) in days.
// Headline metric is the mean age across the last 10 outflows.
func CalculateAgeOfMoney(inflows []InflowBucket, outflows []OutflowRecord) (*AgeOfMoneyResult, error) {
	// Chronologically sort inflows and outflows
	sort.Slice(inflows, func(i, j int) bool {
		return inflows[i].TimestampMicro < inflows[j].TimestampMicro
	})
	sort.Slice(outflows, func(i, j int) bool {
		return outflows[i].TimestampMicro < outflows[j].TimestampMicro
	})

	fifo := make([]InflowBucket, len(inflows))
	copy(fifo, inflows)

	headIdx := 0
	var history []AgeOfMoneyPoint
	var last10Ages []float64

	const microsPerDay = int64(24 * 3600 * 1_000_000)

	for _, out := range outflows {
		remainingToDeplete := out.AmountMinor
		var weightedAgeSum float64
		var totalDepletedForTx int64

		for remainingToDeplete > 0 && headIdx < len(fifo) {
			bucket := &fifo[headIdx]
			if bucket.RemainingMinor <= 0 {
				headIdx++
				continue
			}

			consume := remainingToDeplete
			if consume > bucket.RemainingMinor {
				consume = bucket.RemainingMinor
			}

			// Calculate age in days: (out.Timestamp - bucket.Timestamp) / microsPerDay
			ageDays := float64(out.TimestampMicro-bucket.TimestampMicro) / float64(microsPerDay)
			if ageDays < 0 {
				ageDays = 0 // Inflow after outflow boundary edge-case
			}

			weightedAgeSum += float64(consume) * ageDays
			totalDepletedForTx += consume
			bucket.RemainingMinor -= consume
			remainingToDeplete -= consume

			if bucket.RemainingMinor <= 0 {
				headIdx++
			}
		}

		var txAgeDays float64
		if totalDepletedForTx > 0 {
			txAgeDays = weightedAgeSum / float64(totalDepletedForTx)
		} else {
			// If money was spent before any recorded income was in FIFO
			txAgeDays = 0
		}

		ptAge := int(math.Round(txAgeDays))
		history = append(history, AgeOfMoneyPoint{
			TimestampMicro: out.TimestampMicro,
			AgeDays:        ptAge,
		})

		last10Ages = append(last10Ages, txAgeDays)
		if len(last10Ages) > 10 {
			last10Ages = last10Ages[1:]
		}
	}

	headline := 0
	if len(last10Ages) > 0 {
		var sum float64
		for _, a := range last10Ages {
			sum += a
		}
		headline = int(math.Round(sum / float64(len(last10Ages))))
	}

	return &AgeOfMoneyResult{
		HeadlineDays: headline,
		History:      history,
	}, nil
}
