package report

import (
	"context"
	"testing"

	"github.com/balance/balance/internal/logger"
)

func TestCalculateAgeOfMoney(t *testing.T) {
	// Inflow 1: Day 1, 1000 PLN (100,000 minor)
	// Outflow 1: Day 11, 400 PLN (40,000 minor) -> Age = 10 days
	// Outflow 2: Day 21, 600 PLN (60,000 minor) -> Age = 20 days
	microsPerDay := int64(24 * 3600 * 1_000_000)
	t0 := int64(1700000000000000)

	inflows := []InflowBucket{
		{TimestampMicro: t0, RemainingMinor: 100000},
	}
	outflows := []OutflowRecord{
		{ID: "o1", TimestampMicro: t0 + 10*microsPerDay, AmountMinor: 40000},
		{ID: "o2", TimestampMicro: t0 + 20*microsPerDay, AmountMinor: 60000},
	}

	res, err := CalculateAgeOfMoney(inflows, outflows)
	if err != nil {
		t.Fatalf("CalculateAgeOfMoney failed: %v", err)
	}

	if len(res.History) != 2 {
		t.Fatalf("expected 2 history points, got %d", len(res.History))
	}
	if res.History[0].AgeDays != 10 {
		t.Fatalf("expected o1 age 10, got %d", res.History[0].AgeDays)
	}
	if res.History[1].AgeDays != 20 {
		t.Fatalf("expected o2 age 20, got %d", res.History[1].AgeDays)
	}
	// Headline average of (10 + 20) / 2 = 15
	if res.HeadlineDays != 15 {
		t.Fatalf("expected headline 15, got %d", res.HeadlineDays)
	}
}

func TestRunMonteCarloSimulation(t *testing.T) {
	logger.BindTestLogger(t)

	ctx := context.Background()
	cfg := MonteCarloConfig{
		CurrentAge:       60,
		TargetAge:        65,
		SimulationsCount: 1000,
		ReturnModel:      "RANDOM_NORMAL",
		Pots: []MontePot{
			{
				Name:                 "Retirement Portfolio",
				StartingBalanceMinor: 100000000, // 1,000,000 PLN
				ExpectedReturnPct:    7.0,
				VolatilityStdDevPct:  12.0,
				AccessibleFromAge:    60,
			},
		},
		SpendingPhases: []SpendPhase{
			{
				FromAge:           60,
				ToAge:             65,
				AnnualTargetMinor: 5000000, // 50,000 PLN / year
			},
		},
	}

	res, err := RunMonteCarloSimulation(ctx, cfg)
	if err != nil {
		t.Fatalf("RunMonteCarloSimulation failed: %v", err)
	}

	if res.SuccessRatePct < 90.0 {
		t.Fatalf("expected high success rate (>90%%), got %.2f%%", res.SuccessRatePct)
	}
	if len(res.Percentile50Trajectory) != 6 {
		t.Fatalf("expected 6 points (ages 60..65), got %d", len(res.Percentile50Trajectory))
	}
}

func TestRunMonteCarloSimulation_WithIncomeStreamsAndGuardrails(t *testing.T) {
	logger.BindTestLogger(t)

	ctx := context.Background()
	cfg := MonteCarloConfig{
		CurrentAge:       60,
		TargetAge:        70,
		SimulationsCount: 1000,
		ReturnModel:      "RANDOM_NORMAL",
		WithdrawalRule:   "GUARDRAILS_GUYTON_KLINGER",
		InflationMeanPct: 2.5,
		InflationStdDev:  1.0,
		Pots: []MontePot{
			{
				Name:                 "Bridge Pot",
				StartingBalanceMinor: 20000000, // 200,000 PLN
				ExpectedReturnPct:    6.0,
				VolatilityStdDevPct:  10.0,
				AccessibleFromAge:    60,
			},
			{
				Name:                 "Locked Pension Pot",
				StartingBalanceMinor: 50000000, // 500,000 PLN
				ExpectedReturnPct:    7.0,
				VolatilityStdDevPct:  12.0,
				AccessibleFromAge:    65, // bridge-gap accessible from 65
			},
		},
		IncomeStreams: []IncomeStream{
			{
				Name:              "ZUS State Pension",
				FromAge:           65,
				ToAge:             70,
				AnnualGrossMinor:  3000000, // 30,000 PLN / year from age 65
				AdjustByInflation: true,
			},
		},
		SpendingPhases: []SpendPhase{
			{
				FromAge:           60,
				ToAge:             70,
				AnnualTargetMinor: 4000000, // 40,000 PLN / year
			},
		},
	}

	res, err := RunMonteCarloSimulation(ctx, cfg)
	if err != nil {
		t.Fatalf("RunMonteCarloSimulation failed: %v", err)
	}

	if res.CurrentAge != 60 || res.TargetAge != 70 {
		t.Errorf("expected ages 60 and 70, got %d and %d", res.CurrentAge, res.TargetAge)
	}
	if res.SuccessRatePct < 85.0 {
		t.Errorf("expected high success rate (>85%%), got %.2f%%", res.SuccessRatePct)
	}
	if res.DepletionAgeHistogram == nil {
		t.Errorf("expected non-nil depletion age histogram")
	}
}
