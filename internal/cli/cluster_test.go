package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/kubeconfig"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform/platformtest"
)

// testEnv is a tuggy with a fake platform, a temporary home and kubeconfig,
// and a controllable clock: no cloud, Docker or real files are touched.
type testEnv struct {
	app        *App
	fake       *platformtest.Fake
	kubeconfig string
	now        time.Time
	readyErr   error
	readyCalls int
	input      string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	t.Setenv(clientcmd.RecommendedConfigPathEnvVar, kc)

	env := &testEnv{fake: platformtest.New("fake"), kubeconfig: kc, now: time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)}
	reg := platform.NewRegistry()
	reg.Register(env.fake)
	root := filepath.Join(dir, "tuggy")
	env.app = &App{
		Root:      root,
		Store:     clustermeta.NewLocal(root),
		Platforms: reg,
		Kube:      kubeconfig.New(),
		WaitReady: func(context.Context, *clientcmdapi.Config, kubeconfig.ReadyOptions) (string, error) {
			env.readyCalls++
			return "v1.34.1", env.readyErr
		},
		Now: func() time.Time { return env.now },
	}
	return env
}

func (e *testEnv) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return e.runCtx(t, context.Background(), args...)
}

func (e *testEnv) runCtx(t *testing.T, ctx context.Context, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	e.app.Streams = IOStreams{In: strings.NewReader(e.input), Out: &out, ErrOut: &errOut}
	code = e.app.Run(ctx, args)
	return code, out.String(), errOut.String()
}

func (e *testEnv) status(t *testing.T, name string) clustermeta.Status {
	t.Helper()
	rec, err := e.app.Store.Get(name)
	if errors.Is(err, clustermeta.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return rec.Status
}

func (e *testEnv) mustCreate(t *testing.T, args ...string) {
	t.Helper()
	if code, out, errOut := e.run(t, append([]string{"create", "cluster"}, args...)...); code != ExitOK {
		t.Fatalf("create %v: exit %d\n%s%s", args, code, out, errOut)
	}
}

func TestCreateCluster(t *testing.T) {
	e := newEnv(t)
	code, out, errOut := e.run(t, "create", "cluster", "dev", "--platform", "fake", "--ttl", "8h")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	rec, err := e.app.Store.Get("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != clustermeta.StatusReady || rec.Platform != "fake" || rec.Outputs["endpoint"] == "" {
		t.Errorf("record = %+v", rec)
	}
	if rec.ExpiresAt == nil || !rec.ExpiresAt.Equal(e.now.Add(8*time.Hour)) {
		t.Errorf("expires at %v", rec.ExpiresAt)
	}
	if got := e.fake.Calls(); !slices.Equal(got, []string{"FromV1Alpha1", "Preflight", "Create", "Kubeconfig"}) {
		t.Errorf("platform calls = %v", got)
	}
	if e.readyCalls != 1 {
		t.Errorf("readiness checked %d times", e.readyCalls)
	}
	cfg, err := clientcmd.LoadFromFile(e.kubeconfig)
	if err != nil || cfg.CurrentContext != "tuggy-dev" {
		t.Errorf("kubeconfig current context = %q (%v)", cfg.CurrentContext, err)
	}
	for _, want := range []string{"cluster answers (Kubernetes v1.34.1)", "Cluster dev is ready", "It expires in 8h0m", "kubectl tuggy delete cluster dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	logs, _ := filepath.Glob(filepath.Join(e.app.Store.Dir("dev"), "logs", "*-create.log"))
	if len(logs) != 1 {
		t.Errorf("create logs = %v", logs)
	}
}

func TestCreateDefaultsToOnlyPlatform(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev")
	if e.status(t, "dev") != clustermeta.StatusReady {
		t.Error("with one platform, --platform should not be needed")
	}
}

func TestCreateExistingCluster(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev")
	code, _, errOut := e.run(t, "create", "cluster", "dev")
	if code != ExitError || !strings.Contains(errOut, `cluster "dev" already exists (Ready)`) {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

func TestCreateInvalidInput(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"create", "cluster"}, "a cluster name is required"},
		{[]string{"create", "cluster", "My_Cluster"}, "metadata.name"},
		{[]string{"create", "cluster", "dev", "--ttl", "soon"}, "--ttl: invalid duration"},
		{[]string{"create", "cluster", "dev", "--ttl", "5m"}, "spec.ttl: must be at least 30m"},
		{[]string{"create", "cluster", "dev", "--platform", "eks"}, `unknown platform "eks" (available: fake)`},
		{[]string{"create", "cluster", "dev", "--node-count", "0"}, "at least one node"},
	}
	for _, tt := range tests {
		e := newEnv(t)
		code, _, errOut := e.run(t, tt.args...)
		if code != ExitUsage || !strings.Contains(errOut, tt.want) {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 and %q", tt.args, code, errOut, tt.want)
		}
		if len(e.fake.Calls()) > 0 && slices.Contains(e.fake.Calls(), "Create") {
			t.Errorf("%v: nothing should be created on invalid input", tt.args)
		}
	}
}

func TestCreateFromFile(t *testing.T) {
	e := newEnv(t)
	spec := filepath.Join(t.TempDir(), "cluster.yaml")
	writeFile(t, spec, "apiVersion: tuggy.dev/v1alpha1\nkind: Cluster\nmetadata: {name: from-file}\nspec:\n  platform: fake\n  ttl: 2h\n")

	e.mustCreate(t, "-f", spec)
	if e.status(t, "from-file") != clustermeta.StatusReady {
		t.Error("cluster from file not created")
	}

	code, _, errOut := e.run(t, "create", "cluster", "-f", spec, "--ttl", "3h")
	if code != ExitUsage || !strings.Contains(errOut, "--ttl cannot be combined with -f") {
		t.Errorf("-f with --ttl: exit %d, %q", code, errOut)
	}
	code, _, errOut = e.run(t, "create", "cluster", "other", "-f", spec)
	if code != ExitUsage || !strings.Contains(errOut, `does not match metadata.name "from-file"`) {
		t.Errorf("name mismatch: exit %d, %q", code, errOut)
	}
}

func TestCreatePreflightFailure(t *testing.T) {
	e := newEnv(t)
	e.fake.Checks = []platform.CheckResult{
		{Name: "Engine", Status: platform.CheckPass, Message: "reachable"},
		{Name: "Credentials", Status: platform.CheckFail, Message: "none found", Fix: "log in first"},
	}
	code, out, errOut := e.run(t, "create", "cluster", "dev")
	if code != ExitPreflight || !strings.Contains(errOut, "prerequisites are missing") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "✗ Credentials: none found") || !strings.Contains(out, "fix: log in first") {
		t.Errorf("checks not shown:\n%s", out)
	}
	if e.status(t, "dev") != "" || slices.Contains(e.fake.Calls(), "Create") {
		t.Error("nothing may be recorded or created when preflight fails")
	}
}

func TestCreateFailureThenResume(t *testing.T) {
	e := newEnv(t)
	e.fake.CreateErr = errors.New("quota exceeded")
	code, _, errOut := e.run(t, "create", "cluster", "dev")
	if code != ExitCloud {
		t.Fatalf("exit %d, want %d", code, ExitCloud)
	}
	if !strings.Contains(errOut, "quota exceeded") || !strings.Contains(errOut, "Run the same command again") {
		t.Errorf("stderr = %q", errOut)
	}
	rec, _ := e.app.Store.Get("dev")
	if rec.Status != clustermeta.StatusFailed || rec.Message != "quota exceeded" {
		t.Errorf("record = %+v", rec)
	}

	e.fake.CreateErr = nil
	code, out, _ := e.run(t, "create", "cluster", "dev")
	if code != ExitOK || !strings.Contains(out, "Resuming cluster dev (last attempt: quota exceeded)") {
		t.Errorf("resume: exit %d\n%s", code, out)
	}
	if e.status(t, "dev") != clustermeta.StatusReady {
		t.Error("resumed cluster should be Ready")
	}
}

func TestCreateCancelled(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.app.WaitReady = func(context.Context, *clientcmdapi.Config, kubeconfig.ReadyOptions) (string, error) {
		cancel()
		return "", context.Canceled
	}
	code, _, _ := e.runCtx(t, ctx, "create", "cluster", "dev")
	if code != ExitCancelled {
		t.Errorf("exit %d, want %d", code, ExitCancelled)
	}
	if rec, _ := e.app.Store.Get("dev"); rec.Status != clustermeta.StatusFailed || rec.Message != "cancelled" {
		t.Errorf("record = %+v", rec)
	}
}

func TestCreateNotReady(t *testing.T) {
	e := newEnv(t)
	e.readyErr = errors.New("cluster API server did not become ready within 2m0s")
	code, _, errOut := e.run(t, "create", "cluster", "dev")
	if code != ExitCloud || !strings.Contains(errOut, "did not become ready") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if e.status(t, "dev") != clustermeta.StatusFailed {
		t.Error("a cluster that doesn't answer must not be reported Ready")
	}
}

func TestCreateWithoutKubeconfig(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev", "--no-kubeconfig")
	if _, err := os.Stat(e.kubeconfig); !errors.Is(err, os.ErrNotExist) {
		t.Error("--no-kubeconfig must not write the kubeconfig")
	}
	if e.readyCalls != 1 {
		t.Error("readiness should still be checked")
	}
}

func TestCreateDryRun(t *testing.T) {
	e := newEnv(t)
	code, out, errOut := e.run(t, "create", "cluster", "dev", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "Dry run: would add 1, change 0 and remove 0 resources") || !strings.Contains(out, "create   fake_cluster.this") {
		t.Errorf("output:\n%s", out)
	}
	if e.status(t, "dev") != "" {
		t.Error("a dry run must not record a cluster")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(e.app.Root, "dry-run-*")); len(leftovers) != 0 {
		t.Errorf("dry-run workspace left behind: %v", leftovers)
	}
}

func TestCreateResumesInterruptedCluster(t *testing.T) {
	e := newEnv(t)
	// A tuggy process died while creating: the record says Creating, but
	// nobody holds the lock.
	rec := clustermeta.NewRecord("dev", "fake", "v0.1.0", 0, e.now)
	_ = rec.Transition(clustermeta.StatusCreating, "", e.now)
	if err := e.app.Store.Create(rec); err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.run(t, "create", "cluster", "dev")
	if code != ExitOK || !strings.Contains(out, "interrupted while creating") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestCreateWhileLocked(t *testing.T) {
	e := newEnv(t)
	unlock, err := e.app.Store.Lock("dev")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	code, _, errOut := e.run(t, "create", "cluster", "dev")
	if code != ExitError || !strings.Contains(errOut, "another kubectl tuggy command is working on cluster") {
		t.Errorf("exit %d, %q", code, errOut)
	}
}

func TestDeleteCluster(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev")
	code, out, errOut := e.run(t, "delete", "cluster", "dev", "--yes")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "will remove 1 resources") || !strings.Contains(out, "- fake_cluster.this") || !strings.Contains(out, "Cluster dev deleted.") {
		t.Errorf("output:\n%s", out)
	}
	if e.status(t, "dev") != "" {
		t.Error("record should be gone")
	}
	cfg, _ := clientcmd.LoadFromFile(e.kubeconfig)
	if cfg.Contexts["tuggy-dev"] != nil {
		t.Error("kubeconfig context should be removed")
	}
	if logs, _ := filepath.Glob(filepath.Join(e.app.Root, "logs", "dev", "*.log")); len(logs) != 2 {
		t.Errorf("create and delete logs should be kept, got %v", logs)
	}
}

func TestDeleteAsksFirst(t *testing.T) {
	t.Run("no terminal and no --yes", func(t *testing.T) {
		e := newEnv(t)
		e.mustCreate(t, "dev")
		code, _, errOut := e.run(t, "delete", "cluster", "dev")
		if code != ExitUsage || !strings.Contains(errOut, "re-run with --yes") {
			t.Errorf("exit %d, %q", code, errOut)
		}
		if e.status(t, "dev") != clustermeta.StatusReady || slices.Contains(e.fake.Calls(), "Delete:removed") {
			t.Error("nothing may be removed without confirmation")
		}
	})

	t.Run("user says no", func(t *testing.T) {
		e := newEnv(t)
		e.mustCreate(t, "dev")
		e.app.Interactive, e.input = true, "n\n"
		code, out, _ := e.run(t, "delete", "cluster", "dev")
		if code != ExitCancelled || !strings.Contains(out, "Delete cluster dev? [y/N]") {
			t.Errorf("exit %d\n%s", code, out)
		}
		if e.status(t, "dev") != clustermeta.StatusReady {
			t.Error("declining must leave the cluster as it was")
		}
	})

	t.Run("user says yes", func(t *testing.T) {
		e := newEnv(t)
		e.mustCreate(t, "dev")
		e.app.Interactive, e.input = true, "yes\n"
		if code, _, _ := e.run(t, "delete", "cluster", "dev"); code != ExitOK || e.status(t, "dev") != "" {
			t.Errorf("exit %d, status %q", code, e.status(t, "dev"))
		}
	})
}

func TestDeleteDryRun(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev")
	code, out, _ := e.run(t, "delete", "cluster", "dev", "--dry-run")
	if code != ExitOK || !strings.Contains(out, "Dry run: nothing was removed.") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if e.status(t, "dev") != clustermeta.StatusReady {
		t.Error("dry run must not change the cluster")
	}
}

func TestDeleteFailure(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev")
	e.fake.DeleteErr = errors.New("node pool still in use")
	code, _, errOut := e.run(t, "delete", "cluster", "dev", "--yes")
	if code != ExitCloud || !strings.Contains(errOut, "was not fully deleted") {
		t.Errorf("exit %d, %q", code, errOut)
	}
	if rec, _ := e.app.Store.Get("dev"); rec.Status != clustermeta.StatusFailed || rec.Message != "delete: node pool still in use" {
		t.Errorf("record = %+v", rec)
	}
	e.fake.DeleteErr = nil
	if code, _, _ := e.run(t, "delete", "cluster", "dev", "--yes"); code != ExitOK {
		t.Errorf("retrying the delete: exit %d", code)
	}
}

func TestDeleteUnknown(t *testing.T) {
	e := newEnv(t)
	code, _, errOut := e.run(t, "delete", "cluster", "nope", "--yes")
	if code != ExitError || !strings.Contains(errOut, `cluster "nope" not found`) {
		t.Errorf("exit %d, %q", code, errOut)
	}
}

func TestExpiredClusters(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "short", "--ttl", "1h")
	e.mustCreate(t, "long", "--ttl", "3d")
	e.mustCreate(t, "forever")
	e.now = e.now.Add(2 * time.Hour)

	_, out, errOut := e.run(t, "get", "clusters")
	if !strings.Contains(errOut, "expired clusters are still running and may be costing money: short") {
		t.Errorf("no expiry warning: %q", errOut)
	}
	if !strings.Contains(out, "expired") {
		t.Errorf("table should show the expired cluster:\n%s", out)
	}

	code, out, errOut := e.run(t, "delete", "clusters", "--expired", "--yes")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if e.status(t, "short") != "" || e.status(t, "long") != clustermeta.StatusReady || e.status(t, "forever") != clustermeta.StatusReady {
		t.Error("only the expired cluster should be deleted")
	}
	if _, _, errOut := e.run(t, "get", "clusters"); strings.Contains(errOut, "expired") {
		t.Errorf("warning should be gone: %q", errOut)
	}
	if code, out, _ := e.run(t, "delete", "clusters", "--expired"); code != ExitOK || !strings.Contains(out, "No expired clusters.") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if code, _, _ := e.run(t, "delete", "clusters"); code != ExitUsage {
		t.Errorf("delete clusters without --expired: exit %d", code)
	}
}

func TestGetClusters(t *testing.T) {
	e := newEnv(t)
	if _, out, _ := e.run(t, "get", "clusters"); !strings.Contains(out, "No clusters yet") {
		t.Errorf("empty list:\n%s", out)
	}
	e.mustCreate(t, "dev", "--ttl", "8h")
	e.now = e.now.Add(9 * time.Minute)

	_, out, _ := e.run(t, "get", "clusters")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("table:\n%s", out)
	}
	fields := strings.Fields(lines[1])
	if !slices.Equal(fields, []string{"dev", "fake", "fake-location", "Ready", "9m0s", "7h51m"}) {
		t.Errorf("row = %v", fields)
	}

	_, out, _ = e.run(t, "get", "clusters", "-o", "json")
	var list struct{ Clusters []clusterView }
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Clusters) != 1 || list.Clusters[0].Context != "tuggy-dev" {
		t.Errorf("json: %v\n%s", err, out)
	}
	if code, _, _ := e.run(t, "get", "clusters", "-o", "xml"); code != ExitUsage {
		t.Errorf("-o xml: exit %d", code)
	}
}

func TestDescribeCluster(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev")
	code, out, _ := e.run(t, "describe", "cluster", "dev")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"Name:", "dev", "Status:", "Ready", "Location:", "fake-location", "Expires:", "never", "Context:", "tuggy-dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if code, _, errOut := e.run(t, "describe", "cluster", "nope"); code != ExitError || !strings.Contains(errOut, "not found") {
		t.Errorf("unknown: exit %d %q", code, errOut)
	}
}

func TestGetKubeconfig(t *testing.T) {
	e := newEnv(t)
	e.mustCreate(t, "dev", "--no-kubeconfig")

	code, out, _ := e.run(t, "get", "kubeconfig", "dev")
	if code != ExitOK || !strings.Contains(out, "current-context: tuggy-dev") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if _, err := os.Stat(e.kubeconfig); !errors.Is(err, os.ErrNotExist) {
		t.Error("printing must not write the kubeconfig")
	}

	if code, out, _ := e.run(t, "get", "kubeconfig", "dev", "--merge"); code != ExitOK || !strings.Contains(out, `current context is now "tuggy-dev"`) {
		t.Errorf("--merge: exit %d\n%s", code, out)
	}
	if cfg, _ := clientcmd.LoadFromFile(e.kubeconfig); cfg.CurrentContext != "tuggy-dev" {
		t.Error("--merge should write the kubeconfig")
	}
}

func TestDoctor(t *testing.T) {
	e := newEnv(t)
	e.fake.Checks = []platform.CheckResult{{Name: "Engine", Status: platform.CheckPass, Message: "reachable"}}
	code, out, _ := e.run(t, "doctor")
	if code != ExitOK || !strings.Contains(out, "✓ Engine: reachable") || !strings.Contains(out, "Everything needed is in place.") {
		t.Errorf("exit %d\n%s", code, out)
	}

	e.fake.Checks = append(e.fake.Checks, platform.CheckResult{Name: "Plugin", Status: platform.CheckWarn, Message: "missing", Fix: "install it"})
	if code, out, _ := e.run(t, "doctor"); code != ExitOK || !strings.Contains(out, "! Plugin: missing") || !strings.Contains(out, "fix: install it") {
		t.Errorf("warning: exit %d\n%s", code, out)
	}

	e.fake.Checks = append(e.fake.Checks, platform.CheckResult{Name: "Credentials", Status: platform.CheckFail, Message: "none"})
	if code, _, _ := e.run(t, "doctor"); code != ExitPreflight {
		t.Errorf("failed check: exit %d, want %d", code, ExitPreflight)
	}
	if slices.Contains(e.fake.Calls(), "Create") {
		t.Error("doctor must never create anything")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCreateShowsNextSteps(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.kubeconfig, `apiVersion: v1
kind: Config
current-context: work
clusters: [{name: work, cluster: {server: "https://work.example.com"}}]
users: [{name: work, user: {token: x}}]
contexts: [{name: work, context: {cluster: work, user: work}}]
`)
	code, out, _ := e.run(t, "create", "cluster", "dev", "--ttl", "8h")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	steps := out[strings.Index(out, "Next steps:"):]
	for _, want := range []string{
		"kubectl get nodes",
		"kubectl config use-context tuggy-dev",
		"kubectl config use-context work",
		"# switch back to the cluster you used before",
		"kubectl tuggy describe cluster dev",
		"kubectl tuggy delete cluster dev",
		"(it expires in 8h0m)",
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("next steps missing %q:\n%s", want, steps)
		}
	}
	t.Logf("\n%s", out[strings.Index(out, "Cluster dev is ready"):])
}

func TestCreateNextStepsWithoutKubeconfig(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.run(t, "create", "cluster", "dev", "--no-kubeconfig")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	steps := out[strings.Index(out, "Next steps:"):]
	if !strings.Contains(steps, "kubectl tuggy get kubeconfig dev --merge") || strings.Contains(steps, "use-context") {
		t.Errorf("without a kubeconfig entry, the first step should add one:\n%s", steps)
	}
	if !strings.Contains(steps, "# delete it when you're done\n") {
		t.Errorf("no TTL: the delete step shouldn't mention expiry:\n%s", steps)
	}
	t.Logf("\n%s", steps)
}
