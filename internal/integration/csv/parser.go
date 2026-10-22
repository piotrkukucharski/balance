package csv

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/balance/balance/internal/domain"
	"github.com/balance/balance/internal/transaction"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// Bounds and limits per architecture specification.
const (
	MaxFileSize    int64 = 10 * 1024 * 1024 // 10 MiB
	MaxRowCount    int   = 50000            // 50,000 rows
	SniffByteLimit int   = 32 * 1024        // 32 KiB for encoding & separator detection
)

// Supported encodings.
const (
	EncodingUTF8   = "UTF-8"
	EncodingCP1250 = "WINDOWS-1250"
)

// Predefined bank profiles.
const (
	BankProfileGeneric   = "GENERIC"
	BankProfileMBank     = "MBANK"
	BankProfilePKOBP     = "PKOBP"
	BankProfileSantander = "SANTANDER"
	BankProfileING       = "ING"
	BankProfileMillennium = "MILLENNIUM"
)

// ColumnMapping defines how CSV columns map to transaction fields.
// Indices are 0-based. Set index to -1 if not present.
type ColumnMapping struct {
	DateCol            int
	DateFormat         string // e.g. "2006-01-02", "02.01.2006", "02/01/2006"
	AmountCol          int    // Single amount column (+/-)
	InflowCol          int    // Distinct inflow column (positive credit)
	OutflowCol         int    // Distinct outflow column (debit)
	CurrencyCol        int    // Column containing currency string (e.g. "PLN")
	DefaultCurrency    string // Fallback currency if not in CSV or CurrencyCol == -1
	PayeeCol           int    // Payee / Contractor name
	ReferenceCol       int    // Title / Description / Reference
	CounterpartyIBANCol int   // Optional IBAN of counterparty
	ExternalIDCol      int    // Optional bank's unique transaction reference
	SkipHeaderRows     int    // Number of header rows to skip (default 1)
	Delimiter          rune   // Separator: ',', ';', '\t', etc. (0 for auto-detect)
	Encoding           string // "UTF-8" or "WINDOWS-1250" ("" for auto-detect)
}

// NewDefaultColumnMapping returns a ColumnMapping initialized with -1 for all column indices.
func NewDefaultColumnMapping() ColumnMapping {
	return ColumnMapping{
		DateCol:             -1,
		AmountCol:           -1,
		InflowCol:           -1,
		OutflowCol:          -1,
		CurrencyCol:         -1,
		PayeeCol:            -1,
		ReferenceCol:        -1,
		CounterpartyIBANCol: -1,
		ExternalIDCol:       -1,
		SkipHeaderRows:      1,
		DefaultCurrency:     "PLN",
	}
}
type ParsedTransaction struct {
	ExternalTxID     string
	BookingDateMicro int64
	AmountMinor      int64
	Currency         string
	Payee            string
	CounterpartyIBAN string
	Reference        string
}

// ImportResult contains execution statistics from an import operation.
type ImportResult struct {
	TotalProcessed int
	ImportedCount  int
	DuplicatesCount int
	FailedCount    int
	ImportedIDs    []string
	Errors         []string
}

// SniffResult holds detected file encoding and delimiter.
type SniffResult struct {
	Encoding  string
	Delimiter rune
}

var (
	nonPrintableRegex = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
	cleanSpaceRegex   = regexp.MustCompile(`\s+`)
)

// CleanString strips control characters and trims excess whitespace.
func CleanString(s string) string {
	s = nonPrintableRegex.ReplaceAllString(s, "")
	s = cleanSpaceRegex.ReplaceAllLiteralString(s, " ")
	return strings.TrimSpace(s)
}

// SniffEncodingAndDelimiter examines sample bytes to detect character encoding and CSV delimiter.
func SniffEncodingAndDelimiter(sample []byte) SniffResult {
	result := SniffResult{
		Encoding:  EncodingUTF8,
		Delimiter: ';',
	}

	// 1. Check UTF-8 BOM
	if bytes.HasPrefix(sample, []byte{0xEF, 0xBB, 0xBF}) {
		result.Encoding = EncodingUTF8
		sample = sample[3:]
	} else if utf8.Valid(sample) {
		// Valid UTF-8 byte sequence
		result.Encoding = EncodingUTF8
	} else {
		// If invalid UTF-8, test CP1250 characteristic Polish diacritics
		// Windows-1250 specific bytes for ąćęłńóśźż:
		// ą: 0xB9, ć: 0xE6, ę: 0xEA, ł: 0xB3, ń: 0xF1, ó: 0xF3, ś: 0x9C, ź: 0x9F, ż: 0xBF
		// Ą: 0xA5, Ć: 0xC6, Ę: 0xCA, Ł: 0xA3, Ń: 0xD1, Ó: 0xD3, Ś: 0x8C, Ź: 0x8F, Ż: 0xAF
		result.Encoding = EncodingCP1250
	}

	// 2. Count delimiters on sample lines
	counts := map[rune]int{
		';':  0,
		',':  0,
		'\t': 0,
	}

	scanner := bufio.NewScanner(bytes.NewReader(sample))
	linesChecked := 0
	for scanner.Scan() && linesChecked < 20 {
		line := scanner.Text()
		linesChecked++
		for _, r := range line {
			if _, ok := counts[r]; ok {
				counts[r]++
			}
		}
	}

	maxCount := -1
	bestDelim := ';'
	// Semicolon is the most prevalent Polish bank CSV separator, followed by comma, then tab
	for _, d := range []rune{';', ',', '\t'} {
		if counts[d] > maxCount && counts[d] > 0 {
			maxCount = counts[d]
			bestDelim = d
		}
	}
	result.Delimiter = bestDelim

	return result
}

// GetBankProfile returns predefined column mappings for popular Polish financial institutions.
func GetBankProfile(profile string, defaultCurrency string) (ColumnMapping, error) {
	if defaultCurrency == "" {
		defaultCurrency = "PLN"
	}

	switch strings.ToUpper(profile) {
	case BankProfileMBank:
		m := NewDefaultColumnMapping()
		m.DateCol = 0 // #Data operacji
		m.DateFormat = "2006-01-02"
		m.AmountCol = 6 // #Kwota
		m.DefaultCurrency = defaultCurrency
		m.PayeeCol = 3 // #Odbiorca / Nadawca
		m.ReferenceCol = 2 // #Tytuł
		m.SkipHeaderRows = 38 // mBank statements have metadata preamble
		m.Delimiter = ';'
		m.Encoding = EncodingCP1250
		return m, nil

	case BankProfilePKOBP:
		m := NewDefaultColumnMapping()
		m.DateCol = 0 // Data operacji
		m.DateFormat = "2006-01-02"
		m.AmountCol = 3 // Kwota
		m.CurrencyCol = 4 // Waluta
		m.DefaultCurrency = defaultCurrency
		m.PayeeCol = 5 // Nazwa odbiorcy/nadawcy
		m.ReferenceCol = 7 // Tytuł
		m.CounterpartyIBANCol = 6 // Rachunek odbiorcy/nadawcy
		m.SkipHeaderRows = 1
		m.Delimiter = ','
		m.Encoding = EncodingUTF8
		return m, nil

	case BankProfileSantander:
		m := NewDefaultColumnMapping()
		m.DateCol = 0 // Data transakcji
		m.DateFormat = "2006-01-02"
		m.AmountCol = 5 // Kwota
		m.CurrencyCol = 6 // Waluta
		m.DefaultCurrency = defaultCurrency
		m.PayeeCol = 2 // Dane kontrahenta
		m.ReferenceCol = 3 // Tytuł
		m.CounterpartyIBANCol = 4 // Nr rachunku kontrahenta
		m.SkipHeaderRows = 1
		m.Delimiter = ','
		m.Encoding = EncodingUTF8
		return m, nil

	case BankProfileING:
		m := NewDefaultColumnMapping()
		m.DateCol = 0 // Data transakcji
		m.DateFormat = "2006-01-02"
		m.AmountCol = 8 // Kwota transakcji (ze znakiem)
		m.CurrencyCol = 9 // Waluta
		m.DefaultCurrency = defaultCurrency
		m.PayeeCol = 2 // Kontrahent
		m.ReferenceCol = 3 // Tytuł
		m.CounterpartyIBANCol = 5 // Rachunek kontrahenta
		m.SkipHeaderRows = 1
		m.Delimiter = ';'
		m.Encoding = EncodingCP1250
		return m, nil

	case BankProfileMillennium:
		m := NewDefaultColumnMapping()
		m.DateCol = 1 // Data transakcji
		m.DateFormat = "2006-01-02"
		m.InflowCol = 4 // Uznania
		m.OutflowCol = 3 // Obciążenia
		m.CurrencyCol = 5 // Waluta
		m.DefaultCurrency = defaultCurrency
		m.PayeeCol = 2 // Opis / Odbiorca
		m.ReferenceCol = 6 // Tytuł płatności
		m.SkipHeaderRows = 1
		m.Delimiter = ','
		m.Encoding = EncodingUTF8
		return m, nil

	case BankProfileGeneric:
		m := NewDefaultColumnMapping()
		m.DateCol = 0
		m.DateFormat = "2006-01-02"
		m.AmountCol = 1
		m.PayeeCol = 2
		m.ReferenceCol = 3
		m.DefaultCurrency = defaultCurrency
		m.SkipHeaderRows = 1
		m.Delimiter = ';'
		m.Encoding = EncodingUTF8
		return m, nil

	default:
		return ColumnMapping{}, fmt.Errorf("unsupported bank profile: %s", profile)
	}
}

// ParseAmountMinor converts a currency string into an integer representing minor units (cents/grosze).
// Handles Polish & international decimal separators, spaces, NBSPs, currency symbols, and signs.
// Example: "1 234,56 PLN" -> 123456, "-45,99" -> -4599, "100.00" -> 10000.
func ParseAmountMinor(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("empty amount string")
	}

	// Remove common NBSP characters (0xA0, \u00a0, \u202f)
	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, "\u202f", "")
	s = strings.ReplaceAll(s, " ", "")

	// Extract negative sign if present
	isNegative := false
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		isNegative = true
	}
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "+")
	s = strings.Trim(s, "()")

	// Strip out non-numeric and non-decimal characters (e.g. "PLN", "EUR", "$")
	var cleaned strings.Builder
	hasComma := false
	hasDot := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cleaned.WriteRune(r)
		} else if r == ',' {
			hasComma = true
			cleaned.WriteRune(',')
		} else if r == '.' {
			hasDot = true
			cleaned.WriteRune('.')
		}
	}

	valStr := cleaned.String()
	if valStr == "" {
		return 0, errors.New("no numeric digits in amount")
	}

	// Normalise decimal separator
	if hasComma && hasDot {
		// E.g. "1,234.56" or "1.234,56"
		lastComma := strings.LastIndex(valStr, ",")
		lastDot := strings.LastIndex(valStr, ".")
		if lastDot > lastComma {
			// Dot is decimal: "1,234.56" -> remove comma
			valStr = strings.ReplaceAll(valStr, ",", "")
		} else {
			// Comma is decimal: "1.234,56" -> remove dot and convert comma to dot
			valStr = strings.ReplaceAll(valStr, ".", "")
			valStr = strings.ReplaceAll(valStr, ",", ".")
		}
	} else if hasComma {
		// "1234,56" -> "1234.56"
		valStr = strings.ReplaceAll(valStr, ",", ".")
	}

	// Split by decimal point to accurately construct minor units without float precision loss
	parts := strings.Split(valStr, ".")
	wholePart, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid whole amount: %w", err)
	}

	var fracPart int64 = 0
	if len(parts) > 1 {
		fracStr := parts[1]
		if len(fracStr) == 1 {
			fracStr += "0"
		} else if len(fracStr) > 2 {
			// Truncate/round to 2 decimal places
			fracStr = fracStr[:2]
		}
		fracPart, err = strconv.ParseInt(fracStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid fractional amount: %w", err)
		}
	}

	amountMinor := wholePart*100 + fracPart
	if isNegative {
		amountMinor = -amountMinor
	}

	// Safe bounds check
	if amountMinor < -domain.MaxSafeAmountMinor || amountMinor > domain.MaxSafeAmountMinor {
		return 0, fmt.Errorf("amount minor %d exceeds safe bounds", amountMinor)
	}

	return amountMinor, nil
}

// ParseDateToMicro parses a date string trying specified format and fallback formats, returning UTC microseconds.
func ParseDateToMicro(raw, preferredFormat string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("empty date string")
	}

	formats := []string{
		"2006-01-02",
		"02.01.2006",
		"02/01/2006",
		"2006/01/02",
		"02-01-2006",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"02.01.2006 15:04:05",
	}

	if preferredFormat != "" {
		formats = append([]string{preferredFormat}, formats...)
	}

	var parsed time.Time
	var err error
	for _, f := range formats {
		parsed, err = time.Parse(f, s)
		if err == nil {
			return parsed.UTC().UnixMicro(), nil
		}
	}

	return 0, fmt.Errorf("could not parse date '%s' with supported formats", raw)
}

// StreamParser provides streaming CSV decoding with automatic encoding conversion.
type StreamParser struct {
	mapping ColumnMapping
}

// NewStreamParser creates a new CSV StreamParser instance.
func NewStreamParser(mapping ColumnMapping) *StreamParser {
	return &StreamParser{mapping: mapping}
}

// Parse reads from an io.Reader, decodes the stream, and yields parsed transactions.
func (p *StreamParser) Parse(r io.Reader) ([]ParsedTransaction, error) {
	// 1. Read initial buffer for sniffing if encoding or delimiter are not specified
	bufReader := bufio.NewReader(r)
	peekBytes, _ := bufReader.Peek(SniffByteLimit)

	mapping := p.mapping
	if mapping.Encoding == "" || mapping.Delimiter == 0 {
		sniffed := SniffEncodingAndDelimiter(peekBytes)
		if mapping.Encoding == "" {
			mapping.Encoding = sniffed.Encoding
		}
		if mapping.Delimiter == 0 {
			mapping.Delimiter = sniffed.Delimiter
		}
	}

	// 2. Wrap reader with character encoding transformer if Windows-1250
	var decodedReader io.Reader = bufReader
	if strings.EqualFold(mapping.Encoding, EncodingCP1250) {
		decodedReader = transform.NewReader(bufReader, charmap.Windows1250.NewDecoder())
	}

	// Strip potential UTF-8 BOM
	bomCheck := bufio.NewReader(decodedReader)
	if head, err := bomCheck.Peek(3); err == nil && bytes.Equal(head, []byte{0xEF, 0xBB, 0xBF}) {
		bomCheck.Discard(3)
	}

	// 3. Configure standard CSV reader
	csvReader := csv.NewReader(bomCheck)
	csvReader.Comma = mapping.Delimiter
	csvReader.FieldsPerRecord = -1 // Allow variable row lengths for metadata/footers
	csvReader.LazyQuotes = true
	csvReader.TrimLeadingSpace = true

	// Skip metadata/preamble rows if configured
	for i := 0; i < mapping.SkipHeaderRows; i++ {
		_, err := csvReader.Read()
		if err == io.EOF {
			return nil, errors.New("CSV stream ended before data rows")
		}
		if err != nil {
			return nil, fmt.Errorf("error skipping header row %d: %w", i+1, err)
		}
	}

	var results []ParsedTransaction
	rowCount := 0

	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip corrupted or unreadable row lines
			continue
		}

		rowCount++
		if rowCount > MaxRowCount {
			return nil, fmt.Errorf("CSV row count exceeded maximum limit of %d rows", MaxRowCount)
		}

		// Empty row check
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			continue
		}

		getCol := func(idx int) string {
			if idx >= 0 && idx < len(record) {
				return CleanString(record[idx])
			}
			return ""
		}

		// Parse Date
		dateStr := getCol(mapping.DateCol)
		if dateStr == "" {
			continue // Skip row if date is missing (likely summary footer)
		}
		dateMicro, err := ParseDateToMicro(dateStr, mapping.DateFormat)
		if err != nil {
			continue // Date unparseable -> skip non-transaction row
		}

		// Parse Amount
		var amountMinor int64
		if mapping.AmountCol >= 0 {
			amtStr := getCol(mapping.AmountCol)
			parsedAmt, err := ParseAmountMinor(amtStr)
			if err != nil {
				continue
			}
			amountMinor = parsedAmt
		} else if mapping.InflowCol >= 0 || mapping.OutflowCol >= 0 {
			var inflowStr, outflowStr string
			if mapping.InflowCol >= 0 {
				inflowStr = getCol(mapping.InflowCol)
			}
			if mapping.OutflowCol >= 0 {
				outflowStr = getCol(mapping.OutflowCol)
			}

			var inflow, outflow int64
			if inflowStr != "" {
				inflow, _ = ParseAmountMinor(inflowStr)
				if inflow < 0 {
					inflow = -inflow
				}
			}
			if outflowStr != "" {
				outflow, _ = ParseAmountMinor(outflowStr)
				if outflow < 0 {
					outflow = -outflow
				}
			}

			if inflow > 0 {
				amountMinor = inflow
			} else if outflow > 0 {
				amountMinor = -outflow
			} else {
				continue // Zero or empty
			}
		} else {
			return nil, errors.New("neither AmountCol nor Inflow/Outflow columns configured")
		}

		if amountMinor == 0 {
			continue // Zero amount rows ignored
		}

		// Currency
		currency := ""
		if mapping.CurrencyCol >= 0 {
			currency = getCol(mapping.CurrencyCol)
		}
		if currency == "" {
			currency = mapping.DefaultCurrency
		}
		currency = strings.ToUpper(currency)
		if currency == "" {
			currency = "PLN"
		}

		// Payee, Reference, IBAN, ExternalID
		payee := ""
		if mapping.PayeeCol >= 0 {
			payee = getCol(mapping.PayeeCol)
		}
		ref := ""
		if mapping.ReferenceCol >= 0 {
			ref = getCol(mapping.ReferenceCol)
		}
		iban := ""
		if mapping.CounterpartyIBANCol >= 0 {
			iban = getCol(mapping.CounterpartyIBANCol)
		}
		extID := ""
		if mapping.ExternalIDCol >= 0 {
			extID = getCol(mapping.ExternalIDCol)
		}

		results = append(results, ParsedTransaction{
			ExternalTxID:     extID,
			BookingDateMicro: dateMicro,
			AmountMinor:      amountMinor,
			Currency:         currency,
			Payee:            payee,
			CounterpartyIBAN: iban,
			Reference:        ref,
		})
	}

	return results, nil
}

// ImportTransactions processes a CSV stream and safely persists RawTransactions into tenant database.
// Adheres strictly to the CQRS append-only raw_transactions ledger and raw_transactions_current projection.
func ImportTransactions(
	ctx context.Context,
	tenantDB *sql.DB,
	accountID string,
	agentID string,
	correlationID string,
	r io.Reader,
	mapping ColumnMapping,
) (*ImportResult, error) {
	if tenantDB == nil {
		return nil, errors.New("tenant database is required")
	}
	if accountID == "" {
		return nil, errors.New("account ID is required")
	}
	if agentID == "" {
		return nil, errors.New("agent ID is required")
	}
	if correlationID == "" {
		correlationID = domain.NewUUIDv7()
	}

	// Limit reader to MaxFileSize (10 MiB)
	limitedReader := io.LimitReader(r, MaxFileSize+1)

	parser := NewStreamParser(mapping)
	txs, err := parser.Parse(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("parsing CSV failed: %w", err)
	}

	result := &ImportResult{
		TotalProcessed: len(txs),
		ImportedIDs:    make([]string, 0, len(txs)),
		Errors:         make([]string, 0),
	}

	now := time.Now().UTC().UnixMicro()

	// Track same-day sequences for collision-free deterministic deduplication:
	// SHA256(len_prefixed(account_id, external_tx_id_or_seq, booking_date, amount_minor, currency, clean_payee, clean_reference))
	sameDayCounter := make(map[string]int)

	tx, err := tenantDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction failed: %w", err)
	}
	defer tx.Rollback()

	stmtInsertCurrent, err := tx.PrepareContext(ctx, `
		INSERT INTO raw_transactions_current (
			id, version, account_id, external_transaction_id, dedup_hash, booking_date,
			value_date, amount_minor, currency, raw_payee, counterparty_iban, reference,
			status, updated_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?)
		ON CONFLICT(account_id, dedup_hash) DO NOTHING`)
	if err != nil {
		return nil, fmt.Errorf("prepare raw_transactions_current insert failed: %w", err)
	}
	defer stmtInsertCurrent.Close()

	stmtInsertAppendOnly, err := tx.PrepareContext(ctx, `
		INSERT INTO raw_transactions (
			id, version, account_id, external_transaction_id, dedup_hash, booking_date,
			value_date, amount_minor, currency, raw_payee, counterparty_iban, reference,
			status, action, agent_id, correlation_id, created_at
		) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', 'CREATE', ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare raw_transactions insert failed: %w", err)
	}
	defer stmtInsertAppendOnly.Close()

	for _, pt := range txs {
		// Generate same_day_sequence if external_transaction_id is empty
		extTxIDOrSeq := pt.ExternalTxID
		if extTxIDOrSeq == "" {
			dayKey := fmt.Sprintf("%d:%d:%s", pt.BookingDateMicro, pt.AmountMinor, pt.Payee)
			sameDayCounter[dayKey]++
			extTxIDOrSeq = fmt.Sprintf("seq_%d", sameDayCounter[dayKey])
		}

		dedup := transaction.ComputeCanonicalDedupHash(
			accountID,
			extTxIDOrSeq,
			pt.BookingDateMicro,
			pt.AmountMinor,
			pt.Currency,
			pt.Payee,
			pt.Reference,
		)

		rawID := domain.NewUUIDv7()

		// 1. Insert into current projection with ON CONFLICT DO NOTHING
		res, err := stmtInsertCurrent.ExecContext(ctx,
			rawID, accountID, pt.ExternalTxID, dedup, pt.BookingDateMicro,
			pt.BookingDateMicro, pt.AmountMinor, pt.Currency, pt.Payee, pt.CounterpartyIBAN, pt.Reference,
			now,
		)
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, fmt.Sprintf("failed writing current row: %v", err))
			continue
		}

		rowsAff, err := res.RowsAffected()
		if err != nil || rowsAff == 0 {
			// Row already exists -> deduplicated
			result.DuplicatesCount++
			continue
		}

		// 2. Insert into append-only raw_transactions audit history
		_, err = stmtInsertAppendOnly.ExecContext(ctx,
			rawID, accountID, pt.ExternalTxID, dedup, pt.BookingDateMicro,
			pt.BookingDateMicro, pt.AmountMinor, pt.Currency, pt.Payee, pt.CounterpartyIBAN, pt.Reference,
			agentID, correlationID, now,
		)
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, fmt.Sprintf("failed writing append-only log: %v", err))
			continue
		}

		result.ImportedCount++
		result.ImportedIDs = append(result.ImportedIDs, rawID)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit import transaction failed: %w", err)
	}

	return result, nil
}
