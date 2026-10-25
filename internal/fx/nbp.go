package fx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NBPClient defines the interface for fetching exchange rates from the National Bank of Poland.
type NBPClient interface {
	FetchRates(ctx context.Context, days int) ([]RateQuote, error)
}

// HTTPNBPClient is the production implementation querying the NBP REST API.
type HTTPNBPClient struct {
	BaseURL    string
	HTTPClient HTTPRequestDoer
}

// NewHTTPNBPClient creates a new client targeting NBP REST API endpoints.
func NewHTTPNBPClient(baseURL string, client HTTPRequestDoer) *HTTPNBPClient {
	if baseURL == "" {
		baseURL = "https://api.nbp.pl/api/exchangerates/tables"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPNBPClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: client,
	}
}

// NBP API response structures
type nbpTableEntry struct {
	Table         string        `json:"table"`
	No            string        `json:"no"`
	EffectiveDate string        `json:"effectiveDate"`
	Rates         []nbpRateItem `json:"rates"`
}

type nbpRateItem struct {
	Currency string      `json:"currency"`
	Code     string      `json:"code"`
	Mid      json.Number `json:"mid"`
}

// FetchRates fetches rates from NBP Table A (daily) and Table B (weekly).
func (c *HTTPNBPClient) FetchRates(ctx context.Context, days int) ([]RateQuote, error) {
	var allQuotes []RateQuote

	// Query Table A (Major currencies)
	quotesA, err := c.fetchTable(ctx, "A", days)
	if err != nil {
		return nil, fmt.Errorf("failed fetching NBP table A: %w", err)
	}
	allQuotes = append(allQuotes, quotesA...)

	// Query Table B (Exotic currencies - best effort)
	quotesB, err := c.fetchTable(ctx, "B", days)
	if err == nil {
		allQuotes = append(allQuotes, quotesB...)
	}

	return allQuotes, nil
}

func (c *HTTPNBPClient) fetchTable(ctx context.Context, table string, days int) ([]RateQuote, error) {
	var endpoint string
	now := time.Now().UTC()

	if days <= 1 {
		endpoint = fmt.Sprintf("%s/%s/?format=json", c.BaseURL, table)
	} else {
		// NBP API date range accepts up to 93 days in format YYYY-MM-DD/YYYY-MM-DD
		if days > 90 {
			days = 90
		}
		startDate := now.AddDate(0, 0, -days).Format("2006-01-02")
		endDate := now.Format("2006-01-02")
		endpoint = fmt.Sprintf("%s/%s/%s/%s/?format=json", c.BaseURL, table, startDate, endDate)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Balance-App/1.0 (+https://github.com/balance/balance)")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// NBP returns 404 if no tables were published in that range (e.g. holidays)
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("NBP returned status %d: %s", resp.StatusCode, string(body))
	}

	return ParseNBPJSON(resp.Body)
}

// ParseNBPJSON decodes an NBP JSON response into RateQuotes.
func ParseNBPJSON(r io.Reader) ([]RateQuote, error) {
	var tables []nbpTableEntry
	decoder := json.NewDecoder(r)
	decoder.UseNumber()
	if err := decoder.Decode(&tables); err != nil {
		return nil, fmt.Errorf("failed decoding NBP JSON: %w", err)
	}

	var quotes []RateQuote
	for _, entry := range tables {
		if entry.EffectiveDate == "" {
			continue
		}
		rateDate, err := ParseDateBoundaryMicros(entry.EffectiveDate)
		if err != nil {
			continue
		}

		for _, item := range entry.Rates {
			code := strings.ToUpper(strings.TrimSpace(item.Code))
			if code == "" {
				continue
			}
			rateMicros, err := ParseDecimalMicros(item.Mid.String())
			if err != nil {
				continue
			}

			quotes = append(quotes, RateQuote{
				BaseCurrency:   code,
				TargetCurrency: "PLN",
				RateDate:       rateDate,
				RateMicros:     rateMicros,
				Source:         "NBP",
			})
		}
	}

	return quotes, nil
}
