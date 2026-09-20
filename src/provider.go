package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// tokenPageURL is the vendor's own page that displays an account's auth token. The
// plugin never invents a private token exchange: the operator signs in there and pastes
// the token, or drops the CLI's credentials file into the auth directory.
const tokenPageURL = "https://freebuff.llm.pm"

// settingsStore holds the live configuration behind a mutex. A pluginRuntime is
// published as a whole on reconfigure, so readers never see a half-applied config.
type settingsStore struct {
	mu  sync.RWMutex
	cfg Config
}

func newSettingsStore(cfg Config) *settingsStore {
	return &settingsStore{cfg: cfg}
}

func (s *settingsStore) get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *settingsStore) set(cfg Config) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

// provider implements the host's auth-provider interface for Freebuff accounts.
type provider struct {
	settings *settingsStore
	logins   *loginStore
	now      func() time.Time
}

func newProvider(settings *settingsStore) *provider {
	return &provider{settings: settings, logins: newLoginStore(), now: time.Now}
}

func (p *provider) Identifier() string { return providerKey }

// ParseAuth recognises a Freebuff credential and converts it into a host auth record.
//
// Handled is only true when the payload is positively identified, so an unrelated
// provider's file in the same directory is never claimed.
func (p *provider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if len(req.RawJSON) == 0 {
		return pluginapi.AuthParseResponse{}, nil
	}
	storage, handled, errParse := ParseAuthFile(req.RawJSON, req.Path)
	if !handled {
		return pluginapi.AuthParseResponse{}, nil
	}
	if errParse != nil {
		return pluginapi.AuthParseResponse{Handled: true}, errParse
	}
	if !storage.Valid() {
		return pluginapi.AuthParseResponse{Handled: true}, errors.New("freebuff auth has no usable token")
	}
	if storage.SourceFile == "" {
		storage.SourceFile = req.Path
	}
	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth:    buildAuthData(storage, p.settings.get()),
	}, nil
}

// StartLogin creates a Freebuff browser session. The vendor's auth page completes the
// sign-in and the plugin polls the matching vendor session for the auth token, so the
// operator does not have to copy the token manually.
func (p *provider) StartLogin(ctx context.Context, _ pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	cfg := p.settings.get()
	if !cfg.Enabled {
		return pluginapi.AuthLoginStartResponse{}, errors.New("freebuff-cli is disabled in configuration")
	}
	ttl := 15 * time.Minute
	state, expiresAt, errBegin := p.logins.begin(ttl)
	if errBegin != nil {
		return pluginapi.AuthLoginStartResponse{}, errBegin
	}
	loginURL := tokenPageURL
	instructions := "Open the Freebuff page, sign in, and leave the tab open. The plugin polls the Freebuff session and imports the auth token automatically."
	mode := "vendor"
	vendorSession, errVendor := startFreebuffLogin(ctx)
	if errVendor != nil {
		mode = "manual"
		instructions = "Open the Freebuff page, complete the token flow, then save auth-tokens.json into the auth directory. This page detects the new record automatically."
		hostEventLog("warn", "vendor_login_start_failed", map[string]any{
			"reason": "freebuff_auth_code_unavailable",
		})
	} else {
		p.logins.attachVendorSession(state, vendorSession)
		loginURL = vendorSession.LoginURL
	}
	hostEventLog("info", "login_started", map[string]any{
		"auth_dir": redactPath(cfg.ResolvedAuthDir()),
		"mode":     mode,
	})
	return pluginapi.AuthLoginStartResponse{
		Provider:  providerKey,
		URL:       loginURL,
		State:     state,
		ExpiresAt: expiresAt,
		Metadata: map[string]any{
			"mode":         mode,
			"instructions": instructions,
			"auth_dir":     redactPath(cfg.ResolvedAuthDir()),
		},
	}, nil
}

// PollLogin first checks the vendor session created by StartLogin, then falls back to
// watching the auth directory for the manual token-file flow.
func (p *provider) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	cfg := p.settings.get()
	if !cfg.Enabled {
		return pluginapi.AuthLoginPollResponse{}, errors.New("freebuff-cli is disabled in configuration")
	}
	pending, found := p.logins.lookup(req.State)
	if !found {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "this login flow is unknown or has expired; start a new one",
		}, nil
	}
	if vendorSession, hasVendor := p.logins.vendorSessionFor(req.State); hasVendor {
		status, errPoll := pollFreebuffLogin(ctx, vendorSession)
		if errPoll == nil {
			if status.Error != "" {
				return pluginapi.AuthLoginPollResponse{
					Status:  pluginapi.AuthLoginStatusError,
					Message: status.Error,
				}, nil
			}
			if !status.Pending && status.User != nil && strings.TrimSpace(status.User.AuthToken) != "" {
				storage := Storage{
					Type:       providerKey,
					AuthToken:  status.User.AuthToken,
					Email:      firstNonEmpty(status.User.Email, status.User.Name, status.User.ID),
					SourceFile: "vendor-login:" + firstNonEmpty(status.User.ID, status.User.Email, "default"),
				}
				p.logins.finish(req.State)
				hostEventLog("info", "login_completed", map[string]any{"account": storage.DisplayLabel()})
				return pluginapi.AuthLoginPollResponse{
					Status: pluginapi.AuthLoginStatusSuccess,
					Auth:   buildAuthData(storage, cfg),
				}, nil
			}
		}
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "waiting for the Freebuff browser sign-in to complete",
		}, nil
	}
	storage, _, ok, errFind := findLatestAuthFile(cfg.ResolvedAuthDir(), pending.startedAt)
	if errFind != nil {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "the auth directory could not be read",
		}, nil
	}
	if !ok {
		return pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "waiting for a Freebuff auth token to appear in the auth directory",
		}, nil
	}
	p.logins.finish(req.State)
	hostEventLog("info", "login_completed", map[string]any{"account": storage.DisplayLabel()})
	return pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auth:   buildAuthData(storage, cfg),
	}, nil
}

// RefreshAuth re-issues the auth record.
//
// A Freebuff auth token has no companion refresh token and no published expiry, so there
// is nothing to exchange. The honest behaviour is to hand the record back unchanged and
// schedule the next check far out; liveness is proven on the first real request, where a
// rejected token parks the account instead of silently failing every call.
func (p *provider) RefreshAuth(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	cfg := p.settings.get()
	storage, errDecode := decodeStorage(req.StorageJSON)
	if errDecode != nil {
		return pluginapi.AuthRefreshResponse{}, errDecode
	}
	if !storage.Valid() {
		return pluginapi.AuthRefreshResponse{}, errors.New("freebuff auth has no usable token")
	}
	next := p.now().Add(time.Duration(cfg.RotationIntervalSeconds) * time.Second)
	return pluginapi.AuthRefreshResponse{
		Auth:             buildAuthData(storage, cfg),
		NextRefreshAfter: next,
	}, nil
}

// buildAuthData maps provider storage onto the host's auth record.
func buildAuthData(storage Storage, cfg Config) pluginapi.AuthData {
	attributes := map[string]string{
		"token_type": "Bearer",
	}
	// CPA's scheduler compares credentials by their "priority" attribute, not by anything
	// in the metadata: sdk/cliproxy/auth/selector.go authPriority() reads
	// auth.Attributes["priority"]. The auth file's own value wins, then the plugin default.
	priority := cfg.Priority
	if storage.Priority != nil {
		priority = *storage.Priority
	}
	if priority != 0 {
		attributes["priority"] = strconv.Itoa(priority)
	}

	label := storage.DisplayLabel()
	if cfg.ModelLabel != "" {
		label = cfg.ModelLabel
	}
	metadata := map[string]any{
		"source": "freebuff-token",
	}
	// The management UI reads the account from Metadata["email"] and falls back to the
	// file name, which is why an auth without this key showed a path instead of an account.
	if email := firstNonEmpty(storage.Email, storage.Label); email != "" {
		metadata["email"] = email
		attributes["email"] = email
	}

	prefix := strings.TrimSpace(storage.Prefix)
	if prefix == "" {
		prefix = cfg.ModelPrefix
	}

	return pluginapi.AuthData{
		Provider:    providerKey,
		ID:          storage.AuthID(),
		FileName:    authFileName(storage.SourceFile),
		Label:       label,
		Prefix:      strings.Trim(prefix, "/"),
		Disabled:    storage.Disabled,
		StorageJSON: storage.EncodeJSON(),
		Metadata:    metadata,
		Attributes:  attributes,
	}
}

// EncodeJSON is a thin alias kept on Storage so call sites read naturally.
func (s Storage) EncodeJSON() []byte { return s.EncodeStorage() }

// redactPath keeps only the final path element, so a log line can identify which store
// was scanned without disclosing the operator's full directory layout.
func redactPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return "<path>"
	}
	return ".../" + parts[len(parts)-1]
}

// ResolvedAuthDir is where the plugin looks for operator-supplied credentials.
func (c Config) ResolvedAuthDir() string {
	if dir := strings.TrimSpace(c.AuthDir); dir != "" {
		return dir
	}
	return "/root/.cli-proxy-api"
}
