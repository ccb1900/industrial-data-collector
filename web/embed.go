// Package web embeds the built React UI so a single Go binary can serve the
// dashboard over plain HTTP (go:embed + net/http).
package web

import "embed"

// Dist is the embedded frontend build output (frontend/dist copied here).
// Regenerate with: (cd frontend && npm run build) && cp -R frontend/dist/. web/dist/
//
//go:embed all:dist
var Dist embed.FS
