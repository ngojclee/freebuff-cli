package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestParseExecutorRequestDecodesBase64Payload is the regression for the first real smoke
// test through CPA. The host sends Payload and StorageJSON as []byte, which arrive over the
// JSON RPC base64 encoded. Reading them as plain text produced a body that parsed as
// nothing, and every request came back as "the request carried no model" even though the
// client had sent one.
func TestParseExecutorRequestDecodesBase64Payload(t *testing.T) {
	body := `{"model":"freebuff/google/gemini-3.8-flash","messages":[{"role":"user","content":"hi"}]}`
	storage := `{"type":"freebuff","auth_token":"aaaa-bbbb-cccc-dddd-eeee"}`

	request, errMarshal := json.Marshal(map[string]any{
		"auth_id":      "freebuff-abc",
		"model":        "",
		"payload":      base64.StdEncoding.EncodeToString([]byte(body)),
		"storage_json": base64.StdEncoding.EncodeToString([]byte(storage)),
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}

	parsed, errParse := parseExecutorRequest(request)
	if errParse != nil {
		t.Fatalf("parse: %v", errParse)
	}
	if got := payloadModel(parsed.Payload); got != "freebuff/google/gemini-3.8-flash" {
		t.Fatalf("payload model = %q, want the id from the decoded body", got)
	}
	decoded, errDecode := decodeStorage(parsed.StorageJSON)
	if errDecode != nil {
		t.Fatalf("storage did not decode: %v (%q)", errDecode, parsed.StorageJSON)
	}
	if decoded.AuthToken != "aaaa-bbbb-cccc-dddd-eeee" {
		t.Fatal("the credential did not survive the decode")
	}
}

// TestDecodeHostBytesAcceptsBothShapes keeps the fallback honest: a host that sends the
// body as plain text must still work.
func TestDecodeHostBytesAcceptsBothShapes(t *testing.T) {
	plain := `{"model":"x"}`
	if got := string(decodeHostBytes(plain)); got != plain {
		t.Fatalf("plain text = %q, want it returned unchanged", got)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	if got := string(decodeHostBytes(encoded)); got != plain {
		t.Fatalf("base64 = %q, want %q", got, plain)
	}
	if got := decodeHostBytes("   "); got != nil {
		t.Fatalf("blank = %q, want nil", got)
	}
}

// TestResolveModelFallsBackToThePayload documents the two places the model can come from.
func TestResolveModelFallsBackToThePayload(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []string{"google/gemini-3.8-flash"}
	runtime := newRuntime(cfg)
	// The catalogue is normally filled by the background refresh; seed it directly so the
	// test stays offline.
	runtime.registry.agentModels = map[string][]string{"editor-lite": {"google/gemini-3.8-flash"}}
	runtime.registry.modelToAgent, runtime.registry.models = buildModelMapping(runtime.registry.agentModels)

	upstream, agent, errResolve := runtime.resolveModel(cfg, executorRequest{
		Model:   "freebuff/google/gemini-3.8-flash",
		Payload: []byte(`{"model":"freebuff/google/gemini-3.8-flash"}`),
	})
	if errResolve != nil {
		t.Fatalf("resolve: %v", errResolve)
	}
	if upstream != "google/gemini-3.8-flash" {
		t.Fatalf("upstream model = %q", upstream)
	}
	if agent != "editor-lite" {
		t.Fatalf("agent = %q, want editor-lite", agent)
	}
}

// TestParseExecutorRequestAcceptsGoFieldNames is the live-host regression. pluginapi
// ExecutorRequest carries no json tags and the host marshals it with encoding/json, so the
// wire keys are Go field names. A parser that only read snake_case produced the live
// "the request carried no model" failure while every snake_case unit fixture passed.
func TestParseExecutorRequestAcceptsGoFieldNames(t *testing.T) {
	body := `{"model":"freebuff/openai/gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`
	storage := `{"type":"freebuff","auth_token":"aaaa-bbbb-cccc-dddd-eeee"}`

	request, errMarshal := json.Marshal(map[string]any{
		"AuthID":         "freebuff-abc",
		"AuthProvider":   "freebuff",
		"Model":          "freebuff/openai/gpt-5.6-luna",
		"Format":         "openai",
		"Stream":         false,
		"Payload":        base64.StdEncoding.EncodeToString([]byte(body)),
		"StorageJSON":    base64.StdEncoding.EncodeToString([]byte(storage)),
		"AuthAttributes": map[string]string{"priority": "5"},
		"StreamID":       "stream-1",
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}

	parsed, errParse := parseExecutorRequest(request)
	if errParse != nil {
		t.Fatalf("parse: %v", errParse)
	}
	if parsed.Model != "freebuff/openai/gpt-5.6-luna" {
		t.Fatalf("Model = %q", parsed.Model)
	}
	if payloadModel(parsed.Payload) != "freebuff/openai/gpt-5.6-luna" {
		t.Fatalf("payload did not decode; got %q", string(parsed.Payload))
	}
	if parsed.StreamID != "stream-1" || parsed.AuthID != "freebuff-abc" {
		t.Fatalf("identity fields lost: %+v", parsed)
	}
	if parsed.Attributes["priority"] != "5" {
		t.Fatalf("attributes = %+v", parsed.Attributes)
	}
}

// TestExecutorRequestKeysListsNamesOnly guards the diagnostic itself: it must report field
// names and never a value.
func TestExecutorRequestKeysListsNamesOnly(t *testing.T) {
	raw := []byte(`{"Model":"freebuff/x","Payload":"c3VwZXItc2VjcmV0","StorageJSON":"eA=="}`)
	keys := executorRequestKeys(raw)
	joined := strings.Join(keys, ",")
	if joined != "Model,Payload,StorageJSON" {
		t.Fatalf("keys = %q", joined)
	}
	if strings.Contains(joined, "c3VwZXItc2VjcmV0") {
		t.Fatal("the key list leaked a value")
	}
	if got := executorRequestKeys([]byte("not json")); len(got) != 1 || got[0] != "<unparseable>" {
		t.Fatalf("unparseable marker = %v", got)
	}
}
