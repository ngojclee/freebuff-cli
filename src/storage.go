package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Storage is the provider-owned auth payload this plugin persists through CPA's own auth
// storage. CPA never interprets it; the plugin owns the shape.
type Storage struct {
	Type string `json:"type"`
	// AuthToken is the Freebuff account token. It is a credential and is never logged,
	// never echoed in a status payload, and never written anywhere except the auth file.
	AuthToken string `json:"auth_token"`
	// Email is the account label, named the way every other CPA provider names it, so the
	// management UI shows an account instead of a file path.
	Email string `json:"email,omitempty"`
	// Label is an optional operator nickname that overrides Email in the UI.
	Label string `json:"label,omitempty"`
	// Prefix is the operator's model prefix for this account, when they set one.
	Prefix string `json:"prefix,omitempty"`
	// Priority is the credential priority CPA's scheduler reads from the auth attribute
	// "priority". Nil means "not set here".
	Priority *int `json:"priority,omitempty"`
	// Disabled lets an operator keep an account on disk without serving traffic from it.
	Disabled bool `json:"disabled,omitempty"`
	// SourceFile records which file supplied the credential. It is a path, never a secret.
	SourceFile string `json:"source_file,omitempty"`
}

// freebuffAccount is the runtime view the pool uses.
type freebuffAccount struct {
	AuthToken string
	Email     string
	Label     string
	Prefix    string
	Priority  *int
	Enabled   bool
}

func (a freebuffAccount) label() string {
	if value := strings.TrimSpace(a.Label); value != "" {
		return value
	}
	if value := strings.TrimSpace(a.Email); value != "" {
		return value
	}
	return "freebuff-account"
}

// Valid reports whether the record carries something usable.
func (s Storage) Valid() bool {
	return strings.TrimSpace(s.AuthToken) != ""
}

// DisplayLabel is the user-facing name for this auth. It is a method rather than the
// field itself so the field can stay a plain operator override.
func (s Storage) DisplayLabel() string {
	if value := strings.TrimSpace(s.Label); value != "" {
		return value
	}
	if value := strings.TrimSpace(s.Email); value != "" {
		return value
	}
	return "Freebuff account"
}

// AuthID is a stable, non-reversible identifier. It is derived from the token so two
// files carrying the same account collapse onto one auth id, and it never contains the
// token itself.
func (s Storage) AuthID() string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(s.AuthToken)))
	return "freebuff-" + hex.EncodeToString(digest[:6])
}

// Account converts storage into the pool's runtime view.
func (s Storage) Account() freebuffAccount {
	return freebuffAccount{
		AuthToken: strings.TrimSpace(s.AuthToken),
		Email:     strings.TrimSpace(s.Email),
		Label:     strings.TrimSpace(s.Label),
		Prefix:    strings.TrimSpace(s.Prefix),
		Priority:  s.Priority,
		Enabled:   !s.Disabled && strings.TrimSpace(s.AuthToken) != "",
	}
}

// EncodeStorage is what the host persists and hands back on refresh.
func (s Storage) EncodeStorage() []byte {
	raw, errMarshal := json.Marshal(s)
	if errMarshal != nil {
		return []byte(`{}`)
	}
	return raw
}

// decodeStorage reverses EncodeStorage.
func decodeStorage(raw []byte) (Storage, error) {
	var storage Storage
	if len(raw) == 0 {
		return storage, errors.New("auth storage is empty")
	}
	if errUnmarshal := json.Unmarshal(raw, &storage); errUnmarshal != nil {
		return storage, fmt.Errorf("auth storage could not be decoded: %w", errUnmarshal)
	}
	return storage, nil
}

// ParseAuthFile recognises every shape an operator can realistically supply:
//
//  1. this plugin's own record: {"type":"freebuff","auth_token":"..."}
//  2. the Freebuff CLI credentials file: {"default":{"authToken":"...","email":"..."}}
//  3. a token export from freebuff.llm.pm: {"authToken":"..."} or {"token":"..."}
//  4. a bare token pasted into a .txt/.key file
//
// Handled is false when the payload is not a Freebuff credential, so an unrelated
// provider's file in the same directory is never claimed.
func ParseAuthFile(raw []byte, sourcePath string) (Storage, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return Storage{}, false, nil
	}

	// Shape 1: this plugin's own record.
	if looksLikeFreebuffRecord(raw) {
		storage, errParse := decodeStorage(raw)
		if errParse != nil {
			return Storage{}, true, errParse
		}
		storage.SourceFile = sourcePath
		if storage.Type == "" {
			storage.Type = providerKey
		}
		return storage, true, nil
	}

	// Shape 2: the CLI credentials file, keyed by profile name.
	if looksLikeCredentialsFile(raw) {
		storage, errParse := parseCredentialsFile(raw)
		if errParse != nil {
			return Storage{}, true, errParse
		}
		storage.SourceFile = sourcePath
		return storage, true, nil
	}

	// Shape 3: a flat token export.
	if token := flatToken(raw); token != "" {
		return Storage{
			Type:       providerKey,
			AuthToken:  token,
			SourceFile: sourcePath,
		}, true, nil
	}

	// Shape 4: a bare token. Only accepted when the file extension says it is a key, so a
	// random JSON document is never mistaken for a credential.
	if isTokenLikeFile(sourcePath) && looksLikeToken(trimmed) {
		return Storage{
			Type:       providerKey,
			AuthToken:  trimmed,
			SourceFile: sourcePath,
		}, true, nil
	}

	return Storage{}, false, nil
}

func looksLikeFreebuffRecord(raw []byte) bool {
	var probe struct {
		Type      string `json:"type"`
		AuthToken string `json:"auth_token"`
	}
	if errUnmarshal := json.Unmarshal(raw, &probe); errUnmarshal != nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(probe.Type), providerKey) {
		return true
	}
	// A record with our token key but no type is still ours.
	return strings.TrimSpace(probe.AuthToken) != "" && strings.TrimSpace(probe.Type) == ""
}

func looksLikeCredentialsFile(raw []byte) bool {
	var probe map[string]struct {
		AuthToken string `json:"authToken"`
	}
	if errUnmarshal := json.Unmarshal(raw, &probe); errUnmarshal != nil {
		return false
	}
	for _, entry := range probe {
		if strings.TrimSpace(entry.AuthToken) != "" {
			return true
		}
	}
	return false
}

// parseCredentialsFile picks the first profile that carries a token. When several exist
// the alphabetically first profile wins, so the choice is stable across restarts.
func parseCredentialsFile(raw []byte) (Storage, error) {
	var parsed map[string]struct {
		AuthToken string `json:"authToken"`
		Email     string `json:"email"`
		Name      string `json:"name"`
		ID        string `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return Storage{}, fmt.Errorf("credentials file could not be decoded: %w", errUnmarshal)
	}
	profiles := make([]string, 0, len(parsed))
	for name := range parsed {
		profiles = append(profiles, name)
	}
	sortStrings(profiles)
	for _, name := range profiles {
		entry := parsed[name]
		if strings.TrimSpace(entry.AuthToken) == "" {
			continue
		}
		return Storage{
			Type:      providerKey,
			AuthToken: strings.TrimSpace(entry.AuthToken),
			Email:     firstNonEmpty(entry.Email, entry.Name, entry.ID),
		}, nil
	}
	return Storage{}, errors.New("credentials file carried no auth token")
}

func flatToken(raw []byte) string {
	var probe struct {
		AuthToken string `json:"authToken"`
		Token     string `json:"token"`
	}
	if errUnmarshal := json.Unmarshal(raw, &probe); errUnmarshal != nil {
		return ""
	}
	return firstNonEmpty(probe.AuthToken, probe.Token)
}

// looksLikeToken accepts the shapes Freebuff issues: a UUID, a JWT, or a long opaque
// string. It deliberately rejects anything with whitespace or a newline in the middle so
// a stray document is never treated as a credential.
func looksLikeToken(value string) bool {
	if len(value) < 16 || len(value) > 4096 {
		return false
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	return true
}

func isTokenLikeFile(path string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".txt", ".key", ".token":
		return true
	default:
		return false
	}
}

// authFileName reduces a path to its base name, which is what CPA stores and what the
// management editor asks for. Sending an absolute path makes the download handler reject
// the request with 400.
func authFileName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return providerKey + ".json"
	}
	return filepath.Base(path)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// sortStrings is a tiny insertion sort kept local so storage.go has no extra import for
// a handful of profile names.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
