package clustermeta

import (
	"errors"
	"regexp"
)

// Errors returned by a Store. Check them with errors.Is.
var (
	ErrNotFound    = errors.New("cluster not found")
	ErrExists      = errors.New("cluster already exists")
	ErrLocked      = errors.New("another tuggy command is working on this cluster")
	ErrInvalidName = errors.New("invalid cluster name")
)

// Store saves cluster records and serializes commands working on the same
// cluster. The local implementation keeps everything under ~/.tuggy; a
// shared implementation (GCS, S3) can be added later for teams.
type Store interface {
	// Get returns the record for name, or ErrNotFound.
	Get(name string) (*Record, error)

	// List returns every record, sorted by name.
	List() ([]*Record, error)

	// Create saves a new record and creates the cluster directory. It returns
	// ErrExists if a record with that name is already saved.
	Create(r *Record) error

	// Update saves an existing record, or returns ErrNotFound.
	Update(r *Record) error

	// Delete removes a cluster's record and directory once its resources are
	// gone. The record must be in StatusDeleting. Logs are kept.
	Delete(name string) error

	// Dir is the directory where a platform keeps its files for a cluster.
	Dir(name string) string

	// Lock gives the caller exclusive use of a cluster until unlock is
	// called, or returns ErrLocked if another process holds it. The lock is
	// released automatically if the process exits.
	Lock(name string) (unlock func() error, err error)
}

// namePattern matches names that are safe to use as directory names on
// every operating system. Spec validation enforces stricter rules.
var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ValidName reports whether name can be stored.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}
