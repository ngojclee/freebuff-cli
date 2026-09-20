package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// envelopeResult builds the {"ok":true,"result":...} reply the host decodes. A bare
// object decodes as ok=false, which the host logs as a failed plugin call.
func envelopeResult(value any) []byte {
	result, errMarshalResult := json.Marshal(value)
	if errMarshalResult != nil {
		result = []byte(`{}`)
	}
	raw, errMarshal := json.Marshal(pluginabi.Envelope{OK: true, Result: result})
	if errMarshal != nil {
		return []byte(`{"ok":true,"result":{}}`)
	}
	return raw
}

// envelopeError reports a plugin-side failure. The message is surfaced to the operator,
// so it must never carry a token, a header value or file contents.
func envelopeError(code, message string) []byte {
	raw, errMarshal := json.Marshal(pluginabi.Envelope{
		OK:    false,
		Error: &pluginabi.Error{Code: code, Message: message},
	})
	if errMarshal != nil {
		return []byte(`{"ok":true,"result":{}}`)
	}
	return raw
}

// capabilities mirrors the host's rpcCapabilities.
type capabilities struct {
	AuthProvider          bool     `json:"auth_provider"`
	ManagementAPI         bool     `json:"management_api"`
	ModelRegistrar        bool     `json:"model_registrar"`
	ModelProvider         bool     `json:"model_provider"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

// registration is the answer to plugin.register and plugin.reconfigure.
//
// Metadata and ConfigField come from pluginapi verbatim. That package declares them with
// no json tags, so the host expects Go field names such as "GitHubRepository"; rewriting
// those keys as snake_case silently drops the value.
type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  capabilities       `json:"capabilities"`
}

func newRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepository,
			Logo:             logoURL,
			ConfigFields:     configFields(),
		},
		Capabilities: capabilities{
			AuthProvider:          true,
			ManagementAPI:         true,
			ModelRegistrar:        true,
			ModelProvider:         true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeBoth),
			ExecutorInputFormats:  []string{"openai"},
			ExecutorOutputFormats: []string{"openai"},
		},
	}
}

// configFields is what the management UI renders for this plugin, so every operator knob
// must be declared here or it cannot be set through the UI.
func configFields() []pluginapi.ConfigField {
	field := func(name string, kind pluginapi.ConfigFieldType, description string) pluginapi.ConfigField {
		return pluginapi.ConfigField{Name: name, Type: kind, Description: description}
	}
	return []pluginapi.ConfigField{
		field("enabled", pluginapi.ConfigFieldTypeBoolean, "Master switch. Off means the plugin claims no auth files and offers no models. Default false."),
		field("priority", pluginapi.ConfigFieldTypeInteger, "Default credential priority for ordering against other providers. An auth file's own priority overrides it."),
		field("auth_dir", pluginapi.ConfigFieldTypeString, "Directory the plugin watches for Freebuff auth tokens. Leave empty for /root/.cli-proxy-api. Must be an absolute path."),
		field("model_prefix", pluginapi.ConfigFieldTypeString, "Prefix written onto the auth record this plugin owns. CPA applies it to every published model."),
		field("model_alias_prefix", pluginapi.ConfigFieldTypeString, "Namespace this plugin publishes its models under. Default freebuff, so a model is registered as freebuff/<model> and never collides with another provider's identical bare id. The executor strips it before calling upstream. Set it empty to publish bare ids."),
		field("model_label", pluginapi.ConfigFieldTypeString, "Optional override for the user-facing auth label."),
		field("upstream_base_url", pluginapi.ConfigFieldTypeString, "Freebuff backend origin. Default https://www.codebuff.com"),
		field("http_proxy", pluginapi.ConfigFieldTypeString, "Optional HTTP proxy for outbound upstream traffic. Leave empty for a direct connection."),
		field("request_timeout_seconds", pluginapi.ConfigFieldTypeInteger, "Bound on one complete model request, including the stream. Default 900, range 30..3600."),
		field("rotation_interval_seconds", pluginapi.ConfigFieldTypeInteger, "How long one upstream agent run is reused before it is rotated. Default 21600 (6h), range 60..604800."),
		field("model_refresh_seconds", pluginapi.ConfigFieldTypeInteger, "How often the free-agent catalogue is re-fetched. Default 21600 (6h), range 300..604800."),
		field("waiting_room_timeout_seconds", pluginapi.ConfigFieldTypeInteger, "How long one request may wait for the free-tier waiting room before the account is parked and a retryable error is returned. Default 120, range 0..1800."),
		field("disable_cooling", pluginapi.ConfigFieldTypeBoolean, "Mirror of CPA's disable-cooling switch. When true the plugin still returns a retryable error but never parks an account."),
		field("models", pluginapi.ConfigFieldTypeArray, "Model ids to publish in addition to the discovered catalogue. Useful to keep a model reachable when the upstream source drops it."),
	}
}
