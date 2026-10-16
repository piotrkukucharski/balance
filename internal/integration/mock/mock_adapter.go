package mock

import (
	"context"
	"fmt"
	"time"
)

type ExtAccount struct {
	ID              string
	Name            string
	Currency        string
	IBAN            string
	BalanceMinor    int64
}

type ExtTransaction struct {
	ExternalTxID     string
	BookingDateMicro int64
	AmountMinor      int64
	Currency         string
	Payee            string
	CounterpartyIBAN string
	Reference        string
}

type Institution struct {
	ID        string
	Name      string
	BIC       string
	Countries []string
	Logo      string
}

// MockAdapter provides deterministic bank integration data for dev, test, and E2E suites.
type MockAdapter struct {
	accounts map[string][]ExtAccount
}

func NewMockAdapter() *MockAdapter {
	return &MockAdapter{
		accounts: make(map[string][]ExtAccount),
	}
}

func (m *MockAdapter) ProviderCode() string {
	return "MOCK"
}

func (m *MockAdapter) ListInstitutions(country string) []Institution {
	all := []Institution{
		{
			ID:        "SANDBOXFINANCE_SFIN0000",
			Name:      "Sandbox Finance",
			BIC:       "SFIN0000",
			Countries: []string{"PL", "DE", "GB", "FR"},
			Logo:      "https://cdn-logos.gocardless.com/ais/SANDBOXFINANCE_SFIN0000.png",
		},
		{
			ID:        "MOCK_BANK_PL01",
			Name:      "mBank Mock Retail",
			BIC:       "BREXPLPW",
			Countries: []string{"PL"},
			Logo:      "",
		},
		{
			ID:        "MOCK_BANK_PL02",
			Name:      "PKO Bank Polski Mock",
			BIC:       "BPKOPLPW",
			Countries: []string{"PL"},
			Logo:      "",
		},
		{
			ID:        "MOCK_BANK_PL03",
			Name:      "Santander Bank Polska Mock",
			BIC:       "WBKAPLP1",
			Countries: []string{"PL"},
			Logo:      "",
		},
		{
			ID:        "MOCK_BANK_DE01",
			Name:      "Deutsche Bank Mock",
			BIC:       "DEUTDEDD",
			Countries: []string{"DE"},
			Logo:      "",
		},
		{
			ID:        "MOCK_BANK_GB01",
			Name:      "Barclays Mock UK",
			BIC:       "BARCGB22",
			Countries: []string{"GB"},
			Logo:      "",
		},
	}

	if country == "" {
		return all
	}
	var filtered []Institution
	for _, inst := range all {
		for _, c := range inst.Countries {
			if c == country {
				filtered = append(filtered, inst)
				break
			}
		}
	}
	return filtered
}

func (m *MockAdapter) FetchAccounts(ctx context.Context, userID string) ([]ExtAccount, error) {
	return []ExtAccount{
		{
			ID:           "mock_acc_checking_pln",
			Name:         "mBank Checking PLN",
			Currency:     "PLN",
			IBAN:         "PL11112222333344445555666677",
			BalanceMinor: 542050, // 5,420.50 PLN
		},
		{
			ID:           "mock_acc_savings_eur",
			Name:         "mBank Savings EUR",
			Currency:     "EUR",
			IBAN:         "PL99998888777766665555444433",
			BalanceMinor: 120000, // 1,200.00 EUR
		},
	}, nil
}

func (m *MockAdapter) FetchTransactions(ctx context.Context, extAccountID string, sinceMicro int64) ([]ExtTransaction, error) {
	now := time.Now().UTC().UnixMicro()
	day := int64(24 * 3600 * 1_000_000)

	// Return deterministic batch of raw transactions including Zabka, Salary, and Dinner split
	return []ExtTransaction{
		{
			ExternalTxID:     fmt.Sprintf("tx_mock_salary_%d", now),
			BookingDateMicro: now - 5*day,
			AmountMinor:      750000, // +7,500.00 PLN income
			Currency:         "PLN",
			Payee:            "ACME CORP SP Z O O",
			CounterpartyIBAN: "PL88881111222233334444555566",
			Reference:        "Wynagrodzenie za biezacy miesiac",
		},
		{
			ExternalTxID:     fmt.Sprintf("tx_mock_zabka_1_%d", now),
			BookingDateMicro: now - 3*day,
			AmountMinor:      -2540, // -25.40 PLN
			Currency:         "PLN",
			Payee:            "ZABKA Z1234 WARSZAWA",
			Reference:        "Platnosc karta 1234",
		},
		{
			ExternalTxID:     fmt.Sprintf("tx_mock_zabka_2_%d", now),
			BookingDateMicro: now - 2*day,
			AmountMinor:      -1480, // -14.80 PLN
			Currency:         "PLN",
			Payee:            "ZABKA Z5678 KRAKOW",
			Reference:        "Platnosc karta 1234",
		},
		{
			ExternalTxID:     fmt.Sprintf("tx_mock_dinner_%d", now),
			BookingDateMicro: now - 1*day,
			AmountMinor:      -30000, // -300.00 PLN restaurant bill
			Currency:         "PLN",
			Payee:            "RESTAURACJA POD BARANEM",
			Reference:        "Kolacja",
		},
		{
			ExternalTxID:     fmt.Sprintf("tx_mock_friend_a_%d", now),
			BookingDateMicro: now - 1*day,
			AmountMinor:      10000, // +100.00 PLN friend A repayment
			Currency:         "PLN",
			Payee:            "MAREK KOWALSKI",
			Reference:        "Zwrot za kolacje",
		},
		{
			ExternalTxID:     fmt.Sprintf("tx_mock_friend_b_%d", now),
			BookingDateMicro: now - 1*day,
			AmountMinor:      10000, // +100.00 PLN friend B repayment
			Currency:         "PLN",
			Payee:            "ANNA NOWAK",
			Reference:        "Za wino",
		},
	}, nil
}
