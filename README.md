# marstack-cloud

A cloud platform that runs containers, VMs, and microVMs as one kind of resource — on a single
node or across many baremetal machines, through the same code and the same API.

> Status: **very early.** Control plane skeleton only. Nothing runs an instance yet.

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

`OBSERVED` stays `pending` because nothing runs instances yet: the control plane records intent,
and no runtime reports reality back. That column becomes truthful once the agent runs workloads.

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

There is no image store yet, so the agent reads root filesystem archives from its own directory.
Put one there, then create an instance whose command follows `--`:

```sh
sudo mkdir -p /var/lib/marstack/images
sudo curl -fsSLo /var/lib/marstack/images/alpine_3.20.tar.gz \
  https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/aarch64/alpine-minirootfs-3.20.3-aarch64.tar.gz

sudo marstack agent --name bm-1 --zone rack-a
marstack instance create --name web-1 --image alpine:3.20 -- /bin/sh -c 'while true; do echo alive; sleep 2; done'
```

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

Not implemented yet: container networking (the network namespace is created but empty), image
pulling, restart policy, and `isolation: vm`.

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
