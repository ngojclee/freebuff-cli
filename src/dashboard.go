package main

import (
	"fmt"
	"html"
	"strings"
)

// renderDashboard is a single self-contained page in the same style family as the
// CodeBuddy console and the CPA management UI: plain HTML plus one fetch, no framework and
// no build step, and no credential anywhere on screen.
//
// It is deliberately full width. The useful content is two tables and a wall of model ids,
// which read badly inside a narrow centred column.
func renderDashboard(status, models map[string]any, accounts []map[string]any) string {
	page := strings.Builder{}
	page.WriteString(dashboardHead)
	page.WriteString(dashboardHeader(status))
	page.WriteString(dashboardState(status))
	page.WriteString(dashboardAccounts(accounts))
	page.WriteString(dashboardModels(models))
	page.WriteString(dashboardScript)
	page.WriteString("</body></html>")
	return page.String()
}

const dashboardHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="color-scheme" content="light dark">
<title>Freebuff CLI</title>
<style>
  :root{--bg:#f7f5ef;--panel:#fffdfa;--surface:#f0ede5;--inset:#f8f6f1;--ink:#282521;--ink-2:#69635b;--ink-3:#9b948b;--line:#dfdacf;--line-2:#cfc8bb;--accent:#2563eb;--success:#0f766e;--success-bg:#ccfbf1;--warn:#9a5a00;--warn-bg:#fff0bf;--error:#b44232;--error-bg:#fbe3df;--radius:8px;--shadow:0 1px 2px #00000014}
  *{box-sizing:border-box}
  body{margin:0;background:var(--bg);color:var(--ink);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",sans-serif;font-size:14px;line-height:1.5;padding:24px}
  .head{display:flex;align-items:center;gap:10px;margin-bottom:4px}
  .head img{width:22px;height:22px;border-radius:5px;display:block}
  h1{font-size:20px;margin:0;font-weight:700}
  .sub{color:var(--ink-2);font-size:12.5px;margin-bottom:18px}
  table{width:100%;border-collapse:collapse;font-size:13px}
  th,td{text-align:left;padding:9px 10px;border-bottom:1px solid var(--line);vertical-align:top}
  th{color:var(--ink-3);font-weight:700;font-size:11px;text-transform:uppercase;letter-spacing:.04em}
  .muted{color:var(--ink-3)}
  .tag{display:inline-flex;padding:2px 8px;border-radius:999px;font-size:11px;font-weight:700;border:1px solid var(--line-2);background:var(--surface);color:var(--ink-2)}
  .tag.on{background:var(--success-bg);border-color:#5eead4;color:var(--success)}
  .tag.off{background:var(--error-bg);border-color:#f0a79d;color:var(--error)}
  .card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);padding:14px;margin-bottom:14px}
  .section-title{font-size:11px;font-weight:750;text-transform:uppercase;letter-spacing:.04em;color:var(--ink-3);margin-bottom:10px}
  .grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:10px}
  .kv{background:var(--inset);border-radius:6px;padding:10px;min-width:0}
  .kv span{display:block;color:var(--ink-3);font-size:10.5px;font-weight:700;text-transform:uppercase;letter-spacing:.04em}
  .kv strong{font-weight:650;font-size:13px;word-break:break-all}
  .chips{display:flex;flex-wrap:wrap;gap:6px}
  .chip{display:inline-flex;align-items:center;background:var(--inset);border:1px solid var(--line);border-radius:6px;padding:3px 8px;font-size:12px;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;color:var(--ink);word-break:break-all}
  input[type=password]{width:100%;min-height:36px;border:1px solid var(--line-2);background:#fff;border-radius:8px;color:var(--ink);padding:8px 10px;outline:none;font-size:13px}
  input:focus{border-color:var(--accent);box-shadow:0 0 0 3px #2563eb1a}
  label{display:block;font-size:12px;color:var(--ink-2);font-weight:650;margin-bottom:5px}
  .access-row{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;align-items:end}
  button{border:1px solid var(--line-2);background:#fff;color:var(--ink);border-radius:6px;height:34px;padding:0 11px;font-weight:650;font-size:13px;cursor:pointer;white-space:nowrap}
  button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
  button:disabled{opacity:.5;cursor:not-allowed}
  .status{margin-top:10px;font-size:12.5px;font-weight:650}
  .status.ok{color:var(--success)}
  .status.err{color:var(--error)}
  .hint{font-size:11.5px;color:var(--ink-2);margin-top:8px;line-height:1.5}
</style>
</head>
<body>
`

func dashboardHeader(status map[string]any) string {
	out := strings.Builder{}
	fmt.Fprintf(&out, `<div class="head"><img src="%s" alt=""><h1>%s</h1></div>`,
		html.EscapeString(logoURL), html.EscapeString(pluginName))
	fmt.Fprintf(&out, `<div class="sub">Freebuff accounts, the free-agent catalogue and the waiting-room state. Version %s.</div>`,
		html.EscapeString(fmt.Sprint(status["version"])))
	return out.String()
}

func dashboardState(status map[string]any) string {
	type cell struct{ key, label string }
	cells := []cell{
		{"enabled", "Enabled"},
		{"known_accounts", "Accounts"},
		{"model_alias_prefix", "Namespace"},
		{"upstream_base_url", "Upstream"},
		{"auth_dir", "Auth dir"},
		{"waiting_room_seconds", "Waiting-room budget"},
		{"rotation_seconds", "Run rotation"},
		{"refresh_seconds", "Catalogue refresh"},
		{"cooling_disabled", "Cooling disabled"},
	}
	out := strings.Builder{}
	out.WriteString(`<div class="card"><div class="section-title">State</div><div class="grid">`)
	for _, entry := range cells {
		fmt.Fprintf(&out, `<div class="kv"><span>%s</span><strong>%s</strong></div>`,
			html.EscapeString(entry.label), html.EscapeString(stateText(status[entry.key])))
	}
	if registry, ok := status["registry"].(map[string]any); ok {
		fmt.Fprintf(&out, `<div class="kv"><span>Last refresh</span><strong>%s</strong></div>`,
			html.EscapeString(stateText(registry["last_refresh"])))
		if failure := stateText(registry["last_error"]); failure != "" && failure != "—" {
			fmt.Fprintf(&out, `<div class="kv"><span>Catalogue error</span><strong>%s</strong></div>`,
				html.EscapeString(failure))
		}
	}
	out.WriteString(`</div></div>`)
	return out.String()
}

func dashboardAccounts(accounts []map[string]any) string {
	out := strings.Builder{}
	out.WriteString(`<div class="card"><div class="section-title">Accounts</div>`)
	if len(accounts) == 0 {
		out.WriteString(`<div class="muted">No Freebuff account is loaded yet. Save a token into the auth directory, or use OAuth Login.</div>`)
		out.WriteString(`</div>`)
		return out.String()
	}
	out.WriteString(`<table><thead><tr><th>Account</th><th>Namespace</th><th>Status</th></tr></thead><tbody>`)
	for _, account := range accounts {
		state := `<span class="tag on">active</span>`
		if active, ok := account["active"].(bool); !ok || !active {
			state = `<span class="tag off">inactive</span>`
		}
		fmt.Fprintf(&out, "<tr><td>%s</td><td>%s</td><td>%s</td></tr>",
			html.EscapeString(stateText(account["label"])),
			html.EscapeString(stateText(account["prefix"])),
			state)
	}
	out.WriteString(`</tbody></table>`)
	out.WriteString(`<div class="hint">One auth file per Freebuff account. Labels only: the token never leaves the auth record and is never rendered here.</div>`)
	out.WriteString(`</div>`)
	return out.String()
}

func dashboardModels(models map[string]any) string {
	out := strings.Builder{}
	out.WriteString(`<div class="card"><div class="section-title">Management access</div>`)
	out.WriteString(`<div class="access-row"><div><label for="mkey">CPA management key</label>`)
	out.WriteString(`<input id="mkey" type="password" autocomplete="current-password" placeholder="Only needed to refresh the catalogue now"></div>`)
	out.WriteString(`<button class="primary" id="refresh">Refresh catalogue</button></div>`)
	out.WriteString(`<div id="refreshStatus" class="status"></div></div>`)

	fmt.Fprintf(&out, `<div class="card"><div class="section-title">Published models (%s)</div><div class="chips">`,
		html.EscapeString(stateText(models["published_count"])))
	out.WriteString(chips(asStringSlice(models["published_models"])))
	out.WriteString(`</div><div class="hint">What a client sees through the gateway, already namespaced. Aliases you add in CPA sit on top of these ids.</div></div>`)

	fmt.Fprintf(&out, `<div class="card"><div class="section-title">Upstream catalogue (%s)</div><div class="chips">`,
		html.EscapeString(stateText(models["upstream_count"])))
	out.WriteString(chips(asStringSlice(models["upstream_models"])))
	out.WriteString(`</div><div class="hint">The free-agent map as the vendor publishes it today. The plugin re-reads it on the interval above, and keeps the previous list when a refresh fails.</div></div>`)

	if pinned := asStringSlice(models["pinned_models"]); len(pinned) > 0 {
		out.WriteString(`<div class="card"><div class="section-title">Pinned by config</div><div class="chips">`)
		out.WriteString(chips(pinned))
		out.WriteString(`</div></div>`)
	}
	return out.String()
}

func chips(values []string) string {
	if len(values) == 0 {
		return `<span class="muted">none</span>`
	}
	out := strings.Builder{}
	for _, value := range values {
		fmt.Fprintf(&out, `<span class="chip">%s</span>`, html.EscapeString(value))
	}
	return out.String()
}

// stateText renders any scalar for the page and never leaves an empty cell.
func stateText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "—"
	case bool:
		if typed {
			return "yes"
		}
		return "no"
	case string:
		if strings.TrimSpace(typed) == "" {
			return "—"
		}
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

const dashboardScript = `
<script>
const managementBase = '/v0/management/plugins/freebuff-cli';

function key() {
  const field = document.getElementById('mkey');
  return (field && field.value.trim()) || sessionStorage.getItem('fbManagementKey') || '';
}

document.getElementById('refresh').addEventListener('click', async () => {
  const out = document.getElementById('refreshStatus');
  const value = key();
  if (!value) {
    out.className = 'status err';
    out.textContent = 'Enter the CPA management key first.';
    return;
  }
  sessionStorage.setItem('fbManagementKey', value);
  out.className = 'status';
  out.textContent = 'refreshing...';
  document.getElementById('refresh').disabled = true;
  try {
    const response = await fetch(managementBase + '/models/refresh', {
      method: 'POST',
      headers: { 'X-Management-Key': value, 'Authorization': 'Bearer ' + value }
    });
    const data = await response.json();
    if (!response.ok || data.ok === false) {
      out.className = 'status err';
      out.textContent = 'refresh failed: ' + (data.error || response.status);
      return;
    }
    // The catalogue is server rendered, so a successful refresh reloads the page.
    location.reload();
  } catch (error) {
    out.className = 'status err';
    out.textContent = 'refresh failed: ' + error.message;
  } finally {
    document.getElementById('refresh').disabled = false;
  }
});
</script>
`
