# Mock Bank Adapter (`internal/integration/mock`)

## Overview
Deterministic mock banking adapter providing predefined European and Polish financial institutions, mock bank accounts (mBank Checking PLN, mBank Savings EUR), and simulated booked/pending transactions for offline development, automated tests, and BDD scenarios.

## Interface
- `type ExtAccount struct { ID string, Name string, Currency string, IBAN string, BalanceMinor int64 }` [mock_adapter.go:9-15]
- `type ExtTransaction struct { ExternalTxID string, BookingDateMicro int64, AmountMinor int64, Currency string, Payee string, CounterpartyIBAN string, Reference string, BookingStatus string }` [mock_adapter.go:17-26]
- `type Institution struct { ID string, Name string, BIC string, Countries []string, Logo string }` [mock_adapter.go:28-34]
- `type MockAdapter struct` [mock_adapter.go:37-39]
- `func NewMockAdapter() *MockAdapter` [mock_adapter.go:41-45]
- `func (m *MockAdapter) ProviderCode() string` [mock_adapter.go:47-49]
- `func (m *MockAdapter) ListInstitutions(country string) []Institution` [mock_adapter.go:51-110]
- `func (m *MockAdapter) FetchAccounts(ctx context.Context, userID string) ([]ExtAccount, error)` [mock_adapter.go:112-129]
- `func (m *MockAdapter) FetchTransactions(ctx context.Context, extAccountID string, sinceMicro int64) ([]ExtTransaction, error)` [mock_adapter.go:131-214]

## Invariants
- ProviderCode() always returns 'MOCK' [mock_adapter.go:47-49]
- ListInstitutions returns all preconfigured mock institutions if country is empty, or filters by country code (PL, DE, GB, FR) [mock_adapter.go:52-109]
- FetchAccounts always deterministically returns mock accounts: 'mock_acc_checking_pln' (5,420.50 PLN) and 'mock_acc_savings_eur' (1,200.00 EUR) [mock_adapter.go:113-128]
- FetchTransactions returns deterministic salary, shopping (Zabka), restaurant dinner, split repayments, and coffee pending hold transactions, filtered by sinceMicro if > 0 [mock_adapter.go:136-213]

## Side Effects
- None. In-memory deterministic mock operation without external I/O [mock_adapter.go:37-214]

## Verification
- Run: `go test ./...`
