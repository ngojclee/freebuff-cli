package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// clientSessionIDLength matches the official client's Math.random().toString(36)
// substring, which is 13 base-36 characters.
const clientSessionIDLength = 13

const base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// buildUpstreamPayload rewrites the caller's body into what the upstream expects.
//
// Three things change and nothing else:
//   - the model is replaced with the upstream id (the published id carries our namespace)
//   - codebuff_metadata carries the run id, the free cost mode, a fresh client session id
//     and the waiting-room instance id
//   - tool parameter schemas are reduced to the conservative JSON Schema subset the
//     upstream parser accepts
//
// Everything else - messages, tools, temperature, stream - is forwarded untouched.
func buildUpstreamPayload(payload []byte, upstreamModel, runID, instanceID string) ([]byte, error) {
	decoded := map[string]any{}
	if len(payload) > 0 {
		if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
			return nil, fmt.Errorf("request body is not a JSON object")
		}
	}
	decoded["model"] = upstreamModel

	metadata := map[string]any{}
	if existing, ok := decoded["codebuff_metadata"].(map[string]any); ok && existing != nil {
		metadata = existing
	}
	metadata["run_id"] = runID
	metadata["cost_mode"] = "free"
	metadata["client_id"] = newClientSessionID()
	if strings.TrimSpace(instanceID) != "" {
		metadata["freebuff_instance_id"] = instanceID
	}
	decoded["codebuff_metadata"] = metadata

	if tools, ok := decoded["tools"].([]any); ok {
		normalizeToolSchemas(tools)
	}

	raw, errMarshal := json.Marshal(decoded)
	if errMarshal != nil {
		return nil, fmt.Errorf("request body could not be re-encoded")
	}
	return raw, nil
}

// newClientSessionID generates the same shape the official client sends. It is not a
// credential; it only makes concurrent requests look like distinct sessions.
func newClientSessionID() string {
	buffer := make([]byte, clientSessionIDLength)
	if _, errRead := rand.Read(buffer); errRead != nil {
		// A predictable fallback is acceptable here: the value is a correlation id, not a
		// secret, and refusing to send a request over it would be worse.
		for index := range buffer {
			buffer[index] = byte(index)
		}
	}
	out := make([]byte, clientSessionIDLength)
	for index, value := range buffer {
		out[index] = base36Alphabet[int(value)%len(base36Alphabet)]
	}
	return string(out)
}

// maxSchemaDepth bounds the schema walk so a pathological client cannot make the plugin
// recurse without limit.
const maxSchemaDepth = 12

// normalizeToolSchemas rewrites tool parameter schemas into a conservative subset.
//
// Today that means resolving local $ref pointers and simplifying the nullable constructs
// clients like LobeChat emit. It never drops a tool and never changes its name.
func normalizeToolSchemas(tools []any) {
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := toolMap["function"].(map[string]any)
		if !ok {
			continue
		}
		params, ok := fn["parameters"].(map[string]any)
		if !ok {
			continue
		}
		fn["parameters"] = normalizeSchemaMap(params, collectDefinitions(params), maxSchemaDepth)
	}
}

func collectDefinitions(schema map[string]any) map[string]any {
	merged := map[string]any{}
	for _, key := range []string{"definitions", "$defs"} {
		raw, ok := schema[key].(map[string]any)
		if !ok {
			continue
		}
		for name, value := range raw {
			merged[name] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func normalizeSchemaMap(schema map[string]any, definitions map[string]any, depth int) map[string]any {
	if schema == nil || depth <= 0 {
		return schema
	}

	// Resolve a local $ref when the target is available.
	if ref, ok := schema["$ref"].(string); ok && definitions != nil {
		if name := refName(ref); name != "" {
			if target, found := definitions[name].(map[string]any); found {
				resolved := normalizeSchemaMap(cloneAnyMap(target), definitions, depth-1)
				delete(resolved, "$ref")
				return resolved
			}
		}
	}

	// Flatten the common anyOf/oneOf nullable shape into a single type plus nullable.
	for _, key := range []string{"anyOf", "oneOf"} {
		options, ok := schema[key].([]any)
		if !ok {
			continue
		}
		nonNull := make([]map[string]any, 0, len(options))
		nullable := false
		for _, option := range options {
			optionMap, okOption := option.(map[string]any)
			if !okOption {
				continue
			}
			if kind, _ := optionMap["type"].(string); kind == "null" {
				nullable = true
				continue
			}
			nonNull = append(nonNull, normalizeSchemaMap(optionMap, definitions, depth-1))
		}
		if len(nonNull) == 1 {
			delete(schema, key)
			for field, value := range nonNull[0] {
				schema[field] = value
			}
			if nullable {
				schema["nullable"] = true
			}
		}
	}

	delete(schema, "definitions")
	delete(schema, "$defs")

	for _, key := range []string{"properties"} {
		properties, ok := schema[key].(map[string]any)
		if !ok {
			continue
		}
		for name, value := range properties {
			if child, okChild := value.(map[string]any); okChild {
				properties[name] = normalizeSchemaMap(child, definitions, depth-1)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		schema["items"] = normalizeSchemaMap(items, definitions, depth-1)
	}
	return schema
}

// refName extracts the final segment of a local JSON pointer such as
// "#/definitions/Foo" or "#/$defs/Foo".
func refName(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || !strings.HasPrefix(ref, "#") {
		return ""
	}
	parts := strings.Split(ref, "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}
