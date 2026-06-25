package main

import "testing"

// TestPlaceholder gives the host platform a buildable test package while
// main.go is gated behind //go:build wasip1. It is intentionally empty;
// real tests for the helpers and dispatch land in later issues.
func TestPlaceholder(t *testing.T) {}
