package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

type deleteOptions struct {
	yes     bool
	dryRun  bool
	expired bool
	verbose bool
}

func newDeleteCommand(app *App) *cobra.Command {
	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete a resource",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	var opts deleteOptions
	cluster := &cobra.Command{
		Use:   "cluster NAME",
		Short: "Delete a cluster and everything tuggy built for it",
		Long: `Delete a cluster. tuggy shows exactly what will be removed and asks before
removing anything (--yes skips the question). Only the named cluster's own
resources are touched.`,
		Example: `  kubectl tuggy delete cluster dev
  kubectl tuggy delete cluster dev --dry-run`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.deleteCluster(cmd.Context(), args[0], opts)
		},
	}
	addDeleteFlags(cluster, &opts)

	var all deleteOptions
	clusters := &cobra.Command{
		Use:     "clusters --expired",
		Short:   "Delete every cluster whose TTL has passed",
		Example: `  kubectl tuggy delete clusters --expired`,
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !all.expired {
				return usageError(cmd, errors.New("--expired is required; to delete one cluster use: kubectl tuggy delete cluster NAME"))
			}
			return app.deleteExpired(cmd.Context(), all)
		},
	}
	addDeleteFlags(clusters, &all)
	clusters.Flags().BoolVar(&all.expired, "expired", false, "delete clusters whose TTL has passed")

	del.AddCommand(cluster, clusters)
	return del
}

func addDeleteFlags(cmd *cobra.Command, opts *deleteOptions) {
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "show what would be removed without removing anything")
	cmd.Flags().BoolVarP(&opts.verbose, "verbose", "v", false, "show OpenTofu's full output")
}

func (a *App) deleteCluster(ctx context.Context, name string, opts deleteOptions) error {
	unlock, err := a.lock(name)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	rec, err := a.Store.Get(name)
	if errors.Is(err, clustermeta.ErrNotFound) {
		return fmt.Errorf("cluster %q not found; see: kubectl tuggy get clusters", name)
	}
	if err != nil {
		return err
	}
	if err := a.interrupted(rec); err != nil {
		return err
	}
	p, err := a.Platforms.Get(rec.Platform)
	if err != nil {
		return err
	}

	logFile, err := a.openLog(name, "delete")
	if err != nil {
		return err
	}
	defer logFile.Close()
	var log io.Writer = logFile
	if opts.verbose {
		log = io.MultiWriter(logFile, a.Streams.ErrOut)
	}

	confirmed := false
	err = p.Delete(ctx, rec, platform.DeleteOptions{
		Dir:        a.Store.Dir(name),
		CacheDir:   a.cacheDir(),
		DryRun:     opts.dryRun,
		Log:        log,
		OnProgress: a.progressPrinter(opts.verbose),
		Confirm: func(c platform.Changes) error {
			a.describeRemoval(rec, c)
			if opts.dryRun {
				return nil
			}
			if err := a.confirm(fmt.Sprintf("Delete cluster %s?", name), opts.yes); err != nil {
				return err
			}
			confirmed = true
			if err := rec.Transition(clustermeta.StatusDeleting, "", a.Now()); err != nil {
				return err
			}
			return a.Store.Update(rec)
		},
	})
	switch {
	case opts.dryRun && err == nil:
		a.printf("Dry run: nothing was removed.\n")
		return nil
	case err != nil && !confirmed:
		// Declined, or failed before anything was removed: record unchanged.
		return err
	case err != nil:
		msg := err.Error()
		if errors.Is(err, context.Canceled) {
			msg = "cancelled"
		}
		if rec.Transition(clustermeta.StatusFailed, "delete: "+msg, a.Now()) == nil {
			_ = a.Store.Update(rec)
		}
		fmt.Fprintf(a.Streams.ErrOut, "\nCluster %s was not fully deleted. Full log: %s\nRun the same command again to finish.\n", name, logFile.Name())
		return cloudError(err)
	}

	if err := a.Kube.Remove(contextName(name)); err != nil {
		a.warnf("could not remove context %q from your kubeconfig: %v", contextName(name), err)
	}
	// The log lives in the cluster's folder, which is about to be removed.
	// Windows can't move or delete a file that is still open.
	_ = logFile.Close()
	if err := a.Store.Delete(name); err != nil {
		return err
	}
	a.printf("Cluster %s deleted.\n", name)
	return nil
}

// describeRemoval shows what a delete will remove before asking.
func (a *App) describeRemoval(rec *clustermeta.Record, c platform.Changes) {
	a.printf("\nDeleting cluster %s (%s) will remove %d resources:\n", rec.Name, rec.Platform, c.Remove)
	for _, r := range c.Resources {
		if r.Action == "delete" {
			a.printf("  - %s\n", r.Address)
		}
	}
}

func (a *App) deleteExpired(ctx context.Context, opts deleteOptions) error {
	records, err := a.Store.List()
	if err != nil {
		return err
	}
	var expired []*clustermeta.Record
	for _, r := range records {
		if r.Expired(a.Now()) {
			expired = append(expired, r)
		}
	}
	if len(expired) == 0 {
		a.printf("No expired clusters.\n")
		return nil
	}

	names := make([]string, len(expired))
	for i, r := range expired {
		names[i] = r.Name
		a.printf("  %s expired %s ago\n", r.Name, shortDuration(a.Now().Sub(*r.ExpiresAt)))
	}
	if !opts.dryRun {
		if err := a.confirm(fmt.Sprintf("Delete %d expired cluster(s): %s?", len(expired), strings.Join(names, ", ")), opts.yes); err != nil {
			return err
		}
	}

	// One confirmation covers them all.
	each := opts
	each.yes = true
	var failed []string
	for _, r := range expired {
		if err := a.deleteCluster(ctx, r.Name, each); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			fmt.Fprintf(a.Streams.ErrOut, "Error deleting %s: %v\n", r.Name, err)
			failed = append(failed, r.Name)
		}
	}
	if len(failed) > 0 {
		return WithExitCode(ExitCloud, fmt.Errorf("could not delete: %s", strings.Join(failed, ", ")))
	}
	return nil
}
