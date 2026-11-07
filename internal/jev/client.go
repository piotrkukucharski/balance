package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Common domain errors returned by JevClient.
var (
	ErrUnauthorized    = errors.New("jev: unauthorized (invalid or missing api key)")
	ErrRateLimited     = errors.New("jev: rate limited by system one API")
	ErrJevTimeout      = errors.New("jev: request timed out")
	ErrJevServiceDown  = errors.New("jev: system one service unavailable")
	ErrInvalidResponse = errors.New("jev: invalid response payload from system one API")
)

// JevClient defines the interface for making System One classifications.
type JevClient interface {
	Choice(ctx context.Context, question string, candidates []string, contextData map[string]any) (string, float64, error)
	Noul(ctx context.Context, question string, contextData map[string]any) (float64, float64, error)
}

// HTTPClient implements JevClient using the TypeSafe System One HTTP API.
type HTTPClient struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
	model      string
}

// Config holds configuration for HTTPClient.
type Config struct {
	Endpoint string
	APIKey   string
	Timeout  time.Duration
	Model    string
}

// NewHTTPClient creates a new HTTPClient with hardened timeouts and connection pooling.
func NewHTTPClient(cfg Config) *HTTPClient {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://gateway-us.pydantic.dev/proxy/typesafe/v1/systemone"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 1000 * time.Millisecond
	}
	model := cfg.Model
	if model == "" {
		model = "jev-latest"
	}

	transport := &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	}

	return &HTTPClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   cfg.APIKey,
		model:    model,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
	}
}

type systemOneRequest struct {
	Model     string                    `json:"model"`
	State     any                       `json:"state"`
	Questions map[string]questionConfig `json:"questions"`
}

type questionConfig struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type systemOneResponse struct {
	Model   string                   `json:"model"`
	Answers map[string]answerPayload `json:"answers"`
	Error   string                   `json:"error,omitempty"`
}

type answerPayload struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
}

func sanitizeError(err error, apiKey string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if apiKey != "" {
		msg = strings.ReplaceAll(msg, apiKey, "[REDACTED]")
	}
	// Redact bearer tokens if present in error strings
	if strings.Contains(msg, "Bearer ") {
		parts := strings.Split(msg, "Bearer ")
		for i := 1; i < len(parts); i++ {
			subParts := strings.Fields(parts[i])
			if len(subParts) > 0 {
				msg = strings.ReplaceAll(msg, "Bearer "+subParts[0], "Bearer [REDACTED]")
			}
		}
	}
	return errors.New(msg)
}

// Choice selects the best matching candidate from the list.
func (c *HTTPClient) Choice(ctx context.Context, question string, candidates []string, contextData map[string]any) (string, float64, error) {
	if len(candidates) == 0 {
		return "", 0.0, errors.New("jev: choice requires at least one candidate")
	}

	criteria := make(map[string]string, len(candidates))
	for _, cand := range candidates {
		criteria[cand] = cand
	}

	reqBody := systemOneRequest{
		Model: c.model,
		State: contextData,
		Questions: map[string]questionConfig{
			"q": {
				Type:         "choice",
				Instructions: question,
				Criteria:     criteria,
			},
		},
	}

	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		if errors.Is(err, ErrJevTimeout) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrRateLimited) || errors.Is(err, ErrJevServiceDown) {
			return "", 0.0, err
		}
		return "", 0.0, sanitizeError(err, c.apiKey)
	}

	ans, ok := resp.Answers["q"]
	if !ok {
		return "", 0.0, ErrInvalidResponse
	}

	conf := ans.Confidence
	if conf == 0 && len(ans.Probabilities) > 0 {
		conf = ans.Probabilities[ans.Choice]
	}

	return ans.Choice, conf, nil
}

// Noul evaluates a yes/no hypothesis and returns the probability (0.0..1.0) and confidence.
func (c *HTTPClient) Noul(ctx context.Context, question string, contextData map[string]any) (float64, float64, error) {
	reqBody := systemOneRequest{
		Model: c.model,
		State: contextData,
		Questions: map[string]questionConfig{
			"q": {
				Type:         "noul",
				Instructions: question,
			},
		},
	}

	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		if errors.Is(err, ErrJevTimeout) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrRateLimited) || errors.Is(err, ErrJevServiceDown) {
			return 0.0, 0.0, err
		}
		return 0.0, 0.0, sanitizeError(err, c.apiKey)
	}

	ans, ok := resp.Answers["q"]
	if !ok || ans.Noul == nil {
		return 0.0, 0.0, ErrInvalidResponse
	}

	prob := *ans.Noul
	conf := ans.Confidence
	if conf == 0 {
		conf = prob
	}

	return prob, conf, nil
}

func (c *HTTPClient) doRequest(ctx context.Context, payload systemOneRequest) (*systemOneResponse, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jev: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("jev: failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || osIsTimeout(err) {
			return nil, ErrJevTimeout
		}
		return nil, fmt.Errorf("jev: network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode >= 500 {
		return nil, ErrJevServiceDown
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("jev: api returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var res systemOneResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, ErrInvalidResponse
	}

	return &res, nil
}

func osIsTimeout(err error) bool {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
