package gke

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/engine/tofu"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform/gke/module"
)

// Module output names, also used as keys in the cluster record's outputs.
const (
	OutputEndpoint          = "endpoint"
	OutputCACertificate     = "ca_certificate"
	OutputKubernetesVersion = "kubernetes_version"
	OutputProject           = "project"
	OutputLocation          = "location"
)

// Create writes the module and variables into opts.Dir and runs OpenTofu to
// build the cluster. With opts.DryRun it only plans.
func (p *Platform) Create(ctx context.Context, plan *platform.Plan, opts platform.CreateOptions) (*platform.ClusterInfo, error) {
	vars, ok := plan.Variables.(Variables)
	if !ok {
		return nil, errors.New("plan was not made by the gke platform")
	}
	if err := tofu.WriteModule(opts.Dir, module.FS); err != nil {
		return nil, fmt.Errorf("writing GKE module: %w", err)
	}
	if err := tofu.WriteVariables(opts.Dir, vars); err != nil {
		return nil, err
	}

	eng, ws, err := p.prepare(ctx, plan.Name, opts.Dir, opts.CacheDir, opts.Log, opts.OnProgress)
	if err != nil {
		return nil, err
	}

	info(opts.OnProgress, "Planning")
	planned, err := eng.Plan(ctx, ws, tofu.PlanOptions{})
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		_ = eng.DiscardPlan(ws, planned)
		return &platform.ClusterInfo{Name: vars.Name, Platform: Name, Location: vars.Location, Changes: changes(planned)}, nil
	}

	info(opts.OnProgress, "Applying")
	if _, err := eng.Apply(ctx, ws, planned); err != nil {
		return nil, err
	}

	outputs, err := eng.Outputs(ctx, ws)
	if err != nil {
		return nil, err
	}
	result := describeVariables(vars)
	result.Outputs = tofu.StringOutputs(outputs)
	result.KubernetesVersion = result.Outputs[OutputKubernetesVersion]
	return result, nil
}

// Delete removes every resource of the cluster using the module, variables
// and state in opts.Dir.
func (p *Platform) Delete(ctx context.Context, rec *clustermeta.Record, opts platform.DeleteOptions) error {
	if _, err := os.Stat(filepath.Join(opts.Dir, tofu.VariablesFile)); err != nil {
		return fmt.Errorf("cluster %q has no OpenTofu workspace in %s: %w", rec.Name, opts.Dir, err)
	}
	eng, ws, err := p.prepare(ctx, rec.Name, opts.Dir, opts.CacheDir, opts.Log, opts.OnProgress)
	if err != nil {
		return err
	}

	info(opts.OnProgress, "Planning removal")
	planned, err := eng.Plan(ctx, ws, tofu.PlanOptions{Destroy: true})
	if err != nil {
		return err
	}
	if opts.Confirm != nil {
		if err := opts.Confirm(*changes(planned)); err != nil {
			_ = eng.DiscardPlan(ws, planned)
			return err
		}
	}
	if opts.DryRun {
		return eng.DiscardPlan(ws, planned)
	}

	info(opts.OnProgress, "Removing")
	_, err = eng.Apply(ctx, ws, planned)
	return err
}

// prepare connects to the container engine, makes sure the OpenTofu image is
// present, and runs tofu init in dir.
func (p *Platform) prepare(ctx context.Context, name, dir, cacheDir string, log io.Writer, onProgress func(platform.Progress)) (*tofu.Engine, tofu.Workspace, error) {
	r, err := p.NewRunner()
	if err != nil {
		return nil, tofu.Workspace{}, err
	}
	creds, err := loadCredentials(p.CredentialsPath())
	if err != nil {
		return nil, tofu.Workspace{}, fmt.Errorf("%w (run: gcloud auth application-default login)", err)
	}
	mounts, env := creds.container()

	ws := tofu.Workspace{
		Name:    name,
		Dir:     dir,
		Mounts:  mounts,
		Env:     env,
		Log:     log,
		OnEvent: progressFromEvents(onProgress),
	}

	info(onProgress, "Preparing OpenTofu")
	if err := r.EnsureImage(ctx, p.image(), log); err != nil {
		return nil, ws, err
	}
	eng := p.engine(r, cacheDir)
	if err := eng.Init(ctx, ws); err != nil {
		return nil, ws, err
	}
	return eng, ws, nil
}

// Describe reports the cluster's settings from its variables file and its
// version from the record.
func (p *Platform) Describe(_ context.Context, rec *clustermeta.Record, dir string) (*platform.ClusterInfo, error) {
	data, err := os.ReadFile(filepath.Join(dir, tofu.VariablesFile)) // #nosec G304 -- the cluster's own workspace
	if err != nil {
		return nil, fmt.Errorf("reading settings of cluster %q: %w", rec.Name, err)
	}
	var vars Variables
	if err := json.Unmarshal(data, &vars); err != nil {
		return nil, fmt.Errorf("reading settings of cluster %q: %w", rec.Name, err)
	}
	result := describeVariables(vars)
	result.Outputs = rec.Outputs
	result.KubernetesVersion = rec.Outputs[OutputKubernetesVersion]
	return result, nil
}

func describeVariables(v Variables) *platform.ClusterInfo {
	result := &platform.ClusterInfo{Name: v.Name, Platform: Name, Location: v.Location}
	for _, np := range v.NodePools {
		result.NodePools = append(result.NodePools, platform.NodePoolInfo{Name: np.Name, MachineType: np.MachineType, Count: np.Count})
	}
	return result
}

// Kubeconfig returns entries for the cluster that authenticate through
// gke-gcloud-auth-plugin using Application Default Credentials: the same
// sign-in OpenTofu and preflight use, so users keep only one Google sign-in
// fresh (gcloud auth application-default login) instead of also needing
// gcloud auth login.
func (p *Platform) Kubeconfig(_ context.Context, rec *clustermeta.Record) (*clientcmdapi.Config, error) {
	endpoint, ca := rec.Outputs[OutputEndpoint], rec.Outputs[OutputCACertificate]
	if endpoint == "" || ca == "" {
		return nil, fmt.Errorf("cluster %q has no endpoint or CA certificate recorded; was it created successfully?", rec.Name)
	}
	caData, err := base64.StdEncoding.DecodeString(ca)
	if err != nil {
		return nil, fmt.Errorf("cluster %q has an invalid CA certificate: %w", rec.Name, err)
	}

	name := "tuggy-" + rec.Name
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[name] = &clientcmdapi.Cluster{Server: endpoint, CertificateAuthorityData: caData}
	cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion:         "client.authentication.k8s.io/v1beta1",
		Command:            authPlugin,
		Args:               []string{"--use_application_default_credentials"},
		InstallHint:        "Install gke-gcloud-auth-plugin to use kubectl with GKE: gcloud components install gke-gcloud-auth-plugin",
		ProvideClusterInfo: true,
		InteractiveMode:    clientcmdapi.IfAvailableExecInteractiveMode,
	}}
	cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	cfg.CurrentContext = name
	return cfg, nil
}

// ListRemote is not implemented yet; get clusters --all arrives with the
// commands in milestone 1.10.
func (p *Platform) ListRemote(context.Context, platform.ListOptions) ([]platform.ClusterInfo, error) {
	return nil, platform.ErrNotSupported
}

func changes(plan *tofu.PlanResult) *platform.Changes {
	c := &platform.Changes{Add: plan.Summary.Add, Change: plan.Summary.Change, Remove: plan.Summary.Remove}
	for _, ev := range plan.Changes {
		c.Resources = append(c.Resources, platform.ResourceChange{Address: ev.Resource, Type: ev.ResourceType, Action: ev.Action})
	}
	return c
}

func info(onProgress func(platform.Progress), msg string) {
	if onProgress != nil {
		onProgress(platform.Progress{Kind: platform.ProgressInfo, Message: msg})
	}
}

// progressFromEvents translates OpenTofu resource events into platform
// progress updates.
func progressFromEvents(onProgress func(platform.Progress)) func(tofu.Event) {
	if onProgress == nil {
		return nil
	}
	kinds := map[tofu.EventType]platform.ProgressKind{
		tofu.EventResourceStart:    platform.ProgressStarted,
		tofu.EventResourceProgress: platform.ProgressRunning,
		tofu.EventResourceDone:     platform.ProgressDone,
		tofu.EventResourceFailed:   platform.ProgressFailed,
	}
	return func(ev tofu.Event) {
		kind, ok := kinds[ev.Type]
		if !ok {
			return
		}
		onProgress(platform.Progress{
			Kind:         kind,
			Resource:     ev.Resource,
			ResourceType: ev.ResourceType,
			Action:       ev.Action,
			Elapsed:      ev.Elapsed,
			Message:      ev.Message,
		})
	}
}
