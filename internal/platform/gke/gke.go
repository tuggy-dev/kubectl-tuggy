// Package gke is the Google Kubernetes Engine platform: it builds GKE
// clusters by running the embedded OpenTofu module (./module) in a container.
package gke

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/pflag"

	"github.com/tuggy-dev/kubectl-tuggy/internal/engine/tofu"
	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/docker"
)

// Name is the platform name users pass to --platform.
const Name = "gke"

func init() {
	platform.Register(New())
}

// Platform builds GKE clusters.
type Platform struct {
	// NewRunner returns the container runner. It is called lazily, so
	// registering the platform never contacts Docker.
	NewRunner func() (runner.Runner, error)

	// Image is the OpenTofu image; empty means tofu.Image().
	Image string

	// Now returns the current time, for expiry labels.
	Now func() time.Time

	// CredentialsPath returns where to look for Google credentials.
	CredentialsPath func() string

	// LookPath finds programs on PATH, such as gke-gcloud-auth-plugin.
	LookPath func(string) (string, error)

	api *googleAPI
}

var (
	_ platform.Platform   = (*Platform)(nil)
	_ platform.FlagBinder = (*Platform)(nil)
)

// New returns the GKE platform with real dependencies.
func New() *Platform {
	return &Platform{
		NewRunner: func() (runner.Runner, error) { return docker.New() },
		Now:       time.Now,
		CredentialsPath: func() string {
			home, _ := os.UserHomeDir()
			return adcPath(os.Getenv, runtime.GOOS, home)
		},
		LookPath: exec.LookPath,
		api:      newGoogleAPI(),
	}
}

// Name returns "gke".
func (p *Platform) Name() string { return Name }

// FromV1Alpha1 translates the spec file format into module variables.
func (p *Platform) FromV1Alpha1(c *v1alpha1.Cluster) (*platform.Plan, error) {
	cfg, err := decodeConfig(&c.Spec)
	if err != nil {
		return nil, err
	}
	plan := &platform.Plan{
		Name:      c.Metadata.Name,
		Platform:  Name,
		Variables: variablesFromSpec(c, cfg, p.Now()),
	}
	if c.Spec.TTL != nil {
		plan.TTL = c.Spec.TTL.Duration
	}
	return plan, nil
}

// Flag names. They fill spec.platformConfig.
const (
	flagProject        = "project"
	flagLocation       = "location"
	flagReleaseChannel = "release-channel"
	flagNetwork        = "network"
	flagSubnetwork     = "subnetwork"
	flagImpersonate    = "impersonate-service-account"
)

// flagFields maps each flag to its platformConfig field.
var flagFields = map[string]string{
	flagProject:        "project",
	flagLocation:       "location",
	flagReleaseChannel: "releaseChannel",
	flagNetwork:        "network",
	flagSubnetwork:     "subnetwork",
	flagImpersonate:    "impersonateServiceAccount",
}

// AddFlags registers the GKE flags.
func (p *Platform) AddFlags(fs *pflag.FlagSet) {
	fs.String(flagProject, "", "Google Cloud project ID (gke)")
	fs.String(flagLocation, "", "zone, such as us-central1-a, or region, such as us-central1 (gke)")
	fs.String(flagReleaseChannel, "", "release channel: RAPID, REGULAR, STABLE or EXTENDED (gke, default REGULAR)")
	fs.String(flagNetwork, "", "VPC network (gke, default \"default\")")
	fs.String(flagSubnetwork, "", "subnetwork (gke)")
	fs.String(flagImpersonate, "", "service account email to act as (gke)")
}

// PlatformConfigFromFlags builds spec.platformConfig from the GKE flags the
// user set. Flags left unset are omitted, so platform defaults apply.
func (p *Platform) PlatformConfigFromFlags(fs *pflag.FlagSet) (json.RawMessage, error) {
	cfg := map[string]string{}
	for flag, field := range flagFields {
		f := fs.Lookup(flag)
		if f != nil && f.Changed {
			cfg[field] = f.Value.String()
		}
	}
	return json.Marshal(cfg)
}

func (p *Platform) engine(r runner.Runner, cacheDir string) *tofu.Engine {
	e := &tofu.Engine{Runner: r, Image: p.Image}
	if cacheDir != "" {
		e.PluginCacheDir = cacheDir + string(os.PathSeparator) + "plugins"
	}
	return e
}

func (p *Platform) image() string {
	if p.Image != "" {
		return p.Image
	}
	return tofu.Image()
}
