package kind

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

func TestStatusStep(t *testing.T) {
	tests := []struct {
		line, want string
		ok         bool
	}{
		{" • Ensuring node image (kindest/node:v1.37.0) 🖼️  ...\n", "Ensuring node image (kindest/node:v1.37.0)", true},
		{" • Preparing nodes 📦 📦  ...\n", "Preparing nodes", true},
		{" • Starting control-plane 🕹️  ...\n", "Starting control-plane", true},
		{" ✓ Preparing nodes 📦 📦\n", "", false},
		{"Deleted nodes: [\"tuggy-dev-control-plane\"]", "", false},
	}
	for _, tt := range tests {
		got, ok := statusStep(tt.line)
		if got != tt.want || ok != tt.ok {
			t.Errorf("statusStep(%q) = %q, %v; want %q, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestProgressLogger(t *testing.T) {
	var log bytes.Buffer
	var steps []string
	l := newProgressLogger(&log, func(p platform.Progress) {
		if p.Kind == platform.ProgressInfo {
			steps = append(steps, p.Message)
		}
	})
	l.V(0).Infof(" • %s  ...\n", "Joining worker nodes 🚜")
	l.V(2).Info(" • not a user-facing step  ...")
	l.Warnf("low memory: %d GB", 1)
	l.Error("boom")

	if len(steps) != 1 || steps[0] != "Joining worker nodes" {
		t.Errorf("steps = %v", steps)
	}
	for _, want := range []string{"Joining worker nodes", "not a user-facing step", "WARNING: low memory: 1 GB", "ERROR: boom"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log missing %q:\n%s", want, log.String())
		}
	}
}
