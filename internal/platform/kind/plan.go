package kind

import (
	"fmt"
	"path/filepath"

	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

// Variables are the kind platform's plan: the fully resolved cluster. They
// are written to the cluster's kind-cluster.json for describe.
type Variables struct {
	// Name is the tuggy cluster name.
	Name string `json:"name"`
	// ClusterName is the kind cluster name, tuggy-<name>.
	ClusterName       string        `json:"clusterName"`
	KubernetesVersion string        `json:"kubernetesVersion,omitempty"`
	NodeImage         string        `json:"nodeImage"`
	ControlPlanes     int           `json:"controlPlanes"`
	APIServerPort     int32         `json:"apiServerPort,omitempty"`
	NodePools         []NodePool    `json:"nodePools"`
	ExtraMounts       []Mount       `json:"extraMounts,omitempty"`
	ExtraPortMappings []PortMapping `json:"extraPortMappings,omitempty"`
}

// NodePool is a group of worker nodes, labelled with LabelNodePool.
type NodePool struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// LabelNodePool is set on every worker node to its pool's name, so workloads
// can target a pool with a nodeSelector, as on GKE.
const LabelNodePool = "tuggy.dev/node-pool"

// clusterName is the kind cluster name for a tuggy cluster. The prefix keeps
// tuggy clusters apart from clusters made with kind directly.
func clusterName(name string) string { return "tuggy-" + name }

// variablesFromSpec translates a validated spec and its decoded kind config
// into the platform's plan. Settings that only make sense in the cloud are
// rejected rather than ignored, so one spec file never means two things.
func variablesFromSpec(c *v1alpha1.Cluster, cfg Config) (Variables, error) {
	var errs v1alpha1.ValidationError
	add := func(field, format string, args ...any) {
		errs = append(errs, v1alpha1.FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	const shared = "kind nodes are containers that share your computer's resources"

	v := Variables{
		Name:              c.Metadata.Name,
		ClusterName:       clusterName(c.Metadata.Name),
		KubernetesVersion: c.Spec.KubernetesVersion,
		ControlPlanes:     cfg.ControlPlanes,
		APIServerPort:     cfg.APIServerPort,
		ExtraPortMappings: cfg.ExtraPortMappings,
	}

	switch {
	case cfg.NodeImage != "" && c.Spec.KubernetesVersion != "":
		add("spec.platformConfig.nodeImage", "can't be combined with spec.kubernetesVersion (--kubernetes-version); the image decides the version")
	case cfg.NodeImage != "":
		v.NodeImage = cfg.NodeImage
	default:
		img, err := imageFor(c.Spec.KubernetesVersion)
		if err != nil {
			add("spec.kubernetesVersion", "%s", err.Error())
		}
		v.NodeImage = img
	}

	for i, np := range c.Spec.NodePools {
		path := fmt.Sprintf("spec.nodePools[%d]", i)
		if np.MachineType != "" {
			add(path+".machineType", "is not supported on kind (--machine-type): %s", shared)
		}
		if np.Spot {
			add(path+".spot", "is not supported on kind (--spot): %s", shared)
		}
		if np.DiskSizeGB != 0 {
			add(path+".diskSizeGB", "is not supported on kind: %s", shared)
		}
		pool := NodePool{Name: np.Name}
		if np.Count != nil {
			pool.Count = int(*np.Count)
		}
		v.NodePools = append(v.NodePools, pool)
	}

	for i, m := range cfg.ExtraMounts {
		abs, err := filepath.Abs(m.HostPath)
		if err != nil {
			add(fmt.Sprintf("spec.platformConfig.extraMounts[%d].hostPath", i), "%s", err.Error())
			continue
		}
		m.HostPath = abs
		v.ExtraMounts = append(v.ExtraMounts, m)
	}

	if len(errs) > 0 {
		return v, errs
	}
	return v, nil
}

// kindConfig turns the plan into kind's own cluster config: the control
// planes first (the first one gets the port mappings), then each pool's
// workers labelled with their pool. Every node gets the mounts.
func kindConfig(v Variables) *v1alpha4.Cluster {
	cfg := &v1alpha4.Cluster{
		TypeMeta: v1alpha4.TypeMeta{Kind: "Cluster", APIVersion: "kind.x-k8s.io/v1alpha4"},
		Name:     v.ClusterName,
	}
	cfg.Networking.APIServerPort = v.APIServerPort

	var mounts []v1alpha4.Mount
	for _, m := range v.ExtraMounts {
		mounts = append(mounts, v1alpha4.Mount{HostPath: m.HostPath, ContainerPath: m.ContainerPath, Readonly: m.ReadOnly})
	}

	for i := 0; i < v.ControlPlanes; i++ {
		node := v1alpha4.Node{Role: v1alpha4.ControlPlaneRole, Image: v.NodeImage, ExtraMounts: mounts}
		if i == 0 {
			for _, pm := range v.ExtraPortMappings {
				node.ExtraPortMappings = append(node.ExtraPortMappings, v1alpha4.PortMapping{
					ContainerPort: pm.ContainerPort,
					HostPort:      pm.HostPort,
					ListenAddress: pm.ListenAddress,
					Protocol:      v1alpha4.PortMappingProtocol(pm.Protocol),
				})
			}
		}
		cfg.Nodes = append(cfg.Nodes, node)
	}

	for _, pool := range v.NodePools {
		for i := 0; i < pool.Count; i++ {
			cfg.Nodes = append(cfg.Nodes, v1alpha4.Node{
				Role:        v1alpha4.WorkerRole,
				Image:       v.NodeImage,
				Labels:      map[string]string{LabelNodePool: pool.Name},
				ExtraMounts: mounts,
			})
		}
	}
	return cfg
}

// plannedNodes returns the names kind will give the cluster's containers,
// for dry runs: <cluster>-control-plane, -control-plane2, ..., <cluster>-worker,
// -worker2, ..., and an external load balancer when there are several
// control planes.
func plannedNodes(v Variables) []string {
	var names []string
	add := func(role string, n int) {
		for i := 1; i <= n; i++ {
			name := v.ClusterName + "-" + role
			if i > 1 {
				name = fmt.Sprintf("%s%d", name, i)
			}
			names = append(names, name)
		}
	}
	add("control-plane", v.ControlPlanes)
	workers := 0
	for _, pool := range v.NodePools {
		workers += pool.Count
	}
	add("worker", workers)
	if v.ControlPlanes > 1 {
		names = append(names, v.ClusterName+"-external-load-balancer")
	}
	return names
}
