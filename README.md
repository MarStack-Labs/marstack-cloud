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

```sh
curl -s localhost:7443/healthz
curl -s localhost:7443/v1/version
```

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
