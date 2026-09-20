package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const managementBasePath = "/plugins/" + pluginID

// managementHandler serves the plugin's own routes: a small JSON API plus the embedded
// dashboard. It never returns a token.
type managementHandler struct {
	runtime *pluginRuntime
}

func registerManagementRoutes() pluginapi.ManagementRegistrationResponse {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			// The refresh action stays behind management auth: it is the only route that
			// makes the plugin talk to the network on demand.
			{Method: http.MethodPost, Path: managementBasePath + "/models/refresh"},
		},
		Resources: []pluginapi.ResourceRoute{
			// Resource paths must be non-empty; the host rejects "/" as an invalid route,
			// which is why the dashboard is registered as index.html. These routes are
			// readable without the management key, so they must never carry a token.
			{
				Path:        "/index.html",
				Menu:        pluginName,
				Description: "Freebuff account, model and waiting-room state.",
			},
			{
				Path:        "/status",
				Description: "Redacted plugin state as JSON.",
			},
			{
				Path:        "/accounts",
				Description: "Known Freebuff accounts as JSON, labels only.",
			},
			{
				Path:        "/models",
				Description: "Published and upstream model lists as JSON.",
			},
			{
				Path:        iconRoutePath,
				Description: "The Freebuff mark, served same-origin so the management panel always resolves it.",
			},
		},
	}
}

func (h *managementHandler) HandleManagement(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	path := strings.TrimSpace(req.Path)
	if index := strings.Index(path, managementBasePath); index >= 0 {
		path = path[index+len(managementBasePath):]
	}
	path = "/" + strings.Trim(strings.TrimSuffix(path, "/"), "/")
	if path == "/" || path == "/index.html" {
		return h.dashboard()
	}

	switch {
	case req.Method == http.MethodGet && path == iconRoutePath:
		return iconResponse(), nil
	case req.Method == http.MethodGet && path == "/status":
		return jsonResponse(http.StatusOK, h.status())
	case req.Method == http.MethodGet && path == "/accounts":
		return jsonResponse(http.StatusOK, map[string]any{"accounts": h.accounts()})
	case req.Method == http.MethodGet && path == "/models":
		return jsonResponse(http.StatusOK, h.models())
	case req.Method == http.MethodPost && path == "/models/refresh":
		cfg := h.runtime.settings.get()
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if errRefresh := h.runtime.registry.Refresh(refreshCtx); errRefresh != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{
				"ok":     false,
				"error":  redactReason(errRefresh.Error()),
				"models": h.models(),
			})
		}
		_ = cfg
		return jsonResponse(http.StatusOK, map[string]any{"ok": true, "models": h.models()})
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "unknown route"})
	}
}

// status is the redacted operational view. It carries counts and timestamps only.
func (h *managementHandler) status() map[string]any {
	cfg := h.runtime.settings.get()
	registry := map[string]any{}
	if h.runtime.registry != nil {
		registry = h.runtime.registry.Snapshot()
	}
	return map[string]any{
		"plugin":               pluginID,
		"version":              pluginVersion,
		"enabled":              cfg.Enabled,
		"auth_dir":             redactPath(cfg.ResolvedAuthDir()),
		"upstream_base_url":    cfg.UpstreamBaseURL,
		"model_alias_prefix":   cfg.ModelAliasPrefix,
		"known_accounts":       accountStore.size(),
		"rotation_seconds":     cfg.RotationIntervalSeconds,
		"refresh_seconds":      cfg.ModelRefreshSeconds,
		"waiting_room_seconds": cfg.WaitingRoomTimeoutSeconds,
		"cooling_disabled":     cfg.DisableCooling,
		"registry":             registry,
	}
}

// accounts lists what the plugin knows, with the label and the run state only. There is
// no token field, by construction: freebuffAccount is never marshalled here.
func (h *managementHandler) accounts() []map[string]any {
	cfg := h.runtime.settings.get()
	accounts := accountStore.list("")
	out := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, map[string]any{
			"label":  account.label(),
			"prefix": account.Prefix,
			"active": account.Enabled,
		})
	}
	_ = cfg
	return out
}

func (h *managementHandler) models() map[string]any {
	cfg := h.runtime.settings.get()
	published := publishedModelIDs(cfg, h.runtime.registry)
	upstream := []string{}
	if h.runtime.registry != nil {
		upstream = h.runtime.registry.Models()
	}
	return map[string]any{
		"prefix":           cfg.ModelAliasPrefix,
		"published_models": published,
		"published_count":  len(published),
		"upstream_models":  upstream,
		"upstream_count":   len(upstream),
		"pinned_models":    cfg.Models,
	}
}

// dashboard renders the console. Everything on it is server rendered from the same data
// the JSON routes return, so the page and the routes cannot disagree.
func (h *managementHandler) dashboard() (pluginapi.ManagementResponse, error) {
	body := renderDashboard(h.status(), h.models(), h.accounts())
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
	}, nil
}

func jsonResponse(status int, payload any) (pluginapi.ManagementResponse, error) {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusInternalServerError,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"error":"response could not be encoded"}`),
		}, nil
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       raw,
	}, nil
}

func asStringSlice(value any) []string {
	items, ok := value.([]string)
	if !ok {
		return nil
	}
	out := make([]string, len(items))
	copy(out, items)
	return out
}
