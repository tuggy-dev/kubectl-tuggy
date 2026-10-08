package gke

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform/gke/module"
)

var fixedNow = time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)

func testPlatform() *Platform {
	p := New()
	p.Now = func() time.Time { return fixedNow }
	return p
}

func loadSpec(t *testing.T, yaml string) *v1alpha1.Cluster {
	t.Helper()
	c, err := v1alpha1.Load(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	v1alpha1.SetDefaults(c)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

const minimalSpec = `
apiVersion: tuggy.dev/v1alpha1
kind: Cluster
metadata: {name: dev}
spec:
  platform: gke
  platformConfig: {project: acme-dev, location: us-central1-a}
`

const fullSpec = `
apiVersion: tuggy.dev/v1alpha1
kind: Cluster
metadata: {name: prod}
spec:
  platform: gke
  kubernetesVersion: "1.34"
  ttl: 8h
  nodePools:
    - {name: default, machineType: e2-standard-4, count: 3}
    - {name: batch, count: 0, spot: true, diskSizeGB: 200}
  platformConfig:
    project: acme-prod
    location: us-central1
    releaseChannel: STABLE
    network: tuggy-vpc
    subnetwork: tuggy-subnet
    impersonateServiceAccount: opentofu@acme-prod.iam.gserviceaccount.com
`

func TestFromV1Alpha1Minimal(t *testing.T) {
	plan, err := testPlatform().FromV1Alpha1(loadSpec(t, minimalSpec))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "dev" || plan.Platform != "gke" || plan.TTL != 0 {
		t.Errorf("plan = %+v", plan)
	}
	want := Variables{
		Name:           "dev",
		Project:        "acme-dev",
		Location:       "us-central1-a",
		ReleaseChannel: "REGULAR",
		Network:        "default",
		NodePools:      []NodePool{{Name: "default", MachineType: DefaultMachineType, Count: 1}},
		Labels:         map[string]string{"managed-by": "tuggy", "tuggy-cluster": "dev"},
	}
	if got := plan.Variables.(Variables); !reflect.DeepEqual(got, want) {
		t.Errorf("variables:\n got %+v\nwant %+v", got, want)
	}
}

func TestFromV1Alpha1Full(t *testing.T) {
	plan, err := testPlatform().FromV1Alpha1(loadSpec(t, fullSpec))
	if err != nil {
		t.Fatal(err)
	}
	if plan.TTL != 8*time.Hour {
		t.Errorf("TTL = %v", plan.TTL)
	}
	got, err := json.MarshalIndent(plan.Variables, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "variables-full.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, append(got, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("variables differ from %s:\n%s", golden, got)
	}
}

func TestFromV1Alpha1InvalidConfig(t *testing.T) {
	spec := loadSpec(t, minimalSpec)
	spec.Spec.PlatformConfig = json.RawMessage(`{"project":"acme-dev"}`)
	if _, err := testPlatform().FromV1Alpha1(spec); err == nil || !strings.Contains(err.Error(), "spec.platformConfig.location") {
		t.Errorf("err = %v", err)
	}
}

// TestVariablesMatchModule is the contract between Go and OpenTofu: every
// variable the module declares must be a field of Variables and the other
// way round, with the same for the node pool object's attributes.
func TestVariablesMatchModule(t *testing.T) {
	src, err := fs.ReadFile(module.FS, "variables.tf")
	if err != nil {
		t.Fatal(err)
	}

	declared := regexp.MustCompile(`(?m)^variable "([a-z_]+)"`).FindAllStringSubmatch(string(src), -1)
	var moduleVars []string
	for _, m := range declared {
		moduleVars = append(moduleVars, m[1])
	}
	if diff := symmetricDiff(moduleVars, jsonFields(Variables{})); diff != "" {
		t.Errorf("Variables and variables.tf differ: %s", diff)
	}

	poolBlock := regexp.MustCompile(`(?s)variable "node_pools".*?object\(\{(.*?)\}\)`).FindStringSubmatch(string(src))
	if poolBlock == nil {
		t.Fatal("node_pools object type not found in variables.tf")
	}
	var poolAttrs []string
	for _, m := range regexp.MustCompile(`(?m)^\s*([a-z_]+)\s*=`).FindAllStringSubmatch(poolBlock[1], -1) {
		poolAttrs = append(poolAttrs, m[1])
	}
	if diff := symmetricDiff(poolAttrs, jsonFields(NodePool{})); diff != "" {
		t.Errorf("NodePool and the node_pools object differ: %s", diff)
	}
}

func jsonFields(v any) []string {
	var names []string
	typ := reflect.TypeOf(v)
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		names = append(names, name)
	}
	return names
}

func symmetricDiff(module, goFields []string) string {
	var parts []string
	for _, m := range module {
		if !slices.Contains(goFields, m) {
			parts = append(parts, "missing in Go: "+m)
		}
	}
	for _, g := range goFields {
		if !slices.Contains(module, g) {
			parts = append(parts, "missing in module: "+g)
		}
	}
	return strings.Join(parts, "; ")
}
