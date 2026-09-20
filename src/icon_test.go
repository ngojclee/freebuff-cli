package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestIconRouteServesTheVendorMark pins the same-origin icon route: the management panel
// loads provider marks as plain <img> sources, so this route has to exist, carry an image
// content type and return real bytes.
func TestIconRouteServesTheVendorMark(t *testing.T) {
	handler := &managementHandler{runtime: newRuntime(DefaultConfig())}
	resp, errHandle := handler.HandleManagement(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource" + managementBasePath + iconRoutePath,
	})
	if errHandle != nil {
		t.Fatalf("icon route: %v", errHandle)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Headers.Get("Content-Type"); got != iconContentType {
		t.Fatalf("content type = %q, want %q", got, iconContentType)
	}
	if len(resp.Body) < 100 {
		t.Fatalf("icon body is %d bytes, want the real mark", len(resp.Body))
	}
}

// TestIconRouteIsRegisteredAsAResource keeps the route reachable without the management
// key, which is what lets the panel load it as an image.
func TestIconRouteIsRegisteredAsAResource(t *testing.T) {
	registration := registerManagementRoutes()
	found := false
	for _, resource := range registration.Resources {
		if resource.Path == iconRoutePath {
			found = true
		}
		if resource.Path == "/" {
			t.Fatalf("the host rejects \"/\" as a resource path, got %+v", registration.Resources)
		}
	}
	if !found {
		t.Fatalf("resource route %q missing from the registration", iconRoutePath)
	}
}

// TestLogoURLIsSameOrigin guards against the mark drifting back to a vendor URL, which can
// be unreachable from the operator's browser.
func TestLogoURLIsSameOrigin(t *testing.T) {
	if !strings.HasPrefix(logoURL, "/v0/resource/plugins/") {
		t.Fatalf("logoURL = %q, want a same-origin plugin resource path", logoURL)
	}
	if !strings.HasSuffix(logoURL, iconRoutePath) {
		t.Fatalf("logoURL = %q, want it to point at the icon route", logoURL)
	}
}
