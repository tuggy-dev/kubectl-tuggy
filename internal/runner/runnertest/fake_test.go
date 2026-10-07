package runnertest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
)

func TestFakeScript(t *testing.T) {
	boom := errors.New("engine unavailable")
	f := New(
		Result{Stdout: "planned\n", ExitCode: 0},
		Result{Stderr: "Error: quota\n", ExitCode: 1},
		Result{Err: boom, ExitCode: -1},
	)
	ctx := context.Background()

	var out, errOut bytes.Buffer
	spec := runner.Spec{Image: "tofu", Cmd: []string{"plan"}, Stdout: &out, Stderr: &errOut}

	if code, err := f.Run(ctx, spec); code != 0 || err != nil || out.String() != "planned\n" {
		t.Errorf("run 1 = %d, %v, %q", code, err, out.String())
	}
	if code, err := f.Run(ctx, spec); code != 1 || err != nil || errOut.String() != "Error: quota\n" {
		t.Errorf("run 2 = %d, %v, %q", code, err, errOut.String())
	}
	if _, err := f.Run(ctx, spec); !errors.Is(err, boom) {
		t.Errorf("run 3 err = %v, want %v", err, boom)
	}
	if code, err := f.Run(ctx, spec); code != 0 || err != nil {
		t.Errorf("run after script = %d, %v, want success", code, err)
	}
	if len(f.Specs()) != 4 || f.Specs()[0].Cmd[0] != "plan" {
		t.Errorf("Specs() = %+v", f.Specs())
	}
}

func TestFakeRejectsInvalidSpec(t *testing.T) {
	if _, err := New().Run(context.Background(), runner.Spec{}); err == nil {
		t.Error("Run with no image should fail like the real runner")
	}
}

func TestFakeBlockUntilCancelled(t *testing.T) {
	f := New(Result{Block: true, ExitCode: 130})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)

	code, err := f.Run(ctx, runner.Spec{Image: "tofu"})
	if code != 130 || !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %d, %v; want 130, context.Canceled", code, err)
	}
}
