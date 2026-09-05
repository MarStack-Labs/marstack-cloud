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
- Every isolation attaches extra NICs now. The three VMM paths differ only in how an interface is
  declared: qemu repeats `-netdev`/`-device`, Cloud Hypervisor repeats `--net`, and Firecracker
  grows its `network-interfaces` array. All three take the mac of **that** device; one mac across
  two virtual NICs is a bridge loop, and it is a mutation test in each.
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
- The microvm guest init already had the right shape for one interface, including the link route to
  the gateway. It now loops over `MS_NICS` reading `MS_IP_n` and `MS_GW_n`, and only device 0 adds
  the default route. `MS_DNS` and `MS_SEARCH` stay singular and come from device 0 - two resolvers
  in one `resolv.conf` leaves which one answers to the guest.
- A microvm or sandbox interface can take a while to come up. A dual-NIC sandbox looked like it had
  a broken eth0 for a minute and then answered; before concluding a VMM path is broken, build the
  single-NIC control of the same isolation and give both the same time.
- A network has one range per **family**: `cidr` and optionally `cidr6` of the other family. The
  first is the primary and is what dns A records, published ports, balancers and firewalls read;
  the second is additional. That is the same containment as device 0 in multi-NIC - "the instance's
  address" stays single-valued, and dual stack is a second address rather than a second answer.
- The second range must be the other family. Two v4 ranges on one network would make "the address"
  ambiguous again for no gain, since a second v4 range is a second network.
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
- One name can now hold an address per family and `Resolver.Update` takes `map[string][]string`.
  `pick` chooses by query type, so an AAAA query against a v4-only name gets an empty answer rather
  than the v4 address - answering with the wrong family sends a client somewhere it cannot reach.
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
- A service template carries a node selector and passes it to every replica. Without it a service
  is the one workload kind that cannot be pinned, which is backwards: a replica set is exactly what
  you want on the nodes with the fast disks.
- The service module validates the selector itself rather than letting the instance module refuse
  it at replica-create time. A service that accepts a selector it can never use records `blocked`
  every pass forever and nothing tells the operator at the moment they typed it. This is the
  `validate.Name` case docs/ENGINEERING-PRINCIPLES.md allows - shared mechanism, not a shared rule.
- A forward and a balancer carry a `family`, defaulting to ipv4, which decides **which** of a dual
  stack instance's addresses is published. Without it the second address exists and nothing can
  reach it from outside, which is most of the point of having it.
- Asking for a family the instance does not have is refused rather than silently falling back to the
  other one. A rule pointing at an address that does not exist is a published port that answers
  nothing, and the operator has no way to tell.
- A balancer resolves **every** backend in its own family through `Member.AddressIn`. Resolving per
  backend would let a mixed set through, and one nftables map holds one address type - the whole
  rule then renders as nothing.
- `sealed.SealJSON` and `OpenJSON` live in the kernel because `instance` and `service` both seal a
  template's environment and neither may import the other. Same move as the keyring. They return
  `ErrNoKey` and `ErrKeyMissing` rather than a `fault`, so each module can phrase the refusal in
  terms of what it is about to do.
- A service seals its environment once and unseals it once per pass in `growTo`, not once per
  replica. The replicas of one service share one template, so the alternative is the same AEAD open
  repeated for every replica the pass creates.
- `applyGuards` builds `map[string][]string` and emits one guard per address. It used to be
  `map[string]string` keyed on the instance, so with multi-NIC the last interface **overwrote** the
  others and only one address of a guarded instance had any firewall rules at all - the rest were
  open to anything. Same shape as the `interfacesByInstance` bug fixed in the multi-NIC commit; a
  map keyed on the instance holding per-interface data is worth grepping for.
- The first connection to a freshly guarded IPv6 address can time out while neighbour discovery
  completes, then succeed. That is IPv6, not the firewall: probe three times before concluding a
  rule is wrong, and compare against an unguarded instance on the same network.
- `nc -z` returns the same exit code for "connection refused" and "timed out", so it cannot tell an
  allowed port from a dropped one. Use `curl --connect-timeout`, where 7 is refused and 28 is
  dropped. Half an hour was spent on a firewall that was working.
- A service seals its files with the same key id as its environment, in `EnvKeyID`. One key per
  service template; a second column would let the two drift into a state no rotation path handles,
  which is the same reasoning as `SealKeyID` on an instance.
- `tlsproxy` needed nothing for IPv6 backends: `net.JoinHostPort` brackets the address and
  `net.Listen(":port")` binds both families. There is a test for it anyway, because the next person
  to read `relay` will wonder.
- `internal/runtime/guest` holds what every runtime prepares **inside** a guest: writing config
  files confined to a rootfs, and merging a requested environment over the image's own. Three
  runtimes needed both and none may import another, which is the same reason the keyring moved to
  `kernel/sealed`. It is not build-tagged: `os.OpenRoot` and symlinks work everywhere, so the
  confinement test now runs on macOS too rather than only in Lima.
- `--env` was a silent no-op for microvm and sandbox. Their guest command script exported
  `config.Env` - the **image's** environment - and never merged in what the operator asked for. A
  flag that is accepted, validated, stored, sealed, and then ignored by the runtime is worse than
  one that is refused. Both runtimes call `guest.MergeEnv` now, and there is a test naming that.
- A microvm's files go into the staging directory before `mkfs.ext4`, so they are part of the image
  rather than written into a running guest. `debugfs -R "stat /etc/x"` reads them back out of the
  image without mounting it, which is how the mode was checked.
- Volume work - attach, backup, snapshot, restore - does not touch what an instance is reachable on,
  and `TestVolumeWorkLeavesEveryAddressAlone` records that as a property rather than a hope. It holds
  structurally: `volume` and `backup` cannot import `network`, so there is no path from a snapshot to
  a nic row. The test is a boundary marker, and the half that asserts "the same addresses before and
  after" cannot be mutation-tested for that reason - only the half that counts them can.
- A published port terminates TLS the same way a balancer does, and by the same rule: with a
  certificate it is a userspace listener and is left out of the nftables set, because a listener and
  a dnat rule on one port is undefined. `certs.Inspect`, `Bundle` and `Split` moved into the kernel
  once `forward` and `balancer` both needed them - neither may import the other.
- There are **two** rate limiters and they answer different questions. The one in front of
  `authenticate` keys on the bearer hash and exists so a flood of bogus tokens never reaches the one
  SQLite connection. The one behind it keys on the project and exists so one tenant cannot crowd out
  another. Moving the second in front of `authenticate` gives every request an empty project key and
  merges all tenants into one bucket, which is a mutation test.
- The per-caller default is tighter than the per-project one, so a single client hits the first limit
  and never sees the second. That is intended, and it means a default-config live run cannot tell
  the two apart - the project limiter is exercised in `projectrate_test.go` with explicit config.
- `keep` on a schedule is how many survive a **retention pass**, not how many files exist. Retention
  only counts snapshots that are `ready`, and a sweep both prunes and fires, so the set settles at
  `keep + 1`: the copy created after retention ran is still there. Verified live at 3 for `keep: 2`
  across five intervals. Backup schedules have the same shape.
- Retention only prunes what the schedule made, matched on `schedule_id`. A snapshot somebody took
  by hand is never counted and never cut, which is why `snapshots` carries that column at all.
- Adding a column to `snapshotColumns` is not enough: `snapshotsPageIn` writes its own
  `s.id, s.volume_id, ...` list because it joins volumes, and a mismatch there is a 500 on
  `/v1/snapshots` rather than a compile error. It caught me.
- A service template is versioned by a plain counter, and a replica remembers the revision it was
  made from. A rollout is then just a third case in the loop that already existed: retire one stale
  replica when the count is met, and let `growTo` make the replacement. No surge, no second
  capacity concept, and the `len(present)` vs `Replicas` invariant is untouched.
- The bound falls out of that shape rather than being enforced: retiring only happens when
  `len(present) == Replicas`, so once a replacement cannot be made the count drops below the target,
  the retire branch is never reached again, and **a broken revision costs one replica instead of the
  whole service**. Removing the `==` guard is a mutation test; so is ignoring `MaxReplacePerPass`.
- Scaling down orders stale replicas **last**, because `shrinkTo` removes from the end. Passing
  `staleFirst: true` there cuts the fresh replicas and leaves the rollout to redo the work - the two
  call sites want opposite orderings from the same partition.
- A single-replica service has an outage during a rollout, and that is not a new failure mode: the
  loop already replaces a lost replica by making a new one, so a service with no redundancy never
  had any.
- A revision is the whole template, not a patch. Env and files are sealed and never served back, so
  the CLI **cannot** carry them forward - `checkCarried` refuses rather than publishing a revision
  that silently drops them, and `--drop` is how you say you meant it.
- Rolling back is publishing the old template again, which is a new revision number. Two replicas
  labelled "revision 1" that were made before and after a rollback are not the same thing, so the
  counter never goes backwards.
- Every new route is invisible to non-admin tokens until it is added to `memberPaths` in
  `internal/app/auth.go`. The allow-list is deny-by-default, so a member gets a 403 on a route that
  exists and works - the tenancy test caught it, which is the point of having one per feature.
- An unknown `network_id` used to be accepted at instance create while an unknown `firewall_id` was
  refused, so a typo produced an instance that was never going to get an address. Both are checked
  now, and so is every entry in `extra_networks` - the second interface was the same gap as the
  first, twice over.
- A network in another project answers exactly the way one that does not exist answers. A distinct
  code or status there would confirm the id exists, which is the whole reason `existsIn` compares
  the project rather than letting a not-found bubble up.
- The duplicate-network guard compared each extra against `NetworkID` only, so `["net-a","net-a"]`
  with a different primary went through. It keeps a set now.
- A service template's ids are checked at create **and** at publish, so a bad revision is refused at
  the door instead of retiring a working replica to learn the same thing. `Registry` is one
  consumer-declared interface (`ExistsIn`) that both the network and firewall modules already
  satisfy - the service package cannot import either.
- Do not key a map on an interface value to pair a registry with its ids: iteration order is random,
  so a template with two bad ids names a different one each run. A slice of pairs keeps the message
  deterministic.
- Not everything can be checked up front, and the rollout test now leans on one that cannot: a
  revision asking for more vCPU than the **project** may hold. A quota is about the project rather
  than the template, so it can only fail at create - which is what still makes the retire-one-and-
  stop bound worth having.
- **`127.0.0.1:8088` is the UI proxy, and it injects its own admin token.** An `Authorization`
  header sent there is ignored, so every request lands as admin in `prj-default`. Any test about
  tenancy or roles has to go to the control plane on `:7443` with the token in hand; on the proxy it
  measures nothing. This cost me a wrong conclusion about a tenancy leak that did not exist.
- `Alive` asked whether an instance **exists**, so a replica the node had given up on stayed a
  member forever and the service reported the full count while one replica served nothing. The
  interface is `StatesOf` now and returns the observed state; a member missing from the map is gone,
  and one that is `failed` is replaced.
- Only `failed` is reaped. `stopped` is a decision a person made and gets reported rather than
  deleted; `pending` has not had its chance yet. Widening the rule to "anything not running" reaps
  replicas that are merely waiting to be placed, which breaks the rollout tests too.
- Replacing without a bound is a crashloop the platform runs on your behalf. `Reaped` counts
  replacements and the service gives up at `MaxReapAttempts` with the reason on it, leaving the
  survivors alone.
- **The tally may only be cleared when every replica is actually `running`.** Clearing it whenever
  nothing is `failed` right now looks identical in a unit test and is wrong live: the pass after a
  replacement sees the new replica `pending`, which is not a recovery, so the tally reset every
  other pass and the limit was never reached. A test only catches this with two replicas - one
  healthy, one whose replacements keep failing - because with one replica the loop always exits
  through the grow branch and never reaches the settle.
- **Publishing a revision clears the tally**, because that is the operator saying the template is
  fixed. Without it a service that gave up can never be recovered: the reap gate returns before the
  rollout can retire anything, so a correct new revision changes nothing and the only way out is
  deleting the service. Found live, after the block worked exactly as designed and then would not
  let go.
- Member state is read live on every path rather than stored on the row. A stored copy is up to one
  reconcile interval stale, which means `service get` right after a crash shows `running` - the
  worst possible moment to be wrong. The instance module stays the only source of truth.
- The webhook tests assumed two pumps were enough to deliver. Under a full `./...` run they are not
  always, and the app package got heavier, so `pumpFor` pumps until the sink has what is expected
  and fails if it never arrives. An absence still uses a fixed count - you cannot wait for nothing.
- The agent has **no HTTP server**: everything is the agent pulling from the control plane. So a
  log cannot be fetched on demand - the node ships new output on every reconcile pass, the same
  shape as `PUT /v1/nodes/{id}/usage`. That is why the last line can be a few seconds behind, and
  why the answer is a bounded recent window rather than an archive.
- Four separate bounds, and each one is a mutation test: at most `maxShipBytes` read per pass and
  `maxShipLines` per report (agent), `MaxLinesPerReport` per request and `MaxLineBytes` per line
  (service), `MaxLinesPerInstance` kept (repository), `MaxTail` returned (read). Drop any one and a
  workload printing in a loop either floods a request or fills the control plane's disk.
- **The offset only moves after the report lands.** Advancing it when the lines are read loses
  everything a failed request was carrying. Deleting the offset on failure is the opposite mistake -
  the whole file is resent as duplicate output.
- A file shorter than the stored offset was rotated, so the offset resets to 0. Without that, an
  offset past the end reads nothing ever again.
- A partial trailing line is held back until its newline arrives, or shipped anyway once it passes
  `maxHeldLineSize` - otherwise a workload printing without newlines stalls its own log forever.
- **`UseClock(cfg.Now)` with a nil clock stores nil and panics on the first call**, which the
  recover middleware turns into a 500 with no clue in it. The setter refuses nil now. The older
  modules guard at the call site instead, which is one `if` away from the same bug.
- The trim is per instance. Dropping `WHERE instance_id = ?` from it lets one chatty workload erase
  another's output, and no app-level test can see it because `tail` caps the answer long before the
  store does - that one is only visible from the repository.
- A job needed a signal for **finished on purpose**, and the platform had four observed states with
  no way to tell a clean exit from a kill. The exit code now travels with the status report and is
  kept on the instance, so success is `stopped` **and** exit 0. Reading `stopped` alone records work
  that never happened.
- The exit code is only sent when the phase was `exited` and the workload was not restarted, so a
  restart never leaves a stale code behind. `sameExit` compares pointers by value, or every pass
  reports a status that has not changed.
- **A job's run always carries `restart never`, whatever the template says.** The instance default
  is `always`, so a run left to it is restarted by its node every time it exits and the job never
  finishes. No unit test caught removing this - the node is faked in tests - so there is one that
  reads the created instance back and checks the policy.
- Two runs of one job never overlap. A turn that comes due while the previous run is still going is
  skipped and recorded as `job.run_skipped`, not stacked - same rule as the snapshot schedule.
- A finished run's workload is deleted. Without it a nightly job leaves one exited container behind
  every night until the quota stops it.
- `retries` is retries, not attempts: `attempt <= retries` gives one try plus that many more.
- A job template is one JSON column rather than twenty. It is never queried by field, and the
  service module's twenty-column version is the reason its insert has twenty-eight placeholders to
  keep in step.
- Adding a column to instance `columns` also means adding a `?` to the INSERT. It is the same trap
  as `snapshotsPageIn`, and it shows up as `instance_already_placed` rather than a SQL error.
- The seal/open helpers are copied per module on purpose. They are thin wrappers over
  `sealed.SealJSON`, and their whole content is the fault message - "every replica would carry it"
  against "every run would carry it". Lifting them into the kernel flattens exactly the part that
  tells an operator what to do.
- Autoscaling is its own module because `service` may not import `usage`. It declares `Services`
  and `Load` as consumer interfaces and the composition root joins them, the same shape as
  `scheduler`. The routes still hang off `/v1/services/{id}/autoscale` - owning a route is not
  owning the resource.
- The formula is the easy part. Seven guards are the feature, and each is a mutation test:
  a **deadband** so a rounding error does not scale anything, a **cooldown** so a change is given
  time to take effect, a **step limit** so one reading cannot ask for a cluster, **min/max**, a
  **warmup** so a replica that has just started is not counted, a **settled** check so it never
  decides from a replica count that is not true yet, and a **refusal on stale or missing load**.
- **Refuse rather than average what you have.** A replica whose sample is missing is not zero load;
  leaving it out makes the answer about the replicas that happened to report. Both cases stop the
  pass and say so.
- The warmup guard is the subtle one: a replica that just started reads as idle, so averaging it in
  scales *down* the service that is busy - it kills what it just made.
- Every pass records why nothing happened, not only what changed. A service that will not scale and
  says nothing is the worst version of this feature.
- A policy whose service is gone is deleted on the pass that notices, or it is swept over forever.
- A mutation that fails to compile is not a mutation. `if false` on a line that binds `held` makes
  the package stop building, and a grep for `--- FAIL` shows nothing - which reads exactly like a
  guard that no test covers. Grep for the build failure too, or write the mutation so it compiles.
- Layers were cached and the manifest was not, so a node could not start a container it held every
  byte of. The rule now is **registry first, cache only when the registry fails** - a tag moves, and
  a cache that answers first never notices. Answering from the cache before asking is a mutation
  test.
- The fallback is only taken when every blob the cached manifest names is present. Without that
  check the pull announces it is using what the node holds and then fails fetching a layer that is
  not there: the same failure, one step later, with a misleading line in between. The test counts
  registry calls, because both versions fail and only the call count tells them apart.
- The manifest fetch has its own 20 second budget, separate from the two minutes a layer download
  may need. The pull runs inside the reconcile pass, so before this an unreachable registry stalled
  every workload on the node two minutes at a time.
- **HTTP keep-alive defeats an `/etc/hosts` block.** Blocking the registry and watching a running
  agent pull anyway proves nothing: the connection was already open to the real address, so no name
  was ever resolved. Restart the agent after blocking, or the experiment measures nothing. This
  looked exactly like the fix not working.
- `openPaths` was doing two jobs: skip authentication **and** skip the rate limiter. The login route
  needs the first and needs the second more than anything else on the server, so they are separate
  maps now - `openPaths` for auth, `unlimitedPaths` for the limiter, and only `/healthz` is in both.
- The login has its own limiter inside the `user` module, keyed on the client address, at a rate
  meant for a person typing rather than the per-caller default of fifty a second.
- An unknown address and a wrong password must give **byte-identical** answers. An unknown address
  also spends the same time: `spendTheSameTime` runs the KDF against a decoy so the response does
  not reveal who has an account.
- Passwords are PBKDF2-HMAC-SHA256 from `crypto/pbkdf2`, which is standard library as of Go 1.24 -
  no new dependency in a repo that carries only cobra and sqlite. The scheme and cost are stored in
  front of the hash so either can change without locking anybody out, and a stored value that does
  not parse is refused rather than treated as a match.
- **Disabling a person stops every token they hold**, checked on verify rather than by deleting
  rows, so enabling them again brings the sessions back - a disable that has to be undone by
  re-issuing tokens is not a disable. Changing a password or deleting a person **does** delete the
  rows, because those are one-way.
- `ForgetUser` on delete is not what keeps a deleted person out - `Allowed` already refuses a token
  whose user is gone. What it buys is that the row goes away, so `token list` does not show a
  session nobody can account for. That is what the test has to assert, and the first version did
  not: it checked the request was refused, which was true either way.
- **A token name is unique, so a session name has to be too.** Naming it `session-<user id>` meant
  one person could hold exactly one session; the second login returned a 409. The test that was
  supposed to catch it passed for the wrong reason - it logged in twice, ignored both status codes,
  and asserted an empty token was refused. Live caught it in one command.
- `cmd.Printf` in cobra writes to **stderr**, so a secret printed with it cannot be captured with
  `$(...)`. `marstack login` writes the token to `OutOrStdout` and the note about it to stderr.
- A password is read from a file, never a flag: a command line is kept in shell history and shown
  in `ps`.
- With two targets the **harder pressed** resource decides. Averaging them lets a service whose
  memory is nearly full stay small because its cpu happens to be idle, and taking the minimum is the
  same mistake with the sign flipped. Flipping the comparison in `hardestPressed` is a mutation test.
- The deadband is a **ratio** around 1.0, not a number of percentage points, because a band that
  means something for cpu at 70% means something else for memory at 20%.
- A memory target needs to know how much memory a replica was given. `MemoryKnown` says whether that
  is true, and a target with an unknown allocation stops the pass rather than dividing by a guess.
  An unset target is ignored, not treated as zero - zero would read as "always under target".
- A policy must name at least one target. One with neither would sit in the sweep forever.
- A container gets a tmpfs `/dev` with **six** device nodes - null, zero, full, random, urandom,
  tty - plus `/dev/pts`, a 64 MiB `/dev/shm`, and the standard links. **The list in `dev_linux.go`
  is the boundary**, so anything like `/dev/mem`, `/dev/kmsg` or a loop device belongs nowhere near
  it, and the test says so rather than only checking the six are present.
- **`/dev` itself must not be mounted `nodev`.** That flag makes every node on it useless: they are
  created and then nothing can be read from them. `/dev/shm` and `/dev/pts` do get nosuid, nodev and
  noexec, and the flags are named constants so a test can assert them.
- `syscall.Mkdev` does not exist on Linux in the standard library - it lives in `x/sys/unix`, which
  is an indirect dependency here. The encoding is
  `(major&0xfff)<<8 | minor&0xff | (minor&~0xff)<<12`; my first test expectation for a minor above
  255 was wrong, not the code.
- **`echo x > /dev/stdout` truncates a container's log**, because stdout is a file and `>` opens it
  with O_TRUNC. A probe that wrote its findings that way erased them all and printed one line. Not a
  bug - worth knowing before debugging with it.
- `/sys` is mounted **read-only**, and that is the reason it can be mounted at all rather than a
  nicety: there is no user namespace, so a container runs as real root and a writable sysfs lets it
  change the host kernel's settings. Dropping `MS_RDONLY` is a mutation test.
- Mounting sysfs from inside the network namespace is what makes `/sys/class/net` show only the
  container's own interfaces. Verified live: `eth0 lo`, not the node's bridges.
- `/sys/fs/cgroup` is a **cgroup namespace** plus a read-only cgroup2 mount, not a bind mount of the
  container's directory. `CLONE_NEWCGROUP` makes the kernel present the container's own cgroup as
  the root of the hierarchy, so `/proc/self/cgroup` reads `0::/` and there is no path upward to
  walk. Without it, mounting cgroup2 shows the host's whole tree - every other container's limits
  and usage.
- **The cgroup mount must be read-only.** A container runs as real root, so a writable
  `memory.max` lets it raise its own limit and walk out of the quota it was given. Both the flag and
  the namespace are mutation tests.
- Mounting cgroup2 works even though `/sys` above it is read-only: a mount does not modify the
  filesystem it covers. `/sys/fs/cgroup` already exists in a fresh sysfs, so nothing needs creating.
- Making a container use memory from busybox: doubling a shell variable needs **twice** the memory
  for the moment of the copy, so a steady 52% of the limit peaks at 104% and gets OOM-killed. The
  replica-health loop then churns, which is correct behaviour and looks like the feature failing.
  Give the limit room for the transient instead: 512 MiB with a 20% target and a 134 MB string.
- I claimed per-workload time-series metrics were missing. **They already existed** - per-minute
  buckets with cpu and memory average and peak, project-scoped, a day kept, on
  `GET /v1/usage/history?subject=` and `marstack usage history` with sparklines. Check before
  building.
- Memory is reported in whole MiB, so a workload under 1 MiB reads as exactly zero. An idle
  container using 116 KiB is not a broken sampler; it is the resolution. Verified against
  `memory.current` before calling it a bug.
- An instance takes a **name or an id** now, like every other resource. `getIn` resolves it, and
  the id is tried first: a name that looks like an id must not shadow the instance it identifies.
- **Resolving in one place is not enough.** `setDesired`, `resize` and `delete` all took the
  resolved instance for the permission check and then passed the *typed* string to the repository.
  For delete that is the dangerous one - it releases addresses, volumes, forwards, balancers and
  logs by that string, so a name releases nothing and leaves every one of them pointing at an
  instance that is gone. Reverting `id := found.ID` to `id := ref` is a mutation test.
- `logs` resolves through the instance module rather than repeating the lookup, so
  `marstack logs <name>` works and the response carries the id it resolved to rather than what was
  asked for.
- `/v1/audit` and `/v1/events` page with the same opaque `after` cursor as everything else, but
  **backwards**: a trail is read from the end, so the predicate is `id < ?` and the order is
  `DESC`. Reusing the ascending predicate returns the same first page forever - no error, no
  symptom. That is a mutation test.
- Their `id` is `INTEGER PRIMARY KEY AUTOINCREMENT`, so the cursor is one column rather than the
  `(created_at, id)` tuple the other resources need: there are no ties to break.
- The cursor id is **parsed** rather than passed as a string. `id < '42'` happens to work through
  SQLite's affinity coercion, which is not a thing to depend on.
- `page.Decode` only checks the envelope, so a well-formed cursor carrying a non-numeric or
  negative id gets past it. `page.Back` refuses that, and the test needs cursors like `AGFiYw`
  (base64 of `\x00abc`) to reach it - three obviously-broken strings all failed at the envelope and
  proved nothing.
- Both handlers used to swallow a bad `limit` and hand back the default. `page.From` refuses it, so
  `?limit=abc` is a 400 rather than a different page than the one asked for.
- A filter is not part of the cursor. The caller resends `kind`, `subject` or `severity` with the
  cursor and the query still applies it - verified live across four filtered pages.
- A snapshot is an **internal qcow2 snapshot inside `<volumeID>.qcow2`** on one node, so a copy of
  it can only be made on that node. The new volume is created with the source's `node_id`, and a
  source that is on no node is refused - there is no file to copy from.
- The copy follows the restore-from-backup shape exactly: the volume carries where it came from, the
  agent sees it every pass, and `HasVolume` is the idempotency guard. Nothing new to schedule.
- **An encrypted volume is refused rather than copied.** The copy would either need the key on a
  second disk or write the contents out in the clear; doing either without being asked is a leak.
  That test needs `newSealingApp`, not `newBalancingApp` - on an app with no key the volume cannot
  be made at all, so the test skips and proves nothing.
- **`qemu-img convert -s` does not exist any more.** qemu 8.2 wants
  `-l snapshot.name=<name>`. Nothing but a live run finds a wrong command-line flag: the unit tests
  never execute `qemu-img`.
- `volume attach --instance` takes a name or an id now. The volume module cannot import the instance
  module, so the resolution happens where the dependency already is: `Instances.Placement` takes
  `(ref, projectID)` and returns the resolved `InstanceID`, and `attach` stores **that**, never the
  string the caller typed. `volumeInstances.Placement` in the composition root switched from `Get`
  to `ResolveIn`; reverting either half is a mutation test.
- Resolving first also fixed idempotency: attaching to the instance a volume is already on used to
  compare the typed string, so re-attaching by name got "the volume is attached to i-1" instead of
  the same volume back.
- The two-writers refusal in the service is **not** what enforces the invariant - the UPDATE does,
  through `WHERE instance_id = ''`, which is also the only race-safe half. The service guard earns
  its place by naming the instance holding the disk instead of "someone else", so that is what the
  test asserts. Asserting only the 409 passes with the guard deleted.
- A drain now **carries the disk** of a vm, microvm or sandbox instead of refusing to move it. The
  node is still answering, so it can hand the disk over: park it on the control plane, release the
  placement, let the destination fetch it. The **stranded** path is unchanged and must stay that
  way - a node that stopped answering cannot be asked for anything.
- **`desired = stopped` was being used as "not wanted anywhere", and a migrating instance is very
  much wanted somewhere.** Stopping it for the move made it vanish from `listRunningOn` (so the
  drain declared the node empty and finished with a disk still on it) and from
  `listPendingPlacement` (so once released, nothing ever picked it up). Both queries needed
  `OR migrating = 1`. This is one root cause that produced two different silent failures, found
  live one after the other.
- **The node that parked a disk must not be allowed to take it back.** It is still the assigned node
  until the scheduler releases the placement, so it wins that race every time - the first live run
  ended the move on the node being drained, looking like a success. `disk_from` records who handed
  it over and serving it back to them is refused.
- The scheduler cannot see which runtimes implement `DiskCarrier`, so `carriesItsDisk` names the
  isolations explicitly. Without it a fifth isolation added later would be stopped and then wait
  forever for a hand-over nobody makes - worse than the old honest `drain_blocked`.
- Deleting an instance mid-move left its parked disk on the control plane forever. The cleanup has
  to sit **before** `delete`'s early return on a nil networks module, because that return skips
  everything after it.
- A migration test cannot share an app with a running scheduler: the scheduler releases the
  placement the moment the disk is parked, so any test that parks and then does something with the
  source node races it. `steadyMigration` builds an app with no scheduler and calls
  `BeginMigration` directly; the drain-driven path has its own tests.
- `efivars.fd` is **not** carried, only `disk.qcow2` and `rootfs.ext4`. The destination rebuilds UEFI
  variables, which booted cleanly in the live run, but a guest that depends on a custom boot entry
  would not survive the move.
- `marstack exec` runs **one command** in a container and hands back its output and exit code. It
  is deliberately **not a shell**: no stdin, no terminal. An interactive session needs a channel
  that stays open between the client and the node, and the node only ever calls out, so that is a
  different piece of work rather than a flag on this one.
- Only a **container** can be entered. A vm, microvm or sandbox runs its own kernel, so getting
  inside needs an agent in the guest; the refusal points at `marstack console` instead of handing
  back an empty answer.
- Entering is `nsenter --target <init pid> --mount --uts --ipc --net --pid`. Go cannot `setns` into
  a mount namespace from its own process: by the time `init()` runs the runtime is already
  multithreaded and the call fails with EINVAL.
- **A slow command must not run inside the reconcile pass.** A five minute timeout would stall
  every workload on the node for five minutes - the same shape as the registry pull. Commands are
  served by their own two second loop, which also cut the round trip from a whole pass to under
  three seconds.
- `exec.CommandContext` alone does not end a timed-out command: killing `nsenter` leaves the
  process it started holding the output pipes, so `Run` waits for them. A 3 second timeout took
  **70 seconds**. `Setpgid` plus a `Cancel` that kills the group plus `WaitDelay` brings it to 4.
- **The timeout has to be checked before the exit error, not after.** A SIGKILL makes `Run` return
  an `*exec.ExitError`, so a check for that first always matches and the caller is told
  "exited with -1" instead of "ran out of time". Swapping the two is a mutation test.
- Handing a command out is two steps - read the waiting row, mark it taken - so the **update** has
  to be the one that decides, with `state = 'waiting'` in its WHERE. Eight concurrent takers proved
  it: without the condition four of them ran the same command.
- Output is capped keeping the **end**, like a log: what a command was going to tell you is at the
  end.
- The migration disk routes were missing from `streamingPaths`, so a real vm disk would have been
  cut off by the 30 second request timeout. The live run only passed because the disk was 25 MB.
- `instanceNodeID` now fails on any status but 200. It used to unmarshal whatever came back into
  `{node_id}`, so a 429 read as "not placed yet" - the same misreading any client polling faster
  than the limit would make, and the reason the failure looked like a scheduler bug.

- Runtime packages are split by build tag. Portable constants live in the untagged file; anything
  using `syscall` or `filepath` layout helpers goes in a `_linux.go` file, with a stub for other
  platforms. Putting a Linux-only helper in an untagged file compiles on macOS but shows up as dead
  code, and the tests then only run on one platform.
- A balancer with **routes** is a userspace listener for the same reason one with a certificate is:
  nftables does not read requests. `runsInUserspace()` is where the two reasons meet, and a routed
  balancer is left out of the published set - a listener and a dnat rule on one port is undefined.
  Verified live that `nft list ruleset` has nothing for the routing port.
- The matching order lives in **two** packages on purpose. `internal/runtime/tlsproxy` may not
  import `platform/balancer`, and a node that walked the routes in the order it was handed would
  answer differently the day something reordered a JSON array. Exact host before any host, then
  longest path, `sort.SliceStable` at both ends. `balancer get` shows the resolved order rather
  than what was typed, which is what makes the rule readable.
- A path prefix matches on **segment boundaries**: `/api` covers `/api` and `/api/users` and must
  not cover `/apiary`. A plain `strings.HasPrefix` sends one application's traffic to another, and
  that is a mutation test.
- The Host header is matched **without its port and without case**. A client that dials
  `app.test:8443` sends that whole string, so comparing it raw gives a 404 on the one request that
  was correct.
- **404 and 503 are different answers.** No route matched is a 404; a route whose service holds no
  replica is a 503. Collapsing them hides which half is broken, and falling through to the first
  route instead is how a request nothing claims reaches somebody else's application.
- Nothing is rewritten on the way through. The backend gets the original Host and the whole path,
  plus `X-Forwarded-For` from `SetXForwarded` - a route decides *where* a request goes, not *what*
  it says. `ReverseProxy` in `Rewrite` mode clones the inbound request, so `Out.Host` already
  carries the client's Host; setting `Out.URL.Host` alone is enough and an explicit
  `Out.Host = In.Host` is a line that cannot fail. The behaviour is still tested, because
  `SetURL` - or anyone clearing `Out.Host` - would break it.
- Routes are refused together with `--service` or `--instance`, on `udp`, and with `source_hash`.
  The last one is the `--env` trap again: a routing balancer picks a backend per request out of the
  route that matched, so the algorithm would be accepted, stored, and then ignored.
- **Route backends have to be added to `everyBackend`, not only to the read paths.** Both loops in
  `reportHealth` build their membership map from it, and a report that is not in that map is
  dropped as "not a member" with no error anywhere - the same trap the service-backed balancer
  already had, one level deeper.
- The route table is swapped behind an atomic pointer, and only when
  `routesFingerprint` changes. Rebuilding it on every pass would reset the per-route round robin
  counter every ten seconds, which under light traffic means every request goes to the first
  backend. The listener itself restarts only when the port, the certificate, or **whether there are
  routes at all** changes - that last one is in `Endpoint.fingerprint`, or giving a balancer routes
  leaves it splicing raw bytes forever.
- Deleting a balancer deletes its routes in the same transaction. No API can see an orphan route
  row, so that one is only testable at the repository - the same reasoning as the per-instance log
  trim.
- Alpine's busybox has **no `httpd` applet** (it lives in `busybox-extras`), so the obvious way to
  make a container serve HTTP for a live test fails and the service quietly churns replicas. A
  `nc -l -p 80` loop dropped in with `--file` works and needs no image build.
- The agent's `do` decoded the body on **every** status below 400, so a `204 No Content` came back
  as `decode response: EOF`. `GET /v1/nodes/{id}/exec` answers 204 whenever nothing is waiting,
  which is the steady state, so every idle node logged a warning every two seconds. The CLI client
  already had the guard; the agent's copy did not. A 204 means there is nothing to decode - but
  widening that to any 2xx makes the agent stop reading real answers, and both directions are
  mutation tests.
- No test caught it because the exec tests all had a command waiting. The absence of work is a
  case, and for a poll loop it is the case that runs 99% of the time.
- A crash had a backoff and a **failed start did not**: `ensureRunning`'s default branch called
  `Start` on every pass, so an instance that could not start logged a warning every ten seconds for
  as long as the node ran. A failed start is a restart attempt; it goes through `restartAllowed`
  and `noteRestart` now. Counting the attempt inside `start`'s failure branch rather than before
  the call is deliberate - a successful first start must not be charged one, or the first crash
  would begin at attempt 2.
- Waiting is reported, not silent: the message carries the last reason plus `trying again in Xs
  (attempt N)`. A workload that is waiting and says nothing looks the same as one nobody is
  looking after.
- **A permanent failure is not a slow retry.** `workload.ErrUnstartable` is what a runtime wraps
  when no amount of waiting can help - today only "the image declares no command and none was
  given". The agent reports `failed` with the reason, logs once, and stops calling `Start`. Only
  the runtime can make that call, because nothing above it can see the image.
- The refusal lives in the agent's memory, keyed on the instance id, so a new agent tries once
  more. That is right rather than sloppy: the binary that refused may not be the binary running
  now, and the only fix for this class of failure is delete-and-recreate, which gives a new id.
- The mark has to be a **wrap**, and removing it compiles. That is why the command decision moved
  into `guest.CommandFor` - one place produces the refusal, and `internal/runtime/guest` is
  untagged so a test on macOS can assert `errors.Is(err, workload.ErrUnstartable)`. Left in the two
  `_linux.go` files it was two copies of one error string that no test could reach.
- `a.restarts` was never pruned - one entry per instance the node ever held. `forgetRestarts` runs
  beside `forgetLogs` on every reporting pass, and it matters more now that the map holds the
  give-up decision: state that outlives its instance is a verdict waiting to be inherited.
- `PUT /v1/balancers/{id}/routes` **replaces** the whole table, the same choice as
  `PUT /v1/nodes/{id}/labels` and for the same reasons: merging leaves no way to remove one route
  without inventing a delete route and a null-means-delete convention, and it makes the call
  non-idempotent.
- The replace path calls `normalizeRoutes` and `checkRoutes` too. A second way in that skips the
  rules is the way round every one of them, and each is a mutation test on this route as well.
- An **empty** table is refused: a routing balancer with no routes is a bound port that answers
  every request with a 404, which is not a state anybody asks for on purpose. Deleting the
  balancer is how you stop routing.
- A balancer created without routes cannot grow them, and one with routes cannot drop to none.
  Either direction is a **mode change** - nftables rule versus userspace listener - and doing that
  through a route that looks like an edit would move the port from the kernel to the agent behind
  the operator's back.
- A node could only pull what a registry serves **anonymously**: `runtime/image` sent no
  credentials at all. That is no private image ever, and a much lower rate limit on the public
  ones - the 429s from Docker Hub in the live logs were exactly this.
- A registry credential is **administrative, not project-scoped**, the same precedent as images
  and firewalls: one host holds one login, and a tenant able to read it reads every other
  tenant's pull credential too. The host is unique globally for the same reason - one pull cannot
  be made with two logins.
- The password is sealed with the operator key and served **only** on
  `GET /v1/nodes/{id}/registries`. The operator response has no field for it, and the test asserts
  the exact key set rather than the absence of one string, so any field added there fails.
- The login goes on the **token request**, not on the API request. A bearer challenge means
  fetching a scoped token from the realm, and an anonymous token request gets an anonymous token -
  which is the pull that was already failing. A `Basic` challenge, which self-hosted registries
  send, is now answered directly instead of being refused as "unsupported".
- Changing a credential throws away the cached token for that repository, or a rotation changes
  nothing until the process restarts. Handing down an **unchanged** credential must not throw it
  away, or every reconcile pass costs a token round trip per pull - both directions are mutation
  tests.
- The agent hands credentials to any runtime implementing `UseRegistryCredentials`, type-asserted
  the same way `Resize` is. Only container and microvm pull images; adding it to `Runtime` would
  make three drivers accept a call two of them cannot use.
- There is still **no way to reach a plain-HTTP registry**, and there must not be a flag for it,
  for the same reason there is no skip-verify flag. A self-hosted registry needs a certificate
  from a CA the node trusts - which is how the live test was done: a CA installed into the node's
  trust store, and the agent restarted so Go re-reads it.
- A certificate's `expires_at` was stored and served and **nothing looked at it**. TLS simply
  stopped working one day, on a port that had been fine for a year, with no event and no log line.
  Both `balancer` and `forward` sweep their own certificates every ten minutes now.
- The event is keyed on a **transition**, not a condition - the `instance.stranded` trap again. A
  sweep that emits while a certificate is expiring writes one event every ten minutes forever, so
  the service remembers the last state it reported per id.
- That memory must be **cleared while the certificate is healthy**, or a renewal followed later by
  a second approach to expiry never warns again. A test that only checks "a renewal goes quiet"
  passes without the clearing; the test has to renew, then approach expiry a second time.
- Remaining time is **floored**, never rounded up: 2d23h reads "2 days". A warning that claims
  more time than there is, is worse than no warning. That is why the test uses 3d12h - a life of
  exactly 3 days floors to 2 the instant the sweep runs.
- `certs.Life` and `interval.Human` live in the kernel because `balancer` and `forward` both need
  them and neither may import the other. The rule is shared mechanism; the message is not - each
  module says "balancer web on port 8443" or "published port 9443" in its own words.
- The memory is in the service, not a column, so a restart re-warns once. That is the same choice
  as probe verdicts, and it is the right one here: the alternative is a schema change to store
  something that is only ever an anti-spam counter.
- Detach is a **state the node makes true**, not an action: `volumes.detaching` is set, the node
  sees a disk it holds that is no longer wanted, asks the guest to release it, and reports back.
  Clearing the row and calling it detached was a lie - the guest kept the disk and would have gone
  on writing to it.
- The volume keeps `instance_id` while detaching, because that is the truth and because the node
  needs to know which guest to ask. `State()` reports `detaching`, and the node view carries the
  flag so the agent can leave it out of the wanted set.
- **Only positive evidence counts as released.** Absence from `query-block` does not mean gone: a
  half-finished unplug leaves the block node behind, and an encrypted disk's node is not named the
  way the drive is. The proof is `blockdev-del` succeeding - it fails with "in use" for exactly as
  long as the device is attached, so polling it is both the wait and the evidence.
- **A disk on `pcie.0` can never be hot-unplugged** - `device_del` answers "Bus 'pcie.0' does not
  support hotplugging". Boot-time disks are placed behind a `pcie-root-port` now, the same as
  hot-plugged ones, so attach and detach are symmetric. A guest started before that change must be
  restarted before its disks can be released.
- Unplug is a sequence - `device_del`, wait, `blockdev-del`, `object-del` - and any step can fail
  midway. The leftovers block the next attach with "Duplicate nodes" or "duplicate property", so
  `plug` heals both: it deletes the leftover and retries once. Without that, one failed detach
  makes a volume permanently unattachable to that guest.
- A guest that has the filesystem mounted **will not** release the disk, and that is the whole
  point. The volume stays detaching and the message says to unmount it inside the guest or stop
  the guest. Verified live both ways: a mounted volume refused, an untouched one released without
  a restart.
- Re-attaching to the same instance cancels a detach. Without it an operator who changes their
  mind, or a volume whose node never answers, sits in detaching forever with no way out.
- `Placement.Observed` was added because `Running` is the **desired** state. Asking a guest that is
  not running to release a disk waits for an answer that will never come, so detach from anything
  not observed running finishes at once.
- ACME lives in two places for the usual reason. `kernel/acme` is the protocol - JWS, nonces,
  orders, challenges - and knows nothing about balancers, the same shape as `kernel/s3`.
  `platform/acme` owns the resource and drives one step per sweep.
- The **first** request carries `jwk` and every later one carries `kid`. Sending both, or sending
  the jwk after the account exists, is refused by a real directory. Both directions are mutation
  tests against a fake CA that also refuses a **reused nonce**, which is the other thing a real
  directory checks and a hand-written client gets wrong.
- Only `http-01` is answered. The challenge is served by the node's userspace listener on the
  balancer's own listen port, before route matching, and `/.well-known/acme-challenge/` is
  **terminal**: an unknown token is a 404, never proxied to a backend. Forwarding it would let a
  workload answer a challenge on the platform's behalf.
- That means the authority must be able to reach the balancer on the name it is validating - port
  80 for a public CA. That is how HTTP-01 works, not a limitation this platform invented, and the
  CLI says so.
- The order is a state machine driven one step per sweep, so a pass never blocks on a CA: open,
  wait a grace period for the nodes to pick the token up, accept, poll, finalize, download,
  attach. The grace is why the tests need a movable clock.
- Renewal is `certs.Life` again, asked thirty days early. Stopping the renewal leaves the
  certificate that is already attached alone - a port that stopped answering https the moment you
  said "stop renewing" is not what anybody means by that.
- The account key is generated once and kept. Registering a new account per renewal is how a rate
  limit is reached, and there is a test that a second client reusing the stored account registers
  nothing.
- A stub CA that hands back a self-signed certificate proves nothing: the key will not match the
  CSR and `certs.Inspect` refuses it. The test CA parses the CSR, checks its self-signature, and
  signs **that** public key. Live, a small ACME server on the node actually fetched
  `http://127.0.0.1:8600/.well-known/acme-challenge/<token>` and compared it byte for byte before
  issuing - which is the only way to know the responder is wired to the right port.
- An alert closes the last link: usage history existed, webhooks existed, and nothing joined them.
  It records an `alert.firing` or `alert.cleared` **event**, which the webhook module already fans
  out, so there is no second delivery path to maintain.
- It fires only after the reading **holds for the whole window**, which is why there are three
  states rather than two: quiet, warming, firing. One high sample is a spike, and paging somebody
  for a spike is how alerts get muted.
- The event is written on a **change**, never on a condition - the same trap as `instance.stranded`
  and the certificate sweep. A twenty second sweep that recorded a condition would wake every
  webhook every twenty seconds.
- **A missing reading is not zero, and a stale one is not now.** Both stop the alert rather than
  deciding from them, and they are told apart in the message: a workload that never reported and
  one that stopped reporting send an operator to different places. Testing this needs a `below`
  alert - with `above`, a zero reading is quietly correct and the guard looks unnecessary.
- Memory needs a limit to be a percentage, so an unknown limit stops the alert. Same reasoning as
  autoscale, and the same test shape: only a `below` threshold exposes it.
- The message is written on **every** pass; only the event is held back. An alert whose reason
  changed but whose state did not still has to say the new reason, or the row keeps claiming
  "nothing has been read yet" while the truth is "the reading is thirty minutes old".
- `usage` never took the app's clock, so a frozen-clock test read every sample as sixteen hours
  stale. Any module that stamps a time has to take `cfg.Now`, or the app disagrees with itself
  about what time it is.
- `marstack shell` is the interactive half `exec` deliberately was not. The agent still has no
  listener, so **the node dials out twice**: one GET whose response body carries keystrokes down,
  one POST whose request body carries output up. The control plane joins the two with a pair of
  `io.Pipe`s held in memory, and both routes are in `streamingPaths` or `http.TimeoutHandler`
  buffers the whole thing and nothing arrives until the shell exits.
- **Flush through the wrapper, not past it.** `w.(http.Flusher)` does not follow `Unwrap`, so with
  `statusRecorder` in the chain the assertion silently yields nothing and the response headers
  never leave. `http.NewResponseController(w).Flush()` is the one that works - the same trap
  `statusRecorder.Unwrap` was written for, one level up.
- A blocked `stream` has to be woken when its request dies, or a client that walks away leaves a
  goroutine reading a pipe forever. The context watcher closes the pipe reader, which is what makes
  the blocked `Read` return. `httptest.Server.Close` hanging is how this was found.
- There is **no pty**. The shell gets pipes, so there is no prompt, no line editing and no ctrl-c;
  a command runs and its output comes back. A pty needs `/dev/ptmx` ioctls through `unsafe` or a
  new dependency on an agent that runs as root on customer metal, and neither is worth it for
  prompt echo. The CLI says so in its help rather than letting somebody discover it.
- Stdin ending is not the session ending. Piping commands in closes stdin immediately, and
  cancelling on that killed the output stream before a single line came back. Only a real error or
  ctrl-] ends the session; EOF just stops the input pump.
- Handing a session out is two steps, so the **update** decides, with `state = 'waiting'` in its
  WHERE - the same rule as `exec`. Eight concurrent takers do not prove it, because with one SQLite
  connection the first update usually lands before the others read. The repository test reads the
  row eight times **first** and then takes eight times, which is the interleaving that actually
  happens across nodes.
- A node **reports** the devices it carries on every pass; nobody registers them. Only display and
  processing classes are listed - a bridge or a network card is not something anybody asks to be
  given - and a card only counts as available once it is bound to **vfio-pci**. While a host driver
  holds it, qemu cannot open it, so offering it places a workload that then fails to boot.
- One card goes to one workload. `FreeDevice` is the scheduler's filter and `ClaimDevice` is the
  decision, and the **update** is what decides, with `instance_id = ''` in its WHERE - the same
  rule as taking an exec or a shell. Weakening `FreeDevice` alone changes nothing observable
  because the claim refuses anyway, so the test that catches it asserts the **held reason**: a
  workload the scheduler silently keeps skipping looks exactly like a broken scheduler.
- A device the node stops reporting is deleted **unless a workload holds it**. Those are kept and
  marked `ready = 0, driver = 'missing'`, or a card unbound from vfio while a guest has it just
  disappears and the workload is holding something nothing can account for. Found live: the agent
  truthfully reports no gpu, which wiped an injected one within a pass.
- Only a **vm** may be given a device, because passthrough goes through vfio into a guest kernel.
  A container shares the host kernel and would need a device node instead, which is a different
  feature with different rules.
- **The passthrough itself is unverified.** There is no gpu in the development fleet, so the live
  run proves the inventory, the hold reason, the claim and the address reaching the node view; the
  `vfio-pci,host=...` argument is covered by a unit test on the command line and by nothing else.
  Do not describe this as working on real hardware until it has run on some.
