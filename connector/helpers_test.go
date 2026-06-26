package main

import (
	"encoding/json"
	"testing"
)

// unmarshalBody round-trips a builder's []byte output back into a map so
// tests can assert on field presence and values, and incidentally proves
// the body is valid JSON (the AWS-JSON-1.1 wire form).
func unmarshalBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v (raw=%s)", err, string(b))
	}
	return m
}

func TestRequireString(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		key     string
		want    string
		wantErr bool
	}{
		{"present", map[string]any{"k": "v"}, "k", "v", false},
		{"missing", map[string]any{}, "k", "", true},
		{"empty", map[string]any{"k": ""}, "k", "", true},
		{"wrong type", map[string]any{"k": 42}, "k", "", true},
		{"nil value", map[string]any{"k": nil}, "k", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requireString(tc.args, tc.key)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveRegion(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		got, err := resolveRegion(map[string]any{"region": "us-east-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "us-east-1" {
			t.Fatalf("got %q, want us-east-1", got)
		}
	})
	t.Run("missing is required-arg error (no default)", func(t *testing.T) {
		_, err := resolveRegion(map[string]any{})
		if err == nil {
			t.Fatal("expected error for missing region, got nil")
		}
	})
	t.Run("empty is required-arg error", func(t *testing.T) {
		_, err := resolveRegion(map[string]any{"region": ""})
		if err == nil {
			t.Fatal("expected error for empty region, got nil")
		}
	})
}

func TestOptionalMap(t *testing.T) {
	t.Run("present object", func(t *testing.T) {
		got := optionalMap(map[string]any{"k": map[string]any{"a": "b"}}, "k")
		if got == nil || got["a"] != "b" {
			t.Fatalf("got %v, want {a:b}", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if got := optionalMap(map[string]any{}, "k"); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
	t.Run("wrong type", func(t *testing.T) {
		if got := optionalMap(map[string]any{"k": "notamap"}, "k"); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
}

func TestBuildStartQueryExecution_RequiresQueryString(t *testing.T) {
	if _, err := buildStartQueryExecution(map[string]any{}); err == nil {
		t.Fatal("expected error when QueryString missing, got nil")
	}
	if _, err := buildStartQueryExecution(map[string]any{"QueryString": ""}); err == nil {
		t.Fatal("expected error when QueryString empty, got nil")
	}
}

func TestBuildStartQueryExecution_Minimal(t *testing.T) {
	b, err := buildStartQueryExecution(map[string]any{"QueryString": "SELECT 1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := unmarshalBody(t, b)
	if m["QueryString"] != "SELECT 1" {
		t.Fatalf("QueryString = %v, want SELECT 1", m["QueryString"])
	}
	// Optional members must be absent when not supplied.
	for _, k := range []string{"QueryExecutionContext", "ResultConfiguration", "WorkGroup", "ClientRequestToken"} {
		if _, present := m[k]; present {
			t.Fatalf("optional field %q should be absent when not supplied", k)
		}
	}
}

func TestBuildStartQueryExecution_OptionalsIncludedWhenPresent(t *testing.T) {
	args := map[string]any{
		"QueryString":           "SELECT * FROM t",
		"QueryExecutionContext": map[string]any{"Database": "default", "Catalog": "awsdatacatalog"},
		"ResultConfiguration":   map[string]any{"OutputLocation": "s3://bucket/prefix/"},
		"WorkGroup":             "primary",
		"ClientRequestToken":    "caller-supplied-token-123",
	}
	b, err := buildStartQueryExecution(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := unmarshalBody(t, b)

	ctx, ok := m["QueryExecutionContext"].(map[string]any)
	if !ok || ctx["Database"] != "default" || ctx["Catalog"] != "awsdatacatalog" {
		t.Fatalf("QueryExecutionContext not passed through: %v", m["QueryExecutionContext"])
	}
	rc, ok := m["ResultConfiguration"].(map[string]any)
	if !ok || rc["OutputLocation"] != "s3://bucket/prefix/" {
		t.Fatalf("ResultConfiguration not passed through: %v", m["ResultConfiguration"])
	}
	if m["WorkGroup"] != "primary" {
		t.Fatalf("WorkGroup = %v, want primary", m["WorkGroup"])
	}
	// ClientRequestToken is passthrough-only: exactly as given, never derived.
	if m["ClientRequestToken"] != "caller-supplied-token-123" {
		t.Fatalf("ClientRequestToken = %v, want caller-supplied-token-123", m["ClientRequestToken"])
	}
}

func TestBuildStartQueryExecution_EmptyOptionalsOmitted(t *testing.T) {
	args := map[string]any{
		"QueryString":        "SELECT 1",
		"WorkGroup":          "",
		"ClientRequestToken": "",
	}
	b, err := buildStartQueryExecution(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := unmarshalBody(t, b)
	if _, present := m["WorkGroup"]; present {
		t.Fatal("empty WorkGroup should be omitted")
	}
	if _, present := m["ClientRequestToken"]; present {
		t.Fatal("empty ClientRequestToken should be omitted")
	}
}

func TestValidateReadOnlySQL(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		// Allow-set keywords accepted.
		{"select", "SELECT 1", false},
		{"with", "WITH x AS (SELECT 1) SELECT * FROM x", false},
		{"show", "SHOW TABLES", false},
		{"describe", "DESCRIBE t", false},
		{"desc", "DESC t", false},
		{"explain", "EXPLAIN SELECT 1", false},
		{"values", "VALUES (1)", false},

		// Case-insensitivity.
		{"lowercase select", "select 1", false},
		{"mixed-case select", "SeLeCt 1", false},

		// Representative reject keywords.
		{"insert", "INSERT INTO t VALUES (1)", true},
		{"update", "UPDATE t SET a = 1", true},
		{"delete", "DELETE FROM t", true},
		{"merge", "MERGE INTO t USING s ON t.id = s.id", true},
		{"create", "CREATE TABLE t (a int)", true},
		{"alter", "ALTER TABLE t ADD COLUMN b int", true},
		{"drop", "DROP TABLE t", true},
		{"msck", "MSCK REPAIR TABLE t", true},
		{"call", "CALL system.something()", true},
		{"unload", "UNLOAD (SELECT 1) TO 's3://b/'", true},
		{"grant", "GRANT SELECT ON t TO u", true},
		{"revoke", "REVOKE SELECT ON t FROM u", true},
		{"truncate", "TRUNCATE TABLE t", true},

		// Leading line comments.
		{"line comment then select", "-- c\nSELECT 1", false},
		{"line comment then delete", "-- c\nDELETE FROM t", true},

		// Leading block comments.
		{"block comment then select", "/* c */ SELECT 1", false},
		{"multiple leading comments", "/*a*/ -- b\n SELECT 1", false},
		{"block comment then delete", "/* c */ DELETE FROM t", true},

		// Leading paren.
		{"paren select", "(SELECT 1)", false},
		{"paren comment select", "( /*c*/ SELECT 1 )", false},
		{"paren delete", "(DELETE FROM t)", true},

		// EXPLAIN ANALYZE rejected; plain EXPLAIN allowed.
		{"explain analyze", "EXPLAIN ANALYZE SELECT 1", true},
		{"explain analyze extra space", "EXPLAIN   ANALYZE SELECT 1", true},
		{"explain analyze lowercase", "explain analyze select 1", true},
		{"explain select", "EXPLAIN SELECT 1", false},

		// EXPLAIN ANALYZE with intervening comments still rejected; the
		// next-token check strips comments before comparing to ANALYZE.
		{"explain block-comment analyze", "EXPLAIN/*c*/ANALYZE SELECT 1", true},
		{"explain line-comment analyze", "EXPLAIN  --x\n ANALYZE SELECT 1", true},
		// Parenthesized EXPLAIN option form is not EXPLAIN ANALYZE: the
		// leading "(" makes the next token empty, so it is accepted.
		{"explain option form", "EXPLAIN (TYPE DISTRIBUTED) SELECT 1", false},

		// Single leading "(" tolerance is intentional; a second nested "("
		// is not stripped, so the keyword is not found and it is rejected.
		{"multi leading paren rejected (intentional)", "((SELECT 1))", true},

		// A ";" inside a comment or string literal is not a statement
		// terminator and must not false-reject a valid read query.
		{"semicolon in line comment", "SELECT 1 -- has ; in comment", false},
		{"semicolon in block comment", "/* a;b */ SELECT 1", false},
		{"semicolon in string literal", "SELECT 1 WHERE col = 'a;b'", false},

		// Stacked statements rejected.
		{"stacked drop", "SELECT 1; DROP TABLE t", true},
		{"stacked select", "SELECT 1; SELECT 2", true},

		// Single trailing semicolon accepted.
		{"trailing semicolon", "SELECT 1;", false},
		{"trailing semicolon space", "SELECT 1; ", false},
		{"trailing semicolon comment", "SELECT 1; -- trailing comment", false},
		{"trailing semicolon block comment", "SELECT 1; /* done */", false},

		// Empty / whitespace-only / comment-only / paren-only rejected.
		{"empty", "", true},
		{"whitespace only", "   \n\t ", true},
		{"line comment only", "-- just a comment", true},
		{"block comment only", "/* just a comment */", true},
		{"paren only", "(", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReadOnlySQL(tc.sql)
			if tc.wantErr != (err != nil) {
				t.Fatalf("validateReadOnlySQL(%q) err = %v, wantErr = %v", tc.sql, err, tc.wantErr)
			}
		})
	}
}

func TestBuildStartQueryExecution_ReadOnlyGate(t *testing.T) {
	t.Run("write statement rejected by gate", func(t *testing.T) {
		if _, err := buildStartQueryExecution(map[string]any{"QueryString": "DELETE FROM t"}); err == nil {
			t.Fatal("expected gate error for DELETE, got nil")
		}
	})
	t.Run("select still builds a valid body", func(t *testing.T) {
		b, err := buildStartQueryExecution(map[string]any{"QueryString": "SELECT 1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		m := unmarshalBody(t, b)
		if m["QueryString"] != "SELECT 1" {
			t.Fatalf("QueryString = %v, want SELECT 1", m["QueryString"])
		}
	})
}

func TestBuildGetQueryExecution_RequiresId(t *testing.T) {
	if _, err := buildGetQueryExecution(map[string]any{}); err == nil {
		t.Fatal("expected error when QueryExecutionId missing, got nil")
	}
	if _, err := buildGetQueryExecution(map[string]any{"QueryExecutionId": ""}); err == nil {
		t.Fatal("expected error when QueryExecutionId empty, got nil")
	}
}

func TestBuildGetQueryExecution_Body(t *testing.T) {
	b, err := buildGetQueryExecution(map[string]any{"QueryExecutionId": "abc-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := unmarshalBody(t, b)
	if m["QueryExecutionId"] != "abc-123" {
		t.Fatalf("QueryExecutionId = %v, want abc-123", m["QueryExecutionId"])
	}
	if len(m) != 1 {
		t.Fatalf("body should carry only QueryExecutionId, got %v", m)
	}
}

// --- Shared arg helpers (Unit 1) ----------------------------------------

func TestOptionalString(t *testing.T) {
	cases := []struct {
		name   string
		args   map[string]any
		want   string
		wantOk bool
	}{
		{"present", map[string]any{"k": "v"}, "v", true},
		{"absent", map[string]any{}, "", false},
		{"empty", map[string]any{"k": ""}, "", false},
		{"wrong type", map[string]any{"k": 42}, "", false},
		{"nil", map[string]any{"k": nil}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := optionalString(tc.args, "k")
			if ok != tc.wantOk || got != tc.want {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, tc.want, tc.wantOk)
			}
		})
	}
}

func TestRequireStringSlice(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		got, err := requireStringSlice(map[string]any{"ids": []any{"a", "b"}}, "ids")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Fatalf("got %v, want [a b]", got)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := requireStringSlice(map[string]any{}, "ids"); err == nil {
			t.Fatal("expected error for missing key")
		}
	})
	t.Run("empty array", func(t *testing.T) {
		if _, err := requireStringSlice(map[string]any{"ids": []any{}}, "ids"); err == nil {
			t.Fatal("expected error for empty array")
		}
	})
	t.Run("not an array", func(t *testing.T) {
		if _, err := requireStringSlice(map[string]any{"ids": "a"}, "ids"); err == nil {
			t.Fatal("expected error for non-array")
		}
	})
	t.Run("element not string", func(t *testing.T) {
		if _, err := requireStringSlice(map[string]any{"ids": []any{"a", 42}}, "ids"); err == nil {
			t.Fatal("expected error for non-string element")
		}
	})
	t.Run("element empty string", func(t *testing.T) {
		if _, err := requireStringSlice(map[string]any{"ids": []any{"a", ""}}, "ids"); err == nil {
			t.Fatal("expected error for empty-string element")
		}
	})
}

func TestApplyPaging(t *testing.T) {
	t.Run("both present", func(t *testing.T) {
		p := map[string]any{}
		applyPaging(p, map[string]any{"MaxResults": float64(50), "NextToken": "tok"})
		if p["MaxResults"] != float64(50) {
			t.Fatalf("MaxResults = %v (%T), want 50", p["MaxResults"], p["MaxResults"])
		}
		if p["NextToken"] != "tok" {
			t.Fatalf("NextToken = %v, want tok", p["NextToken"])
		}
	})
	t.Run("both absent", func(t *testing.T) {
		p := map[string]any{}
		applyPaging(p, map[string]any{})
		if _, ok := p["MaxResults"]; ok {
			t.Fatal("MaxResults should be omitted when absent")
		}
		if _, ok := p["NextToken"]; ok {
			t.Fatal("NextToken should be omitted when absent")
		}
	})
	t.Run("empty NextToken omitted", func(t *testing.T) {
		p := map[string]any{}
		applyPaging(p, map[string]any{"NextToken": ""})
		if _, ok := p["NextToken"]; ok {
			t.Fatal("empty NextToken should be omitted")
		}
	})
	t.Run("MaxResults survives JSON round-trip as a number", func(t *testing.T) {
		p := map[string]any{}
		applyPaging(p, map[string]any{"MaxResults": float64(25)})
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back map[string]any
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if back["MaxResults"] != float64(25) {
			t.Fatalf("round-tripped MaxResults = %v (%T), want 25", back["MaxResults"], back["MaxResults"])
		}
	})
	t.Run("MaxResults tolerates int and json.Number", func(t *testing.T) {
		p := map[string]any{}
		applyPaging(p, map[string]any{"MaxResults": 10})
		if p["MaxResults"] != float64(10) {
			t.Fatalf("int MaxResults = %v, want 10", p["MaxResults"])
		}
		p2 := map[string]any{}
		applyPaging(p2, map[string]any{"MaxResults": json.Number("99")})
		if p2["MaxResults"] != float64(99) {
			t.Fatalf("json.Number MaxResults = %v, want 99", p2["MaxResults"])
		}
	})
	t.Run("non-numeric MaxResults omitted", func(t *testing.T) {
		p := map[string]any{}
		applyPaging(p, map[string]any{"MaxResults": "lots"})
		if _, ok := p["MaxResults"]; ok {
			t.Fatal("non-numeric MaxResults should be omitted")
		}
	})
}

// --- The 12 new builders (Unit 2) ---------------------------------------

func TestBuildGetQueryResults(t *testing.T) {
	t.Run("requires id", func(t *testing.T) {
		if _, err := buildGetQueryResults(map[string]any{}); err == nil {
			t.Fatal("expected error when QueryExecutionId missing")
		}
		if _, err := buildGetQueryResults(map[string]any{"QueryExecutionId": ""}); err == nil {
			t.Fatal("expected error when QueryExecutionId empty")
		}
	})
	t.Run("id only", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetQueryResults, map[string]any{"QueryExecutionId": "q1"}))
		if m["QueryExecutionId"] != "q1" || len(m) != 1 {
			t.Fatalf("got %v, want only {QueryExecutionId:q1}", m)
		}
	})
	t.Run("with paging", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetQueryResults, map[string]any{
			"QueryExecutionId": "q1", "MaxResults": float64(10), "NextToken": "n",
		}))
		if m["MaxResults"] != float64(10) || m["NextToken"] != "n" {
			t.Fatalf("paging not included: %v", m)
		}
	})
}

func TestBuildStopQueryExecution(t *testing.T) {
	t.Run("requires id", func(t *testing.T) {
		if _, err := buildStopQueryExecution(map[string]any{}); err == nil {
			t.Fatal("expected error when QueryExecutionId missing")
		}
		if _, err := buildStopQueryExecution(map[string]any{"QueryExecutionId": ""}); err == nil {
			t.Fatal("expected error when QueryExecutionId empty")
		}
	})
	t.Run("body", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildStopQueryExecution, map[string]any{"QueryExecutionId": "q1"}))
		if m["QueryExecutionId"] != "q1" || len(m) != 1 {
			t.Fatalf("got %v, want only {QueryExecutionId:q1}", m)
		}
	})
}

func TestBuildListQueryExecutions(t *testing.T) {
	t.Run("empty args yields empty object", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListQueryExecutions, map[string]any{}))
		if len(m) != 0 {
			t.Fatalf("expected empty object, got %v", m)
		}
	})
	t.Run("workgroup and paging", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListQueryExecutions, map[string]any{
			"WorkGroup": "primary", "MaxResults": float64(5), "NextToken": "n",
		}))
		if m["WorkGroup"] != "primary" || m["MaxResults"] != float64(5) || m["NextToken"] != "n" {
			t.Fatalf("optionals not included: %v", m)
		}
	})
	t.Run("empty workgroup omitted", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListQueryExecutions, map[string]any{"WorkGroup": ""}))
		if _, ok := m["WorkGroup"]; ok {
			t.Fatal("empty WorkGroup should be omitted")
		}
	})
}

func TestBuildBatchGetQueryExecution(t *testing.T) {
	t.Run("valid slice produces array body", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildBatchGetQueryExecution, map[string]any{
			"QueryExecutionIds": []any{"a", "b"},
		}))
		arr, ok := m["QueryExecutionIds"].([]any)
		if !ok || len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
			t.Fatalf("QueryExecutionIds = %v, want [a b]", m["QueryExecutionIds"])
		}
	})
	t.Run("missing rejected", func(t *testing.T) {
		if _, err := buildBatchGetQueryExecution(map[string]any{}); err == nil {
			t.Fatal("expected error when QueryExecutionIds missing")
		}
	})
	t.Run("empty rejected", func(t *testing.T) {
		if _, err := buildBatchGetQueryExecution(map[string]any{"QueryExecutionIds": []any{}}); err == nil {
			t.Fatal("expected error when QueryExecutionIds empty")
		}
	})
	t.Run("non-string element rejected", func(t *testing.T) {
		if _, err := buildBatchGetQueryExecution(map[string]any{"QueryExecutionIds": []any{"a", 1}}); err == nil {
			t.Fatal("expected error for non-string element")
		}
	})
}

func TestBuildListDatabases(t *testing.T) {
	t.Run("requires catalog", func(t *testing.T) {
		if _, err := buildListDatabases(map[string]any{}); err == nil {
			t.Fatal("expected error when CatalogName missing")
		}
	})
	t.Run("with paging", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListDatabases, map[string]any{
			"CatalogName": "AwsDataCatalog", "MaxResults": float64(3), "NextToken": "n",
		}))
		if m["CatalogName"] != "AwsDataCatalog" || m["MaxResults"] != float64(3) || m["NextToken"] != "n" {
			t.Fatalf("got %v", m)
		}
	})
}

func TestBuildGetDatabase(t *testing.T) {
	t.Run("requires both", func(t *testing.T) {
		if _, err := buildGetDatabase(map[string]any{"CatalogName": "c"}); err == nil {
			t.Fatal("expected error when DatabaseName missing")
		}
		if _, err := buildGetDatabase(map[string]any{"DatabaseName": "d"}); err == nil {
			t.Fatal("expected error when CatalogName missing")
		}
	})
	t.Run("body", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetDatabase, map[string]any{"CatalogName": "c", "DatabaseName": "d"}))
		if m["CatalogName"] != "c" || m["DatabaseName"] != "d" || len(m) != 2 {
			t.Fatalf("got %v", m)
		}
	})
}

func TestBuildListTableMetadata(t *testing.T) {
	t.Run("requires catalog and database", func(t *testing.T) {
		if _, err := buildListTableMetadata(map[string]any{"CatalogName": "c"}); err == nil {
			t.Fatal("expected error when DatabaseName missing")
		}
		if _, err := buildListTableMetadata(map[string]any{"DatabaseName": "d"}); err == nil {
			t.Fatal("expected error when CatalogName missing")
		}
	})
	t.Run("optionals included", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListTableMetadata, map[string]any{
			"CatalogName": "c", "DatabaseName": "d", "Expression": "foo*",
			"MaxResults": float64(7), "NextToken": "n",
		}))
		if m["Expression"] != "foo*" || m["MaxResults"] != float64(7) || m["NextToken"] != "n" {
			t.Fatalf("optionals missing: %v", m)
		}
	})
	t.Run("empty expression omitted", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListTableMetadata, map[string]any{
			"CatalogName": "c", "DatabaseName": "d", "Expression": "",
		}))
		if _, ok := m["Expression"]; ok {
			t.Fatal("empty Expression should be omitted")
		}
	})
}

func TestBuildGetTableMetadata(t *testing.T) {
	t.Run("requires all three", func(t *testing.T) {
		if _, err := buildGetTableMetadata(map[string]any{"CatalogName": "c", "DatabaseName": "d"}); err == nil {
			t.Fatal("expected error when TableName missing")
		}
		if _, err := buildGetTableMetadata(map[string]any{"CatalogName": "c", "TableName": "t"}); err == nil {
			t.Fatal("expected error when DatabaseName missing")
		}
		if _, err := buildGetTableMetadata(map[string]any{"DatabaseName": "d", "TableName": "t"}); err == nil {
			t.Fatal("expected error when CatalogName missing")
		}
	})
	t.Run("body", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetTableMetadata, map[string]any{
			"CatalogName": "c", "DatabaseName": "d", "TableName": "t",
		}))
		if m["CatalogName"] != "c" || m["DatabaseName"] != "d" || m["TableName"] != "t" || len(m) != 3 {
			t.Fatalf("got %v", m)
		}
	})
}

func TestBuildListWorkGroups(t *testing.T) {
	t.Run("empty args yields empty object", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListWorkGroups, map[string]any{}))
		if len(m) != 0 {
			t.Fatalf("expected empty object, got %v", m)
		}
	})
	t.Run("with paging", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListWorkGroups, map[string]any{"MaxResults": float64(2), "NextToken": "n"}))
		if m["MaxResults"] != float64(2) || m["NextToken"] != "n" {
			t.Fatalf("got %v", m)
		}
	})
}

func TestBuildGetWorkGroup(t *testing.T) {
	t.Run("requires workgroup", func(t *testing.T) {
		if _, err := buildGetWorkGroup(map[string]any{}); err == nil {
			t.Fatal("expected error when WorkGroup missing")
		}
	})
	t.Run("body", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetWorkGroup, map[string]any{"WorkGroup": "primary"}))
		if m["WorkGroup"] != "primary" || len(m) != 1 {
			t.Fatalf("got %v", m)
		}
	})
}

func TestBuildListDataCatalogs(t *testing.T) {
	t.Run("empty args yields empty object", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListDataCatalogs, map[string]any{}))
		if len(m) != 0 {
			t.Fatalf("expected empty object, got %v", m)
		}
	})
	t.Run("with paging", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildListDataCatalogs, map[string]any{"MaxResults": float64(4), "NextToken": "n"}))
		if m["MaxResults"] != float64(4) || m["NextToken"] != "n" {
			t.Fatalf("got %v", m)
		}
	})
}

func TestBuildGetDataCatalog(t *testing.T) {
	t.Run("requires name", func(t *testing.T) {
		if _, err := buildGetDataCatalog(map[string]any{}); err == nil {
			t.Fatal("expected error when Name missing")
		}
	})
	t.Run("name only", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetDataCatalog, map[string]any{"Name": "cat"}))
		if m["Name"] != "cat" || len(m) != 1 {
			t.Fatalf("got %v", m)
		}
	})
	t.Run("optional workgroup", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetDataCatalog, map[string]any{"Name": "cat", "WorkGroup": "primary"}))
		if m["WorkGroup"] != "primary" {
			t.Fatalf("WorkGroup missing: %v", m)
		}
	})
	t.Run("empty workgroup omitted", func(t *testing.T) {
		m := unmarshalBody(t, mustBuild(t, buildGetDataCatalog, map[string]any{"Name": "cat", "WorkGroup": ""}))
		if _, ok := m["WorkGroup"]; ok {
			t.Fatal("empty WorkGroup should be omitted")
		}
	})
}

func TestBuildAthenaURL(t *testing.T) {
	cases := []struct {
		region string
		want   string
	}{
		{"us-east-1", "https://athena.us-east-1.amazonaws.com/"},
		{"eu-west-2", "https://athena.eu-west-2.amazonaws.com/"},
		{"ap-southeast-1", "https://athena.ap-southeast-1.amazonaws.com/"},
	}
	for _, tc := range cases {
		t.Run(tc.region, func(t *testing.T) {
			if got := buildAthenaURL(tc.region); got != tc.want {
				t.Fatalf("buildAthenaURL(%q) = %q, want %q", tc.region, got, tc.want)
			}
		})
	}
}

func TestBuildAthenaTarget(t *testing.T) {
	cases := []struct {
		action string
		want   string
	}{
		{"StartQueryExecution", "AmazonAthena.StartQueryExecution"},
		{"GetQueryResults", "AmazonAthena.GetQueryResults"},
		{"ListDataCatalogs", "AmazonAthena.ListDataCatalogs"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			if got := buildAthenaTarget(tc.action); got != tc.want {
				t.Fatalf("buildAthenaTarget(%q) = %q, want %q", tc.action, got, tc.want)
			}
		})
	}
}

// --- run_query pure helpers --------------------------------------------

func TestResolveTimeoutSeconds(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want int
	}{
		{"absent uses default", map[string]any{}, defaultRunQueryTimeoutSeconds},
		{"float override", map[string]any{"TimeoutSeconds": float64(30)}, 30},
		{"int override", map[string]any{"TimeoutSeconds": 45}, 45},
		{"json.Number override", map[string]any{"TimeoutSeconds": json.Number("60")}, 60},
		{"zero falls back to default", map[string]any{"TimeoutSeconds": float64(0)}, defaultRunQueryTimeoutSeconds},
		{"negative falls back to default", map[string]any{"TimeoutSeconds": float64(-5)}, defaultRunQueryTimeoutSeconds},
		{"non-numeric falls back to default", map[string]any{"TimeoutSeconds": "soon"}, defaultRunQueryTimeoutSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveTimeoutSeconds(tc.args); got != tc.want {
				t.Fatalf("resolveTimeoutSeconds(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestClassifyQueryState(t *testing.T) {
	cases := []struct {
		state string
		want  queryStateClass
	}{
		{"SUCCEEDED", queryStateSucceeded},
		{"FAILED", queryStateFailed},
		{"CANCELLED", queryStateCancelled},
		{"QUEUED", queryStateRunning},
		{"RUNNING", queryStateRunning},
		{"", queryStateRunning},
		{"BOGUS", queryStateUnknown},
		{"succeeded", queryStateUnknown}, // case-sensitive: Athena emits upper-case
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			if got := classifyQueryState(tc.state); got != tc.want {
				t.Fatalf("classifyQueryState(%q) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

func TestQueryExecutionStatus(t *testing.T) {
	t.Run("full status", func(t *testing.T) {
		resp := map[string]any{
			"QueryExecution": map[string]any{
				"Status": map[string]any{
					"State":             "FAILED",
					"StateChangeReason": "boom",
				},
			},
		}
		state, reason := queryExecutionStatus(resp)
		if state != "FAILED" || reason != "boom" {
			t.Fatalf("got (%q,%q), want (FAILED,boom)", state, reason)
		}
	})
	t.Run("missing members yield empties", func(t *testing.T) {
		state, reason := queryExecutionStatus(map[string]any{})
		if state != "" || reason != "" {
			t.Fatalf("got (%q,%q), want empties", state, reason)
		}
	})
	t.Run("succeeded has no reason", func(t *testing.T) {
		resp := map[string]any{
			"QueryExecution": map[string]any{"Status": map[string]any{"State": "SUCCEEDED"}},
		}
		state, reason := queryExecutionStatus(resp)
		if state != "SUCCEEDED" || reason != "" {
			t.Fatalf("got (%q,%q), want (SUCCEEDED,\"\")", state, reason)
		}
	})
}

func TestResultPage(t *testing.T) {
	t.Run("result set and next token", func(t *testing.T) {
		resp := map[string]any{
			"ResultSet": map[string]any{"Rows": []any{}},
			"NextToken": "tok",
		}
		rs, tok := resultPage(resp)
		if rs == nil || tok != "tok" {
			t.Fatalf("got (%v,%q), want (non-nil, tok)", rs, tok)
		}
	})
	t.Run("last page has no next token", func(t *testing.T) {
		resp := map[string]any{"ResultSet": map[string]any{"Rows": []any{}}}
		rs, tok := resultPage(resp)
		if rs == nil || tok != "" {
			t.Fatalf("got (%v,%q), want (non-nil, \"\")", rs, tok)
		}
	})
	t.Run("missing result set", func(t *testing.T) {
		rs, tok := resultPage(map[string]any{})
		if rs != nil || tok != "" {
			t.Fatalf("got (%v,%q), want (nil, \"\")", rs, tok)
		}
	})
}

func TestMergeResultPages(t *testing.T) {
	// Build a row carrying a single VarCharValue, the Athena Row shape.
	row := func(v string) any {
		return map[string]any{"Data": []any{map[string]any{"VarCharValue": v}}}
	}

	t.Run("single page keeps header and metadata", func(t *testing.T) {
		page1 := map[string]any{
			"Rows":              []any{row("col"), row("a"), row("b")},
			"ResultSetMetadata": map[string]any{"ColumnInfo": []any{map[string]any{"Name": "col"}}},
		}
		merged := mergeResultPages([]map[string]any{page1})
		rows, _ := merged["Rows"].([]any)
		if len(rows) != 3 {
			t.Fatalf("rows = %d, want 3", len(rows))
		}
		if _, ok := merged["ResultSetMetadata"]; !ok {
			t.Fatal("ResultSetMetadata should be carried from page 1")
		}
	})

	t.Run("multi page concatenates without repeating header", func(t *testing.T) {
		// Athena emits the header only on page 1; later pages carry data
		// rows only. Plain concatenation must yield [header, data...] once.
		page1 := map[string]any{
			"Rows":              []any{row("col"), row("a")},
			"ResultSetMetadata": map[string]any{"ColumnInfo": []any{map[string]any{"Name": "col"}}},
		}
		page2 := map[string]any{"Rows": []any{row("b"), row("c")}}
		page3 := map[string]any{"Rows": []any{row("d")}}
		merged := mergeResultPages([]map[string]any{page1, page2, page3})
		rows, _ := merged["Rows"].([]any)
		if len(rows) != 5 {
			t.Fatalf("rows = %d, want 5 (header + 4 data)", len(rows))
		}
		// First row is the header from page 1.
		first, _ := rows[0].(map[string]any)
		data, _ := first["Data"].([]any)
		cell, _ := data[0].(map[string]any)
		if cell["VarCharValue"] != "col" {
			t.Fatalf("first row = %v, want the header row", rows[0])
		}
		// Metadata kept once from page 1.
		if _, ok := merged["ResultSetMetadata"]; !ok {
			t.Fatal("ResultSetMetadata should be kept from page 1")
		}
	})

	t.Run("metadata taken from first page that carries it", func(t *testing.T) {
		page1 := map[string]any{"Rows": []any{row("a")}}
		page2 := map[string]any{
			"Rows":              []any{row("b")},
			"ResultSetMetadata": map[string]any{"ColumnInfo": []any{map[string]any{"Name": "x"}}},
		}
		merged := mergeResultPages([]map[string]any{page1, page2})
		if _, ok := merged["ResultSetMetadata"]; !ok {
			t.Fatal("ResultSetMetadata should be picked up from a later page when page 1 lacks it")
		}
	})

	t.Run("empty pages yield empty rows array", func(t *testing.T) {
		merged := mergeResultPages(nil)
		rows, ok := merged["Rows"].([]any)
		if !ok || len(rows) != 0 {
			t.Fatalf("Rows = %v, want empty array", merged["Rows"])
		}
		if _, ok := merged["ResultSetMetadata"]; ok {
			t.Fatal("no metadata expected when there are no pages")
		}
	})

	t.Run("nil page entries skipped", func(t *testing.T) {
		page := map[string]any{"Rows": []any{row("a")}}
		merged := mergeResultPages([]map[string]any{nil, page, nil})
		rows, _ := merged["Rows"].([]any)
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
	})

	t.Run("page missing Rows contributes nothing", func(t *testing.T) {
		page1 := map[string]any{"Rows": []any{row("a")}}
		page2 := map[string]any{"ResultSetMetadata": map[string]any{}} // no Rows
		merged := mergeResultPages([]map[string]any{page1, page2})
		rows, _ := merged["Rows"].([]any)
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
	})

	t.Run("merged ResultSet round-trips as JSON", func(t *testing.T) {
		page1 := map[string]any{"Rows": []any{row("a")}}
		merged := mergeResultPages([]map[string]any{page1})
		if _, err := json.Marshal(merged); err != nil {
			t.Fatalf("merged ResultSet not JSON-serializable: %v", err)
		}
	})
}

// TestRunQueryReadOnlyGate proves the run_query path rejects writes through
// the SAME read-only gate the async start path uses: run_query builds its
// StartQueryExecution body with buildStartQueryExecution, so a non-read
// QueryString fails before any host call.
func TestRunQueryReadOnlyGate(t *testing.T) {
	t.Run("write statement rejected", func(t *testing.T) {
		for _, q := range []string{"DELETE FROM t", "INSERT INTO t VALUES (1)", "DROP TABLE t", "SELECT 1; DROP TABLE t"} {
			if _, err := buildStartQueryExecution(map[string]any{"QueryString": q}); err == nil {
				t.Fatalf("expected gate error for %q, got nil", q)
			}
		}
	})
	t.Run("read statement passes", func(t *testing.T) {
		if _, err := buildStartQueryExecution(map[string]any{"QueryString": "SELECT 1"}); err != nil {
			t.Fatalf("unexpected gate error for SELECT: %v", err)
		}
	})
}

// mustBuild runs a builder and fails the test on error, returning the body.
func mustBuild(t *testing.T, build func(map[string]any) ([]byte, error), args map[string]any) []byte {
	t.Helper()
	b, err := build(args)
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	return b
}
