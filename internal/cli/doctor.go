package cli

import (
	"errors"
	"slices"

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
  kubectl tuggy doctor --platform kind
  kubectl tuggy doctor -f cluster.yaml`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The checks don't depend on the location, so supply one when
			// the selected platform has a --location flag and it is left
			// out, rather than make the user invent it.
			if spec.file == "" {
				name, err := spec.platformName(cmd, app.Platforms)
				if err != nil {
					return usageError(cmd, err)
				}
				fl := cmd.Flags().Lookup("location")
				if fl != nil && !fl.Changed && slices.Contains(fl.Annotations[platformFlagAnnotation], name) {
					_ = cmd.Flags().Set("location", "us-central1")
				}
			}
			// Without -f the flags describe a hypothetical cluster, so give it a
			// placeholder name; with -f the file names the cluster itself.
			name := "doctor"
			if spec.file != "" {
				name = ""
			}
			c, p, err := spec.build(cmd, name, app.Platforms)
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
