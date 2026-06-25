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
// path end to end. The remaining actions land in later issues; the SQL
// read-only gate (validateReadOnlySQL) is now applied inside
// buildStartQueryExecution.
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
// Known limitations (accepted, by design — not a parser): a ";" embedded
// inside a string literal or a comment is treated structurally and may
// trip the stacked-statement check; the IAM principal remains the
// backstop in every case.
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

// checkSingleStatement enforces the single-statement rule: it finds the
// first ";" and rejects if anything other than whitespace and trailing
// comments follows it (stacked statements). A QueryString with no ";", or
// one whose only ";" is trailing, passes.
func checkSingleStatement(sql string) error {
	i := strings.IndexByte(sql, ';')
	if i < 0 {
		return nil
	}
	tail := stripLeadingNoise(sql[i+1:])
	if tail != "" {
		return fmt.Errorf("read-only gate: multiple SQL statements are not allowed (only a single read-only statement, with an optional trailing semicolon)")
	}
	return nil
}

// isASCIILetter reports whether b is an ASCII letter (A–Z or a–z).
func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
