package kubeconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// existing is a kubeconfig the user already has: two clusters, one current.
const existing = `apiVersion: v1
kind: Config
current-context: work
clusters:
- name: work
  cluster: {server: "https://work.example.com"}
- name: kind-local
  cluster: {server: "https://127.0.0.1:6443"}
users:
- name: work-user
  user: {token: secret}
- name: kind-local
  user: {token: kind}
contexts:
- name: work
  context: {cluster: work, user: work-user}
- name: kind-local
  context: {cluster: kind-local, user: kind-local}
`

func manager(t *testing.T, files ...string) *Manager {
	t.Helper()
	t.Setenv(clientcmd.RecommendedConfigPathEnvVar, strings.Join(files, string(filepath.ListSeparator)))
	return New()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, path string) *clientcmdapi.Config {
	t.Helper()
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func tuggyEntries(name, server string) *clientcmdapi.Config {
	n := Prefix + name
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[n] = &clientcmdapi.Cluster{Server: server, CertificateAuthorityData: []byte("ca")}
	cfg.AuthInfos[n] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{APIVersion: "client.authentication.k8s.io/v1beta1", Command: "gke-gcloud-auth-plugin", InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode}}
	cfg.Contexts[n] = &clientcmdapi.Context{Cluster: n, AuthInfo: n}
	cfg.CurrentContext = n
	return cfg
}

func TestMergeKeepsExistingEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, existing)
	m := manager(t, path)

	if err := m.Merge(tuggyEntries("dev", "https://34.1.2.3"), true); err != nil {
		t.Fatal(err)
	}
	cfg := load(t, path)
	if cfg.CurrentContext != "tuggy-dev" {
		t.Errorf("current context = %q", cfg.CurrentContext)
	}
	for _, name := range []string{"work", "kind-local", "tuggy-dev"} {
		if cfg.Contexts[name] == nil {
			t.Errorf("context %s missing", name)
		}
	}
	if cfg.Clusters["work"].Server != "https://work.example.com" || cfg.AuthInfos["work-user"].Token != "secret" {
		t.Error("existing entries were changed")
	}
	if got := cfg.AuthInfos["tuggy-dev"].Exec; got == nil || got.Command != "gke-gcloud-auth-plugin" {
		t.Errorf("exec config = %+v", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestMergeWithoutSwitchingContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, existing)
	if err := manager(t, path).Merge(tuggyEntries("dev", "https://34.1.2.3"), false); err != nil {
		t.Fatal(err)
	}
	if cfg := load(t, path); cfg.CurrentContext != "work" || cfg.Contexts["tuggy-dev"] == nil {
		t.Errorf("current = %q, tuggy-dev added = %v", cfg.CurrentContext, cfg.Contexts["tuggy-dev"] != nil)
	}
}

func TestMergeCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config")
	if err := manager(t, path).Merge(tuggyEntries("dev", "https://34.1.2.3"), true); err != nil {
		t.Fatal(err)
	}
	if cfg := load(t, path); cfg.CurrentContext != "tuggy-dev" {
		t.Errorf("current context = %q", cfg.CurrentContext)
	}
}

func TestMergeReplacesOwnEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, existing)
	m := manager(t, path)
	if err := m.Merge(tuggyEntries("dev", "https://old"), true); err != nil {
		t.Fatal(err)
	}
	if err := m.Merge(tuggyEntries("dev", "https://new"), true); err != nil {
		t.Fatal(err)
	}
	if got := load(t, path).Clusters["tuggy-dev"].Server; got != "https://new" {
		t.Errorf("server = %q, want the recreated cluster's address", got)
	}
}

func TestMergeRefusesOtherNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, existing)
	entries := tuggyEntries("dev", "https://34.1.2.3")
	entries.Clusters["work"] = &clientcmdapi.Cluster{Server: "https://evil"}

	err := manager(t, path).Merge(entries, true)
	if err == nil || !strings.Contains(err.Error(), "must be named tuggy-*, got work") {
		t.Errorf("err = %v", err)
	}
	if load(t, path).Clusters["work"].Server != "https://work.example.com" {
		t.Error("a non-tuggy entry was overwritten")
	}
}

func TestMergeWithKubeconfigList(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	writeFile(t, second, existing) // first doesn't exist
	m := manager(t, first, second)

	if err := m.Merge(tuggyEntries("dev", "https://34.1.2.3"), true); err != nil {
		t.Fatal(err)
	}
	// kubectl writes new entries to the first existing file in the list.
	cfg := load(t, second)
	if cfg.Contexts["tuggy-dev"] == nil || cfg.Contexts["work"] == nil {
		t.Errorf("contexts in %s: %v", second, cfg.Contexts)
	}
	if _, err := os.Stat(first); err == nil {
		t.Error("tuggy should not create a file kubectl wouldn't write to")
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, existing)
	m := manager(t, path)
	if err := m.Merge(tuggyEntries("dev", "https://34.1.2.3"), true); err != nil {
		t.Fatal(err)
	}
	if err := m.Merge(tuggyEntries("prod", "https://35.1.2.3"), false); err != nil {
		t.Fatal(err)
	}

	if err := m.Remove("tuggy-dev"); err != nil {
		t.Fatal(err)
	}
	cfg := load(t, path)
	if cfg.Contexts["tuggy-dev"] != nil || cfg.Clusters["tuggy-dev"] != nil || cfg.AuthInfos["tuggy-dev"] != nil {
		t.Error("tuggy-dev entries should be gone")
	}
	if cfg.Contexts["tuggy-prod"] == nil || cfg.Contexts["work"] == nil || cfg.Clusters["kind-local"] == nil {
		t.Error("other entries must be kept")
	}
	if cfg.CurrentContext != "" {
		t.Errorf("current context = %q, want none after removing the current one", cfg.CurrentContext)
	}

	if err := m.Remove("tuggy-dev"); err != nil {
		t.Errorf("removing again should be a no-op: %v", err)
	}
	if err := m.Remove("work"); err == nil {
		t.Error("tuggy must refuse to remove entries it didn't add")
	}
}

func TestRemoveKeepsSharedCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, existing)
	m := manager(t, path)
	entries := tuggyEntries("dev", "https://34.1.2.3")
	entries.Contexts["tuggy-dev-admin"] = &clientcmdapi.Context{Cluster: "tuggy-dev", AuthInfo: "tuggy-dev"}
	if err := m.Merge(entries, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("tuggy-dev"); err != nil {
		t.Fatal(err)
	}
	if cfg := load(t, path); cfg.Clusters["tuggy-dev"] == nil {
		t.Error("a cluster still used by another context must be kept")
	}
}

func TestSerialize(t *testing.T) {
	out, err := Serialize(tuggyEntries("dev", "https://34.1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"current-context: tuggy-dev", "server: https://34.1.2.3", "command: gke-gcloud-auth-plugin"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
