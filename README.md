# marstack-cloud

A cloud platform that runs containers, VMs, and microVMs as one kind of resource — on a single
node or across many baremetal machines, through the same code and the same API.

> Status: **early.** Containers run and are networked across nodes. VMs, image pulling, and
> identity are not built yet.

## Design principles

| | |
|---|---|
| `N=1` is the general case | there is no "single node" mode — a one-node cluster takes the same code path |
| One `instance` object | containers, VMs, and microVMs differ by an `isolation` field, not by resource type |
| The agent reconciles | it does not take orders, so a dead control plane does not take running instances down |
| Native routing, no encapsulation | full 1500 MTU, zero switch configuration |
| Names, not addresses | every instance gets an internal DNS name the moment it is created |

## Instances

| `isolation` | Runs on | Suited for |
|---|---|---|
| `container` | own runtime (namespaces, cgroup v2, overlayfs) | workloads that do not need their own kernel |
| `vm` | QEMU | a whole machine: any OS, graphical console, passthrough |
| `microvm` | Cloud Hypervisor | fast boot while keeping a private kernel |
| `sandbox` | Firecracker | ephemeral work, restored from a snapshot |

VMM names never appear in the API or the CLI.

## Images

Containers, microvms and sandboxes pull an OCI image from a registry when they
start. A vm does not: it boots a disk image, and a microvm boots a kernel, and
both are files that have to be on the node. Those are registered in the catalog
and downloaded by the node that needs one.

```sh
marstack image create --name ubuntu-24.04 --kind disk \
  --source https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-arm64.img \
  --checksum sha256:<published digest>

marstack image create --name alpine-virt --kind iso \
  --source https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/aarch64/alpine-virt-3.20.3-aarch64.iso

marstack image list
```

```
NAME           ID                  KIND   ARCH    SIZE   NODES   SOURCE
alpine-virt    img-qmzgg4fy5bv58   iso    arm64   69Mi   1       https://dl-cdn.alpinelinux.org/...
ubuntu-24.04   img-r9wc2qkcwg8fy   disk   arm64   590Mi  2       https://cloud-images.ubuntu.com/...
```

| Kind | What it is | Used by |
|---|---|---|
| `disk` | bootable cloud image, qcow2 or raw | `--image` on isolation `vm` |
| `iso` | optical media, attached and booted first | `--iso` on isolation `vm` |
| `kernel` | uncompressed kernel, no bootloader | `--kernel` on `microvm` and `sandbox` |

`SIZE` and `NODES` come from the nodes, not from the registration: nothing knows
how large an image is until a node has downloaded it. Checksums are optional but
verified when given, and both `sha256:` and `sha512:` are accepted because
publishers disagree about which to sign.

Nothing is downloaded speculatively. A node fetches an image the moment a
workload placed on it needs one, into `/var/lib/marstack/images`, and records
where it came from. When an image leaves the catalog and no instance on the node
references it, the node deletes the file. A file staged by hand has no such
record and is never touched, so an air gapped node still works:

```sh
sudo curl -fL -o /var/lib/marstack/images/debian-12.qcow2 \
  https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-arm64.qcow2
```

For a node with no catalog yet, `make stage-images` downloads the Ubuntu cloud
image and extracts the running kernel to `images/kernel.Image`, which is what
Cloud Hypervisor and Firecracker boot - `/boot/vmlinuz` on arm64 is a gzip
wrapper around the Image and neither VMM will read it.

### Installing an operating system yourself

An iso plus a blank disk is an installer:

```sh
marstack instance create --name build-1 --isolation vm \
  --iso alpine-virt --disk-gib 20 --memory-mib 2048

sudo marstack instance console <id>      # on the node holding it
```

The iso is attached with `bootindex=0`, so it boots before the disk. Give the vm
an `--image` instead and it overlays that disk rather than starting empty; give
it both and the iso boots first, which is how a rescue disk works.

## Tokens

Every endpoint except `/healthz` needs a bearer token. The first time the
control plane starts with an empty database it mints an admin token and writes
it to `<data-dir>/bootstrap-token`, mode 0600, and says so in the log.

```sh
export MARSTACK_TOKEN=$(cat ./data/bootstrap-token)
marstack token list
```

```
NAME        ID                  ROLE    PROJECT       EXPIRES               LAST USED
bm-1        tok-38158xayggpk6   node    prj-default   never                 2026-08-26T16:19:47
bootstrap   tok-sb510112ps6gw   admin   prj-default   never                 2026-08-26T16:19:48
ci          tok-bgs077af71e5j   member  prj-default   2026-09-26T02:11:04   2026-08-27T09:02:11
```

A token can be given a lifetime, and after it the token stops working:

```sh
marstack token create --name ci --role member --ttl 30d
```

Lifetimes read as `12h`, `90m`, `30d` or `4w`. Without `--ttl` a token never
expires, which is the right default for an agent and the wrong one for a
person. An expired token answers `401 token_expired` and stays in the listing
marked `(expired)`, so an outage explains itself instead of looking like a
revoked token. Revoking still works the way it always did.

The rule that the last admin token cannot be revoked counts only tokens that
still work — an expired admin token does not keep you from locking yourself
out, so it does not pretend to.

A node gets its own token, and it cannot do an operator's work with it:

```sh
marstack token create --name bm-1 --role node > /etc/marstack/token
sudo MARSTACK_TOKEN=$(cat /etc/marstack/token) marstack agent --name bm-1
```

| Role | May call |
|---|---|
| `admin` | everything in its project, plus projects, tokens and the audit trail |
| `member` | the resources in its project, and nothing administrative |
| `viewer` | the same resources, read only |
| `node` | register, heartbeat, its own desired state, the dns zone, its image and firewall view |

A node token asking for `/v1/instances` gets 403, and creating an instance with
one gets 403 too, so a compromised node cannot schedule work or read the whole
platform. A member token asking for `/v1/tokens` gets 403, so it cannot mint
itself an admin. A viewer token reaches exactly the reads a member does and
nothing else, because its allow-list is derived from the member one by keeping
only `GET` — a route added for members cannot accidentally become writable for
viewers. Secrets are stored as a sha256 hash and never appear in a
listing. The only admin token cannot be revoked, because that locks everyone
out.

## Projects

A project owns instances, networks, volumes, images, firewalls and published
ports. Every token belongs to exactly one project, and a request only ever sees
rows carrying that project id — whatever the caller's role. Roles decide which
kinds of operation a token may perform, not which project it can reach, so an
operator who needs another project mints a token there.

```sh
marstack project create --name payments
marstack token create --name pay-dev --role member --project prj-9wq0ha4c1tnx6
```

Reaching an id in another project answers `404`, not `403`. A `403` would
confirm the id exists, which is itself something one tenant should not learn
about another.

Names are unique per project, so two teams can each run an instance called
`web`. Two things stay global on purpose, because they are physical rather than
policy: a network range, since routing here carries no encapsulation and
10.20.0.4 has exactly one destination on the wire; and a node port, since only
one process can own port 80 on a machine. The first project to ask takes
10.20.0.0/16, and every project after that is carved a free /16 out of
10.0.0.0/8.

The default project, `prj-default`, exists from the first start and cannot be
deleted — every migration backfills existing rows against that id. A project
holding anything cannot be deleted either.

## Quotas

A project could consume the whole fleet. Limits cap what it may hold, and a
limit of zero means no limit:

```sh
marstack quota set prj-6x45b0a054caa --instances 20 --vcpu 40 --memory-mib 65536   --volumes 10 --volume-gib 500
marstack quota show
```

```
PROJECT             INSTANCES   VCPU     MEMORY MiB     VOLUMES   VOLUME GiB
prj-6x45b0a054caa   3 / 20      3 / 40   1536 / 65536   1 / 10    3 / 500
```

Limits are checked when work is created, against what the project already
holds. Lowering a limit below current usage deletes nothing — it stops the
project growing until it fits again, which is the behaviour an operator wants
when they realise a tenant is too large. Deleting an instance or a volume gives
the room straight back.

A refusal names the number it hit rather than saying no:

```
error: quota_exceeded: the project is limited to 2 of instances and already
holds 2, so 1 more would not fit
```

A member sees the limits binding it through `marstack quota show`, and cannot
change them. Only an admin sets them.

One honest limitation: the check reads current usage and then writes, without
holding a lock across both. Two creates racing at the same instant can both
pass a limit they would individually respect. SQLite runs one writer at a time
so the window is small, and closing it properly needs a transaction spanning
two modules, which this architecture deliberately does not allow.

### Attaching to a running guest

A volume attached to a running instance used to wait for a restart. The node
plugs it in now, through a QMP socket QEMU is started with:

```sh
marstack volume create --name extra --size-gib 5
marstack volume attach extra --instance i-7dn7y7vsn7218
```

The agent asks the guest what it already has and adds what is missing, so the
same reconcile that survives a node restart also covers a plug that failed once.
An encrypted volume is plugged with its key as a QMP `secret` object, and the
key still never reaches the node's persistent storage.

Two things this does not do:

- **Detaching still waits for the next start.** Pulling a disk a guest is
  writing to can hang the guest or lose what was in flight, and nothing on the
  platform side can tell whether the guest has released it. Deferring is the
  honest answer.
- **A guest started before this existed has no QMP socket**, so it needs one
  restart before it can be hot-plugged.

The guest still has to notice the new device and mount it. The platform hands it
a disk.

### Growing a volume

```sh
marstack instance stop i-7dn7y7vsn7218
marstack volume resize data-1 --size-gib 20
```

A volume only grows. Shrinking means choosing which bytes to lose, and that is
not a decision a control plane should make on its own, so it is refused with the
current size in the message.

The guest must be stopped, because qemu holds the disk open while it runs and
`qemu-img resize` would be writing to an image that is changing underneath. The
control plane records the new size and the node applies it on its next pass,
comparing the file's virtual size with the wanted one — so a resize that arrives
while a node is down still happens when it comes back.

A volume that has never been attached has no file yet, so resizing it is only a
change of mind about how big to make it. That case is allowed where snapshots
and backups are not.

Growing the filesystem inside the guest is the guest's job. The platform gives
it a bigger disk and stops there.

## Backups

A snapshot lives inside the volume file, on the node that holds it. That
protects a volume from a bad write or a failed upgrade, but not from losing the
node: if the disk goes, the snapshots go with it. A backup is the other half —
it copies the volume off the node.

```sh
marstack backup create vol-7dn7y7vsn7218 --name before-upgrade
marstack backup list
```

```
NAME             ID                  VOLUME             STATE   SIZE      CREATED
before-upgrade   bkp-x91fa47b7zrza   vol-7dn7y7vsn7218  ready   41156608  2026-08-27T02:11:04
```

The control plane records the backup as `pending` against the node that holds
the volume. On its next reconcile that node copies the volume with `qemu-img
convert`, streams it to the control plane, and the control plane records the
size and a sha256 of what it actually received. A volume attached to a running
instance is skipped rather than copied, for the same reason snapshots are: the
qemu process holds the write lock.

Restoring makes a new volume rather than overwriting a live one:

```sh
marstack volume create --name restored --size-gib 10 --from-backup bkp-x91fa47b7zrza
marstack volume attach restored --instance i-7dn7y7vsn7218
```

The agent notices the volume names a backup, fetches the bytes before anything
starts, and writes them as the volume file. Recovering from a node that is gone
for good works the same way, because the bytes never lived only on that node.

A backup somebody has to remember to take is not protection. A volume can carry
one schedule:

```sh
marstack backup schedule set data-1 --every 6h --keep 7
marstack backup schedule list
```

```
VOLUME              EVERY     KEEP   NEXT                  LAST
vol-xr6yrzzt4paf8   6h0m0s    7      2026-08-27T14:20:14   2026-08-27T08:20:14
```

The control plane sweeps due schedules every minute, queues a backup named
`auto-<timestamp>`, and prunes its own older copies down to `keep`. Two things
it deliberately does not do:

- It never prunes a backup you took by hand. Retention only removes copies
  carrying the schedule's id, so `before-upgrade` survives any number of
  automatic ones.
- It never queues a second copy of a volume while one is still pending. A node
  that cannot upload — out of disk, partitioned, holding a running guest —
  would otherwise collect a queue nobody can drain.

Retention runs when a backup becomes ready, not only on the next sweep, because
the count only changes at that moment. It runs on the sweep as well, so
lowering `keep` takes effect without waiting for the next copy.

### Encrypted volumes

A backup key also protects volumes on the node:

```sh
marstack volume create --name secrets --size-gib 10 --encrypted
marstack volume attach secrets --instance i-7dn7y7vsn7218
```

The node writes a LUKS qcow2 through `qemu-img` and cannot read it on its own.
The control plane generates a random key per volume, seals it with the operator
key, and hands it to the node that holds the volume — over the API, so run this
with TLS. The node writes the key to `/run/marstack/keys/`, which is tmpfs: a
powered-off disk carries the ciphertext and nothing else.

```
$ sudo qemu-img info /var/lib/marstack/volumes/vol-....qcow2
encrypted: yes
```

Guests need no cooperation; QEMU does the crypto and the guest sees an ordinary
virtio disk. Snapshots work as before, with the key passed alongside.

Backups work, and plaintext never touches the node on the way out. The node
creates an encrypted target with the same volume key and converts straight into
it, so the export is a LUKS qcow2 from the first byte. The vault then seals that
with the operator key as it does any other backup.

Restoring adopts the key rather than minting one. Each backup carries the
volume key as a sealed envelope, and a volume created from it inherits that
envelope — the node writes the bytes verbatim and the volume's own key opens
them:

```sh
marstack backup create secrets --name nightly
marstack volume create --name secrets-restored --size-gib 10 --from-backup bkp-...
marstack volume attach secrets-restored --instance i-...
```

The backup module never opens that envelope; it stores and returns it. And it is
never served to an operator — a key that travels on every listing is a key that
ends up in a log.

Three things this refuses, each for a reason:

- **Restoring a plaintext backup into an encrypted volume.** The node writes a
  restore byte for byte, so the result would be a plaintext disk claiming to be
  encrypted.
- **Restoring when the key that wrapped the backup's volume key is gone.** It
  names the missing key rather than producing a disk nobody can read.
- **Creating an encrypted volume without `--backup-key-file`.** A volume key
  stored beside the data it protects is not encryption, so it says so instead of
  pretending.

One cost: the export of an encrypted volume is not compressed, because
`qemu-img` cannot compress into a pre-created target. An encrypted volume's
backups are therefore larger than a plaintext volume's.

### Encryption at rest

Backup content is a copy of a customer's disk, kept indefinitely. Without a key
it sits in the data directory in the clear, and the control plane says so on
every start:

```
level=WARN msg="backups are stored unencrypted, so a copy of every volume sits
  in the data directory in the clear" fix="pass --backup-key-file"
```

```sh
marstack backup keygen > /etc/marstack/backup.key
chmod 600 /etc/marstack/backup.key
marstack server --backup-key-file /etc/marstack/backup.key
```

AES-256-GCM in 64 KiB frames, each frame authenticated. A per-file salt derives
the frame key, so two backups of the same bytes look nothing alike and no nonce
is ever reused. Truncation, appended bytes, a flipped bit and a frame spliced in
from another backup all fail to decrypt rather than returning a short or wrong
disk — the tests in `internal/kernel/sealed` check each of those.

The recorded size and checksum describe the **plaintext**, so a backup's
checksum can still be compared with the volume it came from.

Rotation works by keeping the old key readable. Each backup records which key
sealed it, and the first `--backup-key-file` seals new ones:

```sh
marstack server   --backup-key-file /etc/marstack/backup-2026.key   --backup-key-file /etc/marstack/backup-2025.key
```

Turning encryption on does not touch what came before: a backup taken without a
key stays readable. A backup whose key is no longer held answers with an error
naming the key rather than serving ciphertext as though it were a disk.

**Lose the key and the backups it sealed are gone.** There is no recovery path,
by design — a key escrow the control plane could read would defeat the point.
Keep it somewhere other than the data directory it protects.

The bytes land in `<data-dir>/backups/` on the control plane. That makes the
control plane the thing worth protecting — which is honest, and better than
having no copy off the node at all. Shipping them to object storage instead is
a later change to one interface.

The CLI reads `--token`, then `MARSTACK_TOKEN`, then `--token-file`, then
`MARSTACK_TOKEN_FILE`. Nothing is read implicitly from a default path.

## TLS

Without a certificate the control plane serves plain HTTP, and every bearer
token crosses the network in the clear. It says so on every start, because a
warning you see once a day is better than a default you forget:

```
level=WARN msg="serving plain HTTP, so every bearer token crosses the network
  in the clear" fix="pass --tls-cert and --tls-key"
```

That is fine on a loopback address and wrong anywhere else:

```sh
marstack server --listen 0.0.0.0:7443   --tls-cert /etc/marstack/tls/server.pem --tls-key /etc/marstack/tls/server-key.pem
```

TLS 1.2 is the floor. Clients trust a private CA by pointing at it, and there
is deliberately no flag to skip verification — an encrypted channel to a server
you did not authenticate proves nothing:

```sh
export MARSTACK_ENDPOINT=https://control.internal:7443
export MARSTACK_CA_FILE=/etc/marstack/tls/ca.pem
marstack node list

sudo marstack agent --name bm-1 --ca-file /etc/marstack/tls/ca.pem
```

For a lab, one self-signed certificate is enough. It is its own CA, so the same
file goes to `--tls-cert` and to `--ca-file`:

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1   -nodes -days 365 -keyout server-key.pem -out server.pem   -subj "/CN=marstack" -addext "subjectAltName=IP:192.168.107.2,DNS:localhost"
```

The `subjectAltName` must name the address clients actually dial. A certificate
with only a common name is rejected by every current client.

## Running it

```sh
make build
./bin/marstack server
```

In another shell:

```sh
export MARSTACK_ENDPOINT=http://127.0.0.1:7443

marstack instance create --name api-1 --image alpine:3.20
marstack instance create --name db-1 --isolation vm --image ubuntu-24.04 --vcpu 4 --memory-mib 4096
marstack instance list
marstack instance stop  i-php2q13mwt3qy
marstack instance list --output json
```

```
NAME    ID                ISOLATION   IMAGE          VCPU   MEMORY   DESIRED   OBSERVED   NODE
api-1   i-php2q13mwt3qy   container   alpine:3.20    1      512Mi    stopped   pending    -
db-1    i-cbv25sa6y2z40   vm          ubuntu-24.04   4      4096Mi   running   pending    -
```

`OBSERVED` stays `pending` until an agent picks the instance up: the control plane records intent,
and only a node reports reality back.

Run an agent to make the node itself visible:

```sh
marstack agent --name bm-1 --zone rack-a
```

```
NAME   ID                STATUS   ZONE     ARCH    CPUS   MEMORY   AGENT
bm-1   n-ybttrrpbkargc   ready    rack-a   arm64   4      5910Mi   0.0.1-dev
```

A node is `ready` while its last heartbeat is recent and `unreachable` otherwise. Nothing in the
control plane marks nodes down on a timer; the status is derived when it is read.

Heartbeats run on their own goroutine, separate from reconcile. The two work on different time
scales and must not share one:

```
node considered ready within   30s
stopping a VM (grace period)   30s
pulling a large image          tens of seconds
```

With both on one loop, a node stopping a single VM stops heartbeating for as long as the window
itself, so a node doing exactly what it was asked looks dead and its instances become candidates for
rescheduling.

With nodes registered, the scheduler fills in the `NODE` column, spreading instances across the
least loaded ready nodes:

```
NAME      ID                ISOLATION   IMAGE         DESIRED   OBSERVED   NODE
api-1     i-ps6r4jbnmnjfp   container   alpine:3.20   running   pending    n-t96s3m9vg9qdj
api-2     i-xbt6s6pztt8n4   container   alpine:3.20   running   pending    n-t28xwvyj096g6
db-1      i-56039am40f0nt   container   alpine:3.20   running   pending    n-t96s3m9vg9qdj
cache-1   i-v3j0c58rhr16g   container   alpine:3.20   running   pending    n-t28xwvyj096g6
```

An instance created while no node is ready simply waits, and is placed on the next pass after a
node registers.

## Running a container

The node pulls the image and takes the command from it, so nothing else is needed:

```sh
sudo marstack agent --name bm-1 --zone rack-a
marstack instance create --name web --image nginx:alpine
```

```
INFO image ready       command="[/docker-entrypoint.sh nginx -g daemon off;]"
INFO container started command="[/docker-entrypoint.sh nginx -g daemon off;]"
```

Override it after `--`, like `docker run`:

```sh
marstack instance create --name shell --image alpine:3.20 -- /bin/sh -c 'sleep 3600'
```

```
INFO pulling image  image=registry-1.docker.io/library/nginx:alpine
INFO image ready    image=registry-1.docker.io/library/nginx:alpine layers=8
```

Layers are verified against their digest and cached under
`/var/lib/marstack/cache/blobs`, so a second instance from the same image starts without
downloading anything. Dropping a root filesystem archive in `/var/lib/marstack/images/<name>.tar.gz`
still works and takes precedence, which is the offline path.

```
NAME       ID                ISOLATION   IMAGE         DESIRED   OBSERVED   MESSAGE
web-1      i-bfyv2jzjrq6dp   container   alpine:3.20   running   running    -
broken-1   i-s2tr5qz81hcnw   container   nginx:1.27    running   failed     image nginx:1.27 is not present on this node
```

`OBSERVED` is now the node's report, not a guess. What the kernel says about a running instance:

```
$ sudo readlink /proc/$PID/ns/pid          pid:[4026532433]     ← its own PID namespace
$ sudo cat /proc/$PID/cgroup               0::/marstack/i-bfyv2jzjrq6dp
$ cat /sys/fs/cgroup/marstack/$ID/memory.max   536870912        ← 512Mi enforced
$ cat /sys/fs/cgroup/marstack/$ID/cpu.max      100000 100000    ← one vCPU
```

## Placement

Instances sharing a placement group are spread apart:

```sh
marstack instance create --name web-1 --isolation container --image alpine:3.20   --placement-group web
marstack instance create --name web-2 --isolation container --image alpine:3.20   --placement-group web
```

Zones come first, then nodes. A zone is the failure domain a group exists to
survive, so the scheduler fills the zone holding no member before it looks at
individual nodes. After that it falls back to what it did before: most free
memory, then fewest instances.

Members placed in the same pass do not collide — the scheduler counts what it
has just assigned, not only what the database already knew.

By default the spread is a preference. With `--placement-strict` it is a
guarantee, and an instance that cannot be placed without breaking it stays
pending with the reason on the instance:

```
NAME    DESIRED   OBSERVED   MESSAGE
web-2   running   pending    every ready node already runs a member of placement group web
```

That choice is the operator's: a strict group in a two-node fleet will refuse
to run a third replica, and a soft one will double up rather than stall. Neither
is right for everyone, so neither is the hidden default.

## Getting into a VM

A VM used to be reachable only through the serial console, with a password the
platform generated — cloud-init was told `ssh_pwauth: false` and given no keys,
so ssh was locked with nobody holding a key. Keys are a project resource now:

```sh
marstack key add --name laptop --file ~/.ssh/id_ed25519.pub
marstack instance create --name box --isolation vm --image ubuntu-24.04 --key laptop
```

```
NAME     TYPE          FINGERPRINT                                    COMMENT
laptop   ssh-ed25519   SHA256:kVc8u1D1hbUJ7hRbP4bxjq1sVpqzTk8B0Z...   umar@laptop
```

The fingerprint is the one `ssh-keygen -lf` prints, taken over the key body
alone — so the same key added under two names fingerprints identically, and
naming both on one instance installs it once rather than writing it twice into
`authorized_keys`.

Three things are refused rather than quietly accepted:

- **A key on anything but `vm`.** Containers, microvms and sandboxes do not run
  cloud-init, so the key would be stored and never installed.
- **A key name that does not exist.** A typo would otherwise boot a machine
  nobody can log in to.
- **A private key.** If the file starts with `-----BEGIN`, it says so instead of
  storing your private key on a server.

Keys are resolved when the instance is created and the material travels with it,
because cloud-init runs once at first boot. Adding a key afterwards does not
reach a machine that has already booted, and deleting one does not lock you out
of a machine already carrying it. The serial console password still works and is
still written to `<runtime-root>/vms/<id>/console-login`, mode 0600.

## What the platform did while nobody was looking

The audit trail answers "who called what". It cannot answer "why did my instance
restart at 3am", because nothing called anything — the reconcile loop did it.
Until now that only existed in a log file on the node:

```sh
ssh bm-2 && sudo tail -f /var/log/marstack-agent.log      # the old answer
```

Events are the same information, from the API:

```sh
marstack event --limit 5
```

```
WHEN                  SEVERITY   KIND                 SUBJECT           MESSAGE
2026-08-28T06:24:50   warn       instance.restarted   i-xe25vnmf91kzw   restart 3: restarted after exited with code 127: /bin/sh: httpd: not found
2026-08-28T06:24:41   warn       instance.restarted   i-xe25vnmf91kzw   restart 2: restarted after exited with code 127: /bin/sh: httpd: not found
2026-08-28T06:24:31   warn       instance.restarted   i-xe25vnmf91kzw   restart 1: restarted after exited with code 127: /bin/sh: httpd: not found
2026-08-28T06:24:21   info       instance.running     i-xe25vnmf91kzw   observed pending to running
```

Five things record themselves today:

| Kind | When |
|---|---|
| `instance.placed` | the scheduler chose a node |
| `instance.running` / `.stopped` / `.failed` / `.restarted` | the observed state moved |
| `instance.rescheduled` | its node stopped answering and it was released |
| `instance.stranded` | its node stopped answering and its disk means it cannot move |
| `backup.ready` / `backup.failed` | a copy landed, or a node gave up on one |

Filter by what you are chasing:

```sh
marstack event --subject i-xe25vnmf91kzw     # one resource
marstack event --kind backend.down           # one kind of trouble
marstack event --severity error              # only the bad news
```

**Nothing posts an event.** The control plane derives them from transitions it
already stores: an instance's observed state changing, a balancer backend's
probe verdict flipping. That means one writer instead of one per node, and an
event cannot be a node's opinion — it is a difference between two rows the
platform already had.

It also means the definition of "a transition" has to be exact, and getting it
wrong is how a log becomes noise. An agent restarting forgets its restart
counters, so it re-reports every workload it adopts with the count reset to
zero. That is a changed row and not a changed workload, and it is deliberately
not an event: restarting an agent holding a dozen instances records nothing.

A placement held by a strict group is deliberately *not* an event: the scheduler
retries it every pass, so it would write a row every few seconds for as long as
the instance waits. The reason already lives on the instance itself. The same
thought applies to a workload stranded on a dead node, which the scheduler also
revisits every pass — that one is recorded, but only on the pass where it starts
being stranded, and again only if its node recovers and dies a second time.

Events are project-scoped like everything else, and a viewer can read them —
seeing why your own instance died is the least a read-only role should offer.

**It is a ring, not an archive.** The newest 20000 entries are kept and older
ones are dropped in the background. If you need events past that, take them out
to somewhere built for retention; this is here so an operator can answer a
question now, not to be a system of record.

## Saying something about a machine that the machine cannot say

A node reports what it is — arch, cpu count, memory. What it is *for* is an
operator's decision, and there was nowhere to put it:

```
$ marstack node label bm-1 disk=nvme tier=prod
NAME   STATUS   SCHEDULING   ZONE     ARCH    CPUS   MEMORY   AGENT       LABELS
bm-1   ready    open         rack-a   arm64   4      5910Mi   0.0.1-dev   disk=nvme,tier=prod

$ marstack node list --label tier=prod
bm-1

$ marstack node list --label disk=nvme --label tier=dev
no results
```

Repeating `--label` means **and**, never or. Two filters that read the same and
answer different questions is how somebody ends up trusting the wrong one.

**A node never labels itself.** There is no label field on register, and a node
token asking to set one gets a 403 with a test that says so. A node that could
assert its own labels could pull work to itself by claiming whatever a selector
asks for — the same reason registering again does not clear a cordon.

Setting labels replaces the whole set rather than merging, so a label goes away
by being left out. Merging would need a delete route and a convention for what
null means, and would make the call depend on what was there before.

## IPv6

A network can be IPv6 instead of IPv4:

```sh
marstack network create --name sixnet --cidr fd00:dead:beef::/48
marstack instance create --name six-web --isolation container \
  --image alpine:3.20 --network sixnet
```

```
# inside the container
eth0  inet6 fd00:dead:beef:1::2/64
default via fd00:dead:beef::1 dev eth0

# on the node
ip6 saddr fd00:dead:beef::/48 ip6 daddr != fd00:dead:beef::/48 ... masquerade
iifname "msv-js8ks6d5h0" ip6 saddr != fd00:dead:beef:1::2 drop

# published from another node, over IPv6
$ curl http://[fd61:5b69:...]:9443/
over-v6
```

Slices are /64 rather than /26 — a v6 subnet smaller than /64 works until something
guest-side assumes the standard. Address arithmetic is byte-wise now, so the same
allocator serves both families, and the last address of a slice is only reserved
for v4, which is the family that has a broadcast.

**A network is one family or the other, never both.** Dual stack means every
"what is this instance's address" question gets two answers, and DNS, published
ports, balancers and firewalls all ask it. One family per network keeps that
answer single-valued — and an instance can still sit on one of each, because it
can have more than one interface.

**Rules are rendered in the family of their address.** `ip daddr <v6 address>` is
a syntax error, and a ruleset that fails to load takes *every* instance's rules
with it. Published ports go into `table ip marstack_nat` or `table ip6
marstack_nat6`; a balancer whose backends are not all one family renders nothing,
because one nftables map holds one address type.

IPv6 networks must be under `fc00::/7`. Handing instances a globally routable
prefix should not be something you get by typing a CIDR.

### Two bugs this found

Verifying the above turned up two problems that had nothing to do with IPv6:

The masquerade rule was rendered from the **gateway** (`10.0.0.1/16`) while
nftables stores the masked network (`10.0.0.0/16`). The "is it already there"
check therefore never matched, and a duplicate was appended on every call. One
node had **2924** copies in a single chain. It renders from the masked prefix
now, and there is a test that both forms are identical.

Egress rules were only written when a workload *started*. An agent restart adopts
running workloads without calling `Start`, so after a restart those networks had
no masquerade rule until something happened to restart. They are now applied
every reconcile pass, which is what "the agent reconciles" was supposed to mean.

## Putting an instance on more than one network

An instance had one interface, and `nics.instance_id` was the primary key, so
that was true of the schema and not just the code. Now:

```sh
marstack instance create --name dualnic --isolation container \
  --image alpine:3.20 --network default --network backnet
```

```
# inside the running container
eth0  inet 10.20.0.76/26
eth1  inet 10.90.0.65/26

default via 10.20.0.1 dev eth0
10.20.0.1     dev eth0 scope link
10.90.0.1     dev eth1 scope link
```

**Exactly one default route.** Two would make egress depend on kernel
tie-breaking; the extra interfaces get an on-link route to their own subnet and
nothing else.

**Device 0 is still "the" address.** DNS, published ports, balancers and
firewalls all keep reading the first interface. The alternative is answering
"which address is the instance's address" in six modules, so the question is
answered once, in the query.

Three things had to learn about devices or the second interface would have
disappeared quietly: the link sweeper would have deleted its veth on the next
pass, delete would have leaked it, and — because anti-spoof rules are keyed on
interface name — an unlisted device gets **no** rule rather than a wrong one.
Checked live:

```
iifname "msv-nywswecfek4" ip saddr != 10.20.0.76 drop
iifname "msv-nywswecfe.1" ip saddr != 10.90.0.65 drop
```

Device 0 keeps its old interface name so an upgrade does not recreate the veth of
every running container.

VMs work too — one tap and one `virtio-net-pci` per interface, each with its own
mac, and cloud-init configuring both:

```
# on the node
mst-bj9xtv3vkg2      master msbr-w076ym7q
mst-bj9xtv3vk.1      master msbr-bfep6a55

$ ping 10.20.0.75   # eth0
0% packet loss
$ ping 10.95.0.66   # eth1
0% packet loss
```

The extra interface needs a **link-scope route to its own gateway**, because the
gateway sits outside the /26 slice the guest gets. Without it the guest answers
ARP on that interface and nothing else — which looks exactly like an interface
that was never configured, and cost an hour to tell apart.

Writing that route as `to: <address>/<prefix>` instead is worse: the guest address
has host bits set, netplan refuses the file, and *every* interface including eth0
is left unconfigured. A wrong route on eth1 takes the whole guest off the network.
Both forms are tested now.

Microvms and sandboxes too. The three VMM paths differ only in how an interface
is declared:

| | how a second interface is added |
|---|---|
| qemu | another `-netdev` / `-device virtio-net-pci` pair |
| Cloud Hypervisor | another `--net tap=…,mac=…` |
| Firecracker | another entry in `network-interfaces` |

Each takes the mac of *that* device. The guest side differs too: a vm gets
cloud-init, while a microvm's init script loops over `MS_NICS` and reads
`MS_IP_n` / `MS_GW_n`. Only device 0 adds a default route, and the resolver stays
singular either way.

Checked live on all four isolations, both interfaces answering.

## Terminating TLS at the balancer

A balancer was an nftables rule: the kernel rewrites the destination and never
looks at the bytes. That is fast and it cannot terminate TLS, because there is no
TLS in nftables. So a balancer with a certificate stops being a rule:

```sh
marstack balancer certificate set lb-y3w8wjwe78592 \
  --cert-file cert.pem --key-file key.pem
```

```
NAME     LISTEN     TARGET   ALGORITHM     CHECK         TLS                    BACKENDS
secure   8443/tcp   80       round_robin   vm liveness   secure.marstack.test   2/2 up
```

On the node, the port moves from the kernel to the agent:

```
# with a certificate
LISTEN *:8443 users:(("marstack",pid=422656))     # userspace
nft: no rule for dport 8443

# eight https requests
4 tls-a
4 tls-b

# after certificate remove
LISTEN: nothing on 8443
nft: tcp dport 8443 ... dnat to numgen inc mod 2 map { 0 : 10.20.0.76 . 80, 1 : 10.20.0.79 . 80 }
```

Both directions were checked live, including that the certificate presented is
the one that was uploaded.

**A balancer is never both.** A userspace listener and a dnat rule on one port is
the same undefined situation as two nftables rules matching one `dport`, so a
balancer with a certificate is deliberately left out of the published set.

**One difference worth knowing:** the userspace listener answers traffic that
starts on the node; the nftables rule does not, because prerouting is not on the
local output path. A local `curl` is not a valid check of a plain balancer — that
is a property of dnat, and it caught me out during this work.

The private key is sealed with the operator key, served only to nodes, and never
returned to an operator — the response carries the subject and expiry instead.
`tls.X509KeyPair` runs when the certificate is attached, so a key that does not
match its certificate fails there rather than at 3am when a connection arrives.
As with env, a control plane with no sealing key refuses the certificate outright.

Membership changes swap an atomic pointer rather than restarting the listener.
Restarting would drop every live connection each time a replica came or went,
which is exactly when connections matter.

## Changing an instance's size without rebuilding it

An instance was the size it was created at. Now:

```
$ marstack instance resize i-t3tzmk3cn84me --vcpu 2 --memory-mib 1024
NAME      ID                SIZE          DESIRED   OBSERVED
sizebox   i-t3tzmk3cn84me   2cpu/1024Mi   running   running
```

```
# on the node, before and after
memory.max: 536870912      →  1073741824
cpu.max:    100000 100000  →  200000 100000
pid:        419289         →  419289
```

The pid is the point: **a container takes the new limits without restarting**,
because cgroup limits are writable while it runs.

A vm, microvm or sandbox does not. Nothing here hot-plugs cpu or memory into a
guest, so those keep the size they booted with until they are stopped and
started, and the CLI says so rather than letting you assume otherwise. That is
why resize is an optional interface the agent asks for rather than a method on
`Runtime` — three of the four drivers cannot honour it, and a driver that accepts
a call it cannot fulfil is the failure mode the L rule exists to prevent.

A resize claims only the **growth** against the quota. Claiming the absolute size
would double-count what the instance already holds and refuse a resize that fits;
claiming nothing would let a project grow past its limit one resize at a time.

## Not letting one client take the platform down

Every caller could send as fast as it liked, and each bogus token cost a hash
plus a lookup on the single SQLite connection. Both are fixed by one middleware:

```
$ 400 requests as fast as the loop goes
{200: 169, 429: 231}
Retry-After: 1
```

Buckets are **per caller**, so a hot loop in one client does not refuse everybody
else. The limiter runs **in front of** authentication — behind it, a flood of
wrong tokens would still reach the database, which is the thing being protected.
That means it keys on `sha256(secret)[:8]`, since the token id is not known yet,
and never on the secret itself.

The part that matters most is the cap. A limiter that allocates a bucket per
attacker-chosen key *is* the denial of service it exists to stop, so past 4096
tracked callers everyone new shares one bucket, idle callers are swept, and there
is a test that floods it with distinct keys.

`/healthz` is never limited: throttling the health check makes a busy platform
look like a dead one to whatever is watching it.

Defaults are 50 requests a second with a burst of 100 — invisible to an agent,
which sends a few a second. `--rate-limit 0` turns it off.

## Giving a workload its configuration

A workload needed everything baked into its image. `--env` fixes that, and
because env is where credentials go, it is not stored the way the rest is:

```sh
marstack instance create --name web --isolation container --image alpine:3.20 \
  --env DB_PASSWORD=... --env PORT=8080
```

```
$ marstack instance get i-b152c2hhgee9t -o json
  "env_names": ["DB_PASSWORD", "PORT"]      # names, never values
```

**Values are never served back.** The operator route returns the names only; the
node route returns the values, because the node is the only thing that has to
have them. That is why the node has its own response type rather than sharing
one — the same split as `/v1/usage` and `/v1/usage/nodes`.

**Values are sealed at rest** with the operator key, so a stolen database gives
up nothing. Checked live: the value never appears in the control plane's data
directory, the names do, and the container's process environment on the node has
it. `Instance` carries no plaintext env field at all, so leaking one into the
operator response does not compile.

**A control plane with no key refuses env entirely:**

```
$ marstack instance create --name web ... --env DB_PASSWORD=...
error: no_sealing_key: this control plane has no key to seal an environment
with, and storing credentials in the clear is not something it will do quietly:
start it with --backup-key-file, or leave env off
```

Storing them anyway with a warning in a log would be the usual compromise. A
warning nobody reads is not protection, and env is new enough that nothing
depends on a plaintext path.

Whole files work the same way:

```sh
marstack instance create --name web ... \
  --file /etc/app/app.conf=./app.conf:0640
```

```
# on the node, inside the container's rootfs
-rw-r----- 1 root root 54 /var/lib/marstack/instances/i-.../rootfs/etc/app/app.conf
```

Content is sealed like env and never served back; the **paths** are readable, so
`file_paths` still tells an operator what a workload was given. Checked live: the
file arrived with the right mode and content, and the content appears nowhere in
the control plane's data directory.

Files are written through `os.OpenRoot` on the rootfs rather than by joining
paths. An image is untrusted input — ship one whose `/etc` is a symlink to `/`
and a joined path drops the caller's config onto the host instead. There is a
test that builds exactly that image.

For a container the variables are merged over the image's own, replacing rather
than shadowing. For a vm there is no single process to hand an environment to,
so cloud-init writes `/etc/marstack/environment` at 0600 for the guest to source
— that file, like the console password already there, is in the clear on the
node.

## Placing a workload where it has to go

Labels only matter if something reads them. `--node-selector` is that:

```
$ marstack instance create --name pinned-prod --isolation container \
    --image alpine:3.20 --node-selector tier=prod

$ marstack instance get i-wp071ce286gw0 -o json | grep -A2 node_selector
  "node_selector": { "tier": "prod" },
  "node_id": "n-tcvkwmvckcq62"        # bm-1, tier=prod
```

That node was the **busier** of the two — 14 instances against 6. Load balancing
alone would have chosen the other one, which is what makes it a real check
rather than a coincidence.

A selector filters candidates before anything is scored. It is a requirement,
not a preference: scoring it would let a node that does not match at all win on
being cheaper.

**Nothing falls back.** Ask for a label no node carries and the instance waits,
and says what it is waiting for:

```
$ marstack instance create --name pinned-tape ... --node-selector disk=tape
node    : (none)
message : no ready node carries disk=tape
```

A workload asking for an nvme disk quietly placed on a machine without one is
worse than one that waits and tells you. The wait is a reason rather than a
state, so labelling a node later places it on the next pass — no retry, no
resubmit. That was checked live: labelling `bm-1` with `disk=tape` picked the
held instance up within one interval.

A service takes the same selector, so a replica set can be pinned too:

```sh
marstack service create --name prodpin --replicas 3 \
  --image alpine:3.20 --node-selector tier=prod
```

```
prodpin-awh5jc   n-tcvkwmvckcq62
prodpin-7at164   n-tcvkwmvckcq62
prodpin-aqsn7t   n-tcvkwmvckcq62
# bm-1, tier=prod, 9 instances — the busier of the two
```

The service module validates the selector when the service is created rather than
letting each replica be refused later. A service that accepts a selector it can
never use records `blocked` every pass forever, and says nothing at the moment
somebody typed it.

## Reading a list without reading all of it

Every list endpoint returned the whole table. That is fine at twenty instances
and a large response at ten thousand, and it grows on its own.

`/v1/instances`, `/v1/volumes`, `/v1/backups` and `/v1/snapshots` take `limit`
and `after`:

```
$ curl ".../v1/instances?limit=3"
3 rows, next = MjAyNi0wOC0yNlQwNjoyOToy...
   web-1  web-2  web-3

$ curl ".../v1/instances?limit=3&after=$NEXT"
   web-4  mv-b  mv-c
```

The cursor is opaque and carries the row's ordering column **and** its id. The
ordering column alone is not a total order, and without the tiebreaker a row on a
page boundary comes back twice — which is what happens if you remove it, so there
is a test that does. It is not a theoretical case: seven backups created in a row
share a timestamp to the nanosecond, and cutting the tiebreaker makes that test
fail immediately.

Volumes page by name, instances and snapshots by age, backups newest first. That
last one flips both the comparison and the `ORDER BY` to descending, which is the
easy half of this to get wrong.

`next` appears only when a page came back full. An empty `next` means the list is
exhausted, so a caller loops until it disappears rather than guessing. The price
of not counting rows is one empty request when the total happens to be a multiple
of the limit.

**The CLI and the console follow the cursor**, so `marstack volume list` still
shows everything. One loop in `internal/cli/client.go` does it for all four; a
list command that silently stopped at a hundred would be worse than no paging at
all.

**What is still unpaged.** The lists hanging off a single volume —
`/v1/volumes/{id}/backups` and `/v1/volumes/{id}/snapshots` — return the whole
set, because retention bounds them. DNS records are derived from instances rather
than stored in a table of their own, so this cursor does not apply to them; when
instances are paged, the thing behind the records already is. Everything else is
small by construction: nodes, networks, images, projects.

**One honest wart.** Ordering compares RFC3339Nano strings, and those sort
chronologically except for a timestamp landing on an exact whole second, where
`Z` sorts after `.`. Fixing it means rewriting every stored timestamp, and a
column holding two formats would be worse than a one-in-a-billion row landing on
the wrong page.

## Putting backups somewhere that outlives this machine

Backups lived on the control plane's own disk, which is the single point of
failure they exist to survive. Point them at MinIO, or anything else speaking
S3, and they stop dying with it:

```sh
export MARSTACK_OBJECT_STORE_SECRET_KEY=...
marstack server \
  --object-store-endpoint http://minio.internal:9000 \
  --object-store-bucket backups \
  --object-store-access-key marstack
```

```
level=INFO msg="backups go to an object store" where="bucket backups on http://minio.internal:9000"
level=INFO msg="backups are sealed at rest" key=d1664bd55f2cab68 keys_held=1
```

The bucket is checked on start, so a wrong endpoint or a missing bucket stops the
control plane then rather than the first time a backup runs at 3am.

**No new dependency.** The S3 request signing is about two hundred lines of
standard library. The AWS SDK is tens of modules, and `docs/ENGINEERING-PRINCIPLES.md` is explicit
that every dependency is surface the agent carries onto customer baremetal.
Verified against real MinIO: a backup written, listed in the bucket, pulled back
out through the control plane, and byte-identical to the volume it came from.

```
$ mc ls --recursive local/backups
[2026-08-29 08:21:47] 576KiB STANDARD backups/bkp-az9jes5mb0472

$ sha256sum original.qcow2 pulled.qcow2
449b92b68bbf7db5...  original.qcow2
449b92b68bbf7db5...  pulled.qcow2
```

### The node writes the bytes, not the control plane

With an object store configured, a backup never passes through the control plane.
The node asks where to put it, gets a presigned PUT and a key, seals the stream
itself and uploads:

```
level=INFO msg="backup written straight to the object store" backup=bkp-6s874xxpc582e bytes=589824
```

The key is **per backup**, minted by the control plane and wrapped with the
operator key. The node is handed the unwrapped one for that single backup, which
is the same shape volume encryption already uses. **The operator key still never
leaves the control plane**, so the alternative — shipping it to every node — is
avoided rather than accepted.

The presigned url is a bearer capability for one object and one verb, expires in
thirty minutes, and cannot be edited to name another object: MinIO refuses a url
whose key has been changed, because the signature covers it.

Reading is unchanged from a caller's side. The control plane fetches the object,
unwraps the content key, unseals, and streams. Verified end to end: the object in
the bucket starts `MSBK` rather than a qcow2 magic, so it really is sealed, and
what comes back out is byte-identical to the volume it came from.

Two things are worth knowing. **The size and checksum are the node's word now** —
the control plane no longer sees the bytes, so it cannot compute them. The AEAD
still detects corruption when the backup is read, which is the guarantee that
matters, but the recorded numbers are a report rather than a measurement. And
with no object store configured nothing changes: the control plane says so, and
the node falls back to streaming through it.

The secret key comes from `MARSTACK_OBJECT_STORE_SECRET_KEY` rather than a flag,
because a secret on the command line is a secret in the process list.

Requests are signed with `UNSIGNED-PAYLOAD`. Signing the payload means either
buffering a multi-gigabyte volume in memory or implementing chunked signing, and
neither buys much here: the backup is sealed before it is uploaded, so its AEAD
is what actually detects tampering, not the S3 signature.

## Telling somebody an event happened

Events could only be asked for. A platform that knows a workload is restarting in
a loop but cannot say so is half useful, so an endpoint can subscribe:

```sh
marstack webhook create --name ops --url http://192.168.107.2:9999/hook --kind "instance.*"
```

```
NAME   ID                 STATE    KINDS        URL
ops    wh-panss0sas12n0   active   instance.*   http://192.168.107.2:9999/hook

signing secret: whsec_...
this is the only time it is shown
```

Each delivery is a POST signed with HMAC-SHA256 of the body in
`Marstack-Signature`, verified end to end against a real receiver:

```
instance.placed      sig_valid=True  {'kind': 'instance.placed',    'subject': 'i-0s5f...', 'attempt': 1}
instance.running     sig_valid=True  {'kind': 'instance.running',   'subject': 'i-0s5f...', 'attempt': 1}
instance.restarted   sig_valid=True  {'kind': 'instance.restarted', 'subject': 'i-0s5f...', 'attempt': 1}
```

`instance.*` matches a family; naming no kinds means everything in the project.

**Nothing pushes into the webhook module.** It walks the event table from a
persisted cursor, so recording an event never waits on HTTP, a restart resumes
where it stopped, and an event is fanned out exactly once. A new subscription
starts from now rather than replaying history.

Delivery is queued and retried with a growing backoff, then given up on:

```
WHEN                  KIND                 STATE       TRIES   WHY
2026-08-28T16:59:06   instance.restarted   delivered   1       -
2026-08-28T17:00:11   instance.restarted   pending     1       dial tcp 192.168.107.2:9999: connect: connection refused
2026-08-28T16:59:36   instance.restarted   pending     3       dial tcp 192.168.107.2:9999: connect: connection refused
```

A target that is briefly down loses nothing; a target that is gone stops being
retried instead of queueing forever.

### The URL is caller-chosen, so it is a way in

Posting to an address somebody else picked makes the control plane a request
forwarder. Three things stop that being useful to an attacker:

- **Loopback and link-local are refused at the dial**, not at create. Checking
  the URL then connecting is a race a DNS answer can win, so the check runs on
  the address actually being connected to:

```
$ marstack webhook deliveries loopback
KIND                 STATE     TRIES   WHY
instance.restarted   pending   1       refusing to post to loopback: that is the control plane itself
```

- **Redirects are not followed.** A `302` to `127.0.0.1` would otherwise walk
  straight around the check.
- **No credentials are ever attached**, and credentials in the URL are refused
  at create rather than silently dropped.

Private ranges *are* allowed, because that is where a private fleet lives. The
addresses worth refusing are the ones that mean something specific: the control
plane itself and a metadata service.

**The signing secret is stored recoverable**, unlike an API token, because
signing needs it. A stolen control-plane database therefore exposes webhook
secrets, which is true of every webhook implementation and worth knowing rather
than assuming otherwise.

## Asking what happened earlier

Usage used to be one row per subject: the latest sample and nothing else. There
was no way to ask whether something was busy an hour ago.

Samples are now folded into one-minute buckets as they arrive:

```sh
marstack usage history n-tcvkwmvckcq62 --window 30m
```

```
n-tcvkwmvckcq62 over 30m0s, 5 buckets

cpu     ▁▁▁▁▁  avg 1.9%  peak 2.1%
memory  ▃▃▃▃▃  avg 2247Mi  peak 2257Mi

oldest 2026-08-28T10:02:00   newest 2026-08-28T10:06:00
```

Downsampling on write is what makes this affordable in SQLite: one UPSERT per
report into the current bucket, not one row per sample. Each bucket keeps the
sample count, the running sum and the peak, so average and peak are both real
rather than one being guessed from the other. **Peak is the number that matters
for capacity** — an average hides the spike that mattered.

A day is kept and older buckets are dropped in the background. It is a ring, not
an archive; point a real metrics stack at it if you need to keep more.

Instance history is scoped to your project and node history is administrative,
the same split the snapshot uses. Asking for a node through the instance route
answers 404 rather than 403, like everywhere else.

**Memory is reported in MiB, which rounds a small container to zero.** An idle
container is genuinely about 120 KiB — a busybox shell and nothing else — so it
reads `0 MiB` while a vm reads hundreds. The number is accurate rather than
missing: a container pinned at a busy loop reports `99.0%` cpu and its cgroup
memory reads back exactly, it just cannot be expressed in whole MiB. If seeing
small containers matters, the unit is the thing to change, not the sampler.

## Taking a node out for maintenance

A node used to lose its workloads exactly one way: by stopping answering. To
work on a machine you had to kill its agent and wait out the fence grace, which
is an outage you caused on purpose and cannot undo quickly.

`cordon` stops new placement and touches nothing that is already running:

```sh
marstack node cordon n-2eb567x5k56ta
```

```
NAME   STATUS   SCHEDULING   ZONE     ARCH    CPUS   MEMORY
bm-2   ready    cordoned     rack-b   arm64   4      5910Mi
```

`drain` cordons and moves what can move:

```sh
marstack node drain n-2eb567x5k56ta
```

It returns as soon as the intent is recorded — the scheduler does the moving on
its next pass, the same way creating an instance does not mean it is running
yet. Watch the node until `SCHEDULING` stops saying `draining`.

**Only containers move.** Anything whose disk lives on that node stays, and the
drain says so rather than finishing and leaving you to notice:

```
$ marstack event --kind instance.drain_blocked
SEVERITY   KIND                     SUBJECT           MESSAGE
error      instance.drain_blocked   i-h7pdqctweez1m   its node is draining, and isolation vm cannot be moved without losing its disk
```

A node with something stuck on it therefore keeps saying `draining` forever,
which is the honest answer: the drain has not finished. Stop or delete those
workloads, or accept the node is not empty.

`uncordon` reopens the node and cancels a drain in progress. Anything already
moved stays where it went.

A cordon **survives the agent restarting**. Re-registering does not touch the
flag, because a machine somebody is working on should not start taking work
again just because its agent came back.

### What it composes with

Draining a node holding a service replica is where the last three features meet.
The replica is released, the scheduler places it elsewhere, and it gets a new
address from the new node's slice — the balancer's nftables map picks that up on
its next read:

```
mod 2 map { 0 : 10.20.0.75 . 80, 1 : 10.20.0.137 . 80 }   # one replica on bm-2
mod 2 map { 0 : 10.20.0.75 . 80, 1 : 10.20.0.78  . 80 }   # after draining bm-2
```

Nothing synced that address. Membership and addresses are both resolved when the
balancer is read, so a moved replica cannot leave a stale entry behind.

## Holding a replica count

A placement group spreads replicas and a balancer fronts them, but until now you
made each replica by hand. Nothing held the number: delete one and it stayed
deleted.

A service is a workload template plus a count, and a loop keeps them equal:

```sh
marstack service create --name pool --replicas 3 --isolation container \
  --image alpine:3.20 --placement-group pool -- /bin/sh -c "sleep 3600"
```

```
NAME   ID                  REPLICAS   ISOLATION   IMAGE         STATE
pool   svc-6aq44wyseeh48   3          container   alpine:3.20   3/3 up
```

Delete a replica by hand and it comes back:

```sh
marstack instance delete i-n2kjhb4y3ckm6
marstack event --kind service.replica_lost
```

```
WHEN                  SEVERITY   KIND                   MESSAGE
2026-08-28T08:46:31   warn       service.replica_lost   replica of pool no longer exists, making a replacement
```

`marstack service scale pool 1` goes the other way, and it removes the
**newest** replicas first — killing the one that has been serving longest to
satisfy an arithmetic change is the wrong instinct. Deleting the service deletes
its replicas with it.

Four choices are worth naming:

- **Replica names carry a random suffix**, not an ordinal. `pool-1` would collide
  with an instance somebody made by hand and wedge the service forever; the
  instance id is the real handle anyway.
- **Membership lives in the service module**, not as an owner column on
  instances. Nothing else had to change shape, and the loop has to check
  membership against reality every pass regardless.
- **At most four replicas are created per pass.** A service asked for thirty
  should not hand the scheduler thirty placements in one tick.
- **A service that cannot grow says why and keeps what it has.** Hit a quota and
  it stops at the limit, records the reason, and clears it by itself when the
  limit moves:

```
NAME   REPLICAS   STATE
pool   3          stuck: quota_exceeded: the project is limited to 20 of instances and already holds 20
```

### Pointing a balancer at it

A balancer can take its backends from a service instead of a list of instance
ids:

```sh
marstack lb create --name front --target-port 80 --listen-port 8080 \
  --service pool --check tcp
```

```
NAME    LISTEN     TARGET   ALGORITHM     CHECK       SOURCE         BACKENDS
front   8080/tcp   80       round_robin   tcp (1/2)   service pool   2/2 up
```

Scale the service and the nftables map on every node follows, with nothing
registered by hand:

```
mod 2 map { 0 : 10.20.0.75 . 80, 1 : 10.20.0.137 . 80 }                        # replicas 2
mod 3 map { 0 : 10.20.0.75 . 80, 1 : 10.20.0.137 . 80, 2 : 10.20.0.74 . 80 }   # scaled to 3
mod 1 map { 0 : 10.20.0.75 . 80 }                                              # scaled to 1
```

Membership is **derived, not synced**. The balancer asks the service who its
replicas are every time it is read, so there is no copy to go stale and no
ordering between two loops to get wrong. It also means the health check applies
to replicas exactly as it does to hand-listed backends.

Because the service owns the set, editing it by hand is refused rather than
silently overwritten:

```
$ marstack lb add front i-1pjz70vvajd1g
error: backends_owned_by_service: the backends of front come from service pool,
       so scale that service instead
```

Naming both `--service` and `--instance` is refused for the same reason: two
sets of expectations, one of which would quietly lose.

If the service is deleted, the balancer keeps its port and serves nothing. It
still names the service it followed, so the answer to "why is this empty" is one
line away. Refusing to delete a service that a balancer points at would mean the
service module knowing about balancers, and the dependency is deliberately only
one way.

## Spreading one port across replicas

A placement group keeps replicas off one machine, but on its own that only
halves the damage: a client still talks to one instance, and losing that
instance loses those requests. A balancer puts one port in front of the set.

```sh
marstack lb create --name pool --target-port 80 --listen-port 8080 \
  --instance i-mf7wfr80mhe8r --instance i-d70vdse8nb8gg
marstack lb get pool
```

```
INSTANCE          ADDRESS       STATE
i-d70vdse8nb8gg   10.20.0.133   up
i-mf7wfr80mhe8r   10.20.0.76    up
```

Every node claims the listen port and rewrites arriving packets with one
nftables rule:

```
tcp dport 8080 ct mark set 0x1 dnat to numgen inc mod 2 \
  map { 0 : 10.20.0.133 . 80, 1 : 10.20.0.76 . 80 }
ct mark 0x1 masquerade
```

`numgen inc` walks the map, so consecutive connections land on consecutive
backends. `--algorithm source_hash` swaps it for `jhash ip saddr`, which keeps
one client on one backend for as long as the set does not change — the choice
between spreading load and holding a session.

**There is no single virtual address.** A VIP that survives a node dying needs
anycast and ECMP from the router, or VRRP between the nodes; neither exists
here. What exists is that the same port answers on every node, so any node
address is an entry point and losing one costs only the clients that were using
that one. That is the same trade a Kubernetes NodePort makes, and it is named
here rather than hidden.

The masquerade is load-bearing and it costs something. A backend chosen on
another node would otherwise reply straight to the client, from an address the
client never dialled, and the connection would never establish — this was
measured: seven of ten requests failed before the masquerade was added, exactly
the ones the map sent across the node boundary. The cost is that a balanced
backend sees the node's address rather than the client's. Single-target
published ports are deliberately left unmarked, so they still see the real
client.

A backend only takes traffic while its instance is observed running, and the map
shrinks and grows on its own:

```
tcp dport 8080 ... mod 1 map { 0 : 10.20.0.76 . 80 }     # after one was stopped
tcp dport 8080 ... mod 2 map { 0 : ..., 1 : ... }        # after it came back
```

Deleting an instance drops it from every balancer it was in.

### When running is not the same as serving

By default a backend counts as up while its instance is observed running, which
a process that is running but wedged still satisfies. `--check` replaces that
with a probe:

```sh
marstack lb create --name checked --target-port 80 --listen-port 8080 \
  --check http --check-path /healthz --rise 1 --fall 2 \
  --instance i-a --instance i-b --instance i-wedged
```

```
INSTANCE   ADDRESS       STATE   PROBE     WHY
i-a        10.20.0.133   up      passing   -
i-b        10.20.0.76    up      passing   -
i-wedged   10.20.0.77    down    failing   http request failed or timed out
```

Meanwhile the instance itself still reads healthy, which is the whole point:

```
NAME     DESIRED   OBSERVED   MESSAGE
wedged   running   running    -
```

The node holding an instance is the one that probes it, once per reconcile pass,
and reports a verdict. That keeps one prober per backend and reuses the channel
the agent already reports status on. It also means a partition between one node
and a backend on another is invisible: the node holding it says "passing" and
every other node keeps sending traffic into a hole. Per-node probing would catch
that and is not built.

`--rise` and `--fall` are how many consecutive results it takes to change the
verdict, defaulting to 2 and 2. A wobbling backend therefore keeps its traffic
until it has failed `fall` times in a row, and says so while it wobbles:

```
i-wedged   10.20.0.77   up   passing   failed 1 of 2 needed to go down, still up: ...
```

Three things are deliberate:

- **A checked backend starts down.** Until a probe has run it reads `unknown`
  and takes no traffic. Trusting something nobody has asked yet would defeat
  the feature on the one pass where it matters most.
- **A verdict expires.** If the node holding a backend stops reporting for
  longer than the grace, the backend goes `unknown` rather than staying up on a
  stale answer. Silence is not health.
- **Re-adding a backend forgets its verdict.** Removing and re-adding an
  instance clears the stored report, so it has to earn `up` again instead of
  inheriting one.

The probe is HTTP or a bare TCP connect, with 200–399 counting as healthy and
redirects not followed. An http check needs a tcp balancer. There is no
per-check interval: probes ride the reconcile pass, so the loop that already
paces the platform paces them too.

A balancer holds its listen port on every node, so it cannot share one with a
published port or another balancer. Both modules refuse the collision at create
time rather than letting two nftables rules race for the same `dport`.

## Networking

An instance is given an address when it is placed, and the agent wires it before the workload runs:

```
$ nsenter -t $PID -n ip -brief addr show
lo         UNKNOWN   127.0.0.1/8
eth0@if9   UP        10.20.0.65/26

$ nsenter -t $PID -n ip route show
default via 10.20.0.1 dev eth0
10.20.0.1 dev eth0 scope link
10.20.0.64/26 dev eth0 proto kernel scope link src 10.20.0.65

$ nsenter -t $PID -n ping -c2 1.1.1.1        # egress through the node
$ nsenter -t $PID -n ping -c2 10.20.0.66     # the other container on this node
```

Addresses come from a per-node slice of the network, so routes aggregate per node rather than per
instance:

```
10.20.0.0/16     network default
  10.20.0.0/26     reserved for the gateway and platform addresses
  10.20.0.64/26    node bm-1     → instances get .65, .66, ...
  10.20.0.128/26   node bm-2
```

The gateway address lives on the bridge of every node, so an instance always talks to a local
gateway. Egress is masqueraded on the node the instance runs on; there is no central gateway to
bottleneck.

### Across nodes

Each node programs one route per peer slice, using the address the peer reported at registration:

```
bm-1$ ip route show | grep 10.20
10.20.0.0/16       dev msbr-mr94p0t0 proto kernel scope link src 10.20.0.1
10.20.0.128/26 via 192.168.107.3 dev lima0

bm-2$ ip route show | grep 10.20
10.20.0.0/16       dev msbr-mr94p0t0 proto kernel scope link src 10.20.0.1
10.20.0.64/26  via 192.168.107.2 dev lima0
```

A container on one node reaches a container on the other with its own source address intact — inter
node traffic is routed, not masqueraded:

```
bm-2$ tcpdump -ni any icmp
lima0            In  IP 10.20.0.66 > 10.20.0.130: ICMP echo request
msbr-mr94p0t0   Out  IP 10.20.0.66 > 10.20.0.130: ICMP echo request
msv-1qkjab6xkyz Out  IP 10.20.0.66 > 10.20.0.130: ICMP echo request
msv-1qkjab6xkyz   P  IP 10.20.0.130 > 10.20.0.66: ICMP echo reply
```

A container's address carries the **slice** prefix, not the network prefix, plus a link route to the
gateway. With the network prefix the container would treat the whole network as on-link and ARP for
addresses that live on another node.

Multi node needs `--address` on the agent: the address other nodes reach it on. It is not
auto-detected, because a host with several interfaces has no way to know which one its peers use.

### Losing a node

A node that stops reporting for longer than the scheduler's grace period has its **containers**
released and placed again elsewhere:

```
INFO released a workload from an unreachable node  instance=i-6t1z0... name=web-b node=n-0h79...
INFO instance placed                              instance=i-6t1z0... name=web-b node=bm-1
```

A VM is deliberately left where it is:

```
WARN leaving a workload on an unreachable node because moving it would lose its disk
     instance=i-yghcz... name=db-1 isolation=vm node=n-0h79...
```

A container's root filesystem is derived from its image, so starting it elsewhere loses nothing. A
VM has a copy-on-write disk on the node it ran on, and starting it elsewhere would silently hand
back a fresh disk from the base image. That is data loss dressed up as recovery, so it does not
happen.

There is no fencing. If the node was only partitioned rather than dead, its containers keep running
there until it reconnects, and it then removes them because the control plane no longer assigns them
to it. Two copies can overlap for that window. The grace period is what keeps the window rare, not a
guarantee that it cannot happen.

### Surviving a control plane outage

The agent writes the desired state it last read to `--state-dir` on every pass. If the control plane
is unreachable when the agent starts — a node rebooting during an outage — it replays that state so
workloads come back, then keeps trying to register:

```
WARN the control plane is unreachable at startup     error="..."
WARN reconciling from the cached desired state       node_id=n-... instances=3
```

A replay never reports anything: there is nothing listening, and a node must not act on its own
guesses about what the control plane thinks. An agent with no cache starts nothing rather than
inventing workloads.

The replay is deliberately partial. A `vm`, `microvm` or `sandbox` comes back, because the scheduler
never moves one and nobody else can be running it. A `container` does not: the scheduler re-places a
stranded container after two minutes, so a node that has been out of contact cannot know whether its
containers now belong to somebody else.

### Fencing a partitioned node

A node that cannot reach the control plane for a minute stops its own movable workloads:

```
ERROR fencing this node: the control plane has been unreachable long enough that it may hand
      this work to somebody else                    reason="silent for 1m20s" fence_after=1m0s
WARN  stopping a movable workload before it can run twice   instance=i-re23nf81qt5fp isolation=container
```

There is no IPMI here and no shared disk to poison, so the only honest fence is the node fencing
itself. That makes the deadline the whole design: the agent stops at 60 seconds and the scheduler
only re-places after 120, and a test asserts that ordering with a margin, because a partitioned node
still running work the control plane has already given away is the failure this exists to prevent.

An agent that has never reached the control plane since starting counts as silent. It cannot tell a
one second outage from a week, so it stops movable work rather than assuming the shorter one.

Fencing stops what the scheduler may move and leaves the rest running. Fencing a `vm` would cause an
outage without preventing anything, since no other node will ever be told to run it.

### Garbage collection

Reconcile runs in both directions. Anything on the node that the control plane no longer knows about
is removed: workloads whose instance is gone, their cgroups and directories, their veths, and
bridges for networks the node no longer serves.

```
INFO removing a workload the control plane no longer knows  instance=i-doesnotexist
```

Collection only runs after a successful read of the desired state. A control plane that cannot be
reached must never look like an empty cluster, and a test asserts that nothing is removed in that
case.

A workload that survives an agent restart is adopted rather than restarted, and says so:

```
NAME    ID                DESIRED   OBSERVED   MESSAGE
web-1   i-2ar4kjvfepncp   running   running    adopted after an agent restart
```

### Internal DNS

Every instance is resolvable at `<instance>.<network>.internal`, and the search domain makes the
short name work. The resolver runs on each node's gateway address, so a container always talks to a
local one:

```
$ cat /etc/resolv.conf
nameserver 10.20.0.1
search default.internal
options ndots:1

$ nslookup db
Name:    db.default.internal
Address: 10.20.0.130          # on the other node

$ ping -c2 db
2 packets transmitted, 2 packets received, 0% packet loss

$ nslookup dl-cdn.alpinelinux.org
Address: 151.101.130.132      # forwarded upstream
```

Names outside `.internal` are forwarded to the node's own upstream resolvers, with loopback
addresses skipped so the resolver can never forward to itself.

The resolver answers `A` queries only. A query for another type on a known name returns an empty
answer rather than `NXDOMAIN`, because `NXDOMAIN` makes a client give up on the name entirely — a
container asking for `AAAA` first would then never try `A`.

Not implemented yet: anti-spoof filtering, image pulling, restart policy, and `isolation: vm`.

## Continuous integration

Three jobs on every push and pull request: build, vet, race tests and a gofmt
check; a cross-compile for darwin/arm64 and linux/amd64; and the same security
scans the pre-commit hook runs.

`make check` runs the same cross-compile locally, because on macOS every other
check is blind to the Linux-only half of the runtime.

The cross-compile job earns its place. Runtime packages are split by build tag,
so a check run on one platform never compiles the other's files — `_linux.go`
code is invisible to a macOS `go vet`, and the non-Linux stubs are invisible on
the runner. Building both catches a stub that fell behind its interface.

`gosec` runs with six rules excluded, and the reason is the same for all of
them: they cannot tell a host file from a file being placed inside a guest.
`G204` fires on every `exec.Command` with computed arguments, which is what an
agent driving `qemu-img`, `ip` and `nft` does all day — and these are argv
arrays, not shell strings, so the injection the rule warns about is not
available. `G301`, `G302` and `G306` want every file at 0600, but
`internal/runtime/microvm` writes a guest's `/sbin/init`, which has to be 0755.
`G304` and `G703` fire on paths the agent composed itself from validated ids and
on the operator's own `--token-file`.

Excluding them was worth doing carefully rather than quickly. Reading the
findings first turned up two real defects the rules had buried: the cloud-init
seed ISO, which carries the console password, was world-readable at 0644, and so
was every volume file `qemu-img create` produced — a guest's whole disk readable
by any local process. Both are 0600 now.

Two things are deliberately absent. `gitleaks-action` needs a paid licence for
organisations, so CI installs the gitleaks binary and runs it directly. CodeQL
needs GitHub Advanced Security to upload results on a private repository, so its
job was removed rather than left permanently red — a job that can never pass
teaches people to ignore the build.

## Development

```sh
make hooks      # once per clone: install the pre-commit hook
make tools      # once: install staticcheck, govulncheck, gosec
make check      # vet, race tests, staticcheck, govulncheck, gosec
```

Architecture and its enforced boundaries: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).
Threat model and security invariants: [`docs/SECURITY.md`](docs/SECURITY.md).
Working agreement for changes: [`docs/ENGINEERING-PRINCIPLES.md`](docs/ENGINEERING-PRINCIPLES.md).

## Roadmap

```
 1  store + api + instance object                     done
 2  agent + reconcile loop, isolation: container      done
 3  netdev + nft + dns          → containers talk     done
 4  oci image store                                   done
 5  vmm/qemu                    → isolation: vm       done
 6  node join + routing         → multi node          done
 7  isolation: microvm and sandbox                    done
 8  serial console                                    done
 9  image catalog: disk, iso, kernel                  done
10  volumes                                           done
11  api authentication                                done
12  volume snapshots                                  done
13  fencing a partitioned node                        done
14  published ports + firewall                        done
15  usage metrics + load aware placement              done
16  audit trail                                       done
17  projects and per-project scoping                  done
18  off-node volume backup and restore                done
19  token lifetimes + TLS on the API                  done
20  per-project quotas + read-only viewer role        done
21  scheduled backups + retention                     done
22  backup encryption at rest                         done
23  encrypted volumes on the node                     done
24  backup and restore of encrypted volumes           done
25  volume resize, placement groups, disk hot-plug    done
26  ssh keys for vm instances                         done
27  load balancer across replicas                     done
28  health checked backends                           done
29  events: what the platform did on its own          done
30  services: hold a replica count                    done
31  a balancer that follows a service                 done
32  cordon and drain a node                           done
33  bounded usage history                             done
34  webhooks: tell somebody an event happened         done
35  backups on an S3 object store                     done
36  nodes write backups straight to the store         done
37  paged instance listing                            done
38  paged volume, backup and snapshot listings        done
39  operator labels on nodes                          done
40  placing a workload by node selector               done (instances, services)
41  sealed environment injection                      done
42  sealed config file injection                      done
43  per caller api rate limiting                      done
44  resize an instance's cpu and memory               done
45  tls termination on a balancer                     done
46  more than one network per instance                done
47  ipv6 networks                                     done
```

## License

Apache-2.0
