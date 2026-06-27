+++
name = "get-query-execution"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/get-query-execution@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["get_query_execution"]

[match]
intent = "look up the status of an Athena query execution"

[[execute]]
id = "fetch"
connector = "github://ALRubinger/aileron-connector-athena"
op = "get_query_execution"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "query_execution_id"
type = "string"
description = "The query execution id returned by start-query-execution in QueryExecutionId."
required = true
+++

# Look Up an Athena Query Execution

Reports the lifecycle state of a query previously submitted with
`start-query-execution`. Returns the raw GetQueryExecution response,
which carries the query status (QUEUED, RUNNING, SUCCEEDED, FAILED, or
CANCELLED), the SQL text, the output location, and execution
statistics.

When it fires:
- "is my query done yet"
- "what's the status of query execution abc-123"
- "why did that query fail"

Pair with:
- `start-query-execution` to submit the query and get the id this
  action polls,
- `get-query-results` to read the result rows once the state is
  SUCCEEDED,
- `stop-query-execution` to cancel a query still in QUEUED or RUNNING.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
