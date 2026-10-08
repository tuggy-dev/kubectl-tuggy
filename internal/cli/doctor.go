package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

func newDoctorCommand(app *App) *cobra.Command {
	var spec specFlags
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything needed to create clusters is in place",
		Long: `Run the same checks "create cluster" runs first, without creating anything,
and show how to fix each problem found.`,
		Example: `  kubectl tuggy doctor --platform gke --project my-project
  kubectl tuggy doctor -f cluster.yaml`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The checks don't depend on the location, so supply one when
			// it is left out rather than make the user invent it.
			if spec.file == "" {
				if fl := cmd.Flags().Lookup("location"); fl != nil && !fl.Changed {
					_ = cmd.Flags().Set("location", "us-central1")
				}
			}
			c, p, err := spec.build(cmd, "doctor", app.Platforms)
			if err != nil {
				return err
			}
			plan, err := p.FromV1Alpha1(c)
			if err != nil {
				return WithExitCode(ExitUsage, err)
			}

			app.printf("Checking %s prerequisites\n", p.Name())
			results := p.Preflight(cmd.Context(), plan)
			printChecks(app.Streams.Out, results)
			if platform.Failed(results) {
				return WithExitCode(ExitPreflight, errors.New("some checks failed; see the fixes above"))
			}
			app.printf("Everything needed is in place.\n")
			return nil
		},
	}
	spec.add(cmd, app.Platforms)
	markPlatformFlags(cmd, app.Platforms)
	// Settings that only shape the cluster don't affect the checks.
	for _, name := range []string{"ttl", "kubernetes-version", "machine-type", "node-count", "spot"} {
		_ = cmd.Flags().MarkHidden(name)
	}
	return cmd
}
