package assets

import (
	_ "embed"
)

// FaviconICO contains the embedded binary content of favicon.ico.
//
//go:embed favicon.ico
var FaviconICO []byte
