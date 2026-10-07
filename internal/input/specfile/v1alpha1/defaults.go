package v1alpha1

// DefaultNodePoolName is the name of the node pool added when none is given.
const DefaultNodePoolName = "default"

// SetDefaults fills in fields the user left empty. Platform-specific defaults,
// such as the machine type, are applied by the platform.
func SetDefaults(c *Cluster) {
	if c.APIVersion == "" {
		c.APIVersion = APIVersion
	}
	if c.Kind == "" {
		c.Kind = KindCluster
	}

	if len(c.Spec.NodePools) == 0 {
		c.Spec.NodePools = []NodePool{{Name: DefaultNodePoolName}}
	}
	for i := range c.Spec.NodePools {
		if c.Spec.NodePools[i].Count == nil {
			one := int32(1)
			c.Spec.NodePools[i].Count = &one
		}
	}
}
