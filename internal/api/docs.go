package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"
)

const swaggerDocsHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Balance Personal Finance REST API — Swagger UI</title>
    <link rel="icon" type="image/x-icon" href="/favicon.ico" />
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css" />
    <style>
      html {
        box-sizing: border-box;
        overflow: -moz-scrollbars-vertical;
        overflow-y: scroll;
      }
      *, *:before, *:after {
        box-sizing: inherit;
      }
      body {
        margin: 0;
        background: #fafafa;
      }
      .swagger-ui .topbar {
        display: none;
      }
    </style>
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js" charset="UTF-8"></script>
    <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-standalone-preset.js" charset="UTF-8"></script>
    <script>
      window.onload = function() {
        window.ui = SwaggerUIBundle({
          url: "/api/openapi.json",
          dom_id: "#swagger-ui",
          deepLinking: true,
          presets: [
            SwaggerUIBundle.presets.apis,
            SwaggerUIStandalonePreset
          ],
          plugins: [
            SwaggerUIBundle.plugins.DownloadUrl
          ],
          layout: "StandaloneLayout",
          persistAuthorization: true
        });
      };
    </script>
  </body>
</html>`

// GetRawSpecYAML returns the embedded OpenAPI 3.1 YAML specification bytes.
func GetRawSpecYAML() ([]byte, error) {
	jsonBytes, err := rawSpec()
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		return nil, err
	}
	return yaml.Marshal(doc)
}

// GetSpecJSON returns the embedded OpenAPI 3.1 specification serialized as JSON.
func GetSpecJSON() ([]byte, error) {
	return rawSpec()
}

// ServeAPIDocs serves the interactive Swagger UI at /api/docs.
func ServeAPIDocs(c echo.Context) error {
	return c.HTML(http.StatusOK, swaggerDocsHTML)
}

// ServeOpenAPIYAML serves the raw OpenAPI 3.1 YAML contract at /api/openapi.yaml.
func ServeOpenAPIYAML(c echo.Context) error {
	data, err := GetRawSpecYAML()
	if err != nil {
		return sendError(c, http.StatusInternalServerError, "Failed loading OpenAPI YAML specification", "INTERNAL_ERROR", nil)
	}
	return c.Blob(http.StatusOK, "application/yaml; charset=utf-8", data)
}

// ServeOpenAPIJSON serves the OpenAPI 3.1 JSON contract at /api/openapi.json.
func ServeOpenAPIJSON(c echo.Context) error {
	data, err := GetSpecJSON()
	if err != nil {
		return sendError(c, http.StatusInternalServerError, "Failed loading OpenAPI JSON specification", "INTERNAL_ERROR", nil)
	}
	return c.Blob(http.StatusOK, "application/json; charset=utf-8", data)
}

// RegisterDocsRoutes mounts the public OpenAPI documentation and schema endpoints on Echo.
func RegisterDocsRoutes(e *echo.Echo) {
	e.GET("/api/docs", ServeAPIDocs)
	e.GET("/api/docs/", ServeAPIDocs)
	e.GET("/api/openapi.yaml", ServeOpenAPIYAML)
	e.GET("/api/openapi.json", ServeOpenAPIJSON)
}

// WrapAPIAuthMiddleware wraps SessionOrBearerAuthMiddleware for /api/v1 routes,
// skipping public auth endpoints (/api/v1/auth/login, /api/v1/auth/register)
// and converting any unhandled *echo.HTTPError into a standardized OpenAPI ErrorResponse JSON payload.
func WrapAPIAuthMiddleware(authMiddleware echo.MiddlewareFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		protectedNext := authMiddleware(next)
		return func(c echo.Context) error {
			path := c.Request().URL.Path
			var err error
			if path == "/api/v1/auth/login" || path == "/api/v1/auth/register" {
				err = next(c)
			} else {
				err = protectedNext(c)
			}
			if err != nil && !c.Response().Committed {
				status := http.StatusInternalServerError
				msg := err.Error()
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
					if he.Message != nil {
						msg = fmt.Sprint(he.Message)
					}
				}
				code := statusToErrorCode(status)
				return sendError(c, status, msg, code, nil)
			}
			return err
		}
	}
}

func statusToErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_INPUT"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	default:
		return "INTERNAL_ERROR"
	}
}
