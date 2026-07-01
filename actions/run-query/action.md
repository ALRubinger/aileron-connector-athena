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
name = "idempotency_salt"
type = "string"
description = "Optional opaque salt that forces a fresh execution for an otherwise-identical request. When you omit client_request_token, the connector derives a deterministic idempotency token from the canonical request; supplying a salt folds it into that derivation so the same logical query with a different salt yields a distinct token and a distinct execution. The salt is never sent to Athena as a request field — it only influences the derived token. When absent, behavior is unchanged. Ignored when client_request_token is supplied (an explicit token already controls idempotency). Use it to recover from a 400 IDEMPOTENT_PARAMETER_MISMATCH (a derived-token collision with a prior run whose parameters differed, e.g. a changed work-group result location)."
required = false

[[inputs]]
name = "execution_parameters"
type = "array"
items_type = "string"
description = "Optional ordered list of string values bound to the query's \"?\" placeholders, for a parameterized (prepared) statement, for example [\"14\", \"2026-06-29\"] for `WHERE created >= date_add('day', -?, ?)`. Emitted as Athena's ExecutionParameters field only when present and non-empty; each member must be a non-empty string (Athena's min-length-1 parameter constraint). The read-only SQL gate is unaffected — these are bound values, not SQL text. Bound parameters fold into the synthesized client_request_token, so the same SQL bound to different values yields distinct idempotency tokens and distinct executions."
required = false

[[inputs]]
name = "timeout_seconds"
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
`timeout_seconds` (default 180). A query that is still `QUEUED` or
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

This action is declared `idempotent = true` per ADR-0010. When the
caller omits `client_request_token`, the connector synthesizes a
deterministic token — the hex-encoded SHA-256 of the canonical request
(query string plus any execution context, result configuration, and work
group). Because the token is a pure function of the request, two
identical `run-query` calls carry the same token, and Athena collapses
them onto a single execution rather than starting a second one. A caller
that wants distinct executions for an identical request can still vary
the request, pass an `idempotency_salt` (folded into the derived token
without being sent to Athena), or supply its own `client_request_token`.
This is what makes the action safe to declare idempotent: replaying the
same request does not spawn duplicate work.

Dedup is the right default for a `SUCCEEDED` re-run (no duplicate Athena
scan or cost), but Athena's idempotency also replays a *terminal-failed*
execution: once the deterministic token first produced a `FAILED` or
`CANCELLED` execution, every bare re-launch of the same request returns
that same frozen failure (identical `QueryExecutionId`), so a transient
error that has since been fixed would never re-run. `run-query`
self-heals this on the deterministic-token path (no `client_request_token`,
no `idempotency_salt`) with a clock-free discriminator: when the **first**
`GetQueryExecution` poll already shows a terminal `FAILED` or `CANCELLED`
state — proof Athena replayed a pre-existing execution, since a query this
call actually started is never already-terminal on the first poll — it
re-issues StartQueryExecution **once** with a fresh token salted from the
stale `QueryExecutionId` and polls the new execution. (The connector runs
in the Aileron wasip1 sandbox with a frozen fake wall clock, so it cannot
compare `SubmissionDateTime` against the current time or mint a `UnixNano`
nonce; poll ordinality and the returned `QueryExecutionId` are the
clock-free substitutes.) A genuinely fresh failure — which necessarily runs
past the first poll — is returned as-is, so real failures are never
double-executed, and the single re-issue caps cost and rules out an
infinite loop. Net effect: a successful re-launch still returns the same
result with no salt; a re-launch after a since-fixed failure re-executes
automatically with no salt; and `idempotency_salt` remains the explicit
override to force a fresh execution of an otherwise-identical `SUCCEEDED`
request. (This self-heal is a `run-query` property: the async
`start-query-execution` op returns immediately and structurally cannot
observe the outcome within one call, so it keeps the plain deterministic
token.)

If Athena replies with a 400 `IDEMPOTENT_PARAMETER_MISMATCH` on the
internal StartQueryExecution call, the derived token collided with a prior
execution that used the same logical request but different parameters —
for example a work-group result location that changed since the first run.
`run-query` reports this as an `external_api_error` that names the code and
points you at `idempotency_salt` (or an explicit `client_request_token`)
to mint a fresh token and force a new execution.

The connector runs in the Aileron WASM sandbox with
`[capabilities.network]` allow-listing the regional Athena hosts.
`run-query` makes every internal call — StartQueryExecution,
GetQueryExecution, GetQueryResults — through that same network boundary
with the same `aws_sigv4` credential; it needs no additional network host
or credential. Each request is marked `credential = "aws_sigv4"` and
signed host-side with SigV4 at the network boundary. The connector never
sees the secret access key. See ADR-0005 (sandbox and credential
mediation) in the Aileron docs.
