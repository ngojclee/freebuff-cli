package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// maxExecutorAttempts bounds how many accounts one request may try. Three is enough to
	// survive a cooling account and a queued one without turning a failure into a long
	// stall.
	maxExecutorAttempts = 3

	// maxResponseBytes bounds a non-streaming answer. The upstream can return large
	// completions, so the ceiling is generous but not unbounded.
	maxResponseBytes = 32 << 20
)

// handleExecute answers executor.execute: one complete, non-streaming answer.
func (r *pluginRuntime) handleExecute(request []byte) []byte {
	req, errParse := parseExecutorRequest(request)
	if errParse != nil {
		return executeError(newFreebuffError("invalid_request", "the executor request could not be decoded", false, 0))
	}
	cfg := r.settings.get()
	if !cfg.Enabled {
		return executeError(newFreebuffError("plugin_disabled", "freebuff-cli is disabled in configuration", false, 0))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	defer cancel()

	body, errRun := r.runOnce(ctx, cfg, req)
	if errRun != nil {
		logRequestResult(cfg, req.Model, "failed", errRun)
		return executeError(errRun)
	}
	logRequestResult(cfg, req.Model, "ok", nil)
	return body
}

// runOnce resolves the model, picks an account, and performs the upstream exchange,
// retrying on a retryable failure with the next account.
func (r *pluginRuntime) runOnce(ctx context.Context, cfg Config, req executorRequest) ([]byte, error) {
	upstreamModel, agentID, errResolve := r.resolveModel(cfg, req)
	if errResolve != nil {
		return nil, errResolve
	}

	pool := r.poolForRequest(cfg, req)
	var lastErr error
	for attempt := 0; attempt < maxExecutorAttempts; attempt++ {
		lease, errAcquire := pool.Acquire(ctx, agentID)
		if errAcquire != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errAcquire
		}

		instanceID := lease.pool.instanceID()
		body, errBuild := buildUpstreamPayload(req.Payload, upstreamModel, lease.run.id, instanceID)
		if errBuild != nil {
			pool.Release(lease)
			return nil, newFreebuffError("invalid_request", errBuild.Error(), false, 0)
		}

		response, errBody, errCall := r.client.ChatCompletions(ctx, lease.pool.authToken(), body, instanceID)
		if errCall == nil {
			payload, errRead := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
			_ = response.Body.Close()
			pool.Release(lease)
			if errRead != nil {
				return nil, newFreebuffError("upstream_read_failed", "the upstream response could not be read", true, 0)
			}
			return payload, nil
		}

		lastErr = errCall
		pool.Release(lease)
		r.penalise(pool, lease, errCall, errBody)
		if !isRetryable(errCall) {
			return nil, errCall
		}
	}
	if lastErr == nil {
		lastErr = newFreebuffError("no_account_available", "no Freebuff account could serve the request", true, 0)
	}
	return nil, lastErr
}

// resolveModel turns the published model id into the upstream id and the agent that
// serves it.
func (r *pluginRuntime) resolveModel(cfg Config, req executorRequest) (string, string, error) {
	published := firstNonEmpty(req.Model, payloadModel(req.Payload))
	if published == "" {
		// This is the shape mismatch signature: the host sent a model, but under a key
		// this parser does not read. Logging the key names, and nothing else, makes that
		// visible in one line instead of through a guess.
		hostEventLog("warn", "executor_request_unparsed", map[string]any{
			"keys":        executorRequestKeys(req.RawRequest),
			"payload_len": len(req.Payload),
			"storage_len": len(req.StorageJSON),
		})
		return "", "", newFreebuffError("invalid_request", "the request carried no model", false, 0)
	}
	upstreamModel := cfg.UpstreamModelID(published)
	if upstreamModel == "" {
		return "", "", newFreebuffError("invalid_request", "the request carried no model", false, 0)
	}
	if r.registry == nil {
		return "", "", newFreebuffError("model_registry_unavailable", "the free-agent catalogue is not loaded yet", true, 0)
	}
	agentID, found := r.registry.AgentForModel(upstreamModel)
	if !found {
		return "", "", newFreebuffError("unknown_model", "no free agent serves model "+upstreamModel, false, 0)
	}
	return upstreamModel, agentID, nil
}

// poolForRequest builds the account pool for one request, with the host-selected
// credential first.
func (r *pluginRuntime) poolForRequest(cfg Config, req executorRequest) *tokenPool {
	if storage, errDecode := decodeStorage(req.StorageJSON); errDecode == nil && storage.Valid() {
		accountStore.remember(storage)
	}
	accounts := accountStore.list(req.AuthID)
	return newTokenPool(cfg, r.client, accounts, time.Now)
}

// penalise applies the failure policy: an auth rejection parks the account, an invalid
// session drops the cached run, and everything else is left to the retry loop.
func (r *pluginRuntime) penalise(pool *tokenPool, lease *lease, err error, body []byte) {
	freebuffErr, ok := err.(*freebuffError)
	if !ok {
		pool.Cooldown(lease, waitingRoomCooldown, "unexpected_error")
		return
	}
	switch freebuffErr.Code {
	case "upstream_auth_rejected":
		pool.Cooldown(lease, authCooldown, freebuffErr.Code)
	case "waiting_room_queued", "waiting_room_timeout":
		pool.Cooldown(lease, waitingRoomCooldown, freebuffErr.Code)
	}
	if isSessionInvalid(err) {
		pool.Invalidate(lease, freebuffErr.Code)
	}
	_ = body
}

// executeError renders an OpenAI-shaped error the calling client understands.
func executeError(err error) []byte {
	code := "upstream_error"
	retryable := false
	status := 502
	if freebuffErr, ok := err.(*freebuffError); ok {
		code = freebuffErr.Code
		retryable = freebuffErr.Retryable
		switch freebuffErr.Code {
		case "invalid_request", "unknown_model":
			status = 400
		case "plugin_disabled", "no_accounts":
			status = 503
		case "upstream_auth_rejected":
			status = 401
		case "upstream_rate_limited", "waiting_room_queued", "waiting_room_timeout", "all_accounts_cooling":
			status = 429
		}
	}
	payload := map[string]any{
		"error": map[string]any{
			"message":   safeErrorMessage(err),
			"type":      "freebuff_error",
			"code":      code,
			"retryable": retryable,
			"status":    status,
		},
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return []byte(`{"error":{"message":"freebuff request failed","type":"freebuff_error","code":"upstream_error"}}`)
	}
	return raw
}

// safeErrorMessage keeps the operator-facing message short and free of anything the
// upstream echoed back. Only the plugin's own text is used.
func safeErrorMessage(err error) string {
	if err == nil {
		return "freebuff request failed"
	}
	var freebuffErr *freebuffError
	if errors.As(err, &freebuffErr) && strings.TrimSpace(freebuffErr.Message) != "" {
		return redactReason(freebuffErr.Message)
	}
	return "freebuff request failed"
}

// logRequestResult records one outcome with the model and the error class only. Tokens,
// headers and bodies are never logged.
func logRequestResult(cfg Config, model, outcome string, err error) {
	fields := map[string]any{
		"model":   cfg.UpstreamModelID(model),
		"outcome": outcome,
	}
	if err != nil {
		fields["error"] = errorClass(err)
	}
	level := "info"
	if outcome != "ok" {
		level = "warn"
	}
	hostEventLog(level, "request_completed", fields)
}

var _ = pluginapi.ExecutorRequest{}
