package main

// main exists only so the package is a valid main package under both the cgo and the
// CGO_ENABLED=0 build. The plugin is entered through the exported C symbols in
// main.go, never through this function.
func main() {}
