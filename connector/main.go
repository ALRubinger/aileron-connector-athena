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
//	          "QueryString": "SELECT 1",
//	          "QueryExecutionContext": {"Database": "default"},
//	          "ResultConfiguration": {"OutputLocation": "s3://bucket/prefix/"}}}
//	  → {"output": {"QueryExecutionId": "..."}}
//
//	{"op": "get_query_execution",
//	 "args": {"region": "us-east-1", "QueryExecutionId": "..."}}
//	  → {"output": {"QueryExecution": {"Status": {"State": "SUCCEEDED"}, ...}}}
//
// The connector exposes all 14 read-path Athena ops: start_query_execution,
// get_query_execution, get_query_results, stop_query_execution,
// list_query_executions, batch_get_query_execution, list_databases,
// get_database, list_table_metadata, get_table_metadata, list_work_groups,
// get_work_group, list_data_catalogs, and get_data_catalog. Each routes
// through doSignedAthena with its X-Amz-Target and a region arg.
//
// Every op requires a `region` arg. There is no default region: a missing
// `region` is a connector_runtime_error raised before any host call. The
// region MUST equal the region pinned in manifest.toml's
// [capabilities.network] host and [capabilities.credential].region — the
// connector only validates the arg is present; the host enforces the pin
// fail-closed (a region the allow-list does not list is capability_denied
// at the network boundary, and a region that disagrees with the
// credential produces a SigV4 signature Athena rejects).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		writeError("external_api_error", fmt.Sprintf("Athena API returned %d: %s", status, string(respBody)))
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
		"url":    "https://athena." + region + ".amazonaws.com/",
		"headers": map[string]string{
			"Content-Type": "application/x-amz-json-1.1",
			"X-Amz-Target": "AmazonAthena." + target,
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
