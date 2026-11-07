package config_test

import (
	"testing"

	"github.com/balance/balance/internal/config"
)

func TestGetPublicBaseURL(t *testing.T) {
	tests := []struct {
		name         string
		appPublicURL string
		publicBase   string
		port         string
		fallbackPort string
		expected     string
	}{
		{
			name:         "Default fallback when unset",
			appPublicURL: "",
			publicBase:   "",
			port:         "",
			fallbackPort: "",
			expected:     "http://localhost:8080",
		},
		{
			name:         "Default fallback with custom PORT env",
			appPublicURL: "",
			publicBase:   "",
			port:         "9090",
			fallbackPort: "",
			expected:     "http://localhost:9090",
		},
		{
			name:         "Default fallback with fallbackPort parameter",
			appPublicURL: "",
			publicBase:   "",
			port:         "9090",
			fallbackPort: "3000",
			expected:     "http://localhost:3000",
		},
		{
			name:         "APP_PUBLIC_URL set with https and trailing slash",
			appPublicURL: "https://balance.example.com/",
			publicBase:   "",
			expected:     "https://balance.example.com",
		},
		{
			name:         "APP_PUBLIC_URL set with port and subpath",
			appPublicURL: "https://balance.example.com:8443/subapp/",
			publicBase:   "",
			expected:     "https://balance.example.com:8443/subapp",
		},
		{
			name:         "APP_PUBLIC_URL set without scheme (domain)",
			appPublicURL: "balance.example.com",
			publicBase:   "",
			expected:     "https://balance.example.com",
		},
		{
			name:         "APP_PUBLIC_URL set without scheme (localhost)",
			appPublicURL: "localhost:10317",
			publicBase:   "",
			expected:     "http://localhost:10317",
		},
		{
			name:         "Fallback to PUBLIC_BASE_URL if APP_PUBLIC_URL is empty",
			appPublicURL: "",
			publicBase:   "https://legacy.example.com",
			expected:     "https://legacy.example.com",
		},
		{
			name:         "APP_PUBLIC_URL takes precedence over PUBLIC_BASE_URL",
			appPublicURL: "https://primary.example.com",
			publicBase:   "https://secondary.example.com",
			expected:     "https://primary.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_PUBLIC_URL", tt.appPublicURL)
			t.Setenv("PUBLIC_BASE_URL", tt.publicBase)
			t.Setenv("PORT", tt.port)

			got := config.GetPublicBaseURL(tt.fallbackPort)
			if got != tt.expected {
				t.Errorf("GetPublicBaseURL(%q) = %q, expected %q", tt.fallbackPort, got, tt.expected)
			}
		})
	}
}

func TestBuildPublicURL(t *testing.T) {
	t.Setenv("APP_PUBLIC_URL", "https://balance.ptr.ovh")

	tests := []struct {
		subpath  string
		expected string
	}{
		{"", "https://balance.ptr.ovh"},
		{"/", "https://balance.ptr.ovh"},
		{"/api/v1/budgets", "https://balance.ptr.ovh/api/v1/budgets"},
		{"api/docs", "https://balance.ptr.ovh/api/docs"},
	}

	for _, tt := range tests {
		got := config.BuildPublicURL("", tt.subpath)
		if got != tt.expected {
			t.Errorf("BuildPublicURL(\"\", %q) = %q, expected %q", tt.subpath, got, tt.expected)
		}
	}
}
