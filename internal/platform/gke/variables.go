package gke

import (
	"strconv"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

// Variables mirrors the module's variables.tf exactly; it is written to the
// cluster's tuggy.tfvars.json. TestVariablesMatchModule keeps the two in sync.
type Variables struct {
	Name                      string            `json:"name"`
	Project                   string            `json:"project"`
	Location                  string            `json:"location"`
	ReleaseChannel            string            `json:"release_channel"`
	KubernetesVersion         *string           `json:"kubernetes_version"`
	Network                   string            `json:"network"`
	Subnetwork                *string           `json:"subnetwork"`
	NodePools                 []NodePool        `json:"node_pools"`
	Labels                    map[string]string `json:"labels"`
	ImpersonateServiceAccount *string           `json:"impersonate_service_account"`
	DeletionProtection        bool              `json:"deletion_protection"`
}

// NodePool mirrors one entry of the module's node_pools variable.
type NodePool struct {
	Name        string `json:"name"`
	MachineType string `json:"machine_type"`
	Count       int    `json:"count"`
	Spot        bool   `json:"spot"`
	DiskSizeGB  *int   `json:"disk_size_gb"`
}

// Labels tuggy puts on every resource, so tuggy clusters and their cost can
// be found in Google Cloud.
const (
	LabelManagedBy = "managed-by"
	LabelCluster   = "tuggy-cluster"
	LabelExpiresAt = "tuggy-expires-at" // Unix seconds
)

// variablesFromSpec translates a validated spec and its decoded GKE config
// into module variables. now is the creation time, used for the expiry label.
func variablesFromSpec(c *v1alpha1.Cluster, cfg Config, now time.Time) Variables {
	v := Variables{
		Name:           c.Metadata.Name,
		Project:        cfg.Project,
		Location:       cfg.Location,
		ReleaseChannel: cfg.ReleaseChannel,
		Network:        cfg.Network,
		Labels: map[string]string{
			LabelManagedBy: "tuggy",
			LabelCluster:   c.Metadata.Name,
		},
	}
	if c.Spec.KubernetesVersion != "" {
		v.KubernetesVersion = ptr(c.Spec.KubernetesVersion)
	}
	if cfg.Subnetwork != "" {
		v.Subnetwork = ptr(cfg.Subnetwork)
	}
	if cfg.ImpersonateServiceAccount != "" {
		v.ImpersonateServiceAccount = ptr(cfg.ImpersonateServiceAccount)
	}
	if c.Spec.TTL != nil && c.Spec.TTL.Duration > 0 {
		v.Labels[LabelExpiresAt] = strconv.FormatInt(now.Add(c.Spec.TTL.Duration).Unix(), 10)
	}

	for _, np := range c.Spec.NodePools {
		pool := NodePool{Name: np.Name, MachineType: np.MachineType, Spot: np.Spot}
		if pool.MachineType == "" {
			pool.MachineType = DefaultMachineType
		}
		if np.Count != nil {
			pool.Count = int(*np.Count)
		}
		if np.DiskSizeGB > 0 {
			pool.DiskSizeGB = ptr(int(np.DiskSizeGB))
		}
		v.NodePools = append(v.NodePools, pool)
	}
	return v
}

func ptr[T any](v T) *T { return &v }
