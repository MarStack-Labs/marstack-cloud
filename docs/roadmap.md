# Roadmap

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
48  dual stack networks                               done
49  publishing either side of a dual stack instance   done
50  firewalls covering every address of an instance   done
51  env and config files on every isolation           done
52  tls on a published port                           done
53  scheduled snapshots                               done
54  a rate limit per project                          done
55  rolling update of a service's template            done
56  refusing a workload that names nothing            done
57  replacing a replica the node gave up on            done
58  reading what a workload printed                   done
59  work that finishes: jobs and schedules            done
60  a service that holds its own replica count        done
61  starting a container the node already holds       done
62  people, sessions and who did it                   done
63  scaling on memory, not only cpu                   done
64  a container with a working /dev and /sys          done
65  a container that can read its own limits          done
66  calling an instance by its name                   done
67  paging the audit trail and the event log          done
68  a snapshot that becomes a volume of its own       done
69  taking a node out with a vm on it                 done
70  getting inside a running container                done
71  one port serving many applications                done
72  a start that can never work, said once            done
```
