// Package kubeconfig adds and removes tuggy's entries in the user's
// kubeconfig, and checks that a new cluster answers.
//
// Changes go through client-go's ModifyConfig, the function kubectl's own
// "config" commands use, so tuggy honours KUBECONFIG (including lists of
// files), writes each entry to the right file, locks while writing, and keeps
// files owner-only, exactly as kubectl does.
package kubeconfig

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Prefix starts the name of every cluster, user and context tuggy writes, so
// tuggy only ever changes its own entries.
const Prefix = "tuggy-"

// Manager edits a kubeconfig.
type Manager struct {
	// Access locates and writes the kubeconfig files. The default follows
	// KUBECONFIG and ~/.kube/config like kubectl.
	Access clientcmd.ConfigAccess
}

// New returns a Manager for the user's kubeconfig.
func New() *Manager {
	return &Manager{Access: clientcmd.NewDefaultPathOptions()}
}

// Path is the file new entries are written to.
func (m *Manager) Path() string {
	return m.Access.GetDefaultFilename()
}

// CurrentContext returns the kubeconfig's current context, or "" if none is
// set or the kubeconfig can't be read.
func (m *Manager) CurrentContext() string {
	cfg, err := m.Access.GetStartingConfig()
	if err != nil {
		return ""
	}
	return cfg.CurrentContext
}

// Merge adds or replaces the clusters, users and contexts in entries. Every
// name must start with Prefix, so entries the user made are never touched.
// With setCurrent, entries.CurrentContext becomes the current context.
func (m *Manager) Merge(entries *clientcmdapi.Config, setCurrent bool) error {
	if err := checkNames(entries); err != nil {
		return err
	}
	cfg, err := m.Access.GetStartingConfig()
	if err != nil {
		return fmt.Errorf("reading kubeconfig: %w", err)
	}

	maps.Copy(cfg.Clusters, entries.Clusters)
	maps.Copy(cfg.AuthInfos, entries.AuthInfos)
	maps.Copy(cfg.Contexts, entries.Contexts)
	if setCurrent {
		if _, ok := entries.Contexts[entries.CurrentContext]; !ok {
			return fmt.Errorf("context %q is not among the entries to add", entries.CurrentContext)
		}
		cfg.CurrentContext = entries.CurrentContext
	}

	if err := clientcmd.ModifyConfig(m.Access, *cfg, false); err != nil {
		return fmt.Errorf("writing kubeconfig %s: %w", m.Path(), err)
	}
	return nil
}

// Remove deletes a tuggy context and the cluster and user it refers to,
// unless another context still uses them. If it was the current context,
// no context is current afterwards. Removing a context that doesn't exist is
// not an error.
func (m *Manager) Remove(contextName string) error {
	if !strings.HasPrefix(contextName, Prefix) {
		return fmt.Errorf("refusing to remove %q: tuggy only removes entries named %s*", contextName, Prefix)
	}
	cfg, err := m.Access.GetStartingConfig()
	if err != nil {
		return fmt.Errorf("reading kubeconfig: %w", err)
	}
	ctx, ok := cfg.Contexts[contextName]
	if !ok {
		return nil
	}
	delete(cfg.Contexts, contextName)

	inUse := func(get func(*clientcmdapi.Context) string, name string) bool {
		for _, c := range cfg.Contexts {
			if get(c) == name {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(ctx.Cluster, Prefix) && !inUse(func(c *clientcmdapi.Context) string { return c.Cluster }, ctx.Cluster) {
		delete(cfg.Clusters, ctx.Cluster)
	}
	if strings.HasPrefix(ctx.AuthInfo, Prefix) && !inUse(func(c *clientcmdapi.Context) string { return c.AuthInfo }, ctx.AuthInfo) {
		delete(cfg.AuthInfos, ctx.AuthInfo)
	}
	if cfg.CurrentContext == contextName {
		cfg.CurrentContext = ""
	}

	if err := clientcmd.ModifyConfig(m.Access, *cfg, false); err != nil {
		return fmt.Errorf("writing kubeconfig %s: %w", m.Path(), err)
	}
	return nil
}

// Serialize returns entries as kubeconfig YAML, for "get kubeconfig".
func Serialize(entries *clientcmdapi.Config) ([]byte, error) {
	return clientcmd.Write(*entries)
}

func checkNames(entries *clientcmdapi.Config) error {
	var bad []string
	for _, names := range [][]string{
		slices.Collect(maps.Keys(entries.Clusters)),
		slices.Collect(maps.Keys(entries.AuthInfos)),
		slices.Collect(maps.Keys(entries.Contexts)),
	} {
		for _, n := range names {
			if !strings.HasPrefix(n, Prefix) {
				bad = append(bad, n)
			}
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return fmt.Errorf("kubeconfig entries must be named %s*, got %s", Prefix, strings.Join(bad, ", "))
	}
	if len(entries.Contexts) == 0 {
		return errors.New("no kubeconfig context to add")
	}
	return nil
}
