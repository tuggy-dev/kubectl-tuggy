package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/kubeconfig"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// clusterView is how a cluster appears in get, describe and -o json/yaml.
type clusterView struct {
	Name              string                  `json:"name"`
	Platform          string                  `json:"platform"`
	Status            clustermeta.Status      `json:"status"`
	Message           string                  `json:"message,omitempty"`
	Location          string                  `json:"location,omitempty"`
	KubernetesVersion string                  `json:"kubernetesVersion,omitempty"`
	Endpoint          string                  `json:"endpoint,omitempty"`
	NodePools         []platform.NodePoolInfo `json:"nodePools,omitempty"`
	CreatedAt         time.Time               `json:"createdAt"`
	ExpiresAt         *time.Time              `json:"expiresAt,omitempty"`
	Expired           bool                    `json:"expired"`
	Context           string                  `json:"context"`
	Directory         string                  `json:"directory"`
}

func (a *App) view(ctx context.Context, r *clustermeta.Record) clusterView {
	v := clusterView{
		Name: r.Name, Platform: r.Platform, Status: r.Status, Message: r.Message,
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, Expired: r.Expired(a.Now()),
		Context: contextName(r.Name), Directory: a.Store.Dir(r.Name),
		KubernetesVersion: r.Outputs["kubernetes_version"], Endpoint: r.Outputs["endpoint"],
	}
	if p, err := a.Platforms.Get(r.Platform); err == nil {
		if info, err := p.Describe(ctx, r, a.Store.Dir(r.Name)); err == nil {
			v.Location, v.NodePools = info.Location, info.NodePools
			if info.KubernetesVersion != "" {
				v.KubernetesVersion = info.KubernetesVersion
			}
		}
	}
	return v
}

func newGetCommand(app *App) *cobra.Command {
	get := &cobra.Command{
		Use:   "get",
		Short: "Show resources",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	var output string
	clusters := &cobra.Command{
		Use:     "clusters",
		Aliases: []string{"cluster"},
		Short:   "List the clusters tuggy manages",
		Example: `  kubectl tuggy get clusters
  kubectl tuggy get clusters -o yaml`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return app.getClusters(cmd, output) },
	}
	addOutputFlag(clusters, &output)

	var merge bool
	kc := &cobra.Command{
		Use:   "kubeconfig NAME",
		Short: "Print a cluster's kubeconfig, or add it to yours with --merge",
		Example: `  kubectl tuggy get kubeconfig dev > dev.kubeconfig
  kubectl tuggy get kubeconfig dev --merge`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error { return app.getKubeconfig(cmd, args[0], merge) },
	}
	kc.Flags().BoolVar(&merge, "merge", false, "add the cluster to your kubeconfig and make it the current context")

	get.AddCommand(clusters, kc)
	return get
}

func addOutputFlag(cmd *cobra.Command, output *string) {
	cmd.Flags().StringVarP(output, "output", "o", "", "output format: json or yaml (default: a table)")
}

func (a *App) getClusters(cmd *cobra.Command, output string) error {
	records, err := a.Store.List()
	if err != nil {
		return err
	}
	views := make([]clusterView, 0, len(records))
	for _, r := range records {
		views = append(views, a.view(cmd.Context(), r))
	}

	if output != "" {
		return a.printStructured(cmd, output, map[string]any{"clusters": views})
	}
	if len(views) == 0 {
		a.printf("No clusters yet. Create one with: kubectl tuggy create cluster NAME --platform ...\n")
		return nil
	}
	tw := tabwriter.NewWriter(a.Streams.Out, 0, 4, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPLATFORM\tLOCATION\tSTATUS\tAGE\tEXPIRES")
	for _, v := range views {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.Platform, dash(v.Location), v.Status, shortDuration(a.Now().Sub(v.CreatedAt)), a.expiresIn(v.ExpiresAt))
	}
	return tw.Flush()
}

func (a *App) expiresIn(at *time.Time) string {
	switch {
	case at == nil:
		return "-"
	case !a.Now().Before(*at):
		return "expired"
	default:
		return shortDuration(at.Sub(a.Now()))
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *App) printStructured(cmd *cobra.Command, output string, v any) error {
	var (
		b   []byte
		err error
	)
	switch output {
	case "json":
		b, err = json.MarshalIndent(v, "", "  ")
		b = append(b, '\n')
	case "yaml":
		b, err = yaml.Marshal(v)
	default:
		return usageError(cmd, fmt.Errorf("unsupported output format %q (use json or yaml)", output))
	}
	if err != nil {
		return err
	}
	_, err = a.Streams.Out.Write(b)
	return err
}

func (a *App) record(name string) (*clustermeta.Record, platform.Platform, error) {
	rec, err := a.Store.Get(name)
	if errors.Is(err, clustermeta.ErrNotFound) {
		return nil, nil, fmt.Errorf("cluster %q not found; see: kubectl tuggy get clusters", name)
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := a.Platforms.Get(rec.Platform)
	return rec, p, err
}

func (a *App) getKubeconfig(cmd *cobra.Command, name string, merge bool) error {
	rec, p, err := a.record(name)
	if err != nil {
		return err
	}
	cfg, err := p.Kubeconfig(cmd.Context(), rec)
	if err != nil {
		return err
	}
	if merge {
		if err := a.Kube.Merge(cfg, true); err != nil {
			return err
		}
		a.printf("Added cluster %s to %s; current context is now %q.\n", name, a.Kube.Path(), cfg.CurrentContext)
		return nil
	}
	out, err := kubeconfig.Serialize(cfg)
	if err != nil {
		return err
	}
	_, err = a.Streams.Out.Write(out)
	return err
}

func newDescribeCommand(app *App) *cobra.Command {
	describe := &cobra.Command{
		Use:   "describe",
		Short: "Show details of a resource",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var output string
	cluster := &cobra.Command{
		Use:   "cluster NAME",
		Short: "Show a cluster's details",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			rec, _, err := app.record(args[0])
			if err != nil {
				return err
			}
			v := app.view(cmd.Context(), rec)
			if output != "" {
				return app.printStructured(cmd, output, v)
			}
			app.describe(v)
			return nil
		},
	}
	addOutputFlag(cluster, &output)
	describe.AddCommand(cluster)
	return describe
}

func (a *App) describe(v clusterView) {
	tw := tabwriter.NewWriter(a.Streams.Out, 0, 4, 2, ' ', 0)
	row := func(k, val string) { fmt.Fprintf(tw, "%s:\t%s\n", k, val) }
	row("Name", v.Name)
	row("Platform", v.Platform)
	status := string(v.Status)
	if v.Message != "" {
		status += " (" + v.Message + ")"
	}
	row("Status", status)
	row("Location", dash(v.Location))
	row("Kubernetes", dash(v.KubernetesVersion))
	row("Endpoint", dash(v.Endpoint))
	row("Created", v.CreatedAt.Local().Format(time.RFC1123)+" ("+shortDuration(a.Now().Sub(v.CreatedAt))+" ago)")
	if v.ExpiresAt != nil {
		row("Expires", v.ExpiresAt.Local().Format(time.RFC1123)+" ("+a.expiresIn(v.ExpiresAt)+")")
	} else {
		row("Expires", "never")
	}
	row("Context", v.Context)
	row("Directory", v.Directory)
	_ = tw.Flush()
	if len(v.NodePools) > 0 {
		a.printf("Node pools:\n")
		for _, np := range v.NodePools {
			a.printf("  %s: %d x %s\n", np.Name, np.Count, np.MachineType)
		}
	}
}
