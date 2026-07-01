// Pure request-building and arg-validation helpers for the Athena
// connector. This file has NO build tag, so it compiles on every Go
// target (including the host platform) and is unit-testable without the
// wasip1 host imports. main.go (which is `//go:build wasip1`) supplies
// the host ABI and dispatch and calls into these builders.
//
// Each build* function turns an op's `args` map into an AWS-JSON-1.1
// request body for the matching Athena action, returning a
// connector-runtime-style error (no host call yet) when a required arg
// is missing. The builders here cover all 14 read-path Athena actions —
// from StartQueryExecution and GetQueryExecution (the start/poll path)
// through the results, catalog, database, table-metadata, work-group and
// data-catalog reads, plus StopQueryExecution's body. The SQL read-only
// gate (validateReadOnlySQL) is applied inside buildStartQueryExecution.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
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
// selects the AWS endpoint and, via the outbound host, the matching
// binding. This connector is multi-region: any region whose
// athena.<region>.amazonaws.com host is in manifest.toml's
// [capabilities.network] allow-list is valid. The connector only checks
// presence; the host enforces the allow-list fail-closed (an unlisted
// region is capability_denied at the network boundary) and signs with
// the region-matched binding.
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

// optionalString returns (value, true) only when args[key] is present and
// a non-empty string; otherwise ("", false). It factors the inline
// `args[key].(string)` + non-empty check used for optional string fields
// so every optional string member is handled consistently across builders.
func optionalString(args map[string]any, key string) (string, bool) {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// requireStringSlice reads a required, non-empty array-of-strings arg
// (e.g. BatchGetQueryExecution's QueryExecutionIds). JSON unmarshalling
// yields []any whose elements are string; this converts and validates each
// element. It errors when the key is absent, not an array, empty, or
// contains a non-string (or empty-string) element.
func requireStringSlice(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key].([]any)
	if !ok {
		return nil, fmt.Errorf("%s is required and must be an array of strings", key)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s must not be empty", key)
	}
	out := make([]string, 0, len(raw))
	for i, el := range raw {
		s, ok := el.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s[%d] must be a non-empty string", key, i)
		}
		out = append(out, s)
	}
	return out, nil
}

// optionalStringSlice reads an optional array-of-strings arg (e.g.
// StartQueryExecution's ExecutionParameters). It mirrors requireStringSlice's
// per-element contract — each present element must be a non-empty string,
// matching Athena's min-length-1 parameter constraint — but the field itself
// is optional: an absent key, a non-array value, or an empty array yields
// (nil, nil) so the caller simply omits the member. A present, non-empty array
// containing a non-string or empty-string element is an error, surfaced before
// any host/Athena call.
func optionalStringSlice(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key].([]any)
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	for i, el := range raw {
		s, ok := el.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s[%d] must be a non-empty string", key, i)
		}
		out = append(out, s)
	}
	return out, nil
}

// applyPaging conditionally sets the standard Athena paging members on a
// request payload from args: MaxResults (a numeric arg) and NextToken (an
// optional non-empty string). It centralizes the paging contract shared by
// the six list/paging ops.
//
// Typing note: JSON-unmarshalled numeric args arrive as float64. This
// helper accepts float64 (and tolerates json.Number and the integer kinds)
// and emits MaxResults as a number so it serializes back to a JSON integer.
// A MaxResults arg that is absent or of an unusable type is simply omitted.
func applyPaging(payload map[string]any, args map[string]any) {
	if v, ok := numericArg(args, "max_results"); ok {
		payload["MaxResults"] = v
	}
	if tok, ok := optionalString(args, "next_token"); ok {
		payload["NextToken"] = tok
	}
}

// numericArg extracts a numeric arg as a float64 when present and of a
// numeric type. JSON unmarshalling normally yields float64; json.Number and
// the native integer kinds are tolerated for robustness. Returns (0, false)
// when the key is absent or not numeric.
func numericArg(args map[string]any, key string) (float64, bool) {
	switch n := args[key].(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// buildAthenaURL assembles the regional Athena endpoint URL for a per-call
// region arg: https://athena.<region>.amazonaws.com/. It is factored out of
// doSignedAthena (which is wasip1-gated and so host-untestable) so the URL
// shape gets host unit-test coverage. It does no validation: the region is
// already confirmed present by resolveRegion, and the host enforces the
// network allow-list fail-closed for any region not in the manifest.
func buildAthenaURL(region string) string {
	return "https://athena." + region + ".amazonaws.com/"
}

// buildAthenaTarget assembles the X-Amz-Target header value for an Athena
// action by prefixing the AWS JSON 1.1 service namespace: given the bare
// action "StartQueryExecution" it returns "AmazonAthena.StartQueryExecution".
// Factored out of doSignedAthena for the same host-coverage reason as
// buildAthenaURL.
func buildAthenaTarget(action string) string {
	return "AmazonAthena." + action
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
//	ExecutionParameters   ([]string) — values bound to the query's "?"
//	                                  placeholders, in order, for a
//	                                  parameterized (prepared) statement.
//	                                  Included only when present and
//	                                  non-empty; each member must be a
//	                                  non-empty string (Athena's min-length-1
//	                                  parameter constraint). The read-only
//	                                  gate is unaffected — these are bound
//	                                  values, not SQL text.
//	ClientRequestToken    (string) — idempotency token. A caller-supplied
//	                                  non-empty token is honored verbatim.
//	                                  When absent or empty, a deterministic
//	                                  token is synthesized here as the
//	                                  hex-encoded SHA-256 of the canonical
//	                                  request (query string plus the
//	                                  execution context, result
//	                                  configuration, work group and execution
//	                                  parameters), so the same request always
//	                                  yields the same token — and a request
//	                                  differing only in its bound parameters
//	                                  yields a distinct token. This is
//	                                  required because this
//	                                  connector hand-builds the AWS-JSON-1.1
//	                                  body with no AWS SDK and Athena rejects
//	                                  a null/empty token with 400
//	                                  INVALID_INPUT. The 64-char hex token is
//	                                  within Athena's 32-128 length bound and
//	                                  valid charset.
//	IdempotencySalt       (string) — an opaque caller-supplied salt that,
//	                                  when no explicit ClientRequestToken is
//	                                  given, is folded into the deterministic
//	                                  token's canonical hash so the same
//	                                  logical request with a different salt
//	                                  yields a fresh, still-deterministic
//	                                  token. The salt is NOT emitted as a
//	                                  StartQueryExecution body member (it is
//	                                  not a valid Athena field) — it only
//	                                  influences the hash input. When absent
//	                                  the body is byte-identical to today's.
//	                                  An explicit ClientRequestToken is still
//	                                  honored verbatim and the salt is ignored
//	                                  for derivation.
//
// The SQL read-only gate (validateReadOnlySQL) is applied here, after the
// QueryString is confirmed present and before any host/Athena call: a
// non-read QueryString errors out of this builder and never reaches
// doSignedAthena. The body is emitted as AWS-JSON-1.1 (a plain JSON
// object).
func buildStartQueryExecution(args map[string]any) ([]byte, error) {
	queryString, err := requireString(args, "query_string")
	if err != nil {
		return nil, err
	}
	if err := validateReadOnlySQL(queryString); err != nil {
		return nil, err
	}

	payload := map[string]any{
		"QueryString": queryString,
	}
	if ctx := optionalMap(args, "query_execution_context"); ctx != nil {
		payload["QueryExecutionContext"] = ctx
	}
	if rc := optionalMap(args, "result_configuration"); rc != nil {
		payload["ResultConfiguration"] = rc
	}
	if wg, ok := args["work_group"].(string); ok && wg != "" {
		payload["WorkGroup"] = wg
	}
	// ExecutionParameters is set before the ClientRequestToken branch so it
	// folds into deriveClientRequestToken's canonical hash: two requests with
	// the same SQL but different bound parameter values get distinct
	// synthesized idempotency tokens and so are not collapsed onto one
	// execution.
	params, err := optionalStringSlice(args, "execution_parameters")
	if err != nil {
		return nil, err
	}
	if params != nil {
		payload["ExecutionParameters"] = params
	}
	if token, ok := args["client_request_token"].(string); ok && token != "" {
		// Caller-supplied token is honored verbatim. The idempotency_salt
		// is ignored in this branch — an explicit token fully controls
		// idempotency, so the salt would be meaningless.
		payload["ClientRequestToken"] = token
	} else {
		// Athena rejects a null/empty ClientRequestToken with 400
		// INVALID_INPUT, and this connector has no AWS SDK to
		// auto-generate one. Synthesize a deterministic token that is a
		// pure function of the canonical request so the same request
		// always idempotently maps to the same token. An optional
		// idempotency_salt is folded into the hash input (never emitted as
		// a body member) so the same logical request with a different salt
		// gets a fresh deterministic token. Absent salt → today's bytes.
		salt, _ := optionalString(args, "idempotency_salt")
		payload["ClientRequestToken"] = deriveClientRequestToken(payload, salt)
	}

	return json.Marshal(payload)
}

// deriveClientRequestToken returns a deterministic idempotency token for a
// StartQueryExecution request: the hex-encoded SHA-256 of the canonical JSON
// of the request fields that define it (QueryString plus any
// QueryExecutionContext, ResultConfiguration, WorkGroup and
// ExecutionParameters). Hashing the full canonical request — not just the SQL
// — makes the token a true function of the request, so the same SQL targeting a
// different output location, or bound to different parameter values, does not
// collide on a single token. The result is 64 hex characters, within Athena's
// 32-128 length bound and valid charset, and uses no clock or randomness so it
// is reproducible on the wasip1 target.
//
// An optional salt (the caller's idempotency_salt) is folded into the hash
// input — never emitted as a body member — so the same logical request with a
// different salt yields a fresh deterministic token, letting a caller force a
// distinct execution for an otherwise-identical request. When salt is empty the
// hash input is the canonical payload alone, so the token is byte-identical to
// the no-salt behavior: existing callers are unaffected.
func deriveClientRequestToken(payload map[string]any, salt string) string {
	// json.Marshal of a map emits keys in sorted order, giving a stable
	// canonical encoding without a separate normalization pass.
	canonical, err := json.Marshal(payload)
	if err != nil {
		// payload holds only JSON-marshalable values assembled above, so
		// this is unreachable; fall back to the query string alone.
		canonical = []byte(fmt.Sprintf("%v", payload["QueryString"]))
	}
	h := sha256.New()
	h.Write(canonical)
	if salt != "" {
		// Domain-separate the salt from the canonical request with a NUL
		// byte so no salt value can reproduce a no-salt or different-salt
		// hash input by colliding on the byte boundary.
		h.Write([]byte{0})
		h.Write([]byte(salt))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// startQueryExecutionErrorMessage maps a non-2xx StartQueryExecution reply to
// the external_api_error message text. It is scoped to StartQueryExecution: the
// caller invokes it only on that target's non-2xx path, so the
// IDEMPOTENT_PARAMETER_MISMATCH detection cannot mis-fire on any of the other
// 13 Athena ops.
//
// When the body carries Athena's IDEMPOTENT_PARAMETER_MISMATCH error code (a
// 400 returned when a ClientRequestToken is reused with different request
// parameters), the raw passthrough is replaced with a connector message that
// names the code, explains it is a deterministic-token collision with a prior
// execution that used the same logical request but different parameters (for
// example a work-group result location that changed since the first run), and
// points the caller at the idempotency_salt input (or an explicit
// client_request_token) to force a fresh token. The classification stays
// external_api_error — this is still Athena rejecting the request, just with a
// connector-authored explanation instead of the opaque AWS body. Every other
// non-2xx falls through to the verbatim "Athena API returned <status>: <body>"
// passthrough.
func startQueryExecutionErrorMessage(status int, respBody []byte) string {
	body := string(respBody)
	if strings.Contains(body, "IDEMPOTENT_PARAMETER_MISMATCH") {
		return fmt.Sprintf(
			"Athena API returned %d IDEMPOTENT_PARAMETER_MISMATCH: the connector's deterministic idempotency token collided with a prior StartQueryExecution that used the same logical request but different parameters (for example a changed work-group result location). To force a fresh execution, pass a distinct idempotency_salt input (folded into the derived token) or supply your own client_request_token. Raw Athena response: %s",
			status, body,
		)
	}
	return fmt.Sprintf("Athena API returned %d: %s", status, body)
}

// buildGetQueryExecution constructs the AWS-JSON-1.1 body for Athena's
// GetQueryExecution action.
//
// Required:
//
//	QueryExecutionId (string) — the id returned by StartQueryExecution.
func buildGetQueryExecution(args map[string]any) ([]byte, error) {
	id, err := requireString(args, "query_execution_id")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"QueryExecutionId": id,
	})
}

// buildGetQueryResults constructs the AWS-JSON-1.1 body for Athena's
// GetQueryResults action.
//
// Required:
//
//	QueryExecutionId (string).
//
// Optional:
//
//	MaxResults (number), NextToken (string) — standard paging.
func buildGetQueryResults(args map[string]any) ([]byte, error) {
	id, err := requireString(args, "query_execution_id")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"QueryExecutionId": id}
	applyPaging(payload, args)
	return json.Marshal(payload)
}

// buildStopQueryExecution constructs the AWS-JSON-1.1 body for Athena's
// StopQueryExecution action.
//
// Required:
//
//	QueryExecutionId (string).
//
// Effect: StopQueryExecution is a write/gated operation. Its approval
// gating (citing ADR-0009) and idempotency flag (per ADR-0010) are
// declared in actions/stop-query-execution/action.md (issue #8), NOT here.
// This builder produces only the request body; it adds no gating logic.
func buildStopQueryExecution(args map[string]any) ([]byte, error) {
	id, err := requireString(args, "query_execution_id")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"QueryExecutionId": id,
	})
}

// buildListQueryExecutions constructs the AWS-JSON-1.1 body for Athena's
// ListQueryExecutions action.
//
// Required: none.
//
// Optional:
//
//	WorkGroup (string), MaxResults (number), NextToken (string).
//
// With no args it emits a valid empty JSON object ({}).
func buildListQueryExecutions(args map[string]any) ([]byte, error) {
	payload := map[string]any{}
	if wg, ok := optionalString(args, "work_group"); ok {
		payload["WorkGroup"] = wg
	}
	applyPaging(payload, args)
	return json.Marshal(payload)
}

// buildBatchGetQueryExecution constructs the AWS-JSON-1.1 body for Athena's
// BatchGetQueryExecution action.
//
// Required:
//
//	QueryExecutionIds ([]string) — non-empty array of execution ids.
func buildBatchGetQueryExecution(args map[string]any) ([]byte, error) {
	ids, err := requireStringSlice(args, "query_execution_ids")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"QueryExecutionIds": ids,
	})
}

// buildListDatabases constructs the AWS-JSON-1.1 body for Athena's
// ListDatabases action.
//
// Required:
//
//	CatalogName (string).
//
// Optional:
//
//	MaxResults (number), NextToken (string).
func buildListDatabases(args map[string]any) ([]byte, error) {
	catalog, err := requireString(args, "catalog_name")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"CatalogName": catalog}
	applyPaging(payload, args)
	return json.Marshal(payload)
}

// buildGetDatabase constructs the AWS-JSON-1.1 body for Athena's
// GetDatabase action.
//
// Required:
//
//	CatalogName (string), DatabaseName (string).
func buildGetDatabase(args map[string]any) ([]byte, error) {
	catalog, err := requireString(args, "catalog_name")
	if err != nil {
		return nil, err
	}
	database, err := requireString(args, "database_name")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"CatalogName":  catalog,
		"DatabaseName": database,
	})
}

// buildListTableMetadata constructs the AWS-JSON-1.1 body for Athena's
// ListTableMetadata action.
//
// Required:
//
//	CatalogName (string), DatabaseName (string).
//
// Optional:
//
//	Expression (string), MaxResults (number), NextToken (string).
func buildListTableMetadata(args map[string]any) ([]byte, error) {
	catalog, err := requireString(args, "catalog_name")
	if err != nil {
		return nil, err
	}
	database, err := requireString(args, "database_name")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"CatalogName":  catalog,
		"DatabaseName": database,
	}
	if expr, ok := optionalString(args, "expression"); ok {
		payload["Expression"] = expr
	}
	applyPaging(payload, args)
	return json.Marshal(payload)
}

// buildGetTableMetadata constructs the AWS-JSON-1.1 body for Athena's
// GetTableMetadata action.
//
// Required:
//
//	CatalogName (string), DatabaseName (string), TableName (string).
func buildGetTableMetadata(args map[string]any) ([]byte, error) {
	catalog, err := requireString(args, "catalog_name")
	if err != nil {
		return nil, err
	}
	database, err := requireString(args, "database_name")
	if err != nil {
		return nil, err
	}
	table, err := requireString(args, "table_name")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"CatalogName":  catalog,
		"DatabaseName": database,
		"TableName":    table,
	})
}

// buildListWorkGroups constructs the AWS-JSON-1.1 body for Athena's
// ListWorkGroups action.
//
// Required: none.
//
// Optional:
//
//	MaxResults (number), NextToken (string).
func buildListWorkGroups(args map[string]any) ([]byte, error) {
	payload := map[string]any{}
	applyPaging(payload, args)
	return json.Marshal(payload)
}

// buildGetWorkGroup constructs the AWS-JSON-1.1 body for Athena's
// GetWorkGroup action.
//
// Required:
//
//	WorkGroup (string).
func buildGetWorkGroup(args map[string]any) ([]byte, error) {
	wg, err := requireString(args, "work_group")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"WorkGroup": wg,
	})
}

// buildListDataCatalogs constructs the AWS-JSON-1.1 body for Athena's
// ListDataCatalogs action.
//
// Required: none.
//
// Optional:
//
//	MaxResults (number), NextToken (string).
func buildListDataCatalogs(args map[string]any) ([]byte, error) {
	payload := map[string]any{}
	applyPaging(payload, args)
	return json.Marshal(payload)
}

// buildGetDataCatalog constructs the AWS-JSON-1.1 body for Athena's
// GetDataCatalog action.
//
// Required:
//
//	Name (string).
//
// Optional:
//
//	WorkGroup (string).
func buildGetDataCatalog(args map[string]any) ([]byte, error) {
	name, err := requireString(args, "name")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"Name": name}
	if wg, ok := optionalString(args, "work_group"); ok {
		payload["WorkGroup"] = wg
	}
	return json.Marshal(payload)
}

// defaultRunQueryTimeoutSeconds is the overall poll budget run_query uses
// when the caller supplies no TimeoutSeconds. It mirrors the live
// integration test's 3-minute ceiling (integration_aws_test.go): a trivial
// SELECT settles in seconds, and this guards against a hung or long-queued
// execution rather than waiting forever.
const defaultRunQueryTimeoutSeconds = 180

// resolveTimeoutSeconds reads the optional TimeoutSeconds arg for run_query.
// A present, numeric, positive value is used as-is; an absent, non-numeric,
// or non-positive value falls back to defaultRunQueryTimeoutSeconds. Factored
// out of the wasip1-gated poll loop so the default/override policy is host
// unit-testable.
func resolveTimeoutSeconds(args map[string]any) int {
	if v, ok := numericArg(args, "timeout_seconds"); ok && v > 0 {
		return int(v)
	}
	return defaultRunQueryTimeoutSeconds
}

// queryStateClass classifies an Athena Status.State string for the run_query
// poll loop.
type queryStateClass int

const (
	// queryStateRunning covers the non-terminal states QUEUED, RUNNING, and
	// the empty string (a status not yet populated): keep polling.
	queryStateRunning queryStateClass = iota
	// queryStateSucceeded is the terminal success state.
	queryStateSucceeded
	// queryStateFailed is the terminal FAILED state.
	queryStateFailed
	// queryStateCancelled is the terminal CANCELLED state.
	queryStateCancelled
	// queryStateUnknown is any unrecognized state value: the loop fails fast
	// rather than polling forever on a state it cannot reason about.
	queryStateUnknown
)

// classifyQueryState maps an Athena Status.State string to a queryStateClass.
// SUCCEEDED/FAILED/CANCELLED are terminal; QUEUED, RUNNING, and "" are
// non-terminal (keep polling); anything else is queryStateUnknown. This
// mirrors the terminal-state switch in integration_aws_test.go's
// pollUntilTerminal and is factored here so the classification is host
// unit-testable, leaving only the sleep+host-call loop in the wasip1-gated
// main.go.
func classifyQueryState(state string) queryStateClass {
	switch state {
	case "SUCCEEDED":
		return queryStateSucceeded
	case "FAILED":
		return queryStateFailed
	case "CANCELLED":
		return queryStateCancelled
	case "QUEUED", "RUNNING", "":
		return queryStateRunning
	default:
		return queryStateUnknown
	}
}

// queryExecutionStatus extracts (State, StateChangeReason) from a parsed
// GetQueryExecution response, reading QueryExecution.Status.State and
// QueryExecution.Status.StateChangeReason. Missing members yield empty
// strings (an empty State classifies as still-running). Pure and host
// unit-testable.
func queryExecutionStatus(resp map[string]any) (state, reason string) {
	exec, _ := resp["QueryExecution"].(map[string]any)
	status, _ := exec["Status"].(map[string]any)
	state, _ = status["State"].(string)
	reason, _ = status["StateChangeReason"].(string)
	return state, reason
}

// queryExecutionSubmissionEpoch extracts QueryExecution.Status.SubmissionDateTime
// from a parsed GetQueryExecution response. Athena returns this field over
// AWS-JSON as an epoch number (seconds since the Unix epoch, with a fractional
// millisecond component), so after json.Unmarshal into a map it is a float64.
// The bool reports whether a numeric value was present: a missing or
// non-numeric field yields (0, false), which the run_query self-heal treats as
// "cannot prove a stale replay" and therefore does not re-issue. Pure and host
// unit-testable.
func queryExecutionSubmissionEpoch(resp map[string]any) (epochSeconds float64, ok bool) {
	exec, _ := resp["QueryExecution"].(map[string]any)
	status, _ := exec["Status"].(map[string]any)
	epochSeconds, ok = status["SubmissionDateTime"].(float64)
	return epochSeconds, ok
}

// runQueryReplaySkew is the clock-skew margin the stale-replay discriminator
// allows between the connector host's wall clock and the SubmissionDateTime
// Athena stamps on an execution. A terminal-failed execution is treated as a
// stale replay (safe to re-issue) only when its submission predates the current
// call's StartQueryExecution by more than this margin; a genuinely fresh
// failure submitted at (or just before) callStart stays within the margin and
// is returned as-is, so genuine failures are never double-executed. The margin
// errs toward NOT self-healing, which is the conservative default.
const runQueryReplaySkew = 5 * time.Second

// isStaleReplay reports whether a terminal-failed execution's SubmissionDateTime
// proves Athena replayed a pre-existing failure rather than running a query this
// call started. It is true only when the submission time (epoch seconds) is
// earlier than callStart minus the skew margin. A query this call actually
// started is submitted at (or shortly after) callStart, so it never satisfies
// this predicate; an execution frozen by a prior launch was submitted well
// before callStart and does. Pure and host unit-testable — the run_query
// re-issue loop that acts on it stays wasip1-gated in main.go.
func isStaleReplay(submissionEpochSeconds float64, callStart time.Time, skew time.Duration) bool {
	submitted := epochSecondsToTime(submissionEpochSeconds)
	return submitted.Before(callStart.Add(-skew))
}

// epochSecondsToTime converts an epoch-seconds value carrying a fractional
// sub-second component (as Athena emits SubmissionDateTime over AWS-JSON) into a
// time.Time, preserving the fractional part as nanoseconds. Pure and host
// unit-testable.
func epochSecondsToTime(epochSeconds float64) time.Time {
	sec := int64(epochSeconds)
	nsec := int64((epochSeconds - float64(sec)) * 1e9)
	return time.Unix(sec, nsec)
}

// usesDeterministicToken reports whether a run_query call takes the
// deterministic-token path, i.e. the caller supplied neither a non-empty
// client_request_token nor a non-empty idempotency_salt. Only on that path can a
// terminal-failed execution be a stale replay of the connector's own
// synthesized token, so this gates run_query's self-heal: an explicit token or
// salt means the caller is driving idempotency deliberately and the connector
// must not second-guess it. Pure and host unit-testable.
func usesDeterministicToken(args map[string]any) bool {
	if token, ok := args["client_request_token"].(string); ok && token != "" {
		return false
	}
	if salt, ok := optionalString(args, "idempotency_salt"); ok && salt != "" {
		return false
	}
	return true
}

// retryNonceSalt maps a time-based nonce (time.Now().UnixNano()) to a
// namespaced idempotency_salt string used only for run_query's internal
// self-heal re-issue. Feeding it through the existing salt-fold path
// (deriveClientRequestToken) yields a fresh deterministic token per nonce — the
// salt is never emitted as a StartQueryExecution body member. Because each
// launch's nonce is distinct, the retry token is distinct across launches too,
// so a re-issue cannot re-collide on another cached-failure token the way a
// deterministic retry counter would. The prefix domain-separates these internal
// salts from any value an operator might pass. Pure and host unit-testable.
func retryNonceSalt(nonce int64) string {
	return "run_query.self-heal.retry-nonce:" + strconv.FormatInt(nonce, 10)
}

// withRetryNonce returns a shallow copy of a run_query args map with
// idempotency_salt set to retryNonceSalt(nonce), leaving the caller's original
// map untouched. Rebuilding the StartQueryExecution body from the copy folds the
// nonce into the derived idempotency token, so the self-heal re-issue targets a
// fresh execution instead of Athena's frozen terminal-failed one. Pure and host
// unit-testable; the host re-issue is the wasip1-gated caller.
func withRetryNonce(args map[string]any, nonce int64) map[string]any {
	next := make(map[string]any, len(args)+1)
	for k, v := range args {
		next[k] = v
	}
	next["idempotency_salt"] = retryNonceSalt(nonce)
	return next
}

// resultPage extracts the ResultSet object and the top-level NextToken from a
// parsed GetQueryResults response. In Athena's GetQueryResults response the
// NextToken sits at the top level (a sibling of ResultSet, not inside it), so
// the run_query pager reads it from there. A missing ResultSet yields nil; a
// missing/empty NextToken yields "" (the last page). Pure and host
// unit-testable.
func resultPage(resp map[string]any) (resultSet map[string]any, nextToken string) {
	resultSet, _ = resp["ResultSet"].(map[string]any)
	nextToken, _ = resp["NextToken"].(string)
	return resultSet, nextToken
}

// mergeResultPages concatenates a sequence of GetQueryResults ResultSet pages
// (in fetch order) into a single ResultSet for the run_query response. Athena
// emits the column header as the first Row of the FIRST page only and does
// not repeat it on later pages, so an in-order concatenation of every page's
// Rows yields [header, data...] exactly once. The ResultSetMetadata is taken
// from the first page that carries one and kept once (later pages repeat or
// omit it). nil page entries are skipped. The merged ResultSet always carries
// a Rows array (possibly empty). Pure and host unit-testable; only the
// host-call paging loop lives in the wasip1-gated main.go.
func mergeResultPages(pages []map[string]any) map[string]any {
	merged := map[string]any{}
	rows := []any{}
	for _, rs := range pages {
		if rs == nil {
			continue
		}
		if _, have := merged["ResultSetMetadata"]; !have {
			if md, ok := rs["ResultSetMetadata"]; ok {
				merged["ResultSetMetadata"] = md
			}
		}
		if pr, ok := rs["Rows"].([]any); ok {
			rows = append(rows, pr...)
		}
	}
	merged["Rows"] = rows
	return merged
}

// readOnlyLeadingKeywords is the allow-set of first keywords a read-only
// Athena statement may begin with. Membership is the accept test;
// everything else (INSERT/UPDATE/DELETE/MERGE/CREATE/ALTER/DROP/MSCK/
// CALL/UNLOAD/GRANT/REVOKE/TRUNCATE/REPLACE/…) is rejected by default, so
// this set — not an exhaustive reject list — is the source of truth.
var readOnlyLeadingKeywords = map[string]bool{
	"SELECT":   true,
	"WITH":     true,
	"SHOW":     true,
	"DESCRIBE": true,
	"DESC":     true,
	"EXPLAIN":  true,
	"VALUES":   true,
}

// validateReadOnlySQL is a conservative, defense-in-depth read-only gate
// over an Athena QueryString. It is NOT the primary guarantee: the primary
// guarantee is the read-only IAM principal the host signs requests as —
// the credential simply cannot perform writes/DDL regardless of what SQL
// is submitted. This gate is a second, in-connector layer that rejects
// obvious non-read statements early (before any host/Athena call) so a
// misuse fails fast with a clear connector error rather than a remote IAM
// denial.
//
// It is deliberately a scanner, not a full SQL parser. It strips leading
// whitespace and SQL comments (-- line and /* */ block), tolerates a
// single leading "(", then requires the first keyword token to be in
// readOnlyLeadingKeywords. EXPLAIN ANALYZE is rejected (it executes the
// statement); plain EXPLAIN is allowed. Stacked statements (a semicolon
// followed by more non-trivial SQL) are rejected; a single trailing
// semicolon (optionally followed by whitespace/comments) is allowed.
//
// The stacked-statement check is comment- and string-literal-aware: a ";"
// inside a "--" line comment, a "/* */" block comment, or a single-quoted
// string literal (with doubled-quote escaping) is not treated as a statement
// terminator, so it does not false-reject an otherwise valid read query.
// The IAM principal remains the backstop in every case.
func validateReadOnlySQL(sql string) error {
	// Stacked-statement gate first: at most one ";", and anything after
	// it must be only whitespace/comments (a bare trailing semicolon).
	if err := checkSingleStatement(sql); err != nil {
		return err
	}

	// Strip leading whitespace + comments, then tolerate a single
	// leading "(" (and strip again), to reach the first keyword.
	rest := stripLeadingNoise(sql)
	if strings.HasPrefix(rest, "(") {
		rest = stripLeadingNoise(rest[1:])
	}

	kw, after := leadingKeyword(rest)
	if kw == "" {
		return fmt.Errorf("read-only gate: no SQL statement found (empty, whitespace-only, or comment-only QueryString)")
	}
	upper := strings.ToUpper(kw)
	if !readOnlyLeadingKeywords[upper] {
		return fmt.Errorf("read-only gate: statement starting with %q is not an allowed read-only operation", kw)
	}

	// EXPLAIN is allowed, but EXPLAIN ANALYZE executes the statement.
	if upper == "EXPLAIN" {
		next, _ := leadingKeyword(stripLeadingNoise(after))
		if strings.EqualFold(next, "ANALYZE") {
			return fmt.Errorf("read-only gate: EXPLAIN ANALYZE executes the statement and is not allowed")
		}
	}

	return nil
}

// stripLeadingNoise removes leading whitespace and any run of leading SQL
// comments (-- to end of line, /* */ blocks), looping until the input
// begins with neither. It does not look inside string literals (this is a
// scanner, not a parser).
func stripLeadingNoise(s string) string {
	for {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			} else {
				return "" // line comment runs to EOF
			}
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s[2:], "*/"); i >= 0 {
				s = s[2+i+2:]
			} else {
				return "" // unterminated block comment runs to EOF
			}
		default:
			return s
		}
	}
}

// leadingKeyword returns the leading run of ASCII letters at the start of
// s (the first keyword token) and the remainder after it. It returns
// ("", s) when s does not begin with an ASCII letter.
func leadingKeyword(s string) (kw, rest string) {
	i := 0
	for i < len(s) && isASCIILetter(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// checkSingleStatement enforces the single-statement rule. It performs a
// comment- and string-literal-aware scan for the first statement-terminating
// ";": it skips over "--" line comments (to newline/EOF), "/* */" block
// comments, and single-quoted string literals (handling the doubled-quote escape)
// so that a ";" appearing inside a comment or string literal is never
// mistaken for a statement terminator. Only a real, structural ";" counts;
// the statement is rejected when such a ";" is followed by non-whitespace,
// non-comment text (a stacked statement). A QueryString with no structural
// ";", or one whose only structural ";" is trailing, passes.
func checkSingleStatement(sql string) error {
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			// Line comment: skip to end of line (or EOF).
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				return nil // comment (and any ";" in it) runs to EOF
			}
		case strings.HasPrefix(sql[i:], "/*"):
			// Block comment: skip to closing "*/" (or EOF).
			if j := strings.Index(sql[i+2:], "*/"); j >= 0 {
				i += 2 + j + 2
			} else {
				return nil // unterminated block comment runs to EOF
			}
		case sql[i] == '\'':
			// Single-quoted string literal: skip to the closing quote,
			// treating a doubled '' as an escaped quote (not a close).
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						i += 2 // escaped quote, stay in the literal
						continue
					}
					i++ // closing quote
					break
				}
				i++
			}
		case sql[i] == ';':
			// A real, structural statement terminator. Anything after it
			// other than whitespace and trailing comments is a stacked
			// statement.
			tail := stripLeadingNoise(sql[i+1:])
			if tail != "" {
				return fmt.Errorf("read-only gate: multiple SQL statements are not allowed (only a single read-only statement, with an optional trailing semicolon)")
			}
			return nil
		default:
			i++
		}
	}
	return nil
}

// isASCIILetter reports whether b is an ASCII letter (A–Z or a–z).
func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
