// Package v1alpha1 defines version v1alpha1 of tuggy's spec file format.
//
// A Cluster is the Go form of the YAML file a user passes with
// "kubectl tuggy create cluster -f cluster.yaml"; flags fill the same type.
// It is an input format only: each platform translates it into its own typed
// variables, and nothing after that step depends on this package. Other input
// formats, such as Cluster API manifests, can be added later with their own
// translations.
//
// The format holds only fields every platform understands. Settings that
// belong to one platform live under spec.platformConfig, which this package
// keeps as raw JSON and the selected platform decodes into its own type.
// Adding a platform therefore never changes this package.
package v1alpha1

import "encoding/json"

const (
	// Group is the API group of tuggy types.
	Group = "tuggy.dev"
	// Version is the version of this package's types.
	Version = "v1alpha1"
	// APIVersion is the value of apiVersion for types in this package.
	APIVersion = Group + "/" + Version
	// KindCluster is the kind of the Cluster type.
	KindCluster = "Cluster"
)

// TypeMeta identifies the schema of a document.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

// ObjectMeta holds metadata common to tuggy objects.
type ObjectMeta struct {
	// Name of the cluster. Lowercase letters, digits and '-', starting with a
	// letter, at most 40 characters so it fits cloud naming limits.
	Name string `json:"name"`
}

// Cluster is the desired state of a Kubernetes cluster.
type Cluster struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta  `json:"metadata"`
	Spec     ClusterSpec `json:"spec"`
}

// ClusterSpec holds the fields every platform understands.
type ClusterSpec struct {
	// Platform that creates the cluster, for example "gke".
	Platform string `json:"platform"`

	// KubernetesVersion such as "1.34" or "1.34.1". Empty means the
	// platform's default.
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// TTL is how long the cluster should live before tuggy reports it as
	// expired. Empty or zero means it never expires.
	TTL *Duration `json:"ttl,omitempty"`

	// NodePools are the groups of worker nodes. If none are given, a single
	// pool named "default" with one node is used.
	NodePools []NodePool `json:"nodePools,omitempty"`

	// PlatformConfig holds platform-specific settings. It must be an object;
	// its schema is defined and validated by the platform.
	PlatformConfig json.RawMessage `json:"platformConfig,omitempty"`
}

// NodePool is a group of identical worker nodes.
type NodePool struct {
	// Name of the pool, unique within the cluster.
	Name string `json:"name"`

	// MachineType is the platform's instance type, for example
	// "e2-standard-2". Empty means the platform's default.
	MachineType string `json:"machineType,omitempty"`

	// Count is the number of nodes. Defaults to 1.
	Count *int32 `json:"count,omitempty"`

	// Spot requests cheaper, preemptible capacity where the platform supports it.
	Spot bool `json:"spot,omitempty"`

	// DiskSizeGB is the boot disk size per node. Zero means the platform's default.
	DiskSizeGB int32 `json:"diskSizeGB,omitempty"`
}

// NewCluster returns a Cluster with apiVersion, kind and name set, for
// building a spec from flags.
func NewCluster(name, platform string) *Cluster {
	return &Cluster{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindCluster},
		Metadata: ObjectMeta{Name: name},
		Spec:     ClusterSpec{Platform: platform},
	}
}
