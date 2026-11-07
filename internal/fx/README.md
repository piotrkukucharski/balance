# Foreign Exchange Rates Engine (`internal/fx`)

## Overview
Foreign exchange (FX) rate synchronization and caching engine supporting European Central Bank (ECB) and National Bank of Poland (NBP) feeds.

## Interface
- `type ECBClient interface` [ecb.go:14]
- `type HTTPRequestDoer interface` [ecb.go:19]
- `type HTTPECBClient struct` [ecb.go:24]
- `func NewHTTPECBClient(doer HTTPRequestDoer) *HTTPECBClient` [ecb.go:30]
- `func (c *HTTPECBClient) FetchRates(ctx context.Context) ([]RateRecord, error)` [ecb.go:64]
- `func ParseECBXML(data []byte) ([]RateRecord, error)` [ecb.go:92]
- `type FXService struct` [service.go:25]
- `func NewFXService(mainDB *sql.DB, ecbClient ECBClient) *FXService` [service.go:30]
- `func (s *FXService) SyncRates(ctx context.Context) error` [service.go:60]

## Invariants
- FX rates store exchange ratios normalized with source currency, target currency, date, and rate value [types.go:15-30]
- Rates are cached in the central main.db exchange_rates table with unique constraint on (source, target, date) [service.go:70-95]

## Side Effects
- Outbound HTTP GET calls to ECB and NBP XML/JSON endpoints [ecb.go:64-80, nbp.go:40-60]
- Inserts exchange rate records into main.db exchange_rates table [service.go:85-110]

## Verification
- Run: `go test ./...`
