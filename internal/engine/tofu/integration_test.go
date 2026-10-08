package tofu

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/docker"
)

// TestLifecycleWithRealOpenTofu runs the whole flow with the pinned OpenTofu
// image and the testdata module, which needs no cloud account. It is skipped
// when no container engine is reachable.
func TestLifecycleWithRealOpenTofu(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping container test in -short mode")
	}
	d, err := docker.New()
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skip(err)
	}
	if err := d.EnsureImage(ctx, DefaultImage, nil); err != nil {
		t.Skipf("cannot pull %s: %v", DefaultImage, err)
	}

	// Engines in a VM share only the home directory with containers.
	base := mountableBase(t)
	engine := &Engine{Runner: d, Image: DefaultImage, PluginCacheDir: base + "/plugins"}
	var log bytes.Buffer
	ws := Workspace{Name: "lifecycle", Dir: base + "/cluster", Log: &log}

	step := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v\n--- log ---\n%s", name, err, log.String())
		}
	}

	step("write module", WriteModule(ws.Dir, os.DirFS("testdata/module")))
	step("write variables", WriteVariables(ws.Dir, map[string]any{
		"name":  "dev",
		"pools": []map[string]any{{"name": "default", "count": 1}, {"name": "gpu", "count": 2}},
	}))
	step("init", engine.Init(ctx, ws))

	plan, err := engine.Plan(ctx, ws, PlanOptions{})
	step("plan", err)
	if plan.Summary.Add != 1 {
		t.Errorf("create plan = %+v", plan.Summary)
	}
	_, err = engine.Apply(ctx, ws, plan)
	step("apply", err)

	outputs, err := engine.Outputs(ctx, ws)
	step("outputs", err)
	strs := StringOutputs(outputs)
	if !strings.HasPrefix(strs["pet"], "dev-") {
		t.Errorf("pet = %q, want prefix dev-", strs["pet"])
	}
	if strs["pool_names"] != "default=1,gpu=2" {
		t.Errorf("pool_names = %q: a list of objects did not reach OpenTofu intact", strs["pool_names"])
	}

	// Changing the variables file must be seen by the next run, including
	// through a VM's file sharing (Colima, Docker Desktop).
	step("rewrite variables", WriteVariables(ws.Dir, map[string]any{"name": "prod"}))
	plan, err = engine.Plan(ctx, ws, PlanOptions{})
	step("plan change", err)
	if !plan.HasChanges() || len(plan.Changes) == 0 || plan.Changes[0].Action != "replace" {
		t.Errorf("after changing the name, plan = %+v, want the pet replaced", plan)
	}
	step("discard plan", engine.DiscardPlan(ws, plan))
	step("restore variables", WriteVariables(ws.Dir, map[string]any{"name": "dev"}))

	plan, err = engine.Plan(ctx, ws, PlanOptions{Destroy: true})
	step("destroy plan", err)
	if plan.Summary.Remove != 1 {
		t.Errorf("destroy plan = %+v", plan.Summary)
	}
	_, err = engine.Apply(ctx, ws, plan)
	step("destroy", err)

	outputs, err = engine.Outputs(ctx, ws)
	step("outputs after destroy", err)
	if len(outputs) != 0 {
		t.Errorf("outputs after destroy = %v, want none", outputs)
	}

	// A validation error comes back as a readable message.
	step("bad variables", WriteVariables(ws.Dir, map[string]any{"name": "dev", "length": 0}))
	_, err = engine.Plan(ctx, ws, PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "length must be at least 1.") {
		t.Errorf("plan with invalid variables: %v", err)
	}

	if leftovers, _ := filepath.Glob(filepath.Join(ws.Dir, planFilePrefix+"*")); len(leftovers) != 0 {
		t.Errorf("plan files left behind: %v", leftovers)
	}
}

func mountableBase(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "tuggy-engine-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
