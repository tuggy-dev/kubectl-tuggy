# Contributing to kubectl-tuggy

Thanks for your interest in tuggy! This guide explains how to propose changes.

## Ways to contribute

- **Report a bug or request a feature:** open an [issue](https://github.com/tuggy-dev/kubectl-tuggy/issues/new/choose) using one of the templates.
- **Improve docs:** typo fixes and clarifications are always welcome as a pull request.
- **Write code:** for anything bigger than a small fix, open an issue first so we can agree on the approach before you invest time.

## Design proposals

Significant changes (new providers, new commands, changes to the `Cluster` spec) need a short design record in [`docs/design/`](docs/design/). Copy the format of [0001-architecture.md](docs/design/0001-architecture.md), number it sequentially, and open it as a pull request for discussion.

## Pull requests

1. Fork the repo and create a branch from `main`.
2. Keep each pull request focused on one change.
3. Add or update tests and docs alongside code changes.
4. Make sure CI passes.
5. Sign off every commit (see below).

## Developer Certificate of Origin

We use the [Developer Certificate of Origin](https://developercertificate.org/) (DCO) instead of a CLA. By signing off, you certify that you wrote the change or have the right to submit it under the project's license.

Add the sign-off with `git commit -s`, which appends a line like:

```
Signed-off-by: Your Name <you@example.com>
```

Pull requests with unsigned commits can't be merged.

## Code of Conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). By participating, you agree to uphold it.
