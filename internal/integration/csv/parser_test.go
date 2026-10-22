package csv

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balance/balance/internal/db"
	"github.com/balance/balance/internal/domain"
	"golang.org/x/text/encoding/charmap"
)

func TestParseAmountMinor(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
		wantErr  bool
	}{
		{"Standard dot", "100.50", 10050, false},
		{"Polish comma", "100,50", 10050, false},
		{"Polish thousands with space", "1 234,56", 123456, false},
		{"Polish thousands with NBSP", "1\u00a0234,56 PLN", 123456, false},
		{"Negative amount with minus", "-45,99", -4599, false},
		{"Negative amount with parentheses", "(150,00)", -15000, false},
		{"Zero fractional single digit", "12,5", 1250, false},
		{"Whole integer", "500", 50000, false},
		{"Integer with currency suffix", "250 PLN", 25000, false},
		{"Small fraction", "0,05", 5, false},
		{"Negative small fraction", "-0,99", -99, false},
		{"Invalid empty", "", 0, true},
		{"Invalid non-numeric", "ABC PLN", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAmountMinor(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAmountMinor(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.expected {
				t.Errorf("ParseAmountMinor(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseDateToMicro(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		prefFmt  string
		expected time.Time
		wantErr  bool
	}{
		{"ISO date", "2026-03-15", "", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), false},
		{"Polish dot date", "15.03.2026", "", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), false},
		{"Slash date", "15/03/2026", "", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), false},
		{"With timestamp", "2026-03-15 14:30:00", "", time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC), false},
		{"Invalid date", "not-a-date", "", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDateToMicro(tt.input, tt.prefFmt)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseDateToMicro(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr {
				expMicro := tt.expected.UnixMicro()
				if got != expMicro {
					t.Errorf("ParseDateToMicro(%q) = %d, want %d", tt.input, got, expMicro)
				}
			}
		})
	}
}

func TestSniffEncodingAndDelimiter(t *testing.T) {
	t.Run("Sniff UTF-8 semicolon", func(t *testing.T) {
		csvContent := "Data;Kwota;Tytuł\n2026-01-01;100,00;Płatność w Żabce\n"
		res := SniffEncodingAndDelimiter([]byte(csvContent))
		if res.Encoding != EncodingUTF8 {
			t.Errorf("expected UTF-8, got %s", res.Encoding)
		}
		if res.Delimiter != ';' {
			t.Errorf("expected ';', got %c", res.Delimiter)
		}
	})

	t.Run("Sniff Windows-1250 comma", func(t *testing.T) {
		encoder := charmap.Windows1250.NewEncoder()
		cp1250Bytes, err := encoder.Bytes([]byte("Data,Kwota,Tytuł\n2026-01-01,100.00,Załącznik łąka\n"))
		if err != nil {
			t.Fatal(err)
		}

		res := SniffEncodingAndDelimiter(cp1250Bytes)
		if res.Encoding != EncodingCP1250 {
			t.Errorf("expected Windows-1250, got %s", res.Encoding)
		}
		if res.Delimiter != ',' {
			t.Errorf("expected ',', got %c", res.Delimiter)
		}
	})
}

func TestGenericCSVParser(t *testing.T) {
	csvData := `Data operacji;Kwota;Odbiorca;Tytul;IBAN
2026-04-10;-54,20;Zabka Polska;Zakupy spozywcze;PL11112222333344445555666677
2026-04-11;3500,00;Pracodawca Sp z oo;Wynagrodzenie;PL99998888777766665555444433
`
	mapping := NewDefaultColumnMapping()
	mapping.DateCol = 0
	mapping.AmountCol = 1
	mapping.PayeeCol = 2
	mapping.ReferenceCol = 3
	mapping.CounterpartyIBANCol = 4
	mapping.DefaultCurrency = "PLN"
	mapping.SkipHeaderRows = 1
	mapping.Delimiter = ';'
	mapping.Encoding = EncodingUTF8

	parser := NewStreamParser(mapping)
	txs, err := parser.Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("parser.Parse failed: %v", err)
	}

	if len(txs) != 2 {
		t.Fatalf("expected 2 transactions, got %d", len(txs))
	}

	// First tx (-54.20)
	if txs[0].AmountMinor != -5420 {
		t.Errorf("tx[0] AmountMinor = %d, want -5420", txs[0].AmountMinor)
	}
	if txs[0].Payee != "Zabka Polska" {
		t.Errorf("tx[0] Payee = %s, want Zabka Polska", txs[0].Payee)
	}
	if txs[0].CounterpartyIBAN != "PL11112222333344445555666677" {
		t.Errorf("tx[0] IBAN = %s", txs[0].CounterpartyIBAN)
	}

	// Second tx (+3500.00)
	if txs[1].AmountMinor != 350000 {
		t.Errorf("tx[1] AmountMinor = %d, want 350000", txs[1].AmountMinor)
	}
	if txs[1].Currency != "PLN" {
		t.Errorf("tx[1] Currency = %s, want PLN", txs[1].Currency)
	}
}

func TestSeparateInflowOutflowColumns(t *testing.T) {
	// Format with distinct Debit / Credit columns (e.g. Millennium style)
	csvData := `Nr,Data,Odbiorca,Obciazenia,Uznania,Waluta,Tytul
1,2026-05-01,Kawiarnia,25.50,,PLN,Kawa i ciastko
2,2026-05-02,Zwrot sklepu,,120.00,PLN,Zwrot towaru
`
	mapping := NewDefaultColumnMapping()
	mapping.DateCol = 1
	mapping.PayeeCol = 2
	mapping.OutflowCol = 3
	mapping.InflowCol = 4
	mapping.CurrencyCol = 5
	mapping.ReferenceCol = 6
	mapping.DefaultCurrency = "PLN"
	mapping.SkipHeaderRows = 1
	mapping.Delimiter = ','
	mapping.Encoding = EncodingUTF8

	parser := NewStreamParser(mapping)
	txs, err := parser.Parse(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("parser.Parse failed: %v", err)
	}

	if len(txs) != 2 {
		t.Fatalf("expected 2 transactions, got %d", len(txs))
	}

	if txs[0].AmountMinor != -2550 {
		t.Errorf("tx[0] AmountMinor = %d, want -2550 (outflow)", txs[0].AmountMinor)
	}
	if txs[1].AmountMinor != 12000 {
		t.Errorf("tx[1] AmountMinor = %d, want 12000 (inflow)", txs[1].AmountMinor)
	}
}

func TestWindows1250EncodingDecoding(t *testing.T) {
	encoder := charmap.Windows1250.NewEncoder()
	rawText := "Data operacji;Kwota;Odbiorca;Tytuł\n2026-01-10;-15,00;Piekarnia Żółty Kłos;Bułki i pączek\n"
	encodedBytes, err := encoder.Bytes([]byte(rawText))
	if err != nil {
		t.Fatal(err)
	}

	mapping := NewDefaultColumnMapping()
	mapping.DateCol = 0
	mapping.AmountCol = 1
	mapping.PayeeCol = 2
	mapping.ReferenceCol = 3
	mapping.DefaultCurrency = "PLN"
	mapping.SkipHeaderRows = 1
	mapping.Delimiter = ';'
	mapping.Encoding = EncodingCP1250

	parser := NewStreamParser(mapping)
	txs, err := parser.Parse(strings.NewReader(string(encodedBytes)))
	if err != nil {
		t.Fatalf("parser.Parse CP1250 failed: %v", err)
	}

	if len(txs) != 1 {
		t.Fatalf("expected 1 tx, got %d", len(txs))
	}

	if txs[0].Payee != "Piekarnia Żółty Kłos" {
		t.Errorf("Payee decode mismatch: got %q, want 'Piekarnia Żółty Kłos'", txs[0].Payee)
	}
	if txs[0].Reference != "Bułki i pączek" {
		t.Errorf("Reference decode mismatch: got %q, want 'Bułki i pączek'", txs[0].Reference)
	}
}

func setupTestTenantDB(t *testing.T) (*sql.DB, string, func()) {
	tmpDir, err := os.MkdirTemp("", "balance_csv_test_*")
	if err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(tmpDir, "main.db")
	mainDB, err := db.OpenDB(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(mainDB, "migrations/main"); err != nil {
		t.Fatal(err)
	}

	budgetsDir := filepath.Join(tmpDir, "budgets")
	tenantMgr, err := db.NewTenantDBManager(budgetsDir, mainDB, 10)
	if err != nil {
		t.Fatal(err)
	}

	userID := domain.NewUUIDv7()
	_, err = mainDB.Exec(`
		INSERT INTO users (id, display_name, email, password_hash, created_at, updated_at)
		VALUES (?, 'Test User', 'test@example.com', 'hash', 100, 100)`, userID)
	if err != nil {
		t.Fatal(err)
	}

	budgetID := domain.NewUUIDv7()
	_, err = mainDB.Exec(`
		INSERT INTO budgets (id, name, default_currency, timezone, created_by_agent_id, created_at, updated_at)
		VALUES (?, 'Test Budget', 'PLN', 'Europe/Warsaw', ?, 100, 100)`, budgetID, userID)
	if err != nil {
		t.Fatal(err)
	}

	tenantDB, err := tenantMgr.GetTenantDB(budgetID, true)
	if err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		tenantMgr.CloseAll()
		mainDB.Close()
		os.RemoveAll(tmpDir)
	}

	return tenantDB, budgetID, cleanup
}

func TestImportTransactionsDeduplication(t *testing.T) {
	tenantDB, _, cleanup := setupTestTenantDB(t)
	defer cleanup()

	ctx := context.Background()
	accountID := domain.NewUUIDv7()
	agentID := domain.NewUUIDv7()

	csvContent := `Data;Kwota;Kontrahent;Tytul;IBAN
2026-03-01;-12,50;Zabka Sklep;Kawa i przekaska;PL112233
2026-03-02;500,00;Jan Kowalski;Zwrot pozyczki;PL445566
2026-03-01;-12,50;Zabka Sklep;Kawa i przekaska;PL112233
`
	mapping := NewDefaultColumnMapping()
	mapping.DateCol = 0
	mapping.AmountCol = 1
	mapping.PayeeCol = 2
	mapping.ReferenceCol = 3
	mapping.CounterpartyIBANCol = 4
	mapping.DefaultCurrency = "PLN"
	mapping.SkipHeaderRows = 1
	mapping.Delimiter = ';'
	mapping.Encoding = EncodingUTF8

	// 1. First import: row 1 and row 3 are same day & same payee & amount, but distinct same_day_sequences (seq_1, seq_2)
	res1, err := ImportTransactions(ctx, tenantDB, accountID, agentID, domain.NewUUIDv7(), strings.NewReader(csvContent), mapping)
	if err != nil {
		t.Fatalf("ImportTransactions failed: %v", err)
	}

	if res1.TotalProcessed != 3 {
		t.Errorf("expected 3 processed, got %d", res1.TotalProcessed)
	}
	if res1.ImportedCount != 3 {
		t.Errorf("expected 3 imported on first run (distinct same-day seqs), got %d", res1.ImportedCount)
	}

	// 2. Second import of exact same file -> All 3 must be detected as duplicates
	res2, err := ImportTransactions(ctx, tenantDB, accountID, agentID, domain.NewUUIDv7(), strings.NewReader(csvContent), mapping)
	if err != nil {
		t.Fatalf("Second ImportTransactions failed: %v", err)
	}

	if res2.ImportedCount != 0 {
		t.Errorf("expected 0 imported on re-upload, got %d", res2.ImportedCount)
	}
	if res2.DuplicatesCount != 3 {
		t.Errorf("expected 3 duplicates detected, got %d", res2.DuplicatesCount)
	}

	// 3. Verify in raw_transactions_current
	var count int
	err = tenantDB.QueryRow(`SELECT count(*) FROM raw_transactions_current WHERE account_id = ?`, accountID).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("expected 3 records in DB, found %d", count)
	}

	// 4. Verify append-only log count matches
	var auditCount int
	err = tenantDB.QueryRow(`SELECT count(*) FROM raw_transactions WHERE account_id = ?`, accountID).Scan(&auditCount)
	if err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Errorf("expected 3 audit records in append-only table, found %d", auditCount)
	}
}

func TestBankProfiles(t *testing.T) {
	profiles := []string{
		BankProfileMBank,
		BankProfilePKOBP,
		BankProfileSantander,
		BankProfileING,
		BankProfileMillennium,
		BankProfileGeneric,
	}

	for _, p := range profiles {
		m, err := GetBankProfile(p, "PLN")
		if err != nil {
			t.Errorf("GetBankProfile(%s) failed: %v", p, err)
		}
		if m.DefaultCurrency != "PLN" {
			t.Errorf("profile %s default currency = %s, want PLN", p, m.DefaultCurrency)
		}
	}
}
