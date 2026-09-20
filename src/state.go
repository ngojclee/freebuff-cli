package main

import "sync"

// The active pluginRuntime and its guard live in a non-cgo file so the whole auth path stays
// unit-testable without a C toolchain. main.go, which is cgo-only, reads through
// activeRuntime and never owns configuration itself.
var (
	pluginRt   = newRuntime(DefaultConfig())
	pluginRtMu sync.RWMutex
)

func activeRuntime() *pluginRuntime {
	pluginRtMu.RLock()
	defer pluginRtMu.RUnlock()
	return pluginRt
}

func replaceRuntime(next *pluginRuntime) {
	pluginRtMu.Lock()
	pluginRt = next
	pluginRtMu.Unlock()
}
