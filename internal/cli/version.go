package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/tuggy-dev/kubectl-tuggy/internal/version"
)

func newVersionCommand(streams IOStreams) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the tuggy version",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()

			switch output {
			case "":
				fmt.Fprintf(streams.Out, "kubectl-tuggy %s\n", info.Version)
				if info.Commit != "" {
					fmt.Fprintf(streams.Out, "  commit:   %s\n", info.Commit)
				}
				if info.Date != "" {
					fmt.Fprintf(streams.Out, "  built:    %s\n", info.Date)
				}
				fmt.Fprintf(streams.Out, "  go:       %s\n", info.GoVersion)
				fmt.Fprintf(streams.Out, "  platform: %s\n", info.Platform)
				return nil
			case "json":
				b, err := json.MarshalIndent(info, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(streams.Out, string(b))
				return nil
			case "yaml":
				b, err := yaml.Marshal(info)
				if err != nil {
					return err
				}
				fmt.Fprint(streams.Out, string(b))
				return nil
			default:
				return WithExitCode(ExitUsage, fmt.Errorf("unsupported output format %q (use json or yaml)", output))
			}
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json or yaml")
	return cmd
}
