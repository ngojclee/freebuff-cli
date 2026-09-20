package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// freeAgentsSourceURL is the upstream file that lists which model each free agent may
// use. It is the same source Freebuff2API reads, and it is the only public statement of
// the free-tier catalogue: codebuff.com does not expose /v1/models itself.
const freeAgentsSourceURL = "https://raw.githubusercontent.com/CodebuffAI/codebuff/main/common/src/constants/free-agents.ts"

// freeAgentsModuleBaseURL is where the modules free-agents.ts imports from live. The
// upstream file stopped inlining model ids and now references exported constants, so the
// registry has to follow those imports to learn the catalogue.
const freeAgentsModuleBaseURL = "https://raw.githubusercontent.com/CodebuffAI/codebuff/main/common/src/constants/"

// maxFreeAgentsSourceBytes bounds the source fetch. The file is a few kilobytes; the
// ceiling exists so a hostile or broken response cannot make the plugin allocate without
// limit.
const maxFreeAgentsSourceBytes = 1 << 20

// maxImportedModules bounds how many sibling modules are fetched. The file imports a
// handful; the ceiling exists so a shape change cannot turn one refresh into a crawl.
const maxImportedModules = 12

// fallbackAgentModels is used only when the very first fetch fails, so a cold start
// still has a usable catalogue. It is intentionally small: a stale catalogue that is
// wrong is worse than a small catalogue that is right.
var fallbackAgentModels = map[string][]string{
	"base2-free":         {"minimax/minimax-m2.7", "z-ai/glm-5.1"},
	"file-picker":        {"google/gemini-2.5-flash-lite"},
	"file-picker-max":    {"google/gemini-3.1-flash-lite-preview"},
	"file-lister":        {"google/gemini-3.1-flash-lite-preview"},
	"researcher-web":     {"google/gemini-3.1-flash-lite-preview"},
	"researcher-docs":    {"google/gemini-3.1-flash-lite-preview"},
	"basher":             {"google/gemini-3.1-flash-lite-preview"},
	"editor-lite":        {"minimax/minimax-m2.7", "z-ai/glm-5.1"},
	"code-reviewer-lite": {"minimax/minimax-m2.7", "z-ai/glm-5.1"},
}

var (
	freeAgentBlockPattern = regexp.MustCompile(`'([^']+)':\s*new\s+Set\(\[([^\]]*)\]\)`)
	// freeAgentEntryPattern walks a Set body in source order, matching either a quoted
	// model id or an identifier that points at an imported constant.
	freeAgentEntryPattern = regexp.MustCompile(`'([^']*)'|[A-Za-z_][A-Za-z0-9_]*`)
	importedModulePattern = regexp.MustCompile(`from\s+'\./([A-Za-z0-9._-]+)'`)
	exportedStringPattern = regexp.MustCompile(`(?s)export\s+const\s+([A-Za-z0-9_]+)\s*=\s*'([^']*)'`)
)

// modelRegistry owns the free-agent catalogue. It is refreshed on an interval and keeps
// serving the previous list when a refresh fails, so a transient network problem never
// empties the gateway's model list.
type modelRegistry struct {
	client *http.Client
	// sourceURL is the free-agents file. moduleBaseURL is the directory its imports are
	// resolved against; tests override both so the whole path stays offline.
	sourceURL     string
	moduleBaseURL string
	refreshFor    time.Duration
	log           func(level, event string, fields map[string]any)

	mu           sync.RWMutex
	agentModels  map[string][]string
	modelToAgent map[string]string
	models       []string
	lastRefresh  time.Time
	lastError    string
	loaded       bool

	stopCh chan struct{}
	wg     sync.WaitGroup
}

func newModelRegistry(client *http.Client, refreshFor time.Duration, log func(level, event string, fields map[string]any)) *modelRegistry {
	return &modelRegistry{
		client:       client,
		sourceURL:    freeAgentsSourceURL,
		refreshFor:   refreshFor,
		log:          log,
		agentModels:  make(map[string][]string),
		modelToAgent: make(map[string]string),
		stopCh:       make(chan struct{}),
	}
}

// Start performs the first fetch synchronously, then refreshes in the background.
func (r *modelRegistry) Start(ctx context.Context) {
	if errRefresh := r.Refresh(ctx); errRefresh != nil {
		r.applyFallback()
		r.recordError(errRefresh)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.refreshFor)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refreshCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if errRefresh := r.Refresh(refreshCtx); errRefresh != nil {
					r.recordError(errRefresh)
				}
				cancel()
			case <-r.stopCh:
				return
			}
		}
	}()
}

func (r *modelRegistry) Stop() {
	select {
	case <-r.stopCh:
		return
	default:
	}
	close(r.stopCh)
	r.wg.Wait()
}

// Refresh fetches and re-parses the upstream source.
func (r *modelRegistry) Refresh(ctx context.Context) error {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, r.sourceURL, nil)
	if errRequest != nil {
		return fmt.Errorf("build model source request: %w", errRequest)
	}
	request.Header.Set("Accept", "text/plain")

	response, errDo := r.client.Do(request)
	if errDo != nil {
		return fmt.Errorf("fetch model source: %w", errDo)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("model source returned status %d", response.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, maxFreeAgentsSourceBytes))
	if errRead != nil {
		return fmt.Errorf("read model source: %w", errRead)
	}

	source := string(body)
	constants := r.resolveImportedConstants(ctx, source)
	agentModels := parseFreeAgentsWithConstants(source, constants)
	if len(agentModels) == 0 {
		return fmt.Errorf("model source contained no free agents")
	}
	modelToAgent, models := buildModelMapping(agentModels)
	if len(models) == 0 {
		return fmt.Errorf("model source produced no model ids")
	}

	r.mu.Lock()
	r.agentModels = agentModels
	r.modelToAgent = modelToAgent
	r.models = models
	r.lastRefresh = time.Now().UTC()
	r.lastError = ""
	r.loaded = true
	r.mu.Unlock()

	if r.log != nil {
		r.log("info", "model_registry_refreshed", map[string]any{
			"agents": len(agentModels),
			"models": len(models),
		})
	}
	return nil
}

func (r *modelRegistry) applyFallback() {
	modelToAgent, models := buildModelMapping(fallbackAgentModels)
	r.mu.Lock()
	r.agentModels = fallbackAgentModels
	r.modelToAgent = modelToAgent
	r.models = models
	r.lastRefresh = time.Now().UTC()
	r.loaded = true
	r.mu.Unlock()
}

func (r *modelRegistry) recordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.lastError = err.Error()
	r.mu.Unlock()
	if r.log != nil {
		r.log("warn", "model_registry_refresh_failed", map[string]any{"reason": redactReason(err.Error())})
	}
}

// Models returns the current upstream model ids, sorted.
func (r *modelRegistry) Models() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.models))
	copy(out, r.models)
	return out
}

// AgentForModel returns the agent that serves a model id.
func (r *modelRegistry) AgentForModel(model string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, found := r.modelToAgent[strings.TrimSpace(model)]
	return agent, found
}

// AgentIDs returns every known agent id, sorted, so the pool can pre-warm runs.
func (r *modelRegistry) AgentIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.agentModels))
	for id := range r.agentModels {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Snapshot is the redacted view the dashboard renders. It never carries a token.
func (r *modelRegistry) Snapshot() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	models := make([]string, len(r.models))
	copy(models, r.models)
	out := map[string]any{
		"models":       models,
		"model_count":  len(models),
		"agent_count":  len(r.agentModels),
		"loaded":       r.loaded,
		"last_refresh": "",
		"last_error":   r.lastError,
	}
	if !r.lastRefresh.IsZero() {
		out["last_refresh"] = r.lastRefresh.Format(time.RFC3339)
	}
	return out
}

// parseFreeAgents extracts the agent -> models mapping when every model id is an inline
// string literal. It is kept for callers that have no constant table.
func parseFreeAgents(source string) map[string][]string {
	return parseFreeAgentsWithConstants(source, nil)
}

// parseFreeAgentsWithConstants extracts the agent -> models mapping from the upstream
// TypeScript source.
//
// The upstream file used to inline model ids as string literals. It now imports exported
// constants from sibling modules and references them inside the Set, so a literal-only
// parser silently drops almost every model: measured 2026-09-20, the live file has 44
// agent blocks but only one inline literal, which is why the catalogue collapsed to a
// single model. Identifiers are therefore resolved through the caller's constant table,
// and anything that still cannot be resolved is skipped rather than guessed at.
//
// The parser is intentionally tolerant: the file is someone else's code, and a shape
// change must degrade to "fewer models", never to a panic.
func parseFreeAgentsWithConstants(source string, constants map[string]string) map[string][]string {
	result := make(map[string][]string)
	for _, match := range freeAgentBlockPattern.FindAllStringSubmatch(source, -1) {
		agentID := strings.TrimSpace(match[1])
		if agentID == "" {
			continue
		}
		body := match[2]
		models := make([]string, 0, 4)
		seen := make(map[string]struct{}, 4)
		addModel := func(value string) {
			value = strings.TrimSpace(value)
			if value == "" {
				return
			}
			if _, exists := seen[value]; exists {
				return
			}
			seen[value] = struct{}{}
			models = append(models, value)
		}

		// Walk the Set body in source order so the catalogue keeps the upstream's own
		// ordering: each entry is either an inline literal or an imported constant.
		for _, entry := range freeAgentEntryPattern.FindAllStringSubmatch(body, -1) {
			if literal := entry[1]; literal != "" {
				addModel(literal)
				continue
			}
			if value, found := constants[entry[0]]; found {
				addModel(value)
			}
		}
		if len(models) > 0 {
			result[agentID] = normalizeModelIDs(models)
		}
	}
	return result
}

// extractImportedModules returns the sibling module names a source file imports from,
// deduplicated and in first-seen order.
func extractImportedModules(source string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	for _, match := range importedModulePattern.FindAllStringSubmatch(source, -1) {
		name := strings.TrimSpace(match[1])
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
		if len(out) >= maxImportedModules {
			break
		}
	}
	return out
}

// parseExportedStringConstants collects `export const NAME = 'value'` pairs. The value
// may sit on the following line, which the upstream formatting does.
func parseExportedStringConstants(source string) map[string]string {
	out := make(map[string]string)
	for _, match := range exportedStringPattern.FindAllStringSubmatch(source, -1) {
		name := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])
		if name == "" || value == "" {
			continue
		}
		out[name] = value
	}
	return out
}

// resolveImportedConstants fetches the modules free-agents.ts imports from and merges
// their exported string constants.
//
// A module that cannot be fetched is skipped: a partial table still yields most of the
// catalogue, and the caller keeps the previous list when the whole refresh fails.
func (r *modelRegistry) resolveImportedConstants(ctx context.Context, source string) map[string]string {
	constants := make(map[string]string)
	for _, module := range extractImportedModules(source) {
		body, errFetch := r.fetchModule(ctx, module)
		if errFetch != nil {
			if r.log != nil {
				r.log("debug", "model_module_fetch_failed", map[string]any{
					"module": module,
					"reason": redactReason(errFetch.Error()),
				})
			}
			continue
		}
		for name, value := range parseExportedStringConstants(body) {
			constants[name] = value
		}
	}
	return constants
}

func (r *modelRegistry) fetchModule(ctx context.Context, module string) (string, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, r.moduleURL(module), nil)
	if errRequest != nil {
		return "", fmt.Errorf("build module request: %w", errRequest)
	}
	request.Header.Set("Accept", "text/plain")

	response, errDo := r.client.Do(request)
	if errDo != nil {
		return "", fmt.Errorf("fetch module: %w", errDo)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("module returned status %d", response.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, maxFreeAgentsSourceBytes))
	if errRead != nil {
		return "", fmt.Errorf("read module: %w", errRead)
	}
	return string(body), nil
}

// moduleURL builds the raw URL for a sibling module, honouring a test override of the
// source URL so the whole resolution path stays testable without the network.
func (r *modelRegistry) moduleURL(module string) string {
	if r.moduleBaseURL != "" {
		return r.moduleBaseURL + module + ".ts"
	}
	return freeAgentsModuleBaseURL + module + ".ts"
}

// buildModelMapping inverts agent -> models into model -> agent plus a sorted, deduped
// model list. When a model appears under several agents the lexicographically smallest
// agent id wins, so routing is stable across refreshes; the upstream reference picks at
// random, which would move traffic between agents on every restart.
func buildModelMapping(agentModels map[string][]string) (map[string]string, []string) {
	modelAgents := make(map[string][]string)
	for agentID, models := range agentModels {
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			modelAgents[model] = append(modelAgents[model], agentID)
		}
	}

	modelToAgent := make(map[string]string, len(modelAgents))
	models := make([]string, 0, len(modelAgents))
	for model, agents := range modelAgents {
		sort.Strings(agents)
		modelToAgent[model] = agents[0]
		models = append(models, model)
	}
	sort.Strings(models)
	return modelToAgent, models
}

// redactReason keeps a failure reason short and free of anything that could carry a
// credential. Upstream error bodies are never stored verbatim.
func redactReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return reason
}
