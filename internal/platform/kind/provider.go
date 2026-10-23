package kind

import (
	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/nodes"
)

// kindProvider is the part of the kind library tuggy uses. *cluster.Provider
// implements it; tests use a fake, so the lifecycle code runs without Docker.
type kindProvider interface {
	Create(name string, opts ...cluster.CreateOption) error
	Delete(name, kubeconfigPath string) error
	List() ([]string, error)
	KubeConfig(name string, internal bool) (string, error)
	ListNodes(name string) ([]nodes.Node, error)
}

var _ kindProvider = (*cluster.Provider)(nil)

// newLibraryProvider returns the kind library driving Docker, logging to
// logger. Podman and nerdctl are not supported yet.
func newLibraryProvider(logger *progressLogger) kindProvider {
	return cluster.NewProvider(cluster.ProviderWithLogger(logger), cluster.ProviderWithDocker())
}
