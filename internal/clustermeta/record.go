// Package clustermeta is the cluster metadata store: tuggy's own record of
// each cluster it manages, plus the cluster's folder, lock and logs under
// ~/.tuggy. It does not hold OpenTofu state or platform files; platforms keep
// those in the cluster's folder themselves.
//
// tuggy has no server, so every command starts knowing nothing. A Record is
// how later commands learn what earlier ones did: which platform built a
// cluster, whether the last operation finished, when it expires, and the
// outputs needed to reach it. A Record holds only tuggy's own facts; what the
// user asked for lives in the platform's own files in the cluster directory.
package clustermeta

import (
	"fmt"
	"slices"
	"time"
)

// Status is where a cluster is in its lifecycle.
type Status string

// Cluster statuses. See docs/design/0001-architecture.md for the state diagram.
const (
	StatusCreating Status = "Creating"
	StatusReady    Status = "Ready"
	StatusFailed   Status = "Failed"
	StatusDeleting Status = "Deleting"
)

// transitions lists the statuses each status may move to. The empty status
// is a record that has not been saved yet. A Deleting record that finishes is
// removed with Store.Delete rather than moved to another status.
var transitions = map[Status][]Status{
	"":             {StatusCreating},
	StatusCreating: {StatusReady, StatusFailed},
	StatusFailed:   {StatusCreating, StatusDeleting},
	StatusReady:    {StatusDeleting},
	StatusDeleting: {StatusFailed},
}

// Record is what tuggy knows about one cluster. It is stored as record.yaml
// in the cluster's directory.
type Record struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Status   Status `json:"status"`

	// Message explains the current status, for example why the last
	// operation failed.
	Message string `json:"message,omitempty"`

	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`

	// TuggyVersion is the tuggy release that built the cluster, and so the
	// version of the platform module in the cluster directory.
	TuggyVersion string `json:"tuggyVersion"`

	// Outputs are values known only after creation, such as the API server
	// endpoint and CA certificate. Keys are defined by the platform.
	Outputs map[string]string `json:"outputs,omitempty"`
}

// NewRecord returns an unsaved record for a cluster about to be created.
// A zero ttl means the cluster never expires.
func NewRecord(name, platform, tuggyVersion string, ttl time.Duration, now time.Time) *Record {
	r := &Record{
		Name:         name,
		Platform:     platform,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
		TuggyVersion: tuggyVersion,
	}
	if ttl > 0 {
		expires := now.UTC().Add(ttl)
		r.ExpiresAt = &expires
	}
	return r
}

// CanTransition reports whether the record may move to status to.
func (r *Record) CanTransition(to Status) bool {
	return slices.Contains(transitions[r.Status], to)
}

// Transition moves the record to status to with an explanatory message, or
// returns an error if that move is not allowed. It does not save the record.
func (r *Record) Transition(to Status, message string, now time.Time) error {
	if !r.CanTransition(to) {
		from := r.Status
		if from == "" {
			from = "new"
		}
		return fmt.Errorf("cluster %q cannot go from %s to %s", r.Name, from, to)
	}
	r.Status = to
	r.Message = message
	r.UpdatedAt = now.UTC()
	return nil
}

// Expired reports whether the cluster's TTL has passed.
func (r *Record) Expired(now time.Time) bool {
	return r.ExpiresAt != nil && !now.Before(*r.ExpiresAt)
}
