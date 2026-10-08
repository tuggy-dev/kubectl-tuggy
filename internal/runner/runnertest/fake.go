// Package runnertest provides a fake Runner for testing code that runs
// containers, without a container engine.
package runnertest

import (
	"context"
	"io"
	"sync"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
)

// Result is what one fake run produces.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error

	// Block makes the run wait until its context is cancelled, to test
	// cancellation. The run then returns ExitCode and the context's error.
	Block bool
}

// Fake is a Runner that returns scripted results in order and records the
// specs it was given. When the script runs out, runs succeed with no output.
type Fake struct {
	PingErr  error
	ImageErr error

	// BeforeRun, if set, is called with each spec before its result is
	// returned, for example to create files the real command would write.
	BeforeRun func(runner.Spec)

	mu      sync.Mutex
	results []Result
	specs   []runner.Spec
	images  []string
}

var _ runner.Runner = (*Fake)(nil)

// New returns a fake that answers runs with results, in order.
func New(results ...Result) *Fake {
	return &Fake{results: results}
}

// Specs returns the specs of every run so far.
func (f *Fake) Specs() []runner.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.Spec(nil), f.specs...)
}

// Images returns the images passed to EnsureImage so far.
func (f *Fake) Images() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.images...)
}

// Ping returns PingErr.
func (f *Fake) Ping(context.Context) error { return f.PingErr }

// EnsureImage records image and returns ImageErr.
func (f *Fake) EnsureImage(_ context.Context, image string, _ io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, image)
	return f.ImageErr
}

// Run validates spec, writes the next scripted output and returns its result.
func (f *Fake) Run(ctx context.Context, spec runner.Spec) (int, error) {
	if err := spec.Validate(); err != nil {
		return -1, err
	}

	f.mu.Lock()
	f.specs = append(f.specs, spec)
	var r Result
	if len(f.results) > 0 {
		r = f.results[0]
		f.results = f.results[1:]
	}
	f.mu.Unlock()

	if f.BeforeRun != nil {
		f.BeforeRun(spec)
	}
	write(spec.Stdout, r.Stdout)
	write(spec.Stderr, r.Stderr)

	if r.Block {
		<-ctx.Done()
		return r.ExitCode, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	return r.ExitCode, r.Err
}

func write(w io.Writer, s string) {
	if w != nil && s != "" {
		_, _ = io.WriteString(w, s)
	}
}
