package platform_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform/platformtest"
)

func TestRegistryGet(t *testing.T) {
	r := platform.NewRegistry()
	gke := platformtest.New("gke")
	r.Register(gke)
	r.Register(platformtest.New("metal"))

	got, err := r.Get("gke")
	if err != nil {
		t.Fatal(err)
	}
	if got != gke {
		t.Error("Get returned a different platform than was registered")
	}

	if names := r.Names(); strings.Join(names, ",") != "gke,metal" {
		t.Errorf("Names = %v, want sorted [gke metal]", names)
	}

	_, err = r.Get("eks")
	if err == nil || err.Error() != `unknown platform "eks" (available: gke, metal)` {
		t.Errorf("Get unknown = %v", err)
	}
}

func TestRegistryEmpty(t *testing.T) {
	_, err := platform.NewRegistry().Get("gke")
	if err == nil || !strings.Contains(err.Error(), "no platforms are available") {
		t.Errorf("Get on empty registry = %v", err)
	}
}

func TestRegisterPanics(t *testing.T) {
	tests := map[string]func(r *platform.Registry){
		"empty name": func(r *platform.Registry) { r.Register(platformtest.New("")) },
		"duplicate": func(r *platform.Registry) {
			r.Register(platformtest.New("gke"))
			r.Register(platformtest.New("gke"))
		},
	}
	for name, register := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Register should panic")
				}
			}()
			register(platform.NewRegistry())
		})
	}
}

func TestFailed(t *testing.T) {
	pass := platform.CheckResult{Name: "a", Status: platform.CheckPass}
	warn := platform.CheckResult{Name: "b", Status: platform.CheckWarn}
	fail := platform.CheckResult{Name: "c", Status: platform.CheckFail}

	if platform.Failed(nil) || platform.Failed([]platform.CheckResult{pass, warn}) {
		t.Error("passes and warnings should not block")
	}
	if !platform.Failed([]platform.CheckResult{pass, fail}) {
		t.Error("a failed check should block")
	}
}

// TestLifecycleThroughInterface drives a whole create and delete using only
// the Platform interface and the store, the way the CLI will.
func TestLifecycleThroughInterface(t *testing.T) {
	ctx := context.Background()
	reg := platform.NewRegistry()
	reg.Register(platformtest.New("fake"))
	s := clustermeta.NewLocal(t.TempDir())
	now := time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)

	spec := v1alpha1.NewCluster("dev", "fake")
	spec.Spec.TTL = &v1alpha1.Duration{Duration: 8 * time.Hour}
	v1alpha1.SetDefaults(spec)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}

	p, err := reg.Get(spec.Spec.Platform)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.FromV1Alpha1(spec)
	if err != nil {
		t.Fatal(err)
	}
	if vars, ok := plan.Variables.(platformtest.Variables); !ok || vars.NodeCount != 1 {
		t.Errorf("plan variables = %#v", plan.Variables)
	}
	if platform.Failed(p.Preflight(ctx, plan)) {
		t.Fatal("preflight failed")
	}

	unlock, err := s.Lock(plan.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()

	rec := clustermeta.NewRecord(plan.Name, p.Name(), "v0.1.0", plan.TTL, now)
	if err := rec.Transition(clustermeta.StatusCreating, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(rec); err != nil {
		t.Fatal(err)
	}

	info, err := p.Create(ctx, plan, platform.CreateOptions{Dir: s.Dir(plan.Name)})
	if err != nil {
		t.Fatal(err)
	}
	rec.Outputs = info.Outputs
	if err := rec.Transition(clustermeta.StatusReady, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(rec); err != nil {
		t.Fatal(err)
	}

	saved, err := s.Get("dev")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != clustermeta.StatusReady || saved.Outputs["endpoint"] == "" || saved.ExpiresAt == nil {
		t.Errorf("saved record = %+v", saved)
	}
	kc, err := p.Kubeconfig(ctx, saved)
	if err != nil || kc.Clusters["tuggy-dev"].Server != saved.Outputs["endpoint"] {
		t.Errorf("kubeconfig = %+v, %v", kc, err)
	}

	if err := saved.Transition(clustermeta.StatusDeleting, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(saved); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, saved, platform.DeleteOptions{Dir: s.Dir("dev")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("dev"); !errors.Is(err, clustermeta.ErrNotFound) {
		t.Errorf("record still present after delete: %v", err)
	}
}

func TestFakeFailures(t *testing.T) {
	ctx := context.Background()
	f := platformtest.New("fake")
	f.CreateErr = errors.New("quota exceeded")

	plan, err := f.FromV1Alpha1(v1alpha1.NewCluster("dev", "fake"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Create(ctx, plan, platform.CreateOptions{}); !errors.Is(err, f.CreateErr) {
		t.Errorf("Create = %v, want configured error", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	f.CreateErr = nil
	if _, err := f.Create(cancelled, plan, platform.CreateOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Create with cancelled context = %v, want context.Canceled", err)
	}

	if _, err := f.ListRemote(ctx, platform.ListOptions{}); !errors.Is(err, platform.ErrNotSupported) {
		t.Errorf("ListRemote = %v, want ErrNotSupported", err)
	}
	if got := strings.Join(f.Calls(), ","); got != "FromV1Alpha1,Create,Create,ListRemote" {
		t.Errorf("Calls = %s", got)
	}
}
