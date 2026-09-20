package main

import (
	_ "embed"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// iconBytes is the vendor's own mark, cached in the repository so the icon route never
// depends on freebuff.com being reachable from the operator's browser.
//
// Source: https://freebuff.com/logo-icon.png, fetched 2026-09-20 (PNG, 5969 bytes).
//
//go:embed assets/icon.png
var iconBytes []byte

const (
	iconRoutePath   = "/icon"
	iconContentType = "image/png"
)

// iconResponse serves the cached mark at the plugin's own resource path.
//
// The route is deliberately same-origin and unauthenticated: the management panel loads
// provider marks as plain <img> sources, so a same-origin path always resolves, whereas a
// remote vendor URL can be blocked by region or network policy. The bytes are not a
// credential.
func iconResponse() pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type": []string{iconContentType},
			// The mark changes only when the plugin version changes, so a long cache is
			// safe and keeps the panel from refetching it on every page load.
			"Cache-Control": []string{"public, max-age=86400"},
		},
		Body: iconBytes,
	}
}
