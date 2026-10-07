package tofu

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/docker"
)

func TestDefaultImageIsPinned(t *testing.T) {
	pinned := regexp.MustCompile(`^ghcr\.io/opentofu/opentofu:\d+\.\d+\.\d+@sha256:[0-9a-f]{64}$`)
	if !pinned.MatchString(DefaultImage) {
		t.Errorf("DefaultImage %q must be an exact version pinned by digest", DefaultImage)
	}
}

func TestImageOverride(t *testing.T) {
	t.Setenv(ImageEnv, "")
	if Image() != DefaultImage {
		t.Errorf("Image() = %q, want the default", Image())
	}
	t.Setenv(ImageEnv, "registry.example.com/mirror/opentofu:1.13.1")
	if Image() != "registry.example.com/mirror/opentofu:1.13.1" {
		t.Errorf("Image() = %q, want the override", Image())
	}
}

// TestDefaultImageRuns pulls the pinned image and runs "tofu version" through
// tuggy's own runner. Skipped when no container engine is reachable.
func TestDefaultImageRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container test in -short mode")
	}
	d, err := docker.New()
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skip(err)
	}
	if err := d.EnsureImage(ctx, DefaultImage, nil); err != nil {
		t.Skipf("cannot pull %s on this engine: %v", DefaultImage, err)
	}

	var out bytes.Buffer
	code, err := d.Run(ctx, runner.Spec{Image: DefaultImage, Cmd: []string{"version"}, Stdout: &out, User: runner.CallerUser(), Env: map[string]string{"HOME": "/tmp"}})
	if err != nil || code != 0 {
		t.Fatalf("tofu version: code %d, err %v, output %q", code, err, out.String())
	}

	version := strings.TrimPrefix(strings.SplitN(strings.TrimPrefix(DefaultImage, "ghcr.io/opentofu/opentofu:"), "@", 2)[0], "v")
	if !strings.HasPrefix(out.String(), "OpenTofu v"+version) {
		t.Errorf("output %q does not report OpenTofu v%s", out.String(), version)
	}
}
