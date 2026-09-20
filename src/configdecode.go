package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// rawConfig mirrors the operator keys exactly. Every pointer exists so "absent" is
// distinguishable from "explicitly zero": a zero timeout must become the documented
// default, while an explicitly disabled plugin must stay disabled.
type rawConfig struct {
	Enabled                   *bool     `yaml:"enabled"`
	Priority                  *int      `yaml:"priority"`
	AuthDir                   *string   `yaml:"auth_dir"`
	ModelLabel                *string   `yaml:"model_label"`
	ModelPrefix               *string   `yaml:"model_prefix"`
	ModelAliasPrefix          *string   `yaml:"model_alias_prefix"`
	UpstreamBaseURL           *string   `yaml:"upstream_base_url"`
	HTTPProxy                 *string   `yaml:"http_proxy"`
	RequestTimeoutSeconds     *int      `yaml:"request_timeout_seconds"`
	RotationIntervalSeconds   *int      `yaml:"rotation_interval_seconds"`
	ModelRefreshSeconds       *int      `yaml:"model_refresh_seconds"`
	WaitingRoomTimeoutSeconds *int      `yaml:"waiting_room_timeout_seconds"`
	DisableCooling            *bool     `yaml:"disable_cooling"`
	Models                    *[]string `yaml:"models"`
}

// decodeConfig reads the host lifecycle payload.
//
// The host sends rpcLifecycleRequest{ConfigYAML []byte}, and a Go []byte field becomes a
// base64 string inside JSON. Older or alternative hosts may hand over the YAML document
// directly, so both shapes are accepted.
func decodeConfig(request []byte) (Config, error) {
	cfg := DefaultConfig()
	raw := lifecycleConfigYAML(request)
	if len(raw) == 0 {
		normalized, errNormalize := cfg.Normalize()
		if errNormalize != nil {
			return cfg, errNormalize
		}
		return normalized, normalized.Validate()
	}

	var parsed rawConfig
	if errUnmarshal := yaml.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return cfg, fmt.Errorf("plugin config could not be parsed: %w", errUnmarshal)
	}
	if parsed.Enabled != nil {
		cfg.Enabled = *parsed.Enabled
	}
	if parsed.Priority != nil {
		cfg.Priority = *parsed.Priority
	}
	if parsed.AuthDir != nil {
		cfg.AuthDir = *parsed.AuthDir
	}
	if parsed.ModelLabel != nil {
		cfg.ModelLabel = *parsed.ModelLabel
	}
	if parsed.ModelPrefix != nil {
		cfg.ModelPrefix = *parsed.ModelPrefix
	}
	if parsed.ModelAliasPrefix != nil {
		cfg.ModelAliasPrefix = *parsed.ModelAliasPrefix
	}
	if parsed.UpstreamBaseURL != nil {
		cfg.UpstreamBaseURL = *parsed.UpstreamBaseURL
	}
	if parsed.HTTPProxy != nil {
		cfg.HTTPProxy = *parsed.HTTPProxy
	}
	if parsed.RequestTimeoutSeconds != nil {
		cfg.RequestTimeoutSeconds = *parsed.RequestTimeoutSeconds
	}
	if parsed.RotationIntervalSeconds != nil {
		cfg.RotationIntervalSeconds = *parsed.RotationIntervalSeconds
	}
	if parsed.ModelRefreshSeconds != nil {
		cfg.ModelRefreshSeconds = *parsed.ModelRefreshSeconds
	}
	if parsed.WaitingRoomTimeoutSeconds != nil {
		cfg.WaitingRoomTimeoutSeconds = *parsed.WaitingRoomTimeoutSeconds
	}
	if parsed.DisableCooling != nil {
		cfg.DisableCooling = *parsed.DisableCooling
	}
	if parsed.Models != nil {
		cfg.Models = *parsed.Models
	}

	normalized, errNormalize := cfg.Normalize()
	if errNormalize != nil {
		return normalized, errNormalize
	}
	if errValidate := normalized.Validate(); errValidate != nil {
		return normalized, errValidate
	}
	return normalized, nil
}

// lifecycleConfigYAML extracts the YAML document from whichever envelope shape arrived.
func lifecycleConfigYAML(request []byte) []byte {
	if len(request) == 0 {
		return nil
	}
	var envelope struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if errUnmarshal := json.Unmarshal(request, &envelope); errUnmarshal == nil {
		return envelope.ConfigYAML
	}
	// A base64 string that json.Unmarshal refused to decode into []byte.
	var stringEnvelope struct {
		ConfigYAML string `json:"config_yaml"`
	}
	if errUnmarshal := json.Unmarshal(request, &stringEnvelope); errUnmarshal == nil && stringEnvelope.ConfigYAML != "" {
		if decoded, errDecode := base64.StdEncoding.DecodeString(stringEnvelope.ConfigYAML); errDecode == nil {
			return decoded
		}
		return []byte(stringEnvelope.ConfigYAML)
	}
	// Not JSON at all: assume a raw YAML document.
	if !json.Valid(request) {
		return request
	}
	return nil
}
