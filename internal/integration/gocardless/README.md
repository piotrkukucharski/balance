# GoCardless Bank Account Data Client (`internal/integration/gocardless`)

## Overview
GoCardless Bank Account Data API v2 client providing Open Banking PSD2 institution lookup, EndUserAgreement creation, requisition authorization link generation, account balance queries, and transaction synchronisation.

## Interface
- `type Institution struct { ID string, Name string, BIC string, TransactionTotalDays string, Countries []string, Logo string, MaxAccessValidForDays string }` [client.go:17-25]
- `type EndUserAgreement struct { ID string, Created string, MaxHistoricalDays int, AccessValidForDays int, AccessScope []string, InstitutionID string }` [client.go:27-34]
- `type Requisition struct { ID string, Created string, Redirect string, Status string, InstitutionID string, Agreement string, Reference string, Accounts []string, Link string }` [client.go:36-46]
- `type AccountDetails struct { Account struct { ResourceID string, IBAN string, Currency string, OwnerName string, Name string, DisplayName string, Product string } }` [client.go:48-58]
- `type AccountBalance struct { BalanceAmount struct { Amount string, Currency string }, BalanceType string }` [client.go:60-66]
- `type BalancesResponse struct { Balances []AccountBalance }` [client.go:68-70]
- `type Client struct` [client.go:72-82]
- `func NewClient(secretID, secretKey, redirectURL string) *Client` [client.go:84-99]
- `func (c *Client) ListInstitutions(ctx context.Context, country string) ([]Institution, error)` [client.go:146-175]
- `func (c *Client) GetInstitution(ctx context.Context, institutionID string) (*Institution, error)` [client.go:177-207]
- `func (c *Client) CreateRequisition(ctx context.Context, institutionID, userRef string) (*Requisition, error)` [client.go:209-299]
- `func (c *Client) GetRequisition(ctx context.Context, requisitionID string) (*Requisition, error)` [client.go:301-329]
- `func (c *Client) GetAccountDetails(ctx context.Context, accountID string) (*AccountDetails, error)` [client.go:331-359]
- `func (c *Client) GetAccountBalances(ctx context.Context, accountID string) (*BalancesResponse, error)` [client.go:361-389]
- `type TransactionAmount struct { Amount string, Currency string }` [client.go:391-394]
- `type AccountReference struct { IBAN string }` [client.go:396-398]
- `type BankTransaction struct { TransactionID string, EntryReference string, BookingDate string, BookingDateTime string, ValueDate string, ValueDateTime string, TransactionAmount TransactionAmount, CreditorName string, CreditorAccount AccountReference, DebtorName string, DebtorAccount AccountReference, RemittanceInformationUnstructured string, RemittanceInformationUnstructuredArray []string, InternalTransactionID string }` [client.go:400-415]
- `func (t *BankTransaction) GetAmountMinor() (int64, error)` [client.go:417-424]
- `func (t *BankTransaction) GetBookingDateMicro() int64` [client.go:426-446]
- `func (t *BankTransaction) GetPayee() string` [client.go:448-472]
- `func (t *BankTransaction) GetCounterpartyIBAN() string` [client.go:474-480]
- `func (t *BankTransaction) GetReference() string` [client.go:482-490]
- `func (t *BankTransaction) GetExternalID() string` [client.go:492-503]
- `type TransactionsResponse struct { Transactions struct { Booked []BankTransaction, Pending []BankTransaction } }` [client.go:505-510]
- `func (c *Client) GetAccountTransactions(ctx context.Context, accountID string, dateFrom, dateTo string) (*TransactionsResponse, error)` [client.go:512-553]

## Invariants
- Access tokens are cached thread-safely with a mutex and refreshed when expired or 60 seconds prior to expiration [client.go:79-82, client.go:101-144]
- Base URL defaults to 'https://bankaccountdata.gocardless.com/api/v2' unless overridden via GOCARDLESS_BASE_URL environment variable [client.go:85-88]
- CreateRequisition automatically requests institution metadata to cap maxHistoricalDays and accessValidForDays to bank-supported limits [client.go:216-232]
- EndUserAgreement requested scope is strictly ['balances', 'details', 'transactions'] [client.go:239]

## Side Effects
- Makes outbound HTTPS REST calls to the GoCardless Bank Account Data API [client.go:122, client.go:160, client.go:191, client.go:250, client.go:283, client.go:314, client.go:344, client.go:374, client.go:537]
- Reads environment variables GOCARDLESS_BASE_URL [client.go:85]

## Verification
- Run: `go test ./...`
