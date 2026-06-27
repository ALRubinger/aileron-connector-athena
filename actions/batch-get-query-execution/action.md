+++
name = "batch-get-query-execution"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/batch-get-query-execution@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["batch_get_query_execution"]

[match]
intent = "fetch details for a batch of Athena query executions"

[[execute]]
id = "fetch"
connector = "github://ALRubinger/aileron-connector-athena"
op = "batch_get_query_execution"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "query_execution_ids"
type = "array"
items_type = "string"
description = "A non-empty list of query execution id strings to look up in one call, for example [\"abc-123\", \"def-456\"]. Each id is a string returned by start-query-execution or list-query-executions."
required = true
+++

# Fetch a Batch of Athena Query Executions

Fetches details for a batch of query executions in one call. Returns
the raw BatchGetQueryExecution response, which carries a
QueryExecutions list for the ids that resolved and an
UnprocessedQueryExecutionIds list for any that did not.

When it fires:
- "get details for these query execution ids"
- "fetch the status of the last five queries at once"
- "look up this batch of execution ids"

Pair with:
- `list-query-executions` to gather a page of ids to pass here,
- `get-query-execution` when only one id needs inspection,
- `get-query-results` to read the rows of any id that SUCCEEDED.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
