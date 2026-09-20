package main

import "testing"

func TestPublishedModelIDRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelAliasPrefix = "freebuff"

	published := cfg.PublishedModelID("google/gemini-2.5-flash-lite")
	if published != "freebuff/google/gemini-2.5-flash-lite" {
		t.Fatalf("published = %s", published)
	}
	if upstream := cfg.UpstreamModelID(published); upstream != "google/gemini-2.5-flash-lite" {
		t.Fatalf("upstream = %s", upstream)
	}
}

func TestUpstreamModelIDStripsAnOuterProviderPrefix(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelAliasPrefix = "freebuff"
	// CPA can prepend the provider's own prefix on top of the namespace.
	if upstream := cfg.UpstreamModelID("ln.Freebuff/freebuff/google/gemini-2.5-flash-lite"); upstream != "google/gemini-2.5-flash-lite" {
		t.Fatalf("upstream = %s", upstream)
	}
}

func TestUpstreamModelIDLeavesForeignIDsAlone(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelAliasPrefix = "freebuff"
	if upstream := cfg.UpstreamModelID("gemini-3.8-flash"); upstream != "gemini-3.8-flash" {
		t.Fatalf("upstream = %s", upstream)
	}
}

func TestEmptyAliasPrefixPublishesBareIDs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ModelAliasPrefix = ""
	if published := cfg.PublishedModelID("model-x"); published != "model-x" {
		t.Fatalf("published = %s", published)
	}
	if upstream := cfg.UpstreamModelID("model-x"); upstream != "model-x" {
		t.Fatalf("upstream = %s", upstream)
	}
}

func TestNormalizeUpgradesBareCodebuffHost(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UpstreamBaseURL = "https://codebuff.com/"
	normalized, errNormalize := cfg.Normalize()
	if errNormalize != nil {
		t.Fatalf("normalize: %v", errNormalize)
	}
	if normalized.UpstreamBaseURL != "https://www.codebuff.com" {
		t.Fatalf("base url = %s", normalized.UpstreamBaseURL)
	}
}

func TestValidateRejectsOutOfRangeTimeouts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RequestTimeoutSeconds = 5
	if errValidate := cfg.Validate(); errValidate == nil {
		t.Fatal("expected a validation error for a 5 second request timeout")
	}
}

func TestDecodeConfigReadsOperatorKeys(t *testing.T) {
	raw := []byte("enabled: true\nmodel_alias_prefix: fb\nwaiting_room_timeout_seconds: 30\nmodels:\n  - google/gemini-2.5-flash-lite\n")
	cfg, errDecode := decodeConfig(raw)
	if errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if !cfg.Enabled || cfg.ModelAliasPrefix != "fb" || cfg.WaitingRoomTimeoutSeconds != 30 {
		t.Fatalf("decoded config = %+v", cfg)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("models = %v", cfg.Models)
	}
}
