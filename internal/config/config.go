package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// GetPublicBaseURL returns the configured public base URL of the application.
// It checks APP_PUBLIC_URL (and falls back to PUBLIC_BASE_URL if set).
// If empty, it defaults to "http://localhost:" + fallbackPort (or "http://localhost:8080" if port is empty).
// The returned URL is guaranteed to not have a trailing slash.
func GetPublicBaseURL(fallbackPort string) string {
	raw := strings.TrimSpace(os.Getenv("APP_PUBLIC_URL"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
	}
	if raw == "" {
		port := strings.TrimSpace(fallbackPort)
		if port == "" {
			port = strings.TrimSpace(os.Getenv("PORT"))
		}
		if port == "" {
			port = "8080"
		}
		return fmt.Sprintf("http://localhost:%s", port)
	}

	// If scheme is missing, default to https:// (or http:// for localhost/127.0.0.1)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		if strings.HasPrefix(raw, "localhost") || strings.HasPrefix(raw, "127.0.0.1") {
			raw = "http://" + raw
		} else {
			raw = "https://" + raw
		}
	}

	// Parse to validate and clean up
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		// If unparseable, return trimmed original without trailing slash
		return strings.TrimRight(raw, "/")
	}

	// Format cleaned URL: scheme://host[:port][path]
	cleaned := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	if parsed.Path != "" && parsed.Path != "/" {
		cleaned += strings.TrimRight(parsed.Path, "/")
	}
	return cleaned
}

// BuildPublicURL constructs an absolute public URL for the given subpath.
// subpath can be with or without a leading slash.
func BuildPublicURL(fallbackPort string, subpath string) string {
	base := GetPublicBaseURL(fallbackPort)
	if subpath == "" || subpath == "/" {
		return base
	}
	if !strings.HasPrefix(subpath, "/") {
		subpath = "/" + subpath
	}
	return base + subpath
}

// DefaultJevEndpoint is the default API gateway for TypeSafe Jev System One.
const DefaultJevEndpoint = "https://gateway-us.pydantic.dev/proxy/typesafe/v1/systemone"

// GetJevAPIKey returns the configured API key for Jev from JEV_API_KEY.
func GetJevAPIKey() string {
	return strings.TrimSpace(os.Getenv("JEV_API_KEY"))
}

// GetJevEndpoint returns the configured endpoint for Jev from JEV_ENDPOINT,
// falling back to DefaultJevEndpoint.
func GetJevEndpoint() string {
	endpoint := strings.TrimSpace(os.Getenv("JEV_ENDPOINT"))
	if endpoint == "" {
		return DefaultJevEndpoint
	}
	return strings.TrimRight(endpoint, "/")
}

