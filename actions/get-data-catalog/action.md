+++
name = "get-data-catalog"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/get-data-catalog@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["get_data_catalog"]

[match]
intent = "look up one Athena data catalog's configuration"

[[execute]]
id = "fetch"
connector = "github://ALRubinger/aileron-connector-athena"
op = "get_data_catalog"
idempotent = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "name"
type = "string"
description = "The data catalog to look up, for example \"AwsDataCatalog\". As returned by list-data-catalogs in DataCatalogsSummary[].CatalogName."
required = true

[[inputs]]
name = "work_group"
type = "string"
description = "Optional work group name. Scopes the lookup to the catalog as seen from a specific Athena work group."
required = false
+++

# Look Up One Data Catalog

Returns the configuration of one Athena data catalog. Returns the raw
GetDataCatalog response, which carries the DataCatalog object with its
type (for example GLUE, HIVE, or LAMBDA) and its connection parameters.

When it fires:
- "describe the AwsDataCatalog catalog"
- "what type is this data catalog"
- "show me the parameters on this catalog"

Pair with:
- `list-data-catalogs` to find a catalog name,
- `list-databases` to list the databases inside this catalog,
- `get-database` to read one database in detail.

This is a read-only operation. The connector runs in the Aileron WASM
sandbox with `[capabilities.network]` allow-listing the regional Athena
hosts. Each request is marked `credential = "aws_sigv4"` and signed
host-side with SigV4 at the network boundary. The connector never sees
the secret access key. See ADR-0005 (sandbox and credential mediation)
in the Aileron docs.
