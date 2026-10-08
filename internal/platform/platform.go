// Package platform defines how tuggy talks to the things that build clusters.
//
// A Platform knows how to build one kind of cluster, such as GKE or bare
// metal. The CLI is written only against the Platform interface and looks
// platforms up by name in a Registry, so adding a platform never changes the
// CLI. This is a tuggy concept and unrelated to OpenTofu providers, which a
// platform may use inside its OpenTofu module.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/spf13/pflag"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

// ErrNotSupported is returned by optional operations a platform doesn't offer.
var ErrNotSupported = errors.New("not supported by this platform")

// Platform builds and manages one kind of cluster.
type Platform interface {
	// Name is the value users pass to --platform, for example "gke".
	Name() string

	// FromV1Alpha1 translates the spec file format (or flags) into a Plan
	// with this platform's own typed variables, validating platform-specific
	// settings. Other input formats get their own translation methods.
	FromV1Alpha1(c *v1alpha1.Cluster) (*Plan, error)

	// Preflight checks that everything needed is in place before any change
	// is made: credentials, permissions, tools. Used by create and doctor.
	Preflight(ctx context.Context, p *Plan) []CheckResult

	// Create builds the cluster described by p in opts.Dir and returns its
	// details, including outputs to keep in the cluster's record.
	Create(ctx context.Context, p *Plan, opts CreateOptions) (*ClusterInfo, error)

	// Delete removes every resource of the cluster described by rec.
	Delete(ctx context.Context, rec *clustermeta.Record, opts DeleteOptions) error

	// Describe reports the cluster's details from the platform's own files
	// in dir, such as its variables file.
	Describe(ctx context.Context, rec *clustermeta.Record, dir string) (*ClusterInfo, error)

	// Kubeconfig returns the cluster, user and context entries to merge into
	// the user's kubeconfig.
	Kubeconfig(ctx context.Context, rec *clustermeta.Record) (*clientcmdapi.Config, error)

	// ListRemote lists clusters that exist on the platform but were not
	// created by tuggy. Platforms without such a listing return
	// ErrNotSupported.
	ListRemote(ctx context.Context, opts ListOptions) ([]ClusterInfo, error)
}

// FlagBinder is implemented by platforms that accept their own command-line
// flags, such as --project for GKE. The flags fill spec.platformConfig, so
// flags and spec files go through the same translation.
type FlagBinder interface {
	// AddFlags registers the platform's flags.
	AddFlags(fs *pflag.FlagSet)

	// PlatformConfigFromFlags returns spec.platformConfig built from the
	// flags the user set.
	PlatformConfigFromFlags(fs *pflag.FlagSet) (json.RawMessage, error)
}

// Plan is what a platform will build.
type Plan struct {
	// Name of the cluster.
	Name string

	// Platform that made this plan.
	Platform string

	// TTL until the cluster is reported as expired. Zero means never.
	TTL time.Duration

	// Variables are the platform's own typed inputs, for example
	// gke.Variables. Only the platform that made the plan reads them.
	Variables any
}

// CreateOptions control Create.
type CreateOptions struct {
	// Dir is the cluster's directory, where the platform keeps its files.
	Dir string

	// CacheDir is a directory shared by all clusters for downloads, such as
	// OpenTofu providers. Empty disables caching.
	CacheDir string

	// DryRun plans the cluster and reports the planned changes in
	// ClusterInfo.Changes without building anything.
	DryRun bool

	// Log receives the full output of the operation.
	Log io.Writer

	// OnProgress, if set, receives progress as resources are created.
	OnProgress func(Progress)
}

// DeleteOptions control Delete.
type DeleteOptions struct {
	// Dir is the cluster's directory.
	Dir string

	// CacheDir is the shared download directory; see CreateOptions.
	CacheDir string

	// DryRun reports what would be removed without removing anything.
	DryRun bool

	// Confirm, if set, is called with what will be removed before anything
	// is. Returning an error stops the delete with that error.
	Confirm func(Changes) error

	// Log receives the full output of the operation.
	Log io.Writer

	// OnProgress, if set, receives progress as resources are removed.
	OnProgress func(Progress)
}

// Changes summarizes what an operation will change.
type Changes struct {
	Add    int
	Change int
	Remove int

	// Resources lists each resource change.
	Resources []ResourceChange
}

// ResourceChange is one planned change.
type ResourceChange struct {
	// Address identifies the resource, for example "google_container_cluster.this".
	Address string
	// Type is the kind of resource, for example "google_container_cluster".
	Type string
	// Action is create, update, delete, replace or read.
	Action string
}

// ProgressKind says what a Progress update reports.
type ProgressKind string

// Progress kinds.
const (
	ProgressStarted ProgressKind = "started" // a resource change started
	ProgressRunning ProgressKind = "running" // still in progress
	ProgressDone    ProgressKind = "done"    // finished
	ProgressFailed  ProgressKind = "failed"  // failed
	ProgressInfo    ProgressKind = "info"    // a step of the operation, such as "Planning"
)

// Progress is one update during a long operation.
type Progress struct {
	Kind         ProgressKind
	Resource     string
	ResourceType string
	Action       string
	Elapsed      time.Duration
	Message      string
}

// ListOptions control ListRemote.
type ListOptions struct{}

// ClusterInfo describes a cluster for display and for the record.
type ClusterInfo struct {
	Name              string
	Platform          string
	Location          string
	KubernetesVersion string
	NodePools         []NodePoolInfo

	// Outputs are values known only after creation, such as the API
	// endpoint. Create's outputs are saved in the cluster's record.
	Outputs map[string]string

	// Changes is set by a dry-run Create: what would be built.
	Changes *Changes
}

// NodePoolInfo describes one node pool.
type NodePoolInfo struct {
	Name        string
	MachineType string
	Count       int
}

// CheckStatus is the result of one preflight check.
type CheckStatus string

// Preflight check results.
const (
	CheckPass CheckStatus = "pass"
	CheckWarn CheckStatus = "warn" // worth knowing, but doesn't block
	CheckFail CheckStatus = "fail" // blocks the operation
)

// CheckResult is the outcome of one preflight check.
type CheckResult struct {
	// Name says what was checked, for example "Docker is running".
	Name   string
	Status CheckStatus

	// Message gives details, for example the account found.
	Message string

	// Fix tells the user how to resolve a failure.
	Fix string
}

// Failed reports whether any check blocks the operation.
func Failed(results []CheckResult) bool {
	for _, r := range results {
		if r.Status == CheckFail {
			return true
		}
	}
	return false
}
