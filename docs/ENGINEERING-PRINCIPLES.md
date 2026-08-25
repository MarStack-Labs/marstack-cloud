# Working in this repository

Read `docs/ARCHITECTURE.md` before adding a module. Read `docs/SECURITY.md` before touching
anything that parses input, spawns a process, writes to the host, or handles credentials.

## Language

Code, comments, documentation, commit messages, error strings, and log messages are **English**.

## Commits

- One small, working task per commit. `make check` must pass before committing.
- Conventional prefixes: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`, `build:`.
- Subject in the imperative mood, under 72 characters.
- The body explains **why**, and names any trap a future reader would otherwise hit.

## Principles

These are not decoration. Each one has a concrete test in this repo.

### KISS

Prefer the stdlib. `http.ServeMux` with `"GET /path"` patterns, `log/slog`, `database/sql`. A
dependency is justified by work it removes, not by convenience it adds — every dependency is
surface the agent carries onto customer baremetal.

### YAGNI

Do not build for a requirement that does not exist yet. No shared package before there is a second
consumer. No interface before there is a second implementation, with one exception: driver seams
where a second implementation is already a committed roadmap item (`VMM`, `Runtime`, `Pool`).

If you catch yourself writing an abstraction to be "ready", delete it and write the concrete thing.

### SoC

Layer responsibilities, and they do not blur:

| Layer | Does | Must not |
|---|---|---|
| handler | decode, validate shape, delegate, encode | contain rules or SQL |
| service | rules, orchestration, transactions | know that HTTP exists |
| repository | SQL | make decisions |
| kernel | mechanism | know any domain |

`kernel/fault` exists precisely so services can fail meaningfully without importing `net/http`.

### DRY, with a limit

Deduplicate mechanism — request decoding, error mapping, id generation, migration running.

Do **not** deduplicate two rules that merely look alike today. Two modules validating a name are
allowed to share `validate.Name`; two modules with coincidentally identical business rules are not
allowed to share a service.

### SOLID where it earns its place

- **S** — a module owns one resource. `system` owns health and version; it does not grow features.
- **O** — extend by adding a module or a driver, not by adding a branch to existing code. Adding a
  module must not require editing another module.
- **L** — a driver that cannot honour an operation declares it through capabilities up front. It
  must not accept the call and fail halfway. This is why `VMM` will carry `Caps()`.
- **I** — the consumer declares the interface it needs. `app.Module` lives in `app`, not in a
  package that modules import.
- **D** — modules depend on interfaces they declare; `app` injects the implementation.

## Definition of done

```sh
make check      # vet + test + security scans
```

- New behaviour has a test. New failure mode has a test.
- New input path validates before use, and rejects unknown fields.
- New host-touching code runs with the least privilege that works.
- Errors carry a `fault` kind; nothing internal leaks to a client.
- `docs/ARCHITECTURE.md` updated if a boundary or a module changed.

## Traps in this codebase

- SQLite runs with `SetMaxOpenConns(1)`. Do not raise it to "add parallelism" — SQLite has one
  writer, and a larger pool converts the queue into `SQLITE_BUSY` you then have to handle everywhere.
- `httpx.Decode` sets `DisallowUnknownFields` on purpose. A client sending a field we ignore is a
  client with wrong expectations, and silence is how privilege-escalation-by-typo happens.
- Middleware order in `app.buildRouter` is load-bearing. `RequestID` must stay first and `Recover`
  must stay outside anything that can panic.
- `internal/architecture/rules_test.go` is the architecture. If a boundary needs to change, change
  the rule deliberately in the same commit — do not work around it with an import alias.
- `staticcheck.conf` disables `S1016`, which suggests converting a wire struct straight into a
  domain struct. Wire types and domain types are kept separate on purpose so the HTTP shape can
  change without touching the service; taking that suggestion would silently couple them, and a
  wire-only field would then fail to compile. `ST1000` and `ST102x` are off because this repo does
  not write doc comments.
- Runtime packages are split by build tag. Portable constants live in the untagged file; anything
  using `syscall` or `filepath` layout helpers goes in a `_linux.go` file, with a stub for other
  platforms. Putting a Linux-only helper in an untagged file compiles on macOS but shows up as dead
  code, and the tests then only run on one platform.
