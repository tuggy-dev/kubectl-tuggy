package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// specFlags are the flags that describe a cluster. Each one is the
// command-line form of a field of the spec file format.
type specFlags struct {
	file              string
	platform          string
	ttl               string
	kubernetesVersion string
	machineType       string
	nodeCount         int32
	spot              bool
}

// specFlagNames are the flags that can't be combined with -f.
var specFlagNames = []string{"platform", "ttl", "kubernetes-version", "machine-type", "node-count", "spot"}

func (f *specFlags) add(cmd *cobra.Command, platforms *platform.Registry) {
	fs := cmd.Flags()
	fs.StringVarP(&f.file, "filename", "f", "", "cluster spec file (YAML); see docs/examples/cluster-gke.yaml")
	fs.StringVar(&f.platform, "platform", "", "where to build the cluster: "+strings.Join(platforms.Names(), ", "))
	fs.StringVar(&f.ttl, "ttl", "", "report the cluster as expired after this long, for example 8h or 3d (default: never)")
	fs.StringVar(&f.kubernetesVersion, "kubernetes-version", "", "Kubernetes version such as 1.34 (default: the platform's default)")
	fs.StringVar(&f.machineType, "machine-type", "", "machine type for the nodes (default: the platform's default)")
	fs.Int32Var(&f.nodeCount, "node-count", 1, "number of nodes")
	fs.BoolVar(&f.spot, "spot", false, "use cheaper, preemptible spot capacity")

	// Each platform's own flags, such as --project for gke. A flag offered
	// by several platforms is registered once.
	for _, name := range platforms.Names() {
		p, _ := platforms.Get(name)
		binder, ok := p.(platform.FlagBinder)
		if !ok {
			continue
		}
		own := pflag.NewFlagSet(name, pflag.ContinueOnError)
		binder.AddFlags(own)
		own.VisitAll(func(fl *pflag.Flag) {
			if fs.Lookup(fl.Name) == nil {
				fs.AddFlag(fl)
			}
		})
	}
}

// build returns the cluster spec described by -f or by the flags, with
// defaults applied and validated. name is the positional argument, if any.
func (f *specFlags) build(cmd *cobra.Command, name string, platforms *platform.Registry) (*v1alpha1.Cluster, platform.Platform, error) {
	if f.file != "" {
		return f.fromFile(cmd, name, platforms)
	}
	if name == "" {
		return nil, nil, usageError(cmd, errors.New("a cluster name is required, or a spec file with -f"))
	}

	platformName := f.platform
	if platformName == "" {
		names := platforms.Names()
		if len(names) != 1 {
			return nil, nil, usageError(cmd, fmt.Errorf("--platform is required (one of %s)", strings.Join(names, ", ")))
		}
		platformName = names[0]
	}
	p, err := platforms.Get(platformName)
	if err != nil {
		return nil, nil, usageError(cmd, err)
	}

	c := v1alpha1.NewCluster(name, platformName)
	c.Spec.KubernetesVersion = f.kubernetesVersion
	if f.ttl != "" {
		ttl, err := v1alpha1.ParseDuration(f.ttl)
		if err != nil {
			return nil, nil, usageError(cmd, fmt.Errorf("--ttl: %w", err))
		}
		c.Spec.TTL = &ttl
	}
	count := f.nodeCount
	c.Spec.NodePools = []v1alpha1.NodePool{{
		Name:        v1alpha1.DefaultNodePoolName,
		MachineType: f.machineType,
		Count:       &count,
		Spot:        f.spot,
	}}
	if binder, ok := p.(platform.FlagBinder); ok {
		raw, err := binder.PlatformConfigFromFlags(cmd.Flags())
		if err != nil {
			return nil, nil, usageError(cmd, err)
		}
		c.Spec.PlatformConfig = raw
	}
	return finish(cmd, c, p)
}

func (f *specFlags) fromFile(cmd *cobra.Command, name string, platforms *platform.Registry) (*v1alpha1.Cluster, platform.Platform, error) {
	var conflicting []string
	cmd.Flags().Visit(func(fl *pflag.Flag) {
		if fl.Name != "filename" && isSpecFlag(cmd, fl.Name) {
			conflicting = append(conflicting, "--"+fl.Name)
		}
	})
	if len(conflicting) > 0 {
		return nil, nil, usageError(cmd, fmt.Errorf("%s cannot be combined with -f; put the setting in the file instead", strings.Join(conflicting, ", ")))
	}

	c, err := v1alpha1.LoadFile(f.file)
	if err != nil {
		return nil, nil, WithExitCode(ExitUsage, err)
	}
	if name != "" && name != c.Metadata.Name {
		return nil, nil, usageError(cmd, fmt.Errorf("cluster name %q does not match metadata.name %q in %s", name, c.Metadata.Name, f.file))
	}
	p, err := platforms.Get(c.Spec.Platform)
	if err != nil {
		return nil, nil, WithExitCode(ExitUsage, fmt.Errorf("%s: spec.platform: %w", f.file, err))
	}
	return finish(cmd, c, p)
}

// isSpecFlag reports whether a flag describes the cluster (and so belongs in
// the spec file when -f is used), as opposed to controlling the command.
func isSpecFlag(cmd *cobra.Command, name string) bool {
	for _, n := range specFlagNames {
		if n == name {
			return true
		}
	}
	fl := cmd.Flags().Lookup(name)
	return fl != nil && fl.Annotations[platformFlagAnnotation] != nil
}

const platformFlagAnnotation = "tuggy.dev/platform-flag"

func finish(cmd *cobra.Command, c *v1alpha1.Cluster, p platform.Platform) (*v1alpha1.Cluster, platform.Platform, error) {
	v1alpha1.SetDefaults(c)
	if err := c.Validate(); err != nil {
		return nil, nil, WithExitCode(ExitUsage, err)
	}
	return c, p, nil
}

// markPlatformFlags annotates the flags platforms added, so -f can reject them.
func markPlatformFlags(cmd *cobra.Command, platforms *platform.Registry) {
	for _, name := range platforms.Names() {
		p, _ := platforms.Get(name)
		binder, ok := p.(platform.FlagBinder)
		if !ok {
			continue
		}
		own := pflag.NewFlagSet(name, pflag.ContinueOnError)
		binder.AddFlags(own)
		own.VisitAll(func(fl *pflag.Flag) {
			_ = cmd.Flags().SetAnnotation(fl.Name, platformFlagAnnotation, []string{name})
		})
	}
}
