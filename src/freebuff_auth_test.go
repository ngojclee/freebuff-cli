package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestFreebuffVendorLoginImportsTokenAutomatically(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"fingerprintId":   "freebuff-go-test",
				"fingerprintHash": "hash-test",
				"loginUrl":        "https://freebuff.com/login?auth_code=test-code",
				"expiresAt":       int64(1789910159876),
			})
		case "/api/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending": false,
				"user": map[string]any{
					"id":        "user-1",
					"name":      "Test User",
					"email":     "user@example.com",
					"authToken": "freebuff-token-test",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalOrigin := freebuffAuthOrigin
	freebuffAuthOrigin = server.URL
	defer func() { freebuffAuthOrigin = originalOrigin }()

	cfg := DefaultConfig()
	cfg.Enabled = true
	provider := newProvider(newSettingsStore(cfg))

	start, errStart := provider.StartLogin(context.Background(), pluginapi.AuthLoginStartRequest{})
	if errStart != nil {
		t.Fatalf("start login failed: %v", errStart)
	}
	if start.URL != "https://freebuff.com/login?auth_code=test-code" {
		t.Fatalf("login URL = %q", start.URL)
	}
	if start.Metadata["mode"] != "vendor" {
		t.Fatalf("login mode = %#v", start.Metadata["mode"])
	}

	poll, errPoll := provider.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: start.State})
	if errPoll != nil {
		t.Fatalf("poll login failed: %v", errPoll)
	}
	if poll.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("poll status = %q", poll.Status)
	}
	storage, errDecode := decodeStorage(poll.Auth.StorageJSON)
	if errDecode != nil {
		t.Fatalf("decode auth: %v", errDecode)
	}
	if storage.AuthToken != "freebuff-token-test" || storage.Email != "user@example.com" {
		t.Fatalf("imported storage = %#v", storage)
	}
	if poll.Auth.Metadata["email"] != "user@example.com" {
		t.Fatalf("auth metadata = %#v", poll.Auth.Metadata)
	}
}

func TestFreebuffVendorLoginFallsBackToManualPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	originalOrigin := freebuffAuthOrigin
	freebuffAuthOrigin = server.URL
	defer func() { freebuffAuthOrigin = originalOrigin }()

	cfg := DefaultConfig()
	cfg.Enabled = true
	provider := newProvider(newSettingsStore(cfg))

	start, errStart := provider.StartLogin(context.Background(), pluginapi.AuthLoginStartRequest{})
	if errStart != nil {
		t.Fatalf("start login failed: %v", errStart)
	}
	if start.URL != tokenPageURL {
		t.Fatalf("fallback URL = %q, want %q", start.URL, tokenPageURL)
	}
	if start.Metadata["mode"] != "manual" {
		t.Fatalf("fallback mode = %#v", start.Metadata["mode"])
	}
}
