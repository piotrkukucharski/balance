package templates

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"path"
	"reflect"
	"strings"

	"github.com/balance/balance/internal/i18n"
	"github.com/labstack/echo/v4"
)

//go:embed shared/* transactions/* members/* auth/* profile/* tokens/* history/* debug/* automations/* accounts/* parties/* reports/* workspace/*
var FS embed.FS

var (
	// baseTmpl holds all shared templates (shared/base.html, shared/components.html, etc.).
	// Each module page template clones baseTmpl so {{define "content"}} / {{block ...}}
	// inheritance (like Twig extends/block) is isolated per template file without name collisions.
	baseTmpl *template.Template

	// templateRegistry maps template names (both defined block names and file-based names
	// like "transactions/transactions.html") to their isolated *template.Template instance.
	templateRegistry = make(map[string]*template.Template)

	// logicalToHashedURL maps logical asset paths (e.g. "transactions/transactions.js")
	// to hashed public URLs (e.g. "/static/transactions/transactions.a1b2c3d4e5f6.js").
	logicalToHashedURL = make(map[string]string)

	// hashedPathToLogical maps URL paths stripped of "/static/" (e.g. "transactions/transactions.a1b2c3d4e5f6.js")
	// back to the original embedded file path ("transactions/transactions.js").
	hashedPathToLogical = make(map[string]string)

	// logicalToETag stores the ETag for each logical asset path.
	logicalToETag = make(map[string]string)
)

func init() {
	// 1. Walk embedded FS to compute SHA-256 content hashes for static assets (.js, .css)
	err := fs.WalkDir(FS, ".", func(filePath string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := path.Ext(filePath)
		if ext == ".js" || ext == ".css" {
			data, readErr := FS.ReadFile(filePath)
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(data)
			hashHex := hex.EncodeToString(sum[:6]) // 12 hex characters
			baseNoExt := strings.TrimSuffix(filePath, ext)
			hashedRelPath := fmt.Sprintf("%s.%s%s", baseNoExt, hashHex, ext)

			logicalToHashedURL[filePath] = "/static/" + hashedRelPath
			hashedPathToLogical[hashedRelPath] = filePath
			logicalToETag[filePath] = fmt.Sprintf(`"%s"`, hashHex)
		}
		return nil
	})
	if err != nil {
		panic("failed to initialize embedded static assets: " + err.Error())
	}

	funcMap := template.FuncMap{
		"staticPath": StaticPath,
		"t": func(locale any, key string, args ...any) string {
			locStr := ""
			if s, ok := locale.(string); ok {
				locStr = s
			}
			return i18n.T(locStr, key, args...)
		},
		"formatMoney": func(amountMinor int64, currency string, locale any, customFormat any) string {
			locStr := ""
			if s, ok := locale.(string); ok {
				locStr = s
			}
			custStr := ""
			if s, ok := customFormat.(string); ok {
				custStr = s
			}
			return i18n.FormatMoneyMinor(amountMinor, currency, locStr, custStr)
		},
		"localeDefaults": func(locale any) i18n.LocaleDefaults {
			locStr := ""
			if s, ok := locale.(string); ok {
				locStr = s
			}
			return i18n.GetLocaleDefaults(locStr)
		},
		"dict": func(values ...any) (map[string]any, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict requires even number of arguments")
			}
			m := make(map[string]any, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				m[key] = values[i+1]
			}
			return m, nil
		},
		"field": func(data any, fieldName string) any {
			if data == nil {
				return nil
			}
			v := reflect.ValueOf(data)
			for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
				if v.IsNil() {
					return nil
				}
				v = v.Elem()
			}
			if v.Kind() == reflect.Map {
				kv := reflect.ValueOf(fieldName)
				mv := v.MapIndex(kv)
				if mv.IsValid() {
					return mv.Interface()
				}
				return nil
			}
			if v.Kind() == reflect.Struct {
				f := v.FieldByName(fieldName)
				if f.IsValid() {
					return f.Interface()
				}
			}
			return nil
		},
	}

	// 2. Parse shared base & component templates first
	shared := template.New("").Funcs(funcMap)
	err = fs.WalkDir(FS, "shared", func(filePath string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path.Ext(filePath) == ".html" {
			data, readErr := FS.ReadFile(filePath)
			if readErr != nil {
				return readErr
			}
			if _, parseErr := shared.Parse(string(data)); parseErr != nil {
				return fmt.Errorf("parsing shared template %s: %w", filePath, parseErr)
			}
		}
		return nil
	})
	if err != nil {
		panic("failed to parse shared HTML templates: " + err.Error())
	}
	baseTmpl = shared

	// Register all shared templates in the registry
	for _, st := range baseTmpl.Templates() {
		if st.Name() != "" {
			templateRegistry[st.Name()] = baseTmpl
		}
	}

	// 3. Parse each module .html file into its own cloned copy of baseTmpl
	// This enables Twig-like {{template "shared/base" .}} + {{define "content"}} per file!
	sharedNames := make(map[string]bool)
	for _, st := range baseTmpl.Templates() {
		sharedNames[st.Name()] = true
	}

	err = fs.WalkDir(FS, ".", func(filePath string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(filePath, "shared/") || path.Ext(filePath) != ".html" {
			return nil
		}

		clone, cloneErr := baseTmpl.Clone()
		if cloneErr != nil {
			return fmt.Errorf("cloning base template for %s: %w", filePath, cloneErr)
		}

		data, readErr := FS.ReadFile(filePath)
		if readErr != nil {
			return readErr
		}

		if _, parseErr := clone.Parse(string(data)); parseErr != nil {
			return fmt.Errorf("parsing module template %s: %w", filePath, parseErr)
		}

		// Register by file path (e.g. "transactions/transactions.html") and without extension
		templateRegistry[filePath] = clone
		templateRegistry[strings.TrimSuffix(filePath, ".html")] = clone

		// Also register any newly defined {{define "..."}} templates inside this file
		// (except block overrides like "title", "content", "modals", "scripts", etc.)
		reservedBlocks := map[string]bool{
			"theme": true, "title": true, "head_extra": true,
			"body_start": true, "navbar_start_extra": true, "navbar_end": true,
			"sidebar_extra": true, "content": true, "modals": true, "scripts": true,
		}
		for _, dt := range clone.Templates() {
			name := dt.Name()
			if name != "" && !sharedNames[name] && !reservedBlocks[name] {
				templateRegistry[name] = clone
			}
		}
		return nil
	})
	if err != nil {
		panic("failed to parse module HTML templates: " + err.Error())
	}
}

// StaticPath returns the content-hashed URL for a given logical asset path
// (e.g., "transactions/transactions.js" -> "/static/transactions/transactions.<hash>.js").
func StaticPath(logicalPath string) string {
	logicalPath = strings.TrimPrefix(logicalPath, "/")
	if hashedURL, ok := logicalToHashedURL[logicalPath]; ok {
		return hashedURL
	}
	return "/static/" + logicalPath
}

// ReadStaticFile returns the raw bytes of an embedded file by its logical path.
func ReadStaticFile(logicalPath string) ([]byte, error) {
	return FS.ReadFile(strings.TrimPrefix(logicalPath, "/"))
}

// Render executes the named template and writes the output to w.
func Render(w io.Writer, name string, data any) error {
	t, ok := templateRegistry[name]
	if !ok {
		return fmt.Errorf("template %q not found in registry", name)
	}
	// If the caller passed a file path (e.g. "transactions/transactions.html") that extends "shared/base",
	// and there isn't a template literally named after the file path, execute "shared/base".
	if t.Lookup(name) != nil {
		return t.ExecuteTemplate(w, name, data)
	}
	return t.ExecuteTemplate(w, "shared/base", data)
}

// RenderString executes the named template and returns the rendered string.
func RenderString(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := Render(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RegisterStaticRoutes registers the GET /static/* endpoint on the Echo instance
// to serve embedded .js and .css files with CDN-friendly immutable caching headers.
func RegisterStaticRoutes(e *echo.Echo) {
	e.GET("/static/*", ServeStaticAsset)
}

// ServeStaticAsset handles GET /static/* requests.
func ServeStaticAsset(c echo.Context) error {
	reqPath := strings.TrimPrefix(c.Param("*"), "/")
	if reqPath == "" {
		reqPath = strings.TrimPrefix(c.Request().URL.Path, "/static/")
	}

	ext := path.Ext(reqPath)
	if ext != ".js" && ext != ".css" {
		return echo.NewHTTPError(http.StatusNotFound, "static asset not found")
	}

	isHashed := false
	logicalPath, ok := hashedPathToLogical[reqPath]
	if ok {
		isHashed = true
	} else {
		logicalPath = reqPath
	}

	data, err := FS.ReadFile(logicalPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "static asset not found")
	}

	contentType := "application/octet-stream"
	switch ext {
	case ".js":
		contentType = "application/javascript; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	}

	if etag, hasETag := logicalToETag[logicalPath]; hasETag {
		c.Response().Header().Set("ETag", etag)
		if c.Request().Header.Get("If-None-Match") == etag {
			return c.NoContent(http.StatusNotModified)
		}
	}

	if isHashed {
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Response().Header().Set("Cache-Control", "public, max-age=3600")
	}

	return c.Blob(http.StatusOK, contentType, data)
}
