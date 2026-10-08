package gke

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// requiredPermissions are what creating, updating and deleting a cluster
// needs. iam.serviceAccounts.actAs is for the node pools' service account.
var requiredPermissions = []string{
	"container.clusters.create",
	"container.clusters.get",
	"container.clusters.update",
	"container.clusters.delete",
	"container.operations.get",
	"iam.serviceAccounts.actAs",
}

const authPlugin = "gke-gcloud-auth-plugin"

// Preflight checks, before anything is created, that the container engine
// works, credentials are present and valid, the Kubernetes Engine API is
// enabled, and the caller has the permissions needed. Each failure carries a
// fix. Checks that depend on a failed one are not run.
func (p *Platform) Preflight(ctx context.Context, plan *platform.Plan) []platform.CheckResult {
	vars, ok := plan.Variables.(Variables)
	if !ok {
		return []platform.CheckResult{{Name: "GKE plan", Status: platform.CheckFail, Message: "plan was not made by the gke platform"}}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var results []platform.CheckResult
	add := func(r platform.CheckResult) { results = append(results, r) }

	add(p.checkEngine(ctx))
	add(p.checkAuthPlugin())

	creds, err := loadCredentials(p.CredentialsPath())
	if err != nil {
		fix := "Run: gcloud auth application-default login"
		if !errors.Is(err, errNoCredentials) {
			fix = "Fix or remove the file, then run: gcloud auth application-default login"
		}
		add(fail("Google credentials", err.Error(), fix))
		return results
	}
	credResult := pass("Google credentials", creds.describe())
	if creds.Type == google.ExternalAccount {
		credResult.Status = platform.CheckWarn
		credResult.Message += "; workload identity federation files may refer to files that are not available inside the OpenTofu container"
	}
	add(credResult)

	token, result := p.token(ctx, creds)
	add(result)
	if token == "" {
		return results
	}

	who := "your account"
	if sa := vars.ImpersonateServiceAccount; sa != nil {
		saToken, err := p.api.impersonate(ctx, token, *sa)
		if err != nil {
			add(fail("Impersonate "+*sa, err.Error(),
				fmt.Sprintf("Grant your account roles/iam.serviceAccountTokenCreator on %s", *sa)))
			return results
		}
		add(pass("Impersonate "+*sa, "tuggy will act as this service account"))
		token, who = saToken, *sa
	}

	add(p.checkAPIEnabled(ctx, token, vars.Project))
	add(p.checkPermissions(ctx, token, vars.Project, who))
	return results
}

func (p *Platform) checkEngine(ctx context.Context) platform.CheckResult {
	const name = "Container engine"
	r, err := p.NewRunner()
	if err == nil {
		err = r.Ping(ctx)
	}
	if err != nil {
		return fail(name, err.Error(), "Start Docker Desktop, Colima, Rancher Desktop or another Docker-compatible engine, and check that `docker ps` works")
	}
	return pass(name, "reachable")
}

func (p *Platform) checkAuthPlugin() platform.CheckResult {
	const name = authPlugin
	if path, err := p.LookPath(authPlugin); err == nil {
		return pass(name, path)
	}
	return platform.CheckResult{
		Name:    name,
		Status:  platform.CheckWarn,
		Message: "not found on PATH; kubectl needs it to connect to GKE clusters",
		Fix:     "Run: gcloud components install gke-gcloud-auth-plugin",
	}
}

func (p *Platform) token(ctx context.Context, creds *Credentials) (string, platform.CheckResult) {
	const name = "Google sign-in"
	fix := "Run: gcloud auth application-default login"
	ts, err := p.api.TokenSource(ctx, creds)
	if err != nil {
		return "", fail(name, err.Error(), fix)
	}
	tok, err := ts.Token()
	if err != nil {
		msg := "could not get an access token: " + err.Error()
		if strings.Contains(err.Error(), "invalid_grant") {
			// Expired or revoked, including organization policies that make
			// users sign in again periodically (invalid_rapt).
			msg = "your Google sign-in has expired or was revoked (" + err.Error() + ")"
		}
		return "", fail(name, msg, fix)
	}
	return tok.AccessToken, pass(name, "access token obtained")
}

func (p *Platform) checkAPIEnabled(ctx context.Context, token, project string) platform.CheckResult {
	const service = "container.googleapis.com"
	name := "Kubernetes Engine API in " + project
	enabled, err := p.api.serviceEnabled(ctx, token, project, service)
	var apiErr *apiError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden:
		return platform.CheckResult{Name: name, Status: platform.CheckWarn,
			Message: "could not check whether it is enabled (no permission to read services)"}
	case err != nil:
		return fail(name, err.Error(), "Check that project "+project+" exists and that you can access it")
	case !enabled:
		return fail(name, "not enabled", fmt.Sprintf("Run: gcloud services enable %s --project %s", service, project))
	}
	return pass(name, "enabled")
}

func (p *Platform) checkPermissions(ctx context.Context, token, project, who string) platform.CheckResult {
	name := "Permissions in " + project
	granted, err := p.api.grantedPermissions(ctx, token, project, requiredPermissions)
	if err != nil {
		return fail(name, err.Error(), "Check that project "+project+" exists and that "+who+" can access it")
	}
	var missing []string
	for _, perm := range requiredPermissions {
		if !slices.Contains(granted, perm) {
			missing = append(missing, perm)
		}
	}
	if len(missing) > 0 {
		return fail(name, fmt.Sprintf("%s is missing %s", who, strings.Join(missing, ", ")),
			fmt.Sprintf("Ask a project admin to grant %s roles/container.admin and roles/iam.serviceAccountUser on %s", who, project))
	}
	return pass(name, "can create, update and delete clusters")
}

func pass(name, msg string) platform.CheckResult {
	return platform.CheckResult{Name: name, Status: platform.CheckPass, Message: msg}
}

func fail(name, msg, fix string) platform.CheckResult {
	return platform.CheckResult{Name: name, Status: platform.CheckFail, Message: msg, Fix: fix}
}
