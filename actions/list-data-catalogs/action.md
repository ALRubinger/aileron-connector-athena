+++
name = "list-data-catalogs"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/list-data-catalogs@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["list_data_catalogs"]

[match]
intent = "list registered Athena data catalogs"

[[execute]]
id = "list"
connector = "github://ALRubinger/aileron-connector-athena"
op = "list_data_catalogs"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "max_results"
type = "integer"
description = "Optional maximum number of data catalogs to return in one page. Omit to take Athena's default page size."
required = false

[[inputs]]
name = "next_token"
type = "string"
description = "Optional paging token from a previous ListDataCatalogs response. Pass it to fetch the next page."
required = false
+++

# List Athena Data Catalogs

Lists the data catalogs registered with Athena. Returns the raw
ListDataCatalogs response, which carries a DataCatalogsSummary list of
catalog summaries and a NextToken when more remain. The default Glue
catalog is usually named AwsDataCatalog.

When it fires:
- "what data catalogs are registered"
- "list the catalogs I can query"
- "show me the available data catalogs"

Pair with:
- `get-data-catalog` to read one catalog's configuration in detail,
- `list-databases` to list the databases inside a catalog,
- `list-table-metadata` to drill into a database's tables.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
