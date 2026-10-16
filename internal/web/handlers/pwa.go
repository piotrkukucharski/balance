package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type PWAHandler struct{}

func NewPWAHandler() *PWAHandler {
	return &PWAHandler{}
}

// Manifest returns the Web App Manifest conforming to spec 3.6.
func (p *PWAHandler) Manifest(c echo.Context) error {
	manifestJSON := `{
  "name": "Balance",
  "short_name": "Balance",
  "description": "Envelope Budgeting & Financial Operating System",
  "start_url": "/budgets",
  "display": "standalone",
  "background_color": "#1d232a",
  "theme_color": "#6419e6",
  "icons": [
    {
      "src": "data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><text y='.9em' font-size='90'>⚖️</text></svg>",
      "sizes": "192x192 512x512",
      "type": "image/svg+xml",
      "purpose": "any maskable"
    }
  ]
}`
	return c.Blob(http.StatusOK, "application/manifest+json", []byte(manifestJSON))
}

// ServiceWorker returns pass-through NetworkOnly service worker with offline banner fallback.
func (p *PWAHandler) ServiceWorker(c echo.Context) error {
	swJS := `// Pass-Through Service Worker (Strictly NetworkOnly - No Offline Financial Data Caching)
self.addEventListener('install', (event) => {
    self.skipWaiting();
});

self.addEventListener('activate', (event) => {
    event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
    event.respondWith(
        fetch(event.request).catch(() => {
            return new Response(
                '<!DOCTYPE html><html><body style=\"font-family:sans-serif;text-align:center;padding:50px;\"><h2>No Connection</h2><p>Balance requires an active server connection.</p><button onclick=\"location.reload()\">Retry</button></body></html>',
                { headers: { 'Content-Type': 'text/html' } }
            );
        })
    );
});`
	return c.Blob(http.StatusOK, "application/javascript", []byte(swJS))
}
