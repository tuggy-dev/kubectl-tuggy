package gke

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
	"github.com/tuggy-dev/kubectl-tuggy/internal/runner/runnertest"
)

// fakeGoogle serves the Google APIs preflight calls.
type fakeGoogle struct {
	mu            sync.Mutex
	granted       map[string][]string // token -> permissions
	apiState      string              // "ENABLED", "DISABLED", or "" for 403
	impersonateOK bool
	tokensSeen    []string
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	f.tokensSeen = append(f.tokensSeen, token)
	f.mu.Unlock()

	switch {
	case strings.HasSuffix(r.URL.Path, ":testIamPermissions"):
		var req struct{ Permissions []string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		var out []string
		for _, p := range req.Permissions {
			if slices.Contains(f.granted[token], p) {
				out = append(out, p)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"permissions": out})
	case strings.Contains(r.URL.Path, "/services/container.googleapis.com"):
		if f.apiState == "" {
			http.Error(w, `{"error":{"message":"Permission denied to get service"}}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": f.apiState})
	case strings.HasSuffix(r.URL.Path, ":generateAccessToken"):
		if !f.impersonateOK {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"Permission 'iam.serviceAccounts.getAccessToken' denied"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "sa-token"})
	default:
		http.NotFound(w, r)
	}
}

type preflightHarness struct {
	p      *Platform
	google *fakeGoogle
	runner *runnertest.Fake
}

func newPreflightHarness(t *testing.T) *preflightHarness {
	t.Helper()
	g := &fakeGoogle{
		granted:  map[string][]string{"user-token": requiredPermissions, "sa-token": requiredPermissions},
		apiState: "ENABLED",
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)

	r := runnertest.New()
	p := testPlatform()
	p.NewRunner = func() (runner.Runner, error) { return r, nil }
	credsPath := writeCredentials(t, userCredentials)
	p.CredentialsPath = func() string { return credsPath }
	p.LookPath = func(string) (string, error) { return "/usr/local/bin/gke-gcloud-auth-plugin", nil }
	p.api = &googleAPI{
		ResourceManagerURL: srv.URL, ServiceUsageURL: srv.URL, IAMCredentialsURL: srv.URL,
		HTTPClient: srv.Client(),
		TokenSource: func(context.Context, *Credentials) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "user-token"}), nil
		},
	}
	return &preflightHarness{p: p, google: g, runner: r}
}

func (h *preflightHarness) run(t *testing.T, spec string) []platform.CheckResult {
	t.Helper()
	plan, err := h.p.FromV1Alpha1(loadSpec(t, spec))
	if err != nil {
		t.Fatal(err)
	}
	return h.p.Preflight(context.Background(), plan)
}

func find(results []platform.CheckResult, prefix string) *platform.CheckResult {
	for i := range results {
		if strings.HasPrefix(results[i].Name, prefix) {
			return &results[i]
		}
	}
	return nil
}

func TestPreflightAllPass(t *testing.T) {
	h := newPreflightHarness(t)
	results := h.run(t, minimalSpec)
	if platform.Failed(results) {
		t.Fatalf("unexpected failure: %+v", results)
	}
	for _, name := range []string{"Container engine", "gke-gcloud-auth-plugin", "Google credentials", "Google sign-in", "Kubernetes Engine API in acme-dev", "Permissions in acme-dev"} {
		if r := find(results, name); r == nil || r.Status != platform.CheckPass {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestPreflightNoCredentialsStops(t *testing.T) {
	h := newPreflightHarness(t)
	h.p.CredentialsPath = func() string { return "/nonexistent/adc.json" }
	results := h.run(t, minimalSpec)

	r := find(results, "Google credentials")
	if r == nil || r.Status != platform.CheckFail || r.Fix != "Run: gcloud auth application-default login" {
		t.Errorf("credentials check = %+v", r)
	}
	if find(results, "Permissions") != nil {
		t.Error("checks needing credentials should not run")
	}
}

func TestPreflightEngineDown(t *testing.T) {
	h := newPreflightHarness(t)
	h.runner.PingErr = errors.New("cannot reach Docker at unix:///var/run/docker.sock")
	r := find(h.run(t, minimalSpec), "Container engine")
	if r == nil || r.Status != platform.CheckFail || !strings.Contains(r.Fix, "docker ps") {
		t.Errorf("engine check = %+v", r)
	}
}

func TestPreflightMissingAuthPluginWarns(t *testing.T) {
	h := newPreflightHarness(t)
	h.p.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	results := h.run(t, minimalSpec)
	r := find(results, "gke-gcloud-auth-plugin")
	if r == nil || r.Status != platform.CheckWarn || !strings.Contains(r.Fix, "gcloud components install") {
		t.Errorf("plugin check = %+v", r)
	}
	if platform.Failed(results) {
		t.Error("a missing plugin should warn, not block creation")
	}
}

func TestPreflightTokenFailure(t *testing.T) {
	h := newPreflightHarness(t)
	h.p.api.TokenSource = func(context.Context, *Credentials) (oauth2.TokenSource, error) {
		return failingTokenSource{errors.New(`oauth2: "invalid_grant" "reauth related error (invalid_rapt)"`)}, nil
	}
	results := h.run(t, minimalSpec)
	r := find(results, "Google sign-in")
	if r == nil || r.Status != platform.CheckFail || !strings.HasPrefix(r.Message, "your Google sign-in has expired") {
		t.Errorf("sign-in check = %+v", r)
	}
	if find(results, "Permissions") != nil {
		t.Error("checks needing a token should not run")
	}
}

func TestPreflightAPIDisabled(t *testing.T) {
	h := newPreflightHarness(t)
	h.google.apiState = "DISABLED"
	r := find(h.run(t, minimalSpec), "Kubernetes Engine API")
	if r == nil || r.Status != platform.CheckFail || r.Fix != "Run: gcloud services enable container.googleapis.com --project acme-dev" {
		t.Errorf("API check = %+v", r)
	}
}

func TestPreflightAPICannotBeChecked(t *testing.T) {
	h := newPreflightHarness(t)
	h.google.apiState = "" // 403
	results := h.run(t, minimalSpec)
	if r := find(results, "Kubernetes Engine API"); r == nil || r.Status != platform.CheckWarn {
		t.Errorf("API check = %+v, want a warning", r)
	}
	if platform.Failed(results) {
		t.Error("not being able to read services should not block")
	}
}

func TestPreflightMissingPermissions(t *testing.T) {
	h := newPreflightHarness(t)
	h.google.granted["user-token"] = []string{"container.clusters.get"}
	r := find(h.run(t, minimalSpec), "Permissions in acme-dev")
	if r == nil || r.Status != platform.CheckFail {
		t.Fatalf("permissions check = %+v", r)
	}
	if !strings.Contains(r.Message, "container.clusters.create") || strings.Contains(r.Message, "container.clusters.get,") {
		t.Errorf("message should list only missing permissions: %q", r.Message)
	}
	if !strings.Contains(r.Fix, "roles/container.admin") || !strings.Contains(r.Fix, "roles/iam.serviceAccountUser") {
		t.Errorf("fix = %q", r.Fix)
	}
}

const impersonatingSpec = `
apiVersion: tuggy.dev/v1alpha1
kind: Cluster
metadata: {name: dev}
spec:
  platform: gke
  platformConfig:
    project: acme-dev
    location: us-central1-a
    impersonateServiceAccount: opentofu@acme-dev.iam.gserviceaccount.com
`

func TestPreflightImpersonation(t *testing.T) {
	h := newPreflightHarness(t)
	h.google.impersonateOK = true
	h.google.granted["user-token"] = nil // the user can't create clusters; the service account can
	results := h.run(t, impersonatingSpec)
	if platform.Failed(results) {
		t.Fatalf("unexpected failure: %+v", results)
	}
	if r := find(results, "Impersonate opentofu@acme-dev.iam.gserviceaccount.com"); r == nil || r.Status != platform.CheckPass {
		t.Errorf("impersonation check = %+v", r)
	}
	if last := h.google.tokensSeen[len(h.google.tokensSeen)-1]; last != "sa-token" {
		t.Errorf("permissions were checked with %q, want the service account's token", last)
	}
}

func TestPreflightImpersonationDenied(t *testing.T) {
	h := newPreflightHarness(t)
	results := h.run(t, impersonatingSpec)
	r := find(results, "Impersonate")
	if r == nil || r.Status != platform.CheckFail || !strings.Contains(r.Fix, "roles/iam.serviceAccountTokenCreator") {
		t.Errorf("impersonation check = %+v", r)
	}
}

type failingTokenSource struct{ err error }

func (f failingTokenSource) Token() (*oauth2.Token, error) { return nil, f.err }

func TestPreflightCredentialsUnusable(t *testing.T) {
	h := newPreflightHarness(t)
	h.p.api.TokenSource = func(context.Context, *Credentials) (oauth2.TokenSource, error) {
		return nil, errors.New("missing client_id")
	}
	r := find(h.run(t, minimalSpec), "Google sign-in")
	if r == nil || r.Status != platform.CheckFail || !strings.Contains(r.Message, "missing client_id") {
		t.Errorf("sign-in check = %+v", r)
	}
}
