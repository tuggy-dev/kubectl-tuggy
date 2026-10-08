# Quickstart: your first GKE cluster

This guide takes you from nothing to a running GKE cluster, and back, with `kubectl tuggy`. It takes about 15 minutes, most of it waiting for Google to build the cluster.

> **Cost.** A cluster bills while it runs. The cluster in this guide (one `e2-standard-2` node, zonal) costs a few cents per hour, plus GKE's cluster management fee where it applies. This guide sets a TTL so tuggy reminds you to delete it, and ends by deleting it.

## 1. Prerequisites

You need:

| What | Why | Check |
|---|---|---|
| `kubectl` | tuggy is a kubectl plugin | `kubectl version --client` |
| A Docker-compatible container engine: Docker Desktop, Docker Engine, Colima, Rancher Desktop, OrbStack or Podman | tuggy runs OpenTofu in a container, so you don't install OpenTofu | `docker ps` |
| The Google Cloud CLI (`gcloud`) | To sign in | `gcloud version` |
| `gke-gcloud-auth-plugin` | kubectl uses it to connect to GKE | `gke-gcloud-auth-plugin --version` |
| A Google Cloud project you can create clusters in | | see below |

Install the auth plugin if you don't have it:

```sh
gcloud components install gke-gcloud-auth-plugin
```

### Sign in to Google

tuggy uses Google **Application Default Credentials** for everything: OpenTofu, its checks, and kubectl. Sign in once:

```sh
gcloud auth application-default login
```

This is separate from `gcloud auth login`, and tuggy doesn't need that one. If your organization makes you sign in again periodically, run this command again when tuggy says your sign-in has expired.

> **Automation and CI:** instead of logging in, set `GOOGLE_APPLICATION_CREDENTIALS` to a service account key file or a workload identity federation file.

### Permissions in the project

Your account needs, in the project:

- **Kubernetes Engine Admin** (`roles/container.admin`), to create, update and delete clusters
- **Service Account User** (`roles/iam.serviceAccountUser`), so the nodes can run as the project's default service account

And the **Kubernetes Engine API** must be enabled:

```sh
gcloud services enable container.googleapis.com --project my-project
```

Don't worry about getting this exactly right up front: step 3 checks all of it and tells you what's missing.

## 2. Install tuggy

There is no release yet. Build it from source, which needs [Go](https://go.dev/dl/) 1.27 or newer:

```sh
git clone https://github.com/tuggy-dev/kubectl-tuggy.git
cd kubectl-tuggy
make install
```

`make install` puts `kubectl-tuggy` in `$(go env GOPATH)/bin`, usually `~/go/bin`. Make sure that directory is on your `PATH`, then check that kubectl finds the plugin:

```sh
kubectl plugin list        # should list .../kubectl-tuggy
kubectl tuggy version
```

> On Windows without `make`: `go build -o kubectl-tuggy.exe ./cmd/kubectl-tuggy` and put the `.exe` in a folder on your `PATH`.

## 3. Check everything is in place

Replace `my-project` with your project ID:

```sh
kubectl tuggy doctor --platform gke --project my-project
```

```text
Checking gke prerequisites
  ✓ Container engine: reachable
  ✓ gke-gcloud-auth-plugin: /Users/me/google-cloud-sdk/bin/gke-gcloud-auth-plugin
  ✓ Google credentials: /Users/me/.config/gcloud/application_default_credentials.json (authorized_user)
  ✓ Google sign-in: access token obtained
  ✓ Kubernetes Engine API in my-project: enabled
  ✓ Permissions in my-project: can create, update and delete clusters
Everything needed is in place.
```

Anything marked `✗` comes with a `fix:` line telling you exactly what to run. `!` is a warning that doesn't block you. Fix and run `doctor` again until everything is `✓`.

## 4. See what would be built (dry run)

```sh
kubectl tuggy create cluster dev --platform gke --project my-project --location us-central1-a --ttl 2h --dry-run
```

```text
Checking gke prerequisites for cluster dev
  ✓ ... (the same checks as doctor)
• Preparing OpenTofu
• Planning

Dry run: would add 2, change 0 and remove 0 resources:
  create   google_container_cluster.this
  create   google_container_node_pool.this["default"]
Nothing was created.
```

The first run downloads the OpenTofu image and the Google provider, which takes a minute; later runs take a few seconds.

**What the flags mean:**

| Flag | Meaning |
|---|---|
| `dev` | The cluster's name: lowercase letters, digits and `-`, up to 40 characters |
| `--platform gke` | Build it on Google Kubernetes Engine |
| `--project` | Your Google Cloud project ID |
| `--location` | A **zone** such as `us-central1-a` gives a zonal cluster (cheapest). A **region** such as `us-central1` gives a regional one, with nodes in several zones |
| `--ttl 2h` | tuggy reports the cluster as expired after 2 hours and reminds you to delete it. Nothing is deleted automatically |
| `--dry-run` | Plan only; build nothing |

Other useful flags: `--node-count`, `--machine-type` (default `e2-standard-2`), `--spot`, `--kubernetes-version`, `--release-channel`. Run `kubectl tuggy create cluster --help` for all of them.

## 5. Create the cluster

Run the same command without `--dry-run`:

```sh
kubectl tuggy create cluster dev --platform gke --project my-project --location us-central1-a --ttl 2h
```

```text
Checking gke prerequisites for cluster dev
  ✓ ...
• Preparing OpenTofu
• Planning
• Applying
  Creating google_container_cluster.this ...
  still creating google_container_cluster.this (1m0s)
  ...
  ✓ google_container_cluster.this created in 9m47s
  Creating google_container_node_pool.this["default"] ...
  ✓ google_container_node_pool.this["default"] created in 1m3s
• Waiting for the cluster to answer
  ✓ cluster answers (Kubernetes v1.35.8-gke.1225000)
  ✓ kubeconfig updated; current context is now "tuggy-dev"

Cluster dev is ready (created in 11m2s). It expires in 1h48m.
Delete it with: kubectl tuggy delete cluster dev
```

Building a GKE cluster takes about 10 minutes. tuggy only says "ready" once the cluster actually answers, and it has already switched kubectl to it.

**If it's interrupted** (Ctrl-C, closed laptop, network error): run the **same command again**. tuggy picks up where it left off instead of creating a second cluster.

## 6. Use it

It's a normal cluster, and kubectl is already pointing at it:

```sh
kubectl get nodes
kubectl create deployment hello --image=nginx
kubectl get pods
```

See your clusters and their details:

```sh
kubectl tuggy get clusters
```

```text
NAME   PLATFORM   LOCATION        STATUS   AGE     EXPIRES
dev    gke        us-central1-a   Ready    14m5s   1h45m
```

```sh
kubectl tuggy describe cluster dev
```

To switch back to another cluster, use kubectl as usual (`kubectl config use-context ...`). To add the tuggy cluster back to your kubeconfig later, or on another file:

```sh
kubectl tuggy get kubeconfig dev --merge     # add it and switch to it
kubectl tuggy get kubeconfig dev > dev.yaml  # or just print it
```

## 7. Delete it

```sh
kubectl tuggy delete cluster dev
```

```text
• Preparing OpenTofu
• Planning removal

Deleting cluster dev (gke) will remove 2 resources:
  - google_container_node_pool.this["default"]
  - google_container_cluster.this
Delete cluster dev? [y/N]: y
• Removing
  ...
Cluster dev deleted.
```

tuggy shows exactly what will be removed and asks first. It only touches resources it created for `dev`. It also removes the `tuggy-dev` entry from your kubeconfig. Use `--yes` to skip the question in scripts, and `--dry-run` to only see what would be removed.

**If you forget:** once a TTL has passed, every tuggy command warns you:

```text
Warning: expired clusters are still running and may be costing money: dev
         Delete them with: kubectl tuggy delete clusters --expired
```

## Using a spec file instead of flags

Everything above can live in a file you keep in version control:

```yaml
# cluster.yaml
apiVersion: tuggy.dev/v1alpha1
kind: Cluster
metadata:
  name: dev
spec:
  platform: gke
  ttl: 2h
  nodePools:
    - name: default
      machineType: e2-standard-2
      count: 1
  platformConfig:
    project: my-project
    location: us-central1-a
```

```sh
kubectl tuggy create cluster -f cluster.yaml --dry-run
kubectl tuggy create cluster -f cluster.yaml
```

Settings go either in the file or in flags, not both: `-f` together with `--ttl` or `--project` is an error. A fully annotated example is in [examples/cluster-gke.yaml](examples/cluster-gke.yaml).

## Acting as a service account

To build clusters as a service account (for example one your team uses for infrastructure) instead of as yourself:

```sh
kubectl tuggy doctor --platform gke --project my-project \
  --impersonate-service-account infra@my-project.iam.gserviceaccount.com
```

Your account needs the **Service Account Token Creator** role on that service account, and `doctor` then checks the project permissions *as the service account*. Add the same flag to `create cluster`, or `impersonateServiceAccount:` under `platformConfig` in a spec file. kubectl still connects as you, so your own account needs GKE access too (for example `roles/container.developer`).

## Troubleshooting

| Problem | Fix |
|---|---|
| `Google sign-in: your Google sign-in has expired or was revoked (... invalid_rapt ...)` | Your organization requires signing in again. Run `gcloud auth application-default login`. |
| `Container engine: cannot reach Docker at ...` | Start your engine (Docker Desktop, `colima start`, …) and check `docker ps` works in the same terminal. tuggy finds the engine like `docker` does: `DOCKER_HOST`, then the current `docker context`. |
| `Permissions in my-project: your account is missing ...` | Ask a project admin for `roles/container.admin` and `roles/iam.serviceAccountUser`. |
| `Kubernetes Engine API in my-project: not enabled` | `gcloud services enable container.googleapis.com --project my-project` |
| `gke-gcloud-auth-plugin: not found on PATH` | `gcloud components install gke-gcloud-auth-plugin`, then open a new terminal. |
| `another kubectl tuggy command is working on cluster "dev"` | Another terminal is creating or deleting it. Wait for it to finish. |
| `cluster "dev" already exists (Ready)` | Pick another name, or delete it first. |
| Creation failed partway, e.g. `Insufficient regional quota` | Fix the cause (here: request quota, or use another zone), then run the same `create` command again to finish, or `kubectl tuggy delete cluster dev` to remove what was built. |
| `confirmation needed but no terminal is attached` | In scripts and CI, add `--yes`. |
| A strange error from OpenTofu | Re-run with `-v` to see OpenTofu's full output. Every run's full log is also kept; the error message gives its path. |
| Using a custom `TUGGY_HOME` with Docker Desktop or Colima | Keep it inside your home folder: engines that run in a VM only share the home folder with containers by default. |

## Where tuggy keeps things

| Path | What |
|---|---|
| `~/.tuggy/clusters/<name>/` | Each cluster's record, its OpenTofu files and state, and logs |
| `~/.tuggy/cache/` | Downloaded OpenTofu providers, shared by all clusters |
| `~/.tuggy/logs/<name>/` | Logs of deleted clusters |
| `~/.kube/config` | Your kubeconfig. tuggy only adds and removes entries named `tuggy-<name>` |

Set `TUGGY_HOME` to use another location. For how all of this works, see the [architecture](architecture.md).
