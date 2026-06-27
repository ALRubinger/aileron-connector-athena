+++
name = "get-table-metadata"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/get-table-metadata@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["get_table_metadata"]

[match]
intent = "look up one table's columns and metadata"

[[execute]]
id = "fetch"
connector = "github://ALRubinger/aileron-connector-athena"
op = "get_table_metadata"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "catalog_name"
type = "string"
description = "The data catalog that holds the table, for example \"AwsDataCatalog\". As returned by list-data-catalogs."
required = true

[[inputs]]
name = "database_name"
type = "string"
description = "The database that holds the table, as returned by list-databases in DatabaseList[].Name."
required = true

[[inputs]]
name = "table_name"
type = "string"
description = "The table to look up, as returned by list-table-metadata in TableMetadataList[].Name."
required = true
+++

# Look Up One Table's Metadata

Returns metadata for one table. Returns the raw GetTableMetadata
response, which carries the TableMetadata object with the column list,
partition keys, table type, and parameters.

When it fires:
- "what columns does the orders table have"
- "describe the events table"
- "what are the partition keys on this table"

Pair with:
- `list-table-metadata` to find a table name in a database,
- `get-database` to read the database that holds it,
- `start-query-execution` to run a SELECT once the schema is known.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
