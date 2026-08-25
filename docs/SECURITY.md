# Security design

A cloud platform runs untrusted code on machines it owns and hands out network reachability
between tenants. Almost every feature here is a privilege boundary, so security is a property of
the design rather than a hardening pass at the end.

## Threat model

| Adversary | Can do | Must not be able to |
|---|---|---|
| Tenant inside an instance | run arbitrary code as root in their own instance | escape to the host, reach another tenant, read another tenant's disk |
| Tenant through the API | call any endpoint with their own token | act on a resource in another project, or escalate their own role |
| Compromised node agent | do anything on that one node | act on behalf of the control plane or another node |
| Network observer on the underlay | see node-to-node traffic | forge instance traffic or impersonate a node |
| Whoever reads the logs | read operational data | recover a token, key, or password |

Explicitly out of scope for now: an adversary with physical access to a node, and side-channel
attacks across instances on the same CPU.

## Invariants

These hold everywhere, and a change that weakens one is a design change, not a refactor.

1. **Deny by default.** A request with no matching authorization rule is refused. A firewall with
   no matching rule drops. A new endpoint is unreachable until it declares who may call it.
2. **Validate at the boundary, then trust the value.** Input is parsed into a typed value once, at
   the edge, with unknown fields rejected. Deeper layers never re-parse strings from clients.
3. **Least privilege on the host.** The agent drops every capability it does not need for the
   operation at hand. No component runs as root because it is convenient.
4. **Identity is derived, never asserted.** A caller's project comes from its authenticated
   identity, never from a field in the request body. This is the single most common source of
   cross-tenant bugs in platforms of this kind.
5. **Isolation is structural, not optional.** Anti-spoofing on a tap or veth is applied when the
   device is created, in the same code path. There is no flag to turn it off, because a flag
   eventually defaults wrong.
6. **Secrets never reach a log or an error body.** `5xx` responses carry a code and a request id;
   the detail stays server-side.
7. **Nothing is executed through a shell.** Host commands are built as argument vectors. No string
   interpolation into a command line, ever.

## Where each invariant is enforced

| Invariant | Enforced by |
|---|---|
| Deny by default | `kernel/fault` defaults to `Internal`; authorization middleware refuses unknown routes |
| Validate at the boundary | `httpx.Decode` sets `DisallowUnknownFields` and caps body size; `kernel/validate` |
| Least privilege | agent drivers, once they exist; reviewed per driver |
| Derived identity | authorization middleware puts the project on the request context; handlers read it from there |
| Structural isolation | `netdev` applies MAC and IP filters when it creates the device |
| No secret leakage | `httpx.Wrap` never writes `fault.Unwrap()` to the client |
| No shell execution | `os/exec` with argument slices; `gosec` flags `exec.Command` with a shell |

## Shift-left tooling

Run locally before every commit, and again in CI:

```sh
make check          # vet, test, staticcheck, govulncheck, gosec
make hooks          # install .githooks/pre-commit once per clone
```

| Tool | Catches |
|---|---|
| `go vet` | misuse of stdlib APIs |
| `staticcheck` | dead code, incorrect nil handling, bad conversions |
| `govulncheck` | known CVEs in dependencies actually reachable from our code |
| `gosec` | command injection, weak randomness, unhandled errors, permissive file modes |
| `gitleaks` | credentials about to be committed |
| CodeQL | taint flows across function boundaries |

`govulncheck` is preferred over a plain dependency audit because it reports only vulnerabilities on
a reachable call path, which keeps the signal usable.

## Reporting

The project is pre-release and has no users. Once it does, this section gets a contact address and
a disclosure window.
