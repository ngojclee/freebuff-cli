package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	uint8_t* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

// These prototypes must match what cgo generates from the Go signatures below, which
// are non-const. Declaring const here would be a conflicting C declaration. The host's
// own typedef uses const pointers, so assignment goes through an explicit
// function-pointer cast, which is the pattern the shipped plugins already use.
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host = 0;

static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == 0 || stored_host->call == 0) return 1;
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != 0 && stored_host->free_buffer != 0 && ptr != 0) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(1)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil || response == nil {
		return 1
	}
	methodName := C.GoString(method)

	// Read-only view of the host-owned buffer. Justified by
	// internal/pluginhost/loader_unix.go:179-185, where the host allocates the request
	// with C.CBytes and frees it with a deferred C.free only after this call returns,
	// and by the const uint8_t* request parameter in the ABI signature. The view is a
	// local that is never retained past this function.
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = unsafe.Slice((*byte)(unsafe.Pointer(request)), int(requestLen))
	}

	raw := activeRuntime().dispatch(methodName, requestBytes)
	writeABIResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	// Disabled on shutdown so a quiescing plugin can never claim an auth file.
	replaceRuntime(newRuntime(Config{Enabled: false}))
}

func writeABIResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = (*C.uint8_t)(ptr)
	response.len = C.size_t(len(raw))
}

// callHost invokes a host callback, copying the response out before releasing it
// through the host allocator so no C memory outlives this frame.
func callHost(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}
	var response C.cliproxy_buffer
	rc := C.call_host_api(cMethod, request, C.size_t(len(payload)), &response)
	if rc != 0 {
		if response.ptr != nil {
			C.free_host_buffer(unsafe.Pointer(response.ptr), response.len)
		}
		return nil, errors.New("host callback failed")
	}
	if response.ptr == nil || response.len == 0 {
		return nil, nil
	}
	out := C.GoBytes(unsafe.Pointer(response.ptr), C.int(response.len))
	C.free_host_buffer(unsafe.Pointer(response.ptr), response.len)
	return out, nil
}
