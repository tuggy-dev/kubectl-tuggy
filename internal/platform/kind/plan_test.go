package kind

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/kind/pkg/apis/config/defaults"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, err := decodeConfig(&v1alpha1.ClusterSpec{Platform: Name})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlPlanes != 1 || cfg.APIServerPort != 0 || cfg.NodeImage != "" {
		t.Errorf("defaults = %+v", cfg)
	}
}

func TestDecodeConfigErrors(t *testing.T) {
	tests := []struct {
		name, config, field string
	}{
		{"two control planes", `{"controlPlanes":2}`, "spec.platformConfig.controlPlanes"},
		{"bad api port", `{"apiServerPort":70000}`, "spec.platformConfig.apiServerPort"},
		{"mount without host path", `{"extraMounts":[{"containerPath":"/mnt/data"}]}`, "spec.platformConfig.extraMounts[0].hostPath"},
		{"relative container path", `{"extraMounts":[{"hostPath":"./data","containerPath":"data"}]}`, "spec.platformConfig.extraMounts[0].containerPath"},
		{"bad container port", `{"extraPortMappings":[{"containerPort":0}]}`, "spec.platformConfig.extraPortMappings[0].containerPort"},
		{"bad protocol", `{"extraPortMappings":[{"containerPort":80,"protocol":"icmp"}]}`, "spec.platformConfig.extraPortMappings[0].protocol"},
		{"duplicate host port", `{"extraPortMappings":[{"containerPort":80,"hostPort":8080},{"containerPort":81,"hostPort":8080}]}`, "spec.platformConfig.extraPortMappings[1].hostPort"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeConfig(&v1alpha1.ClusterSpec{Platform: Name, PlatformConfig: json.RawMessage(tt.config)})
			var verr v1alpha1.ValidationError
			if !errors.As(err, &verr) || !hasField(verr, tt.field) {
				t.Errorf("error = %v, want one for %s", err, tt.field)
			}
		})
	}
}

func TestDecodeConfigUnknownField(t *testing.T) {
	_, err := decodeConfig(&v1alpha1.ClusterSpec{Platform: Name, PlatformConfig: json.RawMessage(`{"project":"acme"}`)})
	if err == nil || !strings.Contains(err.Error(), "project") {
		t.Errorf("a gke setting under kind should fail, got %v", err)
	}
}

func hasField(errs v1alpha1.ValidationError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}

func TestVariablesFromSpec(t *testing.T) {
	c := testCluster(`{"controlPlanes":3,"apiServerPort":6443,
		"extraMounts":[{"hostPath":"data","containerPath":"/mnt/data","readOnly":true}],
		"extraPortMappings":[{"containerPort":30080,"hostPort":8080}]}`)
	two := int32(2)
	c.Spec.NodePools = []v1alpha1.NodePool{{Name: "default", Count: &two}}
	cfg, err := decodeConfig(&c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	v, err := variablesFromSpec(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if v.ClusterName != "tuggy-dev" || v.NodeImage != defaults.Image || v.ControlPlanes != 3 || v.APIServerPort != 6443 {
		t.Errorf("variables = %+v", v)
	}
	if len(v.ExtraMounts) != 1 || !filepath.IsAbs(v.ExtraMounts[0].HostPath) {
		t.Errorf("mount host path should be made absolute: %+v", v.ExtraMounts)
	}
	if len(v.ExtraPortMappings) != 1 || v.ExtraPortMappings[0].Protocol != "TCP" {
		t.Errorf("port mappings = %+v", v.ExtraPortMappings)
	}
}

func TestVariablesRejectCloudSettings(t *testing.T) {
	c := testCluster("")
	c.Spec.NodePools[0].MachineType = "e2-standard-2"
	c.Spec.NodePools[0].Spot = true
	c.Spec.NodePools[0].DiskSizeGB = 50
	cfg, _ := decodeConfig(&c.Spec)
	_, err := variablesFromSpec(c, cfg)
	var verr v1alpha1.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v", err)
	}
	for _, f := range []string{"spec.nodePools[0].machineType", "spec.nodePools[0].spot", "spec.nodePools[0].diskSizeGB"} {
		if !hasField(verr, f) {
			t.Errorf("missing error for %s in %v", f, err)
		}
	}
}

func TestVariablesImage(t *testing.T) {
	tests := []struct {
		name, version, config, image, errField string
	}{
		{name: "default", image: defaults.Image},
		{name: "minor", version: "1.37", image: nodeImages["1.37.0"]},
		{name: "exact", version: "1.37.0", image: nodeImages["1.37.0"]},
		{name: "custom image", config: `{"nodeImage":"me/node:dev"}`, image: "me/node:dev"},
		{name: "unknown version", version: "1.20", errField: "spec.kubernetesVersion"},
		{name: "image and version", version: "1.37", config: `{"nodeImage":"me/node:dev"}`, errField: "spec.platformConfig.nodeImage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testCluster(tt.config)
			c.Spec.KubernetesVersion = tt.version
			cfg, err := decodeConfig(&c.Spec)
			if err != nil {
				t.Fatal(err)
			}
			v, err := variablesFromSpec(c, cfg)
			if tt.errField != "" {
				var verr v1alpha1.ValidationError
				if !errors.As(err, &verr) || !hasField(verr, tt.errField) {
					t.Errorf("error = %v, want one for %s", err, tt.errField)
				}
				return
			}
			if err != nil || v.NodeImage != tt.image {
				t.Errorf("image = %q (%v), want %q", v.NodeImage, err, tt.image)
			}
		})
	}
}

func TestDefaultImageIsListed(t *testing.T) {
	for _, img := range nodeImages {
		if img == defaults.Image {
			return
		}
	}
	t.Errorf("nodeImages does not list the kind release's default image %s; update images.go", defaults.Image)
}

func TestKindConfig(t *testing.T) {
	v := Variables{
		ClusterName:       "tuggy-dev",
		NodeImage:         "kindest/node:v1.37.0",
		ControlPlanes:     3,
		APIServerPort:     6443,
		NodePools:         []NodePool{{Name: "default", Count: 2}, {Name: "gpu", Count: 1}},
		ExtraMounts:       []Mount{{HostPath: "/home/me/data", ContainerPath: "/mnt/data", ReadOnly: true}},
		ExtraPortMappings: []PortMapping{{ContainerPort: 30080, HostPort: 8080, Protocol: "TCP"}},
	}
	cfg := kindConfig(v)
	if cfg.Name != "tuggy-dev" || cfg.Networking.APIServerPort != 6443 || len(cfg.Nodes) != 6 {
		t.Fatalf("config = %+v", cfg)
	}
	var roles []string
	for i, n := range cfg.Nodes {
		roles = append(roles, string(n.Role))
		if len(n.ExtraMounts) != 1 || !n.ExtraMounts[0].Readonly {
			t.Errorf("node %d mounts = %+v", i, n.ExtraMounts)
		}
		if wantPorts := i == 0; (len(n.ExtraPortMappings) == 1) != wantPorts {
			t.Errorf("node %d port mappings = %+v; only the first control plane gets them", i, n.ExtraPortMappings)
		}
	}
	want := []string{"control-plane", "control-plane", "control-plane", "worker", "worker", "worker"}
	if !slices.Equal(roles, want) {
		t.Errorf("roles = %v, want %v", roles, want)
	}
	if cfg.Nodes[3].Labels[LabelNodePool] != "default" || cfg.Nodes[5].Labels[LabelNodePool] != "gpu" {
		t.Errorf("worker labels = %v, %v", cfg.Nodes[3].Labels, cfg.Nodes[5].Labels)
	}
	if cfg.Nodes[0].ExtraPortMappings[0].Protocol != v1alpha4.PortMappingProtocolTCP {
		t.Errorf("protocol = %q", cfg.Nodes[0].ExtraPortMappings[0].Protocol)
	}
}

func TestPlannedNodes(t *testing.T) {
	one := plannedNodes(Variables{ClusterName: "tuggy-dev", ControlPlanes: 1, NodePools: []NodePool{{Name: "default", Count: 2}}})
	if want := []string{"tuggy-dev-control-plane", "tuggy-dev-worker", "tuggy-dev-worker2"}; !slices.Equal(one, want) {
		t.Errorf("nodes = %v, want %v", one, want)
	}
	ha := plannedNodes(Variables{ClusterName: "tuggy-dev", ControlPlanes: 3, NodePools: []NodePool{{Name: "default", Count: 1}}})
	if len(ha) != 5 || ha[1] != "tuggy-dev-control-plane2" || ha[4] != "tuggy-dev-external-load-balancer" {
		t.Errorf("nodes = %v", ha)
	}
}

func TestImageFor(t *testing.T) {
	if img, err := imageFor("v1.37"); err != nil || img != nodeImages["1.37.0"] {
		t.Errorf("v1.37 = %q, %v", img, err)
	}
	_, err := imageFor("1.99")
	if err == nil || !strings.Contains(err.Error(), "supported versions: 1.37.0") {
		t.Errorf("unknown version error = %v", err)
	}
}
