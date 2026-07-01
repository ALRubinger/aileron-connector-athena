//go:build wasip1

// Package main is the WASM source for the aileron-connector-athena
// read-path connector. It targets Go's native WASI Preview 1
// (`GOOS=wasip1 GOARCH=wasm`) and calls into Aileron's host-import ABI
// for outbound HTTP and credential mediation.
//
// Athena speaks AWS JSON 1.1: every action is a POST to the regional
// endpoint with a `Content-Type: application/x-amz-json-1.1` header and
// an `X-Amz-Target: AmazonAthena.<Action>` header selecting the
// operation. The request body is the action's JSON payload. The host
// signs the request with SigV4 (Authorization / X-Amz-Date /
// X-Amz-Content-Sha256) when the outbound request marks itself
// `credential: "aws_sigv4"`; the connector never holds the secret access
// key. Static long-lived keys only — no X-Amz-Security-Token (ADR-0019).
//
// Build:
//
//	cd connector && GOOS=wasip1 GOARCH=wasm go build -trimpath \
//	  -ldflags="-s -w" -o ../connector.wasm .
//
// Or via Taskfile from the repo root:
//
//	task build
//
// I/O contract (stdin → stdout JSON):
//
//	{"op": "start_query_execution",
//	 "args": {"region": "us-east-1",
//	          "query_string": "SELECT 1",
//	          "query_execution_context": {"Database": "default"},
//	          "result_configuration": {"OutputLocation": "s3://bucket/prefix/"}}}
//	  → {"output": {"QueryExecutionId": "..."}}
//
//	{"op": "get_query_execution",
//	 "args": {"region": "us-east-1", "query_execution_id": "..."}}
//	  → {"output": {"QueryExecution": {"Status": {"State": "SUCCEEDED"}, ...}}}
//
// The connector exposes all 14 read-path Athena ops: start_query_execution,
// get_query_execution, get_query_results, stop_query_execution,
// list_query_executions, batch_get_query_execution, list_databases,
// get_database, list_table_metadata, get_table_metadata, list_work_groups,
// get_work_group, list_data_catalogs, and get_data_catalog. Each routes
// through doSignedAthena with its X-Amz-Target and a region arg.
//
// It also exposes one synchronous orchestration op, run_query, for
// deterministic (no-LLM) callers such as Aileron Flight Plans whose step
// graph has no loop/poll construct. run_query internalizes the full
// start->poll->page lifecycle in a single call: it starts the query (via
// the same buildStartQueryExecution read-only gate), polls GetQueryExecution
// to a terminal state bounded by an overall deadline, then pages
// GetQueryResults to completion, emitting {QueryExecutionId, ResultSet}. It
// adds no new network host or credential: every internal call still goes
// through doSignedAthena to the same regional Athena host with the same
// aws_sigv4 credential. The three async ops above stay as-is for the
// LLM-in-the-loop flow.
//
// Every op requires a `region` arg. There is no default region: a missing
// `region` is a connector_runtime_error raised before any host call. The
// region selects the AWS endpoint and, via the outbound host, the binding
// the host signs with. This connector is multi-region: any region whose
// athena.<region>.amazonaws.com host is in manifest.toml's
// [capabilities.network] allow-list is valid. The connector only
// validates the arg is present; the host enforces the allow-list
// fail-closed (a region the allow-list does not list is capability_denied
// at the network boundary) and signs with the region-matched binding.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"
)

//go:wasmimport aileron_host log
//go:noescape
func hostLog(levelPtr unsafe.Pointer, levelLen uint32, msgPtr unsafe.Pointer, msgLen uint32)

//go:wasmimport aileron_host http_request
//go:noescape
func hostHTTPRequest(reqPtr unsafe.Pointer, reqLen uint32) int32

//go:wasmimport aileron_host http_response_size
//go:noescape
func hostHTTPResponseSize() int32

//go:wasmimport aileron_host http_response_status
//go:noescape
func hostHTTPResponseStatus() int32

//go:wasmimport aileron_host http_response_read
//go:noescape
func hostHTTPResponseRead(dstPtr unsafe.Pointer, dstLen uint32) int32

// _emptyPtrSentinel keeps the address of an empty byte slice valid; Go
// can't take the address of an empty slice's first element directly.
var _emptyPtrSentinel = [1]byte{}

func ptr(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return unsafe.Pointer(&_emptyPtrSentinel[0])
	}
	return unsafe.Pointer(&b[0])
}

func aileronLog(level, message string) {
	lb := []byte(level)
	mb := []byte(message)
	hostLog(ptr(lb), uint32(len(lb)), ptr(mb), uint32(len(mb)))
}

type input struct {
	Op   string         `json:"op"`
	Args map[string]any `json:"args"`
}

type output struct {
	Output map[string]any `json:"output,omitempty"`
	Error  *outputError   `json:"error,omitempty"`
}

type outputError struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		writeError("connector_runtime_error", "read_stdin: "+err.Error())
		os.Exit(1)
	}
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		writeError("connector_runtime_error", "parse_input: "+err.Error())
		os.Exit(1)
	}

	switch in.Op {
	case "start_query_execution":
		startQueryExecution(in.Args)
	case "get_query_execution":
		getQueryExecution(in.Args)
	case "get_query_results":
		getQueryResults(in.Args)
	case "stop_query_execution":
		stopQueryExecution(in.Args)
	case "list_query_executions":
		listQueryExecutions(in.Args)
	case "batch_get_query_execution":
		batchGetQueryExecution(in.Args)
	case "list_databases":
		listDatabases(in.Args)
	case "get_database":
		getDatabase(in.Args)
	case "list_table_metadata":
		listTableMetadata(in.Args)
	case "get_table_metadata":
		getTableMetadata(in.Args)
	case "list_work_groups":
		listWorkGroups(in.Args)
	case "get_work_group":
		getWorkGroup(in.Args)
	case "list_data_catalogs":
		listDataCatalogs(in.Args)
	case "get_data_catalog":
		getDataCatalog(in.Args)
	case "run_query":
		runQuery(in.Args)
	default:
		writeError("connector_runtime_error", "unknown op: "+in.Op)
		os.Exit(1)
	}
}

// bodyBuilder turns an op's args into an AWS-JSON-1.1 request body (or a
// required-arg error), matching every build* function's signature in
// helpers.go. The dispatch helper takes one so each op routes through a
// single shared signed-call path.
type bodyBuilder func(map[string]any) ([]byte, error)

// dispatch is the shared per-op pipeline: resolve the required region,
// build the body host-testably (returning a required-arg error before any
// host call), issue the signed Athena call via doSignedAthena (which marks
// every request credential: "aws_sigv4" and prefixes "AmazonAthena."),
// map a non-2xx to external_api_error, then parse and emit the response.
// Every error message is prefixed with the op name. `target` is the bare
// Athena action (e.g. "GetQueryResults").
func dispatch(op, target string, args map[string]any, build bodyBuilder) {
	region, err := resolveRegion(args)
	if err != nil {
		writeError("connector_runtime_error", op+": "+err.Error())
		return
	}
	body, err := build(args)
	if err != nil {
		writeError("connector_runtime_error", op+": "+err.Error())
		return
	}
	respBody, status, err := doSignedAthena(region, target, body)
	if err != nil {
		writeError("connector_runtime_error", op+": "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		// Scope the IDEMPOTENT_PARAMETER_MISMATCH rewrite to
		// StartQueryExecution so it cannot mis-fire on the other 13 ops;
		// every other target keeps the verbatim passthrough.
		if target == "StartQueryExecution" {
			writeError("external_api_error", startQueryExecutionErrorMessage(status, respBody))
		} else {
			writeError("external_api_error", fmt.Sprintf("Athena API returned %d: %s", status, string(respBody)))
		}
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		writeError("connector_runtime_error", op+": parse: "+err.Error())
		return
	}
	writeOutput(parsed)
}

// startQueryExecution maps op start_query_execution → Athena's
// StartQueryExecution action. It submits a query for asynchronous
// execution and returns the QueryExecutionId the caller polls with
// get_query_execution. The request body is built host-testably in
// helpers.go.
func startQueryExecution(args map[string]any) {
	dispatch("start_query_execution", "StartQueryExecution", args, buildStartQueryExecution)
}

// getQueryExecution maps op get_query_execution → Athena's
// GetQueryExecution action. It reports the lifecycle state of a query
// previously submitted with start_query_execution. The request body is
// built host-testably in helpers.go.
func getQueryExecution(args map[string]any) {
	dispatch("get_query_execution", "GetQueryExecution", args, buildGetQueryExecution)
}

// getQueryResults maps op get_query_results → Athena's GetQueryResults
// action. It pages the result rows of a SUCCEEDED query.
func getQueryResults(args map[string]any) {
	dispatch("get_query_results", "GetQueryResults", args, buildGetQueryResults)
}

// stopQueryExecution maps op stop_query_execution → Athena's
// StopQueryExecution action. It cancels a running query. (Write/gated
// effect is declared in actions/stop-query-execution/action.md, issue #8;
// no gating logic lives here.)
func stopQueryExecution(args map[string]any) {
	dispatch("stop_query_execution", "StopQueryExecution", args, buildStopQueryExecution)
}

// listQueryExecutions maps op list_query_executions → Athena's
// ListQueryExecutions action. It lists query-execution ids, optionally
// scoped to a work group, with paging.
func listQueryExecutions(args map[string]any) {
	dispatch("list_query_executions", "ListQueryExecutions", args, buildListQueryExecutions)
}

// batchGetQueryExecution maps op batch_get_query_execution → Athena's
// BatchGetQueryExecution action. It fetches details for a batch of
// query-execution ids.
func batchGetQueryExecution(args map[string]any) {
	dispatch("batch_get_query_execution", "BatchGetQueryExecution", args, buildBatchGetQueryExecution)
}

// listDatabases maps op list_databases → Athena's ListDatabases action.
// It lists databases in a data catalog, with paging.
func listDatabases(args map[string]any) {
	dispatch("list_databases", "ListDatabases", args, buildListDatabases)
}

// getDatabase maps op get_database → Athena's GetDatabase action. It
// returns metadata for one database in a data catalog.
func getDatabase(args map[string]any) {
	dispatch("get_database", "GetDatabase", args, buildGetDatabase)
}

// listTableMetadata maps op list_table_metadata → Athena's
// ListTableMetadata action. It lists table metadata in a database,
// optionally filtered by an expression, with paging.
func listTableMetadata(args map[string]any) {
	dispatch("list_table_metadata", "ListTableMetadata", args, buildListTableMetadata)
}

// getTableMetadata maps op get_table_metadata → Athena's GetTableMetadata
// action. It returns metadata for one table.
func getTableMetadata(args map[string]any) {
	dispatch("get_table_metadata", "GetTableMetadata", args, buildGetTableMetadata)
}

// listWorkGroups maps op list_work_groups → Athena's ListWorkGroups
// action. It lists work groups, with paging.
func listWorkGroups(args map[string]any) {
	dispatch("list_work_groups", "ListWorkGroups", args, buildListWorkGroups)
}

// getWorkGroup maps op get_work_group → Athena's GetWorkGroup action. It
// returns the configuration of one work group.
func getWorkGroup(args map[string]any) {
	dispatch("get_work_group", "GetWorkGroup", args, buildGetWorkGroup)
}

// listDataCatalogs maps op list_data_catalogs → Athena's ListDataCatalogs
// action. It lists registered data catalogs, with paging.
func listDataCatalogs(args map[string]any) {
	dispatch("list_data_catalogs", "ListDataCatalogs", args, buildListDataCatalogs)
}

// getDataCatalog maps op get_data_catalog → Athena's GetDataCatalog
// action. It returns the configuration of one data catalog.
func getDataCatalog(args map[string]any) {
	dispatch("get_data_catalog", "GetDataCatalog", args, buildGetDataCatalog)
}

// runQueryPollInterval is the wait between GetQueryExecution polls in
// run_query's wait loop. It mirrors the live integration test's 2s ticker
// (integration_aws_test.go): frequent enough that a fast SELECT returns
// promptly, slow enough not to hammer the Athena API while a query runs.
const runQueryPollInterval = 2 * time.Second

// runQuery maps op run_query → the full synchronous Athena lifecycle in a
// single call, for deterministic (no-LLM) callers whose runtime cannot poll
// between steps. Unlike the other ops it is NOT a single doSignedAthena
// round-trip through `dispatch`; it is a bespoke orchestration:
//
//  1. resolveRegion (required, no default — connector_runtime_error if absent).
//  2. buildStartQueryExecution — the SAME builder the async
//     start_query_execution op uses, so the read-only SQL gate
//     (validateReadOnlySQL) rejects writes/DDL before any host call.
//  3. StartQueryExecution via doSignedAthena; parse the QueryExecutionId.
//  4. Poll GetQueryExecution every runQueryPollInterval, classifying
//     Status.State, until a terminal state or the overall deadline
//     (TimeoutSeconds, default defaultRunQueryTimeoutSeconds). FAILED/CANCELLED
//     → external_api_error carrying the StateChangeReason; deadline →
//     connector_runtime_error.
//  5. On SUCCEEDED, page GetQueryResults following the response NextToken,
//     concatenating ResultSet.Rows (the column header appears only on page 1)
//     and keeping ResultSetMetadata once, then emit {QueryExecutionId,
//     ResultSet}.
//
// Every internal call goes through doSignedAthena to the same regional host
// with the same aws_sigv4 credential: run_query needs no new network host or
// credential. The pure pieces (state classifier, status/result extraction,
// page merge, timeout policy) live in helpers.go and are unit-tested; only
// this sleep+host-call loop is wasip1-gated and host-untestable, consistent
// with the other op funcs.
func runQuery(args map[string]any) {
	region, err := resolveRegion(args)
	if err != nil {
		writeError("connector_runtime_error", "run_query: "+err.Error())
		return
	}

	// Self-heal is a run_query property: on the deterministic-token path (no
	// explicit client_request_token, no idempotency_salt) Athena idempotently
	// replays whatever execution the synthesized token first produced —
	// including a terminal FAILED/CANCELLED one — so a bare re-launch after a
	// since-fixed transient failure would return the frozen failure forever.
	// The discriminator is clock-free: this connector runs in the wasip1
	// sandbox instantiated without WithSysWalltime, so time.Now() is a frozen
	// fake walltime (~2022) and any absolute-timestamp comparison against a real
	// 2026 SubmissionDateTime is a no-op. Instead, key on poll ordinality: a
	// query this call actually started is never already terminal on the FIRST
	// GetQueryExecution poll, so a first-poll terminal FAILED/CANCELLED can only
	// be Athena replaying a frozen prior execution. In that case re-issue ONCE
	// with a fresh token salted from the stale QueryExecutionId (a clock-free
	// value) and poll the new execution. A genuinely fresh failure — which
	// necessarily runs past the first poll — is returned as-is (not
	// double-executed), and the single-shot bound means no infinite loop. The
	// async start_query_execution op cannot observe the outcome within one call
	// and so keeps the plain deterministic token; idempotency_salt remains the
	// explicit override to force a fresh execution of an otherwise-identical
	// SUCCEEDED request.
	selfHeal := usesDeterministicToken(args)
	timeoutSeconds := resolveTimeoutSeconds(args)

	startArgs := args
	retried := false
	for {
		// Build + start. buildStartQueryExecution applies the read-only gate
		// before any host call, so a non-read QueryString fails here.
		startBody, err := buildStartQueryExecution(startArgs)
		if err != nil {
			writeError("connector_runtime_error", "run_query: "+err.Error())
			return
		}
		startResp, status, err := doSignedAthena(region, "StartQueryExecution", startBody)
		if err != nil {
			writeError("connector_runtime_error", "run_query: "+err.Error())
			return
		}
		if status < 200 || status >= 300 {
			// Same StartQueryExecution-scoped IDEMPOTENT_PARAMETER_MISMATCH
			// rewrite as the async start_query_execution path; the run_query
			// prefix is kept so the message names the orchestration op.
			writeError("external_api_error", "run_query: "+startQueryExecutionErrorMessage(status, startResp))
			return
		}
		var started map[string]any
		if err := json.Unmarshal(startResp, &started); err != nil {
			writeError("connector_runtime_error", "run_query: parse StartQueryExecution: "+err.Error())
			return
		}
		queryID, _ := started["QueryExecutionId"].(string)
		if queryID == "" {
			writeError("external_api_error", "run_query: StartQueryExecution returned no QueryExecutionId")
			return
		}

		// Poll to a terminal state, bounded by the overall deadline.
		getExecBody, err := buildGetQueryExecution(map[string]any{"query_execution_id": queryID})
		if err != nil {
			writeError("connector_runtime_error", "run_query: "+err.Error())
			return
		}
		deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
		reissue := false
		firstPoll := true
		for {
			execResp, status, err := doSignedAthena(region, "GetQueryExecution", getExecBody)
			if err != nil {
				writeError("connector_runtime_error", "run_query: "+err.Error())
				return
			}
			if status < 200 || status >= 300 {
				writeError("external_api_error", fmt.Sprintf("run_query: GetQueryExecution returned %d: %s", status, string(execResp)))
				return
			}
			var parsed map[string]any
			if err := json.Unmarshal(execResp, &parsed); err != nil {
				writeError("connector_runtime_error", "run_query: parse GetQueryExecution: "+err.Error())
				return
			}
			state, reason := queryExecutionStatus(parsed)
			switch classifyQueryState(state) {
			case queryStateSucceeded:
				runQueryFetchResults(region, queryID)
				return
			case queryStateFailed, queryStateCancelled:
				// Self-heal only on the deterministic-token path, only once,
				// and only when this FIRST poll already shows a terminal state
				// — the clock-free proof that Athena replayed a frozen failure
				// rather than one this call launched (a query this call started
				// is never already terminal on the first poll). A still-RUNNING
				// replay is not handled here — it stays in the poll loop below,
				// so a legitimate concurrent in-flight dedup is polled to
				// completion rather than re-issued.
				if shouldReissueStaleReplay(selfHeal, retried, firstPoll) {
					reissue = true
					break
				}
				writeError("external_api_error", fmt.Sprintf("run_query: query %s reached terminal state %s: %s", queryID, state, reason))
				return
			case queryStateUnknown:
				writeError("external_api_error", fmt.Sprintf("run_query: query %s returned unexpected state %q", queryID, state))
				return
			}
			if reissue {
				break
			}
			firstPoll = false
			// Still running. Stop if the budget is spent; otherwise wait.
			if time.Now().After(deadline) {
				writeError("connector_runtime_error", fmt.Sprintf("run_query: timed out after %ds waiting for query %s to reach a terminal state (last state %q)", timeoutSeconds, queryID, state))
				return
			}
			time.Sleep(runQueryPollInterval)
		}

		// Stale-replay detected: re-issue once with a fresh token salted from the
		// stale QueryExecutionId (a clock-free value distinct from the wedged
		// token — not a deterministic counter, which would re-wedge, and not the
		// frozen UnixNano wall clock, which cannot vary in-sandbox), then restart
		// the poll loop against the new QueryExecutionId.
		startArgs = withRetryNonce(args, queryID)
		retried = true
	}
}

// runQueryFetchResults pages GetQueryResults for a SUCCEEDED execution and
// writes the run_query output. It follows the response NextToken to the last
// page, merges the pages with mergeResultPages (header row from page 1 only,
// ResultSetMetadata kept once), and emits {QueryExecutionId, ResultSet}. Any
// host/transport failure is a connector_runtime_error; a non-2xx Athena reply
// is an external_api_error — matching the rest of run_query's error mapping.
func runQueryFetchResults(region, queryID string) {
	var pages []map[string]any
	nextToken := ""
	for {
		resultArgs := map[string]any{"query_execution_id": queryID}
		if nextToken != "" {
			resultArgs["next_token"] = nextToken
		}
		body, err := buildGetQueryResults(resultArgs)
		if err != nil {
			writeError("connector_runtime_error", "run_query: "+err.Error())
			return
		}
		resp, status, err := doSignedAthena(region, "GetQueryResults", body)
		if err != nil {
			writeError("connector_runtime_error", "run_query: "+err.Error())
			return
		}
		if status < 200 || status >= 300 {
			writeError("external_api_error", fmt.Sprintf("run_query: GetQueryResults returned %d: %s", status, string(resp)))
			return
		}
		var parsed map[string]any
		if err := json.Unmarshal(resp, &parsed); err != nil {
			writeError("connector_runtime_error", "run_query: parse GetQueryResults: "+err.Error())
			return
		}
		rs, tok := resultPage(parsed)
		pages = append(pages, rs)
		if tok == "" {
			break
		}
		nextToken = tok
	}
	writeOutput(map[string]any{
		"QueryExecutionId": queryID,
		"ResultSet":        mergeResultPages(pages),
	})
}

// doSignedAthena is the single shared signed caller for every Athena
// action. It issues a POST to the regional endpoint
// https://athena.<region>.amazonaws.com/ carrying the AWS JSON 1.1
// content type and the X-Amz-Target action selector, marked
// `credential: "aws_sigv4"` so the host signs it with SigV4. The
// connector never sees the secret access key.
//
// `region` is the per-call region arg (required, no default).  `target`
// is the bare Athena action name (e.g. "StartQueryExecution"); this
// function prefixes the "AmazonAthena." service namespace.  `body` is the
// AWS-JSON-1.1 request payload built by a helpers.go builder.
//
// The `credential: "aws_sigv4"` marker MUST exactly equal manifest.toml's
// [capabilities.credential].kind; a mismatch is capability_denied
// host-side. Returns (respBody, status, err) following the host-import
// read pattern: rc check, size check, bounded read.
func doSignedAthena(region, target string, body []byte) ([]byte, int, error) {
	req, err := json.Marshal(map[string]any{
		"method": "POST",
		"url":    buildAthenaURL(region),
		"headers": map[string]string{
			"Content-Type": "application/x-amz-json-1.1",
			"X-Amz-Target": buildAthenaTarget(target),
		},
		"body":       string(body),
		"credential": "aws_sigv4",
	})
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}
	rc := hostHTTPRequest(ptr(req), uint32(len(req)))
	if rc != 0 {
		// The host has stuck a structured *Error on the per-call state;
		// the runtime surfaces it as an ADR-0010 envelope to the caller.
		// Emitting our own error here would double-wrap the host's — so
		// we just bail with the rc.
		return nil, 0, fmt.Errorf("http_request denied or failed (rc=%d)", rc)
	}
	size := hostHTTPResponseSize()
	if size < 0 {
		return nil, 0, fmt.Errorf("http_response_size returned %d", size)
	}
	respBody := make([]byte, size)
	if size > 0 {
		n := hostHTTPResponseRead(ptr(respBody), uint32(size))
		if n < 0 {
			return nil, 0, fmt.Errorf("http_response_read returned %d", n)
		}
		respBody = respBody[:n]
	}
	return respBody, int(hostHTTPResponseStatus()), nil
}

func writeOutput(out map[string]any) {
	_ = json.NewEncoder(os.Stdout).Encode(output{Output: out})
}

func writeError(class, message string) {
	aileronLog("error", message)
	_ = json.NewEncoder(os.Stdout).Encode(output{Error: &outputError{Class: class, Message: message}})
}
