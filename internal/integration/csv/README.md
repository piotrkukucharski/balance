# Bank Statement CSV Parser & Importer (`internal/integration/csv`)

## Overview
Bank statement CSV parser and importer providing auto-detection of delimiters and encodings (UTF-8, Windows-1250), predefined Polish bank profiles (mBank, PKO BP, Santander, ING, Millennium), minor unit currency calculation, and append-only idempotent CQRS ingestion into raw_transactions and raw_transactions_current.

## Interface
- `const (MaxFileSize = 10 * 1024 * 1024, MaxRowCount = 50000, SniffByteLimit = 32 * 1024)` [parser.go:25-29]
- `const (EncodingUTF8 = "UTF-8", EncodingCP1250 = "WINDOWS-1250")` [parser.go:32-35]
- `const (BankProfileGeneric = "GENERIC", BankProfileMBank = "MBANK", BankProfilePKOBP = "PKOBP", BankProfileSantander = "SANTANDER", BankProfileING = "ING", BankProfileMillennium = "MILLENNIUM")` [parser.go:38-45]
- `type ColumnMapping struct { DateCol int, DateFormat string, AmountCol int, InflowCol int, OutflowCol int, CurrencyCol int, DefaultCurrency string, PayeeCol int, ReferenceCol int, CounterpartyIBANCol int, ExternalIDCol int, SkipHeaderRows int, Delimiter rune, Encoding string }` [parser.go:49-64]
- `func NewDefaultColumnMapping() ColumnMapping` [parser.go:67-81]
- `type ParsedTransaction struct { ExternalTxID string, BookingDateMicro int64, AmountMinor int64, Currency string, Payee string, CounterpartyIBAN string, Reference string }` [parser.go:82-90]
- `type ImportResult struct { TotalProcessed int, ImportedCount int, DuplicatesCount int, FailedCount int, ImportedIDs []string, Errors []string }` [parser.go:93-100]
- `type SniffResult struct { Encoding string, Delimiter rune }` [parser.go:103-106]
- `func CleanString(s string) string` [parser.go:114-118]
- `func SniffEncodingAndDelimiter(sample []byte) SniffResult` [parser.go:121-173]
- `func GetBankProfile(profile string, defaultCurrency string) (ColumnMapping, error)` [parser.go:176-271]
- `func ParseAmountMinor(raw string) (int64, error)` [parser.go:276-368]
- `func ParseDateToMicro(raw, preferredFormat string) (int64, error)` [parser.go:371-402]
- `type StreamParser struct` [parser.go:405-407]
- `func NewStreamParser(mapping ColumnMapping) *StreamParser` [parser.go:410-412]
- `func (p *StreamParser) Parse(r io.Reader) ([]ParsedTransaction, error)` [parser.go:415-591]
- `func ImportTransactions(ctx context.Context, tenantDB *sql.DB, accountID string, agentID string, correlationID string, r io.Reader, mapping ColumnMapping) (*ImportResult, error)` [parser.go:595-728]

## Invariants
- Parse limits enforce MaxFileSize = 10 MiB, MaxRowCount = 50,000 rows, and SniffByteLimit = 32 KiB sample size [parser.go:25-29, parser.go:418, parser.go:475, parser.go:618]
- Parsed amounts minor must not exceed domain.MaxSafeAmountMinor bounds [parser.go:363-365]
- Parser skips zero amount transactions (amountMinor == 0) and missing date rows [parser.go:493-495, parser.go:544-546]
- ImportTransactions deduplicates deterministically using transaction.ComputeCanonicalDedupHash with same-day sequence numbering for unreferenced transactions, writing into raw_transactions_current ON CONFLICT(account_id, dedup_hash) DO NOTHING [parser.go:636-637, parser.go:650, parser.go:668-685]
- Every successfully ingested row in raw_transactions_current is mirrored in the CQRS append-only audit ledger raw_transactions with action 'CREATE' [parser.go:657-662, parser.go:708-717]

## Side Effects
- Consumes bytes from the passed io.Reader stream [parser.go:415, parser.go:618]
- Opens an atomic database transaction on tenantDB [parser.go:638-643, parser.go:723-725]
- Inserts rows into tenant tables raw_transactions_current and raw_transactions [parser.go:644-666, parser.go:689-717]

## Verification
- Run: `go test ./...`
