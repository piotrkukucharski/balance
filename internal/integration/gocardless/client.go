package gocardless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Institution struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	BIC                   string   `json:"bic"`
	TransactionTotalDays  string   `json:"transaction_total_days"`
	Countries             []string `json:"countries"`
	Logo                  string   `json:"logo"`
	MaxAccessValidForDays string   `json:"max_access_valid_for_days"`
}

type EndUserAgreement struct {
	ID                 string   `json:"id"`
	Created            string   `json:"created"`
	MaxHistoricalDays  int      `json:"max_historical_days"`
	AccessValidForDays int      `json:"access_valid_for_days"`
	AccessScope        []string `json:"access_scope"`
	InstitutionID      string   `json:"institution_id"`
}

type Requisition struct {
	ID            string   `json:"id"`
	Created       string   `json:"created"`
	Redirect      string   `json:"redirect"`
	Status        string   `json:"status"`
	InstitutionID string   `json:"institution_id"`
	Agreement     string   `json:"agreement"`
	Reference     string   `json:"reference"`
	Accounts      []string `json:"accounts"`
	Link          string   `json:"link"`
}

type AccountDetails struct {
	Account struct {
		ResourceID  string `json:"resourceId"`
		IBAN        string `json:"iban"`
		Currency    string `json:"currency"`
		OwnerName   string `json:"ownerName"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Product     string `json:"product"`
	} `json:"account"`
}

type AccountBalance struct {
	BalanceAmount struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"balanceAmount"`
	BalanceType string `json:"balanceType"`
}

type BalancesResponse struct {
	Balances []AccountBalance `json:"balances"`
}

type Client struct {
	secretID    string
	secretKey   string
	baseURL     string
	redirectURL string
	httpClient  *http.Client

	mu           sync.Mutex
	accessToken  string
	tokenExpires time.Time
}

func NewClient(secretID, secretKey, redirectURL string) *Client {
	if redirectURL == "" {
		redirectURL = "http://localhost:8080/budgets/integrations/gocardless/callback"
	}
	return &Client{
		secretID:    secretID,
		secretKey:   secretKey,
		baseURL:     "https://bankaccountdata.gocardless.com/api/v2",
		redirectURL: redirectURL,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) getAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.tokenExpires) {
		return c.accessToken, nil
	}

	payload := map[string]string{
		"secret_id":  c.secretID,
		"secret_key": c.secretKey,
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/token/new/", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gocardless token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gocardless token error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var res struct {
		Access        string `json:"access"`
		AccessExpires int    `json:"access_expires"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	c.accessToken = res.Access
	c.tokenExpires = time.Now().Add(time.Duration(res.AccessExpires-60) * time.Second)
	return c.accessToken, nil
}

func (c *Client) ListInstitutions(ctx context.Context, country string) ([]Institution, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/institutions/?country=%s", c.baseURL, country)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch institutions (status %d)", resp.StatusCode)
	}

	var list []Institution
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

func (c *Client) GetInstitution(ctx context.Context, institutionID string) (*Institution, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/institutions/%s/", c.baseURL, institutionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch institution %s (status %d): %s", institutionID, resp.StatusCode, string(respBody))
	}

	var inst Institution
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

func (c *Client) CreateRequisition(ctx context.Context, institutionID, userRef string) (*Requisition, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	// Fetch institution constraints to respect max historical and access days
	inst, err := c.GetInstitution(ctx, institutionID)
	maxHistoricalDays := 90
	accessValidForDays := 90
	if err == nil && inst != nil {
		if inst.TransactionTotalDays != "" {
			var d int
			if _, err := fmt.Sscanf(inst.TransactionTotalDays, "%d", &d); err == nil && d > 0 {
				maxHistoricalDays = d
			}
		}
		if inst.MaxAccessValidForDays != "" {
			var d int
			if _, err := fmt.Sscanf(inst.MaxAccessValidForDays, "%d", &d); err == nil && d > 0 {
				accessValidForDays = d
			}
		}
	}

	// 1. Create End User Agreement
	agrPayload := map[string]any{
		"institution_id":        institutionID,
		"max_historical_days":   maxHistoricalDays,
		"access_valid_for_days": accessValidForDays,
		"access_scope":          []string{"balances", "details", "transactions"},
	}
	agrBytes, _ := json.Marshal(agrPayload)
	agrReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/agreements/enduser/", bytes.NewReader(agrBytes))
	if err != nil {
		return nil, err
	}
	agrReq.Header.Set("Authorization", "Bearer "+token)
	agrReq.Header.Set("Content-Type", "application/json")
	agrReq.Header.Set("Accept", "application/json")

	agrResp, err := c.httpClient.Do(agrReq)
	if err != nil {
		return nil, fmt.Errorf("create agreement error: %w", err)
	}
	defer agrResp.Body.Close()

	agrRespBody, _ := io.ReadAll(agrResp.Body)
	if agrResp.StatusCode < 200 || agrResp.StatusCode >= 300 {
		return nil, fmt.Errorf("create agreement rejected (status %d): %s", agrResp.StatusCode, string(agrRespBody))
	}

	var agreement EndUserAgreement
	if err := json.Unmarshal(agrRespBody, &agreement); err != nil {
		return nil, fmt.Errorf("decode agreement error: %w, response: %s", err, string(agrRespBody))
	}

	// 2. Create Requisition
	reqPayload := map[string]string{
		"redirect":       c.redirectURL,
		"institution_id": institutionID,
		"agreement":      agreement.ID,
		"reference":      userRef,
		"user_language":  "EN",
	}
	reqBytes, _ := json.Marshal(reqPayload)
	reqReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/requisitions/", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	reqReq.Header.Set("Authorization", "Bearer "+token)
	reqReq.Header.Set("Content-Type", "application/json")
	reqReq.Header.Set("Accept", "application/json")

	reqResp, err := c.httpClient.Do(reqReq)
	if err != nil {
		return nil, fmt.Errorf("create requisition error: %w", err)
	}
	defer reqResp.Body.Close()

	reqRespBody, _ := io.ReadAll(reqResp.Body)
	if reqResp.StatusCode < 200 || reqResp.StatusCode >= 300 {
		return nil, fmt.Errorf("create requisition rejected (status %d): %s", reqResp.StatusCode, string(reqRespBody))
	}

	var requisition Requisition
	if err := json.Unmarshal(reqRespBody, &requisition); err != nil {
		return nil, fmt.Errorf("decode requisition error: %w, response: %s", err, string(reqRespBody))
	}
	return &requisition, nil
}

func (c *Client) GetRequisition(ctx context.Context, requisitionID string) (*Requisition, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/requisitions/"+requisitionID+"/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("requisition request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var requisition Requisition
	if err := json.Unmarshal(respBody, &requisition); err != nil {
		return nil, fmt.Errorf("decode requisition error: %w, response: %s", err, string(respBody))
	}
	return &requisition, nil
}

func (c *Client) GetAccountDetails(ctx context.Context, accountID string) (*AccountDetails, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/accounts/%s/details/", c.baseURL, accountID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("account details request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var details AccountDetails
	if err := json.Unmarshal(respBody, &details); err != nil {
		return nil, fmt.Errorf("decode account details error: %w, response: %s", err, string(respBody))
	}
	return &details, nil
}

func (c *Client) GetAccountBalances(ctx context.Context, accountID string) (*BalancesResponse, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/accounts/%s/balances/", c.baseURL, accountID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("account balances request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var balances BalancesResponse
	if err := json.Unmarshal(respBody, &balances); err != nil {
		return nil, fmt.Errorf("decode account balances error: %w, response: %s", err, string(respBody))
	}
	return &balances, nil
}

type TransactionAmount struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type AccountReference struct {
	IBAN string `json:"iban"`
}

type BankTransaction struct {
	TransactionID                         string            `json:"transactionId"`
	EntryReference                        string            `json:"entryReference"`
	BookingDate                           string            `json:"bookingDate"`
	BookingDateTime                       string            `json:"bookingDateTime"`
	ValueDate                             string            `json:"valueDate"`
	ValueDateTime                         string            `json:"valueDateTime"`
	TransactionAmount                     TransactionAmount `json:"transactionAmount"`
	CreditorName                          string            `json:"creditorName"`
	CreditorAccount                       AccountReference  `json:"creditorAccount"`
	DebtorName                            string            `json:"debtorName"`
	DebtorAccount                         AccountReference  `json:"debtorAccount"`
	RemittanceInformationUnstructured     string            `json:"remittanceInformationUnstructured"`
	RemittanceInformationUnstructuredArray []string        `json:"remittanceInformationUnstructuredArray"`
	InternalTransactionID                 string            `json:"internalTransactionId"`
}

func (t *BankTransaction) GetAmountMinor() (int64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(t.TransactionAmount.Amount), "%f", &f)
	if err != nil {
		return 0, err
	}
	return int64(math.Round(f * 100)), nil
}

func (t *BankTransaction) GetBookingDateMicro() int64 {
	dateStr := t.BookingDate
	if dateStr == "" {
		dateStr = t.BookingDateTime
	}
	if dateStr == "" {
		dateStr = t.ValueDate
	}
	if dateStr == "" {
		dateStr = t.ValueDateTime
	}
	if len(dateStr) >= 10 {
		if pt, err := time.Parse("2006-01-02", dateStr[:10]); err == nil {
			return pt.UTC().UnixMicro()
		}
	}
	if pt, err := time.Parse(time.RFC3339, dateStr); err == nil {
		return pt.UTC().UnixMicro()
	}
	return time.Now().UTC().UnixMicro()
}

func (t *BankTransaction) GetPayee() string {
	var payee string
	amountMinor, _ := t.GetAmountMinor()
	if amountMinor < 0 {
		payee = t.CreditorName
		if payee == "" {
			payee = t.DebtorName
		}
	} else {
		payee = t.DebtorName
		if payee == "" {
			payee = t.CreditorName
		}
	}
	if payee == "" {
		payee = t.RemittanceInformationUnstructured
	}
	if payee == "" && len(t.RemittanceInformationUnstructuredArray) > 0 {
		payee = t.RemittanceInformationUnstructuredArray[0]
	}
	if payee == "" {
		payee = "Unknown Bank Payee"
	}
	return strings.TrimSpace(payee)
}

func (t *BankTransaction) GetCounterpartyIBAN() string {
	amountMinor, _ := t.GetAmountMinor()
	if amountMinor < 0 {
		return t.CreditorAccount.IBAN
	}
	return t.DebtorAccount.IBAN
}

func (t *BankTransaction) GetReference() string {
	if t.RemittanceInformationUnstructured != "" {
		return strings.TrimSpace(t.RemittanceInformationUnstructured)
	}
	if len(t.RemittanceInformationUnstructuredArray) > 0 {
		return strings.TrimSpace(strings.Join(t.RemittanceInformationUnstructuredArray, " "))
	}
	return strings.TrimSpace(t.EntryReference)
}

func (t *BankTransaction) GetExternalID() string {
	if t.TransactionID != "" {
		return t.TransactionID
	}
	if t.InternalTransactionID != "" {
		return t.InternalTransactionID
	}
	if t.EntryReference != "" {
		return t.EntryReference
	}
	return ""
}

type TransactionsResponse struct {
	Transactions struct {
		Booked  []BankTransaction `json:"booked"`
		Pending []BankTransaction `json:"pending"`
	} `json:"transactions"`
}

func (c *Client) GetAccountTransactions(ctx context.Context, accountID string, dateFrom, dateTo string) (*TransactionsResponse, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/accounts/%s/transactions/", c.baseURL, accountID)
	var queryParams []string
	if dateFrom != "" {
		queryParams = append(queryParams, "date_from="+dateFrom)
	}
	if dateTo != "" {
		queryParams = append(queryParams, "date_to="+dateTo)
	}
	if len(queryParams) > 0 {
		endpoint += "?" + strings.Join(queryParams, "&")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("account transactions request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var txResp TransactionsResponse
	if err := json.Unmarshal(respBody, &txResp); err != nil {
		return nil, fmt.Errorf("decode account transactions error: %w, response: %s", err, string(respBody))
	}
	return &txResp, nil
}

