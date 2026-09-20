package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hostAuthListResponse mirrors the host's reply to host.auth.list. The field name is the
// host's own, so it is decoded rather than assumed.
type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

// listHostAuthFiles asks CPA for the credentials it currently knows about.
//
// The plugin's own in-memory cache is only ever a fallback: it starts empty on every load
// and is only filled when a request or an auth parse touches a credential, so a hot reload
// silently empties the account list. The host is the authority on which auth files exist.
func listHostAuthFiles() ([]pluginapi.HostAuthFileEntry, error) {
	raw, errCall := hostCall(pluginabi.MethodHostAuthList, []byte("{}"))
	if errCall != nil {
		return nil, errCall
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var response hostAuthListResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth list: %w", errUnmarshal)
	}
	return response.Files, nil
}

// accountView is one row of the dashboard's account table. It is built from what the host
// publishes about a credential, never from the credential itself, so there is no path from
// an auth token to this struct.
type accountView struct {
	Label       string `json:"label"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Active      bool   `json:"active"`
	Source      string `json:"source"`
	RuntimeOnly bool   `json:"runtime_only"`
	Prefix      string `json:"prefix,omitempty"`
}

// hostAccountViews lists this provider's credentials through the host and degrades to the
// in-memory cache when the host callback is unavailable, which is the case in a non-cgo
// build and in unit tests.
func hostAccountViews() ([]accountView, bool) {
	entries, errList := listHostAuthFiles()
	if errList != nil {
		return nil, false
	}
	views := make([]accountView, 0, len(entries))
	for _, entry := range entries {
		if !isFreebuffAuthEntry(entry) {
			continue
		}
		label := firstNonEmpty(entry.Label, entry.Name)
		if label == "" {
			label = "Freebuff account"
		}
		views = append(views, accountView{
			Label:       label,
			Name:        entry.Name,
			Status:      authEntryStatus(entry),
			Active:      !entry.Disabled && !entry.Unavailable,
			Source:      strings.TrimSpace(entry.Source),
			RuntimeOnly: entry.RuntimeOnly,
		})
	}
	// The host knows the file exists even when nothing has read its token yet, so the
	// prefix is only known for accounts this process has already served.
	for index := range views {
		for _, cached := range accountStore.list("") {
			if cached.Prefix != "" && strings.EqualFold(cached.label(), views[index].Label) {
				views[index].Prefix = cached.Prefix
				break
			}
		}
	}
	return views, true
}

// isFreebuffAuthEntry keeps the table to this plugin's own credentials. CPA reports the
// provider key on the entry, and the file name is the fallback for records written before
// the provider key was set.
func isFreebuffAuthEntry(entry pluginapi.HostAuthFileEntry) bool {
	for _, candidate := range []string{entry.Provider, entry.Type} {
		if strings.EqualFold(strings.TrimSpace(candidate), providerKey) {
			return true
		}
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(entry.Name)), providerKey+"-")
}

// authEntryStatus prefers the host's own status text and falls back to a derived one, so
// an operator always sees something meaningful.
func authEntryStatus(entry pluginapi.HostAuthFileEntry) string {
	if status := strings.TrimSpace(entry.Status); status != "" {
		return status
	}
	switch {
	case entry.Disabled:
		return "disabled"
	case entry.Unavailable:
		return "unavailable"
	default:
		return "ready"
	}
}
