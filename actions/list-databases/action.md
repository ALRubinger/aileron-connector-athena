+++
name = "list-databases"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/list-databases@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["list_databases"]

[match]
intent = "list databases in a Glue data catalog"

[[execute]]
id = "list"
connector = "github://ALRubinger/aileron-connector-athena"
op = "list_databases"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "catalog_name"
type = "string"
description = "The data catalog to list databases from, for example \"AwsDataCatalog\" for the default Glue catalog. As returned by list-data-catalogs in DataCatalogsSummary[].CatalogName."
required = true

[[inputs]]
name = "max_results"
type = "integer"
description = "Optional maximum number of databases to return in one page. Omit to take Athena's default page size."
required = false

[[inputs]]
name = "next_token"
type = "string"
description = "Optional paging token from a previous ListDatabases response. Pass it to fetch the next page."
required = false
+++

# List Databases in a Data Catalog

Lists the databases registered in a data catalog. Returns the raw
ListDatabases response, which carries a DatabaseList of database
summaries and a NextToken when more remain.

When it fires:
- "what databases are in the AwsDataCatalog catalog"
- "list the schemas I can query"
- "show me the databases in this catalog"

Pair with:
- `list-data-catalogs` to find a catalog name to pass here,
- `get-database` to read one database in detail,
- `list-table-metadata` to list the tables inside a database,
- `get-table-metadata` to read one table in detail.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
