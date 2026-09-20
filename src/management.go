package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
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

func (h *managementHandler) dashboard() (pluginapi.ManagementResponse, error) {
	status := h.status()
	models := h.models()
	body := renderDashboard(status, models)
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

// renderDashboard is a single self-contained page: no external assets, no build step, and
// no credential on screen.
func renderDashboard(status, models map[string]any) string {
	modelList := renderChips(asStringSlice(models["published_models"]))
	upstreamList := renderChips(asStringSlice(models["upstream_models"]))
	return fmt.Sprintf(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
:root{--bg:#f7f5ef;--panel:#fffdfa;--surface:#f0ede5;--inset:#f8f6f1;--ink:#282521;--ink-2:#69635b;--line:#dfdacf;--accent:#2563eb;--radius:8px}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
main{max-width:960px;margin:0 auto;padding:24px}
h1{font-size:20px;margin:0 0 4px}h2{font-size:14px;margin:0 0 10px;color:var(--ink-2);text-transform:uppercase;letter-spacing:.04em}
.card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);padding:16px;margin-bottom:16px}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px}
.kv{background:var(--inset);border-radius:6px;padding:10px}
.kv span{display:block;color:var(--ink-2);font-size:12px}
.kv strong{font-weight:600}
.chips{display:flex;flex-wrap:wrap;gap:6px}
.chip{background:var(--surface);border:1px solid var(--line);border-radius:999px;padding:3px 10px;font-size:12px}
button{background:var(--accent);color:#fff;border:0;border-radius:6px;padding:8px 14px;cursor:pointer;font-size:13px}
pre{background:var(--inset);border-radius:6px;padding:10px;overflow:auto;font-size:12px;margin:0}
</style></head><body><main>
<h1>%s</h1>
<p style="color:var(--ink-2);margin:0 0 16px">version %s</p>
<div class="card"><h2>State</h2><div class="grid">
%s
</div></div>
<div class="card"><h2>Published models (%s)</h2><div class="chips">%s</div></div>
<div class="card"><h2>Upstream catalogue (%s)</h2><div class="chips">%s</div></div>
</main></body></html>`,
		html.EscapeString(pluginName),
		html.EscapeString(pluginName),
		html.EscapeString(pluginVersion),
		renderState(status),
		fmt.Sprint(models["published_count"]), modelList,
		fmt.Sprint(models["upstream_count"]), upstreamList,
	)
}

func renderState(status map[string]any) string {
	keys := []string{
		"enabled", "auth_dir", "upstream_base_url", "model_alias_prefix",
		"known_accounts", "rotation_seconds", "refresh_seconds", "waiting_room_seconds",
	}
	out := strings.Builder{}
	for _, key := range keys {
		value := status[key]
		fmt.Fprintf(&out, "<div class=\"kv\"><span>%s</span><strong>%s</strong></div>",
			html.EscapeString(key), html.EscapeString(fmt.Sprint(value)))
	}
	return out.String()
}

func renderChips(values []string) string {
	if len(values) == 0 {
		return "<span class=\"chip\">none</span>"
	}
	out := strings.Builder{}
	for _, value := range values {
		fmt.Fprintf(&out, "<span class=\"chip\">%s</span>", html.EscapeString(value))
	}
	return out.String()
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
