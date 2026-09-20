package main

// pluginVersion is stamped by CI with -ldflags "-X main.pluginVersion=...".
var pluginVersion = "0.1.8-dev"

const (
	pluginID = "freebuff-cli"

	// providerKey is the CLIProxyAPI auth provider identifier. It is what appears as
	// the "type" of the stored auth and what the management UI groups by, so it must
	// stay lowercase and stable.
	providerKey = "freebuff"

	pluginName = "Freebuff CLI"

	// logoURL is the vendor's own mark, but served by this plugin rather than from the
	// vendor's site. CPA exposes pluginapi.Metadata.Logo to management clients, and a
	// same-origin path always resolves in the management panel, whereas freebuff.com can
	// be blocked by region or network policy.
	//
	// registry.json keeps the absolute vendor URL instead: the plugin store lists the
	// plugin before it is installed, so the route below does not exist yet at that point.
	logoURL = "/v0/resource/plugins/" + pluginID + iconRoutePath

	pluginAuthor     = "ngojclee"
	pluginRepository = "https://github.com/ngojclee/freebuff-cli"
)
