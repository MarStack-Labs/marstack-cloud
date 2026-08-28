# Architecture

`marstack-cloud` is a **modular monolith**: one binary, one process, one database — split into
modules with boundaries that are enforced by tests rather than by convention.

It is not a set of microservices, and that is a deliberate choice. A cloud control plane needs
transactions that span placement, addressing, and storage. Splitting those across services buys
independent deploys nobody asked for and pays for it in distributed transactions.

## Layers

```
cmd/marstack            entry point, nothing else

internal/cli            command surface (cobra), flag parsing, process lifecycle
internal/app            composition root: owns config, builds modules, wires the router
internal/platform/*     the modules — one vertical slice each
internal/store          SQLite, migration runner, transaction helpers
internal/kernel/*        cross-cutting primitives with no domain knowledge
internal/version        build metadata, injected by -ldflags
```

## Dependency rules

Enforced by `internal/architecture/rules_test.go`. Breaking one fails `make test`.

| Rule | Reason |
|---|---|
| `kernel/*` must not import `platform/*`, `app`, or `store` | primitives stay reusable and free of domain knowledge |
| `platform/x` must not import `platform/y` | modules stay independently understandable and testable |
| `platform/*` must not import `app` | the composition root depends on modules, never the reverse |
| `store` must not import `platform/*` or `app` | persistence stays infrastructure |
| `cmd/*` may only import `internal/cli` | the entry point holds no logic |

When two modules genuinely need to talk, the caller declares the interface it needs and `app`
injects the implementation. No shared package is created before there is a second consumer.

`scheduler` is the worked example. It needs ready nodes from `node` and placement writes on
`instance`, but importing either would break the rule. Instead it declares `NodeSource` and
`InstanceSource` in its own package, in its own types, and `internal/app/adapters.go` converts:

```
scheduler  ──declares──▶  NodeSource, InstanceSource
app        ──implements─▶  nodeSource{*node.Module}, instanceSource{*instance.Module}
```

The modules never learn that a scheduler exists, and the scheduler never learns which modules
answer it. Both are testable with fakes, and the only place that knows the whole graph is the
composition root.

## Background workers

Not every component is a module. `scheduler` has no routes and no migrations, so forcing it into
the `Module` shape would be a lie. It is a plain worker with `Run(ctx)`, started by `app.Run`
alongside the HTTP server and stopped by the same context.

## A module

Each directory under `internal/platform/` is one module. It owns its data, its HTTP routes, and
its migrations. `app` sees only this shape:

```go
type Module interface {
	Name() string
	Migrations() []store.Migration
	Routes(mux *http.ServeMux)
}
```

The interface lives in `app` — the consumer — not in a package modules import. Modules satisfy it
structurally, so a module never imports the thing that wires it.

Internally a module is expected to grow into:

```
internal/platform/instance/
	module.go      wiring, routes, migrations
	handler.go     HTTP: decode, validate, delegate, encode
	service.go     rules and orchestration; the only place decisions are made
	repository.go  SQL, and nothing else
	types.go       the module's own types
```

Handlers do not write SQL. Repositories do not make decisions. Services do not know HTTP exists.

## Migrations

Migrations belong to modules, not to a global folder. `store.Migration` carries the owning module
name and an index, and `schema_migrations` records the pair. Two consequences:

- adding a module never renumbers another module's migrations
- each migration runs in its own transaction, so a failure leaves no partial schema and no record

## Errors

`kernel/fault` is the error taxonomy: `Invalid`, `NotFound`, `Conflict`, `Unauthenticated`,
`Forbidden`, `Unavailable`, `Internal`. It knows nothing about HTTP.

`kernel/httpx` maps a `fault.Kind` to a status code at the edge. Services return faults; only the
HTTP layer decides what a fault looks like on the wire. A `5xx` never carries the underlying error
to the client — it is logged with the request id instead.

## Request pipeline

```
RequestID → Recover → AccessLog → SecureHeaders → Timeout → auditTrail → authenticate
          → mux → module handler
```

Ordering matters: `RequestID` runs first so every later layer can log it, and `Recover` wraps
everything after it so a panic in any handler becomes a logged `500` rather than a dropped
connection. `auditTrail` sits outside `authenticate` so a refused request is still recorded.

## Tenancy

Every token belongs to exactly one project, and a project is the unit a resource is owned by.
Scoping is not a role check: a request may only see rows carrying its own `project_id`, whatever
the caller's role. Roles decide *which kinds of operation* a token may perform, not *which project*
it can reach — so an operator who needs another project mints a token there.

`kernel/scope` carries the caller's project, token and role in the request context.
`authenticate` puts it there; module handlers read it and pass the project down to their service as
an ordinary argument. Services never read the context, so they stay testable without HTTP.

The role vocabulary lives in `platform/token`; the policy of which role reaches which route lives in
`app` as three allow-lists — `nodePaths`, `memberPaths`, and admin, which is everything. Allow-lists
rather than deny-lists: a route added without a policy entry fails closed with a `403` instead of
being silently reachable.

The trail carries the project too, and reading it is scoped the same way as everything else: an
admin sees its own project's entries plus the ones that belong to no project at all. Those are
infrastructure traffic and refusals where the caller never proved who they were - hiding them from
every project would hide them from everyone.

Two routes used to serve both an agent and an operator: `GET /v1/images` and
`GET /v1/firewalls`. Scoping them to the caller's project would have blinded every agent, since a
node token carries no project worth honouring. They are now split along the existing
`/v1/nodes/{id}/...` convention: the node path answers with the whole catalogue, the bare path
answers with the caller's project. One path, one audience.

Modules never learn about roles. `/v1/projects` is administrative in its entirety, so the project
module has no role check inside it — the composition root simply keeps members off those routes.

## Quotas

The quota module owns the limits and nothing else. It cannot see instances or
volumes, so it declares what it needs — `Usage.InProject` — and `app` implements it by adding up
the footprints the instance and volume modules report. In the other direction those two modules
declare their own narrow admission interface (`AdmitInstance`, `AdmitVolume`) taking plain numbers
rather than a shared claim type, because a shared type would mean importing each other.

Enforcement therefore lives at create time in the module that owns the resource, and the arithmetic
lives in one place. The cost is that the read and the write are not one transaction: two
simultaneous creates can both pass. That is stated in the README rather than papered over, because
the fix would be a cross-module transaction and the boundary is worth more than the last percent of
strictness here.

## A balancer that follows a service

Two modules now have to agree on one set of instances, and there were three ways to arrange it: the
service writes backends into the balancer, the balancer reads members from the service, or the
composition root syncs them. The middle one won for the reason the event module already taught -
derive, do not sync. A synced copy has an owner, an ordering, and a window where it is wrong; a
derived view has none of those.

So `balancer` declares `Services` with one method and resolves membership on every read. The cost is
that the resolution has to be everywhere: the four read paths plus both loops inside `reportHealth`,
where a missed call does not fail loudly but drops a node's probe report as coming from a
non-member. That is the sharpest edge in this change and it is called out in `docs/ENGINEERING-PRINCIPLES.md`.

The dependency runs one way only. `service` does not know balancers exist, which is what keeps the
module boundary honest and is also why deleting a service leaves its balancer serving nothing
instead of being refused: the alternative is a referential check that only a mutual dependency could
enforce. The balancer keeps naming the service it followed so the empty set explains itself.

## Services

A service is the first module that creates another module's resource. That shape needed a decision:
either instances grow an owner column so a service can query its own, or the service keeps its own
membership table. The second won, because the first changes the shape of the instance module for the
benefit of a newer one, and because the loop has to reconcile membership against reality every pass
either way - a replica deleted directly is gone whatever the schema says.

So `service` declares `Workloads` with three methods - create, delete, and a batch liveness check -
and `app` implements it over `instance.Module`. That is also where quota enforcement comes from for
free: replicas are created through the same path an operator uses, so a service cannot mint
workloads a project is not allowed to have. When it is refused, the service records why and keeps
what it already made rather than failing the pass.

The loop lives in the module, in the same shape as backup schedules: `Run` on a ticker, `Reconcile`
callable directly so tests drive it a pass at a time rather than sleeping.

Two limits in the loop are deliberate. It creates at most a few replicas per pass, so a service
asked for thirty does not hand the scheduler thirty placements at once. And it deduplicates the
blocked reason and clears it on any pass that reaches the target, because a service stuck behind a
quota is a condition that repeats - the same lesson the scheduler taught about `instance.stranded`.

## Events

`audit` was already there and does not answer the question. It records requests: actor, method,
path, status. The things an operator actually chases - a workload restarting in a loop, a backend
leaving a balancer - have no request behind them, so they were only ever in a log file on a node.

The module that fixes that had two possible shapes. Either producers post events, or the control
plane derives them. Deriving won: the control plane already stores every observed-state transition
and every health verdict, so an event is a difference between two rows it already had. That gives
one writer instead of one per node, makes dedup a local comparison rather than distributed
agreement, and means an event cannot be a claim a node makes about itself.

The interface lives in `kernel/events` rather than being declared five times over. A timestamped
record with a subject and a severity is mechanism, not domain, which is the same argument that put
the keyring in `kernel/sealed`. `Record` returns nothing: recording is a side effect of an
operation and must never be able to fail it.

The hard part is not storage, it is deciding what counts as a transition. Live verification caught
the first answer being wrong - an agent restart forgets its restart counters, re-reports every
instance it adopts with the count reset, and the naive "did any field change" test turned one agent
restart into a dozen rows claiming workloads had come up. A transition is now a changed observed
state or a rising restart count, and nothing else.

Retention is a ring, pruned in the same shape `audit` uses. That is a deliberate limit rather than
a missing feature: this exists to answer a question now, and anything needing real retention should
be shipped out to something built for it.

## Balancing

`forward` and `balancer` both hand out ports on a node, and their spaces overlap: a published port
is claimed on one node, a balancer's listen port on all of them. Neither may import the other, so
each declares the question it needs answered - `Balancers.ListenPortTaken` and
`Ports.NodePortTaken` - and `app` wires both directions to the other module. This is the rule about
consumer-declared interfaces earning its keep: the dependency is genuinely mutual, and the only
place that can see both is the composition root.

The node side stays one authority. Balancers are not a second nftables table at a second priority;
the agent merges them into the same `[]workload.Publish` that published ports already travel in, and
`ApplyForwards` renders one table. `Publish` gained `Targets` and `Algorithm`, and the renderer takes
the map branch when `Targets` is set. A second table would have meant two writers racing over the
same `dport`, discovered only in production.

Health has two layers. The cheap one is borrowed: the control plane already knows each instance's
observed state, so a balancer with no check treats running as up. The real one is opt-in, and it
answers the question the cheap one cannot - whether the application is serving.

The prober lives in the agent, on the node that holds the instance. That placement follows the seam
already there: the agent reports observed state for its own instances, so it reports probe verdicts
the same way, and there is exactly one prober per backend with nobody to disagree with. The cost is
named in the README: a partition between one node and a backend on another is invisible.

The threshold state machine is in the agent too, because the counters belong next to the probes and
reporting a settled verdict keeps the control plane free of per-tick state. That trade has a
consequence - an agent restart loses the counters - which is why the control plane keeps the last
verdict under a staleness grace instead of requiring a fresh report on every read. The same shape as
node heartbeats, for the same reason.

One consequence worth stating: because the node needs the membership in order to probe it, the
node-facing view stopped filtering unhealthy backends. Health became a flag on each backend and the
agent filters when it renders. Filtering server-side would have been a deadlock - a backend that is
down would never be sent to a node, so it would never be probed, so it could never come back up.

## Data protection

Backup content is sealed with `kernel/sealed`, a streaming chunked AEAD over the standard library
alone - `crypto/aes`, `crypto/cipher`, `crypto/hkdf`. Streaming is the requirement that shapes it: a
volume does not fit in memory, so the whole file cannot be one `Seal` call, and a naive split into
independently sealed chunks would let an attacker truncate, reorder or splice them undetected. Each
frame therefore carries a nonce derived from its index under a key derived per file, and the final
frame is marked through its additional data so a stream that ends early fails.

Volume encryption uses the same operator key, one level up: the control plane mints a random key
per volume, seals it with `sealed.SealBytes`, and stores the wrapped blob. The node that holds the
volume fetches the unwrapped key from `GET /v1/nodes/{id}/volumes/{id}/key` and keeps it only in
tmpfs. So the threat this addresses is precisely a stolen node disk - not a compromised control
plane, which by construction can open everything it stores keys for.

The keyring moved to `kernel/sealed` once both the backup and volume modules needed it; a keyring is
a set of keys by fingerprint, which is mechanism, and neither module may import the other.

The keyring lives in the backup module and is injected from `app`, so the module decides nothing
about where keys come from. Each backup records the key that sealed it, which is what makes rotation
possible without rewriting history.


Two mechanisms with different failure domains, deliberately not merged:

| | Lives in | Survives | Does not survive |
|---|---|---|---|
| snapshot | the qcow2 file on the node | a bad write, a failed upgrade | losing the node |
| backup | the control plane's vault | losing the node | losing the control plane |

A backup is created as `pending` against the node that currently holds the volume. That node copies
the volume on its next reconcile and streams it to `PUT /v1/nodes/{id}/backups/{id}/content`, which
records the size and a sha256 of the bytes that actually arrived. Restoring never overwrites a live
volume: a new volume is created naming the backup, and the agent fetches the content before
anything starts.

Schedules live in the backup module rather than a module of their own. A schedule produces backups
and needs nothing the backup module does not already have, so splitting it would mean one of them
importing the other or a third adapter in `app` to carry creation across the boundary. It is one
resource’s lifecycle, not two resources.

The sweep is a ticker started from `App.Run` alongside the scheduler, and it marks a schedule fired
before creating the backup. Marking first means a failure loses one cycle; marking after would risk
a schedule that never advances and fires forever.

The runtime exposes this through `workload.VolumeArchiver`, a capability interface alongside
`VolumeKeeper`. A runtime that has no volumes simply does not implement it, and the agent skips it.

## Roadmap shape

Modules are added one at a time, each with its own migrations and routes:

```
system     health, version                        done
identity   projects, tokens, authorization        done
node       registry, join tokens, heartbeat
instance   the single instance object
image      registry pull, layer store
network    vpc, subnet, slice, ipam, firewall, dns
storage    disk, snapshot, pool
```

The agent will live in the same binary under a second command, sharing `kernel` and reusing the
same `fault` taxonomy across the wire.
