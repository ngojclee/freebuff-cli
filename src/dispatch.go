package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// pluginRuntime is the plugin's whole state. A pluginRuntime value is treated as
// immutable once published, and reconfigure publishes a new one rather than editing a
// live struct.
type pluginRuntime struct {
	settings *settingsStore
	auth     *provider
	mgmt     *managementHandler
	client   *freebuffClient
	registry *modelRegistry

	startOnce sync.Once
	stopOnce  sync.Once
}

func newRuntime(cfg Config) *pluginRuntime {
	store := newSettingsStore(cfg)
	client, errClient := newFreebuffClient(cfg)
	if errClient != nil {
		// A bad proxy URL is a configuration error, not a crash: the plugin still loads
		// and reports the problem through the dashboard.
		client = &freebuffClient{baseURL: cfg.UpstreamBaseURL, httpClient: http.DefaultClient}
	}
	registry := newModelRegistry(
		&http.Client{Timeout: 30 * time.Second},
		time.Duration(cfg.ModelRefreshSeconds)*time.Second,
		hostEventLog,
	)
	rt := &pluginRuntime{
		settings: store,
		client:   client,
		registry: registry,
	}
	rt.auth = newProvider(store)
	rt.mgmt = &managementHandler{runtime: rt}
	return rt
}

// envelope wrappers keep the request shapes explicit. The embedded pluginapi types carry
// no json tags, so their fields appear as Go names on the wire; embedding them here
// preserves that and only adds the host callback id the RPC layer appends.
type authParseEnvelope struct {
	pluginapi.AuthParseRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authLoginStartEnvelope struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authLoginPollEnvelope struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authRefreshEnvelope struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// dispatch routes one plugin ABI method. Anything unrecognised answers with a successful
// empty result, which the host treats as "no change".
func (r *pluginRuntime) dispatch(method string, request []byte) []byte {
	ctx := context.Background()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return r.register(request)
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		r.disable()
		return envelopeResult(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodAuthIdentifier:
		return envelopeResult(map[string]string{"identifier": providerKey})
	case pluginabi.MethodAuthParse:
		var envelope authParseEnvelope
		if errUnmarshal := json.Unmarshal(request, &envelope); errUnmarshal != nil {
			return envelopeError("parse_error", "auth.parse request could not be decoded")
		}
		resp, errParse := r.auth.ParseAuth(ctx, envelope.AuthParseRequest)
		if errParse != nil {
			return envelopeError("auth_parse_failed", errParse.Error())
		}
		return envelopeResult(resp)
	case pluginabi.MethodAuthLoginStart:
		var envelope authLoginStartEnvelope
		if errUnmarshal := json.Unmarshal(request, &envelope); errUnmarshal != nil {
			return envelopeError("parse_error", "auth.login.start request could not be decoded")
		}
		resp, errStart := r.auth.StartLogin(ctx, envelope.AuthLoginStartRequest)
		if errStart != nil {
			return envelopeError("login_start_failed", errStart.Error())
		}
		return envelopeResult(resp)
	case pluginabi.MethodAuthLoginPoll:
		var envelope authLoginPollEnvelope
		if errUnmarshal := json.Unmarshal(request, &envelope); errUnmarshal != nil {
			return envelopeError("parse_error", "auth.login.poll request could not be decoded")
		}
		resp, errPoll := r.auth.PollLogin(ctx, envelope.AuthLoginPollRequest)
		if errPoll != nil {
			return envelopeError("login_poll_failed", errPoll.Error())
		}
		return envelopeResult(resp)
	case pluginabi.MethodAuthRefresh:
		var envelope authRefreshEnvelope
		if errUnmarshal := json.Unmarshal(request, &envelope); errUnmarshal != nil {
			return envelopeError("parse_error", "auth.refresh request could not be decoded")
		}
		resp, errRefresh := r.auth.RefreshAuth(ctx, envelope.AuthRefreshRequest)
		if errRefresh != nil {
			return envelopeError("auth_refresh_failed", errRefresh.Error())
		}
		return envelopeResult(resp)
	case pluginabi.MethodCommandLineRegister:
		return envelopeResult(pluginapi.CommandLineRegistrationResponse{
			Flags: []pluginapi.CommandLineFlag{
				{
					Name:         "freebuff-auth-dir",
					Usage:        "Override the directory this plugin watches for Freebuff auth tokens.",
					Type:         "string",
					DefaultValue: "",
				},
			},
		})
	case pluginabi.MethodModelStatic, pluginabi.MethodModelRegister, pluginabi.MethodModelForAuth:
		return envelopeResult(currentModelResponse(r.settings.get(), r.registry))
	case pluginabi.MethodExecutorExecute:
		body := r.handleExecute(request)
		return envelopeResult(pluginapi.ExecutorResponse{
			Payload: body,
			Headers: jsonHeaders(),
		})
	case pluginabi.MethodExecutorExecuteStream:
		return r.handleExecuteStream(request)
	case pluginabi.MethodManagementRegister:
		return envelopeResult(registerManagementRoutes())
	case pluginabi.MethodManagementHandle:
		var envelope struct {
			pluginapi.ManagementRequest
			HostCallbackID string `json:"host_callback_id,omitempty"`
		}
		if errUnmarshal := json.Unmarshal(request, &envelope); errUnmarshal != nil {
			return envelopeError("parse_error", "management.handle request could not be decoded")
		}
		resp, errHandle := r.mgmt.HandleManagement(ctx, envelope.ManagementRequest)
		if errHandle != nil {
			return envelopeError("management_handle_failed", errHandle.Error())
		}
		return envelopeResult(resp)
	default:
		return envelopeResult(map[string]any{})
	}
}

func jsonHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json"}}
}

func streamHeaders() map[string][]string {
	return map[string][]string{"Content-Type": {"text/event-stream"}}
}

// abiExecutorStreamResponse mirrors the host's streaming executor reply, whose channel is
// materialised as an array over the RPC.
type abiExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

// register validates configuration and answers with the capability declaration.
//
// An invalid config is a refusal, not a silent default: the host surfaces the message and
// the plugin stays out of the way, which is the correct outcome when the operator wrote
// something that cannot be honoured safely.
func (r *pluginRuntime) register(request []byte) []byte {
	cfg, errConfigure := decodeConfig(request)
	if errConfigure != nil {
		hostEventLog("error", "config_rejected", map[string]any{"reason": "invalid_config"})
		return envelopeError("config_error", errConfigure.Error())
	}
	r.settings.set(cfg)

	if cfg.Enabled {
		r.startOnce.Do(func() {
			// The catalogue fetch is intentionally not on the registration path: a slow or
			// unreachable GitHub raw endpoint must never delay the host's startup.
			go r.registry.Start(context.Background())
		})
	}

	hostEventLog("info", "configured", map[string]any{
		"enabled":        cfg.Enabled,
		"auth_dir":       redactPath(cfg.ResolvedAuthDir()),
		"accounts_known": accountStore.size(),
	})
	return envelopeResult(newRegistration())
}

func (r *pluginRuntime) disable() {
	cfg := r.settings.get()
	cfg.Enabled = false
	r.settings.set(cfg)
	r.stopOnce.Do(func() { r.registry.Stop() })
}

// executorRequest mirrors pluginapi.ExecutorRequest plus the host fields the RPC layer
// appends. The host decodes untagged Go fields, so these keys are the Go field names.
type executorRequest struct {
	AuthID         string
	AuthProvider   string
	Model          string
	Format         string
	Stream         bool
	Alt            string
	Headers        map[string][]string
	Payload        []byte
	StorageJSON    []byte
	Metadata       map[string]any
	Attributes     map[string]string
	HostCallbackID string
	StreamID       string
}

func parseExecutorRequest(raw []byte) (executorRequest, error) {
	var req executorRequest
	if len(raw) == 0 {
		return req, nil
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		return req, errUnmarshal
	}
	req.AuthID, _ = stringValue(decoded, "auth_id")
	req.AuthProvider, _ = stringValue(decoded, "auth_provider")
	req.Model, _ = stringValue(decoded, "model")
	req.Format, _ = stringValue(decoded, "format")
	req.Stream, _ = boolValue(decoded, "stream")
	req.Alt, _ = stringValue(decoded, "alt")
	req.HostCallbackID, _ = stringValue(decoded, "host_callback_id")
	req.StreamID, _ = stringValue(decoded, "stream_id")
	// Payload and StorageJSON are []byte on the host's struct, so over the JSON RPC they
	// arrive base64 encoded. Reading them as plain text yields a body that parses as
	// nothing, which is how a real request came back as "the request carried no model".
	if value, ok := stringValue(decoded, "payload"); ok {
		req.Payload = decodeHostBytes(value)
	}
	if value, ok := stringValue(decoded, "storage_json"); ok {
		req.StorageJSON = decodeHostBytes(value)
	}
	// The host has used both spellings for the credential attributes bag across releases.
	if value, ok := mapValue(decoded, "auth_attributes", "attributes"); ok {
		req.Attributes = stringMapFromAny(asMap(value))
	}
	return req, nil
}

// payloadModel reads the model field from the caller's own body, which is authoritative
// when the host did not pass one separately.
func payloadModel(payload []byte) string {
	var decoded struct {
		Model string `json:"model"`
	}
	if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(decoded.Model)
}

// decodeHostBytes accepts the host's base64 byte fields and falls back to the raw text, so
// a host that sends the payload unencoded keeps working.
func decodeHostBytes(value string) []byte {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if decoded, errDecode := base64.StdEncoding.DecodeString(value); errDecode == nil {
		return decoded
	}
	return []byte(value)
}

func stringMapFromAny(raw map[string]any) map[string]string {
	if raw == nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		if text, okText := value.(string); okText {
			out[key] = text
		}
	}
	return out
}
