package main

import (
	"sort"
	"strings"
	"time"
)

// modelInfo mirrors pluginapi.ModelInfo field names. The host decodes this with untagged
// Go fields, so snake_case tags would silently drop every value.
type modelInfo struct {
	ID      string `json:"ID"`
	Object  string `json:"Object"`
	Created int64  `json:"Created"`
	OwnedBy string `json:"OwnedBy"`
	Type    string `json:"Type"`
}

// modelRegistrationResponse mirrors pluginapi.ModelRegistrationResponse.
type modelRegistrationResponse struct {
	Provider string      `json:"Provider"`
	Models   []modelInfo `json:"Models"`
}

// hostConfigSummary mirrors the part of pluginapi.HostConfigSummary this plugin reads.
// The host marshals its own struct with Go field names, so snake_case tags here would
// silently decode to nothing.
type hostConfigSummary struct {
	OAuthModelAlias map[string][]hostModelAlias `json:"OAuthModelAlias"`
}

// hostModelAlias mirrors pluginapi.ModelAlias. Name is the published id and Alias is the
// operator-facing replacement configured in CPA.
type hostModelAlias struct {
	Name  string `json:"Name"`
	Alias string `json:"Alias"`
}

// modelRequest is the envelope the host sends to model.static and model.for_auth.
type modelRequest struct {
	Host hostConfigSummary `json:"Host"`
}

// publishedModelIDs merges the operator's pinned list with the discovered registry and
// applies the namespace. The operator's list is merged rather than replaced so a model
// stays reachable even when the upstream source temporarily drops it.
func publishedModelIDs(cfg Config, registry *modelRegistry) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)

	appendModel := func(upstreamID string) {
		upstreamID = strings.TrimSpace(upstreamID)
		if upstreamID == "" {
			return
		}
		published := cfg.PublishedModelID(upstreamID)
		if published == "" {
			return
		}
		if _, exists := seen[published]; exists {
			return
		}
		seen[published] = struct{}{}
		out = append(out, published)
	}

	if registry != nil {
		for _, id := range registry.Models() {
			appendModel(id)
		}
	}
	for _, id := range cfg.Models {
		appendModel(id)
	}
	sort.Strings(out)
	return out
}

// currentModelResponse is what the host registers for this provider.
func currentModelResponse(cfg Config, registry *modelRegistry) modelRegistrationResponse {
	if !cfg.Enabled {
		return modelRegistrationResponse{Provider: providerKey, Models: []modelInfo{}}
	}
	created := time.Now().Unix()
	ids := publishedModelIDs(cfg, registry)
	models := make([]modelInfo, 0, len(ids))
	for _, id := range ids {
		models = append(models, modelInfo{
			ID:      id,
			Object:  "model",
			Created: created,
			OwnedBy: providerKey,
			Type:    "openai-compatibility",
		})
	}
	return modelRegistrationResponse{Provider: providerKey, Models: models}
}

// staticModelResponse answers model.static. CPA also publishes this provider-level
// catalogue under the executor's own model client, and that copy never sees OAuth model
// aliases. Leaving an aliased id here makes the original visible even when Keep original
// is off, so the aliased ids are omitted and the per-auth path stays authoritative.
func staticModelResponse(cfg Config, registry *modelRegistry, host hostConfigSummary) modelRegistrationResponse {
	out := currentModelResponse(cfg, registry)
	aliased := aliasedModelIDs(host)
	if len(aliased) == 0 || len(out.Models) == 0 {
		return out
	}
	kept := make([]modelInfo, 0, len(out.Models))
	for _, model := range out.Models {
		if _, skip := aliased[strings.ToLower(strings.TrimSpace(model.ID))]; skip {
			continue
		}
		kept = append(kept, model)
	}
	// Never publish an empty catalogue: the per-auth path is what keeps this provider
	// visible when every model has an alias.
	if len(kept) == 0 {
		return out
	}
	out.Models = kept
	return out
}

// aliasedModelIDs collects the published ids CPA has an alias for, scoped to this
// provider. Names are compared case-insensitively because CPA keys aliases on the
// lowercased id.
func aliasedModelIDs(host hostConfigSummary) map[string]struct{} {
	entries := host.OAuthModelAlias[providerKey]
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		alias := strings.TrimSpace(entry.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		out[strings.ToLower(name)] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
