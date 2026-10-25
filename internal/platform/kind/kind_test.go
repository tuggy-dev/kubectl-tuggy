package kind

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

func TestPlatformConfigFromFlags(t *testing.T) {
	p := New()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	p.AddFlags(fs)
	if err := fs.Parse([]string{"--node-image", "kindest/node:v1.37.0", "--control-planes", "3"}); err != nil {
		t.Fatal(err)
	}
	raw, err := p.PlatformConfigFromFlags(fs)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.NodeImage != "kindest/node:v1.37.0" || cfg.ControlPlanes != 3 || cfg.APIServerPort != 0 {
		t.Errorf("config = %s", raw)
	}
}

func TestPlatformConfigFromNoFlags(t *testing.T) {
	p := New()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	p.AddFlags(fs)
	raw, err := p.PlatformConfigFromFlags(fs)
	if err != nil || string(raw) != "{}" {
		t.Errorf("unset flags should give {}, got %s (%v)", raw, err)
	}
}

func TestFromV1Alpha1(t *testing.T) {
	c := testCluster("")
	c.Spec.TTL = &v1alpha1.Duration{Duration: 8 * time.Hour}
	pl, err := New().FromV1Alpha1(c)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Name != "dev" || pl.Platform != Name || pl.TTL != 8*time.Hour {
		t.Errorf("plan = %+v", pl)
	}
	if _, ok := pl.Variables.(Variables); !ok {
		t.Errorf("variables = %T", pl.Variables)
	}
}
