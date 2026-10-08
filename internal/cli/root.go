// Package cli defines the kubectl-tuggy command tree.
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// Run executes kubectl-tuggy with args and returns the process exit code.
// Errors are printed once here; commands only return them.
func Run(ctx context.Context, args []string, streams IOStreams) int {
	app, err := newApp(streams)
	if err != nil {
		fmt.Fprintf(streams.ErrOut, "Error: %v\n", err)
		return ExitError
	}
	return app.Run(ctx, args)
}

// Run executes kubectl-tuggy with args using the app's dependencies.
func (a *App) Run(ctx context.Context, args []string) int {
	root := NewRootCommand(a)
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)
	if err != nil {
		fmt.Fprintf(a.Streams.ErrOut, "Error: %v\n", err)
	}
	return ExitCodeFor(err)
}

// NewRootCommand builds the full command tree.
func NewRootCommand(app *App) *cobra.Command {
	streams := app.Streams
	root := &cobra.Command{
		Use:   "tuggy",
		Short: "Your Kubernetes sidekick",
		Long: `tuggy turns common Kubernetes chores into one command,
starting with creating and deleting clusters.`,
		Annotations: map[string]string{
			// Show "kubectl tuggy" rather than "tuggy" in help, as kubectl plugins should.
			cobra.CommandDisplayNameAnnotation: "kubectl tuggy",
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageError(cmd, fmt.Errorf("unknown command %q", args[0]))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		// After every command that works with clusters, mention any that
		// have passed their TTL, so forgotten clusters don't keep costing.
		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			switch cmd.Name() {
			case "version", "help", "completion", "tuggy":
				return
			}
			app.warnExpired()
		},
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.ErrOut)

	root.SetFlagErrorFunc(usageError)

	root.AddCommand(
		newCreateCommand(app),
		newGetCommand(app),
		newDescribeCommand(app),
		newDeleteCommand(app),
		newDoctorCommand(app),
		newVersionCommand(streams),
	)
	return root
}

// usageError marks err as invalid input (exit code 2) and points at --help.
func usageError(cmd *cobra.Command, err error) error {
	return WithExitCode(ExitUsage, fmt.Errorf("%w\nSee '%s --help' for usage", err, cmd.CommandPath()))
}

// usageArgs wraps a cobra argument validator so its errors exit with code 2.
func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return usageError(cmd, err)
		}
		return nil
	}
}
