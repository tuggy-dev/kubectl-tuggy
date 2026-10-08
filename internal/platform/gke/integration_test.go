package gke

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/engine/tofu"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform/gke/module"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/docker"
)

// contractTest plans the module with a mock Google provider, using variables
// passed in the environment, and checks they arrived with the right types.
const contractTest = `
mock_provider "google" {}

run "generated_variables" {
  command = plan

  assert {
    condition     = google_container_cluster.this.name == "prod" && google_container_cluster.this.location == "us-central1"
    error_message = "name or location"
  }
  assert {
    condition     = google_container_cluster.this.release_channel[0].channel == "STABLE" && google_container_cluster.this.min_master_version == "1.34"
    error_message = "release channel or version"
  }
  assert {
    condition     = google_container_cluster.this.network == "tuggy-vpc" && google_container_cluster.this.subnetwork == "tuggy-subnet"
    error_message = "network"
  }
  assert {
    condition     = google_container_node_pool.this["default"].node_count == 3 && google_container_node_pool.this["default"].node_config[0].machine_type == "e2-standard-4"
    error_message = "default pool"
  }
  assert {
    condition     = google_container_node_pool.this["batch"].node_count == 0 && google_container_node_pool.this["batch"].node_config[0].spot && google_container_node_pool.this["batch"].node_config[0].disk_size_gb == 200
    error_message = "batch pool"
  }
  assert {
    condition     = google_container_node_pool.this["default"].node_config[0].resource_labels["tuggy-cluster"] == "prod"
    error_message = "labels"
  }
}
`

// TestGeneratedVariablesWithOpenTofu translates the full example spec, passes
// the variables to OpenTofu exactly as the engine does (TF_VAR_ environment
// variables), and plans the real module against a mock Google provider. It
// proves OpenTofu accepts what Go produces. Skipped without a container
// engine.
func TestGeneratedVariablesWithOpenTofu(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container test in -short mode")
	}
	d, err := docker.New()
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skip(err)
	}
	if err := d.EnsureImage(ctx, tofu.DefaultImage, nil); err != nil {
		t.Skipf("cannot pull %s: %v", tofu.DefaultImage, err)
	}

	plan, err := testPlatform().FromV1Alpha1(loadSpec(t, fullSpec))
	if err != nil {
		t.Fatal(err)
	}

	base, err := os.UserCacheDir() // shared with VM-based engines
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "tuggy-test-plugins")
	work, err := os.MkdirTemp(base, "tuggy-gke-contract-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	dir := filepath.Join(work, "module")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.CopyFS(dir, module.FS); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "contract.tftest.hcl"), []byte(contractTest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tofu.WriteVariables(dir, plan.Variables); err != nil {
		t.Fatal(err)
	}
	env, err := tofu.VariablesEnv(dir)
	if err != nil {
		t.Fatal(err)
	}
	env["HOME"], env["TF_PLUGIN_CACHE_DIR"] = "/tmp", "/plugin-cache"

	for _, args := range [][]string{{"init", "-input=false", "-no-color"}, {"test", "-no-color"}} {
		var out bytes.Buffer
		code, err := d.Run(ctx, runner.Spec{
			Image: tofu.DefaultImage, Cmd: args, WorkingDir: "/module", Env: env, User: runner.CallerUser(),
			Mounts: []runner.Mount{
				{Type: runner.MountBind, Source: dir, Target: "/module"},
				{Type: runner.MountBind, Source: cache, Target: "/plugin-cache"},
			},
			Stdout: &out, Stderr: &out,
		})
		if err != nil || code != 0 {
			t.Fatalf("tofu %s: exit %d, err %v\n%s", args[0], code, err, out.String())
		}
	}
}
