package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
)

// These tests run real containers. They are skipped when no Docker-compatible
// engine is reachable, for example on CI runners without Docker.

const testImage = "busybox:1.37"

var (
	setupOnce sync.Once
	shared    *Docker
	setupErr  error
)

func dockerForTest(t *testing.T) *Docker {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping container tests in -short mode")
	}
	setupOnce.Do(func() {
		shared, setupErr = New()
		if setupErr != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if setupErr = shared.Ping(ctx); setupErr != nil {
			return
		}
		setupErr = shared.EnsureImage(ctx, testImage, nil)
	})
	if setupErr != nil {
		t.Skipf("no usable container engine: %v", setupErr)
	}
	return shared
}

func run(t *testing.T, d *Docker, spec runner.Spec) (code int, stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	spec.Image = testImage
	spec.Stdout, spec.Stderr = &out, &errOut
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	code, err = d.Run(ctx, spec)
	return code, out.String(), errOut.String(), err
}

func TestRunExitCodes(t *testing.T) {
	d := dockerForTest(t)
	for _, want := range []int{0, 1, 3} {
		code, _, _, err := run(t, d, runner.Spec{Cmd: []string{"sh", "-c", "exit " + string(rune('0'+want))}})
		if err != nil {
			t.Fatalf("exit %d: unexpected error %v", want, err)
		}
		if code != want {
			t.Errorf("exit code = %d, want %d", code, want)
		}
	}
}

func TestRunSeparatesStdoutAndStderr(t *testing.T) {
	d := dockerForTest(t)
	_, stdout, stderr, err := run(t, d, runner.Spec{Cmd: []string{"sh", "-c", "echo to-out; echo to-err >&2; echo more-out"}})
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "to-out\nmore-out\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "to-err\n" {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRunEnvWorkingDirAndEntrypoint(t *testing.T) {
	d := dockerForTest(t)
	_, stdout, _, err := run(t, d, runner.Spec{
		Entrypoint: []string{"sh", "-c"},
		Cmd:        []string{`echo "$GREETING $(pwd)"`},
		WorkingDir: "/tmp",
		Env:        map[string]string{"GREETING": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "hello /tmp\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

// mountableDir returns a temporary directory under the user's home. Engines
// that run in a VM (Docker Desktop, Colima) only share the home directory by
// default, so the system temp directory may not be visible to containers.
func mountableDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "tuggy-runner-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestRunBindMount(t *testing.T) {
	d := dockerForTest(t)
	dir := mountableDir(t)
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("from-host"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stdout, stderr, err := run(t, d, runner.Spec{
		Cmd:        []string{"sh", "-c", "cat input.txt; echo from-container > output.txt"},
		WorkingDir: "/workspace",
		Mounts:     []runner.Mount{{Type: runner.MountBind, Source: dir, Target: "/workspace"}},
		User:       runner.CallerUser(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "from-host" {
		t.Errorf("container did not see host file: stdout %q, stderr %q", stdout, stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil || string(got) != "from-container\n" {
		t.Errorf("host did not see container file: %q, %v", got, err)
	}
}

func TestRunReadOnlyMount(t *testing.T) {
	d := dockerForTest(t)
	dir := mountableDir(t)
	code, _, _, err := run(t, d, runner.Spec{
		Cmd:    []string{"sh", "-c", "touch /creds/new"},
		Mounts: []runner.Mount{{Type: runner.MountBind, Source: dir, Target: "/creds", ReadOnly: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Error("writing to a read-only mount should fail")
	}
}

func TestRunNamedVolumePersists(t *testing.T) {
	d := dockerForTest(t)
	volume := "tuggy-test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "-" + time.Now().Format("150405")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = d.cli.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{Force: true})
	})
	m := []runner.Mount{{Type: runner.MountVolume, Source: volume, Target: "/cache"}}

	if _, _, _, err := run(t, d, runner.Spec{Cmd: []string{"sh", "-c", "echo cached > /cache/plugin"}, Mounts: m}); err != nil {
		t.Fatal(err)
	}
	_, stdout, _, err := run(t, d, runner.Spec{Cmd: []string{"cat", "/cache/plugin"}, Mounts: m})
	if err != nil || stdout != "cached\n" {
		t.Errorf("second run read %q, %v; want data from the first run", stdout, err)
	}
}

// signalWriter reports when a marker appears in the output.
type signalWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marker string
	seen   chan struct{}
	once   sync.Once
}

func newSignalWriter(marker string) *signalWriter {
	return &signalWriter{marker: marker, seen: make(chan struct{})}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.marker) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestRunCancelSendsSIGINT(t *testing.T) {
	d := dockerForTest(t)
	out := newSignalWriter("ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-out.seen:
			cancel()
		case <-time.After(time.Minute):
		}
	}()

	start := time.Now()
	code, err := d.Run(ctx, runner.Spec{
		Image:  testImage,
		Cmd:    []string{"sh", "-c", "trap 'echo got-SIGINT; exit 130' INT; echo ready; while true; do sleep 0.1; done"},
		Stdout: out,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if code != 130 {
		t.Errorf("exit code = %d, want 130 from the command's own SIGINT handler", code)
	}
	if !strings.Contains(out.String(), "got-SIGINT") {
		t.Errorf("command never saw SIGINT; output %q", out.String())
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("graceful stop took %s", elapsed)
	}
}

func TestRunCancelKillsAfterGracePeriod(t *testing.T) {
	d := dockerForTest(t)
	impatient := *d
	impatient.StopTimeout = 2 * time.Second

	out := newSignalWriter("ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-out.seen
		cancel()
	}()

	start := time.Now()
	_, err := impatient.Run(ctx, runner.Spec{
		Image:  testImage,
		Cmd:    []string{"sh", "-c", "trap '' INT; echo ready; while true; do sleep 0.1; done"}, // ignores SIGINT
		Stdout: out,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	elapsed := time.Since(start)
	if elapsed < impatient.StopTimeout {
		t.Errorf("returned after %s, before the %s grace period ended", elapsed, impatient.StopTimeout)
	}
	if elapsed > 20*time.Second {
		t.Errorf("kill after grace period took %s", elapsed)
	}
}

func TestRunRemovesContainers(t *testing.T) {
	d := dockerForTest(t)
	label := runner.ManagedLabel + "-test=" + time.Now().Format("150405.000000")
	key, value, _ := strings.Cut(label, "=")

	if _, _, _, err := run(t, d, runner.Spec{Cmd: []string{"true"}, Labels: map[string]string{key: value}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := run(t, d, runner.Spec{Cmd: []string{"false"}, Labels: map[string]string{key: value}}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", label)})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("%d containers left behind", len(list.Items))
	}
}

func TestRunMissingImage(t *testing.T) {
	d := dockerForTest(t)
	_, err := d.Run(context.Background(), runner.Spec{Image: "tuggy.invalid/does-not-exist:0", Cmd: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "creating container") {
		t.Errorf("err = %v, want a container creation error", err)
	}
}

func TestEnsureImage(t *testing.T) {
	d := dockerForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Run("unknown image fails", func(t *testing.T) {
		err := d.EnsureImage(ctx, "tuggy.invalid/does-not-exist:0", nil)
		if err == nil || !strings.Contains(err.Error(), "pulling image") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("falls back to a local copy when pull fails", func(t *testing.T) {
		local := "tuggy.invalid/busybox:test-" + time.Now().Format("150405")
		if _, err := d.cli.ImageTag(ctx, client.ImageTagOptions{Source: testImage, Target: local}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.cli.ImageRemove(context.Background(), local, client.ImageRemoveOptions{}) })

		var progress bytes.Buffer
		if err := d.EnsureImage(ctx, local, &progress); err != nil {
			t.Fatalf("err = %v, want fallback to the local image", err)
		}
		if !strings.Contains(progress.String(), "using the copy already on this machine") {
			t.Errorf("progress = %q, want a warning", progress.String())
		}
	})
}

func TestPingUnreachable(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "no-engine.sock"))
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err == nil || !strings.Contains(err.Error(), "cannot reach Docker at unix://") {
		t.Errorf("Ping = %v", err)
	}
}
