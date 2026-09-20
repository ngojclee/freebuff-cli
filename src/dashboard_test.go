package main

import (
	"strings"
	"testing"
)

func TestDashboardIsFullWidthAndListsModels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	runtime := newRuntime(cfg)
	handler := &managementHandler{runtime: runtime}

	page := renderDashboard(handler.status(), handler.models(), handler.accounts())

	for _, want := range []string{
		`<div class="head">`,
		`id="refresh"`,
		`Published models`,
		`Upstream catalogue`,
		`Management access`,
		`X-Management-Key`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("dashboard is missing %q", want)
		}
	}

	// Full width: the old page wrapped everything in a centred 960px column.
	if strings.Contains(page, "max-width:960px") || strings.Contains(page, "<main>") {
		t.Fatal("the dashboard must use the full viewport width, not a centred column")
	}
}

func TestDashboardNeverRendersAToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	secret := "token-supersecretvalue-9999"
	accountStore.remember(Storage{Type: providerKey, AuthToken: secret, Email: "me@example.com"})
	t.Cleanup(func() { accountStore = newAccountCache() })

	handler := &managementHandler{runtime: newRuntime(cfg)}
	page := renderDashboard(handler.status(), handler.models(), handler.accounts())

	if strings.Contains(page, secret) {
		t.Fatal("the dashboard leaked an auth token")
	}
	if !strings.Contains(page, "me@example.com") {
		t.Fatal("the dashboard must still show the account label")
	}
}

func TestDashboardShowsPublishedModelIDs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Models = []string{"google/gemini-3.8-flash"}

	runtime := newRuntime(cfg)
	handler := &managementHandler{runtime: runtime}
	page := renderDashboard(handler.status(), handler.models(), handler.accounts())

	if !strings.Contains(page, "freebuff/google/gemini-3.8-flash") {
		t.Fatalf("the dashboard must list the namespaced model id, page was:\n%s", page)
	}
}
