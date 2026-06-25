+++
name = "get-database"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/get-database@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["get_database"]

[match]
intent = "look up one database in a Glue data catalog"

[[execute]]
id = "fetch"
connector = "github://ALRubinger/aileron-connector-athena"
op = "get_database"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. It must equal the region pinned in the connector manifest's [capabilities.network] host and [capabilities.credential].region. A region the allow-list does not list fails closed as capability_denied at the network boundary. A region that disagrees with the credential yields a SigV4 signature Athena rejects."
required = true

[[inputs]]
name = "CatalogName"
type = "string"
description = "The data catalog that holds the database, for example \"AwsDataCatalog\". As returned by list-data-catalogs."
required = true

[[inputs]]
name = "DatabaseName"
type = "string"
description = "The database to look up, as returned by list-databases in DatabaseList[].Name."
required = true
+++

# Look Up One Database

Returns metadata for one database in a data catalog. Returns the raw
GetDatabase response, which carries the Database object with its name,
description, and parameters.

When it fires:
- "describe the sales database"
- "what's the description of this schema"
- "show me the parameters on the events database"

Pair with:
- `list-databases` to find a database name in a catalog,
- `list-table-metadata` to list the tables inside this database,
- `get-table-metadata` to read one table in detail.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` pinned to the one regional Athena
host. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
