package gke

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/runnertest"
)

// fakeGKE serves the GKE node pool and operation calls made when cleaning up
// GKE's default node pool.
type fakeGKE struct {
	mu        sync.Mutex
	pools     []string
	deleteErr int // HTTP status that deletes fail with; 0 means they succeed
	deleted   []string
	requests  int
	polls     int
}

func (f *fakeGKE) set(pools []string, deleteErr int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pools, f.deleteErr = pools, deleteErr
}

func (f *fakeGKE) snapshot() (deleted []string, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted), f.requests
}

func (f *fakeGKE) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/nodePools"):
		type pool struct {
			Name string `json:"name"`
		}
		var out struct {
			NodePools []pool `json:"nodePools"`
		}
		for _, name := range f.pools {
			out.NodePools = append(out.NodePools, pool{Name: name})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/nodePools/"):
		if f.deleteErr != 0 {
			w.WriteHeader(f.deleteErr)
			_, _ = w.Write([]byte(`{"error":{"message":"permission denied"}}`))
			return
		}
		name := path.Base(r.URL.Path)
		f.deleted = append(f.deleted, name)
		f.pools = slices.DeleteFunc(f.pools, func(p string) bool { return p == name })
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "op-delete"})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/operations/op-delete"):
		f.polls++
		status := "RUNNING"
		if f.polls > 1 {
			status = "DONE"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	default:
		http.NotFound(w, r)
	}
}

func newFakeGKEAPI(t *testing.T, f *fakeGKE) *googleAPI {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &googleAPI{
		ContainerURL: srv.URL,
		HTTPClient:   srv.Client(),
		TokenSource: func(context.Context, *Credentials) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "user-token"}), nil
		},
	}
}

// fastPolling makes waiting for GKE operations immediate.
func fastPolling(t *testing.T) {
	t.Helper()
	old := operationPollInterval
	operationPollInterval = time.Millisecond
	t.Cleanup(func() { operationPollInterval = old })
}

func createResults(t *testing.T) []runnertest.Result {
	t.Helper()
	return []runnertest.Result{
		{}, // init
		{Stdout: fixture(t, "plan-create.jsonl"), ExitCode: 2}, // plan
		{Stdout: fixture(t, "apply-create.jsonl")},             // apply
		{Stdout: fixture(t, "output.json")},                    // output
	}
}

func (h *lifecycleHarness) create(t *testing.T, plan *platform.Plan) ([]string, error) {
	t.Helper()
	var infos []string
	_, err := h.p.Create(context.Background(), plan, platform.CreateOptions{
		Dir: h.dir, CacheDir: filepath.Join(t.TempDir(), "cache"), Log: h.log,
		OnProgress: func(p platform.Progress) {
			if p.Kind == platform.ProgressInfo {
				infos = append(infos, p.Message)
			}
		},
	})
	return infos, err
}

// An interrupted first create leaves GKE's default pool behind; the next
// create removes it.
func TestCreateRemovesLeftoverDefaultPool(t *testing.T) {
	fastPolling(t)
	h := newLifecycleHarness(t, createResults(t)...)
	h.gke.set([]string{"default-pool", "default"}, 0)

	infos, err := h.create(t, h.plan(t))
	if err != nil {
		t.Fatalf("Create: %v\n%s", err, h.log)
	}
	if deleted, _ := h.gke.snapshot(); !slices.Equal(deleted, []string{"default-pool"}) {
		t.Errorf("deleted node pools = %v, want [default-pool]", deleted)
	}
	if !slices.Contains(infos, "Removing GKE's leftover node pool default-pool") {
		t.Errorf("progress = %v, want a message about removing default-pool", infos)
	}
}

func TestCreateWithoutLeftoverPoolDeletesNothing(t *testing.T) {
	h := newLifecycleHarness(t, createResults(t)...)

	if _, err := h.create(t, h.plan(t)); err != nil {
		t.Fatalf("Create: %v\n%s", err, h.log)
	}
	if deleted, requests := h.gke.snapshot(); len(deleted) != 0 || requests != 1 {
		t.Errorf("deleted = %v after %d requests, want nothing deleted after 1 request", deleted, requests)
	}
}

func TestCreateKeepsDefaultPoolTheUserAskedFor(t *testing.T) {
	h := newLifecycleHarness(t, createResults(t)...)
	h.gke.set([]string{"default-pool"}, 0)
	plan := h.plan(t)
	vars, ok := plan.Variables.(Variables)
	if !ok {
		t.Fatalf("plan variables = %T", plan.Variables)
	}
	vars.NodePools[0].Name = gkeDefaultPool
	plan.Variables = vars

	if _, err := h.create(t, plan); err != nil {
		t.Fatalf("Create: %v\n%s", err, h.log)
	}
	if _, requests := h.gke.snapshot(); requests != 0 {
		t.Errorf("made %d requests to GKE, want none when the spec has a pool named %s", requests, gkeDefaultPool)
	}
}

func TestCreateFailsWhenLeftoverPoolCannotBeRemoved(t *testing.T) {
	h := newLifecycleHarness(t, createResults(t)...)
	h.gke.set([]string{"default-pool", "default"}, http.StatusForbidden)

	_, err := h.create(t, h.plan(t))
	if err == nil || !strings.Contains(err.Error(), "removing GKE's default node pool") {
		t.Errorf("Create error = %v, want one about removing GKE's default node pool", err)
	}
}
