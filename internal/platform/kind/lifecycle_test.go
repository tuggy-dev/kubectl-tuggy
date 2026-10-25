package kind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tuggy-dev/kubectl-tuggy/internal/clustermeta"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

func plan(t *testing.T, p *Platform, config string) *platform.Plan {
	t.Helper()
	pl, err := p.FromV1Alpha1(testCluster(config))
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

func TestCreate(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	var log bytes.Buffer
	var progress []string
	info, err := h.p.Create(context.Background(), plan(t, h.p, ""), platform.CreateOptions{
		Dir: dir,
		Log: &log,
		OnProgress: func(pr platform.Progress) {
			progress = append(progress, pr.Message)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.provider.Calls(); !slices.Contains(got, "Create tuggy-dev") {
		t.Errorf("provider calls = %v", got)
	}
	if info.Outputs[OutputEndpoint] != "https://127.0.0.1:41234" || info.Outputs[OutputKubernetesVersion] != "v1.37.0" || info.Outputs[OutputNodeImage] == "" {
		t.Errorf("outputs = %v", info.Outputs)
	}
	if !slices.Equal(progress, []string{"Preparing nodes"}) {
		t.Errorf("progress = %v", progress)
	}
	if !strings.Contains(log.String(), "debug detail") {
		t.Errorf("debug output should reach the log:\n%s", log.String())
	}
	for _, f := range []string{VariablesFile, ConfigFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, kubeconfigFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("scratch kubeconfig should be removed, stat = %v", err)
	}
}

func TestCreateDryRun(t *testing.T) {
	h := newHarness(t)
	info, err := h.p.Create(context.Background(), plan(t, h.p, ""), platform.CreateOptions{Dir: t.TempDir(), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.provider.Calls()) != 0 {
		t.Errorf("a dry run must not touch kind: %v", h.provider.Calls())
	}
	c := info.Changes
	if c == nil || c.Add != 2 || c.Resources[0].Address != "kind_node.tuggy-dev-control-plane" || c.Resources[1].Action != "create" {
		t.Errorf("changes = %+v", c)
	}
}

func TestCreateFailure(t *testing.T) {
	h := newHarness(t)
	h.provider.createErr = errors.New("node failed to start")
	_, err := h.p.Create(context.Background(), plan(t, h.p, ""), platform.CreateOptions{Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "node failed to start") {
		t.Errorf("error = %v", err)
	}
}

func TestCreateWrongPlan(t *testing.T) {
	h := newHarness(t)
	_, err := h.p.Create(context.Background(), &platform.Plan{Variables: "gke"}, platform.CreateOptions{Dir: t.TempDir()})
	if err == nil {
		t.Error("a plan from another platform should be refused")
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t)
	h.provider.clusters["tuggy-dev"] = []string{"tuggy-dev-control-plane", "tuggy-dev-worker"}
	rec := &clustermeta.Record{Name: "dev", Platform: Name}

	var confirmed platform.Changes
	err := h.p.Delete(context.Background(), rec, platform.DeleteOptions{
		Dir:     t.TempDir(),
		Confirm: func(c platform.Changes) error { confirmed = c; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Remove != 2 || confirmed.Resources[1].Address != "kind_node.tuggy-dev-worker" {
		t.Errorf("confirmed = %+v", confirmed)
	}
	if _, ok := h.provider.clusters["tuggy-dev"]; ok {
		t.Error("cluster not deleted")
	}
}

func TestDeleteDeclinedAndDryRun(t *testing.T) {
	h := newHarness(t)
	h.provider.clusters["tuggy-dev"] = []string{"tuggy-dev-control-plane"}
	rec := &clustermeta.Record{Name: "dev", Platform: Name}

	declined := errors.New("declined")
	err := h.p.Delete(context.Background(), rec, platform.DeleteOptions{Dir: t.TempDir(), Confirm: func(platform.Changes) error { return declined }})
	if !errors.Is(err, declined) {
		t.Errorf("error = %v", err)
	}
	if err := h.p.Delete(context.Background(), rec, platform.DeleteOptions{Dir: t.TempDir(), DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.provider.clusters["tuggy-dev"]; !ok {
		t.Error("declined and dry-run deletes must keep the cluster")
	}
}

func TestDeleteMissingCluster(t *testing.T) {
	h := newHarness(t)
	rec := &clustermeta.Record{Name: "gone", Platform: Name}
	if err := h.p.Delete(context.Background(), rec, platform.DeleteOptions{Dir: t.TempDir()}); err != nil {
		t.Errorf("deleting a cluster whose containers are gone should succeed: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	if _, err := h.p.Create(context.Background(), plan(t, h.p, ""), platform.CreateOptions{Dir: dir, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	rec := &clustermeta.Record{Name: "dev", Platform: Name, Outputs: map[string]string{OutputKubernetesVersion: "v1.37.0"}}
	info, err := h.p.Describe(context.Background(), rec, dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Platform != Name || info.KubernetesVersion != "v1.37.0" || len(info.NodePools) != 1 || info.NodePools[0].Count != 1 {
		t.Errorf("info = %+v", info)
	}

	data, _ := os.ReadFile(filepath.Join(dir, VariablesFile))
	var v Variables
	if err := json.Unmarshal(data, &v); err != nil || v.ClusterName != "tuggy-dev" {
		t.Errorf("%s = %s (%v)", VariablesFile, data, err)
	}
}

func TestKubeconfig(t *testing.T) {
	h := newHarness(t)
	h.provider.clusters["tuggy-dev"] = []string{"tuggy-dev-control-plane"}
	cfg, err := h.p.Kubeconfig(context.Background(), &clustermeta.Record{Name: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "tuggy-dev" || cfg.Clusters["tuggy-dev"].Server != "https://127.0.0.1:41234" {
		t.Errorf("kubeconfig = %+v", cfg)
	}
	if u := cfg.AuthInfos["tuggy-dev"]; u == nil || len(u.ClientCertificateData) == 0 {
		t.Errorf("user = %+v", u)
	}
	if len(cfg.Clusters) != 1 || len(cfg.Contexts) != 1 || len(cfg.AuthInfos) != 1 {
		t.Errorf("only tuggy-dev entries should be returned: %+v", cfg)
	}

	if _, err := h.p.Kubeconfig(context.Background(), &clustermeta.Record{Name: "gone"}); err == nil {
		t.Error("a missing cluster should fail")
	}
}

func TestListRemoteNotSupported(t *testing.T) {
	h := newHarness(t)
	if _, err := h.p.ListRemote(context.Background(), platform.ListOptions{}); !errors.Is(err, platform.ErrNotSupported) {
		t.Errorf("error = %v", err)
	}
}

func TestRegistered(t *testing.T) {
	p, err := platform.Get(Name)
	if err != nil || p.Name() != Name {
		t.Errorf("kind should register itself: %v", err)
	}
}
