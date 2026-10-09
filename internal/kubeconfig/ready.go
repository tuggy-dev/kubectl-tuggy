package kubeconfig

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ReadyOptions control WaitReady.
type ReadyOptions struct {
	// Timeout is how long to keep trying. Defaults to 2 minutes.
	Timeout time.Duration

	// Interval is the pause between attempts. Defaults to 5 seconds.
	Interval time.Duration

	// OnAttempt, if set, is told about each failed attempt.
	OnAttempt func(attempt int, err error)
}

// WaitReady connects to the cluster of cfg's current context the way kubectl
// would (including exec plugins such as gke-gcloud-auth-plugin) and waits
// until its API server reports ready on /readyz. It returns the server's
// Kubernetes version. A new cluster can take a short while to accept
// requests after the cloud reports it created, so failures are retried
// until the timeout.
func WaitReady(ctx context.Context, cfg *clientcmdapi.Config, opts ReadyOptions) (string, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Second
	}

	restCfg, err := clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return "", fmt.Errorf("building client for context %q: %w", cfg.CurrentContext, err)
	}
	restCfg.Timeout = 15 * time.Second
	client, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var lastErr error
	for attempt := 1; ; attempt++ {
		version, err := check(ctx, client)
		if err == nil {
			return version, nil
		}
		// An attempt cut short by the overall deadline only says time ran
		// out; keep the earlier attempt's error, which says why the cluster
		// isn't answering (for example an untrusted certificate).
		if lastErr == nil || ctx.Err() == nil {
			lastErr = err
		}
		if opts.OnAttempt != nil {
			opts.OnAttempt(attempt, err)
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("cluster API server at %s did not become ready within %s: %w", restCfg.Host, opts.Timeout, lastErr)
			}
			return "", ctx.Err()
		case <-time.After(opts.Interval):
		}
	}
}

func check(ctx context.Context, client *discovery.DiscoveryClient) (string, error) {
	body, err := client.RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
	switch {
	case apierrors.IsUnauthorized(err):
		return "", fmt.Errorf("/readyz: the cluster rejected the credentials (Unauthorized): %w", err)
	case apierrors.IsForbidden(err):
		return "", fmt.Errorf("/readyz: the credentials are not allowed to read the cluster (Forbidden): %w", err)
	case err != nil:
		return "", fmt.Errorf("/readyz: %w", err)
	}
	if string(body) != "ok" {
		return "", fmt.Errorf("/readyz answered %q", body)
	}
	info, err := client.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("/version: %w", err)
	}
	return info.GitVersion, nil
}
