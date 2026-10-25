package gke

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

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/engine/tofu"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/runnertest"
)

// These fixtures have the shape of OpenTofu's -json output for the GKE module.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// planFileWriter makes the fake runner create the plan file a real
// "tofu plan -out=..." would write.
func planFileWriter(dir string) func(runner.Spec) {
	return func(spec runner.Spec) {
		for _, a := range spec.Cmd {
			if f, ok := strings.CutPrefix(a, "-out="); ok {
				_ = os.WriteFile(filepath.Join(dir, f), []byte("plan"), 0o600)
			}
		}
	}
}

type lifecycleHarness struct {
	p    *Platform
	fake *runnertest.Fake
	dir  string
	log  *bytes.Buffer
	gke  *fakeGKE
}

func newLifecycleHarness(t *testing.T, results ...runnertest.Result) *lifecycleHarness {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dev")
	fake := runnertest.New(results...)
	fake.BeforeRun = planFileWriter(dir)
	p := testPlatform()
	p.Image = "tofu:test"
	p.NewRunner = func() (runner.Runner, error) { return fake, nil }
	creds := writeCredentials(t, userCredentials)
	p.CredentialsPath = func() string { return creds }
	g := &fakeGKE{pools: []string{"default"}}
	p.api = newFakeGKEAPI(t, g)
	return &lifecycleHarness{p: p, fake: fake, dir: dir, log: &bytes.Buffer{}, gke: g}
}

func (h *lifecycleHarness) plan(t *testing.T) *platform.Plan {
	t.Helper()
	plan, err := h.p.FromV1Alpha1(loadSpec(t, minimalSpec))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func commands(f *runnertest.Fake) []string {
	var cmds []string
	for _, s := range f.Specs() {
		cmds = append(cmds, s.Cmd[0])
	}
	return cmds
}

func TestCreate(t *testing.T) {
	h := newLifecycleHarness(t,
		runnertest.Result{}, // init
		runnertest.Result{Stdout: fixture(t, "plan-create.jsonl"), ExitCode: 2}, // plan
		runnertest.Result{Stdout: fixture(t, "apply-create.jsonl")},             // apply
		runnertest.Result{Stdout: fixture(t, "output.json")},                    // output
	)
	var progress []platform.Progress
	result, err := h.p.Create(context.Background(), h.plan(t), platform.CreateOptions{
		Dir: h.dir, CacheDir: filepath.Join(t.TempDir(), "cache"), Log: h.log,
		OnProgress: func(p platform.Progress) { progress = append(progress, p) },
	})
	if err != nil {
		t.Fatalf("Create: %v\n%s", err, h.log)
	}

	if got := commands(h.fake); !slices.Equal(got, []string{"init", "plan", "apply", "output"}) {
		t.Errorf("tofu commands = %v", got)
	}
	if h.fake.Images()[0] != "tofu:test" {
		t.Errorf("image = %v", h.fake.Images())
	}
	if result.Outputs[OutputEndpoint] != "https://34.1.2.3" || result.KubernetesVersion != "1.34.1-gke.1200" || result.Location != "us-central1-a" {
		t.Errorf("result = %+v", result)
	}
	if len(result.NodePools) != 1 || result.NodePools[0].MachineType != DefaultMachineType {
		t.Errorf("node pools = %+v", result.NodePools)
	}

	// Workspace: the module, and the variables the platform translated.
	for _, f := range []string{"main.tf", "variables.tf", "outputs.tf", "versions.tf", ".terraform.lock.hcl", tofu.VariablesFile} {
		if _, err := os.Stat(filepath.Join(h.dir, f)); err != nil {
			t.Errorf("%s not in workspace: %v", f, err)
		}
	}
	var vars Variables
	data, _ := os.ReadFile(filepath.Join(h.dir, tofu.VariablesFile))
	if err := json.Unmarshal(data, &vars); err != nil || vars.Project != "acme-dev" {
		t.Errorf("variables file = %s (%v)", data, err)
	}

	// Credentials reach OpenTofu as one read-only file.
	spec := h.fake.Specs()[0]
	if spec.Env["GOOGLE_APPLICATION_CREDENTIALS"] != containerCredentialsPath || spec.Env["TF_VAR_project"] != "acme-dev" {
		t.Errorf("env = %v", spec.Env)
	}
	var credMount *runner.Mount
	for i, m := range spec.Mounts {
		if m.Target == containerCredentialsPath {
			credMount = &spec.Mounts[i]
		}
	}
	if credMount == nil || !credMount.ReadOnly {
		t.Errorf("credentials mount = %+v", credMount)
	}

	// Progress: steps, then each resource starting and finishing.
	var done []string
	for _, p := range progress {
		if p.Kind == platform.ProgressDone {
			done = append(done, p.Resource)
		}
	}
	if !slices.Equal(done, []string{"google_container_cluster.this", `google_container_node_pool.this["default"]`}) {
		t.Errorf("finished resources = %v", done)
	}
	if progress[0].Kind != platform.ProgressInfo || progress[0].Message != "Preparing OpenTofu" {
		t.Errorf("first progress = %+v", progress[0])
	}
}

func TestCreateDryRun(t *testing.T) {
	h := newLifecycleHarness(t,
		runnertest.Result{},
		runnertest.Result{Stdout: fixture(t, "plan-create.jsonl"), ExitCode: 2},
	)
	result, err := h.p.Create(context.Background(), h.plan(t), platform.CreateOptions{Dir: h.dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := commands(h.fake); !slices.Equal(got, []string{"init", "plan"}) {
		t.Errorf("tofu commands = %v; a dry run must not apply", got)
	}
	if result.Changes == nil || result.Changes.Add != 2 || len(result.Changes.Resources) != 2 || result.Changes.Resources[0].Type != "google_container_cluster" {
		t.Errorf("changes = %+v", result.Changes)
	}
	if plans, _ := filepath.Glob(filepath.Join(h.dir, "tfplan-*")); len(plans) != 0 {
		t.Errorf("dry run left a saved plan: %v", plans)
	}
}

func TestCreateWithoutCredentials(t *testing.T) {
	h := newLifecycleHarness(t)
	h.p.CredentialsPath = func() string { return filepath.Join(t.TempDir(), "none.json") }
	_, err := h.p.Create(context.Background(), h.plan(t), platform.CreateOptions{Dir: h.dir})
	if err == nil || !strings.Contains(err.Error(), "gcloud auth application-default login") {
		t.Errorf("err = %v", err)
	}
	if len(h.fake.Specs()) != 0 {
		t.Error("OpenTofu must not run without credentials")
	}
}

func TestCreateApplyFailure(t *testing.T) {
	h := newLifecycleHarness(t,
		runnertest.Result{},
		runnertest.Result{Stdout: fixture(t, "plan-create.jsonl"), ExitCode: 2},
		runnertest.Result{Stdout: `{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Error creating Cluster: googleapi: Error 403: Insufficient regional quota"}}` + "\n", ExitCode: 1},
	)
	_, err := h.p.Create(context.Background(), h.plan(t), platform.CreateOptions{Dir: h.dir})
	if err == nil || !strings.Contains(err.Error(), "Insufficient regional quota") {
		t.Errorf("err = %v", err)
	}
}

func readyRecord() *clustermeta.Record {
	return &clustermeta.Record{
		Name: "dev", Platform: Name, Status: clustermeta.StatusReady,
		CreatedAt: fixedNow,
		Outputs: map[string]string{
			OutputEndpoint: "https://34.1.2.3", OutputCACertificate: "Q0EgY2VydGlmaWNhdGU=", OutputKubernetesVersion: "1.34.1-gke.1200",
		},
	}
}

func TestDelete(t *testing.T) {
	h := newLifecycleHarness(t,
		runnertest.Result{},
		runnertest.Result{Stdout: fixture(t, "plan-destroy.jsonl"), ExitCode: 2},
		runnertest.Result{},
	)
	if err := tofu.WriteVariables(h.dir, map[string]string{"name": "dev"}); err != nil {
		t.Fatal(err)
	}
	var confirmed platform.Changes
	err := h.p.Delete(context.Background(), readyRecord(), platform.DeleteOptions{
		Dir:     h.dir,
		Confirm: func(c platform.Changes) error { confirmed = c; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Remove != 2 {
		t.Errorf("confirmation showed %+v, want 2 resources to remove", confirmed)
	}
	if got := commands(h.fake); !slices.Equal(got, []string{"init", "plan", "apply"}) {
		t.Errorf("tofu commands = %v", got)
	}
	if !slices.Contains(h.fake.Specs()[1].Cmd, "-destroy") {
		t.Error("delete must plan with -destroy")
	}
}

func TestDeleteDeclined(t *testing.T) {
	h := newLifecycleHarness(t,
		runnertest.Result{},
		runnertest.Result{Stdout: fixture(t, "plan-destroy.jsonl"), ExitCode: 2},
	)
	if err := tofu.WriteVariables(h.dir, map[string]string{"name": "dev"}); err != nil {
		t.Fatal(err)
	}
	declined := errors.New("cancelled by user")
	err := h.p.Delete(context.Background(), readyRecord(), platform.DeleteOptions{
		Dir:     h.dir,
		Confirm: func(platform.Changes) error { return declined },
	})
	if !errors.Is(err, declined) {
		t.Errorf("err = %v, want the confirmation's error", err)
	}
	if got := commands(h.fake); slices.Contains(got, "apply") {
		t.Errorf("tofu commands = %v; nothing may be removed after the user declines", got)
	}
	if plans, _ := filepath.Glob(filepath.Join(h.dir, "tfplan-*")); len(plans) != 0 {
		t.Errorf("declined delete left a saved plan: %v", plans)
	}
}

func TestDeleteWithoutWorkspace(t *testing.T) {
	h := newLifecycleHarness(t)
	err := h.p.Delete(context.Background(), readyRecord(), platform.DeleteOptions{Dir: h.dir})
	if err == nil || !strings.Contains(err.Error(), "no OpenTofu workspace") {
		t.Errorf("err = %v", err)
	}
}

func TestDescribe(t *testing.T) {
	h := newLifecycleHarness(t)
	plan := h.plan(t)
	if err := tofu.WriteVariables(h.dir, plan.Variables); err != nil {
		t.Fatal(err)
	}
	info, err := h.p.Describe(context.Background(), readyRecord(), h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Location != "us-central1-a" || info.KubernetesVersion != "1.34.1-gke.1200" || len(info.NodePools) != 1 || info.NodePools[0].Count != 1 {
		t.Errorf("info = %+v", info)
	}
}

func TestKubeconfig(t *testing.T) {
	cfg, err := testPlatform().Kubeconfig(context.Background(), readyRecord())
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["tuggy-dev"]
	if cluster == nil || cluster.Server != "https://34.1.2.3" || string(cluster.CertificateAuthorityData) != "CA certificate" {
		t.Errorf("cluster = %+v", cluster)
	}
	user := cfg.AuthInfos["tuggy-dev"]
	if user == nil || user.Exec == nil || user.Exec.Command != "gke-gcloud-auth-plugin" || !user.Exec.ProvideClusterInfo {
		t.Errorf("user = %+v", user)
	}
	// kubectl must use the same sign-in as OpenTofu (Application Default
	// Credentials), not the separate gcloud CLI login.
	if user != nil && user.Exec != nil && !slices.Equal(user.Exec.Args, []string{"--use_application_default_credentials"}) {
		t.Errorf("exec args = %v", user.Exec.Args)
	}
	if ctx := cfg.Contexts["tuggy-dev"]; ctx == nil || ctx.Cluster != "tuggy-dev" || ctx.AuthInfo != "tuggy-dev" || cfg.CurrentContext != "tuggy-dev" {
		t.Errorf("context = %+v, current %q", ctx, cfg.CurrentContext)
	}

	rec := readyRecord()
	delete(rec.Outputs, OutputEndpoint)
	if _, err := testPlatform().Kubeconfig(context.Background(), rec); err == nil {
		t.Error("a record without an endpoint should be an error")
	}
}

func TestRegistered(t *testing.T) {
	p, err := platform.Get("gke")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "gke" {
		t.Errorf("registered platform is %q", p.Name())
	}
	if _, ok := p.(platform.FlagBinder); !ok {
		t.Error("the gke platform should offer flags")
	}
}
