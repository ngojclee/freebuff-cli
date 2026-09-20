package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// userAgent mimics the official client. The upstream rejects unknown agents, and the
// value is not a credential, so it is safe to keep in source.
const userAgent = "ai-sdk/openai-compatible/1.0.25/codebuff"

// maxUpstreamErrorBytes bounds how much of an upstream error body is read. The body is
// only ever used to classify the failure; it is never logged verbatim.
const maxUpstreamErrorBytes = 8 << 10

// sessionStatus mirrors the free-tier waiting-room states the upstream reports.
type sessionStatus string

const (
	sessionStatusDisabled   sessionStatus = "disabled"
	sessionStatusNone       sessionStatus = "none"
	sessionStatusQueued     sessionStatus = "queued"
	sessionStatusActive     sessionStatus = "active"
	sessionStatusEnded      sessionStatus = "ended"
	sessionStatusSuperseded sessionStatus = "superseded"
)

// freebuffSession is the waiting-room state for one account.
type freebuffSession struct {
	Status      sessionStatus
	InstanceID  string
	Position    int
	QueueDepth  int
	ExpiresAt   time.Time
	RemainingMs int64
	Message     string
}

// freebuffError is a typed upstream failure. Code is a stable, loggable classification;
// it never contains upstream text.
type freebuffError struct {
	Code       string
	Message    string
	Retryable  bool
	HTTPStatus int
}

func (e *freebuffError) Error() string {
	if e == nil {
		return "freebuff upstream error"
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func newFreebuffError(code, message string, retryable bool, status int) *freebuffError {
	return &freebuffError{Code: code, Message: message, Retryable: retryable, HTTPStatus: status}
}

// freebuffClient speaks the three upstream calls this plugin needs: agent runs, chat
// completions, and the free-tier session.
type freebuffClient struct {
	baseURL    string
	httpClient *http.Client
}

func newFreebuffClient(cfg Config) (*freebuffClient, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.HTTPProxy != "" {
		proxyURL, errParse := url.Parse(cfg.HTTPProxy)
		if errParse != nil {
			return nil, fmt.Errorf("parse http_proxy: %w", errParse)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &freebuffClient{
		baseURL: strings.TrimRight(cfg.UpstreamBaseURL, "/"),
		httpClient: &http.Client{
			Timeout:   time.Duration(cfg.RequestTimeoutSeconds) * time.Second,
			Transport: transport,
		},
	}, nil
}

// StartRun opens an agent run. The upstream bills and rate-limits per run, so a run is
// created before the first chat completion and finished when the exchange is over.
func (c *freebuffClient) StartRun(ctx context.Context, authToken, agentID string) (string, error) {
	body, errMarshal := json.Marshal(map[string]any{
		"action":  "START",
		"agentId": agentID,
	})
	if errMarshal != nil {
		return "", fmt.Errorf("encode start run: %w", errMarshal)
	}

	response, errDo := c.doJSON(ctx, authToken, "/api/v1/agent-runs", body)
	if errDo != nil {
		return "", errDo
	}
	defer func() { _ = response.Body.Close() }()

	payload, errRead := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorBytes))
	if errRead != nil {
		return "", fmt.Errorf("read start run response: %w", errRead)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", classifyUpstreamFailure(response.StatusCode, payload)
	}

	var parsed struct {
		RunID string `json:"runId"`
	}
	if errUnmarshal := json.Unmarshal(payload, &parsed); errUnmarshal != nil {
		return "", newFreebuffError("upstream_protocol_error", "start run response was not JSON", true, response.StatusCode)
	}
	if strings.TrimSpace(parsed.RunID) == "" {
		return "", newFreebuffError("upstream_protocol_error", "start run response carried no runId", true, response.StatusCode)
	}
	return parsed.RunID, nil
}

// FinishRun closes an agent run. A failure here must never fail the caller's request:
// the answer is already on the wire, and a leaked run expires upstream on its own.
func (c *freebuffClient) FinishRun(ctx context.Context, authToken, runID string, totalSteps int) error {
	if strings.TrimSpace(runID) == "" {
		return nil
	}
	body, errMarshal := json.Marshal(map[string]any{
		"action":        "FINISH",
		"runId":         runID,
		"status":        "completed",
		"totalSteps":    totalSteps,
		"directCredits": 0,
		"totalCredits":  0,
	})
	if errMarshal != nil {
		return fmt.Errorf("encode finish run: %w", errMarshal)
	}

	response, errDo := c.doJSON(ctx, authToken, "/api/v1/agent-runs", body)
	if errDo != nil {
		return errDo
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorBytes))
		return classifyUpstreamFailure(response.StatusCode, payload)
	}
	return nil
}

// ChatCompletions forwards the caller's body after metadata injection. A non-2xx reply
// is returned as a typed error together with the raw body so the executor can decide
// whether to retry on another account.
func (c *freebuffClient) ChatCompletions(ctx context.Context, authToken string, body []byte, instanceID string) (*http.Response, []byte, error) {
	response, errDo := c.doJSON(ctx, authToken, "/api/v1/chat/completions", body)
	if errDo != nil {
		return nil, nil, errDo
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil, nil
	}
	payload, errRead := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorBytes))
	_ = response.Body.Close()
	if errRead != nil {
		return response, nil, fmt.Errorf("read upstream error body: %w", errRead)
	}
	return response, payload, classifyUpstreamFailure(response.StatusCode, payload)
}

// CreateOrRefreshSession joins the free-tier waiting room. A 404 means the upstream does
// not use a waiting room for this account, which is a normal "disabled" answer.
func (c *freebuffClient) CreateOrRefreshSession(ctx context.Context, authToken string) (freebuffSession, error) {
	return c.doSessionRequest(ctx, http.MethodPost, authToken, "")
}

// GetSession polls the waiting-room position for an already created session.
func (c *freebuffClient) GetSession(ctx context.Context, authToken, instanceID string) (freebuffSession, error) {
	return c.doSessionRequest(ctx, http.MethodGet, authToken, instanceID)
}

// EndSession releases a slot. It is best-effort.
func (c *freebuffClient) EndSession(ctx context.Context, authToken string) error {
	requestURL, errJoin := url.JoinPath(c.baseURL, "/api/v1/freebuff/session")
	if errJoin != nil {
		return fmt.Errorf("build session url: %w", errJoin)
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodDelete, requestURL, nil)
	if errRequest != nil {
		return fmt.Errorf("build session request: %w", errRequest)
	}
	c.applyCommonHeaders(request, authToken)

	response, errDo := c.httpClient.Do(request)
	if errDo != nil {
		return fmt.Errorf("send session delete: %w", errDo)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorBytes))
		return classifyUpstreamFailure(response.StatusCode, payload)
	}
	return nil
}

func (c *freebuffClient) doSessionRequest(ctx context.Context, method, authToken, instanceID string) (freebuffSession, error) {
	requestURL, errJoin := url.JoinPath(c.baseURL, "/api/v1/freebuff/session")
	if errJoin != nil {
		return freebuffSession{}, fmt.Errorf("build session url: %w", errJoin)
	}
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewReader([]byte("{}"))
	}
	request, errRequest := http.NewRequestWithContext(ctx, method, requestURL, body)
	if errRequest != nil {
		return freebuffSession{}, fmt.Errorf("build session request: %w", errRequest)
	}
	c.applyCommonHeaders(request, authToken)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodGet && strings.TrimSpace(instanceID) != "" {
		request.Header.Set("x-freebuff-instance-id", instanceID)
	}

	response, errDo := c.httpClient.Do(request)
	if errDo != nil {
		return freebuffSession{}, fmt.Errorf("send session request: %w", errDo)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotFound {
		return freebuffSession{Status: sessionStatusDisabled}, nil
	}
	payload, errRead := io.ReadAll(io.LimitReader(response.Body, maxUpstreamErrorBytes))
	if errRead != nil {
		return freebuffSession{}, fmt.Errorf("read session response: %w", errRead)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return freebuffSession{}, classifyUpstreamFailure(response.StatusCode, payload)
	}

	var decoded struct {
		Status      string `json:"status"`
		InstanceID  string `json:"instanceId"`
		Position    int    `json:"position"`
		QueueDepth  int    `json:"queueDepth"`
		ExpiresAt   string `json:"expiresAt"`
		RemainingMs int64  `json:"remainingMs"`
		Message     string `json:"message"`
	}
	if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
		return freebuffSession{}, newFreebuffError("upstream_protocol_error", "session response was not JSON", true, response.StatusCode)
	}
	session := freebuffSession{
		Status:      sessionStatus(strings.TrimSpace(decoded.Status)),
		InstanceID:  strings.TrimSpace(decoded.InstanceID),
		Position:    decoded.Position,
		QueueDepth:  decoded.QueueDepth,
		RemainingMs: decoded.RemainingMs,
		Message:     redactReason(decoded.Message),
	}
	if session.Status == "" {
		session.Status = sessionStatusNone
	}
	if parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(decoded.ExpiresAt)); errParse == nil {
		session.ExpiresAt = parsed
	}
	return session, nil
}

func (c *freebuffClient) doJSON(ctx context.Context, authToken, path string, body []byte) (*http.Response, error) {
	requestURL, errJoin := url.JoinPath(c.baseURL, path)
	if errJoin != nil {
		return nil, fmt.Errorf("build upstream url: %w", errJoin)
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if errRequest != nil {
		return nil, fmt.Errorf("build upstream request: %w", errRequest)
	}
	c.applyCommonHeaders(request, authToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	response, errDo := c.httpClient.Do(request)
	if errDo != nil {
		return nil, newFreebuffError("upstream_unreachable", "upstream request could not be sent", true, 0)
	}
	return response, nil
}

func (c *freebuffClient) applyCommonHeaders(request *http.Request, authToken string) {
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(authToken))
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
}

// upstreamErrorCode extracts the machine-readable error field the upstream returns.
func upstreamErrorCode(payload []byte) string {
	var decoded struct {
		Error string `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(payload, &decoded); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(decoded.Error)
}

// classifyUpstreamFailure turns a status and body into a typed error. The upstream text
// is never copied into the message; only the machine-readable code is, and only after it
// has been matched against the known set.
func classifyUpstreamFailure(status int, payload []byte) *freebuffError {
	code := upstreamErrorCode(payload)
	switch code {
	case "freebuff_update_required":
		return newFreebuffError("upstream_update_required", "the upstream client is too old", false, status)
	case "waiting_room_required", "waiting_room_queued":
		return newFreebuffError("waiting_room_queued", "the account is queued in the free-tier waiting room", true, status)
	case "session_superseded", "session_expired":
		return newFreebuffError("session_invalid", "the free-tier session is no longer valid", true, status)
	case "run_not_found", "run_invalid", "run_expired":
		return newFreebuffError("run_invalid", "the agent run is no longer valid", true, status)
	}

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return newFreebuffError("upstream_auth_rejected", "the upstream rejected this account", true, status)
	case status == http.StatusTooManyRequests:
		return newFreebuffError("upstream_rate_limited", "the upstream rate limited this account", true, status)
	case status >= 500:
		return newFreebuffError("upstream_unavailable", "the upstream is unavailable", true, status)
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return newFreebuffError("upstream_rejected_request", "the upstream rejected the request", false, status)
	default:
		return newFreebuffError("upstream_error", "the upstream returned an unexpected status", true, status)
	}
}

// isSessionInvalid reports whether an error means the cached session must be dropped.
func isSessionInvalid(err error) bool {
	var freebuffErr *freebuffError
	if !errors.As(err, &freebuffErr) {
		return false
	}
	switch freebuffErr.Code {
	case "session_invalid", "waiting_room_queued", "run_invalid", "upstream_update_required":
		return true
	default:
		return false
	}
}
