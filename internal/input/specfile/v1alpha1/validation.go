package v1alpha1

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxNameLength is the longest allowed cluster or node pool name. GKE
	// cluster names are limited to 40 characters.
	MaxNameLength = 40

	// MinTTL is the shortest allowed TTL. Creating a cluster takes around
	// ten minutes, so anything shorter would expire before it is ready.
	MinTTL = 30 * time.Minute

	// MinDiskSizeGB is the smallest boot disk platforms accept.
	MinDiskSizeGB = 10
)

var (
	namePattern     = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
	platformPattern = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	versionPattern  = regexp.MustCompile(`^\d+\.\d+(\.\d+)?(-[0-9A-Za-z.-]+)?$`)
)

// FieldError is a problem with one field of a spec.
type FieldError struct {
	// Field is the path to the field, for example "spec.nodePools[1].name".
	Field   string
	Message string
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// ValidationError lists every problem found in a spec, so the user can fix
// them all at once.
type ValidationError []FieldError

func (e ValidationError) Error() string {
	if len(e) == 1 {
		return "invalid cluster spec: " + e[0].Error()
	}
	var b strings.Builder
	b.WriteString("invalid cluster spec:")
	for _, fe := range e {
		b.WriteString("\n  - ")
		b.WriteString(fe.Error())
	}
	return b.String()
}

// Validate checks the fields every platform understands. Call it after
// SetDefaults. The platform validates spec.platformConfig separately.
func (c *Cluster) Validate() error {
	var errs ValidationError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if c.APIVersion != APIVersion {
		add("apiVersion", "must be %q, got %q", APIVersion, c.APIVersion)
	}
	if c.Kind != KindCluster {
		add("kind", "must be %q, got %q", KindCluster, c.Kind)
	}

	if msg := validateName(c.Metadata.Name); msg != "" {
		add("metadata.name", "%s", msg)
	}

	s := c.Spec
	switch {
	case s.Platform == "":
		add("spec.platform", "is required, for example \"gke\"")
	case !platformPattern.MatchString(s.Platform):
		add("spec.platform", "%q is not a valid platform name", s.Platform)
	}

	if s.KubernetesVersion != "" && !versionPattern.MatchString(s.KubernetesVersion) {
		add("spec.kubernetesVersion", "%q is not a version such as \"1.34\" or \"1.34.1\"", s.KubernetesVersion)
	}

	if s.TTL != nil && s.TTL.Duration != 0 {
		switch {
		case s.TTL.Duration < 0:
			add("spec.ttl", "must not be negative")
		case s.TTL.Duration < MinTTL:
			add("spec.ttl", "must be at least %s, or 0 for no expiry", Duration{MinTTL})
		}
	}

	if len(s.NodePools) == 0 {
		add("spec.nodePools", "at least one node pool is required")
	}
	seen := map[string]int{}
	var totalNodes int64
	for i, np := range s.NodePools {
		path := fmt.Sprintf("spec.nodePools[%d]", i)
		if msg := validateName(np.Name); msg != "" {
			add(path+".name", "%s", msg)
		} else if first, dup := seen[np.Name]; dup {
			add(path+".name", "%q is already used by spec.nodePools[%d]", np.Name, first)
		} else {
			seen[np.Name] = i
		}
		if np.Count != nil {
			if *np.Count < 0 {
				add(path+".count", "must not be negative")
			} else {
				totalNodes += int64(*np.Count)
			}
		}
		if np.DiskSizeGB != 0 && np.DiskSizeGB < MinDiskSizeGB {
			add(path+".diskSizeGB", "must be at least %d", MinDiskSizeGB)
		}
	}
	if len(s.NodePools) > 0 && totalNodes == 0 {
		add("spec.nodePools", "the cluster needs at least one node in total")
	}

	if len(s.PlatformConfig) > 0 {
		trimmed := bytes.TrimSpace(s.PlatformConfig)
		if !bytes.Equal(trimmed, []byte("null")) && (len(trimmed) == 0 || trimmed[0] != '{') {
			add("spec.platformConfig", "must be a set of key: value settings")
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func validateName(name string) string {
	switch {
	case name == "":
		return "is required"
	case len(name) > MaxNameLength:
		return fmt.Sprintf("%q is %d characters; the maximum is %d", name, len(name), MaxNameLength)
	case !namePattern.MatchString(name):
		return fmt.Sprintf("%q must use lowercase letters, digits and '-', start with a letter and end with a letter or digit", name)
	}
	return ""
}
