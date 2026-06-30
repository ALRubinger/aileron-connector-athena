# aileron-connector-athena

Read-path AWS Athena connector for the Aileron action runtime
(see [github.com/ALRubinger/aileron](https://github.com/ALRubinger/aileron)).
It runs and inspects Athena queries, fetches query results, and browses
Glue Data Catalog metadata. The connector runs inside the Aileron WASM
sandbox and never holds AWS credentials. The host signs every outbound
request with SigV4 (`aws_sigv4`) at the network boundary, so the secret
access key stays vault-only.

## Why this is safe

Read-only access here is enforced by two independent layers. Either one
alone would block a write. Both run on every query.

### Layer 1 (primary): the sealed read-only IAM principal

The host signs every Athena request as a single IAM principal whose
policy grants read actions only. That principal cannot run DDL, cannot
write to source tables, and cannot mutate Athena, Glue, or S3 source
data, regardless of what SQL text is submitted. This is the load-bearing
guarantee. A submitted `INSERT` or `CREATE TABLE` fails at AWS because
the signing principal lacks the permission, even if it somehow reached
Athena.

### Layer 2 (defense in depth): the in-connector SQL gate

`buildStartQueryExecution` in `connector/helpers.go` runs
`validateReadOnlySQL` before any host or Athena call. It accepts only
statements whose first keyword is one of `SELECT`, `WITH`, `SHOW`,
`DESCRIBE`, `DESC`, `EXPLAIN`, or `VALUES`. It rejects `EXPLAIN ANALYZE`
(which executes the statement) and rejects stacked statements (more than
one semicolon-separated statement), while allowing a single trailing
semicolon. A non-read query fails fast with a clear connector error
rather than a remote IAM denial. This gate is a scanner, not a full SQL
parser. It is the second layer, never the only one. The IAM principal in
Layer 1 remains the backstop in every case.

### The `stop_query_execution` approval gate

`stop_query_execution` is the one action in the suite that contacts
Athena to change server-side state. It cancels a running query. Because
its `action.md` declares `[approval] required = true` (the manifest has
no `effect =` key — this README's write/gated categorization is editorial,
derived from the presence of that `[approval]` block),
the runtime pauses the call and asks the user to approve via the
launch-comms channel (the CLI prompt or the webapp approvals surface)
before dispatching to Athena. On denial the connector is never invoked
and the runtime audit-logs the deny. This approval is a consequence of
the declared write effect. It is not a configuration toggle. See ADR-0009
in the Aileron docs.

## Read-only IAM policy to seal the principal

Attach the policy below to the IAM principal whose static access key the
binding signs with. The policy is per-principal and region-agnostic. The
Athena and Glue actions it grants are not region-pinned, so one principal
works across every region you bind. The bound key's region governs SigV4
signing, not the policy. The policy grants read access to Athena and Glue
plus the S3 access Athena needs to read source data and write result
files. There are NO write, DDL, or data-mutation permissions in it. The
principal cannot create, alter, or drop databases, tables, work groups,
or data catalogs. It cannot `INSERT`, `UNLOAD`, or run CTAS into any
table.

Replace `your-source-data-bucket` with the bucket(s) holding the tables
you query, and `your-athena-results-bucket/prefix` with the Athena
result location (see "Region, work group, and output location" below).

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AthenaRead",
      "Effect": "Allow",
      "Action": [
        "athena:StartQueryExecution",
        "athena:GetQueryExecution",
        "athena:GetQueryResults",
        "athena:StopQueryExecution",
        "athena:ListQueryExecutions",
        "athena:BatchGetQueryExecution",
        "athena:ListDatabases",
        "athena:GetDatabase",
        "athena:ListTableMetadata",
        "athena:GetTableMetadata",
        "athena:ListWorkGroups",
        "athena:GetWorkGroup",
        "athena:ListDataCatalogs",
        "athena:GetDataCatalog"
      ],
      "Resource": "*"
    },
    {
      "Sid": "GlueCatalogRead",
      "Effect": "Allow",
      "Action": [
        "glue:GetDatabase",
        "glue:GetDatabases",
        "glue:GetTable",
        "glue:GetTables",
        "glue:GetPartition",
        "glue:GetPartitions"
      ],
      "Resource": "*"
    },
    {
      "Sid": "SourceDataRead",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:GetBucketLocation",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::your-source-data-bucket",
        "arn:aws:s3:::your-source-data-bucket/*"
      ]
    },
    {
      "Sid": "AthenaResultsLocation",
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:GetBucketLocation",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::your-athena-results-bucket",
        "arn:aws:s3:::your-athena-results-bucket/prefix/*"
      ]
    }
  ]
}
```

The lone `s3:PutObject` in this policy is scoped to the Athena results
location, not to source data. Athena writes the result file of a query
to its configured output location, and the connector reads that file
back through `get_query_results`. That `PutObject` exists solely so
Athena can deposit those result files. It is read-back-only output. It is
not a data-plane write to any source table. No statement in this policy
grants write access to source data or to the catalog.

`athena:StopQueryExecution` is the one Athena action in this policy that
changes server-side state. It only cancels a running query. It does not
write data. Its use is approval-gated at the runtime, as described above.

## Region, work group, and output location

`region` is a REQUIRED input arg on every op. There is no default region
and no build-time substitution. A missing `region` is a connector error
raised before any host call (`resolveRegion` / `requireString` in
`connector/helpers.go`).

Region is selected per call. The runtime `region` arg picks the AWS
endpoint the connector dials (`athena.<region>.amazonaws.com`). The host
parses that target region from the outbound host and resolves the
binding scoped to it, then signs with that binding's region and access
key id. One install serves many regions this way.

The `[capabilities.network]` allow-list in `connector/manifest.toml`
enumerates every region this connector supports. It lists one
`athena.<region>.amazonaws.com:443` host per region where Athena is
available in the AWS commercial partition. A region the allow-list does
not list is denied as `capability_denied` at the network boundary.

To serve a region, set up a binding for it with
`aileron binding setup --region <r> --access-key-id <id>` (see [Binding
setup](#binding-setup)). The bound key's region governs SigV4 signing.
The runtime `region` arg, the binding region, and the region of the work
group and S3 output location you query against must all agree for a
given call.

`work_group` and `result_configuration.OutputLocation` are optional inputs
to `start_query_execution`. A work group can pin its own result location
through its configuration, in which case you do not pass
`result_configuration.OutputLocation` per call. Whichever location applies,
the results S3 bucket must be in the same region as the Athena endpoint
you target, and that bucket is the one the `AthenaResultsLocation` policy
statement above scopes.

## Operations

Fifteen ops. Fourteen read. One (`stop_query_execution`) is an
approval-gated write. Fourteen are a single `POST` to the regional Athena
host with `Content-Type: application/x-amz-json-1.1` and an
`X-Amz-Target: AmazonAthena.<Action>` header selecting the operation. The
fifteenth, `run_query`, is a synchronous orchestration of three of those
calls in one op (see [Synchronous run_query for deterministic
runtimes](#synchronous-run_query-for-deterministic-runtimes)).

| Op | `X-Amz-Target` | Effect | Idempotency |
|---|---|---|---|
| `start_query_execution` | `AmazonAthena.StartQueryExecution` | read | idempotent |
| `get_query_execution` | `AmazonAthena.GetQueryExecution` | read | idempotent |
| `get_query_results` | `AmazonAthena.GetQueryResults` | read | idempotent |
| `run_query` | `StartQueryExecution` → `GetQueryExecution` → `GetQueryResults` | read | idempotent |
| `stop_query_execution` | `AmazonAthena.StopQueryExecution` | write (gated) | idempotent |
| `list_query_executions` | `AmazonAthena.ListQueryExecutions` | read | idempotent |
| `batch_get_query_execution` | `AmazonAthena.BatchGetQueryExecution` | read | idempotent |
| `list_databases` | `AmazonAthena.ListDatabases` | read | idempotent |
| `get_database` | `AmazonAthena.GetDatabase` | read | idempotent |
| `list_table_metadata` | `AmazonAthena.ListTableMetadata` | read | idempotent |
| `get_table_metadata` | `AmazonAthena.GetTableMetadata` | read | idempotent |
| `list_work_groups` | `AmazonAthena.ListWorkGroups` | read | idempotent |
| `get_work_group` | `AmazonAthena.GetWorkGroup` | read | idempotent |
| `list_data_catalogs` | `AmazonAthena.ListDataCatalogs` | read | idempotent |
| `get_data_catalog` | `AmazonAthena.GetDataCatalog` | read | idempotent |

`start_query_execution` declares `idempotent = true`: when the caller
omits `client_request_token`, the connector synthesizes a deterministic
token — the hex-encoded SHA-256 of the canonical request (query string
plus any execution context, result configuration, and work group) — so
two identical calls carry the same token and Athena collapses them onto a
single execution instead of starting a new one. (Athena requires a
non-null/non-empty token, and this connector hand-builds the request with
no AWS SDK to auto-generate one; the synthesized token also fixes the
out-of-the-box 400 INVALID_INPUT "clientRequestToken is null or empty"
failure.) The read ops are idempotent because re-issuing them returns the
same view without side effects. `stop_query_execution` is
`idempotent = true` because stopping an already-stopped execution is a
no-op against the same id. `run_query` declares `idempotent = true` for
the same reason as `start_query_execution`: it derives the same
deterministic token from an identical request, so a replay resolves to
the same execution rather than spawning a duplicate.

## Async start, poll, results flow

Athena query execution is asynchronous. The pattern is start, poll, then
read results.

1. `start_query_execution` submits the SQL and returns a
   `QueryExecutionId`. The SQL passes the read-only gate first. Optional
   `query_execution_context` (database and catalog), `result_configuration`
   (output location), and `work_group` are passed through when supplied.
2. `get_query_execution` reports the lifecycle state for that execution,
   keyed by the `query_execution_id` input. Poll it until the state
   reaches `SUCCEEDED`, `FAILED`, or `CANCELLED`. A `FAILED` state carries
   a reason in the response.
3. `get_query_results` pages the rows of a `SUCCEEDED` query, by
   `query_execution_id`, with `max_results` and `next_token` paging.
4. `stop_query_execution` cancels a query that is still `QUEUED` or
   `RUNNING`. This is the one approval-gated step.

## Synchronous run_query for deterministic runtimes

The start/poll/results flow above suits an LLM-in-the-loop caller that can
poll between calls. A deterministic Aileron Flight Plan cannot: its step
graph has no loop, conditional, or wait construct, and `get_query_results`
fails until the query has reached `SUCCEEDED`. `run_query` exists for that
caller. It internalizes the whole lifecycle in one synchronous op:

1. `StartQueryExecution` with the given SQL, after the same read-only gate
   (`validateReadOnlySQL`) the async path applies — a non-read query fails
   in-connector before any AWS call.
2. Poll `GetQueryExecution` every two seconds until the execution reaches
   a terminal state, bounded by an overall budget (`timeout_seconds`,
   default 180). A still-`QUEUED`/`RUNNING` query at the deadline fails
   with `connector_runtime_error`; a `FAILED`/`CANCELLED` execution fails
   with `external_api_error` carrying Athena's `StateChangeReason`.
3. On `SUCCEEDED`, page `GetQueryResults` to completion, concatenating the
   rows (the column header appears once, on the first page) and keeping
   `ResultSetMetadata` once.

The response is `{QueryExecutionId, ResultSet}`. `run_query` takes the
same `region` / `query_string` / `query_execution_context` /
`result_configuration` / `work_group` / `client_request_token` inputs as
`start_query_execution`, plus the optional `timeout_seconds`. It is
additive: the three async ops stay as-is for the agentic flow. It adds no
new network host or credential — every internal call goes through the same
signed Athena boundary with the same `aws_sigv4` credential.

## Binding setup

The end-to-end demo path installs the connector and an action, binds a
static AWS access key for the `aws_sigv4` credential kind, then launches
your agent:

```sh
# Install the connector and an action. Replace <version> with a tag
# from the releases page. The Aileron resolver requires a pinned
# version per ADR-0004. There is no `latest` channel.
aileron connector install github://ALRubinger/aileron-connector-athena@<version>
aileron action add github://ALRubinger/aileron-connector-athena/actions/start-query-execution@<version>

# Bind a static AWS access key for the aws_sigv4 credential kind. Pass
# the target region and the NON-secret access key id as flags. The
# setup prompts for the secret access key, which is stored vault-only
# and the connector never sees.
aileron binding setup github://ALRubinger/aileron-connector-athena@<version> \
  --region us-east-1 --access-key-id <access-key-id>

# Launch your agent. Aileron exposes the action via MCP.
aileron launch claude

# In the agent: "run SELECT count(*) FROM my_db.my_table in Athena"
# The LLM picks start_query_execution, Aileron executes it in the WASM
# sandbox with the bound credential after the SQL passes the read-only
# gate, and returns the QueryExecutionId to poll.
```

`aileron action add-suite github://ALRubinger/aileron-connector-athena/suite.toml@<version>`
installs the suite and connector together and auto-prompts for the
`aws_sigv4` binding when none exists, mirroring the OAuth sibling
connectors.

The binding carries the region and access key id; the manifest fixes only
the credential kind and service:

- `--region`: the AWS region this binding signs for. The host selects
  this binding when a call targets that region (see [Region, work group,
  and output location](#region-work-group-and-output-location)). Bind one
  region per binding. Add more bindings to serve more regions from a
  single install.
- `--access-key-id`: the AWS access key id. This is the NON-secret half
  of the key pair, the public identifier. It is safe to record. It lives
  on the binding alongside the region.
- The secret access key is prompted for and stored vault-only.
- `service` is `athena` and is fixed by the manifest, not the binding.

The secret access key is the only secret. It is stored vault-only and is
never present in source, in the manifest, or in the connector binary. The
host pairs it with the binding's `access_key_id` at signing time and
injects the `Authorization`, `X-Amz-Date`, and `X-Amz-Content-Sha256`
headers at the network boundary. The connector never sees it.

### Static keys only

This connector uses static long-lived access keys only. It does not use
session or temporary credentials. It never sends an
`X-Amz-Security-Token`. Temporary-credential support is deferred per
ADR-0019.

## Write and DDL: a future, separate connector

This connector is read-path only. Write and DDL access is out of scope by
design and will live in a SEPARATE future connector sealed to its own
write-capable IAM principal. Keeping the write path in a distinct
connector means this read-path binding can stay sealed to a principal
that physically cannot write.

As a result this connector ships no dead write surface. There is no CTAS,
`INSERT`, or `UNLOAD`. There is no `CREATE`, `ALTER`, or `DROP`. There is
no work group or data catalog CRUD, and no prepared-statement management.
The only state-changing action is `stop_query_execution`, which cancels a
running query and is approval-gated.

## Repo layout

```
aileron-connector-athena/
├── connector/
│   ├── main.go         # wasip1 source: host ABI, dispatch, signed Athena call
│   ├── helpers.go      # pure request builders + region + read-only SQL gate
│   ├── go.mod
│   └── manifest.toml   # capability declarations + aws_sigv4 credential config
├── actions/
│   ├── start-query-execution/action.md
│   ├── get-query-execution/action.md
│   ├── get-query-results/action.md
│   ├── run-query/action.md              # synchronous start->poll->results
│   ├── stop-query-execution/action.md   # the one approval-gated write
│   ├── list-query-executions/action.md
│   ├── batch-get-query-execution/action.md
│   ├── list-databases/action.md
│   ├── get-database/action.md
│   ├── list-table-metadata/action.md
│   ├── get-table-metadata/action.md
│   ├── list-work-groups/action.md
│   ├── get-work-group/action.md
│   ├── list-data-catalogs/action.md
│   └── get-data-catalog/action.md
├── suite.toml          # action suite manifest
├── keys/
│   └── publisher.pub   # ed25519 public key: add to trust this publisher
├── Taskfile.yml        # local build + test
└── .github/workflows/  # CI (ci.yml), live integration (integration-aws.yml),
                        # signed release on tag push (release.yml)
```

## Building locally

```sh
task build
```

Produces `connector.wasm` from `connector/main.go` (Go's native WASI
Preview 1 target, `GOOS=wasip1 GOARCH=wasm`, built `-trimpath
-ldflags="-s -w"`).

## Testing

Run everything in `task test`, or each layer independently.

### Unit tests: pure helpers, host platform

```sh
task test:unit
```

Runs `go test` against the request builders, region resolution, and the
read-only SQL gate in `connector/helpers.go`. That file has no build tag,
so `go test` exercises it on the host platform. The WASM-only entry point
in `connector/main.go` is excluded by its `//go:build wasip1` tag during
host builds. The run writes `coverage.out` for upload to Codecov in CI.

### wasip1 build smoke test

```sh
task test:wasip1
```

Confirms `connector/main.go` still compiles for the wasip1 target. Runs
as `GOOS=wasip1 GOARCH=wasm go build -o /dev/null .`.

`task test` runs both of the above.

### Live AWS integration

```sh
task test:integration:aws
```

Runs a live, real-AWS Athena round trip (`StartQueryExecution`, poll
`get_query_execution`, then `GetQueryResults`) against a throwaway,
read-only AWS account. It is gated behind the `athena_integration` build
tag and excluded from the default `task test` suite. It signs requests with
the `aws` CLI from your ambient credential chain, reusing the connector's
own request builders. A passing run proves the connector's wire format is
correct against live Athena and that the read-only IAM policy grants exactly
enough for the full flow.

This is the CLI-signed path. It does not exercise Aileron's host-side
`aws_sigv4` sealing. The sealed path is the [Binding setup](#binding-setup)
flow above. Use this test to validate the connector quickly without an
Aileron runtime. This test's region is independent of the manifest pin. The
pin described in
[Region, work group, and output location](#region-work-group-and-output-location)
governs the sealed install only.

The rest of this section is a start-to-finish guide for a new account.

#### What you do not need

Athena is serverless. There is no cluster, database, or instance to stand
up. Every account and region already has a default `primary` work group.
The default query is `SELECT 1`, which scans no data, so no table, Glue
catalog entry, or seed data is required.

#### Prerequisites

You need the `aws` CLI installed and logged in as an identity that can
create S3 buckets and IAM users. `aws login` (IAM Identity Center) or
`aws configure` (static keys) both work for this admin step. The read-only
identity used by the test is created for you by the setup task below.

#### Configure with environment variables

The setup tasks and the integration test share one environment-variable
namespace. Set the values once, then run the tasks with no arguments.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `ATHENA_INTEGRATION_REGION` | yes | none | AWS region for the bucket, work group, and test |
| `ATHENA_INTEGRATION_BUCKET` | yes | none | Globally unique S3 bucket name for query results |
| `ATHENA_INTEGRATION_IAM_USER` | no | `aileron-athena-readonly` | Name of the read-only IAM user |
| `ATHENA_INTEGRATION_WORKGROUP` | no | `primary` | Athena work group |
| `ATHENA_INTEGRATION_RESULTS_PREFIX` | no | `athena-results` | S3 key prefix for results |
| `ATHENA_INTEGRATION_POLICY_NAME` | no | `AthenaReadOnly` | Inline policy name on the user |
| `ATHENA_INTEGRATION_DELETE_BUCKET` | no | `false` | `aws:teardown` only; `true` also deletes the bucket |

The region, bucket, and work group must all agree. Pick any region your
account allows. S3 bucket names are globally unique across all of AWS, so
choose a name that is likely to be free.

The `ATHENA_INTEGRATION_*` values are configuration, not secrets, so they
are safe to keep in your shell profile.

#### Provision the AWS prerequisites

```sh
export ATHENA_INTEGRATION_REGION=us-east-2            # any region your account allows
export ATHENA_INTEGRATION_BUCKET=my-athena-results-1  # any globally unique name

task aws:setup
```

`task aws:setup` is idempotent and safe to re-run. It creates the S3 results
bucket with public access blocked, points the work group at
`s3://<bucket>/<prefix>/`, creates the read-only IAM user, and attaches an
inline policy scoped to Athena reads, Glue catalog reads, and the results
bucket. IAM is global, so the same user is reused if you switch regions. The
targets use only the `aws` CLI and Task's built-in shell, so they run the
same on Windows, macOS, and Linux.

#### Mint the read-only access key

```sh
task aws:setup:key
```

It prints the access key id and the secret access key once, tab separated.
Store them now. Aileron's `aws_sigv4` sealing requires a static long-lived
key, so this is also the key you bind in the [Binding setup](#binding-setup)
flow.

#### Run the round trip

```sh
export AWS_ACCESS_KEY_ID=<access key id from aws:setup:key>
export AWS_SECRET_ACCESS_KEY=<secret access key from aws:setup:key>

task test:integration:aws
```

A passing run looks like this:

```
--- PASS: TestIntegrationAthenaRoundTrip (2.22s)
round-trip OK: query <id> SUCCEEDED, GetQueryResults returned 2 row(s)
```

Signing with the read-only key, rather than your admin identity, is what
confirms the inline policy is sufficient. Keep the access key out of your
shell startup files. Export it for the test session, or store it in a named
`aws` profile, then unset it when you are done:

```sh
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
```

To run the test as your logged-in identity instead of the read-only key,
skip the two `AWS_*` exports and run the test directly. Override the query
with `ATHENA_INTEGRATION_QUERY`, which must pass the read-only SQL gate. See
`connector/integration_aws_test.go` for the full env contract.

#### Tear down

```sh
task aws:teardown
```

Deletes the read-only user's access keys, its inline policy, and the user.
It leaves the bucket in place by default. To also empty and delete the
bucket, set `ATHENA_INTEGRATION_DELETE_BUCKET=true` with
`ATHENA_INTEGRATION_BUCKET` set to the bucket name.

### Packing

```sh
task pack
```

Mirrors the release workflow's tarball build. Without `AILERON_SIGNING_KEY`
set, it skips the signature and is useful as an offline smoke test.

## Releasing

Releases are cut by pushing a `vX.Y.Z` tag. Versions stay in the `0.0.x`
range. The source manifests carry a `0.0.0-dev` placeholder in their
`version` fields. The release workflow substitutes the real version
(extracted from the pushed tag) into a build copy of the connector and
action manifests before hashing, signing, and packing. The committed
source intentionally keeps the placeholder so the publisher does not edit
version fields by hand on every release.

## Trusting this publisher

Aileron's install pipeline (ADR-0004) verifies every connector and action
download against the publisher's ed25519 public key in `keys/publisher.pub`.
To trust this publisher, extract the raw key bytes and add them to your
`~/.aileron/keyring.json` under the
`github://ALRubinger/aileron-connector-athena` authority. Without an entry
for this authority, `aileron connector install` fails closed. The full,
step-by-step trust instructions are in [`keys/README.md`](keys/README.md).

## License

Apache-2.0.
