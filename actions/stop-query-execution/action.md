+++
name = "stop-query-execution"
# `version` and the `0.0.0-dev` markers in `source` and the
# `[[requires.connectors]]` block are placeholders. CI substitutes
# them with the real version (from the pushed tag) into a build copy
# of this manifest before signing and packing. Source stays template;
# only the published tarball carries the real version.
version = "0.0.0-dev"
source = "github://ALRubinger/aileron-connector-athena/actions/stop-query-execution@0.0.0-dev"

[[requires.connectors]]
name = "github://ALRubinger/aileron-connector-athena"
version = "0.0.0-dev"
# `hash` is the connector tarball's content-addressed identity per
# ADR-0002. CI substitutes this placeholder with the real hash at
# release time (see .github/workflows/release.yml). The committed
# source intentionally keeps the placeholder so each release runs the
# same substitution against an unchanged template.
hash = "sha256:bound-at-release"
capabilities = ["stop_query_execution"]

[match]
intent = "cancel a running Athena query"

[[execute]]
id = "stop"
connector = "github://ALRubinger/aileron-connector-athena"
op = "stop_query_execution"
idempotent = true

# Per-call approval gate. Cancelling a query is a state-changing write
# against a running execution, so the runtime pauses the call and asks
# the user to approve via the launch-comms channel before dispatching
# to Athena. On approval the connector runs. On denial the connector
# is never invoked and the runtime audit-logs the deny. This is the
# only gated action in the suite. See ADR-0009 (user channel, agent in
# trust path) for the rationale.
[approval]
required = true

[[inputs]]
name = "region"
type = "string"
description = "AWS region of the Athena endpoint, e.g. \"us-east-1\". Required, with no default. The region selects the AWS endpoint the connector dials (athena.<region>.amazonaws.com) and, via the outbound host, the binding the host signs with. Any region whose host is in the connector manifest's [capabilities.network] allow-list is valid. A region the allow-list does not list fails closed as capability_denied at the network boundary."
required = true

[[inputs]]
name = "query_execution_id"
type = "string"
description = "The query execution id to cancel, as returned by start-query-execution."
required = true
+++

# Cancel a Running Athena Query

Cancels a query that is still QUEUED or RUNNING. Returns the raw
StopQueryExecution response. This is the one write action in the
suite. Every other action reads.

When it fires:
- "cancel query execution abc-123"
- "stop that query, it's scanning too much data"
- "kill the running query in this work group"

This action changes server-side state, so it is gated on per-call user
approval. When the agent calls `stop_query_execution`, the Aileron
runtime pauses the call and asks the user to approve via the
launch-comms channel, which is the CLI prompt or the webapp approvals
surface. Athena is not contacted until approval is granted. On denial
the call returns an error to the agent and the runtime records the
deny in the audit log. See ADR-0009 (user channel, agent in trust
path) for the rationale.

This action is declared `idempotent = true`. Stopping a query that has
already stopped is a no-op against the same execution id, so the
runtime's retry layer may safely re-issue it on a transient failure.

The connector runs in the Aileron WASM sandbox with
`[capabilities.network]` allow-listing the regional Athena hosts. Each
request is marked `credential = "aws_sigv4"` and signed host-side with
SigV4 at the network boundary. The connector never sees the secret
access key. See ADR-0005 (sandbox and credential mediation) and
ADR-0009 (user channel, agent in trust path) in the Aileron docs.
