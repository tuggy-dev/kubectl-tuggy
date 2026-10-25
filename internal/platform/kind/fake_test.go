package kind

import (
	"errors"
	"os"
	"slices"
	"sync"
	"testing"

	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/nodes"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/runnertest"
)

// fakeNode is a kind node with only a name. Calling anything else panics,
// which is fine: the platform only reads names, and KubeVersion is faked.
type fakeNode struct {
	nodes.Node
	name string
}

func (n fakeNode) String() string { return n.name }

// fakeProvider stands in for the kind library.
type fakeProvider struct {
	mu        sync.Mutex
	clusters  map[string][]string // cluster -> node names
	createErr error
	listErr   error
	calls     []string
	logger    *progressLogger
}

var _ kindProvider = (*fakeProvider)(nil)

func newFakeProvider() *fakeProvider {
	return &fakeProvider{clusters: map[string][]string{}}
}

func (f *fakeProvider) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeProvider) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeProvider) Create(name string, _ ...cluster.CreateOption) error {
	f.record("Create " + name)
	if f.logger != nil {
		f.logger.V(0).Infof(" • %s  ...\n", "Preparing nodes 📦")
		f.logger.V(1).Info("debug detail")
	}
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	f.clusters[name] = []string{name + "-control-plane", name + "-worker"}
	f.mu.Unlock()
	return nil
}

func (f *fakeProvider) Delete(name, _ string) error {
	f.record("Delete " + name)
	f.mu.Lock()
	delete(f.clusters, name)
	f.mu.Unlock()
	return nil
}

func (f *fakeProvider) List() ([]string, error) {
	f.record("List")
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for n := range f.clusters {
		names = append(names, n)
	}
	return names, nil
}

func (f *fakeProvider) KubeConfig(name string, _ bool) (string, error) {
	f.record("KubeConfig " + name)
	f.mu.Lock()
	_, ok := f.clusters[name]
	f.mu.Unlock()
	if !ok {
		return "", errors.New("no such cluster")
	}
	return `apiVersion: v1
kind: Config
clusters:
- name: kind-` + name + `
  cluster:
    server: https://127.0.0.1:41234
    certificate-authority-data: Y2E=
users:
- name: kind-` + name + `
  user:
    client-certificate-data: Y2VydA==
    client-key-data: a2V5
contexts:
- name: kind-` + name + `
  context:
    cluster: kind-` + name + `
    user: kind-` + name + `
current-context: kind-` + name + `
`, nil
}

func (f *fakeProvider) ListNodes(name string) ([]nodes.Node, error) {
	f.record("ListNodes " + name)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []nodes.Node
	for _, n := range f.clusters[name] {
		out = append(out, fakeNode{name: n})
	}
	return out, nil
}

// harness is a kind platform wired to fakes.
type harness struct {
	p        *Platform
	provider *fakeProvider
	runner   *runnertest.Fake
	busy     map[int32]bool
	missing  map[string]bool
	noDocker bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{provider: newFakeProvider(), runner: runnertest.New(), busy: map[int32]bool{}, missing: map[string]bool{}}
	h.p = &Platform{
		NewProvider: func(l *progressLogger) kindProvider {
			h.provider.logger = l
			return h.provider
		},
		NewRunner: func() (runner.Runner, error) { return h.runner, nil },
		LookPath: func(name string) (string, error) {
			if h.noDocker {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		Stat: func(path string) (os.FileInfo, error) {
			if h.missing[path] {
				return nil, os.ErrNotExist
			}
			return nil, nil
		},
		PortFree:    func(_ string, port int32) bool { return !h.busy[port] },
		KubeVersion: func(nodes.Node) (string, error) { return "v1.37.0", nil },
	}
	return h
}

// testCluster returns a defaulted spec named dev on the kind platform.
func testCluster(config string) *v1alpha1.Cluster {
	c := v1alpha1.NewCluster("dev", Name)
	if config != "" {
		c.Spec.PlatformConfig = []byte(config)
	}
	v1alpha1.SetDefaults(c)
	return c
}
