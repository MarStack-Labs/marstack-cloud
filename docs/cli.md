# CLI reference

Every command `marstack` accepts, generated from the binary itself with
`make docs`. If a command is here it exists, and if a flag is missing from here it
does not.

One binary is three things: `marstack server` is the control plane,
`marstack agent` runs on each node, and every other command is the client talking to a
control plane over HTTP.

## Reaching a control plane

The client needs an endpoint and a token. Both can come from the environment, so they do not have
to be repeated on every command.

| Variable | Flag | Meaning |
|---|---|---|
| `MARSTACK_ENDPOINT` | `--endpoint` | control plane to talk to |
| `MARSTACK_TOKEN` | | bearer token to send |
| `MARSTACK_CA_FILE` | `--ca-file` | certificate authority to trust for https |

```sh
export MARSTACK_ENDPOINT=https://control.example.internal:7443
export MARSTACK_TOKEN=$(marstack login --email you@example.test)

marstack instance list
marstack instance list --output json
```

A control plane writes its first admin token to `<data-dir>/bootstrap-token` on the first
start. That token is how the first person signs in and creates everyone else.

## Commands

| Command | What it does |
|---|---|
| [`agent`](#marstack-agent) | Run the node agent |
| [`alert`](#marstack-alert) | Watch a workload's load and record when it crosses a line |
| [`audit`](#marstack-audit) | Show who changed what, and who was refused |
| [`backup`](#marstack-backup) | Manage volume backups |
| [`balancer`](#marstack-balancer) | Spread one port across several instances |
| [`device`](#marstack-device) | List the devices nodes can hand to a guest |
| [`event`](#marstack-event) | Show what the platform did on its own |
| [`exec`](#marstack-exec) | Run one command inside a running container and show what it printed |
| [`firewall`](#marstack-firewall) | Manage the rules that decide what may reach an instance |
| [`forward`](#marstack-forward) | Publish an instance port on the node that runs it |
| [`image`](#marstack-image) | Manage the image catalog |
| [`instance`](#marstack-instance) | Manage instances |
| [`job`](#marstack-job) | Run a workload to completion, once or on a schedule |
| [`key`](#marstack-key) | Manage the ssh keys an instance can be created with |
| [`login`](#marstack-login) | Sign in and print a token that expires |
| [`logs`](#marstack-logs) | Show what a workload printed |
| [`network`](#marstack-network) | Manage networks |
| [`node`](#marstack-node) | Inspect nodes |
| [`project`](#marstack-project) | Manage projects |
| [`quota`](#marstack-quota) | Show and set what a project may consume |
| [`registry`](#marstack-registry) | Log in to a private image registry |
| [`server`](#marstack-server) | Run the control plane |
| [`service`](#marstack-service) | Keep a number of replicas of one workload running |
| [`shell`](#marstack-shell) | Open a shell inside a running container and stay attached |
| [`token`](#marstack-token) | Manage API tokens |
| [`ui`](#marstack-ui) | Install the web console this control plane can serve |
| [`usage`](#marstack-usage) | Show what each node and instance is actually using |
| [`user`](#marstack-user) | People who can sign in, and what they may do |
| [`version`](#marstack-version) | Print the version and exit |
| [`volume`](#marstack-volume) | Manage volumes |
| [`webhook`](#marstack-webhook) | Send events to somewhere that can act on them |

### `marstack agent`

Run the node agent

```
marstack agent [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--address` | `string` |  | address the other nodes reach this one on, required for multi-node routing |
| `--fence-after` | `duration` | `1m0s` | stop movable workloads after losing the control plane for this long, so the scheduler never hands running work to another node |
| `--interval` | `duration` | `10s` | heartbeat and reconcile interval |
| `--log-level` | `string` | `info` | log level: debug, info, warn, error |
| `--name` | `string` |  | node name, defaults to the hostname |
| `--runtime-root` | `string` | `/var/lib/marstack` | directory holding images and instance state on this node |
| `--state-dir` | `string` | `/var/lib/marstack/agent` | directory where the agent keeps the last known desired state |
| `--zone` | `string` |  | failure domain this node belongs to |

### `marstack alert`

Watch a workload's load and record when it crosses a line.

An alert fires only once the reading has held for the whole window, so a single
spike is not an alert. It fires once and clears once: the event is written when
the state changes, never on every pass, so a webhook is not woken every twenty
seconds for something it already knows.

A reading that is missing or stale stops the alert rather than counting as zero.
A workload that stopped reporting is not a workload that went quiet.

```
marstack alert
```

| Subcommand | What it does |
|---|---|
| [`marstack alert create`](#marstack-alert-create) | Watch one workload |
| [`marstack alert delete`](#marstack-alert-delete) | Stop watching |
| [`marstack alert list`](#marstack-alert-list) | List the alerts and what they read last |

#### `marstack alert create`

Watch one workload

```
marstack alert create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--comparison` | `string` | `above` | above or below |
| `--for` | `string` |  | how long it must hold before firing, such as 5m; defaults to 2m |
| `--instance` | `string` |  | workload to watch, by name or id |
| `--metric` | `string` | `cpu` | cpu or memory |
| `--name` | `string` |  | alert name, unique in the project |
| `--threshold` | `float64` | `0` | percentage to cross |

#### `marstack alert delete`

Stop watching

```
marstack alert delete <name|id>
```

#### `marstack alert list`

List the alerts and what they read last

```
marstack alert list
```

### `marstack audit`

Show who changed what, and who was refused.

Reads are not recorded and request bodies are never stored, so a token secret
cannot leak into the trail. Only the most recent entries are kept.

```
marstack audit [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--limit` | `int` | `0` | how many entries to show, newest first |

### `marstack backup`

Manage volume backups.

A snapshot lives inside the volume file on the node that holds it, so it
survives a bad write but not a lost node. A backup copies the volume off the
node to the control plane, and a new volume can be created from it with
marstack volume create --from-backup.

```
marstack backup
```

| Subcommand | What it does |
|---|---|
| [`marstack backup create`](#marstack-backup-create) | Ask the node holding a volume to copy it off the node |
| [`marstack backup delete`](#marstack-backup-delete) | Delete a backup and the bytes it holds |
| [`marstack backup keygen`](#marstack-backup-keygen) | Print a new backup key |
| [`marstack backup list`](#marstack-backup-list) | List backups |
| [`marstack backup schedule`](#marstack-backup-schedule) | Take backups on a timer and keep only the newest |

#### `marstack backup create`

Ask the node holding a volume to copy it off the node

```
marstack backup create <volume> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--name` | `string` |  | backup name, unique for that volume |

#### `marstack backup delete`

Delete a backup and the bytes it holds

```
marstack backup delete <id>
```

#### `marstack backup keygen`

Print a new backup key.

Write it to a file and pass that file to marstack server --backup-key-file.
Keep it somewhere other than the data directory it protects: a key stored next
to the backups it seals protects nothing. Lose it and those backups are gone.

```
marstack backup keygen
```

#### `marstack backup list`

List backups

```
marstack backup list [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--volume` | `string` |  | only backups of this volume |

#### `marstack backup schedule`

Take backups on a timer and keep only the newest.

A volume carries at most one schedule. Retention only ever removes copies the
schedule itself made, so a backup you took by hand is never pruned.

```
marstack backup schedule
```

| Subcommand | What it does |
|---|---|
| [`marstack backup schedule clear`](#marstack-backup-schedule-clear) | Stop taking scheduled backups of a volume |
| [`marstack backup schedule list`](#marstack-backup-schedule-list) | List backup schedules |
| [`marstack backup schedule set`](#marstack-backup-schedule-set) | Set or replace the backup schedule of a volume |

##### `marstack backup schedule clear`

Stop taking scheduled backups of a volume

```
marstack backup schedule clear <volume>
```

##### `marstack backup schedule list`

List backup schedules

```
marstack backup schedule list
```

##### `marstack backup schedule set`

Set or replace the backup schedule of a volume

```
marstack backup schedule set <volume> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--every` | `string` |  | how often, such as 6h, 1d or 1w |
| `--keep` | `int` | `7` | how many of its own copies to keep |

### `marstack balancer`

Spread one port across several instances

```
marstack balancer
```

| Subcommand | What it does |
|---|---|
| [`marstack balancer add`](#marstack-balancer-add) | Add a backend to a balancer |
| [`marstack balancer certificate`](#marstack-balancer-certificate) | Terminate TLS on a balancer's listen port |
| [`marstack balancer create`](#marstack-balancer-create) | Create a balancer in front of a set of instances |
| [`marstack balancer delete`](#marstack-balancer-delete) | Delete a balancer and release its listen port |
| [`marstack balancer get`](#marstack-balancer-get) | Show a balancer and the state of every backend |
| [`marstack balancer list`](#marstack-balancer-list) | List balancers |
| [`marstack balancer remove`](#marstack-balancer-remove) | Take a backend out of a balancer |
| [`marstack balancer route`](#marstack-balancer-route) | Change which host and path each service answers |

#### `marstack balancer add`

Add a backend to a balancer

```
marstack balancer add <name|id> <instance-id>
```

#### `marstack balancer certificate`

Terminate TLS on a balancer's listen port.

A balancer without a certificate is an nftables rule: the kernel rewrites the
destination and never looks at the bytes. A balancer with one cannot be, because
nothing in nftables terminates TLS, so the node accepts the connection itself and
opens a plain one to the backend.

The private key is sealed with the operator key and served only to nodes.

```
marstack balancer certificate
```

| Subcommand | What it does |
|---|---|
| [`marstack balancer certificate auto`](#marstack-balancer-certificate-auto) | Let a certificate authority issue and renew the certificate |
| [`marstack balancer certificate remove`](#marstack-balancer-certificate-remove) | Stop terminating TLS and go back to an nftables rule |
| [`marstack balancer certificate set`](#marstack-balancer-certificate-set) | Give a balancer a certificate to terminate with |

##### `marstack balancer certificate auto`

Let a certificate authority issue and renew the certificate.

The authority proves the name belongs to you by fetching
http://<name>/.well-known/acme-challenge/<token>, which the node answers on the
balancer's own listen port. That only works if the authority can reach it, so a
public authority needs the balancer listening on port 80 and the name pointing
at a node.

Renewal happens on its own thirty days before expiry. Nothing is uploaded and
no private key ever leaves the platform.

```
marstack balancer certificate auto
```

| Subcommand | What it does |
|---|---|
| [`marstack balancer certificate auto list`](#marstack-balancer-certificate-auto-list) | List every certificate this platform keeps renewed |
| [`marstack balancer certificate auto start`](#marstack-balancer-certificate-auto-start) | Ask for a certificate and keep it renewed |
| [`marstack balancer certificate auto status`](#marstack-balancer-certificate-auto-status) | Show where the certificate order got to |
| [`marstack balancer certificate auto stop`](#marstack-balancer-certificate-auto-stop) | Stop renewing; the certificate already attached stays |

###### `marstack balancer certificate auto list`

List every certificate this platform keeps renewed

```
marstack balancer certificate auto list
```

###### `marstack balancer certificate auto start`

Ask for a certificate and keep it renewed

```
marstack balancer certificate auto start <balancer> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--name` | `stringSlice` |  | host to cover, repeatable; defaults to every host the balancer routes |

###### `marstack balancer certificate auto status`

Show where the certificate order got to

```
marstack balancer certificate auto status <balancer>
```

###### `marstack balancer certificate auto stop`

Stop renewing; the certificate already attached stays

```
marstack balancer certificate auto stop <balancer>
```

##### `marstack balancer certificate remove`

Stop terminating TLS and go back to an nftables rule

```
marstack balancer certificate remove <balancer id>
```

##### `marstack balancer certificate set`

Give a balancer a certificate to terminate with

```
marstack balancer certificate set <balancer id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cert-file` | `string` |  | PEM certificate chain, leaf first |
| `--key-file` | `string` |  | PEM private key for the leaf certificate |

#### `marstack balancer create`

Create a balancer in front of a set of instances.

Every node claims the listen port and rewrites arriving packets to one of the
backends with nftables. There is no single virtual address: any node's address is
an entry point, so losing a node costs only the clients that were using it.

With --route the balancer reads each request and picks a service by the Host
header and the path, so one port serves many applications: --route app.test=web
--route app.test/api=api. The most specific rule wins - an exact host before any
host, then the longest path - and a request that matches nothing gets a 404.
Routes cannot be combined with --service or --instance, since a request cannot be
answered two ways, and they need tcp because there is no request in a datagram.

With --service the backends are whatever replicas that service currently holds,
so scaling the service moves traffic and nothing has to be registered by hand.

Without --check a backend counts as up while its instance is observed running,
which a process that is running but wedged still satisfies. With --check the node
holding the instance probes it every reconcile pass, and only a backend that
answers takes traffic.

```
marstack balancer create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--algorithm` | `string` | `round_robin` | round_robin or source_hash, which keeps one client on one backend |
| `--check` | `string` | `none` | none, tcp, or http: what makes a backend count as up |
| `--check-path` | `string` |  | path an http check asks for, defaults to / |
| `--fall` | `int` | `0` | consecutive failures before a backend stops taking traffic, defaults to 2 |
| `--family` | `string` |  | which of a dual stack instance's addresses to balance: ipv4 or ipv6 |
| `--instance` | `stringSlice` |  | backend instance id, repeatable |
| `--listen-port` | `int` | `0` | port claimed on every node, defaults to the target port |
| `--name` | `string` |  | balancer name, unique in the project |
| `--protocol` | `string` | `tcp` | tcp or udp |
| `--rise` | `int` | `0` | consecutive passes before a backend takes traffic, defaults to 2 |
| `--route` | `stringArray` |  | send one host and path prefix to one service, as host[/path]=service. Repeatable, and a rule starting with / matches any host |
| `--service` | `string` |  | follow a service: backends join and leave with its replica count |
| `--target-port` | `int` | `0` | port inside every backend |

#### `marstack balancer delete`

Delete a balancer and release its listen port

```
marstack balancer delete <name|id>
```

#### `marstack balancer get`

Show a balancer and the state of every backend

```
marstack balancer get <name|id>
```

#### `marstack balancer list`

List balancers

```
marstack balancer list
```

#### `marstack balancer remove`

Take a backend out of a balancer

```
marstack balancer remove <name|id> <instance-id>
```

#### `marstack balancer route`

Change which host and path each service answers.

Only a balancer that was created with routes can be changed this way. A balancer
that sends everything on its port to one set of backends stays that way, because
turning one into the other changes what the port answers with no way back.

The node picks the new table up on its next pass, and it does so without
restarting the listener, so live connections are not dropped.

```
marstack balancer route
```

| Subcommand | What it does |
|---|---|
| [`marstack balancer route set`](#marstack-balancer-route-set) | Replace every route on a balancer |

##### `marstack balancer route set`

Replace every route on a balancer.

This replaces the whole table rather than merging, so what you pass is what the
balancer has afterwards. Merging would leave no way to remove one route without
inventing a delete route, and would make the call non-idempotent.

```
marstack balancer route set <name|id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--route` | `stringArray` |  | send one host and path prefix to one service, as host[/path]=service. Repeatable, and a rule starting with / matches any host |

### `marstack device`

List the devices nodes can hand to a guest.

Nodes report what they carry on every pass, so this is an inventory rather than
something anybody registers. A card only counts as available once it is bound
to vfio-pci: while a host driver holds it, qemu cannot open it, and placing a
workload on it would fail after the placement already happened.

One card goes to one workload. Deleting the workload gives it back.

```
marstack device
```

### `marstack event`

Show what the platform did on its own.

The audit trail records calls somebody made. This records the things that happen
with no caller: a workload restarting, a backend leaving a balancer. It is a ring
of the most recent entries, not an archive.

```
marstack event [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--kind` | `string` |  | only entries of this kind, such as instance.restarted |
| `--limit` | `int` | `0` | how many entries, newest first |
| `--severity` | `string` |  | info, warn, or error |
| `--subject` | `string` |  | only entries about this resource id |

### `marstack exec`

Run one command inside a running container and show what it printed.

This is not a shell. There is no stdin and no terminal: the command runs, its
output is captured, and you get the output and the exit code. An interactive
session needs a channel that stays open between here and the node, and the
node only ever calls out - so that is a different thing, not a flag on this.

Only a container can be entered. A vm, microvm or sandbox runs its own kernel,
so getting inside one needs something running in the guest; marstack console on
the node attaches to a vm's serial port instead.

The node picks the command up on its next pass, so expect a few seconds before
anything happens.

```
marstack exec <instance> -- command args... [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--timeout` | `string` |  | how long the command may run inside, such as 10s or 2m |
| `--wait` | `duration` | `2m0s` | how long to wait for a node to pick it up and answer |

| Subcommand | What it does |
|---|---|
| [`marstack exec list`](#marstack-exec-list) | List the commands that have been run |

#### `marstack exec list`

List the commands that have been run

```
marstack exec list
```

### `marstack firewall`

Manage the rules that decide what may reach an instance

```
marstack firewall
```

| Subcommand | What it does |
|---|---|
| [`marstack firewall create`](#marstack-firewall-create) | Create a firewall, allowing only what its rules name |
| [`marstack firewall delete`](#marstack-firewall-delete) | Delete a firewall |
| [`marstack firewall list`](#marstack-firewall-list) | List firewalls and their rules |
| [`marstack firewall rules`](#marstack-firewall-rules) | Replace the rules of a firewall |

#### `marstack firewall create`

Create a firewall, allowing only what its rules name.

A rule is a comma separated list: --rule 'protocol=tcp,port=80'
                                  --rule 'port=8000-8100,source=10.20.0.0/16'
                                  --rule 'protocol=icmp'

An instance with no firewall is reachable from anywhere, as before. An instance
that names a firewall with no rules is reachable from nowhere.

```
marstack firewall create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--name` | `string` |  | firewall name, unique within the platform |
| `--rule` | `stringArray` |  | an allow rule, repeatable |

#### `marstack firewall delete`

Delete a firewall

```
marstack firewall delete <name|id>
```

#### `marstack firewall list`

List firewalls and their rules

```
marstack firewall list
```

#### `marstack firewall rules`

Replace the rules of a firewall

```
marstack firewall rules <name|id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--rule` | `stringArray` |  | an allow rule, repeatable; none means deny all |

### `marstack forward`

Publish an instance port on the node that runs it

```
marstack forward
```

| Subcommand | What it does |
|---|---|
| [`marstack forward certificate`](#marstack-forward-certificate) | Terminate TLS on a published port |
| [`marstack forward create`](#marstack-forward-create) | Publish a port, reachable on the address the node reported |
| [`marstack forward delete`](#marstack-forward-delete) | Stop publishing a port |
| [`marstack forward list`](#marstack-forward-list) | List published ports |

#### `marstack forward certificate`

Terminate TLS on a published port.

A published port without a certificate is an nftables rule and the kernel never
looks at the bytes. With one it cannot be, because nothing in nftables terminates
TLS, so the node accepts the connection and opens a plain one to the instance.

The private key is sealed with the operator key and served only to nodes.

```
marstack forward certificate
```

| Subcommand | What it does |
|---|---|
| [`marstack forward certificate remove`](#marstack-forward-certificate-remove) | Stop terminating TLS and go back to an nftables rule |
| [`marstack forward certificate set`](#marstack-forward-certificate-set) | Give a published port a certificate to terminate with |

##### `marstack forward certificate remove`

Stop terminating TLS and go back to an nftables rule

```
marstack forward certificate remove <forward id>
```

##### `marstack forward certificate set`

Give a published port a certificate to terminate with

```
marstack forward certificate set <forward id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cert-file` | `string` |  | PEM certificate chain, leaf first |
| `--key-file` | `string` |  | PEM private key for the leaf certificate |

#### `marstack forward create`

Publish a port, reachable on the address the node reported.

The node rewrites arriving packets to the instance with nftables. Nothing binds a
socket, so a privileged node port is fine.

```
marstack forward create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--family` | `string` |  | which of a dual stack instance's addresses to publish: ipv4 or ipv6 |
| `--instance` | `string` |  | instance id whose port is published |
| `--node-port` | `int` | `0` | port on the node, defaults to the target port |
| `--protocol` | `string` | `tcp` | tcp or udp |
| `--target-port` | `int` | `0` | port inside the instance |

#### `marstack forward delete`

Stop publishing a port

```
marstack forward delete <id>
```

#### `marstack forward list`

List published ports

```
marstack forward list
```

### `marstack image`

Manage the image catalog

```
marstack image
```

| Subcommand | What it does |
|---|---|
| [`marstack image create`](#marstack-image-create) | Register an image a node can download |
| [`marstack image delete`](#marstack-image-delete) | Remove an image from the catalog |
| [`marstack image get`](#marstack-image-get) | Show one image |
| [`marstack image list`](#marstack-image-list) | List registered images |

#### `marstack image create`

Register an image a node can download.

kind disk is a bootable cloud image for isolation vm, kind iso is optical media
to attach to one, and kind kernel is the uncompressed kernel a microvm boots.
Container, microvm and sandbox root filesystems come from an OCI reference on the
instance itself and are not registered here.

```
marstack image create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--arch` | `string` |  | arm64 or amd64, defaults to arm64 |
| `--checksum` | `string` |  | sha256:<hex>, verified after download and pinned for later checks |
| `--kind` | `string` | `disk` | disk, iso or kernel |
| `--name` | `string` |  | image name, unique within the platform |
| `--source` | `string` |  | http or https URL a node downloads from |

#### `marstack image delete`

Remove an image from the catalog

```
marstack image delete <id>
```

#### `marstack image get`

Show one image

```
marstack image get <name|id>
```

#### `marstack image list`

List registered images

```
marstack image list
```

### `marstack instance`

Manage instances

```
marstack instance
```

| Subcommand | What it does |
|---|---|
| [`marstack instance console`](#marstack-instance-console) | Attach to the serial console of an instance running on this node |
| [`marstack instance create`](#marstack-instance-create) | Create an instance |
| [`marstack instance delete`](#marstack-instance-delete) | Delete an instance |
| [`marstack instance get`](#marstack-instance-get) | Show one instance |
| [`marstack instance list`](#marstack-instance-list) | List instances |
| [`marstack instance resize`](#marstack-instance-resize) | Change how much cpu and memory an instance may use |
| [`marstack instance start`](#marstack-instance-start) | Ask the platform to run an instance |
| [`marstack instance stop`](#marstack-instance-stop) | Ask the platform to stop an instance |

#### `marstack instance console`

Attach to the serial console of an instance running on this node.

This talks to the hypervisor directly, so it must run on the node that
holds the instance, as root. Press Ctrl-] to detach.

```
marstack instance console <id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--runtime-root` | `string` | `/var/lib/marstack` | directory the node agent keeps runtime state in |

#### `marstack instance create`

Create an instance

```
marstack instance create [-- command args...] [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--device` | `string` |  | gpu or accelerator to hand to the guest; only a vm can be given one, and only a card already bound to vfio-pci counts |
| `--disk-gib` | `int` | `0` | size of a vm disk created without a base image, in GiB |
| `--env` | `stringArray` |  | NAME=value passed to the workload, repeatable; needs a control plane started with --backup-key-file, and values are never served back |
| `--file` | `stringArray` |  | /path/in/the/workload=local-file[:mode] to write before it starts, repeatable; content is sealed at rest and never served back |
| `--firewall` | `string` |  | firewall id whose rules decide what may reach this instance |
| `--image` | `string` |  | image the instance boots from: an OCI reference for container, microvm and sandbox, or a registered disk image for vm |
| `--iso` | `string` |  | registered iso image to attach to a vm and boot first, for installing an os yourself |
| `--isolation` | `string` | `container` | isolation: container, vm, microvm or sandbox |
| `--kernel` | `string` |  | registered kernel image a microvm or sandbox boots, instead of the one staged on the node |
| `--key` | `stringArray` |  | ssh key to install at first boot, by name; repeat for more, isolation vm only |
| `--memory-mib` | `int` | `0` | memory in MiB, defaults to the platform default |
| `--name` | `string` |  | instance name, unique within the platform |
| `--network` | `stringArray` |  | network to attach, repeatable; the first is eth0 and carries the default route, the rest are extra interfaces |
| `--node-selector` | `stringArray` |  | only place on a node carrying key=value, repeatable and combined with and |
| `--placement-group` | `string` |  | instances sharing this name are spread across nodes and zones |
| `--placement-strict` |  |  | hold this instance pending rather than share a node with its group |
| `--restart` | `string` |  | restart policy: always, on-failure, never |
| `--vcpu` | `int` | `0` | virtual CPUs, defaults to the platform default |

#### `marstack instance delete`

Delete an instance

```
marstack instance delete <id>
```

#### `marstack instance get`

Show one instance

```
marstack instance get <id>
```

#### `marstack instance list`

List instances

```
marstack instance list
```

#### `marstack instance resize`

Change how much cpu and memory an instance may use.

A container takes the new limits on the next reconcile pass without restarting,
because cgroup limits are writable while it runs. A vm, microvm or sandbox keeps
the size it booted with until it is stopped and started again - nothing here
hot-plugs cpu or memory into a guest.

Leave a flag off to keep that dimension as it is.

```
marstack instance resize <id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--memory-mib` | `int` | `0` | memory in MiB |
| `--vcpu` | `int` | `0` | virtual CPUs |

#### `marstack instance start`

Ask the platform to run an instance

```
marstack instance start <id>
```

#### `marstack instance stop`

Ask the platform to stop an instance

```
marstack instance stop <id>
```

### `marstack job`

Run a workload to completion, once or on a schedule

```
marstack job
```

| Subcommand | What it does |
|---|---|
| [`marstack job create`](#marstack-job-create) | Create a job that runs a workload to completion |
| [`marstack job delete`](#marstack-job-delete) | Delete a job, its run history and any workload still running |
| [`marstack job get`](#marstack-job-get) | Show a job and the runs it remembers |
| [`marstack job list`](#marstack-job-list) | List jobs |
| [`marstack job pause`](#marstack-job-pause) | Stop a scheduled job firing until it is resumed |
| [`marstack job resume`](#marstack-job-resume) | Let a paused job fire again |
| [`marstack job run`](#marstack-job-run) | Start a run now |

#### `marstack job create`

Create a job that runs a workload to completion.

A run finishes when its workload exits: zero is a success, anything else is a
failure, and a failure is retried up to --retries times before the job gives up.
The workload of a finished run is removed, so a nightly job does not leave one
behind every night.

A run always carries a restart policy of never, whatever the image would
otherwise do: a node that restarts the workload means the run never ends.

With --every the job also runs on a schedule. Two runs of one job never overlap;
if a run is still going when the next is due, that turn is skipped and recorded
rather than stacked.

```
marstack job create -- command args... [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--env` | `stringArray` |  | NAME=value each run gets, repeatable; sealed at rest and never served back |
| `--every` | `string` |  | run on this interval as well, for example 1h or 24h |
| `--file` | `stringArray` |  | /path/in/the/workload=local-file[:mode] each run gets, repeatable; sealed at rest |
| `--firewall` | `string` |  | firewall each run carries |
| `--image` | `string` |  | image each run boots |
| `--isolation` | `string` | `container` | container, vm, microvm, or sandbox |
| `--keep` | `int` | `0` | how many finished runs to remember |
| `--kernel` | `string` |  | kernel image for microvm and sandbox |
| `--memory-mib` | `int` | `0` | memory per run in MiB |
| `--name` | `string` |  | job name, unique in the project |
| `--network` | `string` |  | network each run joins |
| `--node-selector` | `stringArray` |  | only place runs on nodes carrying key=value, repeatable |
| `--retries` | `int` | `0` | how many times to retry a failed run |
| `--vcpu` | `int` | `0` | virtual CPUs per run |

#### `marstack job delete`

Delete a job, its run history and any workload still running

```
marstack job delete <name|id>
```

#### `marstack job get`

Show a job and the runs it remembers

```
marstack job get <name|id>
```

#### `marstack job list`

List jobs

```
marstack job list
```

#### `marstack job pause`

Stop a scheduled job firing until it is resumed

```
marstack job pause <name|id>
```

#### `marstack job resume`

Let a paused job fire again

```
marstack job resume <name|id>
```

#### `marstack job run`

Start a run now

```
marstack job run <name|id>
```

### `marstack key`

Manage the ssh keys an instance can be created with.

Keys belong to a project and are installed by cloud-init at first boot, so only
isolation vm can use them, and adding a key later does not reach an instance
that has already booted.

```
marstack key
```

| Subcommand | What it does |
|---|---|
| [`marstack key add`](#marstack-key-add) | Add a public key |
| [`marstack key delete`](#marstack-key-delete) | Remove a key, leaving instances already booted with it alone |
| [`marstack key list`](#marstack-key-list) | List the keys in this project |

#### `marstack key add`

Add a public key

```
marstack key add [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--file` | `string` |  | path to a .pub file |
| `--name` | `string` |  | a name to refer to the key by |

#### `marstack key delete`

Remove a key, leaving instances already booted with it alone

```
marstack key delete <name>
```

#### `marstack key list`

List the keys in this project

```
marstack key list
```

### `marstack login`

Sign in and print a token that expires.

The token is printed rather than saved, so where it is kept is your choice:
export it as MARSTACK_TOKEN, or write it somewhere and pass --token-file.

```
marstack login [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--email` | `string` |  | the address to sign in with |
| `--password-file` | `string` |  | file holding your password |

### `marstack logs`

Show what a workload printed.

The node ships new output to the control plane on every reconcile pass, so the
last line can be a few seconds behind. Only a recent window is kept: the oldest
lines are dropped once an instance has more than the platform holds, and nothing
survives the instance being deleted. This is for looking at why something is not
working, not for keeping records - ship them somewhere else for that.

```
marstack logs <instance-id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--tail` | `int` | `0` | how many of the most recent lines to show |

### `marstack network`

Manage networks

```
marstack network
```

| Subcommand | What it does |
|---|---|
| [`marstack network create`](#marstack-network-create) | Create a network |
| [`marstack network delete`](#marstack-network-delete) | Delete a network that has no addresses handed out |
| [`marstack network list`](#marstack-network-list) | List networks |

#### `marstack network create`

Create a network

```
marstack network create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--cidr` | `string` |  | address range, must not overlap another network |
| `--cidr6` | `string` |  | a second range of the other family, so every instance takes one address from each |
| `--name` | `string` |  | network name, unique within the platform |

#### `marstack network delete`

Delete a network that has no addresses handed out

```
marstack network delete <id>
```

#### `marstack network list`

List networks

```
marstack network list
```

### `marstack node`

Inspect nodes

```
marstack node
```

| Subcommand | What it does |
|---|---|
| [`marstack node cordon`](#marstack-node-cordon) | Stop placing new workloads on a node |
| [`marstack node drain`](#marstack-node-drain) | Cordon a node and move what can move off it |
| [`marstack node get`](#marstack-node-get) | Show one node |
| [`marstack node label`](#marstack-node-label) | Replace the labels an operator has put on a node |
| [`marstack node list`](#marstack-node-list) | List nodes |
| [`marstack node uncordon`](#marstack-node-uncordon) | Let a node take workloads again |

#### `marstack node cordon`

Stop placing new workloads on a node.

What is already running stays running and keeps serving. Use drain to move it.

```
marstack node cordon <id>
```

#### `marstack node drain`

Cordon a node and move what can move off it.

This returns as soon as the intent is recorded; the scheduler does the moving on
its next pass, so watch the node until it stops draining.

Only containers move. Anything with a disk on that node stays, because moving it
would lose the disk, and the drain reports it rather than pretending otherwise.

```
marstack node drain <id>
```

#### `marstack node get`

Show one node

```
marstack node get <id>
```

#### `marstack node label`

Replace the labels an operator has put on a node.

This replaces the whole set rather than merging, so drop a label by leaving it
out. With no pairs and --clear the node ends up with none.

A node never sets its own labels. They say what an operator has decided about a
machine, and a node that could assert them could attract workloads to itself.

```
marstack node label <id> [key=value ...] [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--clear` |  |  | remove every label instead of setting any |

#### `marstack node list`

List nodes

```
marstack node list [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--label` | `stringArray` |  | only nodes carrying key=value, repeatable and combined with and |

#### `marstack node uncordon`

Let a node take workloads again.

This also cancels a drain in progress; anything already moved stays where it went.

```
marstack node uncordon <id>
```

### `marstack project`

Manage projects.

A project owns instances, networks, volumes, images, firewalls and published
ports. Every token belongs to one project and sees only what that project owns.
To work in another project, mint a token there.

```
marstack project
```

| Subcommand | What it does |
|---|---|
| [`marstack project create`](#marstack-project-create) | Create a project |
| [`marstack project delete`](#marstack-project-delete) | Delete an empty project |
| [`marstack project list`](#marstack-project-list) | List projects |

#### `marstack project create`

Create a project

```
marstack project create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--name` | `string` |  | project name, unique within the platform |

#### `marstack project delete`

Delete an empty project

```
marstack project delete <id>
```

#### `marstack project list`

List projects

```
marstack project list
```

### `marstack quota`

Show and set what a project may consume.

A limit of zero means no limit. Limits are checked when work is created, so
lowering a limit below what a project already holds does not delete anything;
it stops the project growing until it fits again.

```
marstack quota
```

| Subcommand | What it does |
|---|---|
| [`marstack quota clear`](#marstack-quota-clear) | Remove every limit from a project |
| [`marstack quota list`](#marstack-quota-list) | List every project that carries a limit |
| [`marstack quota set`](#marstack-quota-set) | Replace the limits of a project |
| [`marstack quota show`](#marstack-quota-show) | Show the limits and usage of a project, defaulting to your own |

#### `marstack quota clear`

Remove every limit from a project

```
marstack quota clear <project>
```

#### `marstack quota list`

List every project that carries a limit

```
marstack quota list
```

#### `marstack quota set`

Replace the limits of a project.

Every limit is replaced, not merged: a flag you leave out becomes zero, which
means unlimited. Pass all the limits you want to keep.

```
marstack quota set <project> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--instances` | `int` | `0` | most instances, 0 for unlimited |
| `--memory-mib` | `int` | `0` | most memory in total, 0 for unlimited |
| `--vcpu` | `int` | `0` | most vCPU in total, 0 for unlimited |
| `--volume-gib` | `int` | `0` | most volume capacity in total, 0 for unlimited |
| `--volumes` | `int` | `0` | most volumes, 0 for unlimited |

#### `marstack quota show`

Show the limits and usage of a project, defaulting to your own

```
marstack quota show [project]
```

### `marstack registry`

Log in to a private image registry.

Without a credential a node can only pull what a registry serves anonymously,
which means no private image at all and a much lower rate limit on the public
ones. A credential is held per host and used by every node.

The password is sealed with the operator key and served only to nodes. It is
never returned to an operator, so there is nothing to read back: replace it by
deleting the credential and adding it again.

```
marstack registry
```

| Subcommand | What it does |
|---|---|
| [`marstack registry add`](#marstack-registry-add) | Add a login for one registry host |
| [`marstack registry list`](#marstack-registry-list) | List the registries this platform can log in to |
| [`marstack registry remove`](#marstack-registry-remove) | Forget a registry login |

#### `marstack registry add`

Add a login for one registry host

```
marstack registry add [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--host` | `string` |  | registry host, such as ghcr.io or registry-1.docker.io |
| `--password-file` | `string` |  | file holding the password or token; a flag would be kept in shell history and shown in ps |
| `--username` | `string` |  | user this credential logs in as |

#### `marstack registry list`

List the registries this platform can log in to

```
marstack registry list
```

#### `marstack registry remove`

Forget a registry login

```
marstack registry remove <host|id>
```

### `marstack server`

Run the control plane.

Without --tls-cert and --tls-key it serves plain HTTP, and every bearer token
crosses the network in the clear. That is fine on a loopback address and wrong
anywhere else, so it says so on every start.

Without --backup-key-file, backup content is stored unencrypted. Lose a key and
the backups it sealed are gone for good, so keep it somewhere other than the
data directory it protects.

Without --object-store-endpoint, backups live on this machine's disk, which is
the single point of failure they exist to survive. Point it at MinIO or anything
else speaking S3 and they outlive this machine. The secret key is read from
MARSTACK_OBJECT_STORE_SECRET_KEY so it never reaches the process list.

```
marstack server [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--acme-contact` | `string` |  | email the certificate authority writes to about expiry and problems |
| `--acme-directory` | `string` |  | ACME directory URL to get certificates from, such as https://acme-v02.api.letsencrypt.org/directory |
| `--backup-key-file` | `stringArray` |  | file holding 64 hex characters; repeat to keep reading older backups, the first seals new ones |
| `--data-dir` | `string` | `./data` | directory holding the control plane database |
| `--listen` | `string` | `127.0.0.1:7443` | address the control plane listens on |
| `--log-level` | `string` | `info` | log level: debug, info, warn, error |
| `--object-store-access-key` | `string` |  | access key id for the object store |
| `--object-store-bucket` | `string` |  | bucket backups are written into |
| `--object-store-endpoint` | `string` |  | S3 endpoint holding backups, such as http://minio.internal:9000 |
| `--object-store-region` | `string` | `us-east-1` | region to sign with; MinIO ignores it but the signature covers it |
| `--project-rate-burst` | `int` | `400` | requests one project may send at once before its rate applies |
| `--project-rate-limit` | `int` | `200` | requests per second one project may sustain across all its tokens, 0 to accept everything |
| `--rate-burst` | `int` | `100` | requests one caller may send at once before the rate applies |
| `--rate-limit` | `int` | `50` | requests per second one caller may sustain, 0 to accept everything |
| `--tls-cert` | `string` |  | PEM certificate chain to serve HTTPS with |
| `--tls-key` | `string` |  | PEM private key for --tls-cert |
| `--ui-dir` | `string` |  | directory holding the web console to serve at /, as installed by marstack ui install; without it nothing is served there |

### `marstack service`

Keep a number of replicas of one workload running

```
marstack service
```

| Subcommand | What it does |
|---|---|
| [`marstack service autoscale`](#marstack-service-autoscale) | Let a service hold its own replica count against a target load |
| [`marstack service create`](#marstack-service-create) | Create a service and let the platform hold the replica count |
| [`marstack service delete`](#marstack-service-delete) | Delete a service and every replica it holds |
| [`marstack service get`](#marstack-service-get) | Show a service and the replicas it holds |
| [`marstack service list`](#marstack-service-list) | List services |
| [`marstack service scale`](#marstack-service-scale) | Change how many replicas a service holds |
| [`marstack service update`](#marstack-service-update) | Publish a new revision and roll the replicas onto it |

#### `marstack service autoscale`

Let a service hold its own replica count against a target load.

The loop compares the average cpu of the replicas that have been up long
enough to have one against the target, and moves the count toward it. It
refuses to act on a reading that is missing or stale, waits out a cooldown
after every change, ignores a difference inside a deadband, and moves by a
bounded step - a service that scales on a guess oscillates, and one that
jumps on a single reading is worse than one that does nothing.

With both a cpu and a memory target the harder pressed of the two decides.
Averaging them would let a service whose memory is nearly full stay small
because its cpu happens to be idle.

```
marstack service autoscale
```

| Subcommand | What it does |
|---|---|
| [`marstack service autoscale list`](#marstack-service-autoscale-list) | List the services that scale themselves |
| [`marstack service autoscale off`](#marstack-service-autoscale-off) | Stop a service scaling itself, leaving the count where it is |
| [`marstack service autoscale set`](#marstack-service-autoscale-set) | Scale a service between min and max toward a target cpu |

##### `marstack service autoscale list`

List the services that scale themselves

```
marstack service autoscale list
```

##### `marstack service autoscale off`

Stop a service scaling itself, leaving the count where it is

```
marstack service autoscale off <service>
```

##### `marstack service autoscale set`

Scale a service between min and max toward a target cpu

```
marstack service autoscale set <service> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--max` | `int` | `4` | most replicas to hold |
| `--min` | `int` | `1` | fewest replicas to hold |
| `--target-cpu` | `int` | `70` | average cpu percent across the replicas to aim for, 0 to ignore cpu |
| `--target-memory` | `int` | `0` | average share of each replica's memory to aim for, 0 to ignore memory |

#### `marstack service create`

Create a service and let the platform hold the replica count.

A service is a workload template plus a number. A loop makes replicas until the
count is met and replaces any that disappear, so a deleted or unrecoverable
replica comes back without anybody typing anything.

Replica names are the service name with a random suffix. Ordinals would collide
with an instance somebody made by hand and wedge the service permanently.

```
marstack service create [-- command args...] [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--disk-gib` | `int` | `0` | root disk size for a vm |
| `--env` | `stringArray` |  | NAME=value every replica runs with, repeatable; sealed at rest and never served back |
| `--file` | `stringArray` |  | /path/in/the/workload=local-file[:mode] every replica gets, repeatable; sealed at rest |
| `--firewall` | `string` |  | firewall every replica carries |
| `--image` | `string` |  | image every replica boots |
| `--iso` | `string` |  | iso every replica boots |
| `--isolation` | `string` | `container` | container, vm, microvm, or sandbox |
| `--kernel` | `string` |  | kernel image for microvm and sandbox |
| `--key` | `stringArray` |  | ssh key name, repeatable |
| `--memory-mib` | `int` | `0` | memory per replica in MiB |
| `--name` | `string` |  | service name, unique in the project |
| `--network` | `stringArray` |  | network every replica joins, repeatable; the first is eth0 |
| `--node-selector` | `stringArray` |  | only place replicas on nodes carrying key=value, repeatable and combined with and |
| `--placement-group` | `string` |  | spread replicas across zones and nodes |
| `--placement-strict` |  |  | refuse to place a replica that would break the spread |
| `--replicas` | `int` | `1` | how many replicas to hold |
| `--restart` | `string` |  | never, on-failure, or always |
| `--vcpu` | `int` | `0` | virtual CPUs per replica |

#### `marstack service delete`

Delete a service and every replica it holds

```
marstack service delete <name|id>
```

#### `marstack service get`

Show a service and the replicas it holds

```
marstack service get <name|id>
```

#### `marstack service list`

List services

```
marstack service list
```

#### `marstack service scale`

Change how many replicas a service holds

```
marstack service scale <name|id> <replicas>
```

#### `marstack service update`

Publish a new revision and roll the replicas onto it.

A revision is the whole template, not a patch: whatever is not passed is not
carried over. Environment variables and files are sealed and never served back,
so the command cannot quietly bring them along - pass them again, or --drop to
say out loud that the new revision runs without them.

One replica is retired per pass and the loop makes its replacement, so a service
runs one short while a replica turns over and a revision that cannot start costs
one replica rather than all of them. Rolling back is publishing the old template
again, which is a new revision of its own.

```
marstack service update <name|id> [-- command args...] [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--disk-gib` | `int` | `0` | root disk size for a vm |
| `--drop` |  |  | publish without the environment and files the current revision carries |
| `--env` | `stringArray` |  | NAME=value every replica runs with, repeatable; sealed at rest and never served back |
| `--file` | `stringArray` |  | /path/in/the/workload=local-file[:mode] every replica gets, repeatable; sealed at rest |
| `--firewall` | `string` |  | firewall every replica carries |
| `--image` | `string` |  | image every replica boots |
| `--iso` | `string` |  | iso every replica boots |
| `--isolation` | `string` | `container` | container, vm, microvm, or sandbox |
| `--kernel` | `string` |  | kernel image for microvm and sandbox |
| `--key` | `stringArray` |  | ssh key name, repeatable |
| `--memory-mib` | `int` | `0` | memory per replica in MiB |
| `--network` | `stringArray` |  | network every replica joins, repeatable; the first is eth0 |
| `--node-selector` | `stringArray` |  | only place replicas on nodes carrying key=value, repeatable and combined with and |
| `--placement-group` | `string` |  | spread replicas across zones and nodes |
| `--placement-strict` |  |  | refuse to place a replica that would break the spread |
| `--restart` | `string` |  | never, on-failure, or always |
| `--vcpu` | `int` | `0` | virtual CPUs per replica |

### `marstack shell`

Open a shell inside a running container and stay attached.

Unlike marstack exec, which runs one command and hands back its output, this
holds the connection open: what you type goes down and what the shell prints
comes back, until you press ctrl-] or the shell exits.

The node has no listener, so it is the node that dials out: it takes the
session, opens one request to read your keystrokes and another to send the
output back, and the control plane joins the two.

This is not a terminal. There is no pty on the far side, so there is no prompt,
no line editing and no ctrl-c: a command runs and its output comes back. Say
exit, or press ctrl-], to leave.

Only a container can be entered. A vm, microvm or sandbox runs its own kernel.

```
marstack shell <instance> [-- command args...]
```

| Subcommand | What it does |
|---|---|
| [`marstack shell list`](#marstack-shell-list) | List the shell sessions this project has opened |

#### `marstack shell list`

List the shell sessions this project has opened

```
marstack shell list
```

### `marstack token`

Manage API tokens

```
marstack token
```

| Subcommand | What it does |
|---|---|
| [`marstack token create`](#marstack-token-create) | Create a token, printing the secret once |
| [`marstack token list`](#marstack-token-list) | List tokens without their secrets |
| [`marstack token revoke`](#marstack-token-revoke) | Revoke a token |

#### `marstack token create`

Create a token, printing the secret once.

Role admin can call everything in its project, and administers projects, tokens
and the audit trail. Role member manages resources in its project only. Role
viewer reads them and changes nothing. Role node can only call the endpoints an
agent needs, so a compromised node cannot schedule work or read the whole
platform.

A token without --project lands in the project of the token that created it, and a
token without --ttl never expires.

```
marstack token create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--name` | `string` |  | token name, unique within the platform |
| `--project` | `string` |  | project the token works in |
| `--role` | `string` | `admin` | admin, member, viewer or node |
| `--ttl` | `string` |  | how long the token stays valid, such as 12h, 30d or 4w; empty means never expires |

#### `marstack token list`

List tokens without their secrets

```
marstack token list
```

#### `marstack token revoke`

Revoke a token

```
marstack token revoke <id>
```

### `marstack ui`

Install the web console this control plane can serve.

The console is not part of the binary. It is published as its own archive per
release, so a control plane that only answers the API never holds it, and the
console can be replaced without replacing the binary.

Once installed, point the control plane at it with
marstack server --ui-dir <directory>.

```
marstack ui
```

| Subcommand | What it does |
|---|---|
| [`marstack ui install`](#marstack-ui-install) | Download and unpack the console |

#### `marstack ui install`

Download the console archive for this binary's version, check it against the
release's SHA256SUMS, and unpack it.

--from installs a file already on disk instead of downloading one. The
checksum is not checked in that case, because the archive is whatever you
chose to hand it.

```
marstack ui install [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | `string` | `/var/lib/marstack/console` | directory to unpack the console into |
| `--from` | `string` |  | install this local .tar.gz instead of downloading one |
| `--version` | `string` |  | release to install, such as v0.2.0; defaults to this binary's version |

### `marstack usage`

Show what each node and instance is actually using.

Instance usage is scoped to your project. Node load is administrative, since it
is infrastructure rather than yours, so a member sees only the instance rows.

```
marstack usage
```

| Subcommand | What it does |
|---|---|
| [`marstack usage history`](#marstack-usage-history) | Show how a node or instance has been loaded |

#### `marstack usage history`

Show how a node or instance has been loaded.

Samples are folded into one minute buckets as they arrive and a day is kept, so
this answers whether something was busy earlier. It is not an archive: point a
real metrics stack at it if you need to keep more than that.

```
marstack usage history <node-id|instance-id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--window` | `string` |  | how far back to look, such as 15m or 6h |

### `marstack user`

People who can sign in, and what they may do

```
marstack user
```

| Subcommand | What it does |
|---|---|
| [`marstack user create`](#marstack-user-create) | Add somebody who can sign in |
| [`marstack user delete`](#marstack-user-delete) | Remove somebody and every token they hold |
| [`marstack user disable`](#marstack-user-disable) | Stop somebody signing in, and stop every token they hold |
| [`marstack user enable`](#marstack-user-enable) | Let somebody sign in again |
| [`marstack user get`](#marstack-user-get) | Show one person |
| [`marstack user list`](#marstack-user-list) | List the people who can sign in |
| [`marstack user password`](#marstack-user-password) | Give somebody a new password, ending the sessions it replaces |

#### `marstack user create`

Add somebody who can sign in.

Their password is read from a file rather than a flag, because a command line
is kept in a shell history and shown in ps. Signing in gives back a token that
carries their role and expires; disabling them stops every token they hold, and
changing their password ends the sessions it replaces.

```
marstack user create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--email` | `string` |  | the address they sign in with |
| `--name` | `string` |  | what to call them |
| `--password-file` | `string` |  | file holding their password |
| `--project` | `string` |  | the project their sessions work in |
| `--role` | `string` | `member` | admin, member, or viewer |

#### `marstack user delete`

Remove somebody and every token they hold

```
marstack user delete <email|id>
```

#### `marstack user disable`

Stop somebody signing in, and stop every token they hold

```
marstack user disable <email|id>
```

#### `marstack user enable`

Let somebody sign in again

```
marstack user enable <email|id>
```

#### `marstack user get`

Show one person

```
marstack user get <email|id>
```

#### `marstack user list`

List the people who can sign in

```
marstack user list
```

#### `marstack user password`

Give somebody a new password, ending the sessions it replaces

```
marstack user password <email|id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--password-file` | `string` |  | file holding the new password |

### `marstack version`

Print the version and exit

```
marstack version
```

### `marstack volume`

Manage volumes

```
marstack volume
```

| Subcommand | What it does |
|---|---|
| [`marstack volume attach`](#marstack-volume-attach) | Attach a volume to an instance already placed on a node |
| [`marstack volume create`](#marstack-volume-create) | Create a volume, which lives on the node it is first attached to |
| [`marstack volume delete`](#marstack-volume-delete) | Delete a volume and the data on it |
| [`marstack volume detach`](#marstack-volume-detach) | Detach a volume from its instance |
| [`marstack volume list`](#marstack-volume-list) | List volumes |
| [`marstack volume resize`](#marstack-volume-resize) | Grow a volume |
| [`marstack volume snapshot`](#marstack-volume-snapshot) | Take, list, restore, copy and remove volume snapshots |

#### `marstack volume attach`

Attach a volume to an instance already placed on a node

```
marstack volume attach <name|id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--instance` | `string` |  | name or id of the instance the volume attaches to |

#### `marstack volume create`

Create a volume, which lives on the node it is first attached to.

With --encrypted the node writes a LUKS qcow2 and cannot read it without a key
the control plane hands over at start. A stolen node disk gives up nothing. The
key never touches the node's persistent storage, so the volume cannot be opened
without the control plane, and it cannot be backed up yet.

```
marstack volume create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--encrypted` |  |  | encrypt the volume on the node it lands on |
| `--from-backup` | `string` |  | restore from this backup instead of starting empty |
| `--name` | `string` |  | volume name, unique within the project |
| `--size-gib` | `int` | `0` | size in GiB |

#### `marstack volume delete`

Delete a volume and the data on it

```
marstack volume delete <name|id>
```

#### `marstack volume detach`

Detach a volume from its instance

```
marstack volume detach <name|id>
```

#### `marstack volume list`

List volumes

```
marstack volume list
```

#### `marstack volume resize`

Grow a volume.

A volume only grows. Shrinking means choosing which bytes to lose, which is not
a decision a control plane should make on its own. The guest must be stopped,
because qemu holds the disk open while it runs, and the node applies the new
size on its next pass. The filesystem inside the guest still needs growing by
the guest.

```
marstack volume resize <volume> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--size-gib` | `int` | `0` | the new size in GiB |

#### `marstack volume snapshot`

Take, list, restore, copy and remove volume snapshots

```
marstack volume snapshot
```

| Subcommand | What it does |
|---|---|
| [`marstack volume snapshot clone`](#marstack-volume-snapshot-clone) | Make a new volume from a snapshot, leaving the original alone |
| [`marstack volume snapshot create`](#marstack-volume-snapshot-create) | Take a snapshot of a volume whose instance is stopped |
| [`marstack volume snapshot delete`](#marstack-volume-snapshot-delete) | Remove a snapshot |
| [`marstack volume snapshot list`](#marstack-volume-snapshot-list) | List snapshots, of one volume or of all of them |
| [`marstack volume snapshot restore`](#marstack-volume-snapshot-restore) | Roll a volume back to a snapshot, discarding what came after |

##### `marstack volume snapshot clone`

Make a new volume from a snapshot, leaving the original alone.

Restoring rolls a volume back and throws away what came after. This copies
instead, so the snapshot's contents turn up as a volume of their own: a copy of
production data to test against, or one file recovered without losing the rest.

A snapshot lives inside the disk file on the node that holds the volume, so the
copy is made there and lands on that node. An encrypted volume is refused: the
copy would either need its key on a second disk or write the contents out in
the clear, and neither is something to do quietly.

```
marstack volume snapshot clone <snapshot id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--name` | `string` |  | name for the new volume |

##### `marstack volume snapshot create`

Take a snapshot of a volume whose instance is stopped

```
marstack volume snapshot create <volume> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--name` | `string` |  | snapshot name, unique within the volume |

##### `marstack volume snapshot delete`

Remove a snapshot

```
marstack volume snapshot delete <snapshot id>
```

##### `marstack volume snapshot list`

List snapshots, of one volume or of all of them

```
marstack volume snapshot list [volume]
```

##### `marstack volume snapshot restore`

Roll a volume back to a snapshot, discarding what came after

```
marstack volume snapshot restore <snapshot id>
```

### `marstack webhook`

Send events to somewhere that can act on them

```
marstack webhook
```

| Subcommand | What it does |
|---|---|
| [`marstack webhook create`](#marstack-webhook-create) | Subscribe an endpoint to this project's events |
| [`marstack webhook delete`](#marstack-webhook-delete) | Delete a webhook and anything queued for it |
| [`marstack webhook deliveries`](#marstack-webhook-deliveries) | Show what was sent and what came back |
| [`marstack webhook list`](#marstack-webhook-list) | List webhooks |
| [`marstack webhook pause`](#marstack-webhook-pause) | Stop sending to a webhook |
| [`marstack webhook resume`](#marstack-webhook-resume) | Start sending to a webhook again |

#### `marstack webhook create`

Subscribe an endpoint to this project's events.

Each delivery is a POST carrying the event kind and subject, signed with an
HMAC-SHA256 of the body in the Marstack-Signature header. The signing secret is
shown once, here, and never again.

A failed delivery is retried with a growing backoff and then given up on, so a
target that is briefly down loses nothing and a target that is gone does not
queue forever. Loopback and link-local addresses are refused at the dial: the
control plane will not be used to reach itself or a metadata service.

```
marstack webhook create [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--kind` | `stringSlice` |  | only these kinds, repeatable, and instance.* matches a family |
| `--name` | `string` |  | webhook name, unique in the project |
| `--url` | `string` |  | where to post, http or https |

#### `marstack webhook delete`

Delete a webhook and anything queued for it

```
marstack webhook delete <name|id>
```

#### `marstack webhook deliveries`

Show what was sent and what came back

```
marstack webhook deliveries <name|id> [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--limit` | `int` | `0` | how many, newest first |

#### `marstack webhook list`

List webhooks

```
marstack webhook list
```

#### `marstack webhook pause`

Stop sending to a webhook

```
marstack webhook pause <name|id>
```

#### `marstack webhook resume`

Start sending to a webhook again

```
marstack webhook resume <name|id>
```

