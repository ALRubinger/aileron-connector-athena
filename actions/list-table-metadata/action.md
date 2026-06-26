+++
name = "list-table-metadata"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/list-table-metadata@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["list_table_metadata"]

[match]
intent = "list table metadata in a database"

[[execute]]
id = "list"
connector = "github://ALRubinger/aileron-connector-athena"
op = "list_table_metadata"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "CatalogName"
type = "string"
description = "The data catalog that holds the database, for example \"AwsDataCatalog\". As returned by list-data-catalogs."
required = true

[[inputs]]
name = "DatabaseName"
type = "string"
description = "The database whose tables to list, as returned by list-databases in DatabaseList[].Name."
required = true

[[inputs]]
name = "Expression"
type = "string"
description = "Optional name filter expression. Restricts the listing to tables whose names match the expression, for example \"orders\" to find tables containing that substring."
required = false

[[inputs]]
name = "MaxResults"
type = "integer"
description = "Optional maximum number of tables to return in one page. Omit to take Athena's default page size."
required = false

[[inputs]]
name = "NextToken"
type = "string"
description = "Optional paging token from a previous ListTableMetadata response. Pass it to fetch the next page."
required = false
+++

# List Table Metadata in a Database

Lists table metadata in a database, optionally filtered by a name
expression. Returns the raw ListTableMetadata response, which carries
a TableMetadataList of table summaries with their columns and a
NextToken when more remain.

When it fires:
- "what tables are in the sales database"
- "list tables matching orders in this schema"
- "show me the tables I can query here"

Pair with:
- `list-databases` to find a database name to list tables from,
- `get-database` to read the database itself,
- `get-table-metadata` to read one table's full column detail.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
