package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverFaviconHTML(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_FAVICON_HOSTS", "true")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		html := `<!DOCTYPE html>
<html>
<head>
    <link rel="shortcut icon" href="/favicon.ico">
    <link rel="icon" type="image/png" sizes="32x32" href="/icons/favicon-32x32.png">
    <link rel="icon" type="image/png" sizes="192x192" href="/icons/android-chrome-192x192.png">
    <link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png">
</head>
<body><h1>Bank Test</h1></body>
</html>`
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer ts.Close()

	ctx := context.Background()
	bestFavicon, err := DiscoverFavicon(ctx, ts.URL)
	if err != nil {
		t.Fatalf("unexpected error discovering favicon: %v", err)
	}

	if !strings.HasSuffix(bestFavicon, "android-chrome-192x192.png") {
		t.Errorf("expected 192x192 icon to win, got: %s", bestFavicon)
	}
}

func TestDiscoverFaviconSVGPreference(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_FAVICON_HOSTS", "true")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		html := `<!DOCTYPE html>
<html>
<head>
    <link rel="icon" href="/favicon.ico">
    <link rel="icon" href="/logo.svg" sizes="any">
</head>
<body><h1>Fintech</h1></body>
</html>`
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer ts.Close()

	ctx := context.Background()
	bestFavicon, err := DiscoverFavicon(ctx, ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasSuffix(bestFavicon, "logo.svg") {
		t.Errorf("expected svg icon to win, got: %s", bestFavicon)
	}
}

func TestDiscoverFaviconDirectImage(t *testing.T) {
	ctx := context.Background()
	direct := "https://example.com/brand/logo.png"
	res, err := DiscoverFavicon(ctx, direct)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != direct {
		t.Errorf("expected direct URL, got %s", res)
	}
}
