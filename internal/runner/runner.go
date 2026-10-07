// Package runner runs a command in a container and reports how it ended.
//
// Cloud platforms use it to run OpenTofu without requiring it on the user's
// machine. The Runner interface hides the container engine, so the rest of
// tuggy can be tested with runnertest.Fake and the Docker implementation can
// be swapped (for example for a local tofu binary) without other changes.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// ManagedLabel is set on every container tuggy starts, so leftovers can be
// found and removed.
const ManagedLabel = "dev.tuggy.managed"

// Runner runs commands in containers.
type Runner interface {
	// Ping checks that the container engine is reachable.
	Ping(ctx context.Context) error

	// EnsureImage makes image available locally, pulling it if possible. If
	// the pull fails but the image is already present, it warns on progress
	// and continues.
	EnsureImage(ctx context.Context, image string, progress io.Writer) error

	// Run starts a container from spec, streams its output, waits for it to
	// exit, removes it, and returns its exit code. The error is non-nil only
	// when the container could not be run or was cancelled: a command that
	// runs and fails returns its non-zero exit code and a nil error.
	//
	// If ctx is cancelled, Run asks the command to stop with SIGINT, waits
	// for a grace period, then kills it, and returns ctx.Err().
	Run(ctx context.Context, spec Spec) (exitCode int, err error)
}

// Spec describes one container run.
type Spec struct {
	// Image to run, for example "ghcr.io/tuggy-dev/tofu:1.13.1".
	Image string

	// Entrypoint overrides the image's entrypoint when set.
	Entrypoint []string

	// Cmd is the command or arguments to run.
	Cmd []string

	// WorkingDir inside the container.
	WorkingDir string

	// Env sets environment variables inside the container.
	Env map[string]string

	// Mounts are host paths or named volumes made available in the container.
	Mounts []Mount

	// Labels are added to the container, in addition to ManagedLabel.
	Labels map[string]string

	// User to run as, "uid:gid". Empty uses the image's default user.
	User string

	// Stdout and Stderr receive the command's output separately. Nil
	// discards it.
	Stdout io.Writer
	Stderr io.Writer
}

// MountType says what a Mount's Source refers to.
type MountType string

// Mount types.
const (
	// MountBind makes a host directory or file available. Source must be
	// an absolute host path.
	MountBind MountType = "bind"

	// MountVolume uses a named volume managed by the container engine,
	// created on first use. Source is the volume name.
	MountVolume MountType = "volume"
)

// Mount makes a host path or named volume available in the container.
type Mount struct {
	Type     MountType
	Source   string
	Target   string
	ReadOnly bool
}

// Validate checks that spec can be run.
func (s Spec) Validate() error {
	if s.Image == "" {
		return errors.New("container image is required")
	}
	for _, m := range s.Mounts {
		if m.Target == "" || !isAbsContainerPath(m.Target) {
			return fmt.Errorf("mount target %q must be an absolute path inside the container", m.Target)
		}
		switch m.Type {
		case MountBind:
			if !filepath.IsAbs(m.Source) {
				return fmt.Errorf("bind mount source %q must be an absolute host path", m.Source)
			}
		case MountVolume:
			if m.Source == "" {
				return errors.New("volume mount needs a volume name")
			}
		default:
			return fmt.Errorf("unknown mount type %q", m.Type)
		}
	}
	return nil
}

// Containers are Linux, so paths inside them always start with "/", even
// when tuggy runs on Windows.
func isAbsContainerPath(p string) bool {
	return len(p) > 0 && p[0] == '/'
}

// CallerUser returns "uid:gid" of the current user on Linux, where files a
// container writes into a bind mount would otherwise be owned by root. On
// macOS and Windows the container engine's VM maps ownership already, so it
// returns "".
func CallerUser() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}
