package gke

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

// Config is the gke platform's part of the spec file: spec.platformConfig.
type Config struct {
	// Project is the Google Cloud project ID.
	Project string `json:"project"`

	// Location is a zone (zonal cluster) or a region (regional cluster).
	Location string `json:"location"`

	// ReleaseChannel controls automatic upgrades: RAPID, REGULAR (default),
	// STABLE, EXTENDED or UNSPECIFIED.
	ReleaseChannel string `json:"releaseChannel,omitempty"`

	// Network is the VPC network. Defaults to "default".
	Network string `json:"network,omitempty"`

	// Subnetwork in Network. Empty lets GKE choose.
	Subnetwork string `json:"subnetwork,omitempty"`

	// ImpersonateServiceAccount is a service account email to act as, as the
	// Sarayo demo did. Empty uses the caller's own credentials.
	ImpersonateServiceAccount string `json:"impersonateServiceAccount,omitempty"`
}

// Defaults for settings the user left empty.
const (
	DefaultReleaseChannel = "REGULAR"
	DefaultNetwork        = "default"

	// DefaultMachineType is a small general-purpose machine. The demo's
	// e2-small (2 GB) is too small for most system workloads.
	DefaultMachineType = "e2-standard-2"
)

var (
	// https://cloud.google.com/resource-manager/docs/creating-managing-projects
	projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	// Regions like us-central1, zones like us-central1-a.
	locationPattern = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+(-[a-z])?$`)
	serviceAccount  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z0-9-]+\.iam\.gserviceaccount\.com$`)
	releaseChannels = []string{"RAPID", "REGULAR", "STABLE", "EXTENDED", "UNSPECIFIED"}
)

// decodeConfig reads spec.platformConfig, applies defaults and validates it.
func decodeConfig(spec *v1alpha1.ClusterSpec) (Config, error) {
	var cfg Config
	if err := spec.DecodePlatformConfig(&cfg); err != nil {
		return cfg, err
	}
	cfg.ReleaseChannel = strings.ToUpper(cfg.ReleaseChannel)
	if cfg.ReleaseChannel == "" {
		cfg.ReleaseChannel = DefaultReleaseChannel
	}
	if cfg.Network == "" {
		cfg.Network = DefaultNetwork
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var errs v1alpha1.ValidationError
	add := func(field, format string, args ...any) {
		errs = append(errs, v1alpha1.FieldError{Field: "spec.platformConfig." + field, Message: fmt.Sprintf(format, args...)})
	}

	switch {
	case c.Project == "":
		add("project", "is required: the Google Cloud project ID (--project)")
	case !projectPattern.MatchString(c.Project):
		add("project", "%q is not a valid project ID (6-30 lowercase letters, digits or '-', starting with a letter)", c.Project)
	}
	switch {
	case c.Location == "":
		add("location", "is required: a zone such as us-central1-a, or a region such as us-central1 (--location)")
	case !locationPattern.MatchString(c.Location):
		add("location", "%q is not a zone (us-central1-a) or region (us-central1)", c.Location)
	}
	if !contains(releaseChannels, c.ReleaseChannel) {
		add("releaseChannel", "%q is not one of %s", c.ReleaseChannel, strings.Join(releaseChannels, ", "))
	}
	if c.ImpersonateServiceAccount != "" && !serviceAccount.MatchString(c.ImpersonateServiceAccount) {
		add("impersonateServiceAccount", "%q is not a service account email (name@project.iam.gserviceaccount.com)", c.ImpersonateServiceAccount)
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

// Regional reports whether the location is a region rather than a zone.
func (c Config) Regional() bool {
	return strings.Count(c.Location, "-") == 1
}
