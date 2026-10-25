package kind

import (
	"context"
	"errors"
	"testing"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

func statusOf(results []platform.CheckResult, name string) platform.CheckStatus {
	for _, r := range results {
		if r.Name == name {
			return r.Status
		}
	}
	return ""
}

func TestPreflightPasses(t *testing.T) {
	h := newHarness(t)
	results := h.p.Preflight(context.Background(), plan(t, h.p, ""))
	if platform.Failed(results) {
		t.Errorf("results = %+v", results)
	}
	for _, name := range []string{"Container engine", "docker CLI", "Cluster name tuggy-dev"} {
		if statusOf(results, name) != platform.CheckPass {
			t.Errorf("%s: %+v", name, results)
		}
	}
}

func TestPreflightFailures(t *testing.T) {
	tests := []struct {
		name   string
		config string
		setup  func(*harness)
		check  string
	}{
		{"engine down", "", func(h *harness) { h.runner.PingErr = errors.New("cannot connect") }, "Container engine"},
		{"no docker CLI", "", func(h *harness) { h.noDocker = true }, "docker CLI"},
		{"name taken", "", func(h *harness) { h.provider.clusters["tuggy-dev"] = nil }, "Cluster name tuggy-dev"},
		{"api port busy", `{"apiServerPort":6443}`, func(h *harness) { h.busy[6443] = true }, "API server port 6443"},
		{"host port busy", `{"extraPortMappings":[{"containerPort":30080,"hostPort":8080}]}`, func(h *harness) { h.busy[8080] = true }, "Host port 8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.setup(h)
			results := h.p.Preflight(context.Background(), plan(t, h.p, tt.config))
			if statusOf(results, tt.check) != platform.CheckFail {
				t.Errorf("%s should fail: %+v", tt.check, results)
			}
			for _, r := range results {
				if r.Status == platform.CheckFail && r.Fix == "" {
					t.Errorf("%s fails without a fix", r.Name)
				}
			}
		})
	}
}

func TestPreflightMissingMount(t *testing.T) {
	h := newHarness(t)
	pl := plan(t, h.p, `{"extraMounts":[{"hostPath":"/no/such/dir","containerPath":"/mnt/data"}]}`)
	h.missing[pl.Variables.(Variables).ExtraMounts[0].HostPath] = true
	results := h.p.Preflight(context.Background(), pl)
	if !platform.Failed(results) {
		t.Errorf("a missing mount path should fail: %+v", results)
	}
}

func TestPreflightListWarning(t *testing.T) {
	h := newHarness(t)
	h.provider.listErr = errors.New("docker hiccup")
	results := h.p.Preflight(context.Background(), plan(t, h.p, ""))
	if statusOf(results, "Cluster name tuggy-dev") != platform.CheckWarn {
		t.Errorf("a failed listing should only warn: %+v", results)
	}
}
