package web

import "embed"

//go:embed templates/*.html
var templateFS embed.FS

// staticFS holds the stylesheet and the vendored htmx (S7); nothing is
// loaded from anywhere else (S3).
//
//go:embed static
var staticFS embed.FS
