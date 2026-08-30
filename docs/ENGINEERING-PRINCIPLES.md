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
- A backup's `written.Size` is the plaintext length and the sealed file on disk is longer, by a
  salt and a tag per frame. The object store upload must take its Content-Length from the staged
  file, not from `written.Size` - getting that wrong fails as `ContentLength=X with Body length Y`
  only once a real S3 server sees it, which is exactly how it was found.
- With an object store, the node seals and uploads a backup itself using a per-backup key that the
  control plane mints and wraps with the operator key. The operator key never leaves the control
  plane; only the unwrapped key for one backup is handed out. Never "simplify" this by giving nodes
  the operator key.
- `vault.open` takes both `keyID` and `contentKey`. A backup written directly is sealed with the
  per-backup key, not the operator key, so reading it with `keyID` alone decrypts to garbage and
  fails partway through a 200 response. Backups written before this exist with an empty
  `content_key` and must keep working through the operator-key path.
- Sealing now happens in two places, the control-plane vault and the agent, which is why the
  seal-plus-digest-plus-limit logic lives in `kernel/sealed` as `SealMeasured`. Both must call it;
  a second implementation is how the two ends stop agreeing on a format.
- A directly uploaded backup's `size_bytes` and `checksum` are reported by the node, not measured by
  the control plane, which cannot see the bytes. The AEAD is what actually detects corruption on
  read; the recorded numbers are a report.
- `kernel/s3` signs with `UNSIGNED-PAYLOAD`. Signing the body means buffering the whole volume or
  implementing chunked signing, and the backup is already sealed, so its AEAD is what detects
  tampering. Do not "improve" this into a full payload hash without noticing the memory cost.
- The object store is checked with a HEAD on the bucket during `app.New`. A wrong endpoint stops
  the control plane at start rather than at 3am when a schedule fires.
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
- The webhook module walks the event table from a persisted cursor rather than being called when an
  event is recorded. `events.Recorder.Record` must never wait on HTTP or fail, and a cursor is what
  makes fan-out exactly-once across a restart. Do not "simplify" it into a direct call.
- A fresh cursor is seeded to the newest event id, not to zero. Otherwise the first pass after
  adding a webhook replays the whole ring at it.
- A webhook target is an address the caller chose, which makes the control plane a request
  forwarder if it is not guarded. The guard runs in `net.Dialer.Control` on the address actually
  dialled, not on the parsed URL: checking a hostname then connecting is a race a DNS answer wins.
  Redirects are not followed for the same reason.
- Loopback and link-local are refused; private ranges are allowed on purpose, because a private
  fleet lives there. The refusals are the addresses that mean something specific - the control plane
  itself, and a metadata service.
- `AllowLoopbackBecauseThisIsATest` exists because `httptest` binds loopback and the guard refuses
  it. The name is ugly so nobody reaches for it in production. The guard itself is unit tested
  separately, and one app test deliberately does not call it so the refusal stays covered.
- A webhook signing secret is stored recoverable, unlike an API token, because signing needs it.
  It is shown once on create and never served again, but a stolen database exposes it.
- A page cursor carries the ordering column **and** the row id. `created_at` alone is not a total
  order, and a duplicate timestamp on a page boundary returns the same row on two pages. The
  comparison is `order > ? OR (order = ? AND id > ?)`, written out rather than as a row value so it
  does not depend on the SQLite version. This is not theoretical: seven backups created in one test
  share a timestamp to the nanosecond, and removing the id half returns rows twice.
- Backups page newest first, so their comparison is `<` and their `ORDER BY` is `DESC` on both
  columns. Flipping one and not the other walks the list and never terminates.
- `next` is emitted only when a page came back exactly full. Never emitting it makes everything past
  the first page unreachable; emitting it whenever rows exist does not loop forever, it costs every
  client one extra empty request, which is why the walk tests assert that a short page carries no
  cursor rather than only that the walk ends.
- The cost of "exactly full" is one empty request when the total is a multiple of the limit. That
  is the honest price of not counting rows, and counting them means a second query per page.
- `page.Default` and `page.Max` live in `kernel/page` because a page size is mechanism, not a rule
  any one module owns. They were `instance.DefaultPage`, and copying that pair into every module
  that paginates is how the limits drift apart.
- `/v1/instances`, `/v1/volumes`, `/v1/backups` and `/v1/snapshots` are paged. The lists hanging
  off one volume - `/v1/volumes/{id}/backups` and `/v1/volumes/{id}/snapshots` - are not, because
  retention bounds them; they return no cursor, so a client that follows `next` still works.
- Ordering by an RFC3339Nano string is lexicographic, which matches chronological order except for
  a timestamp landing on an exact whole second: `Z` sorts after `.`, so `10:00:00Z` compares greater
  than `10:00:00.5Z`. Fixing it means rewriting every stored timestamp, and a mixed-format column
  would be worse than a one-in-a-billion row on the wrong page. Do not change the write format
  without migrating every row in the same commit.
- Any client that lists must follow `next`, and `walkPages` in `internal/cli/client.go` is the one
  loop that does it; a list view opts in by implementing `cursor()`. A client that reads one page
  and stops is silently showing a partial answer, which is worse than no paging.
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
- Usage history is downsampled on write into one-minute buckets, one UPSERT per report. Do not
  "simplify" it into an append-only table: a node reporting every ten seconds with twenty instances
  writes about 180k rows a day, and the read then has to aggregate on every request.
- A bucket keeps `samples`, `cpu_sum` and `cpu_peak` separately so both the average and the peak are
  real. Peak is the number that answers a capacity question, and it cannot be recovered from an
  average, so `MAX` in the upsert is load bearing.
- Memory is carried in MiB everywhere, and an idle container really does use about 120 KiB, so it
  reads 0 MiB. That is truncation in the unit, not a broken sampler: `container.Sample` reads
  `memory.current` correctly and a busy container reports 99% cpu. Do not "fix" it by rounding up,
  which replaces an accurate zero with an invented one - change the unit or leave it.
- `GET /v1/usage` is scoped to the caller's project and `GET /v1/usage/nodes` is administrative.
  They were one unscoped route, which let any member read every project's instance load and the
  node ids behind it. Splitting them follows the same precedent as images and firewalls: one path,
  one audience. Do not merge them back for convenience.
- The usage module cannot see projects on its own, so it declares `Workloads.IDsIn` and filters what
  it stores against the caller's project. A sample whose instance is not in the project is dropped
  rather than hidden by the handler, so nothing downstream can accidentally serve it.
- Cordon and drain are state on the node, not actions: `schedulable` and `draining` are set by the
  API and the scheduler makes them true on its next pass. That is what makes a drain idempotent and
  survivable across a control-plane restart, and it is why the drain call returns immediately.
- `updateOnRegister` deliberately does not touch `schedulable` or `draining`. A cordoned node whose
  agent restarts stays cordoned; sending work back to a machine somebody is working on because its
  agent came back would be the worst possible time to do it.
- A drain only finishes when nothing movable is left, and only containers are movable. A node with
  a vm on it stays `draining` forever, on purpose - the alternative is reporting a drain complete
  while a workload is still there. `instance.drain_blocked` names each one, once per drain.
- `emptyDraining` reuses `StrandedOn`, which is also what fencing uses. The two differ only in which
  nodes they ask about: unreachable ones versus deliberately draining ones. Keep the movable rule in
  one place so a change cannot apply to one path and not the other.
- Anything the scheduler does runs again on the next pass, so an event emitted there needs to be
  keyed on a transition, not on a condition. `instance.placed` is safe because a placed instance
  stops being pending; `instance.stranded` is not, which is why the scheduler keeps a set of the
  instances it has already reported and clears it when they stop being stranded. A held placement
  emits nothing at all for the same reason - the reason is already on the instance.
- `kernel/events` holds the `Entry` and `Recorder` only. Storage lives in `platform/event`, and
  producers depend on the kernel interface so five modules do not each declare an identical one -
  the same reasoning that moved the keyring into `kernel/sealed`.
- A balancer with `service_id` set takes its backends from the service on every read; its
  `balancer_backends` rows are unused. `service.membership` is what fills them, and it must run on
  every path that touches `Backends` - reads, the node view, and both loops inside `reportHealth`.
  Miss the last one and a node's probe report is dropped as "not a member", silently, because the
  membership map was built from an empty table.
- The dependency is one way: `balancer` reads `service`, and `service` knows nothing about
  balancers. That is why deleting a service leaves the balancer serving nothing rather than being
  refused, and why membership is derived on read rather than synced by whichever loop ran last.
- Backends of a service-backed balancer cannot be added or removed by hand, and naming both a
  service and instances at create is refused. Both are the same rule: one owner for the set.
- A service owns its membership in `service_members`; instances carry no owner column. That means
  the reconcile loop must treat its own table as a guess and ask `Workloads.Alive` every pass -
  an instance deleted directly is gone, and the member row is stale until the next pass proves it.
- Replica names are the service name plus a random suffix, never an ordinal. `pool-1` collides with
  an instance somebody created by hand, and a service that cannot name its next replica is a
  service stuck forever with no way out but renaming the workload.
- Scaling down removes the newest members first. The list from `membersOf` is ordered by
  `created_at`, so the loop walks it backwards; changing that ordering silently starts killing the
  replica that has been serving longest.
- `growTo` creates at most `MaxCreatePerPass` replicas per pass on purpose. Without it a service
  asked for thirty hands the scheduler thirty placements in one tick, and every one of them is a
  quota check plus an address allocation.
- A create refused mid-grow is not an error: the service records why it is stuck and keeps what it
  already has. Returning an error there would abandon the replicas it did manage to make.
- `service.blocked` is deduplicated on the reason string, and `clearBlocked` runs on every pass that
  reaches the target. Without both, a service stuck behind a quota writes an event every ten
  seconds forever - the same trap as `instance.stranded`.
- Deleting a service deletes its replicas first and refuses to delete the service if any replica
  will not go. Half a deletion leaves workloads nothing owns and nothing will clean up.
- A node never sets its own labels, and there is no field for them on register. Labels say what an
  operator decided about a machine, so a node that could assert them could pull work to itself by
  claiming a label a selector asks for. This is the same reason `updateOnRegister` leaves
  `schedulable` and `draining` alone, and there is a test that a node token gets 403.
- `PUT /v1/nodes/{id}/labels` replaces the whole set rather than merging. Merging leaves no way to
  remove a label without inventing a delete route and a null-means-delete convention, and makes the
  call non-idempotent. The transaction deletes then inserts for the same reason - an upsert would
  silently turn the replace back into a merge, which is a mutation test in `label_test.go`.
- `?label=k=v` repeats to mean and, never or. An or filter reads the same and answers a different
  question, so if one is ever wanted it needs its own syntax rather than a flag on this one.
- A node selector filters candidates before `bestFor` rather than scoring inside it. A selector is a
  requirement, not a preference: folding it into the sort would let a heavily labelled node lose to
  a cheaper one that does not match at all.
- An unmatched selector holds the instance and records why. It must not fall back to any node - a
  workload asking for a gpu quietly placed on a machine without one is worse than one that waits and
  says so. Both halves are mutation tested.
- The hold is a reason, not a state, so labelling a node later gets the instance placed on the next
  pass with no retry logic. Verified live: an instance held on `disk=tape` was placed the moment a
  node was given that label.
- A selector is stored as JSON in one column, the same shape as `ssh_keys` and `command`. A join
  table would be the right answer if anything queried instances by selector; nothing does, and the
  scheduler already holds every candidate in memory.
- An instance environment is refused outright when the control plane has no sealing key, rather than
  stored in the clear with a warning. Env is where credentials go, the platform has a way to protect
  them, and a warning nobody reads is not protection. The refusal names `--backup-key-file`. This is
  safe to be strict about because env is new - nothing shipped depends on the plaintext path.
- `Instance` deliberately has **no** plaintext env field. Only `EnvSealed`, `EnvKeyID` and
  `EnvNames` are on the struct, and the values exist only inside `service.envOf`. That is not
  tidiness: it makes leaking env into the operator response fail to compile rather than fail a test.
- `env_names` is stored separately in the clear on purpose. Names are not secret, the operator view
  needs them on every list, and keeping them out of the sealed blob means a control plane missing
  the key can still list instances instead of failing every read.
- `GET /v1/instances` never carries values and `GET /v1/nodes/{id}/instances` does, which is why the
  node route has its own `nodeResponse` rather than sharing `toResponse`. Same precedent as usage
  and images: one path, one audience.
- For a container the env is merged over the image's own, replacing rather than appending - two
  entries for one name leaves which wins to the exec implementation. The merge is sorted so a
  restart with the same input produces the same list and does not look like a change.
- For a vm the env is written by cloud-init to `/etc/marstack/environment` at 0600, base64 encoded
  so arbitrary bytes survive YAML. A whole machine has no single process to hand an environment to,
  so it is a file the guest may source, not a process environment. That file and the seed ISO carry
  the values in the clear on the node, exactly as the console password already does.
- Config files follow env exactly: sealed with the same key, paths kept in the clear in
  `file_paths`, content served only to the node. `SealKeyID` is one column for both - an instance is
  sealed with one key or none, and two key columns would let them drift into a state no rotation
  path handles.
- `dropFiles` writes through `os.OpenRoot` on the rootfs, not `filepath.Join`. An image is untrusted
  input: ship one whose `/etc` is a symlink to `/` and a joined path writes the caller's config
  straight onto the host. There is a Linux test that builds exactly that image, and swapping
  `OpenRoot` for a join makes it fail.
- A file path must be absolute and already clean. Accepting `/etc/../x` would mean the path an
  operator reads back in `file_paths` is not the path the node writes, and two files may not name
  the same path because which one wins would be undefined.
- The container's files are written before the cgroup is created and long before init runs, so a
  workload never observes a half-populated config directory.
- The rate limiter sits **in front of** `authenticate`, not behind it. Behind it, every bogus token
  costs a hash plus a lookup on the one SQLite connection before anything says no, which is a denial
  of service the limiter is supposed to stop. It therefore keys on the bearer secret's hash rather
  than the token id, which it cannot know yet.
- The key is `sha256(secret)[:8]`, never the secret. The limiter keeps its keys in memory and the
  map would otherwise be a place a raw credential lives for five minutes.
- `MaxTracked` and the idle sweep are the point of the whole file. A limiter that allocates a bucket
  per attacker-chosen key **is** the denial of service; past the cap, callers share one bucket
  instead of growing the map, and `Overflowed` says when that started. Removing the cap is a
  mutation test.
- `/healthz` is exempt. Throttling the health check makes a busy platform look like a dead one to
  whatever is watching it, which is the worst possible moment to be wrong.
- Buckets are per caller, not global, so one hot loop cannot take the platform down for everybody -
  also a mutation test. Agents are unaffected in practice because a node's steady traffic is a few
  requests a second against a default of 50.
- `New` respects the burst it is given even below the rate. An earlier version quietly raised burst
  to the rate, which made `--rate-burst` a suggestion rather than a setting.
- Every test app builds with `RatePerSecond: &unlimited`. `waitForPlacement` polls every 10ms for
  three seconds, which is 300 requests against a default of 50 a second, and the first thing the
  limiter did was make an unrelated cordon test fail. Tests about placement must not silently become
  tests about throttling; `ratelimit_test.go` is where the limit is exercised on purpose.
- Resize is an optional interface the agent type-asserts, not a method on `Runtime`. Only the
  container runtime can honour it while a workload runs; adding it to `Runtime` would make three
  drivers accept a call they cannot fulfil, which is exactly what the L rule forbids.
- The agent reapplies the size on **every** reconcile pass while a container runs, rather than on a
  change. It has no memory of the last size, the cgroup write is idempotent and cheap, and a pass
  that skipped it would leave a resize unapplied after an agent restart.
- A vm keeps the size it booted with. Nothing here hot-plugs cpu or memory into a guest, and the
  CLI says so on the spot rather than letting an operator assume it took effect.
- A resize claims only the **growth** against the quota, never the absolute size. Claiming the
  absolute size double-counts what the instance already holds and refuses a resize that fits;
  claiming nothing lets a project grow past its limit one resize at a time. Both are mutation tests.
- A balancer with a certificate is **not** an nftables rule. Nothing in nftables terminates TLS, so
  the agent binds the listen port itself and `applyForwards` deliberately leaves that balancer out
  of the published set. Emitting both would put a userspace listener and a dnat rule on one port,
  and which wins is undefined - the same trap as two rules matching one `dport`.
- The two modes differ in one way an operator will notice: the userspace listener answers traffic
  that originates on the node, and the nftables rule does not, because prerouting is not on the
  local output path. That is a property of dnat, not a bug, and it means a local curl is not a
  valid check of a plain balancer.
- `tlsproxy` restarts a listener only when the port or the certificate changes; a membership change
  swaps an atomic pointer. Restarting on membership would drop every live connection every time a
  replica came or went, which is exactly when connections matter.
- The certificate and key are sealed as a **PEM bundle**, not as a JSON struct with a `private_key`
  field. gosec flags the latter as a marshalled secret and it is right to: PEM is already a
  self-describing sequence of blocks, so the invented envelope bought nothing and cost a warning
  that would have had to be silenced.
- `tls.X509KeyPair` runs when the certificate is attached, so a key that does not match its
  certificate is refused there rather than at 3am when a connection arrives. There is a mutation
  test that drops the pair check.
- A certificate is refused outright when the control plane has no sealing key, same as env: handing
  a private key to every node out of a database it cannot protect is not a quiet default.
- `nics.instance_id` used to be the primary key, which made one NIC per instance a property of the
  schema. Migrations 12-15 rebuild the table because SQLite cannot drop a primary key in place; the
  copy sets `device = 0` for every existing row, so nothing that already ran changes behaviour.
- Device 0 is **the** interface everywhere else: dns, forwards, balancers and firewalls all keep
  reading `NICOf`, which is now `ORDER BY device LIMIT 1`. That containment is deliberate - the
  alternative is answering "which address is the instance's address" in six modules.
- Only device 0 gets a default route. Two default routes make egress depend on kernel tie-breaking,
  which changes under you; the extras get an on-link route to their own subnet and nothing else.
- `hostName` and `peerName` keep their old names for device 0 and add `.N` after that, so an
  upgrade does not recreate the veth of every running container. `maxIfName` still applies, so the
  base name is truncated to make room for the suffix rather than producing a name the kernel
  refuses.
- Three places had to learn about devices or extra NICs would break silently: the link sweeper
  (`Keep`) would delete `eth1`'s veth on the next pass, `Detach` would leak it, and the anti-spoof
  filter is keyed on interface name, so an unlisted device gets **no** rule rather than a wrong one.
  Verified live that both NICs carry their own drop rules.
- `netdev.MaxDevices` and `network.MaxNICs` are the same number in two packages on purpose. The
  agent's sweeper cannot import a platform module, and the alternative is a kernel package holding
  one constant that only these two use.
- `interfacesByInstance` returns a slice per instance. It used to be `map[string]*NetworkConfig`,
  where a second NIC silently overwrote the first - the map key is the instance, not the interface.
- Extra NICs are attached by the container and qemu runtimes. Cloud Hypervisor and Firecracker each
  still need a second netdev plus guest configuration, so a microvm or sandbox asking for two
  networks gets both addresses allocated and one interface.
- A vm gets one tap and one `-netdev`/`-device` pair per interface, with device 0 keeping the tap
  name it always had. Reusing one mac across two `virtio-net-pci` devices is a bridge loop, so
  `macOf` reads the mac of that device rather than of the primary.
- In `network-config`, only device 0 carries the default route, the nameserver and the search
  domain. Two default routes leave egress to kernel tie-breaking and two resolvers leave name
  lookups to the guest.
- An extra interface needs a **link-scope route to its own gateway**, because the gateway sits
  outside the /26 slice the guest is given. Without it the guest answers arp on that interface and
  nothing else, which looks exactly like an interface that was never configured. This is the same
  route `netdev.Attach` adds for containers with `ip route add <gateway> dev ethN scope link`.
- Do not write that route as `to: <address>/<prefix>`. The guest address has host bits set, netplan
  refuses the file, and **every** interface including eth0 is then left unconfigured - a wrong
  route on eth1 takes the whole guest off the network. Found live; both forms are now tested.
- A network is IPv4 **or** IPv6, never both. Dual stack means every address question in the platform
  gets two answers, and dns, forwards, balancers and firewalls all ask it; one family per network
  keeps that question single-valued, and an instance can still sit on one of each through multi-NIC.
- IPv6 networks must be under `fc00::/7`. Handing instances a globally routable prefix this
  platform allocated for you is not a default anyone should get by typing a cidr.
- The slice is /26 for v4 and /64 for v6. A v6 subnet smaller than /64 breaks SLAAC and is the kind
  of thing that works until something on the guest side assumes the standard.
- Address arithmetic is byte-wise over `AsSlice()` so it works in both families. The old code used
  `uint32`, and `isBroadcast` reserved the last address of a slice - IPv6 has no broadcast, so
  reserving one there silently loses an address per slice.
- nftables rules must be rendered in the family of the address. `ip daddr <v6 address>` is a syntax
  error, and a ruleset that fails to load takes **every** instance's rules with it, not just the
  one that was wrong. The same applies to `icmp` versus `icmpv6` and to arp, which v6 does not have.
- Forwards are rendered into `table ip marstack_nat` and `table ip6 marstack_nat6`. One table holds
  one address type, and both are emitted even when empty so a rule cannot outlive the balancer that
  made it.
- A balancer whose backends are not all one family renders nothing. One nftables map holds one
  address type, so the alternative is a rule that will not load.
- `Resolver.Update` used to drop every address that was not v4, which is why an IPv6 instance
  answered NXDOMAIN with a perfectly good record in the control plane. It now keeps both and
  `answer` emits A or AAAA to match; a v4 address written as v6 is still dropped, because it would
  be answered as AAAA and no client asking for A would ever see it.

- Two pre-existing bugs surfaced while verifying IPv6, both in `EnsureEgress`:
  - The masquerade rule was rendered from the **gateway** (`10.0.0.1/16`) while nft stores the
    masked network (`10.0.0.0/16`), so the "is it already there" check never matched and a rule was
    appended on every call. A node found with 2924 duplicates in one chain. The rule is now rendered
    from `prefix.Masked()`, and there is a test that the gateway and network forms are identical.
  - Egress rules were only written when a workload **started**. An agent restart adopts running
    workloads without calling `Start`, so those networks lost their masquerade rule until something
    happened to restart. `ApplyEgress` now runs every reconcile pass over every network on the node,
    which is what "the agent reconciles" was supposed to mean.
- `EnsureEgress` holds `egressMu` and rewrites the whole chain rather than appending. Check-then-act
  from several workload starts in one pass is what let duplicates in; the rewrite is also what heals
  a chain that already has them.
- `instanceNodeID` now fails on any status but 200. It used to unmarshal whatever came back into
  `{node_id}`, so a 429 read as "not placed yet" - the same misreading any client polling faster
  than the limit would make, and the reason the failure looked like a scheduler bug.

- Runtime packages are split by build tag. Portable constants live in the untagged file; anything
  using `syscall` or `filepath` layout helpers goes in a `_linux.go` file, with a stub for other
  platforms. Putting a Linux-only helper in an untagged file compiles on macOS but shows up as dead
  code, and the tests then only run on one platform.
