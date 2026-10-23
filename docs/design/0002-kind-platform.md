# 0002: kind platform

| | |
|---|---|
| **Status** | Proposal |
| **Date** | 2026-10-22 |
| **Authors** | Dinesh Bakiaraj |

## Summary

Add a `kind` platform so `kubectl tuggy create cluster --platform kind` builds a local Kubernetes cluster in Docker with the same commands, spec file and records as GKE. Unlike GKE, the kind platform does not use OpenTofu: it calls the [kind](https://kind.sigs.k8s.io/) Go library (`sigs.k8s.io/kind`) directly from the tuggy process. Users need only a Docker-compatible engine and its CLI, no cloud account.

## Motivation

- **Fast, free inner loop.** A kind cluster starts in about a minute and costs nothing. Teams that deploy to GKE want the same tool to create a local cluster for development and CI.
- **One way to work.** The same `create`, `get`, `describe`, `delete` and `doctor` commands, the same spec file, the same kubeconfig handling, locks and TTL, whichever platform is used.
- **Proves the platform interface.** A second platform with a very different build mechanism shows that the CLI is really written only against `platform.Platform` (0001, *Interfaces and extension points*).

## Goals

- `create`, `delete`, `describe`, `get clusters`, `doctor` and kubeconfig merge for kind clusters.
- Map the shared spec (`kubernetesVersion`, `nodePools`, `ttl`) onto kind nodes predictably.
- Expose the kind settings that local development needs: node image, host directory mounts, host port mappings.
- Work on macOS, Linux and Windows (Docker Desktop, Colima, Rancher Desktop, Podman).

## Non-goals (this proposal)

- Simulating cloud features: load balancers, Workload Identity, cloud IAM.
- Multi-node control planes as a default (allowed as an option, see `controlPlanes`).
- A local image registry and an image-load command; outlined under *Later*.

## Why not OpenTofu for kind

A community provider exists (`tehcyx/kind` on the OpenTofu registry), but it fits tuggy's design poorly:

| | OpenTofu + `tehcyx/kind` | kind Go library (proposed) |
|---|---|---|
| Docker access | The OpenTofu **container** needs the host's Docker socket mounted, so it can start containers on the host. The socket path differs between Docker Desktop, Colima, Podman and Windows. | tuggy runs on the host and uses Docker like the `docker` CLI does. |
| Kubeconfig | Points at `127.0.0.1:<port>`, which is wrong from inside the OpenTofu container; tuggy must patch it. | Correct by construction. |
| State | OpenTofu state duplicates what kind already keeps in Docker. | kind's own state (container labels) is the source of truth. |
| Ownership | One community maintainer. | The kind project (Kubernetes SIG Testing). |
| Speed | Pulls the OpenTofu image and the provider first. | No extra downloads beyond the node image. |

The platform interface leaves each platform free to choose how it builds clusters; OpenTofu stays where it earns its keep, for cloud platforms.

## User experience

```sh
kubectl tuggy doctor --platform kind
kubectl tuggy create cluster dev --platform kind
kubectl tuggy create cluster -f cluster-kind.yaml
kubectl tuggy get clusters
kubectl tuggy delete cluster dev
```

```yaml
apiVersion: tuggy.dev/v1alpha1
kind: Cluster
metadata:
  name: dev
spec:
  platform: kind
  kubernetesVersion: "1.36"        # empty = the kind release's default
  ttl: 8h
  nodePools:
    - name: default
      count: 2                      # two worker nodes, labelled tuggy.dev/node-pool=default
  platformConfig:
    nodeImage: ""                   # optional; overrides kubernetesVersion with an exact image
    controlPlanes: 1                # 1 (default) or 3
    apiServerPort: 0                # 0 = random free port
    extraMounts:                    # host directories visible inside every node
      - hostPath: ./data
        containerPath: /mnt/data
        readOnly: true
    extraPortMappings:              # host ports forwarded to the first control-plane node
      - containerPort: 30080
        hostPort: 8080
```

Flags fill `platformConfig` as with GKE: `--node-image`, `--control-planes`, `--api-server-port`. Mounts and port mappings are lists and stay file-only.

## Design

### Package layout

```text
internal/platform/kind/
├── kind.go           Platform, Name = "kind", init() registers it; flags
├── config.go         Config (platformConfig), defaults, validation
├── plan.go           Variables: the resolved cluster (nodes, image, mounts, ports)
├── images.go         Kubernetes minor version -> pinned node image (generated)
├── preflight.go      checks
├── lifecycle.go      Create, Delete, Describe, Kubeconfig, ListRemote
├── provider.go       small interface over the kind library (for tests)
└── progress.go       kind log lines -> platform.Progress
```

`provider.go` wraps the parts of `*cluster.Provider` tuggy uses behind an interface, so all lifecycle code is unit-testable without Docker:

```go
type kindProvider interface {
	Create(name string, opts ...cluster.CreateOption) error
	Delete(name, kubeconfigPath string) error
	List() ([]string, error)
	KubeConfig(name string, internal bool) (string, error)
	ListNodes(name string) ([]nodes.Node, error)
}
```

### Naming

The kind cluster is named `tuggy-<name>`, so tuggy clusters never collide with clusters users create with `kind` directly, and are recognisable in `docker ps` (`tuggy-dev-control-plane`, `tuggy-dev-worker`, …). The kubeconfig context is `tuggy-<name>`, as for GKE.

### Mapping the spec

| Spec | kind |
|---|---|
| `kubernetesVersion` | Looked up in `images.go`: a minor (`1.36`) or exact (`1.36.2`) version maps to a digest-pinned `kindest/node` image published for the pinned kind release. Unknown versions fail validation and list the supported ones. Empty uses the release default (`defaults.Image`; v0.33.0 ships Kubernetes 1.37.0). |
| `platformConfig.nodeImage` | Used as-is and wins over `kubernetesVersion`; for custom-built node images. Setting both is an error. |
| `nodePools[].count` | That many `worker` nodes, each labelled `tuggy.dev/node-pool=<pool>`. Workloads target a pool with a `nodeSelector`, as on GKE. |
| all counts 0 / no workers | A single control-plane node, which kind lets run workloads. |
| `nodePools[].machineType`, `spot`, `diskSizeGB` | **Rejected** with a clear message: kind nodes are containers sharing the host's resources. Strict errors keep a GKE spec from silently meaning something else. |
| `platformConfig.controlPlanes` | 1 (default) or 3 control-plane nodes. |
| `platformConfig.extraMounts` | Added to every node. Relative host paths resolve against the spec file's directory. |
| `platformConfig.extraPortMappings` | Added to the first control-plane node. |
| `ttl` | Recorded by tuggy (`ExpiresAt`) as for GKE; kind itself has no labels for it. |

`plan.go` resolves all of this into `Variables` (the platform's typed plan), which `Create` turns into a `v1alpha4.Cluster` passed with `cluster.CreateWithV1Alpha4Config`.

### Preflight (`doctor`)

| Check | Fails when | Fix |
|---|---|---|
| Container engine | Docker API not reachable (reuses the GKE check) | Start Docker Desktop, Colima, … |
| `docker` CLI | Not on `PATH`. The kind library drives the engine through the CLI, not the API. | Install the Docker CLI, or use `--provider podman` |
| Engine resources | **Warn** below ~2 GB memory per node, from `docker info` | Give the engine more memory |
| Name | A kind cluster `tuggy-<name>` already exists that tuggy has no record of | Delete it with `kind delete cluster --name tuggy-<name>`, or pick another name |
| Host ports | `apiServerPort` or an `extraPortMappings.hostPort` is in use | Pick another port |
| Mount paths | An `extraMounts.hostPath` does not exist | Fix the path |
| Linux inotify | **Warn** if `fs.inotify.max_user_watches` / `max_user_instances` are below kind's documented minimums for multi-node clusters | Raise them with `sysctl` |

The provider (Docker, Podman, nerdctl) is detected with `cluster.DetectNodeProvider`, and can be forced with `platformConfig.provider`.

### Create

1. Resolve `Variables`; write them to `<cluster dir>/kind-cluster.json` (for `describe`) and the rendered kind config to `kind-config.yaml` (for troubleshooting).
2. `--dry-run`: report the planned nodes as `platform.Changes` (one `kind_node` resource per container, action `create`) and stop.
3. `provider.Create("tuggy-<name>", CreateWithV1Alpha4Config(cfg), CreateWithNodeImage(img), CreateWithWaitForReady(5*time.Minute), CreateWithKubeconfigPath(<temp file in cluster dir>), CreateWithRetain(false), CreateWithDisplayUsage(false), CreateWithDisplaySalutation(false))`.
   - The explicit kubeconfig path keeps kind from editing `~/.kube/config`; tuggy merges its own entries as for GKE. The temp file is removed afterwards.
   - `retain=false` makes kind remove half-created nodes on failure, so a failed create leaves nothing behind.
4. Progress: a `log.Logger` adapter turns kind's status lines ("Ensuring node image", "Preparing nodes", "Starting control-plane", "Installing CNI", "Joining worker nodes", …) into `platform.ProgressInfo` updates, so the CLI shows the same progress UI as GKE. Full output goes to `opts.Log`.
5. Outputs saved in the record: `endpoint` (`https://127.0.0.1:<port>`), `kubernetes_version` (from `nodeutils.KubeVersion`), `node_image`, `provider`.
6. The CLI's existing readiness check (`kubeconfig.WaitReady`) then runs unchanged.

### Delete

`provider.ListNodes` lists the containers for `Confirm` and `--dry-run` (as `kind_node` removals); then `provider.Delete("tuggy-<name>", <temp kubeconfig>)`. Deleting a cluster whose containers are already gone succeeds, so the record can always be cleaned up.

### Describe, kubeconfig, list

- `Describe` reads `kind-cluster.json`, like GKE reads its variables file.
- `Kubeconfig` calls `provider.KubeConfig("tuggy-<name>", false)` **each time** and renames the entries to `tuggy-<name>`. kind's kubeconfig embeds the cluster's admin client certificate; fetching it live means tuggy never stores that credential in its own record. It still ends up in the user's kubeconfig, as with `kind` itself.
- `ListRemote` returns kind clusters on the machine that have no tuggy record (`provider.List()` minus records), so `get clusters --all` shows them.

### Cross-cutting

- Locks, records, statuses, TTL reporting and `get`/`describe` output are CLI-level and unchanged.
- **Dependencies:** `sigs.k8s.io/kind` (Apache-2.0), pinned. tuggy already depends on `client-go`, so the extra dependency surface is small. The kind version determines the node images; `images.go` is regenerated from that release's notes when the dependency is bumped, and a test checks that the default image matches `defaults.Image`.
- **Windows:** kind supports Docker Desktop (WSL 2). Host paths in `extraMounts` are passed through as Windows paths, which Docker Desktop translates.

## CLI impact

The `tuggy.dev/v1alpha1` format and the `platform.Platform` interface do not change: a kind cluster is `spec.platform: kind` with kind's own fields under `platformConfig`. There are no new commands. Spec files (`-f`) are unaffected because they always name their platform.

Flag-based commands do change, because tuggy goes from one registered platform to two. `internal/cli/spec.go` and `doctor.go` need three fixes, all in milestone 1:

### 1. Choosing the platform without `--platform`

Today `specFlags.build` uses the only registered platform when `--platform` is omitted, and otherwise fails with `--platform is required`. With kind registered, `kubectl tuggy create cluster dev --project acme --location us-central1-a`, which works today, would start failing.

**Proposed: infer the platform from platform-specific flags.** Each platform flag is already annotated with its owner (`tuggy.dev/platform-flag`). If every platform flag the user set belongs to one platform, that platform is used; if none was set, or flags from several platforms were set, `--platform` is required, with an error naming the candidates.

| Command | Result |
|---|---|
| `create cluster dev --project acme --location us-central1-a` | gke (unchanged from today) |
| `create cluster dev --node-image kindest/node:v1.36.2` | kind |
| `create cluster dev --platform kind` | kind |
| `create cluster dev` | error: `--platform is required (one of gke, kind)` |
| `create cluster dev --project acme --node-image …` | error: flags for both gke and kind |

Alternatives considered: keep `gke` as the default (simple, but arbitrary once there are several platforms, and a bare `create cluster dev` would quietly create a billed cloud cluster); or require `--platform` always (clear, but breaks existing commands while tuggy is pre-1.0).

### 2. Rejecting another platform's flags

All platforms' flags are registered on `create` and `doctor`, and each platform's `PlatformConfigFromFlags` reads only its own, so `--platform kind --project acme` would silently drop `--project`. The fix reuses the owner annotation: a flag owned by a platform other than the selected one is a usage error (`--project is a gke flag; this cluster uses kind`). This is the same mechanism that already rejects platform flags combined with `-f`.

### 3. `doctor`'s default location

`doctor` sets `--location us-central1` when it is not given, because GKE checks need a location but don't depend on which. That must only happen when the selected platform is gke; otherwise fix 2 would reject it as a gke flag on a kind check. Better still, move the default into the gke platform (a `doctor`-only default in its flag handling) so the CLI stays platform-neutral.

### Visible but unchanged behaviour

- `create --help` and `doctor --help` list kind's flags (`--node-image`, `--control-planes`, `--api-server-port`), labelled `(kind)` as GKE's are labelled `(gke)`.
- `--node-count` counts worker nodes on kind; the control-plane node is extra. `--machine-type` and `--spot` are rejected for kind with a clear message.
- `get clusters` shows `-` in the Location column for kind clusters (or `local`; see open questions).

## Differences from GKE that users will notice

| | GKE | kind |
|---|---|---|
| Google identity for pods | Workload Identity | **None.** Pods reach Google APIs only with an explicit credentials file (Secret), or avoid them, e.g. by reading data from an `extraMounts` directory. |
| `Service type=LoadBalancer` | Real load balancer | Stays pending unless a local LB (e.g. cloud-provider-kind) is installed; use `NodePort` + `extraPortMappings`. |
| Images | Pulled from registries with the node service account | Public images pull normally; private and locally built images need loading into the nodes (see *Later*). |
| Cost / time | Billed; several minutes | Free; about a minute |

## Example: app development against local data

```yaml
spec:
  platform: kind
  nodePools: [{name: default, count: 1}]
  platformConfig:
    extraMounts:
      - {hostPath: ./data, containerPath: /mnt/data, readOnly: true}
    extraPortMappings:
      - {containerPort: 30080, hostPort: 8080}
```

A pod mounts `/mnt/data` with a `hostPath` volume and reads the developer's local files directly, with no cloud credentials in the cluster. A `NodePort` service on 30080 makes the app reachable at `http://localhost:8080`.

## Testing

- **Unit:** config validation and spec mapping (versions, pools, rejected fields, mounts, ports); `Variables` → `v1alpha4.Cluster` golden file; lifecycle against a fake `kindProvider` (create success/failure, dry-run, delete of a missing cluster, kubeconfig renaming, ListRemote filtering); preflight with fake Docker/CLI/port probes; progress mapping.
- **CLI:** platform inference (each row of the table above), rejection of another platform's flags with and without `-f`, and `doctor` defaulting a location only for gke.
- **End-to-end:** `TUGGY_E2E_KIND=1` builds a one-node cluster, checks `kubectl get nodes` through the merged kubeconfig, then deletes it. Runs in CI on the Linux job only; GitHub's macOS and Windows runners have no Docker.

## Milestones

1. **Core:** platform skeleton and registration; config, mapping, `images.go`; preflight (engine, CLI, name, ports, mounts); create/delete/describe/kubeconfig; progress; the three CLI fixes (platform inference, rejecting other platforms' flags, gke-only `doctor` location default); unit tests; docs (quickstart section, example spec).
2. **Polish:** `ListRemote`; resource and inotify warnings; Podman/nerdctl selection; Linux CI end-to-end test.
3. **Later (separate proposals):** `kubectl tuggy load image <image> --cluster <name>` via an optional `platform.ImageLoader` interface (kind: `nodeutils.LoadImageArchive` on every node; GKE: not supported); an optional local registry wired into the nodes; LoadBalancer support via cloud-provider-kind.

## Open questions

1. **Strict or lenient** for GKE-only fields (`machineType`, `spot`, `diskSizeGB`) under `platform: kind`? Proposed: strict errors, so one spec file can't mean different things.
2. **Prefix:** `tuggy-<name>` for the kind cluster name (proposed), or the bare name so `kind` commands work without the prefix?
3. **Version table:** generate `images.go` by script from kind's release notes, or maintain it by hand?
4. **Podman** in milestone 1, or Docker only until there is demand?
5. **Image loading** is the most-asked kind feature after create/delete; promote it into milestone 1?
6. **Platform inference** (proposed) versus a fixed default platform or always requiring `--platform`.
7. **Location column:** show `-` or `local` for kind clusters in `get clusters` and `describe`?
