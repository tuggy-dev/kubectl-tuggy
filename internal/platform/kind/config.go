package kind

import (
	"fmt"
	"strings"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

// Config is the kind platform's part of the spec file: spec.platformConfig.
type Config struct {
	// NodeImage is an exact node image, such as a custom-built one. It wins
	// over spec.kubernetesVersion; setting both is an error.
	NodeImage string `json:"nodeImage,omitempty"`

	// ControlPlanes is the number of control-plane nodes: 1 (default) or 3.
	ControlPlanes int `json:"controlPlanes,omitempty"`

	// APIServerPort is the host port for the Kubernetes API. 0 picks a
	// random free port.
	APIServerPort int32 `json:"apiServerPort,omitempty"`

	// ExtraMounts are host directories visible inside every node.
	ExtraMounts []Mount `json:"extraMounts,omitempty"`

	// ExtraPortMappings forward host ports to the first control-plane node.
	ExtraPortMappings []PortMapping `json:"extraPortMappings,omitempty"`
}

// Mount makes a host directory visible inside every node.
type Mount struct {
	// HostPath on the user's computer. Relative paths are resolved against
	// the current directory.
	HostPath string `json:"hostPath"`
	// ContainerPath inside the node, an absolute path such as /mnt/data.
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly,omitempty"`
}

// PortMapping forwards a host port to a port on the first control-plane node.
type PortMapping struct {
	ContainerPort int32 `json:"containerPort"`
	// HostPort on the user's computer. 0 picks a random free port.
	HostPort int32 `json:"hostPort,omitempty"`
	// ListenAddress on the host. Empty means all addresses (0.0.0.0).
	ListenAddress string `json:"listenAddress,omitempty"`
	// Protocol is TCP (default), UDP or SCTP.
	Protocol string `json:"protocol,omitempty"`
}

// DefaultControlPlanes is the number of control-plane nodes when none is set.
const DefaultControlPlanes = 1

var protocols = []string{"TCP", "UDP", "SCTP"}

// decodeConfig reads spec.platformConfig, applies defaults and validates it.
func decodeConfig(spec *v1alpha1.ClusterSpec) (Config, error) {
	var cfg Config
	if err := spec.DecodePlatformConfig(&cfg); err != nil {
		return cfg, err
	}
	if cfg.ControlPlanes == 0 {
		cfg.ControlPlanes = DefaultControlPlanes
	}
	for i := range cfg.ExtraPortMappings {
		pm := &cfg.ExtraPortMappings[i]
		pm.Protocol = strings.ToUpper(pm.Protocol)
		if pm.Protocol == "" {
			pm.Protocol = "TCP"
		}
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var errs v1alpha1.ValidationError
	add := func(field, format string, args ...any) {
		errs = append(errs, v1alpha1.FieldError{Field: "spec.platformConfig." + field, Message: fmt.Sprintf(format, args...)})
	}

	if c.ControlPlanes != 1 && c.ControlPlanes != 3 {
		add("controlPlanes", "must be 1 or 3, got %d (--control-planes)", c.ControlPlanes)
	}
	if c.APIServerPort < 0 || c.APIServerPort > 65535 {
		add("apiServerPort", "%d is not a port (0-65535; 0 picks a free one)", c.APIServerPort)
	}

	for i, m := range c.ExtraMounts {
		path := fmt.Sprintf("extraMounts[%d]", i)
		if m.HostPath == "" {
			add(path+".hostPath", "is required: a directory on your computer")
		}
		switch {
		case m.ContainerPath == "":
			add(path+".containerPath", "is required: where the directory appears inside the nodes, such as /mnt/data")
		case !strings.HasPrefix(m.ContainerPath, "/"):
			add(path+".containerPath", "%q must be an absolute path, such as /mnt/data", m.ContainerPath)
		}
	}

	hostPorts := map[string]int{}
	for i, pm := range c.ExtraPortMappings {
		path := fmt.Sprintf("extraPortMappings[%d]", i)
		if pm.ContainerPort < 1 || pm.ContainerPort > 65535 {
			add(path+".containerPort", "%d is not a port (1-65535)", pm.ContainerPort)
		}
		if pm.HostPort < 0 || pm.HostPort > 65535 {
			add(path+".hostPort", "%d is not a port (0-65535; 0 picks a free one)", pm.HostPort)
		}
		if !contains(protocols, pm.Protocol) {
			add(path+".protocol", "%q is not one of %s", pm.Protocol, strings.Join(protocols, ", "))
		}
		if pm.HostPort != 0 {
			key := fmt.Sprintf("%s/%s/%d", pm.ListenAddress, pm.Protocol, pm.HostPort)
			if first, dup := hostPorts[key]; dup {
				add(path+".hostPort", "%d is already used by extraPortMappings[%d]", pm.HostPort, first)
			} else {
				hostPorts[key] = i
			}
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
