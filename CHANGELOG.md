# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[semantic versioning](https://semver.org/spec/v2.0.0.html). Before 1.0 the API, the CLI and the
on-disk state may change between minor versions.

A release ships one binary, `marstack`, which is the control plane, the node agent and the client at
once. The web console is not part of a release yet.

## [0.1.0] - 2026-09-06

The first release.

### Added

Instances

- Container, VM, microVM and sandbox as one resource type, backed by runc, QEMU, Cloud Hypervisor
  and Firecracker.
- Boot a VM from an ISO and a microVM from a chosen kernel.
- Resize the CPU and memory of an existing instance.
- Attach an instance to more than one network, on any isolation.
- Address an instance by name as well as by id.
- Read what a workload printed, and attach to the serial console of a VM or microVM behind a
  per-instance console password.
- Open an interactive shell inside a running container.
- Run a workload to completion, once or on a schedule.
- Inject sealed configuration files and environment variables into an instance.
- Hand a PCI device through to a guest, from an inventory each node reports.
- Restart a workload that exits, with backoff, and give up on one that cannot start at all.

Nodes and placement

- A node registry and a pull-only agent: the agent polls the control plane, so a node needs no
  inbound port.
- Separate heartbeat and reconcile loops, so a slow reconcile never looks like a dead node.
- Place instances by measured CPU and memory use rather than by count.
- Pin a service's replicas to labelled nodes.
- Fence a partitioned node before its work is given away, and place its containers again once it
  stops reporting.
- Bring a node's workloads back after a restart without reaching the control plane.
- Collect datapath and workload garbage on the node.

Networking

- Networks with a slice per node and native routing between nodes, no encapsulation.
- IPv4, IPv6 and dual-stack networks, with a range per family.
- More than one interface per VM, microVM and sandbox.
- Refuse overlapping ranges, and stop an instance using an address it was not given.
- Firewalls that decide what may reach an instance.
- Publish an instance port on the node that runs it, on either family, with optional TLS.

Load balancing and certificates

- Terminate TLS on a balancer.
- Route one balancer port to many services by host and path, and replace those routes without
  recreating the balancer.
- Get and renew certificates from an ACME certificate authority over HTTP-01.
- Warn about a certificate before it expires.

Storage

- Volumes that outlive the instance they are attached to, attachable by instance name.
- Take a disk back from a running guest.
- Snapshot a volume, roll it back, make a new volume from a snapshot, and snapshot on a schedule.
- Copy volumes off the node they live on, and carry a VM's disk off a draining node.

Images

- An image catalog in the control plane, staged on the node that needs it and reclaimed when it
  leaves the catalog.
- Pull from a registry on the node, take the command from the image, and start a container from the
  layers the node already holds.
- Log in to a private image registry with credentials only nodes can read.

Services

- Replicas, a rolling update one replica at a time, and replacement of a replica the node has given
  up on.
- Hold a replica count against a target load, on CPU or on memory.

Identity and tenancy

- Bearer tokens with roles, lifetimes and a project binding, and a node role that cannot schedule.
- Projects, with instances, networks, volumes, snapshots, firewalls, ports and images scoped to one.
- People who sign in, and an audit trail that records who changed what and who was refused.
- A rate limit per caller, and a resource limit per project.
- The API served over TLS.

Observability

- Usage sampling for nodes and instances.
- An event log derived from state transitions, and alerts when a workload's load crosses a line.
- Paging over the audit trail and the event log.
