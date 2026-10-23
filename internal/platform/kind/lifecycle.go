package kind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/yaml"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// Files the platform keeps in the cluster's directory.
const (
	// VariablesFile holds the resolved plan, read by Describe.
	VariablesFile = "kind-cluster.json"
	// ConfigFile is the rendered kind config, for troubleshooting.
	ConfigFile = "kind-config.yaml"
	// kubeconfigFile is a scratch kubeconfig kind writes to instead of
	// ~/.kube/config; tuggy merges its own entries.
	kubeconfigFile = "kind-kubeconfig.tmp"
)

// Output names, also used as keys in the cluster record's outputs.
const (
	OutputEndpoint          = "endpoint"
	OutputKubernetesVersion = "kubernetes_version"
	OutputNodeImage         = "node_image"
	OutputProvider          = "provider"
)

// waitForReady is how long kind waits for the control plane to be ready.
const waitForReady = 5 * time.Minute

// Create writes the plan into opts.Dir and builds the cluster with the kind
// library. With opts.DryRun it only reports the nodes it would create.
func (p *Platform) Create(ctx context.Context, plan *platform.Plan, opts platform.CreateOptions) (*platform.ClusterInfo, error) {
	vars, ok := plan.Variables.(Variables)
	if !ok {
		return nil, errors.New("plan was not made by the kind platform")
	}
	cfg := kindConfig(vars)
	if err := writeFiles(opts.Dir, vars, cfg); err != nil {
		return nil, err
	}

	if opts.DryRun {
		result := describeVariables(vars)
		result.Changes = nodeChanges(plannedNodes(vars), "create")
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	kubeconfigPath := filepath.Join(opts.Dir, kubeconfigFile)
	defer func() { _ = os.Remove(kubeconfigPath) }()

	provider := p.NewProvider(newProgressLogger(opts.Log, opts.OnProgress))
	// The kind library has no cancellation, so a cancelled context is only
	// noticed when it returns; retain=false makes a failed create remove
	// what it built.
	err := provider.Create(vars.ClusterName,
		cluster.CreateWithV1Alpha4Config(cfg),
		cluster.CreateWithNodeImage(vars.NodeImage),
		cluster.CreateWithWaitForReady(waitForReady),
		cluster.CreateWithKubeconfigPath(kubeconfigPath),
		cluster.CreateWithRetain(false),
		cluster.CreateWithDisplayUsage(false),
		cluster.CreateWithDisplaySalutation(false),
	)
	if err != nil {
		return nil, fmt.Errorf("creating kind cluster %s: %w", vars.ClusterName, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := describeVariables(vars)
	result.Outputs = map[string]string{
		OutputNodeImage: vars.NodeImage,
		OutputProvider:  "docker",
	}
	if kc, err := p.kubeconfig(provider, vars.Name); err == nil {
		for _, c := range kc.Clusters {
			result.Outputs[OutputEndpoint] = c.Server
		}
	}
	if version := p.version(provider, vars.ClusterName); version != "" {
		result.Outputs[OutputKubernetesVersion] = version
		result.KubernetesVersion = version
	}
	return result, nil
}

// Delete removes the cluster's containers. Deleting a cluster whose
// containers are already gone succeeds, so its record can be cleaned up.
func (p *Platform) Delete(ctx context.Context, rec *clustermeta.Record, opts platform.DeleteOptions) error {
	provider := p.NewProvider(newProgressLogger(opts.Log, opts.OnProgress))
	name := clusterName(rec.Name)

	nodes, err := provider.ListNodes(name)
	if err != nil {
		return fmt.Errorf("listing nodes of kind cluster %s: %w", name, err)
	}
	var names []string
	for _, n := range nodes {
		names = append(names, n.String())
	}
	if opts.Confirm != nil {
		if err := opts.Confirm(*nodeChanges(names, "delete")); err != nil {
			return err
		}
	}
	if opts.DryRun {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	info(opts.OnProgress, "Removing nodes")
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return err
	}
	kubeconfigPath := filepath.Join(opts.Dir, kubeconfigFile)
	defer func() { _ = os.Remove(kubeconfigPath) }()
	if err := provider.Delete(name, kubeconfigPath); err != nil {
		return fmt.Errorf("deleting kind cluster %s: %w", name, err)
	}
	return nil
}

// Describe reports the cluster's details from its kind-cluster.json.
func (p *Platform) Describe(_ context.Context, rec *clustermeta.Record, dir string) (*platform.ClusterInfo, error) {
	data, err := os.ReadFile(filepath.Join(dir, VariablesFile)) // #nosec G304 -- the cluster's own directory
	if err != nil {
		return nil, fmt.Errorf("reading settings of cluster %q: %w", rec.Name, err)
	}
	var vars Variables
	if err := json.Unmarshal(data, &vars); err != nil {
		return nil, fmt.Errorf("reading settings of cluster %q: %w", rec.Name, err)
	}
	result := describeVariables(vars)
	result.Outputs = rec.Outputs
	result.KubernetesVersion = rec.Outputs[OutputKubernetesVersion]
	return result, nil
}

// Kubeconfig fetches the cluster's kubeconfig from kind each time and
// renames its entries to tuggy-<name>. kind's kubeconfig holds the cluster's
// admin client certificate; fetching it live means tuggy's record never
// stores that credential.
func (p *Platform) Kubeconfig(_ context.Context, rec *clustermeta.Record) (*clientcmdapi.Config, error) {
	return p.kubeconfig(p.NewProvider(newProgressLogger(nil, nil)), rec.Name)
}

func (p *Platform) kubeconfig(provider kindProvider, name string) (*clientcmdapi.Config, error) {
	raw, err := provider.KubeConfig(clusterName(name), false)
	if err != nil {
		return nil, fmt.Errorf("getting kubeconfig of kind cluster %s: %w", clusterName(name), err)
	}
	kc, err := clientcmd.Load([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig of kind cluster %s: %w", clusterName(name), err)
	}
	kindCtx, ok := kc.Contexts[kc.CurrentContext]
	if !ok {
		return nil, fmt.Errorf("kubeconfig of kind cluster %s has no current context", clusterName(name))
	}
	c, u := kc.Clusters[kindCtx.Cluster], kc.AuthInfos[kindCtx.AuthInfo]
	if c == nil || u == nil {
		return nil, fmt.Errorf("kubeconfig of kind cluster %s is incomplete", clusterName(name))
	}

	entry := "tuggy-" + name
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[entry] = c
	cfg.AuthInfos[entry] = u
	cfg.Contexts[entry] = &clientcmdapi.Context{Cluster: entry, AuthInfo: entry}
	cfg.CurrentContext = entry
	return cfg, nil
}

// ListRemote is not implemented yet (milestone 2).
func (p *Platform) ListRemote(context.Context, platform.ListOptions) ([]platform.ClusterInfo, error) {
	return nil, platform.ErrNotSupported
}

// version reads the Kubernetes version from the first node, or returns "".
func (p *Platform) version(provider kindProvider, name string) string {
	nodes, err := provider.ListNodes(name)
	if err != nil || len(nodes) == 0 {
		return ""
	}
	v, err := p.KubeVersion(nodes[0])
	if err != nil {
		return ""
	}
	return v
}

// writeFiles saves the plan for describe and the kind config for
// troubleshooting.
func writeFiles(dir string, vars Variables, cfg any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, VariablesFile), append(data, '\n'), 0o600); err != nil {
		return err
	}
	rendered, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ConfigFile), rendered, 0o600)
}

func describeVariables(v Variables) *platform.ClusterInfo {
	result := &platform.ClusterInfo{Name: v.Name, Platform: Name, KubernetesVersion: v.KubernetesVersion}
	for _, np := range v.NodePools {
		result.NodePools = append(result.NodePools, platform.NodePoolInfo{Name: np.Name, Count: np.Count})
	}
	return result
}

// nodeChanges reports one kind_node resource per container.
func nodeChanges(names []string, action string) *platform.Changes {
	c := &platform.Changes{}
	for _, n := range names {
		c.Resources = append(c.Resources, platform.ResourceChange{Address: "kind_node." + n, Type: "kind_node", Action: action})
	}
	if action == "delete" {
		c.Remove = len(names)
	} else {
		c.Add = len(names)
	}
	return c
}

func info(onProgress func(platform.Progress), msg string) {
	if onProgress != nil {
		onProgress(platform.Progress{Kind: platform.ProgressInfo, Message: msg})
	}
}
