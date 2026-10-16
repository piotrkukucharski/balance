package report

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

// MonteCarloConfig mirrors the configuration schema defined in spec 13.3.
type MonteCarloConfig struct {
	CurrentAge       int           `json:"current_age"`
	TargetAge        int           `json:"target_age"`
	SimulationsCount int           `json:"simulations_count"` // 1000..10000 (default 5000)
	ReturnModel      string        `json:"return_model"`      // RANDOM_NORMAL, HISTORICAL_SHUFFLED, etc.
	InflationMeanPct float64       `json:"inflation_mean_pct"`
	InflationStdDev  float64       `json:"inflation_std_dev"`
	Pots             []MontePot    `json:"pots"`
	SpendingPhases   []SpendPhase  `json:"spending_phases"`
}

type MontePot struct {
	Name                string  `json:"name"`
	StartingBalanceMinor int64  `json:"starting_balance_minor"`
	ExpectedReturnPct   float64 `json:"expected_return_pct"`
	VolatilityStdDevPct float64 `json:"volatility_std_dev_pct"`
	AccessibleFromAge   int     `json:"accessible_from_age"`
}

type SpendPhase struct {
	FromAge          int   `json:"from_age"`
	ToAge            int   `json:"to_age"`
	AnnualTargetMinor int64 `json:"annual_target_minor"`
}

type MonteCarloResult struct {
	SuccessRatePct       float64   `json:"success_rate_pct"`
	MedianEndingBalance  int64     `json:"median_ending_balance"`
	ChanceOfRunningOut   float64   `json:"chance_of_running_out"`
	Percentile10Trajectory []int64 `json:"p10_trajectory"`
	Percentile50Trajectory []int64 `json:"p50_trajectory"`
	Percentile90Trajectory []int64 `json:"p90_trajectory"`
}

// Global semaphore limiting concurrent heavy simulations to max 4
var simulationSemaphore = make(chan struct{}, 4)

// RunMonteCarloSimulation runs parallel stochastic replays across worker goroutines.
func RunMonteCarloSimulation(ctx context.Context, cfg MonteCarloConfig) (*MonteCarloResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	select {
	case simulationSemaphore <- struct{}{}:
		defer func() { <-simulationSemaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	count := cfg.SimulationsCount
	if count < 1000 {
		count = 1000
	} else if count > 10000 {
		count = 10000
	}

	years := cfg.TargetAge - cfg.CurrentAge
	if years < 1 {
		years = 1
	}

	trajectories := make([][]int64, count)
	successCount := 0
	var mu sync.Mutex

	// Worker pool
	jobs := make(chan int, count)
	var wg sync.WaitGroup
	workers := 8

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(workerID+1)))

			for job := range jobs {
				traj, success := simulateSingleRun(cfg, years, rng)
				mu.Lock()
				trajectories[job] = traj
				if success {
					successCount++
				}
				mu.Unlock()
			}
		}(w)
	}

	for i := 0; i < count; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// Compute percentiles for each year
	p10 := make([]int64, years+1)
	p50 := make([]int64, years+1)
	p90 := make([]int64, years+1)

	for y := 0; y <= years; y++ {
		yearBalances := make([]int64, count)
		for i := 0; i < count; i++ {
			yearBalances[i] = trajectories[i][y]
		}
		sort.Slice(yearBalances, func(i, j int) bool {
			return yearBalances[i] < yearBalances[j]
		})

		idx10 := int(float64(count) * 0.10)
		idx50 := int(float64(count) * 0.50)
		idx90 := int(float64(count) * 0.90)

		p10[y] = yearBalances[idx10]
		p50[y] = yearBalances[idx50]
		p90[y] = yearBalances[idx90]
	}

	successRate := (float64(successCount) / float64(count)) * 100.0

	return &MonteCarloResult{
		SuccessRatePct:       math.Round(successRate*100) / 100,
		MedianEndingBalance:  p50[years],
		ChanceOfRunningOut:   math.Round((100.0-successRate)*100) / 100,
		Percentile10Trajectory: p10,
		Percentile50Trajectory: p50,
		Percentile90Trajectory: p90,
	}, nil
}

func simulateSingleRun(cfg MonteCarloConfig, years int, rng *rand.Rand) ([]int64, bool) {
	traj := make([]int64, years+1)

	// Clone starting pot balances
	pots := make([]int64, len(cfg.Pots))
	var initialTotal int64
	for i, p := range cfg.Pots {
		pots[i] = p.StartingBalanceMinor
		initialTotal += p.StartingBalanceMinor
	}
	traj[0] = initialTotal

	success := true

	for y := 1; y <= years; y++ {
		age := cfg.CurrentAge + y

		// 1. Determine spending requirement for this age
		var neededSpend int64
		for _, sp := range cfg.SpendingPhases {
			if age >= sp.FromAge && age <= sp.ToAge {
				neededSpend = sp.AnnualTargetMinor
				break
			}
		}

		// 2. Withdraw from accessible pots
		for neededSpend > 0 {
			withdrawnAny := false
			for i, p := range cfg.Pots {
				if age >= p.AccessibleFromAge && pots[i] > 0 {
					draw := neededSpend
					if draw > pots[i] {
						draw = pots[i]
					}
					pots[i] -= draw
					neededSpend -= draw
					withdrawnAny = true
					if neededSpend <= 0 {
						break
					}
				}
			}
			if !withdrawnAny {
				// Ran out of accessible funds
				success = false
				break
			}
		}

		// 3. Apply market return to remaining pot balances
		var yearEndTotal int64
		for i, p := range cfg.Pots {
			if pots[i] > 0 {
				// Normal random return
				mean := p.ExpectedReturnPct / 100.0
				std := p.VolatilityStdDevPct / 100.0
				r := rng.NormFloat64()*std + mean
				pots[i] = int64(math.Round(float64(pots[i]) * (1.0 + r)))
				if pots[i] < 0 {
					pots[i] = 0
				}
			}
			yearEndTotal += pots[i]
		}

		traj[y] = yearEndTotal
		if yearEndTotal <= 0 && neededSpend > 0 {
			success = false
		}
	}

	return traj, success
}
