package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// withHostCall swaps the host-call indirection for the duration of a test.
func withHostCall(t *testing.T, fn func(method string, payload []byte) ([]byte, error)) {
	t.Helper()
	previous := hostCall
	hostCall = fn
	t.Cleanup(func() { hostCall = previous })
}

func TestAccountsComeFromTheHostNotTheCache(t *testing.T) {
	// The cache is empty here on purpose: that is exactly the state right after a reload,
	// and it used to render an empty table even though credentials existed.
	withHostCall(t, func(method string, payload []byte) ([]byte, error) {
		return []byte(`{"files":[
			{"name":"freebuff-a@example.com.json","label":"a@example.com","provider":"freebuff","status":"ready","source":"file"},
			{"name":"freebuff-b@example.com.json","label":"b@example.com","type":"freebuff","disabled":true,"source":"file"},
			{"name":"codex-x.json","label":"x","provider":"codex"},
			{"name":"freebuff-runtime-only.json","provider":"freebuff","runtime_only":true}
		]}`), nil
	})

	handler := &managementHandler{runtime: newRuntime(DefaultConfig())}
	accounts := handler.accounts()

	// Two freebuff rows plus the runtime-only one; the codex credential is not ours.
	if len(accounts) != 3 {
		t.Fatalf("accounts = %d, want 3: %+v", len(accounts), accounts)
	}
	if accounts[0]["label"] != "a@example.com" || accounts[0]["active"] != true {
		t.Fatalf("first row = %+v", accounts[0])
	}
	if accounts[1]["active"] != true && accounts[1]["status"] != "disabled" {
		t.Fatalf("disabled row = %+v", accounts[1])
	}
	if accounts[2]["name"] != "freebuff-runtime-only.json" || accounts[2]["runtime_only"] != true {
		t.Fatalf("runtime-only row = %+v", accounts[2])
	}
}

func TestAccountsFallBackToCacheWhenTheHostCallFails(t *testing.T) {
	withHostCall(t, func(method string, payload []byte) ([]byte, error) {
		return nil, errors.New("host callback unavailable in a non-cgo build")
	})
	accountStore.remember(Storage{Type: providerKey, AuthToken: "aaaa-bbbb-cccc-dddd-1111", Email: "cached@example.com"})
	t.Cleanup(func() { accountStore = newAccountCache() })

	handler := &managementHandler{runtime: newRuntime(DefaultConfig())}
	accounts := handler.accounts()
	if len(accounts) != 1 {
		t.Fatalf("accounts = %+v, want the cache fallback", accounts)
	}
	if accounts[0]["label"] != "cached@example.com" || accounts[0]["source"] != "cache" {
		t.Fatalf("fallback row = %+v", accounts[0])
	}
}

// TestAccountsPayloadHasNoCredentialFields is the structural guarantee: the row type has no
// place for a token, so neither the JSON route nor the page can leak one.
func TestAccountsPayloadHasNoCredentialFields(t *testing.T) {
	withHostCall(t, func(method string, payload []byte) ([]byte, error) {
		return []byte(`{"files":[{"name":"freebuff-x.json","label":"x@example.com","provider":"freebuff","status":"ready","source":"file"}]}`), nil
	})

	handler := &managementHandler{runtime: newRuntime(DefaultConfig())}
	accounts := handler.accounts()
	if len(accounts) != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}

	allowed := map[string]struct{}{
		"label": {}, "name": {}, "status": {}, "active": {}, "source": {}, "runtime_only": {}, "prefix": {},
	}
	for key := range accounts[0] {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("unexpected account field %q, which is not on the allow list", key)
		}
	}

	raw, errMarshal := json.Marshal(accounts)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, banned := range []string{"auth_token", "AuthToken", "refresh_token", "cookie", "token"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(banned)) {
			t.Fatalf("the accounts payload mentions %q: %s", banned, raw)
		}
	}
}
