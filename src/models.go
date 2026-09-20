package main

import (
	"sort"
	"strings"
	"time"
)

// modelInfo mirrors pluginapi.ModelInfo field names. The host decodes this with untagged
// Go fields, so snake_case tags would silently drop every value.
type modelInfo struct {
	ID          string `json:"ID"`
	Object      string `json:"Object"`
	Created     int64  `json:"Created"`
	OwnedBy     string `json:"OwnedBy"`
	Type        string `json:"Type"`
	DisplayName string `json:"DisplayName"`
	Name        string `json:"Name"`
}

// modelRegistrationResponse mirrors pluginapi.ModelRegistrationResponse.
type modelRegistrationResponse struct {
	Provider string      `json:"Provider"`
	Models   []modelInfo `json:"Models"`
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
			ID:          id,
			Object:      "model",
			Created:     created,
			OwnedBy:     providerKey,
			Type:        "openai-compatibility",
			DisplayName: id,
			Name:        id,
		})
	}
	return modelRegistrationResponse{Provider: providerKey, Models: models}
}
