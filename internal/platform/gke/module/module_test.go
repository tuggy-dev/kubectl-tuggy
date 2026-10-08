package module

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/engine/tofu"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/docker"
)

func TestEmbeddedFiles(t *testing.T) {
	var files []string
	if err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{".terraform.lock.hcl", "main.tf", "outputs.tf", "variables.tf", "versions.tf"}
	if !slices.Equal(files, want) {
		t.Errorf("embedded files = %v, want %v", files, want)
	}
}

// TestModuleWithOpenTofu checks formatting, validates the module against the
// real Google provider, and runs the tofu tests in tests/ (which use a mock
// provider and need no Google Cloud account). It runs OpenTofu in the pinned
// container and is skipped when no container engine is reachable.
func TestModuleWithOpenTofu(t *testing.T) {
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

	cache, dir := testDirs(t)
	if err := os.CopyFS(dir, os.DirFS(".")); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"fmt", "-check", "-recursive", "-diff"},
		{"init", "-input=false", "-no-color"},
		{"validate", "-no-color"},
		{"test", "-no-color"},
	} {
		var out bytes.Buffer
		code, err := d.Run(ctx, runner.Spec{
			Image:      tofu.DefaultImage,
			Cmd:        args,
			WorkingDir: "/module",
			Env:        map[string]string{"HOME": "/tmp", "TF_IN_AUTOMATION": "1", "TF_PLUGIN_CACHE_DIR": "/plugin-cache"},
			Mounts: []runner.Mount{
				{Type: runner.MountBind, Source: dir, Target: "/module"},
				{Type: runner.MountBind, Source: cache, Target: "/plugin-cache"},
			},
			User:   runner.CallerUser(),
			Stdout: &out,
			Stderr: &out,
		})
		if err != nil || code != 0 {
			t.Fatalf("tofu %s: exit %d, err %v\n%s", args[0], code, err, out.String())
		}
	}
}

// testDirs returns a shared provider cache and a fresh copy directory, both
// under the home directory so VM-based engines can mount them.
func testDirs(t *testing.T) (cache, dir string) {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	cache = filepath.Join(base, "tuggy-test-plugins")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err = os.MkdirTemp(base, "tuggy-gke-module-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return cache, filepath.Join(dir, "module")
}
