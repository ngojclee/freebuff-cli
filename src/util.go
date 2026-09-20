package main

import (
	"encoding/json"
	"os"
	"strings"
)

// maxAuthFileBytes bounds how much of a vendor auth file is read. The observed record
// is well under a kilobyte; the ceiling exists so a corrupted or hostile file in the
// auth directory cannot make the plugin allocate without limit.
const maxAuthFileBytes = 1 << 20

func jsonUnmarshal(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}

// readFileLimited reads at most maxAuthFileBytes from path.
func readFileLimited(path string) ([]byte, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, errOpen
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, maxAuthFileBytes)
	read, errRead := file.Read(buffer)
	if errRead != nil && read == 0 {
		return nil, errRead
	}
	return buffer[:read], nil
}

// mapValue looks a key up under any of its accepted spellings. CPA's own config uses
// hyphenated keys while the plugin API uses snake_case, so every reader accepts both.
func mapValue(source map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, found := source[key]; found {
			return value, true
		}
	}
	return nil, false
}

func stringValue(source map[string]any, keys ...string) (string, bool) {
	value, found := mapValue(source, keys...)
	if !found {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}

func boolValue(source map[string]any, keys ...string) (bool, bool) {
	value, found := mapValue(source, keys...)
	if !found {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

// asMap normalises a decoded JSON object into map[string]any.
func asMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

// cloneAnyMap copies one decoded JSON object so a caller can mutate its own view without
// touching the caller's payload.
func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
