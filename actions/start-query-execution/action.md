+++
name = "start-query-execution"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/start-query-execution@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["start_query_execution"]

[match]
intent = "run an Athena SQL query"

[[execute]]
id = "start"
connector = "github://ALRubinger/aileron-connector-athena"
op = "start_query_execution"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "query_string"
type = "string"
description = "The SQL text to execute. Only read-only statements are accepted: SELECT, WITH, SHOW, DESCRIBE, DESC, EXPLAIN, and VALUES. EXPLAIN ANALYZE is rejected because it executes the statement, and stacked statements (a second statement after a semicolon) are rejected. A single trailing semicolon is allowed."
required = true
multiline = true

[[inputs]]
name = "query_execution_context"
type = "object"
description = "Optional execution context object with the shape {Database, Catalog}. Sets the default database and data catalog the query runs against. Passed through to Athena verbatim only when present."
required = false

[[inputs]]
name = "result_configuration"
type = "object"
description = "Optional result configuration object with the shape {OutputLocation, ...}. Selects where Athena writes the query results, for example {\"OutputLocation\": \"s3://bucket/prefix/\"}. Passed through to Athena verbatim only when present."
required = false

[[inputs]]
name = "work_group"
type = "string"
description = "Optional work group name. Scopes the query to a specific Athena work group, which can pin its own result location and limits."
required = false

[[inputs]]
name = "client_request_token"
type = "string"
description = "Optional caller-supplied idempotency token. Athena treats two StartQueryExecution calls carrying the same token as the same request. A non-empty token supplied here is honored verbatim. When omitted, the connector synthesizes a deterministic token — the hex-encoded SHA-256 of the canonical request (query string plus execution context, result configuration, and work group) — so the same request always maps to the same token. (Athena requires a non-null/non-empty token, and this connector hand-builds the request with no AWS SDK to auto-generate one.) Because the token is a deterministic function of the request, this action is declared idempotent = true at the manifest level per ADR-0010."
required = false

[[inputs]]
name = "execution_parameters"
type = "array"
items_type = "string"
description = "Optional ordered list of string values bound to the query's \"?\" placeholders, for a parameterized (prepared) statement, for example [\"14\", \"2026-06-29\"] for `WHERE created >= date_add('day', -?, ?)`. Emitted as Athena's ExecutionParameters field only when present and non-empty; each member must be a non-empty string (Athena's min-length-1 parameter constraint). The read-only SQL gate is unaffected — these are bound values, not SQL text. Bound parameters fold into the synthesized client_request_token, so the same SQL bound to different values yields distinct idempotency tokens and distinct executions."
required = false
+++

# Run an Athena SQL Query

Submits a SQL query to Athena for asynchronous execution. Returns the
raw StartQueryExecution response carrying the `QueryExecutionId` that
the caller polls with `get-query-execution` and reads with
`get-query-results`. The query runs in the background. This action does
not wait for it to finish.

When it fires:
- "run a query counting orders by month against the sales table"
- "select the top 10 customers by revenue this quarter"
- "show the columns in the events table"

Pair with:
- `get-query-execution` to poll the lifecycle state until the query
  reaches SUCCEEDED, FAILED, or CANCELLED,
- `get-query-results` to page the result rows once the query has
  SUCCEEDED,
- `stop-query-execution` to cancel a query that is still running.

This action enforces a read-only SQL gate before any Athena call. The
connector's `validateReadOnlySQL` accepts only statements that begin
with SELECT, WITH, SHOW, DESCRIBE, DESC, EXPLAIN, or VALUES. It rejects
EXPLAIN ANALYZE because that form executes the statement, and it
rejects stacked statements where a second statement follows a
semicolon. A single trailing semicolon is allowed. The gate is a
defense-in-depth layer. The primary guarantee is the read-only IAM
principal the host signs requests as, which cannot perform writes or
DDL regardless of the SQL submitted.

This action is declared `idempotent = true` per ADR-0010. When the
caller omits `client_request_token`, the connector synthesizes a
deterministic token — the hex-encoded SHA-256 of the canonical request
(query string plus any execution context, result configuration, and work
group). Because the token is a pure function of the request, two
identical calls carry the same token and Athena collapses them onto a
single query execution rather than starting a second one. A caller that
wants distinct executions for an identical request can vary the request
or supply its own `client_request_token`. Replaying the same request
therefore does not spawn duplicate work, which is what makes the action
safe to declare idempotent.

The connector runs in the Aileron WASM sandbox with
`[capabilities.network]` allow-listing the regional Athena hosts. Each
request is marked `credential = "aws_sigv4"` and signed host-side with
SigV4 at the network boundary. The connector never sees the secret
access key. See ADR-0005 (sandbox and credential mediation) in the
Aileron docs.
