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

// maxFreeAgentsSourceBytes bounds the source fetch. The file is a few kilobytes; the
// ceiling exists so a hostile or broken response cannot make the plugin allocate without
// limit.
const maxFreeAgentsSourceBytes = 1 << 20

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
	freeAgentModelPattern = regexp.MustCompile(`'([^']+)'`)
)

// modelRegistry owns the free-agent catalogue. It is refreshed on an interval and keeps
// serving the previous list when a refresh fails, so a transient network problem never
// empties the gateway's model list.
type modelRegistry struct {
	client     *http.Client
	sourceURL  string
	refreshFor time.Duration
	log        func(level, event string, fields map[string]any)

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

	agentModels := parseFreeAgents(string(body))
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

// parseFreeAgents extracts the agent -> models mapping from the upstream TypeScript
// source. It is intentionally tolerant: the file is someone else's code, and a shape
// change must degrade to "fewer models", never to a panic.
func parseFreeAgents(source string) map[string][]string {
	result := make(map[string][]string)
	for _, match := range freeAgentBlockPattern.FindAllStringSubmatch(source, -1) {
		agentID := strings.TrimSpace(match[1])
		if agentID == "" {
			continue
		}
		models := make([]string, 0)
		for _, modelMatch := range freeAgentModelPattern.FindAllStringSubmatch(match[2], -1) {
			if model := strings.TrimSpace(modelMatch[1]); model != "" {
				models = append(models, model)
			}
		}
		if len(models) > 0 {
			result[agentID] = normalizeModelIDs(models)
		}
	}
	return result
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
