package main

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// Config is the operator-facing configuration for the Freebuff plugin. Every field is
// deliberately explicit: this plugin talks to an unofficial upstream, so an operator
// must be able to see and bound every timeout and every namespace it uses.
type Config struct {
	Enabled    bool
	Priority   int
	AuthDir    string
	ModelLabel string

	// ModelPrefix is the prefix written onto the auth record this plugin owns. CPA
	// applies it to every model the plugin publishes.
	ModelPrefix string

	// ModelAliasPrefix is the namespace the plugin publishes its model ids under, so a
	// model is registered as freebuff/<model> and never collides with another
	// provider's identical bare id. The executor strips it before calling upstream.
	ModelAliasPrefix string

	UpstreamBaseURL string
	HTTPProxy       string

	RequestTimeoutSeconds     int
	RotationIntervalSeconds   int
	ModelRefreshSeconds       int
	WaitingRoomTimeoutSeconds int

	// DisableCooling mirrors CPA's disable-cooling switch. When true the plugin still
	// reports a retryable error but never parks a token, so an operator debugging the
	// upstream is not fighting the scheduler.
	DisableCooling bool

	// Models is the operator's pinned list. It is merged with whatever the registry
	// discovers, so a model can be kept reachable even if the upstream source changes.
	Models []string
}

const (
	defaultUpstreamBaseURL = "https://www.codebuff.com"

	defaultRequestTimeoutSeconds     = 900
	defaultRotationIntervalSeconds   = 6 * 60 * 60
	defaultModelRefreshSeconds       = 6 * 60 * 60
	defaultWaitingRoomTimeoutSeconds = 120

	minRequestTimeoutSeconds     = 30
	maxRequestTimeoutSeconds     = 3600
	minRotationIntervalSeconds   = 60
	maxRotationIntervalSeconds   = 7 * 24 * 60 * 60
	minModelRefreshSeconds       = 300
	maxModelRefreshSeconds       = 7 * 24 * 60 * 60
	minWaitingRoomTimeoutSeconds = 0
	maxWaitingRoomTimeoutSeconds = 1800
)

// DefaultConfig is the configuration used when the host sends no config at all.
func DefaultConfig() Config {
	return Config{
		Enabled:                   false,
		Priority:                  0,
		ModelAliasPrefix:          providerKey,
		UpstreamBaseURL:           defaultUpstreamBaseURL,
		RequestTimeoutSeconds:     defaultRequestTimeoutSeconds,
		RotationIntervalSeconds:   defaultRotationIntervalSeconds,
		ModelRefreshSeconds:       defaultModelRefreshSeconds,
		WaitingRoomTimeoutSeconds: defaultWaitingRoomTimeoutSeconds,
	}
}

// Normalize fills defaults and trims operator input. It never invents a credential.
func (c Config) Normalize() (Config, error) {
	c.AuthDir = strings.TrimSpace(c.AuthDir)
	c.ModelLabel = strings.TrimSpace(c.ModelLabel)
	c.ModelPrefix = strings.Trim(strings.TrimSpace(c.ModelPrefix), "/")
	c.ModelAliasPrefix = strings.Trim(strings.TrimSpace(c.ModelAliasPrefix), "/")
	c.HTTPProxy = strings.TrimSpace(c.HTTPProxy)
	c.Models = normalizeModelIDs(c.Models)

	c.UpstreamBaseURL = normalizeUpstreamBaseURL(c.UpstreamBaseURL)
	if c.UpstreamBaseURL == "" {
		c.UpstreamBaseURL = defaultUpstreamBaseURL
	}

	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = defaultRequestTimeoutSeconds
	}
	if c.RotationIntervalSeconds == 0 {
		c.RotationIntervalSeconds = defaultRotationIntervalSeconds
	}
	if c.ModelRefreshSeconds == 0 {
		c.ModelRefreshSeconds = defaultModelRefreshSeconds
	}
	if c.WaitingRoomTimeoutSeconds == 0 {
		c.WaitingRoomTimeoutSeconds = defaultWaitingRoomTimeoutSeconds
	}

	return c, nil
}

// Validate rejects a configuration the plugin cannot honour safely.
func (c Config) Validate() error {
	switch {
	case c.RequestTimeoutSeconds < minRequestTimeoutSeconds || c.RequestTimeoutSeconds > maxRequestTimeoutSeconds:
		return fmt.Errorf("request_timeout_seconds must be between %d and %d", minRequestTimeoutSeconds, maxRequestTimeoutSeconds)
	case c.RotationIntervalSeconds < minRotationIntervalSeconds || c.RotationIntervalSeconds > maxRotationIntervalSeconds:
		return fmt.Errorf("rotation_interval_seconds must be between %d and %d", minRotationIntervalSeconds, maxRotationIntervalSeconds)
	case c.ModelRefreshSeconds < minModelRefreshSeconds || c.ModelRefreshSeconds > maxModelRefreshSeconds:
		return fmt.Errorf("model_refresh_seconds must be between %d and %d", minModelRefreshSeconds, maxModelRefreshSeconds)
	case c.WaitingRoomTimeoutSeconds < minWaitingRoomTimeoutSeconds || c.WaitingRoomTimeoutSeconds > maxWaitingRoomTimeoutSeconds:
		return fmt.Errorf("waiting_room_timeout_seconds must be between %d and %d", minWaitingRoomTimeoutSeconds, maxWaitingRoomTimeoutSeconds)
	case c.AuthDir != "" && !filepath.IsAbs(c.AuthDir):
		return errors.New("auth_dir must be an absolute path")
	}
	if c.HTTPProxy != "" {
		parsed, errParse := url.Parse(c.HTTPProxy)
		if errParse != nil || parsed.Host == "" {
			return errors.New("http_proxy must be a valid absolute URL")
		}
	}
	return nil
}

// PublishedModelID converts an upstream model id into the id CPA publishes.
func (c Config) PublishedModelID(upstreamID string) string {
	upstreamID = strings.TrimSpace(upstreamID)
	if upstreamID == "" {
		return ""
	}
	if c.ModelAliasPrefix == "" {
		return upstreamID
	}
	return c.ModelAliasPrefix + "/" + upstreamID
}

// UpstreamModelID reverses PublishedModelID. It also tolerates a CPA provider prefix in
// front of the namespace, because CPA can prepend the provider's own prefix on top.
func (c Config) UpstreamModelID(publishedID string) string {
	trimmed := strings.TrimSpace(publishedID)
	if trimmed == "" {
		return ""
	}
	namespace := c.ModelAliasPrefix
	if namespace == "" {
		return trimmed
	}
	// Strip every leading segment up to and including the namespace, so
	// "freebuff/gemini-3.8-flash" and "provider/freebuff/gemini-3.8-flash" both work.
	segments := strings.Split(trimmed, "/")
	for index := 0; index < len(segments)-1; index++ {
		if segments[index] == namespace {
			return strings.Join(segments[index+1:], "/")
		}
	}
	return trimmed
}

func normalizeModelIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// normalizeUpstreamBaseURL keeps the operator's origin but upgrades the bare
// codebuff.com host to www.codebuff.com, which is the host the vendor actually serves.
func normalizeUpstreamBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil {
		return raw
	}
	if strings.EqualFold(parsed.Host, "codebuff.com") {
		parsed.Host = "www.codebuff.com"
	}
	return strings.TrimRight(parsed.String(), "/")
}
