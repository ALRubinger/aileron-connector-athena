+++
name = "get-query-results"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/get-query-results@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["get_query_results"]

[match]
intent = "fetch the result rows of an Athena query"

[[execute]]
id = "fetch"
connector = "github://ALRubinger/aileron-connector-athena"
op = "get_query_results"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. It must equal the region pinned in the connector manifest's [capabilities.network] host and [capabilities.credential].region. A region the allow-list does not list fails closed as capability_denied at the network boundary. A region that disagrees with the credential yields a SigV4 signature Athena rejects."
required = true

[[inputs]]
name = "QueryExecutionId"
type = "string"
description = "The query execution id of a SUCCEEDED query, as returned by start-query-execution and confirmed SUCCEEDED by get-query-execution."
required = true

[[inputs]]
name = "MaxResults"
type = "integer"
description = "Optional maximum number of result rows to return in one page. Omit to take Athena's default page size."
required = false

[[inputs]]
name = "NextToken"
type = "string"
description = "Optional paging token from a previous GetQueryResults response. Pass it to fetch the next page of rows."
required = false
+++

# Fetch Athena Query Results

Pages the result rows of a SUCCEEDED query. Returns the raw
GetQueryResults response, which carries a ResultSet of rows with a
column metadata header and a NextToken when more rows remain.

When it fires:
- "show me the rows from query execution abc-123"
- "get the next page of results"
- "what did that query return"

Pair with:
- `get-query-execution` to confirm the query reached SUCCEEDED before
  reading rows,
- `start-query-execution` to submit the query that produced this id,
- this action again with the returned NextToken to walk additional
  pages.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` pinned to the one regional Athena
host. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
