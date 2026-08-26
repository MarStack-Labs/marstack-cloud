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
| `microvm` | QEMU (v1) → Cloud Hypervisor (v2) | fast boot while keeping a private kernel |
| `sandbox` | Firecracker (v3) | ephemeral work, restored from a snapshot |

VMM names never appear in the API or the CLI.

## Staging node images

Do this once per node, before creating anything that is not a container.

```sh
make stage-images
```

Containers, microvms and sandboxes pull an OCI image from a registry at start.
A vm does not: it boots a qcow2 disk that has to be on the node already, and a
microvm boots an uncompressed kernel that has to be there too.

| Isolation | What the image field means | Where it comes from |
|---|---|---|
| `container` | OCI reference | pulled at start |
| `microvm` | OCI reference, unpacked into an ext4 root | pulled at start, plus `images/kernel.Image` |
| `sandbox` | OCI reference, unpacked into an ext4 root | pulled at start, plus `images/kernel.Image` |
| `vm` | file name of a staged disk | `images/<name>.qcow2`, staged by hand |

So `--isolation vm --image alpine:3.20` cannot work. It resolves to
`/var/lib/marstack/images/alpine_3.20.qcow2`, which nobody staged - `/`, `:`
and spaces become `_`. Use the name of a disk that is there:

```sh
sudo ls /var/lib/marstack/images
marstack instance create --name db-1 --isolation vm --image ubuntu-24.04
```

To stage a different distribution, drop the qcow2 in yourself:

```sh
sudo curl -fL -o /var/lib/marstack/images/debian-12.qcow2 \
  https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-arm64.qcow2
```

`make stage-images` is deliberately dumb: it downloads the Ubuntu cloud image
for the node architecture, extracts the running kernel to `images/kernel.Image`
for the microvm runtimes, verifies both are the format they claim to be, and
does nothing if they are already there. The agent never downloads a disk image
on its own - what a node boots stays an operator decision.

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
1  store + api + instance object
2  agent + reconcile loop, isolation: container
3  netdev + nft + dns          → containers can talk
4  image store
5  vmm/qemu                    → isolation: vm
6  node join + routing         → multi node
7  isolation: microvm
8  disk + snapshot
9  cloud hypervisor, then sandbox + firecracker
```

## License

Apache-2.0
