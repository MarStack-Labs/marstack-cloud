# marstack-cloud

Cloud platform yang menjalankan container, VM, dan microVM sebagai satu jenis resource, di
satu node maupun banyak baremetal, dengan kode dan API yang sama.

> Status: **awal sekali.** Baru control plane skeleton. Belum ada instance yang bisa jalan.

## Prinsip desain

| | |
|---|---|
| `N=1` adalah kasus umum | tidak ada mode "single node" — cluster satu node memakai jalur kode yang sama |
| Satu objek `instance` | container, VM, dan microVM dibedakan oleh field `isolation`, bukan resource type terpisah |
| Agent reconcile, bukan terima perintah | control plane mati tidak menjatuhkan instance yang sudah jalan |
| Native routing, tanpa enkapsulasi | MTU 1500 utuh, nol konfigurasi switch |
| Nama, bukan IP | setiap instance punya nama DNS internal sejak dibuat |

## Instance

| `isolation` | Dijalankan oleh | Untuk |
|---|---|---|
| `container` | runtime sendiri (namespace, cgroup v2, overlayfs) | workload yang tidak butuh kernel sendiri |
| `vm` | QEMU | mesin utuh, OS bebas, console grafis |
| `microvm` | QEMU (v1) → Cloud Hypervisor (v2) | boot cepat, tetap punya kernel sendiri |
| `sandbox` | Firecracker (v3) | ephemeral, restore dari snapshot |

Nama VMM tidak pernah muncul di API maupun CLI.

## Menjalankan

```sh
make build
./bin/marstack server
```

```sh
curl -s localhost:7443/healthz
curl -s localhost:7443/v1/version
```

## Peta jalan

```
1  store + api + objek instance
2  agent + reconcile loop, isolation: container
3  netdev + nft + dns          → container saling bicara
4  image store
5  vmm/qemu                    → isolation: vm
6  node join + routing         → multi node
7  isolation: microvm
8  disk + snapshot
9  cloud hypervisor, lalu sandbox + firecracker
```

## Lisensi

Apache-2.0
