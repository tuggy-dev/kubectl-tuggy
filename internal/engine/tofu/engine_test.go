package tofu

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/runnertest"
)

// fixture returns recorded OpenTofu 1.13.1 output from testdata.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type harness struct {
	engine *Engine
	fake   *runnertest.Fake
	ws     Workspace
	log    *bytes.Buffer
	events *[]Event
}

func newHarness(t *testing.T, results ...runnertest.Result) *harness {
	t.Helper()
	fake := runnertest.New(results...)
	var events []Event
	log := &bytes.Buffer{}
	h := &harness{
		engine: &Engine{Runner: fake, Image: "tofu:test", PluginCacheDir: filepath.Join(t.TempDir(), "plugins")},
		fake:   fake,
		log:    log,
		events: &events,
		ws: Workspace{
			Name:    "dev",
			Dir:     t.TempDir(),
			Env:     map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/creds/adc.json"},
			Mounts:  []runner.Mount{{Type: runner.MountBind, Source: mustAbs(t, "creds.json"), Target: "/creds/adc.json", ReadOnly: true}},
			Log:     log,
			OnEvent: func(ev Event) { events = append(events, ev) },
		},
	}
	return h
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// savedPlan creates a plan file as if Plan had run.
func (h *harness) savedPlan(t *testing.T) (*PlanResult, string) {
	t.Helper()
	plan := &PlanResult{File: planFilePrefix + "test"}
	p := filepath.Join(h.ws.Dir, plan.File)
	if err := os.WriteFile(p, []byte("plan"), filePerm); err != nil {
		t.Fatal(err)
	}
	return plan, p
}

func planFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, planFilePrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func eventTypes(events []Event) []EventType {
	var types []EventType
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	return types
}

func TestContainerSpec(t *testing.T) {
	h := newHarness(t)
	if err := h.engine.Init(context.Background(), h.ws); err != nil {
		t.Fatal(err)
	}
	spec := h.fake.Specs()[0]

	if spec.Image != "tofu:test" || spec.WorkingDir != "/workspace" {
		t.Errorf("image %q, working dir %q", spec.Image, spec.WorkingDir)
	}
	if !slices.Equal(spec.Cmd, []string{"init", "-input=false", "-json"}) {
		t.Errorf("cmd = %v", spec.Cmd)
	}
	if spec.Labels[ClusterLabel] != "dev" {
		t.Errorf("labels = %v", spec.Labels)
	}
	if spec.User != runner.CallerUser() {
		t.Errorf("user = %q, want %q", spec.User, runner.CallerUser())
	}
	for k, want := range map[string]string{
		"HOME":                           "/tmp",
		"TF_IN_AUTOMATION":               "1",
		"TF_PLUGIN_CACHE_DIR":            "/plugin-cache",
		"GOOGLE_APPLICATION_CREDENTIALS": "/creds/adc.json",
	} {
		if spec.Env[k] != want {
			t.Errorf("env %s = %q, want %q", k, spec.Env[k], want)
		}
	}

	targets := map[string]runner.Mount{}
	for _, m := range spec.Mounts {
		targets[m.Target] = m
	}
	if m := targets["/workspace"]; m.Source != h.ws.Dir || m.ReadOnly {
		t.Errorf("workspace mount = %+v", m)
	}
	if m := targets["/plugin-cache"]; m.Source != h.engine.PluginCacheDir {
		t.Errorf("plugin cache mount = %+v", m)
	}
	if m := targets["/creds/adc.json"]; !m.ReadOnly {
		t.Errorf("credentials mount should be read-only: %+v", m)
	}
	if _, err := os.Stat(h.engine.PluginCacheDir); err != nil {
		t.Errorf("plugin cache directory not created: %v", err)
	}
	if !strings.HasPrefix(h.log.String(), "$ tofu init -input=false -json\n") {
		t.Errorf("log should start with the command, got %q", h.log.String())
	}
}

func TestDefaultImageUsedWhenUnset(t *testing.T) {
	h := newHarness(t)
	h.engine.Image = ""
	t.Setenv(ImageEnv, "")
	if err := h.engine.Init(context.Background(), h.ws); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Specs()[0].Image; got != DefaultImage {
		t.Errorf("image = %q, want DefaultImage", got)
	}
}

func TestPlanCreate(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stdout: fixture(t, "plan-create.jsonl"), ExitCode: 2})
	plan, err := h.engine.Plan(context.Background(), h.ws, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.HasChanges() || plan.Summary.Add != 1 || plan.Summary.Operation != "plan" {
		t.Errorf("summary = %+v", plan.Summary)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Resource != "random_pet.this" || plan.Changes[0].Action != "create" || plan.Changes[0].ResourceType != "random_pet" {
		t.Errorf("changes = %+v", plan.Changes)
	}
	if got := eventTypes(*h.events); !slices.Equal(got, []EventType{EventPlannedChange, EventSummary}) {
		t.Errorf("events = %v", got)
	}
	cmd := h.fake.Specs()[0].Cmd
	if !strings.HasPrefix(plan.File, planFilePrefix) || !slices.Contains(cmd, "-out="+plan.File) {
		t.Errorf("plan file %q, cmd %v", plan.File, cmd)
	}
	if !slices.Contains(cmd, "-detailed-exitcode") || slices.Contains(cmd, "-destroy") {
		t.Errorf("cmd = %v", cmd)
	}
	if !strings.Contains(h.log.String(), `"type":"change_summary"`) {
		t.Error("raw OpenTofu output should be in the log")
	}
}

func TestPlanNoChanges(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stdout: fixture(t, "plan-nochange.jsonl"), ExitCode: 0})
	plan, err := h.engine.Plan(context.Background(), h.ws, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasChanges() {
		t.Errorf("summary = %+v, want no changes", plan.Summary)
	}
}

func TestPlanDestroy(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stdout: fixture(t, "plan-destroy.jsonl"), ExitCode: 2})
	plan, err := h.engine.Plan(context.Background(), h.ws, PlanOptions{Destroy: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Remove != 1 || plan.Changes[0].Action != "delete" {
		t.Errorf("plan = %+v", plan)
	}
	if !slices.Contains(h.fake.Specs()[0].Cmd, "-destroy") {
		t.Error("destroy plan should pass -destroy")
	}
}

func TestPlanError(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stdout: fixture(t, "plan-error.jsonl"), ExitCode: 1})
	// Simulate OpenTofu having written part of a plan before failing.
	h.fake.BeforeRun = func(spec runner.Spec) {
		for _, a := range spec.Cmd {
			if f, ok := strings.CutPrefix(a, "-out="); ok {
				_ = os.WriteFile(filepath.Join(h.ws.Dir, f), []byte("partial"), filePerm)
			}
		}
	}

	_, err := h.engine.Plan(context.Background(), h.ws, PlanOptions{})
	var tofuErr *Error
	if !errors.As(err, &tofuErr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if tofuErr.ExitCode != 1 || len(tofuErr.Diagnostics) != 1 {
		t.Errorf("error = %+v", tofuErr)
	}
	if want := "tofu plan failed: Invalid value for variable: length must be at least 1."; err.Error() != want {
		t.Errorf("message = %q\n         want %q", err, want)
	}
	if files := planFiles(t, h.ws.Dir); len(files) != 0 {
		t.Errorf("plan files left after a failed plan: %v", files)
	}
}

func TestInitError(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stdout: fixture(t, "init-error.jsonl"), ExitCode: 1})
	err := h.engine.Init(context.Background(), h.ws)
	if err == nil || !strings.HasPrefix(err.Error(), "tofu init failed: Failed to query available provider packages: Could not retrieve the list of available versions for provider hashicorp/randomx-does-not-exist") {
		t.Errorf("err = %v", err)
	}
}

func TestApply(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stdout: fixture(t, "apply-create.jsonl"), ExitCode: 0})
	plan, planPath := h.savedPlan(t)

	summary, err := h.engine.Apply(context.Background(), h.ws, plan)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Add != 1 || summary.Operation != "apply" {
		t.Errorf("summary = %+v", summary)
	}
	if !slices.Equal(h.fake.Specs()[0].Cmd, []string{"apply", "-input=false", "-json", plan.File}) {
		t.Errorf("cmd = %v", h.fake.Specs()[0].Cmd)
	}
	var started, done bool
	for _, ev := range *h.events {
		started = started || (ev.Type == EventResourceStart && ev.Resource == "random_pet.this" && ev.Action == "create")
		done = done || (ev.Type == EventResourceDone && ev.Resource == "random_pet.this")
	}
	if !started || !done {
		t.Errorf("missing resource start/done events: %v", eventTypes(*h.events))
	}
	if _, err := os.Stat(planPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("plan file should be removed after apply")
	}
}

func TestApplyWithoutPlan(t *testing.T) {
	h := newHarness(t)
	if _, err := h.engine.Apply(context.Background(), h.ws, nil); err == nil || !strings.Contains(err.Error(), "no saved plan") {
		t.Errorf("err = %v", err)
	}
	gone := &PlanResult{File: planFilePrefix + "gone"}
	if _, err := h.engine.Apply(context.Background(), h.ws, gone); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Errorf("err = %v", err)
	}
	if len(h.fake.Specs()) != 0 {
		t.Error("OpenTofu should not run without a plan")
	}
}

func TestApplyFailureWithoutDiagnostics(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stderr: "something odd\npanic: provider crashed\n", ExitCode: 11})
	plan, planPath := h.savedPlan(t)

	_, err := h.engine.Apply(context.Background(), h.ws, plan)
	if err == nil || err.Error() != "tofu apply failed: panic: provider crashed" {
		t.Errorf("err = %v", err)
	}
	if _, statErr := os.Stat(planPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("plan file should be removed even when apply fails")
	}
	if !strings.Contains(h.log.String(), "panic: provider crashed") {
		t.Error("stderr should be in the log")
	}
}

func TestErrorWithoutOutput(t *testing.T) {
	if got := (&Error{Command: "init", ExitCode: 3}).Error(); got != "tofu init failed with exit code 3" {
		t.Errorf("got %q", got)
	}
}

func TestCancelled(t *testing.T) {
	h := newHarness(t, runnertest.Result{Block: true, ExitCode: 130})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, _ := h.savedPlan(t)
	_, err := h.engine.Apply(ctx, h.ws, plan)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRunnerFailure(t *testing.T) {
	boom := errors.New("cannot reach Docker")
	h := newHarness(t, runnertest.Result{Err: boom, ExitCode: -1})
	if err := h.engine.Init(context.Background(), h.ws); !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the runner error", err)
	}
}

func TestOutputs(t *testing.T) {
	h := newHarness(t, runnertest.Result{Stdout: fixture(t, "output.json")})
	outputs, err := h.engine.Outputs(context.Background(), h.ws)
	if err != nil {
		t.Fatal(err)
	}
	strs := StringOutputs(outputs)
	if strs["name"] != "dev" || !strings.HasPrefix(strs["pet"], "dev-") {
		t.Errorf("outputs = %v", strs)
	}
	if len(*h.events) != 0 {
		t.Error("output should not emit progress events")
	}
}

func TestStringOutputs(t *testing.T) {
	got := StringOutputs(map[string]Output{
		"endpoint": {Value: []byte(`"https://1.2.3.4"`)},
		"secret":   {Value: []byte(`"hunter2"`), Sensitive: true},
		"count":    {Value: []byte(`3`)},
	})
	if len(got) != 1 || got["endpoint"] != "https://1.2.3.4" {
		t.Errorf("got %v; want only the non-sensitive string output", got)
	}
}

func TestRelativeWorkspaceRejected(t *testing.T) {
	e := &Engine{Runner: runnertest.New()}
	if err := e.Init(context.Background(), Workspace{Name: "dev", Dir: "relative"}); err == nil {
		t.Error("a relative workspace directory should be rejected")
	}
}

func TestWriteModule(t *testing.T) {
	module := fstest.MapFS{
		"main.tf":             {Data: []byte("# v1 main")},
		"variables.tf":        {Data: []byte("# v1 vars")},
		".terraform.lock.hcl": {Data: []byte("# lock")},
		"README.md":           {Data: []byte("docs")},
		"sub/extra.tf":        {Data: []byte("# sub")},
	}
	dir := filepath.Join(t.TempDir(), "dev")
	if err := WriteModule(dir, module); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"main.tf", "variables.tf", ".terraform.lock.hcl", filepath.Join("sub", "extra.tf")} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !errors.Is(err, os.ErrNotExist) {
		t.Error("non-module files should be skipped")
	}

	// A newer module must not replace the one an existing cluster was built with.
	module["main.tf"] = &fstest.MapFile{Data: []byte("# v2 main")}
	if err := WriteModule(dir, module); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "main.tf")); string(got) != "# v1 main" {
		t.Errorf("main.tf = %q, want the original module kept", got)
	}
}

func TestWriteVariables(t *testing.T) {
	dir := t.TempDir()
	type vars struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	if err := WriteVariables(dir, vars{Name: "dev", Count: 1}); err != nil {
		t.Fatal(err)
	}
	if err := WriteVariables(dir, vars{Name: "dev", Count: 3}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, VariablesFile)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\n  \"name\": \"dev\",\n  \"count\": 3\n}\n" {
		t.Errorf("variables file = %q", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != filePerm {
			t.Errorf("mode = %o, want %o", info.Mode().Perm(), filePerm)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*tmp*")); len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestEventWriterSplitsLines(t *testing.T) {
	var log bytes.Buffer
	var events []Event
	w := newEventWriter(&log, func(ev Event) { events = append(events, ev) })

	line := `{"type":"change_summary","@message":"Plan: 1 to add","changes":{"add":1,"operation":"plan"}}`
	_, _ = w.Write([]byte("not json\n" + line[:20]))
	_, _ = w.Write([]byte(line[20:] + "\n" + `{"type":"diagnostic","diagnostic":{"severity":"warning","summary":"careful"}}`))
	_ = w.Close()

	if got := eventTypes(events); !slices.Equal(got, []EventType{EventSummary, EventDiagnostic}) {
		t.Errorf("events = %v", got)
	}
	summary, diags := w.result()
	if summary == nil || summary.Add != 1 {
		t.Errorf("summary = %+v", summary)
	}
	if len(diags) != 0 {
		t.Errorf("warnings should not be recorded as errors: %+v", diags)
	}
	if strings.Count(log.String(), "\n") != 3 || !strings.HasPrefix(log.String(), "not json\n") {
		t.Errorf("log = %q, want all three lines", log.String())
	}
}

func TestVariablesPassedAsEnvironment(t *testing.T) {
	h := newHarness(t)
	err := WriteVariables(h.ws.Dir, map[string]any{
		"name":    "dev",
		"length":  3,
		"spot":    true,
		"version": nil,
		"pools":   []map[string]any{{"name": "default", "count": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Init(context.Background(), h.ws); err != nil {
		t.Fatal(err)
	}

	env := h.fake.Specs()[0].Env
	for k, want := range map[string]string{
		"TF_VAR_name":   "dev",
		"TF_VAR_length": "3",
		"TF_VAR_spot":   "true",
		"TF_VAR_pools":  `[{"count":2,"name":"default"}]`,
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if _, ok := env["TF_VAR_version"]; ok {
		t.Error("null variables should be left out so the module default applies")
	}
}

func TestVariablesFileNotAutoLoaded(t *testing.T) {
	// OpenTofu loads terraform.tfvars.json and *.auto.tfvars.json by itself;
	// tuggy's file must not match, or a stale copy could be read.
	if VariablesFile == "terraform.tfvars.json" || strings.HasSuffix(VariablesFile, ".auto.tfvars.json") {
		t.Errorf("VariablesFile %q would be loaded automatically by OpenTofu", VariablesFile)
	}
}

func TestInvalidVariablesFile(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.ws.Dir, VariablesFile), []byte("[1,2]"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Init(context.Background(), h.ws); err == nil || !strings.Contains(err.Error(), VariablesFile) {
		t.Errorf("err = %v, want an error naming the variables file", err)
	}
}
