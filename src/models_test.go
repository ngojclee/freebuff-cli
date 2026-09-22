package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func decodeEnvelope(t *testing.T, raw []byte) pluginabi.Envelope {
	t.Helper()
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("envelope: %v (%s)", err, raw)
	}
	return envelope
}

// model.static backs the executor's own provider catalogue and never sees OAuth aliases.
// An aliased id left here is republished alongside the alias even when Keep original is
// off, which is what the Freebuff live model list showed.
func TestModelStaticDropsAliasedIDsButModelForAuthKeepsThem(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Models = []string{"deepseek/deepseek-v4.1-flash", "google/gemini-3.8-flash"}
	runtime := newRuntime(cfg)
	request := []byte(`{"Host":{"OAuthModelAlias":{"freebuff":[{"Name":"freebuff/deepseek/deepseek-v4.1-flash","Alias":"deepseek-flash"}]}}}`)

	static := decodeEnvelope(t, runtime.dispatch(pluginabi.MethodModelStatic, request))
	if !static.OK {
		t.Fatalf("model.static failed: %+v", static.Error)
	}
	var staticPayload modelRegistrationResponse
	if err := json.Unmarshal(static.Result, &staticPayload); err != nil {
		t.Fatal(err)
	}
	staticIDs := map[string]bool{}
	for _, model := range staticPayload.Models {
		staticIDs[model.ID] = true
	}
	if staticIDs["freebuff/deepseek/deepseek-v4.1-flash"] {
		t.Fatalf("model.static republished the aliased id: %v", staticPayload.Models)
	}
	if !staticIDs["freebuff/google/gemini-3.8-flash"] {
		t.Fatalf("model.static dropped an unaliased id: %v", staticPayload.Models)
	}

	perAuth := decodeEnvelope(t, runtime.dispatch(pluginabi.MethodModelForAuth, request))
	if !perAuth.OK {
		t.Fatalf("model.for_auth failed: %+v", perAuth.Error)
	}
	var perAuthPayload modelRegistrationResponse
	if err := json.Unmarshal(perAuth.Result, &perAuthPayload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range perAuthPayload.Models {
		if model.ID == "freebuff/deepseek/deepseek-v4.1-flash" {
			found = true
		}
	}
	if !found {
		t.Fatalf("model.for_auth must keep the id so CPA can apply the alias: %v", perAuthPayload.Models)
	}
}

// Empty, foreign-provider and identity aliases must not filter this provider's catalogue.
func TestModelStaticIgnoresForeignAndEmptyAliasTables(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Models = []string{"deepseek/deepseek-v4.1-flash"}
	cases := map[string]string{
		"empty request":      `{}`,
		"no aliases":         `{"Host":{"OAuthModelAlias":{}}}`,
		"another provider":   `{"Host":{"OAuthModelAlias":{"codebuddy":[{"Name":"freebuff/deepseek/deepseek-v4.1-flash","Alias":"x"}]}}}`,
		"identity alias":     `{"Host":{"OAuthModelAlias":{"freebuff":[{"Name":"freebuff/deepseek/deepseek-v4.1-flash","Alias":"freebuff/deepseek/deepseek-v4.1-flash"}]}}}`,
		"malformed envelope": `not-json`,
	}
	for name, request := range cases {
		runtime := newRuntime(cfg)
		response := decodeEnvelope(t, runtime.dispatch(pluginabi.MethodModelStatic, []byte(request)))
		if !response.OK {
			t.Fatalf("%s: model.static failed: %+v", name, response.Error)
		}
		var payload modelRegistrationResponse
		if err := json.Unmarshal(response.Result, &payload); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(payload.Models) != 1 || payload.Models[0].ID != "freebuff/deepseek/deepseek-v4.1-flash" {
			t.Fatalf("%s: expected the catalogue untouched, got %v", name, payload.Models)
		}
	}
}
