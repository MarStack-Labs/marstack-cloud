# Development VM

The agent touches namespaces, cgroup v2, nftables, and later `/dev/kvm`. macOS has none of those,
so anything below the control plane is developed inside a Linux VM.

The host keeps the repository; the VM mounts it. Edit on macOS, build and run in the VM.

## Create it

```sh
limactl start --name=marstack-dev --tty=false lima/marstack-dev.yaml
```

Takes about a minute on Apple silicon. What the template guarantees:

| | |
|---|---|
| Ubuntu 24.04, kernel 6.8 | cgroup v2 with `cpu`, `memory`, `pids`, `cpuset`, `io` |
| `nestedVirtualization: true` | `/dev/kvm` present, so QEMU runs accelerated |
| Go 1.26.6 | installed from upstream, not the distro package |
| `nftables`, `iproute2`, `qemu-system-arm` | the datapath and VMM tooling |
| repository mounted writable | at the same absolute path as on the host |

`nestedVirtualization` needs macOS 15 or newer on an M3 or newer chip. Without it the VM still
boots and containers still work, but `/dev/kvm` is missing and `isolation: vm` cannot run.

## Use it

```sh
limactl shell marstack-dev

cd /Users/umarsabirin/Documents/Portfolio/marstack-cloud
export GOFLAGS=-buildvcs=false
go build -o bin/marstack-linux ./cmd/marstack
```

`GOFLAGS=-buildvcs=false` is needed because the mounted `.git` is owned by the host user, and Go
refuses to stamp VCS information from a repository it considers unsafe.

The control plane may run on the host; the VM reaches it at `192.168.5.2`.

## Reset it

```sh
limactl delete --force marstack-dev
```

Nothing of value lives in the VM. The repository is on the host, and the template rebuilds the
environment from scratch.
