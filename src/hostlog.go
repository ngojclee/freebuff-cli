package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

// hostEventLog is the plugin's only diagnostic egress.
//
// Callers pass an event name and small scalar fields. Tokens, auth file contents and
// header values must never be placed in fields, and the auth-dir helper redacts path
// prefixes before they reach here.
func hostEventLog(level, event string, fields map[string]any) {
	payload := map[string]any{
		"level":   level,
		"message": pluginID + ": " + event,
	}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return
	}
	_, _ = callHost(pluginabi.MethodHostLog, raw)
}
