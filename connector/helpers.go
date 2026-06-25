// Pure request-building and arg-validation helpers for the Athena
// connector. This file has NO build tag, so it compiles on every Go
// target (including the host platform) and is unit-testable without the
// wasip1 host imports. main.go (which is `//go:build wasip1`) supplies
// the host ABI and dispatch and calls into these builders.
//
// Each build* function turns an op's `args` map into an AWS-JSON-1.1
// request body for the matching Athena action, returning a
// connector-runtime-style error (no host call yet) when a required arg
// is missing. The vertical slice here covers two actions —
// StartQueryExecution and GetQueryExecution — proving the start → poll
// path end to end. The remaining actions and the SQL read-only gate land
// in later issues.
package main

import (
	"encoding/json"
	"fmt"
)

// requireString reads a required string arg. It returns an error when the
// key is absent, not a string, or the empty string — the same
// fail-on-missing contract every Athena builder needs for its required
// fields.
func requireString(args map[string]any, key string) (string, error) {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

// resolveRegion reads the required `region` arg. There is NO default
// region and no build-time substitution: a missing region is an error
// surfaced as connector_runtime_error before any host call. The region
// must equal the region pinned in manifest.toml's
// [capabilities.network] host and [capabilities.credential].region; the
// connector only checks presence and the host enforces the pin
// fail-closed (an unlisted region is capability_denied at the network
// boundary; a disagreeing region yields a SigV4 signature Athena
// rejects).
func resolveRegion(args map[string]any) (string, error) {
	region, err := requireString(args, "region")
	if err != nil {
		return "", err
	}
	return region, nil
}

// optionalMap returns args[key] when it is a JSON object, else nil. Used
// for the nested Athena request members (QueryExecutionContext,
// ResultConfiguration) that are included only when the caller supplies
// them.
func optionalMap(args map[string]any, key string) map[string]any {
	m, ok := args[key].(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// buildStartQueryExecution constructs the AWS-JSON-1.1 body for Athena's
// StartQueryExecution action.
//
// Required:
//
//	QueryString (string) — the SQL text to execute.
//
// Optional (passed through verbatim only when present):
//
//	QueryExecutionContext (object) — {Database, Catalog}.
//	ResultConfiguration   (object) — {OutputLocation, ...}.
//	WorkGroup             (string).
//	ClientRequestToken    (string) — idempotency token; passthrough-only,
//	                                  never derived or synthesized here.
//
// The SQL read-only gate is intentionally NOT applied in this slice; it
// is layered into this builder in a later issue. The body is emitted as
// AWS-JSON-1.1 (a plain JSON object).
func buildStartQueryExecution(args map[string]any) ([]byte, error) {
	queryString, err := requireString(args, "QueryString")
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"QueryString": queryString,
	}
	if ctx := optionalMap(args, "QueryExecutionContext"); ctx != nil {
		payload["QueryExecutionContext"] = ctx
	}
	if rc := optionalMap(args, "ResultConfiguration"); rc != nil {
		payload["ResultConfiguration"] = rc
	}
	if wg, ok := args["WorkGroup"].(string); ok && wg != "" {
		payload["WorkGroup"] = wg
	}
	if token, ok := args["ClientRequestToken"].(string); ok && token != "" {
		payload["ClientRequestToken"] = token
	}

	return json.Marshal(payload)
}

// buildGetQueryExecution constructs the AWS-JSON-1.1 body for Athena's
// GetQueryExecution action.
//
// Required:
//
//	QueryExecutionId (string) — the id returned by StartQueryExecution.
func buildGetQueryExecution(args map[string]any) ([]byte, error) {
	id, err := requireString(args, "QueryExecutionId")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"QueryExecutionId": id,
	})
}
