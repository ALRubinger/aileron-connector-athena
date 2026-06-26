+++
name = "run-query"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/run-query@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["run_query"]

[match]
intent = "run an Athena SQL query to completion and return its rows"

[[execute]]
id = "run"
connector = "github://ALRubinger/aileron-connector-athena"
op = "run_query"
idempotent = false

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "QueryString"
type = "string"
description = "The SQL text to execute. Only read-only statements are accepted: SELECT, WITH, SHOW, DESCRIBE, DESC, EXPLAIN, and VALUES. EXPLAIN ANALYZE is rejected because it executes the statement, and stacked statements (a second statement after a semicolon) are rejected. A single trailing semicolon is allowed."
required = true
multiline = true

[[inputs]]
name = "QueryExecutionContext"
type = "object"
description = "Optional execution context object with the shape {Database, Catalog}. Sets the default database and data catalog the query runs against. Passed through to Athena verbatim only when present."
required = false

[[inputs]]
name = "ResultConfiguration"
type = "object"
description = "Optional result configuration object with the shape {OutputLocation, ...}. Selects where Athena writes the query results, for example {\"OutputLocation\": \"s3://bucket/prefix/\"}. Passed through to Athena verbatim only when present."
required = false

[[inputs]]
name = "WorkGroup"
type = "string"
description = "Optional work group name. Scopes the query to a specific Athena work group, which can pin its own result location and limits."
required = false

[[inputs]]
name = "ClientRequestToken"
type = "string"
description = "Optional caller-supplied idempotency token. Athena treats two StartQueryExecution calls carrying the same token as the same request. This token is passthrough only and the connector never derives or synthesizes it. Because the caller owns idempotency here, this action is declared idempotent = false at the manifest level."
required = false

[[inputs]]
name = "TimeoutSeconds"
type = "integer"
description = "Optional overall budget, in seconds, for the wait loop to drive the query to a terminal state. Defaults to 180 (three minutes), mirroring the connector's live round-trip test ceiling. A query still QUEUED or RUNNING when the budget is spent fails with a connector_runtime_error rather than hanging. A non-positive or non-numeric value falls back to the default."
required = false
+++

# Run an Athena SQL Query to Completion

Runs a SQL query against Athena end to end in a single synchronous call
and returns its full result set. The action internalizes the whole
Athena lifecycle: it submits the query with `StartQueryExecution`, waits
on `GetQueryExecution` until the execution reaches a terminal state, and
on success pages `GetQueryResults` to completion. The response carries
`{QueryExecutionId, ResultSet}`, where `ResultSet` is the concatenation
of every result page (the column header appears once, from the first
page) with the `ResultSetMetadata` kept once.

This is the deterministic-runtime sibling of `start-query-execution`.
Aileron Flight Plans and other no-LLM callers have a step graph with no
loop, conditional, or wait construct, so they cannot run the async
start/poll/results dance themselves. `run-query` does that waiting and
paging inside one action call, so a single deterministic step can drive a
query to its rows.

When it fires:
- "run SELECT count(*) FROM sales.orders and return the rows"
- "run this Athena query to completion in a Flight Plan step"
- "give me the result of this query in one call"

Pair with:
- `start-query-execution` + `get-query-execution` + `get-query-results`
  when an LLM is in the loop and can poll between calls. Those three async
  actions stay as-is; `run-query` is additive, not a replacement.
- `stop-query-execution` to cancel a query that is still running.

Bounded wait. The wait loop polls every two seconds and is bounded by
`TimeoutSeconds` (default 180). A query that is still `QUEUED` or
`RUNNING` when the budget is spent fails with a `connector_runtime_error`
rather than hanging the call. A `FAILED` or `CANCELLED` execution fails
with an `external_api_error` carrying Athena's `StateChangeReason`.

Read-only. This action enforces the same read-only SQL gate as
`start-query-execution` before any Athena call. The connector's
`validateReadOnlySQL` accepts only statements that begin with SELECT,
WITH, SHOW, DESCRIBE, DESC, EXPLAIN, or VALUES. It rejects EXPLAIN
ANALYZE because that form executes the statement, and it rejects stacked
statements where a second statement follows a semicolon. A single
trailing semicolon is allowed. The gate is a defense-in-depth layer. The
primary guarantee is the read-only IAM principal the host signs requests
as, which cannot perform writes or DDL regardless of the SQL submitted.

This action is declared `idempotent = false`. Like
`start-query-execution`, each call submits a fresh execution and returns
a new `QueryExecutionId`. Athena's own idempotency is caller-driven
through the optional `ClientRequestToken`; two calls without a shared
token start two separate executions, so the manifest leaves idempotency
to the caller rather than asserting it.

The connector runs in the Aileron WASM sandbox with
`[capabilities.network]` allow-listing the regional Athena hosts.
`run-query` makes every internal call — StartQueryExecution,
GetQueryExecution, GetQueryResults — through that same network boundary
with the same `aws_sigv4` credential; it needs no additional network host
or credential. Each request is marked `credential = "aws_sigv4"` and
signed host-side with SigV4 at the network boundary. The connector never
sees the secret access key. See ADR-0005 (sandbox and credential
mediation) in the Aileron docs.
