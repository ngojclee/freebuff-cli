package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// The host opens a stream bridge before calling executor.execute_stream and passes its id
// in the request. Returning chunks buffers them; returning none and emitting through the
// bridge lets the host deliver each chunk as it arrives. This plugin uses the bridge, so
// a client sees frames when the upstream produces them rather than one lump at the end.

// hostCall is the single indirection this file uses to reach the host. It exists so a
// test can observe exactly which frames the plugin emits, in order, without a C
// toolchain; the production value is the real ABI call in main.go.
var hostCall = callHost

// emitChunk pushes one payload through the host stream bridge.
func emitChunk(streamID string, payload []byte) bool {
	if strings.TrimSpace(streamID) == "" || len(payload) == 0 {
		return false
	}
	raw, errMarshal := json.Marshal(map[string]any{
		"stream_id": streamID,
		"payload":   payload,
	})
	if errMarshal != nil {
		return false
	}
	_, errCall := hostCall(pluginabi.MethodHostStreamEmit, raw)
	return errCall == nil
}

// closeBridge ends the downstream stream. An empty message means a clean finish.
func closeBridge(streamID, message string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	raw, errMarshal := json.Marshal(map[string]string{
		"stream_id": streamID,
		"error":     message,
	})
	if errMarshal != nil {
		return
	}
	_, _ = hostCall(pluginabi.MethodHostStreamClose, raw)
}

// deltaChunk renders one OpenAI streaming frame. It is used only when the plugin has to
// synthesise a frame of its own, for example the terminal frame or an error.
func deltaChunk(model, text string, finish bool) []byte {
	delta := map[string]any{}
	var finishReason any
	if finish {
		finishReason = "stop"
	} else {
		delta["role"] = "assistant"
		delta["content"] = text
	}
	chunk := map[string]any{
		"id":      "chatcmpl-freebuff",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
	raw, errMarshal := json.Marshal(chunk)
	if errMarshal != nil {
		return nil
	}
	return raw
}

// handleExecuteStream answers executor.execute_stream.
//
// With a bridge id the work is handed to a goroutine and the call returns immediately,
// which is what lets the host start flushing. Without one, the finished answer is
// returned as a single chunk so the request still succeeds.
func (r *pluginRuntime) handleExecuteStream(request []byte) []byte {
	req, errParse := parseExecutorRequest(request)
	if errParse != nil {
		return envelopeResult(abiExecutorStreamResponse{
			Headers: jsonHeaders(),
			Chunks:  []pluginapi.ExecutorStreamChunk{{Payload: executeError(newFreebuffError("invalid_request", "the executor request could not be decoded", false, 0))}},
		})
	}
	cfg := r.settings.get()
	if !cfg.Enabled {
		return envelopeResult(abiExecutorStreamResponse{
			Headers: jsonHeaders(),
			Chunks:  []pluginapi.ExecutorStreamChunk{{Payload: executeError(newFreebuffError("plugin_disabled", "freebuff-cli is disabled in configuration", false, 0))}},
		})
	}

	if strings.TrimSpace(req.StreamID) != "" {
		go r.pumpToBridge(cfg, req, req.StreamID)
		return envelopeResult(abiExecutorStreamResponse{Headers: streamHeaders()})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	body, errRun := r.runOnce(ctx, cfg, req)
	if errRun != nil {
		return envelopeResult(abiExecutorStreamResponse{
			Headers: jsonHeaders(),
			Chunks:  []pluginapi.ExecutorStreamChunk{{Payload: executeError(errRun)}},
		})
	}
	return envelopeResult(abiExecutorStreamResponse{
		Headers: streamHeaders(),
		Chunks: []pluginapi.ExecutorStreamChunk{
			{Payload: body},
			{Payload: []byte("data: [DONE]\n\n")},
		},
	})
}

// pumpToBridge performs the run and forwards each upstream SSE frame downstream as it
// arrives, then closes the bridge.
//
// Failure is reported through the bridge rather than by returning an error, because the
// host has already handed the response to the client by the time this goroutine runs.
func (r *pluginRuntime) pumpToBridge(cfg Config, req executorRequest, streamID string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			closeBridge(streamID, "freebuff stream failed unexpectedly")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
	defer cancel()

	upstreamModel, agentID, errResolve := r.resolveModel(cfg, req)
	if errResolve != nil {
		emitChunk(streamID, executeError(errResolve))
		closeBridge(streamID, "")
		return
	}

	pool := r.poolForRequest(cfg, req)
	var lastErr error
	for attempt := 0; attempt < maxExecutorAttempts; attempt++ {
		lease, errAcquire := pool.Acquire(ctx, agentID)
		if errAcquire != nil {
			lastErr = errAcquire
			break
		}
		instanceID := lease.pool.instanceID()
		body, errBuild := buildUpstreamPayload(req.Payload, upstreamModel, lease.run.id, instanceID)
		if errBuild != nil {
			pool.Release(lease)
			lastErr = newFreebuffError("invalid_request", errBuild.Error(), false, 0)
			break
		}

		response, errBody, errCall := r.client.ChatCompletions(ctx, lease.pool.authToken(), body, instanceID)
		if errCall == nil {
			sentAny := r.relayStream(response.Body, streamID)
			_ = response.Body.Close()
			pool.Release(lease)
			if !sentAny {
				emitChunk(streamID, executeError(newFreebuffError("upstream_empty_stream", "the upstream closed the stream without sending a frame", true, 0)))
			}
			logRequestResult(cfg, req.Model, "ok", nil)
			closeBridge(streamID, "")
			return
		}

		lastErr = errCall
		pool.Release(lease)
		r.penalise(pool, lease, errCall, errBody)
		if !isRetryable(errCall) {
			break
		}
	}

	if lastErr == nil {
		lastErr = newFreebuffError("no_account_available", "no Freebuff account could serve the request", true, 0)
	}
	logRequestResult(cfg, req.Model, "failed", lastErr)
	emitChunk(streamID, executeError(lastErr))
	closeBridge(streamID, "")
}

// relayStream forwards each upstream SSE line downstream verbatim.
//
// Nothing is rewritten: the upstream already speaks OpenAI chunk framing, so the client
// sees the same bytes the vendor sent. Blank lines are preserved because they terminate a
// frame; the final [DONE] is forwarded as well.
func (r *pluginRuntime) relayStream(body io.Reader, streamID string) bool {
	reader := bufio.NewReaderSize(body, 64<<10)
	sentAny := false
	for {
		line, errRead := reader.ReadBytes('\n')
		if len(line) > 0 {
			if emitChunk(streamID, line) {
				sentAny = true
			}
		}
		if errRead != nil {
			return sentAny
		}
	}
}
