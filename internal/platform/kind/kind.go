// Package kind is the kind platform: it builds local Kubernetes clusters whose
// nodes are containers, using the kind Go library (sigs.k8s.io/kind) directly
// from the tuggy process. Unlike the gke platform it does not use OpenTofu:
// kind keeps its own state in container labels, and tuggy already runs on the
// host next to the container engine. See docs/design/0002-kind-platform.md.
package kind

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"

	"github.com/spf13/pflag"
	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/docker"
)

// Name is the platform name users pass to --platform.
const Name = "kind"

func init() {
	platform.Register(New())
}

// Platform builds kind clusters.
type Platform struct {
	// NewProvider returns the kind library, logging to logger. It is called
	// lazily, so registering the platform never contacts the engine.
	NewProvider func(logger *progressLogger) kindProvider

	// NewRunner returns tuggy's container runner, used only to check that
	// the engine is reachable.
	NewRunner func() (runner.Runner, error)

	// LookPath finds programs on PATH, such as docker.
	LookPath func(string) (string, error)

	// Stat checks that mount paths exist.
	Stat func(string) (os.FileInfo, error)

	// PortFree reports whether a host port can be used.
	PortFree func(address string, port int32) bool

	// KubeVersion reads the Kubernetes version from a node.
	KubeVersion func(node nodes.Node) (string, error)
}

var (
	_ platform.Platform   = (*Platform)(nil)
	_ platform.FlagBinder = (*Platform)(nil)
)

// New returns the kind platform with real dependencies.
func New() *Platform {
	return &Platform{
		NewProvider: newLibraryProvider,
		NewRunner:   func() (runner.Runner, error) { return docker.New() },
		LookPath:    exec.LookPath,
		Stat:        os.Stat,
		PortFree:    portFree,
		KubeVersion: nodeutils.KubeVersion,
	}
}

// Name returns "kind".
func (p *Platform) Name() string { return Name }

// FromV1Alpha1 translates the spec file format into the platform's plan.
func (p *Platform) FromV1Alpha1(c *v1alpha1.Cluster) (*platform.Plan, error) {
	cfg, err := decodeConfig(&c.Spec)
	if err != nil {
		return nil, err
	}
	vars, err := variablesFromSpec(c, cfg)
	if err != nil {
		return nil, err
	}
	plan := &platform.Plan{
		Name:      c.Metadata.Name,
		Platform:  Name,
		Variables: vars,
	}
	if c.Spec.TTL != nil {
		plan.TTL = c.Spec.TTL.Duration
	}
	return plan, nil
}

// Flag names. They fill spec.platformConfig. Mounts and port mappings are
// lists, so they are set in a spec file only.
const (
	flagNodeImage     = "node-image"
	flagControlPlanes = "control-planes"
	flagAPIServerPort = "api-server-port"
)

// AddFlags registers the kind flags.
func (p *Platform) AddFlags(fs *pflag.FlagSet) {
	fs.String(flagNodeImage, "", "exact node image, such as kindest/node:v1.37.0 (kind; overrides --kubernetes-version)")
	fs.Int(flagControlPlanes, 0, "number of control-plane nodes: 1 or 3 (kind, default 1)")
	fs.Int32(flagAPIServerPort, 0, "host port for the Kubernetes API (kind, default: a random free port)")
}

// PlatformConfigFromFlags builds spec.platformConfig from the kind flags the
// user set. Flags left unset are omitted, so platform defaults apply.
func (p *Platform) PlatformConfigFromFlags(fs *pflag.FlagSet) (json.RawMessage, error) {
	cfg := map[string]any{}
	if f := fs.Lookup(flagNodeImage); f != nil && f.Changed {
		cfg["nodeImage"] = f.Value.String()
	}
	for flag, field := range map[string]string{flagControlPlanes: "controlPlanes", flagAPIServerPort: "apiServerPort"} {
		f := fs.Lookup(flag)
		if f == nil || !f.Changed {
			continue
		}
		n, err := strconv.Atoi(f.Value.String())
		if err != nil {
			return nil, err
		}
		cfg[field] = n
	}
	return json.Marshal(cfg)
}
