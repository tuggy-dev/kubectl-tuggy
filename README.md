# kubectl-tuggy

> Your Kubernetes sidekick. Like a tugboat, tuggy does the heavy pulling so you can steer.

`kubectl tuggy` is a [kubectl plugin](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/) that turns common Kubernetes chores into one command, starting with creating clusters on GKE and bare metal.

```sh
kubectl tuggy create cluster dev --platform gke --project my-proj --location us-central1-a
kubectl tuggy get clusters
kubectl tuggy delete cluster dev
```

> **Status: early development.** Nothing is released yet. See the [architecture](docs/architecture.md) for how it's built, and the [design](docs/design/0001-architecture.md) and [plan](docs/implementation-plan.md) for what's coming.

## Planned features

| Feature | Status |
|---|---|
| GKE clusters | Planned for v0.1 |
| Bare metal clusters | Planned for v0.2 |
| Install add-ons (ingress, cert-manager, databases) | Planned for v0.3 |
| Install via [Krew](https://krew.sigs.k8s.io/) | Planned for v0.3 |
| EKS and AKS clusters | Later |

## How it works

For cloud platforms, tuggy runs [OpenTofu](https://opentofu.org/) modules built into the plugin, with inputs generated from your flags or spec file, in a container. Each cluster gets its own workspace under `~/.tuggy/clusters/<name>/`, so you can always inspect the exact code and state that built it.

You'll need:

- `kubectl`
- A Docker-compatible container runtime (Docker Desktop, Docker Engine, Podman, Colima, or Rancher Desktop)
- Credentials for your cloud (for example `gcloud auth application-default login`)

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) to get started, and please follow our [Code of Conduct](CODE_OF_CONDUCT.md).

To report a security issue, see [SECURITY.md](SECURITY.md). Please don't open a public issue.

## License

[Apache License 2.0](LICENSE)
