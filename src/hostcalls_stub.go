//go:build !cgo

package main

import "errors"

// callHost keeps the unit tests runnable without a C toolchain. Production builds use
// the C-ABI implementation in main.go. Returning an error rather than an empty result
// means a mis-built plugin fails its host calls loudly instead of silently claiming
// success.
func callHost(method string, payload []byte) ([]byte, error) {
	return nil, errors.New("host callback unavailable in a non-cgo build")
}
