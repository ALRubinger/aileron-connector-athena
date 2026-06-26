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
	"encoding/json"
	"fmt"
	"strings"
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
	if v, ok := numericArg(args, "MaxResults"); ok {
		payload["MaxResults"] = v
	}
	if tok, ok := optionalString(args, "NextToken"); ok {
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
//	ClientRequestToken    (string) — idempotency token; passthrough-only,
//	                                  never derived or synthesized here.
//
// The SQL read-only gate (validateReadOnlySQL) is applied here, after the
// QueryString is confirmed present and before any host/Athena call: a
// non-read QueryString errors out of this builder and never reaches
// doSignedAthena. The body is emitted as AWS-JSON-1.1 (a plain JSON
// object).
func buildStartQueryExecution(args map[string]any) ([]byte, error) {
	queryString, err := requireString(args, "QueryString")
	if err != nil {
		return nil, err
	}
	if err := validateReadOnlySQL(queryString); err != nil {
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
	id, err := requireString(args, "QueryExecutionId")
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
	id, err := requireString(args, "QueryExecutionId")
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
	if wg, ok := optionalString(args, "WorkGroup"); ok {
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
	ids, err := requireStringSlice(args, "QueryExecutionIds")
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
	catalog, err := requireString(args, "CatalogName")
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
	catalog, err := requireString(args, "CatalogName")
	if err != nil {
		return nil, err
	}
	database, err := requireString(args, "DatabaseName")
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
	catalog, err := requireString(args, "CatalogName")
	if err != nil {
		return nil, err
	}
	database, err := requireString(args, "DatabaseName")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"CatalogName":  catalog,
		"DatabaseName": database,
	}
	if expr, ok := optionalString(args, "Expression"); ok {
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
	catalog, err := requireString(args, "CatalogName")
	if err != nil {
		return nil, err
	}
	database, err := requireString(args, "DatabaseName")
	if err != nil {
		return nil, err
	}
	table, err := requireString(args, "TableName")
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
	wg, err := requireString(args, "WorkGroup")
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
	name, err := requireString(args, "Name")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"Name": name}
	if wg, ok := optionalString(args, "WorkGroup"); ok {
		payload["WorkGroup"] = wg
	}
	return json.Marshal(payload)
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
