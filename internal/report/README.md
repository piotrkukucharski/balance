# Analytics & Reporting Engine (`internal/report`)

## Overview
Analytical reporting and financial forecasting engine calculating Age of Money, Monte Carlo simulations, Sankey flows, and spending trends.

## Interface
- `type InflowBucket struct` [age_of_money.go:9]
- `type OutflowRecord struct` [age_of_money.go:15]
- `type AgeOfMoneyResult struct` [age_of_money.go:22]
- `type AgeOfMoneyPoint struct` [age_of_money.go:27]
- `func CalculateAgeOfMoney(inflows []InflowBucket, outflows []OutflowRecord) (*AgeOfMoneyResult, error)` [age_of_money.go:36]
- `type ForecastPoint struct` [balance_forecast.go:11]
- `func GenerateBalanceForecast(ctx context.Context, tenantDB *sql.DB, months int) ([]ForecastPoint, error)` [balance_forecast.go:40]
- `func RunMonteCarloSimulation(params SimulationParams) (*MonteCarloResult, error)` [monte_carlo.go:30]
- `func CalculateNetWorth(ctx context.Context, tenantDB *sql.DB) (*NetWorthReport, error)` [net_worth.go:25]
- `func GenerateSankeyFlow(ctx context.Context, tenantDB *sql.DB, fromDate, toDate time.Time) (*SankeyData, error)` [sankey_flow.go:35]

## Invariants
- Age of Money uses First-In-First-Out (FIFO) queue matching outflow dollars to historical inflow events [age_of_money.go:36-80]
- Reports query tenant tables and cache results in report_cache when enabled [balance_forecast.go:50-80]

## Side Effects
- Reads tenant SQLite tables and optionally writes aggregated cache entries to report_cache [balance_forecast.go:60-90]

## Verification
- Run: `go test ./...`
