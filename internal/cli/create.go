package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/kubeconfig"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/version"
)

type createOptions struct {
	spec         specFlags
	dryRun       bool
	noKubeconfig bool
	verbose      bool
}

func newCreateCommand(app *App) *cobra.Command {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a resource",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	var opts createOptions
	cluster := &cobra.Command{
		Use:   "cluster [NAME]",
		Short: "Create a Kubernetes cluster",
		Long: `Create a Kubernetes cluster from flags or from a spec file (-f).

tuggy first checks that everything needed is in place (the same checks as
"kubectl tuggy doctor"), then builds the cluster, adds it to your kubeconfig
and waits until it answers. If creation fails or is interrupted, run the same
command again to finish it.`,
		Example: `  # A GKE cluster that is reported as expired after 8 hours
  kubectl tuggy create cluster dev --platform gke --project my-project --location us-central1-a --ttl 8h

  # From a spec file
  kubectl tuggy create cluster -f cluster.yaml

  # Show what would be built, without building it
  kubectl tuggy create cluster dev --platform gke --project my-project --location us-central1-a --dry-run`,
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return app.createCluster(cmd, name, opts)
		},
	}
	opts.spec.add(cluster, app.Platforms)
	markPlatformFlags(cluster, app.Platforms)
	cluster.Flags().BoolVar(&opts.dryRun, "dry-run", false, "show what would be built without building anything")
	cluster.Flags().BoolVar(&opts.noKubeconfig, "no-kubeconfig", false, "don't add the cluster to your kubeconfig")
	cluster.Flags().BoolVarP(&opts.verbose, "verbose", "v", false, "show OpenTofu's full output")

	create.AddCommand(cluster)
	return create
}

func (a *App) createCluster(cmd *cobra.Command, name string, opts createOptions) error {
	ctx := cmd.Context()
	spec, p, err := opts.spec.build(cmd, name, a.Platforms)
	if err != nil {
		return err
	}
	name = spec.Metadata.Name

	plan, err := p.FromV1Alpha1(spec)
	if err != nil {
		return WithExitCode(ExitUsage, err)
	}

	unlock, err := a.lock(name)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	rec, resuming, err := a.existingForCreate(name, p.Name())
	if err != nil {
		return err
	}

	a.printf("Checking %s prerequisites for cluster %s\n", p.Name(), name)
	if err := a.preflight(ctx, p, plan); err != nil {
		return err
	}

	if opts.dryRun {
		return a.dryRunCreate(ctx, p, plan, opts)
	}

	// Record the cluster before anything is built, so an interruption is
	// visible and resumable.
	if resuming {
		a.printf("Resuming cluster %s (last attempt: %s)\n", name, rec.Message)
		if err := rec.Transition(clustermeta.StatusCreating, "", a.Now()); err != nil {
			return err
		}
		err = a.Store.Update(rec)
	} else {
		rec = clustermeta.NewRecord(name, p.Name(), version.Get().Version, plan.TTL, a.Now())
		if err := rec.Transition(clustermeta.StatusCreating, "", a.Now()); err != nil {
			return err
		}
		err = a.Store.Create(rec)
	}
	if err != nil {
		return err
	}

	logFile, err := a.openLog(name, "create")
	if err != nil {
		return err
	}
	defer logFile.Close()
	var log io.Writer = logFile
	if opts.verbose {
		log = io.MultiWriter(logFile, a.Streams.ErrOut)
	}

	start := a.Now()
	info, err := p.Create(ctx, plan, platform.CreateOptions{
		Dir:        a.Store.Dir(name),
		CacheDir:   a.cacheDir(),
		Log:        log,
		OnProgress: a.progressPrinter(opts.verbose),
	})
	if err != nil {
		return a.failCreate(rec, err, logFile.Name())
	}
	rec.Outputs = info.Outputs
	_ = a.Store.Update(rec)

	if err := a.connect(ctx, p, rec, !opts.noKubeconfig); err != nil {
		return a.failCreate(rec, err, logFile.Name())
	}

	if err := rec.Transition(clustermeta.StatusReady, "", a.Now()); err != nil {
		return err
	}
	if err := a.Store.Update(rec); err != nil {
		return err
	}

	a.printf("\nCluster %s is ready (created in %s).", name, shortDuration(a.Now().Sub(start)))
	if rec.ExpiresAt != nil {
		a.printf(" It expires in %s.", shortDuration(rec.ExpiresAt.Sub(a.Now())))
	}
	a.printf("\nDelete it with: kubectl tuggy delete cluster %s\n", name)
	return nil
}

// existingForCreate decides what to do about a cluster that already has a
// record: Failed (or interrupted) clusters are resumed, others refused.
func (a *App) existingForCreate(name, platformName string) (*clustermeta.Record, bool, error) {
	rec, err := a.Store.Get(name)
	if errors.Is(err, clustermeta.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := a.interrupted(rec); err != nil {
		return nil, false, err
	}
	switch {
	case rec.Platform != platformName:
		return nil, false, fmt.Errorf("cluster %q already exists on platform %s", name, rec.Platform)
	case rec.Status == clustermeta.StatusFailed:
		return rec, true, nil
	default:
		return nil, false, fmt.Errorf("cluster %q already exists (%s); see: kubectl tuggy describe cluster %s", name, rec.Status, name)
	}
}

// preflight runs and prints the platform's checks.
func (a *App) preflight(ctx context.Context, p platform.Platform, plan *platform.Plan) error {
	results := p.Preflight(ctx, plan)
	printChecks(a.Streams.Out, results)
	if platform.Failed(results) {
		return WithExitCode(ExitPreflight, errors.New("prerequisites are missing; fix the problems above and try again"))
	}
	return nil
}

func (a *App) dryRunCreate(ctx context.Context, p platform.Platform, plan *platform.Plan, opts createOptions) error {
	// Plan in a throwaway workspace under the tuggy directory, which
	// container engines running in a VM can share.
	if err := os.MkdirAll(a.Root, 0o700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(a.Root, "dry-run-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	log := io.Discard
	if opts.verbose {
		log = a.Streams.ErrOut
	}
	info, err := p.Create(ctx, plan, platform.CreateOptions{
		Dir: dir, CacheDir: a.cacheDir(), DryRun: true, Log: log, OnProgress: a.progressPrinter(opts.verbose),
	})
	if err != nil {
		return cloudError(err)
	}
	c := info.Changes
	a.printf("\nDry run: would add %d, change %d and remove %d resources:\n", c.Add, c.Change, c.Remove)
	for _, r := range c.Resources {
		a.printf("  %-8s %s\n", r.Action, r.Address)
	}
	a.printf("Nothing was created.\n")
	return nil
}

// connect writes the kubeconfig entry and waits until the cluster answers.
func (a *App) connect(ctx context.Context, p platform.Platform, rec *clustermeta.Record, merge bool) error {
	cfg, err := p.Kubeconfig(ctx, rec)
	if err != nil {
		return err
	}
	a.printf("• Waiting for the cluster to answer\n")
	version, err := a.WaitReady(ctx, cfg, kubeconfig.ReadyOptions{})
	if err != nil {
		return err
	}
	a.printf("  ✓ cluster answers (Kubernetes %s)\n", version)

	if merge {
		if err := a.Kube.Merge(cfg, true); err != nil {
			return err
		}
		a.printf("  ✓ kubeconfig updated; current context is now %q\n", cfg.CurrentContext)
	}
	return nil
}

// failCreate marks the record Failed and explains how to continue.
func (a *App) failCreate(rec *clustermeta.Record, cause error, logPath string) error {
	msg := cause.Error()
	if errors.Is(cause, context.Canceled) {
		msg = "cancelled"
	}
	if rec.Transition(clustermeta.StatusFailed, msg, a.Now()) == nil {
		_ = a.Store.Update(rec)
	}
	fmt.Fprintf(a.Streams.ErrOut, "\nCluster %s was not completed. Full log: %s\n", rec.Name, logPath)
	fmt.Fprintf(a.Streams.ErrOut, "Run the same command again to finish it, or remove what was built with: kubectl tuggy delete cluster %s\n", rec.Name)
	return cloudError(cause)
}

// cloudError gives a failed cloud operation its exit code, keeping
// cancellation as cancellation.
func cloudError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return WithExitCode(ExitCloud, err)
}

func printChecks(w io.Writer, results []platform.CheckResult) {
	marks := map[platform.CheckStatus]string{platform.CheckPass: "✓", platform.CheckWarn: "!", platform.CheckFail: "✗"}
	for _, r := range results {
		fmt.Fprintf(w, "  %s %s: %s\n", marks[r.Status], r.Name, r.Message)
		if r.Fix != "" && r.Status != platform.CheckPass {
			fmt.Fprintf(w, "      fix: %s\n", r.Fix)
		}
	}
}
