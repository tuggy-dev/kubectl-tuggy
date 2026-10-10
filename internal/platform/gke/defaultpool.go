package gke

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// gkeDefaultPool is the node pool GKE creates with every cluster. The module
// asks for it to be removed (remove_default_node_pool), but GKE's provider
// does that only at the end of the cluster's first creation. If that creation
// is interrupted, the pool stays, and a resumed create only updates the
// cluster, so the pool would keep running and costing money unnoticed.
const gkeDefaultPool = "default-pool"

// operationPollInterval is how often to check whether GKE has finished
// removing the pool. Tests make it shorter.
var operationPollInterval = 5 * time.Second

// removeLeftoverDefaultPool deletes GKE's default node pool if it is still
// there and the spec doesn't ask for a pool of that name.
func (p *Platform) removeLeftoverDefaultPool(ctx context.Context, vars Variables, onProgress func(platform.Progress)) error {
	for _, np := range vars.NodePools {
		if np.Name == gkeDefaultPool {
			return nil // the user's own pool
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	token, err := p.apiToken(ctx, vars)
	if err != nil {
		return fmt.Errorf("checking for GKE's default node pool: %w", err)
	}
	pools, err := p.api.nodePoolNames(ctx, token, vars.Project, vars.Location, vars.Name)
	if err != nil {
		return fmt.Errorf("checking for GKE's default node pool: %w", err)
	}
	if !slices.Contains(pools, gkeDefaultPool) {
		return nil
	}

	info(onProgress, "Removing GKE's leftover node pool "+gkeDefaultPool)
	op, err := p.api.deleteNodePool(ctx, token, vars.Project, vars.Location, vars.Name, gkeDefaultPool)
	if err != nil {
		return fmt.Errorf("removing GKE's default node pool: %w", err)
	}
	if op == "" {
		return nil
	}
	for {
		done, err := p.api.operationDone(ctx, token, vars.Project, vars.Location, op)
		if err != nil {
			return fmt.Errorf("removing GKE's default node pool: %w", err)
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("removing GKE's default node pool: %w", ctx.Err())
		case <-time.After(operationPollInterval):
		}
	}

}

// apiToken returns an access token for Google's APIs, acting as the
// impersonated service account when the cluster uses one, the same way
// OpenTofu does.
func (p *Platform) apiToken(ctx context.Context, vars Variables) (string, error) {
	creds, err := loadCredentials(p.CredentialsPath())
	if err != nil {
		return "", err
	}
	ts, err := p.api.TokenSource(ctx, creds)
	if err != nil {
		return "", err
	}
	tok, err := ts.Token()
	if err != nil {
		return "", err
	}
	if sa := vars.ImpersonateServiceAccount; sa != nil {
		return p.api.impersonate(ctx, tok.AccessToken, *sa)
	}
	return tok.AccessToken, nil
}
