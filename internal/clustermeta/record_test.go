package clustermeta

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)

func TestNewRecord(t *testing.T) {
	r := NewRecord("dev", "gke", "v0.1.0", 8*time.Hour, now)
	if r.Status != "" {
		t.Errorf("new record status = %q, want empty until saved", r.Status)
	}
	if r.ExpiresAt == nil || !r.ExpiresAt.Equal(now.Add(8*time.Hour)) {
		t.Errorf("ExpiresAt = %v, want %v", r.ExpiresAt, now.Add(8*time.Hour))
	}

	forever := NewRecord("dev", "gke", "v0.1.0", 0, now)
	if forever.ExpiresAt != nil {
		t.Errorf("ttl 0 should never expire, got ExpiresAt %v", forever.ExpiresAt)
	}
}

func TestTransitions(t *testing.T) {
	all := []Status{"", StatusCreating, StatusReady, StatusFailed, StatusDeleting}
	allowed := map[[2]Status]bool{
		{"", StatusCreating}:           true,
		{StatusCreating, StatusReady}:  true,
		{StatusCreating, StatusFailed}: true,
		{StatusFailed, StatusCreating}: true,
		{StatusFailed, StatusDeleting}: true,
		{StatusReady, StatusDeleting}:  true,
		{StatusDeleting, StatusFailed}: true,
	}

	for _, from := range all {
		for _, to := range all[1:] {
			r := &Record{Name: "dev", Status: from}
			err := r.Transition(to, "msg", now)
			want := allowed[[2]Status{from, to}]
			if want && err != nil {
				t.Errorf("%q -> %s: unexpected error %v", from, to, err)
			}
			if !want && err == nil {
				t.Errorf("%q -> %s: allowed, want error", from, to)
			}
			if want && (r.Status != to || r.Message != "msg" || !r.UpdatedAt.Equal(now)) {
				t.Errorf("%q -> %s: record not updated: %+v", from, to, r)
			}
			if !want && r.Status != from {
				t.Errorf("%q -> %s: status changed to %s despite error", from, to, r.Status)
			}
		}
	}
}

func TestTransitionErrorMessage(t *testing.T) {
	r := &Record{Name: "dev", Status: StatusReady}
	err := r.Transition(StatusCreating, "", now)
	if err == nil || err.Error() != `cluster "dev" cannot go from Ready to Creating` {
		t.Errorf("got %v", err)
	}
}

func TestExpired(t *testing.T) {
	r := NewRecord("dev", "gke", "v0.1.0", time.Hour, now)
	if r.Expired(now.Add(59 * time.Minute)) {
		t.Error("expired before its TTL")
	}
	if !r.Expired(now.Add(time.Hour)) {
		t.Error("not expired at its TTL")
	}
	if NewRecord("dev", "gke", "v0.1.0", 0, now).Expired(now.Add(1000 * time.Hour)) {
		t.Error("a cluster without TTL expired")
	}
}
