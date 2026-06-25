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
