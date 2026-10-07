// Package docker implements runner.Runner with the Docker Engine API. It
// works with any engine that speaks that API: Docker Engine, Docker Desktop,
// Colima, Rancher Desktop, OrbStack and Podman.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
)

const (
	// DefaultStopTimeout is how long a cancelled command gets to exit after
	// SIGINT before it is killed. OpenTofu uses this time to release its
	// state lock and record what it already created.
	DefaultStopTimeout = 60 * time.Second

	// cleanupTimeout bounds container removal and output flushing.
	cleanupTimeout = 30 * time.Second
)

// Docker runs containers through the Docker Engine API.
type Docker struct {
	cli      *client.Client
	endpoint Endpoint

	// StopTimeout is the grace period between SIGINT and SIGKILL when a run
	// is cancelled.
	StopTimeout time.Duration
}

var _ runner.Runner = (*Docker)(nil)

// New connects to the engine chosen by ResolveEndpoint. It does not contact
// the engine; call Ping to check it is reachable.
func New() (*Docker, error) {
	ep, err := ResolveEndpoint(os.Getenv, DefaultConfigDir(os.Getenv))
	if err != nil {
		return nil, err
	}

	// The client negotiates the API version with the engine automatically.
	var opts []client.Opt
	if ep.Source == SourceDockerHost {
		// Also honours DOCKER_TLS_VERIFY, DOCKER_CERT_PATH and DOCKER_API_VERSION.
		opts = append(opts, client.FromEnv)
	} else {
		opts = append(opts, client.WithHost(ep.Host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to Docker at %s: %w", ep, err)
	}
	return &Docker{cli: cli, endpoint: ep, StopTimeout: DefaultStopTimeout}, nil
}

// Endpoint returns the engine this runner talks to.
func (d *Docker) Endpoint() Endpoint { return d.endpoint }

// Close releases the connection.
func (d *Docker) Close() error { return d.cli.Close() }

// Ping checks that the engine is reachable.
func (d *Docker) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return fmt.Errorf("cannot reach Docker at %s: %w", d.endpoint, err)
	}
	return nil
}

// EnsureImage pulls image, or uses a local copy if the pull fails.
func (d *Docker) EnsureImage(ctx context.Context, image string, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}

	pullErr := d.pull(ctx, image, progress)
	if pullErr == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, err := d.cli.ImageInspect(ctx, image); err == nil {
		fmt.Fprintf(progress, "Could not pull %s (%v); using the copy already on this machine.\n", image, pullErr)
		return nil
	}
	return fmt.Errorf("pulling image %s: %w", image, pullErr)
}

func (d *Docker) pull(ctx context.Context, image string, progress io.Writer) error {
	resp, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()

	fmt.Fprintf(progress, "Pulling %s\n", image)
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return err
		}
		if msg.Error != nil {
			return errors.New(msg.Error.Message)
		}
	}
	return nil
}

// Run runs spec in a new container and removes the container afterwards.
func (d *Docker) Run(ctx context.Context, spec runner.Spec) (int, error) {
	if err := spec.Validate(); err != nil {
		return -1, err
	}
	stdout, stderr := orDiscard(spec.Stdout), orDiscard(spec.Stderr)

	id, err := d.create(ctx, spec)
	if err != nil {
		return -1, err
	}
	defer d.remove(id)

	// Attach and start waiting before starting, so no output or exit is missed.
	attach, err := d.cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return -1, fmt.Errorf("attaching to container: %w", err)
	}
	defer attach.Close()
	copied := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(stdout, stderr, attach.Reader)
		copied <- err
	}()

	// The wait uses its own context so the exit code can still be read after
	// the caller cancels.
	waitCtx, stopWaiting := context.WithCancel(context.Background())
	defer stopWaiting()
	wait := d.cli.ContainerWait(waitCtx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})

	// Start must not be cut short by cancellation: the command can begin
	// running (and producing output) before the start call returns, and a
	// cancelled start would remove it without the graceful SIGINT below.
	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	_, err = d.cli.ContainerStart(startCtx, id, client.ContainerStartOptions{})
	cancelStart()
	if err != nil {
		return -1, fmt.Errorf("starting container: %w", err)
	}

	code, err := d.waitForExit(ctx, id, wait)
	d.flush(copied)
	return code, err
}

func (d *Docker) create(ctx context.Context, spec runner.Spec) (string, error) {
	labels := maps.Clone(spec.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[runner.ManagedLabel] = "true"

	init := true // a tiny init process forwards signals to the command and reaps children
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        spec.Image,
			Entrypoint:   spec.Entrypoint,
			Cmd:          spec.Cmd,
			WorkingDir:   spec.WorkingDir,
			Env:          envList(spec.Env),
			Labels:       labels,
			User:         spec.User,
			AttachStdout: true,
			AttachStderr: true,
		},
		HostConfig: &container.HostConfig{
			Mounts: mounts(spec.Mounts),
			Init:   &init,
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating container from %s: %w", spec.Image, err)
	}
	return created.ID, nil
}

func (d *Docker) waitForExit(ctx context.Context, id string, wait client.ContainerWaitResult) (int, error) {
	select {
	case res := <-wait.Result:
		return exitCode(res)
	case err := <-wait.Error:
		return -1, fmt.Errorf("waiting for container: %w", err)
	case <-ctx.Done():
	}

	// Cancelled: ask the command to stop, then force it after the grace period.
	stopCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, _ = d.cli.ContainerKill(stopCtx, id, client.ContainerKillOptions{Signal: "SIGINT"})

	grace := time.NewTimer(d.StopTimeout)
	defer grace.Stop()
	select {
	case res := <-wait.Result:
		code, _ := exitCode(res)
		return code, ctx.Err()
	case <-wait.Error:
		return -1, ctx.Err()
	case <-grace.C:
	}

	_, _ = d.cli.ContainerKill(stopCtx, id, client.ContainerKillOptions{Signal: "SIGKILL"})
	select {
	case res := <-wait.Result:
		code, _ := exitCode(res)
		return code, ctx.Err()
	case <-wait.Error:
	case <-stopCtx.Done():
	}
	return -1, ctx.Err()
}

func exitCode(res container.WaitResponse) (int, error) {
	if res.Error != nil && res.Error.Message != "" {
		return int(res.StatusCode), fmt.Errorf("container exited with an engine error: %s", res.Error.Message)
	}
	return int(res.StatusCode), nil
}

// flush waits briefly for the last output to be copied.
func (d *Docker) flush(copied <-chan error) {
	select {
	case <-copied:
	case <-time.After(5 * time.Second):
	}
}

func (d *Docker) remove(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, _ = d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
}

func envList(env map[string]string) []string {
	keys := slices.Sorted(maps.Keys(env))
	list := make([]string, 0, len(keys))
	for _, k := range keys {
		list = append(list, k+"="+env[k])
	}
	return list
}

func mounts(in []runner.Mount) []mount.Mount {
	out := make([]mount.Mount, 0, len(in))
	for _, m := range in {
		t := mount.TypeBind
		if m.Type == runner.MountVolume {
			t = mount.TypeVolume
		}
		out = append(out, mount.Mount{Type: t, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	return out
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
