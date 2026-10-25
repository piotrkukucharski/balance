package fx

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ECBClient defines the interface for fetching exchange rates from the European Central Bank.
type ECBClient interface {
	FetchRates(ctx context.Context, days int) ([]RateQuote, error)
}

// HTTPClient represents a standard HTTP executor.
type HTTPRequestDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPECBClient is the production implementation fetching ECB XML feeds.
type HTTPECBClient struct {
	BaseURL    string
	HTTPClient HTTPRequestDoer
}

// NewHTTPECBClient creates a new client targeting ECB XML endpoints.
func NewHTTPECBClient(baseURL string, client HTTPRequestDoer) *HTTPECBClient {
	if baseURL == "" {
		baseURL = "https://www.ecb.europa.eu/stats/eurofxref"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPECBClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: client,
	}
}

// XML structures for ECB eurofxref
type ecbEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Cube    ecbCubeRoot `xml:"Cube"`
}

type ecbCubeRoot struct {
	TimeCubes []ecbTimeCube `xml:"Cube"`
}

type ecbTimeCube struct {
	Time      string         `xml:"time,attr"`
	RateCubes []ecbRateCube `xml:"Cube"`
}

type ecbRateCube struct {
	Currency string `xml:"currency,attr"`
	Rate     string `xml:"rate,attr"`
}

// FetchRates fetches exchange rates from ECB. If days > 1, fetches the 90-day history feed; otherwise daily.
func (c *HTTPECBClient) FetchRates(ctx context.Context, days int) ([]RateQuote, error) {
	endpoint := c.BaseURL + "/eurofxref-daily.xml"
	if days > 1 {
		endpoint = c.BaseURL + "/eurofxref-hist-90d.xml"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating ECB request: %w", err)
	}
	req.Header.Set("User-Agent", "Balance-App/1.0 (+https://github.com/balance/balance)")
	req.Header.Set("Accept", "application/xml, text/xml")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ECB feed request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ECB feed returned status %d: %s", resp.StatusCode, string(body))
	}

	return ParseECBXML(resp.Body)
}

// ParseECBXML parses an ECB eurofxref XML stream into a slice of RateQuotes.
func ParseECBXML(r io.Reader) ([]RateQuote, error) {
	var env ecbEnvelope
	decoder := xml.NewDecoder(r)
	if err := decoder.Decode(&env); err != nil {
		return nil, fmt.Errorf("failed decoding ECB XML: %w", err)
	}

	var quotes []RateQuote
	for _, tc := range env.Cube.TimeCubes {
		if tc.Time == "" {
			continue
		}
		rateDate, err := ParseDateBoundaryMicros(tc.Time)
		if err != nil {
			continue
		}

		for _, rc := range tc.RateCubes {
			curr := strings.ToUpper(strings.TrimSpace(rc.Currency))
			if curr == "" {
				continue
			}
			rateMicros, err := ParseDecimalMicros(rc.Rate)
			if err != nil {
				continue
			}

			quotes = append(quotes, RateQuote{
				BaseCurrency:   "EUR",
				TargetCurrency: curr,
				RateDate:       rateDate,
				RateMicros:     rateMicros,
				Source:         "ECB",
			})
		}
	}

	return quotes, nil
}
