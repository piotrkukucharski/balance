package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/balance/balance/internal/logger"
	"golang.org/x/net/html"
)

// FaviconCandidate represents a discovered favicon link with parsed priority.
type FaviconCandidate struct {
	URL      string
	Rel      string
	Sizes    string
	Priority int // Higher is better
	Width    int
}

// FaviconDiscoveryResult represents the API response for favicon lookup.
type FaviconDiscoveryResult struct {
	URL         string `json:"url"`
	BestFavicon string `json:"best_favicon"`
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
}

// DiscoverFavicon fetches an external URL, parses its HTML <link> tags, and returns the highest quality favicon URL.
func DiscoverFavicon(ctx context.Context, rawTargetURL string) (string, error) {
	if rawTargetURL == "" {
		return "", fmt.Errorf("URL cannot be empty")
	}

	targetURL := strings.TrimSpace(rawTargetURL)
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "https://" + targetURL
	}

	parsedURL, err := url.Parse(targetURL)
	if err != nil || parsedURL.Host == "" {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	// Security: validate host to mitigate SSRF
	hostname := parsedURL.Hostname()
	allowLoopback := os.Getenv("ALLOW_PRIVATE_FAVICON_HOSTS") == "true"
	if !allowLoopback && isPrivateHost(hostname) {
		return "", fmt.Errorf("access to internal/private hosts is forbidden")
	}

	// Direct image link check: if URL already points to an image/ico
	lowerPath := strings.ToLower(parsedURL.Path)
	if strings.HasSuffix(lowerPath, ".png") || strings.HasSuffix(lowerPath, ".ico") ||
		strings.HasSuffix(lowerPath, ".svg") || strings.HasSuffix(lowerPath, ".webp") ||
		strings.HasSuffix(lowerPath, ".jpg") || strings.HasSuffix(lowerPath, ".jpeg") {
		return parsedURL.String(), nil
	}

	// Safe HTTP Client with timeout and restricted transport
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   3 * time.Second,
				KeepAlive: 5 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 3 * time.Second,
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; BalanceFaviconBot/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		// Fallback to https://host/favicon.ico
		logger.Warn(ctx, "Failed fetching site for favicon discovery, using fallback", "host", parsedURL.Host, "error", err.Error())
		return fmt.Sprintf("%s://%s/favicon.ico", parsedURL.Scheme, parsedURL.Host), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Sprintf("%s://%s/favicon.ico", parsedURL.Scheme, parsedURL.Host), nil
	}

	contentType := resp.Header.Get("Content-Type")
	// If the server directly returned an image
	if strings.HasPrefix(contentType, "image/") {
		return resp.Request.URL.String(), nil
	}

	// Read up to 1MB of HTML body
	limitReader := io.LimitReader(resp.Body, 1024*1024)
	doc, err := html.Parse(limitReader)
	if err != nil {
		return fmt.Sprintf("%s://%s/favicon.ico", parsedURL.Scheme, parsedURL.Host), nil
	}

	candidates := extractFaviconCandidates(doc, resp.Request.URL)
	if len(candidates) == 0 {
		return fmt.Sprintf("%s://%s/favicon.ico", parsedURL.Scheme, parsedURL.Host), nil
	}

	// Choose candidate with highest priority
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.Priority > best.Priority {
			best = c
		}
	}
	return best.URL, nil
}

func extractFaviconCandidates(n *html.Node, baseURL *url.URL) []FaviconCandidate {
	var candidates []FaviconCandidate

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "link") {
			var rel, href, sizes string
			for _, attr := range node.Attr {
				switch strings.ToLower(attr.Key) {
				case "rel":
					rel = strings.ToLower(strings.TrimSpace(attr.Val))
				case "href":
					href = strings.TrimSpace(attr.Val)
				case "sizes":
					sizes = strings.ToLower(strings.TrimSpace(attr.Val))
				}
			}

			if isIconRel(rel) && href != "" {
				resolvedURL := resolveURL(baseURL, href)
				if resolvedURL != "" {
					priority, width := calculatePriority(rel, sizes, href)
					candidates = append(candidates, FaviconCandidate{
						URL:      resolvedURL,
						Rel:      rel,
						Sizes:    sizes,
						Priority: priority,
						Width:    width,
					})
				}
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(n)
	return candidates
}

func isIconRel(rel string) bool {
	parts := strings.Fields(rel)
	for _, p := range parts {
		if p == "icon" || p == "shortcut" || p == "apple-touch-icon" || p == "apple-touch-icon-precomposed" || p == "fluid-icon" {
			return true
		}
	}
	return false
}

func resolveURL(base *url.URL, href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(u)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	return resolved.String()
}

var sizeRegex = regexp.MustCompile(`(\d+)x(\d+)`)

func calculatePriority(rel, sizes, href string) (int, int) {
	priority := 10
	width := 0

	// Parse sizes="192x192" or "any"
	if sizes != "" {
		if sizes == "any" {
			priority += 50 // SVG vector icon
		} else {
			matches := sizeRegex.FindStringSubmatch(sizes)
			if len(matches) == 3 {
				w, _ := strconv.Atoi(matches[1])
				width = w
				// Prefer sizes between 64px and 256px
				if w >= 128 && w <= 512 {
					priority += 100 + w
				} else if w >= 64 && w < 128 {
					priority += 80 + w
				} else if w >= 32 && w < 64 {
					priority += 40 + w
				} else {
					priority += 10 + w/10
				}
			}
		}
	}

	// Apple touch icons are usually high resolution PNGs (180x180) if no size is specified
	if strings.Contains(rel, "apple-touch-icon") {
		if sizes == "" {
			width = 180
			priority += 80 // fallback apple touch icon
		}
	}

	// Vector SVG
	if strings.HasSuffix(strings.ToLower(href), ".svg") {
		priority += 120
	} else if strings.HasSuffix(strings.ToLower(href), ".png") {
		priority += 30
	} else if strings.HasSuffix(strings.ToLower(href), ".ico") {
		priority += 5 // ICO is usually low-res fallback
	}

	return priority, width
}

func isPrivateHost(hostname string) bool {
	if hostname == "localhost" || strings.HasSuffix(hostname, ".local") || strings.HasSuffix(hostname, ".internal") {
		return true
	}
	ip := net.ParseIP(hostname)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
