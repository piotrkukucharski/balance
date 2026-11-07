package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPClient_Choice_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req systemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Questions["q"].Type != "choice" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp := systemOneResponse{
			Model: "jev-latest",
			Answers: map[string]answerPayload{
				"q": {
					Type:       "choice",
					Choice:     "Transport",
					Confidence: 0.95,
					Probabilities: map[string]float64{
						"Transport": 0.95,
						"Food":      0.05,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewHTTPClient(Config{
		Endpoint: server.URL,
		APIKey:   "secret-token-123",
		Timeout:  time.Second,
	})

	choice, conf, err := client.Choice(context.Background(), "Categorize transaction", []string{"Transport", "Food"}, map[string]any{"note": "Uber"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != "Transport" {
		t.Errorf("expected choice 'Transport', got %q", choice)
	}
	if conf != 0.95 {
		t.Errorf("expected conf 0.95, got %f", conf)
	}
}

func TestHTTPClient_Noul_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prob := 0.88
		resp := systemOneResponse{
			Model: "jev-latest",
			Answers: map[string]answerPayload{
				"q": {
					Type:       "noul",
					Noul:       &prob,
					Confidence: 0.91,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewHTTPClient(Config{
		Endpoint: server.URL,
		APIKey:   "test-key",
		Timeout:  time.Second,
	})

	prob, conf, err := client.Noul(context.Background(), "Is recurring?", map[string]any{"note": "Spotify"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prob != 0.88 {
		t.Errorf("expected prob 0.88, got %f", prob)
	}
	if conf != 0.91 {
		t.Errorf("expected conf 0.91, got %f", conf)
	}
}

func TestHTTPClient_ErrorSanitizationAndRedaction(t *testing.T) {
	secretKey := "super-secret-key-xyz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal database error with Bearer " + secretKey))
	}))
	defer server.Close()

	client := NewHTTPClient(Config{
		Endpoint: server.URL,
		APIKey:   secretKey,
		Timeout:  time.Second,
	})

	_, _, err := client.Choice(context.Background(), "test", []string{"a", "b"}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, ErrJevServiceDown) {
		// Server down error expected for 500
		t.Logf("got error: %v", err)
	}

	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("error exposed secret key: %s", err.Error())
	}
}

func TestHTTPClient_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewHTTPClient(Config{
		Endpoint: server.URL,
		APIKey:   "key",
		Timeout:  20 * time.Millisecond,
	})

	_, _, err := client.Choice(context.Background(), "test", []string{"a"}, nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrJevTimeout) && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}
