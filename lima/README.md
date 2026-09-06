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

cd /Users/<you>/Documents/Portfolio/marstack-cloud
export GOFLAGS=-buildvcs=false
make build
sudo make install
```

`make build` writes `bin/marstack`; `sudo make install` puts it on `PATH` at
`/usr/local/bin/marstack`. Without the install step the binary exists but the shell will not find
it, because `bin/` is not on `PATH`.

`GOFLAGS=-buildvcs=false` is needed because the mounted `.git` is owned by the host user, and Go
refuses to stamp VCS information from a repository it considers unsafe. Put it in `~/.bashrc` inside
the VM to stop repeating it.

Then bring up a control plane and one agent:

```sh
make stage-images  # once per node: vm disk and microvm kernel
make dev-up        # builds, starts both, prints the node list
make dev-logs      # tail both logs
make dev-down      # stop them
make dev-reset     # stop, drop the database and all instance state
```

```sh
marstack network list
marstack instance create --name api-1 --image alpine:3.20 -- /bin/sh -c 'sleep 3600'
marstack instance list
```

`dev-up` runs the agent under `sudo` because the container runtime needs root for namespaces and
cgroups. The control plane does not.

The control plane may also run on the macOS host instead; the VM reaches it at `192.168.5.2`.

## Reset it

```sh
limactl delete --force marstack-dev
```

Nothing of value lives in the VM. The repository is on the host, and the template rebuilds the
environment from scratch.
