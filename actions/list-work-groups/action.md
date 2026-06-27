+++
name = "list-work-groups"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/list-work-groups@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["list_work_groups"]

[match]
intent = "list Athena work groups"

[[execute]]
id = "list"
connector = "github://ALRubinger/aileron-connector-athena"
op = "list_work_groups"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "max_results"
type = "integer"
description = "Optional maximum number of work groups to return in one page. Omit to take Athena's default page size."
required = false

[[inputs]]
name = "next_token"
type = "string"
description = "Optional paging token from a previous ListWorkGroups response. Pass it to fetch the next page."
required = false
+++

# List Athena Work Groups

Lists the Athena work groups in the account. Returns the raw
ListWorkGroups response, which carries a WorkGroups list of work group
summaries and a NextToken when more remain.

When it fires:
- "what work groups are available"
- "list the Athena work groups"
- "show me the query work groups I can use"

Pair with:
- `get-work-group` to read one work group's configuration in detail,
- `list-query-executions` to scope a query listing to a work group,
- `start-query-execution` to run a query against a chosen work group.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
