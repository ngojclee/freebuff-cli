package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

// The upstream file stopped inlining model ids: it now imports exported constants and
// references them inside the Set. This is the regression that collapsed the catalogue to
// a single model on 2026-09-20.
const freeAgentsWithConstantsFixture = `
import {
  FREEBUFF_GEMINI_38_FLASH_MODEL_ID,
  FREEBUFF_GLM_V52_MODEL_ID,
} from './freebuff-models'
import { GEMINI_3_1_FLASH_LITE_MODEL_ID } from './gemini'

export const freeAgentModels = {
  'base2-free': new Set([FREEBUFF_GLM_V52_MODEL_ID, 'google/gemini-2.5-flash-lite']),
  'researcher-web': new Set([GEMINI_3_1_FLASH_LITE_MODEL_ID]),
  'editor-lite': new Set([FREEBUFF_GEMINI_38_FLASH_MODEL_ID]),
}
`

func TestParseFreeAgentsResolvesImportedConstants(t *testing.T) {
	constants := map[string]string{
		"FREEBUFF_GEMINI_38_FLASH_MODEL_ID": "google/gemini-3.8-flash",
		"FREEBUFF_GLM_V52_MODEL_ID":         "z-ai/glm-5.2",
		"GEMINI_3_1_FLASH_LITE_MODEL_ID":    "google/gemini-3.1-flash-lite-preview",
	}
	parsed := parseFreeAgentsWithConstants(freeAgentsWithConstantsFixture, constants)
	if len(parsed) != 3 {
		t.Fatalf("agent count = %d, want 3", len(parsed))
	}
	base := parsed["base2-free"]
	if len(base) != 2 || base[0] != "z-ai/glm-5.2" || base[1] != "google/gemini-2.5-flash-lite" {
		t.Fatalf("base2-free models = %v", base)
	}
	if got := parsed["editor-lite"]; len(got) != 1 || got[0] != "google/gemini-3.8-flash" {
		t.Fatalf("editor-lite models = %v", got)
	}
}

func TestParseFreeAgentsSkipsUnresolvableIdentifiers(t *testing.T) {
	parsed := parseFreeAgentsWithConstants(freeAgentsWithConstantsFixture, nil)
	// Only the inline literal survives when no constant table is supplied.
	if len(parsed) != 1 {
		t.Fatalf("agent count = %d, want 1 (only the inline literal)", len(parsed))
	}
	if got := parsed["base2-free"]; len(got) != 1 || got[0] != "google/gemini-2.5-flash-lite" {
		t.Fatalf("base2-free models = %v", got)
	}
}

func TestExtractImportedModules(t *testing.T) {
	modules := extractImportedModules(freeAgentsWithConstantsFixture)
	if len(modules) != 2 || modules[0] != "freebuff-models" || modules[1] != "gemini" {
		t.Fatalf("modules = %v", modules)
	}
}

func TestParseExportedStringConstantsHandlesLineBreaks(t *testing.T) {
	source := "export const A_MODEL_ID =\n  'vendor/model-a'\nexport const B_MODEL_ID = 'vendor/model-b'\nexport const NOT_A_STRING = 42\n"
	constants := parseExportedStringConstants(source)
	if constants["A_MODEL_ID"] != "vendor/model-a" || constants["B_MODEL_ID"] != "vendor/model-b" {
		t.Fatalf("constants = %v", constants)
	}
	if _, found := constants["NOT_A_STRING"]; found {
		t.Fatal("a numeric constant must not be collected")
	}
}

// TestRegistryRefreshFollowsImportedModules is the end-to-end regression for the reported
// bug: the plugin published a single model because the upstream source no longer inlines
// model ids. It serves both the main file and the imported module over httptest.
func TestRegistryRefreshFollowsImportedModules(t *testing.T) {
	mainFile := `
import {
  FREEBUFF_GEMINI_38_FLASH_MODEL_ID,
  FREEBUFF_GLM_V52_MODEL_ID,
} from './freebuff-models'
export const freeAgentModels = {
  'base2-free': new Set([FREEBUFF_GLM_V52_MODEL_ID]),
  'editor-lite': new Set([FREEBUFF_GEMINI_38_FLASH_MODEL_ID]),
}
`
	modelsModule := `
export const FREEBUFF_GEMINI_38_FLASH_MODEL_ID = 'google/gemini-3.8-flash'
export const FREEBUFF_GLM_V52_MODEL_ID =
  'z-ai/glm-5.2'
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/free-agents.ts":
			_, _ = w.Write([]byte(mainFile))
		case "/freebuff-models.ts":
			_, _ = w.Write([]byte(modelsModule))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	registry := newModelRegistry(&http.Client{Timeout: 5 * time.Second}, time.Hour, nil)
	registry.sourceURL = server.URL + "/free-agents.ts"
	registry.moduleBaseURL = server.URL + "/"

	if errRefresh := registry.Refresh(t.Context()); errRefresh != nil {
		t.Fatalf("refresh: %v", errRefresh)
	}
	models := registry.Models()
	if len(models) != 2 {
		t.Fatalf("models = %v, want 2", models)
	}
	if agent, found := registry.AgentForModel("google/gemini-3.8-flash"); !found || agent != "editor-lite" {
		t.Fatalf("agent for gemini-3.8-flash = %q found=%v", agent, found)
	}
	if fmt.Sprint(models) == "" {
		t.Fatal("unreachable")
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
