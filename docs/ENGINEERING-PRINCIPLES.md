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
- Heartbeats and reconcile are separate goroutines in the agent, and must stay that way. Liveness is
  measured in seconds while a single reconcile step can take tens of seconds (image pull, VM stop),
  so sharing a loop makes a busy node look dead.
- `internal/runtime/container` re-executes `/proc/self/exe` and takes over the process through an
  init hook keyed on `MARSTACK_INIT_CONFIG`. Do not move that entrypoint back into the CLI: any
  binary linking the package, including test binaries, has to be able to act as container init, or
  the child re-runs whatever the parent was doing.
- Authentication is a middleware in `app`, not in `kernel/httpx` and not in the modules. The
  mechanism (parse a bearer token, hash it, compare) belongs to `platform/token`; the policy of
  which role may call which path belongs to the composition root, where every route is already
  visible. A module that checked roles itself would have to know about roles.
- `app.nodePaths` and `app.memberPaths` are the whole authorisation policy for those roles. Adding
  a route without adding it there makes callers fail with 403 at runtime, not at compile time. They
  are allow-lists, not deny-lists, so a forgotten entry fails closed - keep it that way.
- Scoping is by project, not by role. A handler reads `scope.From(ctx).ProjectID` and passes it to
  its service as an argument; services never read the context. A module that branched on role would
  have to know the role vocabulary, which is why `/v1/projects` is administrative in its entirety
  rather than role-checked inside the project module.
- The literal `'prj-default'` appears in a migration in several modules. It is the default project
  id that existing rows are backfilled against, and it must match `project.DefaultID`. It is
  duplicated on purpose: importing the project module from every other module to share a constant
  would break the rule that modules do not depend on each other. Never change the literal - a
  shipped migration is history.
- Every route that takes a volume accepts a name or an id, so anything stored must be the id that
  `Volumes.Source` resolves - never the string the caller typed. The agent matches its volumes by
  id; a name stored in `backups.volume_id` is a backup no node ever picks up, and it fails silently
  because the pending guard then blocks every retry.
- A backup carries the volume key as a sealed envelope. The backup module never opens it and never
  serves it, and the volume module adopts it on restore instead of minting a new one. Minting a new
  key for a restore produces a disk that nothing can open, and the failure looks like a corrupt
  backup rather than a wrong key.
- Exporting an encrypted volume creates the target first and converts with `-n`, because
  `--target-image-opts` requires it. That also means no `-c`, so those exports are uncompressed.
- An encrypted volume's key reaches the node over the API and is written to `/run/marstack/keys`,
  which must stay tmpfs. Writing it under the state dir or the runtime root would put the key on
  the same disk as the ciphertext and make the whole feature pointless.
- QEMU is started with `-qmp` as well as `-monitor none`; those are different channels and both
  are wanted. Removing the QMP socket silently turns hot-plug back into "wait for a restart", with
  no error anywhere - `SyncDisks` returns zero when the socket is absent, on purpose, because a
  guest started by an older build genuinely has none.
- Hot-plug adds disks and never removes them. A guest cannot be asked to release a disk it is
  writing to, and unplugging behind its back loses whatever was in flight. Detach stays deferred.
- Every `qemu-img` call against an encrypted volume needs `--object secret` plus `--image-opts`
  with `encrypt.key-secret`; the plain `qemu-img verb file` form cannot open one. `imageArgs` in
  `runtime/qemu/snapshots_linux.go` builds both shapes - use it rather than adding a third.
- `kernel/sealed` writes 64 KiB AEAD frames with a per-file salt, and the final frame is marked
  through its additional data. That marking is the only thing that makes truncation detectable, so
  never "simplify" the AAD away. `Seal` never emits a full-size final frame, which is why a reader
  can treat a short read as the end.
- A backup's `size_bytes` and `checksum` describe the plaintext, not the file on disk. The download
  handler sets Content-Length from that, and an agent sizes the restored disk from it - taking the
  ciphertext length instead writes the wrong thing.
- Backup retention runs in two places on purpose: when a backup becomes ready, and on every sweep.
  The first is when the count actually changes; the second is what makes a lowered `keep` take
  effect without waiting for the next copy. Removing either leaves a real gap.
- A snapshot and a backup protect against different things. A snapshot is internal to the qcow2
  file on the node, so it survives a bad write but dies with the disk. A backup is a copy held by
  the control plane. Do not "simplify" one into the other; the whole point is that they fail
  independently.
- A quota check is not a transaction. `admit` reads usage then the caller writes, so two
  simultaneous creates can both pass a limit. Do not "fix" this by reaching into another module's
  tables from the quota service; the honest options are a cross-module transaction or the current
  documented looseness.
- There is no flag to skip certificate verification, and there must not be one. An encrypted
  channel to a server nobody authenticated proves nothing, and such a flag is always found by
  somebody in a hurry. Point `--ca-file` at the authority instead.
- `httpx.Timeout` wraps `http.TimeoutHandler`, which buffers the whole response in memory and
  cuts it off at the deadline. That is right for an API call and fatal for moving a volume, so
  `app.streamingPaths` names the routes that skip it. Adding a route that streams bytes without
  adding it there gives a truncated body at 30 seconds, and a memory spike on the way.
- `statusRecorder` implements `Unwrap`. Without it `http.NewResponseController` cannot reach the
  real connection, and setting a deadline silently returns `ErrNotSupported`. Any new
  `ResponseWriter` wrapper needs the same method.
- The agent has two HTTP clients. `http` carries a 15 second timeout, which is right for reconcile
  calls and wrong for moving a volume. Backup content goes through `transfer`, whose timeout is
  measured in minutes. Sending a body through the wrong one truncates it at 15 seconds.
- `qemu-img` cannot read a volume that a running qemu holds. Snapshots, backups and restores all
  skip a volume whose instance the runtime reports as running, and that check must stay in front of
  every one of them.
- Anything `qemu-img` or `xorrisofs` creates lands at 0644 through the default umask, and some of
  those files are a guest's whole disk or its cloud-init seed with the console password in it.
  Every such path goes through `restrict` or an explicit `os.Chmod(..., 0o600)` after creation.
  Adding a new file the agent creates by running a tool means adding that too.
- `make check` on macOS does not compile a single `_linux.go` file, so it will happily pass on code
  that does not build on a node. `make cross` is part of `check` for that reason - it builds and
  vets for linux and builds for darwin. A green local run without it means nothing for the agent.
- A balancer claims its listen port on **every** node, while a published port claims one on a
  single node. That makes their port spaces overlap, so `forward` and `balancer` each declare an
  interface for the other and `app` wires both directions. Dropping either check does not fail
  loudly - it produces two nftables rules matching the same `dport` in the same chain, and which
  one wins is undefined.
- Balanced traffic is marked with `ct mark 0x1` and masqueraded in postrouting. Without it a
  backend on another node replies straight to the client from an address the client never dialled,
  and the connection never establishes - the symptom is that exactly the cross-node share of
  requests times out while local ones succeed. Single-target forwards are deliberately left
  unmarked so they keep seeing the real client address; marking them would hide it for nothing.
- `GET /v1/nodes/{id}/balancers` returns the same set to every node. The agent is what skips
  unhealthy backends, and a balancer left with no target programs no rule at all rather than an
  empty map, which `nft` would reject.
- A backend is "up" when its instance is observed running, unless the balancer carries a check.
  Plain VM liveness cannot see a process that is running but wedged, which is what `--check` exists
  for; leaving the default in place is a choice, not an oversight.
- The node holding an instance is the only one that may report health for it, and `reportHealth`
  enforces that through `Members.NodeID`. Without the check any node token could mark another
  node's backends up or down, which is either a blackhole or a denial of service.
- `GET /v1/nodes/{id}/balancers` deliberately returns every backend, healthy or not. The node needs
  the full membership to know what to probe, and it is the agent that filters when it builds the
  nftables map. Dropping unhealthy backends server-side again would silently stop all probing,
  because a backend that is down would never be probed to come back up.
- A checked backend with no report yet reads `unknown` and takes no traffic, and a report older than
  `balancer.HealthGrace` goes back to `unknown`. Both are on purpose: trusting an unprobed or
  unrefreshed backend defeats the check exactly when it matters. Do not "fix" the startup blip by
  defaulting to healthy.
- Removing a backend deletes its `balancer_health` row, and the agent drops its counters in
  `forgetProbes`. Skipping either lets a re-added backend inherit an old verdict and take traffic
  without being probed.
- Probe thresholds live in the agent's memory, so an agent restart re-earns every verdict from
  scratch. The control-plane grace is what stops that from looking like an outage; it must stay
  comfortably larger than the reconcile interval.
- `audit` records calls somebody made; `event` records what the platform did with no caller.
  They are not the same table and neither replaces the other. Audit is admin-only because it is
  governance; events are member-readable because they are about the caller's own workloads.
- Nothing posts an event. Events are derived in the control plane from transitions it already
  stores, which is why there is no agent API for them. Adding one would let a node assert history
  it cannot prove, and would need dedup logic on the receiving side.
- A transition is a changed observed state or a *rising* restart count. A falling one means an
  agent restarted and forgot its in-memory counters, not that anything happened to the workload -
  treating that as an event makes every agent restart write one row per adopted instance.
- `event.list` fails closed: an empty project id returns nothing rather than everything. Every
  token carries a project, so this cannot happen over HTTP, and the day it can, silence is the
  safe answer.
- An event with no project id is invisible to every caller, because reads are strictly
  project-scoped. Producers must always set `ProjectID`; there is deliberately no "global" feed,
  since serving one would mean the module knowing which callers are admins.
- `events.Recorder.Record` returns no error on purpose. Recording must never fail the operation
  that caused it, so a write that fails is logged by the event module and dropped.
- `kernel/events` holds the `Entry` and `Recorder` only. Storage lives in `platform/event`, and
  producers depend on the kernel interface so five modules do not each declare an identical one -
  the same reasoning that moved the keyring into `kernel/sealed`.
- Runtime packages are split by build tag. Portable constants live in the untagged file; anything
  using `syscall` or `filepath` layout helpers goes in a `_linux.go` file, with a stub for other
  platforms. Putting a Linux-only helper in an untagged file compiles on macOS but shows up as dead
  code, and the tests then only run on one platform.
