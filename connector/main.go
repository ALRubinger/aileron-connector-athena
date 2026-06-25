//go:build wasip1

// Package main is the WASM source for the aileron-connector-athena
// read-path connector. It targets Go's native WASI Preview 1
// (`GOOS=wasip1 GOARCH=wasm`) and will call into Aileron's host-import
// ABI for outbound HTTP and credential mediation.
//
// This file is a scaffold placeholder. The host ABI, request dispatch,
// and the AWS Athena read actions land in later issues; for now it only
// has to compile for the wasip1 target.
//
// Build:
//
//	cd connector && GOOS=wasip1 GOARCH=wasm go build -trimpath \
//	  -ldflags="-s -w" -o ../connector.wasm .
//
// Or via Taskfile from the repo root:
//
//	task build
package main

func main() {}
