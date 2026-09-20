package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const freeAgentsFixture = `
export const freeAgentModels = {
  'base2-free': new Set(['minimax/minimax-m2.7', 'z-ai/glm-5.1']),
  'file-picker': new Set(['google/gemini-2.5-flash-lite']),
  'researcher-web': new Set(['google/gemini-3.1-flash-lite-preview']),
  'basher': new Set(['google/gemini-3.1-flash-lite-preview']),
}
`

func TestParseFreeAgentsExtractsEveryAgent(t *testing.T) {
	parsed := parseFreeAgents(freeAgentsFixture)
	if len(parsed) != 4 {
		t.Fatalf("agent count = %d, want 4", len(parsed))
	}
	if got := parsed["base2-free"]; len(got) != 2 || got[0] != "minimax/minimax-m2.7" {
		t.Fatalf("base2-free models = %v", got)
	}
}

func TestParseFreeAgentsToleratesUnknownShape(t *testing.T) {
	if parsed := parseFreeAgents("this file changed shape entirely"); len(parsed) != 0 {
		t.Fatalf("expected no agents, got %d", len(parsed))
	}
}

func TestBuildModelMappingIsDeterministic(t *testing.T) {
	// A model served by two agents must always resolve to the same one, so traffic does
	// not move between agents across restarts.
	first, firstModels := buildModelMapping(map[string][]string{
		"zebra": {"shared/model"},
		"alpha": {"shared/model"},
	})
	second, _ := buildModelMapping(map[string][]string{
		"alpha": {"shared/model"},
		"zebra": {"shared/model"},
	})
	if first["shared/model"] != "alpha" || second["shared/model"] != "alpha" {
		t.Fatalf("shared model resolved to %s then %s, want alpha both times", first["shared/model"], second["shared/model"])
	}
	if len(firstModels) != 1 || firstModels[0] != "shared/model" {
		t.Fatalf("models = %v", firstModels)
	}
}

func TestRegistryFallsBackWhenTheSourceIsUnreachable(t *testing.T) {
	registry := newModelRegistry(&http.Client{Timeout: time.Second}, 0, nil)
	registry.sourceURL = "http://127.0.0.1:1/none"
	if errRefresh := registry.Refresh(t.Context()); errRefresh == nil {
		t.Fatal("expected the refresh to fail")
	}
	registry.applyFallback()
	models := registry.Models()
	if len(models) == 0 {
		t.Fatal("fallback catalogue must not be empty")
	}
	if _, found := registry.AgentForModel(models[0]); !found {
		t.Fatalf("model %s has no agent", models[0])
	}
}

func TestRegistrySnapshotHasNoToken(t *testing.T) {
	registry := newModelRegistry(&http.Client{Timeout: time.Second}, 0, nil)
	registry.applyFallback()
	snapshot := registry.Snapshot()
	if _, found := snapshot["token"]; found {
		t.Fatal("registry snapshot must not carry a token field")
	}
	if !strings.Contains(strings.ToLower(strings.Join(registry.Models(), ",")), "gemini") {
		t.Log("fallback catalogue did not contain gemini; shape may have changed upstream")
	}
}
