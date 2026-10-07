package clustermeta

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/gofrs/flock"
	"sigs.k8s.io/yaml"
)

const (
	// HomeEnv overrides the default ~/.tuggy location.
	HomeEnv = "TUGGY_HOME"

	recordFile = "record.yaml"
	dirPerm    = 0o700 // owner only: OpenTofu state can contain secrets
	filePerm   = 0o600
)

// DefaultRoot returns $TUGGY_HOME, or .tuggy in the user's home directory.
func DefaultRoot() (string, error) {
	if dir := os.Getenv(HomeEnv); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory (set %s to choose a location): %w", HomeEnv, err)
	}
	return filepath.Join(home, ".tuggy"), nil
}

// Local is a Store on the local file system:
//
//	<root>/clusters/<name>/record.yaml   the record
//	<root>/clusters/<name>/...           platform files
//	<root>/locks/<name>.lock             per-cluster lock
//	<root>/logs/<name>/                  logs of deleted clusters
//
// Locks live outside the cluster directory so a cluster can be locked before
// its directory exists and while the directory is being removed.
type Local struct {
	root string
}

var _ Store = (*Local)(nil)

// NewLocal returns a Store rooted at root. Directories are created on demand.
func NewLocal(root string) *Local {
	return &Local{root: root}
}

// Root returns the store's root directory.
func (s *Local) Root() string { return s.root }

// Dir returns the directory where a platform keeps its files for a cluster.
func (s *Local) Dir(name string) string {
	return filepath.Join(s.root, "clusters", name)
}

func (s *Local) recordPath(name string) string {
	return filepath.Join(s.Dir(name), recordFile)
}

// Get returns the record for name, or ErrNotFound.
func (s *Local) Get(name string) (*Record, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return s.read(s.recordPath(name), name)
}

func (s *Local) read(path, name string) (*Record, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is built from a validated name
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("reading record for cluster %q (%s): %w", name, path, err)
	}
	if r.Name != name {
		return nil, fmt.Errorf("record %s is for cluster %q, expected %q", path, r.Name, name)
	}
	return &r, nil
}

// List returns every record, sorted by name. Directories without a record,
// for example left by an interrupted create, are skipped.
func (s *Local) List() ([]*Record, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "clusters"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var records []*Record
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		r, err := s.read(s.recordPath(e.Name()), e.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, nil
}

// Create saves a new record and creates the cluster directory.
func (s *Local) Create(r *Record) error {
	if !ValidName(r.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, r.Name)
	}
	if _, err := os.Stat(s.recordPath(r.Name)); err == nil {
		return fmt.Errorf("%w: %q", ErrExists, r.Name)
	}
	// MkdirAll also accepts a directory left without a record by a crash
	// between creating it and writing the record.
	if err := os.MkdirAll(s.Dir(r.Name), dirPerm); err != nil {
		return err
	}
	return s.write(r)
}

// Update saves an existing record.
func (s *Local) Update(r *Record) error {
	if _, err := s.Get(r.Name); err != nil {
		return err
	}
	return s.write(r)
}

func (s *Local) write(r *Record) error {
	data, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.recordPath(r.Name), data)
}

// Delete removes a cluster's record and directory. Logs move to
// <root>/logs/<name>/ so the history of deleted clusters is kept.
func (s *Local) Delete(name string) error {
	r, err := s.Get(name)
	if err != nil {
		return err
	}
	if r.Status != StatusDeleting {
		return fmt.Errorf("cluster %q is %s; only a cluster being deleted can be removed from the store", name, r.Status)
	}

	if err := s.keepLogs(name); err != nil {
		return fmt.Errorf("keeping logs of cluster %q: %w", name, err)
	}
	return os.RemoveAll(s.Dir(name))
}

func (s *Local) keepLogs(name string) error {
	src := filepath.Join(s.Dir(name), "logs")
	entries, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dst := filepath.Join(s.root, "logs", name)
	if err := os.MkdirAll(dst, dirPerm); err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Lock gives the caller exclusive use of a cluster. The operating system
// releases the lock if the process exits, so a crash never leaves a cluster
// locked.
func (s *Local) Lock(name string) (func() error, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	dir := filepath.Join(s.root, "locks")
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}

	lock := flock.New(filepath.Join(dir, name+".lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("locking cluster %q: %w", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrLocked, name)
	}
	return lock.Unlock, nil
}

// writeFileAtomic writes data to a temporary file and renames it over path,
// so readers never see a half-written file.
func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), filePerm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
