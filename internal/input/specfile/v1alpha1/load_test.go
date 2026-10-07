package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fullSpec = `
apiVersion: tuggy.dev/v1alpha1
kind: Cluster
metadata:
  name: dev
spec:
  platform: gke
  kubernetesVersion: "1.34"
  ttl: 8h
  nodePools:
    - name: default
      machineType: e2-standard-2
      count: 2
      spot: true
      diskSizeGB: 50
  platformConfig:
    project: acme-dev
    location: us-central1-a
`

func TestLoadFullSpec(t *testing.T) {
	c, err := Load(strings.NewReader(fullSpec))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	SetDefaults(c)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if c.Metadata.Name != "dev" || c.Spec.Platform != "gke" || c.Spec.KubernetesVersion != "1.34" {
		t.Errorf("unexpected metadata or spec: %+v", c)
	}
	if c.Spec.TTL == nil || c.Spec.TTL.Duration != 8*time.Hour {
		t.Errorf("TTL = %v, want 8h", c.Spec.TTL)
	}
	np := c.Spec.NodePools[0]
	if np.MachineType != "e2-standard-2" || *np.Count != 2 || !np.Spot || np.DiskSizeGB != 50 {
		t.Errorf("node pool = %+v", np)
	}

	var cfg struct {
		Project  string `json:"project"`
		Location string `json:"location"`
	}
	if err := c.Spec.DecodePlatformConfig(&cfg); err != nil {
		t.Fatalf("DecodePlatformConfig: %v", err)
	}
	if cfg.Project != "acme-dev" || cfg.Location != "us-central1-a" {
		t.Errorf("platform config = %+v", cfg)
	}
}

func TestLoadMinimalSpec(t *testing.T) {
	c, err := Load(strings.NewReader(`
apiVersion: tuggy.dev/v1alpha1
kind: Cluster
metadata:
  name: dev
spec:
  platform: gke
  ttl: 0
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	SetDefaults(c)
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.Spec.TTL == nil || c.Spec.TTL.Duration != 0 {
		t.Errorf("TTL = %v, want 0", c.Spec.TTL)
	}
}

func TestLoadErrors(t *testing.T) {
	header := "apiVersion: tuggy.dev/v1alpha1\nkind: Cluster\nmetadata:\n  name: dev\n"
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"empty", "", "empty"},
		{"only whitespace", "  \n\n", "empty"},
		{"not yaml", "spec: [unclosed", "not valid YAML"},
		{"missing kind", "apiVersion: tuggy.dev/v1alpha1\n", `kind must be "Cluster"`},
		{"other kind", "apiVersion: v1\nkind: Pod\n", `kind must be "Cluster", got "Pod"`},
		{"other version", "apiVersion: tuggy.dev/v1\nkind: Cluster\n", `apiVersion must be "tuggy.dev/v1alpha1"`},
		{"misspelled field", header + "spec:\n  platform: gke\n  nodePool:\n    - name: a\n", `unknown field "spec.nodePool"`},
		{"misspelled nested field", header + "spec:\n  platform: gke\n  nodePools:\n    - name: a\n      cnt: 2\n", `unknown field "spec.nodePools[0].cnt"`},
		{"duplicate field", header + "spec:\n  platform: gke\n  platform: eks\n", "platform"},
		{"ttl without unit", header + "spec:\n  platform: gke\n  ttl: 8\n", "needs a unit"},
		{"bad ttl", header + "spec:\n  platform: gke\n  ttl: soon\n", "invalid duration"},
		{"two documents", header + "spec:\n  platform: gke\n---\n" + header, "single document"},
		{"too large", header + "# " + strings.Repeat("x", maxSpecSize), "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(tt.input))
			if err == nil {
				t.Fatalf("Load succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadAllowsLeadingDocumentSeparator(t *testing.T) {
	_, err := Load(strings.NewReader("# my cluster\n---\napiVersion: tuggy.dev/v1alpha1\nkind: Cluster\nmetadata:\n  name: dev\nspec:\n  platform: gke\n"))
	if err != nil {
		t.Errorf("Load with a leading '---' should succeed, got %v", err)
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(path, []byte(fullSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Errorf("LoadFile: %v", err)
	}

	_, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Error("LoadFile of a missing file should fail")
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("kind: Pod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(bad); err == nil || !strings.HasPrefix(err.Error(), bad+":") {
		t.Errorf("LoadFile error should start with the file path, got %v", err)
	}
}

func TestExampleSpecsAreValid(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "docs", "examples", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no example specs found in docs/examples")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			c, err := LoadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			SetDefaults(c)
			if err := c.Validate(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestDecodePlatformConfig(t *testing.T) {
	type cfg struct {
		Project string `json:"project"`
	}

	t.Run("unknown field", func(t *testing.T) {
		s := ClusterSpec{PlatformConfig: []byte(`{"project":"p","zone":"z"}`)}
		var out cfg
		err := s.DecodePlatformConfig(&out)
		if err == nil || !strings.Contains(err.Error(), `spec.platformConfig: json: unknown field "zone"`) {
			t.Errorf("got %v, want unknown field error", err)
		}
	})

	t.Run("empty leaves defaults", func(t *testing.T) {
		for _, raw := range []string{"", "null", "  "} {
			s := ClusterSpec{PlatformConfig: []byte(raw)}
			out := cfg{Project: "default"}
			if err := s.DecodePlatformConfig(&out); err != nil || out.Project != "default" {
				t.Errorf("raw %q: got %+v, %v", raw, out, err)
			}
		}
	})
}
