# Security Policy

## Reporting a vulnerability

Please **do not** report security issues through public GitHub issues, discussions, or pull requests.

Report them privately using [GitHub's private vulnerability reporting](https://github.com/tuggy-dev/kubectl-tuggy/security/advisories/new). Include:

- A description of the issue and its impact
- Steps to reproduce, or a proof of concept
- The tuggy version and platform you tested on

We aim to acknowledge reports within 3 business days and will keep you updated as we investigate and fix the issue. We're happy to credit reporters in the advisory unless you'd rather stay anonymous.

## Supported versions

tuggy is pre-1.0. Security fixes are made on the latest release only.

## Scope notes

tuggy handles cloud credentials and OpenTofu state, which can contain secrets. Issues where tuggy exposes credentials, writes state with overly broad permissions, or acts on the wrong cluster are in scope.
