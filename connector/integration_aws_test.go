//go:build athena_integration

// Live, real-AWS integration test for the Athena read-path connector.
//
// This file is gated behind the `athena_integration` build tag and is
// therefore EXCLUDED from the default `task test` / `go test ./...`
// suite. It is run only on explicit request:
//
//	task test:integration:aws
//
// or directly:
//
//	go test -tags athena_integration -run TestIntegrationAthenaRoundTrip -v ./...
//
// It proves acceptance criterion 9: a real, signed StartQueryExecution ->
// poll get_query_execution -> GetQueryResults round-trip against a live
// AWS account. There is deliberately NO t.Skip in this file. Selection is
// by build tag alone (mirroring aileron's `integration_sandbox` pattern):
// when the tag is absent the file does not compile into the test binary at
// all, so the default suite is unaffected; when the tag IS present the
// caller has opted in and the test runs to completion or fails — it never
// silently no-ops.
//
// Why the aws CLI rather than the connector binary directly?  main.go is
// `//go:build wasip1` and reaches AWS through Aileron's host-import ABI
// (hostHTTPRequest et al.), which only exists inside the Aileron WASM
// sandbox. A host-platform `go test` process has no such host, so it
// cannot drive main.go's signed path. Instead this test reuses the SAME
// request bodies the connector builds — the build* functions in
// helpers.go (buildStartQueryExecution, buildGetQueryExecution,
// buildGetQueryResults) — and submits them through the `aws athena` CLI as
// a host subprocess. The CLI signs with SigV4 from the ambient AWS
// credential chain, exactly as the Aileron host would at the network
// boundary. So the wire bodies under test are the connector's own; only
// the transport/signing differs.
//
// Configuration is by environment, with NO defaults (mirroring the
// connector's own no-default-region contract):
//
//	ATHENA_INTEGRATION_REGION   (required) AWS region, e.g. us-east-1.
//	                            Absent => the test FAILS (not skips).
//	ATHENA_INTEGRATION_WORKGROUP (optional) Athena work group; defaults to
//	                            "primary" when unset.
//	ATHENA_INTEGRATION_OUTPUT   (optional) s3:// result-output location.
//	                            Required only if the work group does not
//	                            already enforce one; absent is fine for a
//	                            work group with managed/enforced output.
//	ATHENA_INTEGRATION_QUERY    (optional) the read-only SQL to run;
//	                            defaults to "SELECT 1". MUST be read-only —
//	                            it passes through the connector's own
//	                            validateReadOnlySQL gate before submission.
//
// Read-only by construction: the only query submitted is a SELECT (or the
// caller's read-only override, which is still validated read-only), and
// every other CLI call is a read (get-query-execution, get-query-results).
// The intended target is a throwaway, read-only AWS account; nothing here
// creates, mutates, or deletes any resource.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// awsCLI runs `aws <args...>` with the given JSON request body passed as
// --cli-input-json, returning the parsed stdout. The body is one of the
// connector's own build* outputs, so the wire shape under test is the
// connector's. The --region flag comes from the required env region; the
// CLI signs the call with SigV4 from the ambient credential chain.
func awsCLI(ctx context.Context, t *testing.T, region, service, action string, body []byte) map[string]any {
	t.Helper()

	args := []string{
		service, action,
		"--region", region,
		"--cli-input-json", string(body),
		"--output", "json",
	}
	cmd := exec.CommandContext(ctx, "aws", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("aws %s %s failed: %v\n--- args ---\n%v\n--- stderr ---\n%s",
			service, action, err, args, stderr.String())
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		// Some actions legitimately return an empty body; callers that
		// need fields assert on them, so an empty map is fine here.
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("aws %s %s: stdout is not JSON: %v\n--- stdout ---\n%s",
			service, action, err, out)
	}
	return parsed
}

// requireEnv reads a required env var. Mirroring the connector's
// no-default-region contract, absence is a hard failure of THIS test (the
// caller opted in via the build tag), never a skip.
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Fatalf("%s is required for the athena_integration test and has no default; "+
			"set it to the target AWS region (e.g. us-east-1)", key)
	}
	return v
}

// envOr reads an optional env var, falling back to def when unset/empty.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// TestIntegrationAthenaRoundTrip performs the real signed round-trip:
// build the StartQueryExecution body with the connector's builder, submit
// it via the aws CLI, poll get-query-execution until the query reaches a
// terminal state, then fetch the rows with get-query-results — every body
// built by the connector's own helpers.go builders. This is the live proof
// of acceptance criterion 9.
func TestIntegrationAthenaRoundTrip(t *testing.T) {
	region := requireEnv(t, "ATHENA_INTEGRATION_REGION")
	workgroup := envOr("ATHENA_INTEGRATION_WORKGROUP", "primary")
	query := envOr("ATHENA_INTEGRATION_QUERY", "SELECT 1")
	output := strings.TrimSpace(os.Getenv("ATHENA_INTEGRATION_OUTPUT"))

	// Overall budget for the whole round-trip. A trivial SELECT 1 settles
	// in seconds; the ceiling guards against a hung/queued execution.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 1. StartQueryExecution — body built by the connector's builder,
	//    including its read-only SQL gate (validateReadOnlySQL). A
	//    non-read query would fail here, in-connector, before any AWS call.
	startArgs := map[string]any{
		"region":      region,
		"QueryString": query,
		"WorkGroup":   workgroup,
	}
	if output != "" {
		startArgs["ResultConfiguration"] = map[string]any{
			"OutputLocation": output,
		}
	}
	startBody, err := buildStartQueryExecution(startArgs)
	if err != nil {
		t.Fatalf("buildStartQueryExecution: %v", err)
	}
	started := awsCLI(ctx, t, region, "athena", "start-query-execution", startBody)
	queryID, _ := started["QueryExecutionId"].(string)
	if queryID == "" {
		t.Fatalf("start-query-execution returned no QueryExecutionId: %v", started)
	}
	t.Logf("started query execution %s in region %s (work group %q)", queryID, region, workgroup)

	// 2. Poll get-query-execution until terminal — body built by the
	//    connector's builder. SUCCEEDED is the only acceptable terminal
	//    state; FAILED/CANCELLED fail the test with Athena's reason.
	getExecBody, err := buildGetQueryExecution(map[string]any{
		"region":           region,
		"QueryExecutionId": queryID,
	})
	if err != nil {
		t.Fatalf("buildGetQueryExecution: %v", err)
	}

	state := pollUntilTerminal(ctx, t, region, getExecBody)
	if state != "SUCCEEDED" {
		t.Fatalf("query %s reached terminal state %q, want SUCCEEDED", queryID, state)
	}

	// 3. GetQueryResults — body built by the connector's builder. A
	//    SUCCEEDED SELECT must return a non-empty ResultSet with rows.
	resultsBody, err := buildGetQueryResults(map[string]any{
		"region":           region,
		"QueryExecutionId": queryID,
	})
	if err != nil {
		t.Fatalf("buildGetQueryResults: %v", err)
	}
	results := awsCLI(ctx, t, region, "athena", "get-query-results", resultsBody)

	rs, ok := results["ResultSet"].(map[string]any)
	if !ok {
		t.Fatalf("get-query-results returned no ResultSet: %v", results)
	}
	rows, ok := rs["Rows"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("get-query-results returned an empty ResultSet.Rows for a SUCCEEDED query: %v", rs)
	}
	t.Logf("round-trip OK: query %s SUCCEEDED, GetQueryResults returned %d row(s)", queryID, len(rows))
}

// pollUntilTerminal polls get-query-execution (using the connector-built
// body) until the execution reaches a terminal Athena state
// (SUCCEEDED/FAILED/CANCELLED) or the context deadline fires, returning the
// terminal state. A FAILED/CANCELLED execution still returns its state so
// the caller can fail with Athena's StateChangeReason.
func pollUntilTerminal(ctx context.Context, t *testing.T, region string, getExecBody []byte) string {
	t.Helper()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		resp := awsCLI(ctx, t, region, "athena", "get-query-execution", getExecBody)
		exec, ok := resp["QueryExecution"].(map[string]any)
		if !ok {
			t.Fatalf("get-query-execution returned no QueryExecution: %v", resp)
		}
		status, ok := exec["Status"].(map[string]any)
		if !ok {
			t.Fatalf("get-query-execution returned no Status: %v", exec)
		}
		state, _ := status["State"].(string)

		switch state {
		case "SUCCEEDED", "FAILED", "CANCELLED":
			if state != "SUCCEEDED" {
				reason, _ := status["StateChangeReason"].(string)
				t.Logf("query reached %s: %s", state, reason)
			}
			return state
		case "QUEUED", "RUNNING", "":
			// not terminal yet; keep polling
		default:
			t.Fatalf("get-query-execution returned unexpected State %q", state)
		}

		select {
		case <-ctx.Done():
			t.Fatalf("timed out polling get-query-execution (last state %q): %v", state, ctx.Err())
		case <-ticker.C:
		}
	}
}
