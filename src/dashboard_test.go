package main

import (
	"strings"
	"testing"
)

// dashboardSections is the parity contract with the CodeBuddy console: the same cards, in
// the same order, so an operator moving between the two does not relearn the layout. The
// key card sits second on both, because CodeBuddy cannot read anything without it.
var dashboardSections = []string{
	"State",
	"Actions",
	"Accounts",
	"Published models",
	"Upstream catalogue",
}

func TestDashboardIsFullWidthAndListsModels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	runtime := newRuntime(cfg)
	handler := &managementHandler{runtime: runtime}

	page := renderDashboard(handler.status(), handler.models(), handler.accounts())

	for _, want := range []string{`<div class="head">`, `id="refresh"`, `X-Management-Key`} {
		if !strings.Contains(page, want) {
			t.Fatalf("dashboard is missing %q", want)
		}
	}

	// Sections must appear in the agreed order, not merely exist.
	previous := -1
	for _, section := range dashboardSections {
		at := strings.Index(page, `section-title">`+section)
		if at < 0 {
			t.Fatalf("dashboard is missing the %q section", section)
		}
		if at < previous {
			t.Fatalf("section %q is out of order: position %d follows %d", section, at, previous)
		}
		previous = at
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
