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

## Data protection

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
