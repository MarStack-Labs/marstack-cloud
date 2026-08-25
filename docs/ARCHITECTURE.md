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
RequestID → Recover → AccessLog → SecureHeaders → Timeout → mux → module handler
```

Ordering matters: `RequestID` runs first so every later layer can log it, and `Recover` wraps
everything after it so a panic in any handler becomes a logged `500` rather than a dropped
connection.

## Roadmap shape

Modules are added one at a time, each with its own migrations and routes:

```
system     health, version                        done
identity   projects, tokens, authorization
node       registry, join tokens, heartbeat
instance   the single instance object
image      registry pull, layer store
network    vpc, subnet, slice, ipam, firewall, dns
storage    disk, snapshot, pool
```

The agent will live in the same binary under a second command, sharing `kernel` and reusing the
same `fault` taxonomy across the wire.
