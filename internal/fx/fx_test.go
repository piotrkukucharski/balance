package fx

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed opening in-memory db: %v", err)
	}

	schema := `
	CREATE TABLE currency_exchange_rates (
		id TEXT PRIMARY KEY,
		base_currency TEXT NOT NULL,
		target_currency TEXT NOT NULL,
		rate_date INTEGER NOT NULL,
		rate_micros INTEGER NOT NULL CHECK(rate_micros > 0),
		source TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		UNIQUE (base_currency, target_currency, rate_date)
	);
	CREATE INDEX idx_fx_rates_lookup ON currency_exchange_rates(base_currency, target_currency, rate_date DESC);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed creating schema: %v", err)
	}
	return db
}

type mockECBClient struct {
	quotes []RateQuote
	err    error
}

func (m *mockECBClient) FetchRates(ctx context.Context, days int) ([]RateQuote, error) {
	return m.quotes, m.err
}

type mockNBPClient struct {
	quotes []RateQuote
	err    error
}

func (m *mockNBPClient) FetchRates(ctx context.Context, days int) ([]RateQuote, error) {
	return m.quotes, m.err
}

func TestParseDecimalMicros(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"4.3215", 4321500, false},
		{"1.08", 1080000, false},
		{"0.000001", 1, false},
		{"0.0000004", 0, true}, // rounds to 0, which is non-positive
		{"0.0000005", 1, false}, // rounds up to 1
		{"-1.23", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		res, err := ParseDecimalMicros(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseDecimalMicros(%q) expected error, got %d", tt.input, res)
			}
		} else {
			if err != nil {
				t.Errorf("ParseDecimalMicros(%q) unexpected error: %v", tt.input, err)
			}
			if res != tt.expected {
				t.Errorf("ParseDecimalMicros(%q) = %d, expected %d", tt.input, res, tt.expected)
			}
		}
	}
}

func TestInterpolateRate(t *testing.T) {
	// Friday Oct 16 to Monday Oct 19
	t1, _ := ParseDateBoundaryMicros("2026-10-16")
	r1 := int64(4300000)
	t2, _ := ParseDateBoundaryMicros("2026-10-19")
	r2 := int64(4330000)

	// Saturday Oct 17
	tSat, _ := ParseDateBoundaryMicros("2026-10-17")
	rSat := InterpolateRate(r1, t1, r2, t2, tSat)
	if rSat != 4310000 {
		t.Errorf("expected Saturday rate 4310000, got %d", rSat)
	}

	// Sunday Oct 18
	tSun, _ := ParseDateBoundaryMicros("2026-10-18")
	rSun := InterpolateRate(r1, t1, r2, t2, tSun)
	if rSun != 4320000 {
		t.Errorf("expected Sunday rate 4320000, got %d", rSun)
	}
}

func TestInvertRateMicros(t *testing.T) {
	// 1 EUR = 1.08 USD (1080000 micros)
	// 1 USD = 1/1.08 EUR = 0.9259259... -> 925926 micros
	inv := InvertRateMicros(1080000)
	if inv != 925926 {
		t.Errorf("expected inverted rate 925926, got %d", inv)
	}
}

func TestService_Sync_ECB_Priority(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	defer db.Close()

	d, _ := ParseDateBoundaryMicros("2026-10-23")

	ecbMock := &mockECBClient{
		quotes: []RateQuote{
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: d, RateMicros: 4300000, Source: "ECB"},
			{BaseCurrency: "EUR", TargetCurrency: "USD", RateDate: d, RateMicros: 1080000, Source: "ECB"},
		},
	}
	nbpMock := &mockNBPClient{
		quotes: []RateQuote{
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: d, RateMicros: 4320000, Source: "NBP"},
			{BaseCurrency: "GBP", TargetCurrency: "PLN", RateDate: d, RateMicros: 5150000, Source: "NBP"},
		},
	}

	svc := NewService(db, ecbMock, nbpMock)
	count, err := svc.Sync(ctx, SyncOptions{})
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if count < 3 {
		t.Errorf("expected at least 3 synced rates, got %d", count)
	}

	var totalDistinct int
	if err := db.QueryRow("SELECT COUNT(*) FROM currency_exchange_rates").Scan(&totalDistinct); err != nil {
		t.Fatalf("failed counting rows: %v", err)
	}
	if totalDistinct != 3 { // EUR/PLN, EUR/USD, GBP/PLN
		t.Errorf("expected 3 distinct rows in database, got %d", totalDistinct)
	}

	// Verify ECB took priority for EUR/PLN
	var rate int64
	var source string
	err = db.QueryRow("SELECT rate_micros, source FROM currency_exchange_rates WHERE base_currency = 'EUR' AND target_currency = 'PLN'").Scan(&rate, &source)
	if err != nil {
		t.Fatalf("failed querying EUR/PLN: %v", err)
	}
	if rate != 4300000 || source != "ECB" {
		t.Errorf("expected ECB rate 4300000 from ECB, got %d from %s", rate, source)
	}

	// Verify GBP/PLN was stored from NBP
	err = db.QueryRow("SELECT rate_micros, source FROM currency_exchange_rates WHERE base_currency = 'GBP' AND target_currency = 'PLN'").Scan(&rate, &source)
	if err != nil {
		t.Fatalf("failed querying GBP/PLN: %v", err)
	}
	if rate != 5150000 || source != "NBP" {
		t.Errorf("expected NBP rate 5150000 from NBP, got %d from %s", rate, source)
	}
}

func TestService_Convert_TriangulationAndInversion(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	defer db.Close()

	d, _ := ParseDateBoundaryMicros("2026-10-23")

	ecbMock := &mockECBClient{
		quotes: []RateQuote{
			{BaseCurrency: "EUR", TargetCurrency: "USD", RateDate: d, RateMicros: 1080000, Source: "ECB"},
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: d, RateMicros: 4320000, Source: "ECB"},
		},
	}
	svc := NewService(db, ecbMock, &mockNBPClient{})
	_, err := svc.Sync(ctx, SyncOptions{})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// 100.00 USD (10000 minor) to EUR (should invert EUR->USD)
	// 10000 * 925926 / 1000000 = 9259 minor units = 92.59 EUR
	eurMinor, err := svc.Convert(ctx, 10000, "USD", "EUR", d)
	if err != nil {
		t.Fatalf("USD to EUR convert failed: %v", err)
	}
	if eurMinor != 9259 {
		t.Errorf("expected 9259 minor units EUR (92.59), got %d", eurMinor)
	}

	// 100.00 USD to PLN (triangulate USD -> EUR -> PLN)
	// Rate USD -> EUR = 925926 micros
	// Rate EUR -> PLN = 4320000 micros
	// Composite rate = (925926 * 4320000) / 10^6 = 4000000 micros = 4.00 PLN/USD
	// 10000 * 4000000 / 1000000 = 40000 minor units = 400.00 PLN
	plnMinor, err := svc.Convert(ctx, 10000, "USD", "PLN", d)
	if err != nil {
		t.Fatalf("USD to PLN convert failed: %v", err)
	}
	if plnMinor != 40000 {
		t.Errorf("expected 40000 minor units PLN (400.00), got %d", plnMinor)
	}
}

func TestService_Convert_Weekend_Interpolation(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	defer db.Close()

	tFri, _ := ParseDateBoundaryMicros("2026-10-16")
	tMon, _ := ParseDateBoundaryMicros("2026-10-19")

	ecbMock := &mockECBClient{
		quotes: []RateQuote{
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: tFri, RateMicros: 4300000, Source: "ECB"},
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: tMon, RateMicros: 4330000, Source: "ECB"},
		},
	}
	svc := NewService(db, ecbMock, &mockNBPClient{})
	_, err := svc.Sync(ctx, SyncOptions{})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Saturday 2026-10-17: 100.00 EUR -> 431.00 PLN
	tSat, _ := ParseDateBoundaryMicros("2026-10-17")
	satMinor, err := svc.Convert(ctx, 10000, "EUR", "PLN", tSat)
	if err != nil {
		t.Fatalf("Saturday convert failed: %v", err)
	}
	if satMinor != 43100 {
		t.Errorf("expected 43100 minor units (431.00 PLN), got %d", satMinor)
	}

	// Sunday 2026-10-18: 100.00 EUR -> 432.00 PLN
	tSun, _ := ParseDateBoundaryMicros("2026-10-18")
	sunMinor, err := svc.Convert(ctx, 10000, "EUR", "PLN", tSun)
	if err != nil {
		t.Fatalf("Sunday convert failed: %v", err)
	}
	if sunMinor != 43200 {
		t.Errorf("expected 43200 minor units (432.00 PLN), got %d", sunMinor)
	}
}

func TestService_Convert_InProgressWeekendFallback(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	defer db.Close()

	tFri, _ := ParseDateBoundaryMicros("2026-10-23")
	ecbMock := &mockECBClient{
		quotes: []RateQuote{
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: tFri, RateMicros: 4300000, Source: "ECB"},
		},
	}
	svc := NewService(db, ecbMock, &mockNBPClient{})
	_, err := svc.Sync(ctx, SyncOptions{})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Saturday Oct 24 (Monday Oct 26 not yet published) -> falls back to Friday rate 4.300000
	tSat, _ := ParseDateBoundaryMicros("2026-10-24")
	satMinor, err := svc.Convert(ctx, 10000, "EUR", "PLN", tSat)
	if err != nil {
		t.Fatalf("in-progress Saturday convert failed: %v", err)
	}
	if satMinor != 43000 {
		t.Errorf("expected 43000 minor units (430.00 PLN), got %d", satMinor)
	}
}

func TestService_Convert_StrictErrorOnMissingHistory(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	defer db.Close()

	tHistStart, _ := ParseDateBoundaryMicros("2026-07-27")
	ecbMock := &mockECBClient{
		quotes: []RateQuote{
			{BaseCurrency: "EUR", TargetCurrency: "PLN", RateDate: tHistStart, RateMicros: 4300000, Source: "ECB"},
		},
	}
	svc := NewService(db, ecbMock, &mockNBPClient{})
	_, _ = svc.Sync(ctx, SyncOptions{})

	// Date from January 2026 (preceding July 2026) -> strict error!
	tPast, _ := ParseDateBoundaryMicros("2026-01-15")
	_, err := svc.Convert(ctx, 10000, "EUR", "PLN", tPast)
	if err == nil {
		t.Fatalf("expected ErrExchangeRateNotFound for date preceding history, got nil")
	}
	if err != ErrExchangeRateNotFound {
		t.Errorf("expected ErrExchangeRateNotFound, got %v", err)
	}
}

func TestECB_XMLParsing(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
	<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
		<Cube>
			<Cube time="2026-10-23">
				<Cube currency="USD" rate="1.0850"/>
				<Cube currency="PLN" rate="4.3215"/>
			</Cube>
		</Cube>
	</gesmes:Envelope>`

	quotes, err := ParseECBXML(strings.NewReader(xmlData))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(quotes) != 2 {
		t.Fatalf("expected 2 quotes, got %d", len(quotes))
	}
	if quotes[0].BaseCurrency != "EUR" || quotes[0].TargetCurrency != "USD" || quotes[0].RateMicros != 1085000 {
		t.Errorf("unexpected quote 0: %+v", quotes[0])
	}
	if quotes[1].BaseCurrency != "EUR" || quotes[1].TargetCurrency != "PLN" || quotes[1].RateMicros != 4321500 {
		t.Errorf("unexpected quote 1: %+v", quotes[1])
	}
}

func TestNBP_JSONParsing(t *testing.T) {
	jsonData := `[
		{
			"table": "A",
			"no": "207/A/NBP/2026",
			"effectiveDate": "2026-10-23",
			"rates": [
				{ "currency": "euro", "code": "EUR", "mid": 4.3215 },
				{ "currency": "dolar amerykański", "code": "USD", "mid": 3.9850 }
			]
		}
	]`

	quotes, err := ParseNBPJSON(strings.NewReader(jsonData))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(quotes) != 2 {
		t.Fatalf("expected 2 quotes, got %d", len(quotes))
	}
	if quotes[0].BaseCurrency != "EUR" || quotes[0].TargetCurrency != "PLN" || quotes[0].RateMicros != 4321500 {
		t.Errorf("unexpected quote 0: %+v", quotes[0])
	}
	if quotes[1].BaseCurrency != "USD" || quotes[1].TargetCurrency != "PLN" || quotes[1].RateMicros != 3985000 {
		t.Errorf("unexpected quote 1: %+v", quotes[1])
	}
}
