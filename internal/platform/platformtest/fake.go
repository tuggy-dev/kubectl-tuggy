// Package platformtest provides a fake Platform for testing code that uses
// platforms, such as the CLI, without a cloud account.
package platformtest

import (
	"context"
	"sync"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// Variables are the fake platform's typed variables.
type Variables struct {
	Name      string
	NodeCount int
}

// Fake is a Platform that records calls and returns configured results.
// Set the *Err fields to make an operation fail.
type Fake struct {
	PlatformName string

	FromV1Alpha1Err error
	Checks          []platform.CheckResult
	CreateErr       error
	DeleteErr       error
	Outputs         map[string]string

	mu    sync.Mutex
	calls []string
}

var _ platform.Platform = (*Fake)(nil)

// New returns a fake platform called name.
func New(name string) *Fake {
	return &Fake{PlatformName: name, Outputs: map[string]string{"endpoint": "https://127.0.0.1:6443"}}
}

// Calls returns the names of the methods called so far, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *Fake) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

// Name returns the fake's name.
func (f *Fake) Name() string { return f.PlatformName }

// FromV1Alpha1 builds a Plan with fake Variables.
func (f *Fake) FromV1Alpha1(c *v1alpha1.Cluster) (*platform.Plan, error) {
	f.record("FromV1Alpha1")
	if f.FromV1Alpha1Err != nil {
		return nil, f.FromV1Alpha1Err
	}
	nodes := 0
	for _, np := range c.Spec.NodePools {
		if np.Count != nil {
			nodes += int(*np.Count)
		}
	}
	var ttl = c.Spec.TTL
	p := &platform.Plan{
		Name:      c.Metadata.Name,
		Platform:  f.PlatformName,
		Variables: Variables{Name: c.Metadata.Name, NodeCount: nodes},
	}
	if ttl != nil {
		p.TTL = ttl.Duration
	}
	return p, nil
}

// Preflight returns the configured checks.
func (f *Fake) Preflight(context.Context, *platform.Plan) []platform.CheckResult {
	f.record("Preflight")
	return f.Checks
}

// Create returns the configured outputs or error.
func (f *Fake) Create(ctx context.Context, p *platform.Plan, _ platform.CreateOptions) (*platform.ClusterInfo, error) {
	f.record("Create")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	return &platform.ClusterInfo{Name: p.Name, Platform: f.PlatformName, Outputs: f.Outputs}, nil
}

// Delete returns the configured error.
func (f *Fake) Delete(ctx context.Context, _ *clustermeta.Record, _ platform.DeleteOptions) error {
	f.record("Delete")
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.DeleteErr
}

// Describe returns basic details from the record.
func (f *Fake) Describe(_ context.Context, rec *clustermeta.Record, _ string) (*platform.ClusterInfo, error) {
	f.record("Describe")
	return &platform.ClusterInfo{Name: rec.Name, Platform: f.PlatformName, Location: "fake-location", Outputs: rec.Outputs}, nil
}

// Kubeconfig returns a minimal kubeconfig pointing at the record's endpoint.
func (f *Fake) Kubeconfig(_ context.Context, rec *clustermeta.Record) (*clientcmdapi.Config, error) {
	f.record("Kubeconfig")
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[rec.Name] = &clientcmdapi.Cluster{Server: rec.Outputs["endpoint"]}
	cfg.AuthInfos[rec.Name] = &clientcmdapi.AuthInfo{}
	cfg.Contexts[rec.Name] = &clientcmdapi.Context{Cluster: rec.Name, AuthInfo: rec.Name}
	return cfg, nil
}

// ListRemote is not supported by the fake.
func (f *Fake) ListRemote(context.Context, platform.ListOptions) ([]platform.ClusterInfo, error) {
	f.record("ListRemote")
	return nil, platform.ErrNotSupported
}
