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
```

## License

Apache-2.0
