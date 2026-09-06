# Contributing

Thanks for taking the time. This is a pre-1.0 project, so the shape of things can still change —
if you are about to spend real effort, open an issue first and we can agree on the approach before
you write it.

## Getting set up

Go 1.26 and a Linux host for anything that actually runs a workload. macOS runs the control plane
and the client fine; the [Lima VM](lima/README.md) gives you the rest.

```sh
make hooks      # once per clone: install the pre-commit hook
make tools      # once: install staticcheck, govulncheck, gosec
make check      # the gate: vet, cross build, race tests, staticcheck, govulncheck, gosec
```

The pre-commit hook runs the same checks CI does, so a commit that lands locally lands in CI.

## What a change looks like

**A change to behaviour comes with a test that fails without it.** The bar is not coverage, it is
whether the test would notice. A useful way to check your own work: break the rule you just wrote —
invert the condition, delete the guard — and see whether a test goes red. If none does, the test is
describing the code rather than holding it to anything.

**Boundaries are enforced, not suggested.** `internal/kernel` may not import `internal/platform`,
`internal/app` or `internal/store`, and `cmd` may only wire `internal/cli`.
`internal/architecture/rules_test.go` fails the build if that stops being true. The reasoning is in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

**Refuse rather than guess.** When the input is ambiguous or the state is not what was expected, the
platform says no and says why. A silent fallback is a bug that surfaces somewhere else later.

**Error messages are for the person reading them at 3am.** Say what happened, and what they can do
about it.

## Commits

Conventional commits — `feat:`, `fix:`, `docs:`, `test:`, `build:`, `perf:`, `refactor:`, with an
optional scope like `feat(console):`. The subject says what changed; the body says why, and what it
now guarantees that it did not before.

Keep unrelated changes in separate commits.

## Documentation

`docs/cli.md` is generated. If you add or change a command or a flag, run `make docs` and commit the
result — CI fails if the reference and the binary disagree.

Prose lives in [docs/guide.md](docs/guide.md), and anything that changes what a release contains
goes in [CHANGELOG.md](CHANGELOG.md) under `Unreleased`.

## Reporting a vulnerability

Do not open a public issue. See [docs/SECURITY.md](docs/SECURITY.md).
