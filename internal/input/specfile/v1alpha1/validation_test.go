package v1alpha1

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

// validCluster returns a defaulted cluster that passes validation.
func validCluster() *Cluster {
	c := NewCluster("dev", "gke")
	c.Spec.PlatformConfig = json.RawMessage(`{"project":"acme-dev"}`)
	SetDefaults(c)
	return c
}

func TestSetDefaults(t *testing.T) {
	c := &Cluster{Metadata: ObjectMeta{Name: "dev"}, Spec: ClusterSpec{Platform: "gke"}}
	SetDefaults(c)

	if c.APIVersion != APIVersion || c.Kind != KindCluster {
		t.Errorf("type meta = %+v, want %s %s", c.TypeMeta, APIVersion, KindCluster)
	}
	if len(c.Spec.NodePools) != 1 || c.Spec.NodePools[0].Name != DefaultNodePoolName {
		t.Fatalf("node pools = %+v, want one pool named %q", c.Spec.NodePools, DefaultNodePoolName)
	}
	if c.Spec.NodePools[0].Count == nil || *c.Spec.NodePools[0].Count != 1 {
		t.Errorf("default pool count = %v, want 1", c.Spec.NodePools[0].Count)
	}
	if c.Spec.TTL != nil {
		t.Errorf("TTL = %v, want nil (never expires)", c.Spec.TTL)
	}
}

func TestSetDefaultsKeepsUserValues(t *testing.T) {
	c := NewCluster("dev", "gke")
	c.Spec.NodePools = []NodePool{
		{Name: "big", Count: ptr(int32(5))},
		{Name: "zero", Count: ptr(int32(0))},
		{Name: "unset"},
	}
	SetDefaults(c)

	got := []int32{*c.Spec.NodePools[0].Count, *c.Spec.NodePools[1].Count, *c.Spec.NodePools[2].Count}
	want := []int32{5, 0, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pool %d count = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestValidateValid(t *testing.T) {
	tests := map[string]func(c *Cluster){
		"defaults only":          func(*Cluster) {},
		"no platform config":     func(c *Cluster) { c.Spec.PlatformConfig = nil },
		"null platform config":   func(c *Cluster) { c.Spec.PlatformConfig = json.RawMessage("null") },
		"minor version":          func(c *Cluster) { c.Spec.KubernetesVersion = "1.34" },
		"patch version":          func(c *Cluster) { c.Spec.KubernetesVersion = "1.34.1" },
		"platform version":       func(c *Cluster) { c.Spec.KubernetesVersion = "1.34.1-gke.1200" },
		"ttl at minimum":         func(c *Cluster) { c.Spec.TTL = &Duration{MinTTL} },
		"ttl zero means forever": func(c *Cluster) { c.Spec.TTL = &Duration{} },
		"max length name":        func(c *Cluster) { c.Metadata.Name = "a" + strings.Repeat("b", MaxNameLength-1) },
		"one empty pool among others": func(c *Cluster) {
			c.Spec.NodePools = append(c.Spec.NodePools, NodePool{Name: "spare", Count: ptr(int32(0))})
		},
		"disk at minimum": func(c *Cluster) { c.Spec.NodePools[0].DiskSizeGB = MinDiskSizeGB },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := validCluster()
			mutate(c)
			if err := c.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestValidateInvalid(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(c *Cluster)
		wantField string
		wantMsg   string
	}{
		{"wrong apiVersion", func(c *Cluster) { c.APIVersion = "tuggy.dev/v1" }, "apiVersion", "must be"},
		{"wrong kind", func(c *Cluster) { c.Kind = "NodePool" }, "kind", "must be"},
		{"missing name", func(c *Cluster) { c.Metadata.Name = "" }, "metadata.name", "is required"},
		{"uppercase name", func(c *Cluster) { c.Metadata.Name = "Dev" }, "metadata.name", "lowercase"},
		{"name starts with digit", func(c *Cluster) { c.Metadata.Name = "1dev" }, "metadata.name", "start with a letter"},
		{"name ends with dash", func(c *Cluster) { c.Metadata.Name = "dev-" }, "metadata.name", "end with a letter or digit"},
		{"name with underscore", func(c *Cluster) { c.Metadata.Name = "my_dev" }, "metadata.name", "lowercase"},
		{"name too long", func(c *Cluster) { c.Metadata.Name = strings.Repeat("a", MaxNameLength+1) }, "metadata.name", "maximum is 40"},
		{"missing platform", func(c *Cluster) { c.Spec.Platform = "" }, "spec.platform", "is required"},
		{"bad platform", func(c *Cluster) { c.Spec.Platform = "Google Cloud" }, "spec.platform", "not a valid platform"},
		{"bad version", func(c *Cluster) { c.Spec.KubernetesVersion = "latest" }, "spec.kubernetesVersion", "not a version"},
		{"negative ttl", func(c *Cluster) { c.Spec.TTL = &Duration{-time.Hour} }, "spec.ttl", "negative"},
		{"ttl too short", func(c *Cluster) { c.Spec.TTL = &Duration{10 * time.Minute} }, "spec.ttl", "at least 30m"},
		{"no node pools", func(c *Cluster) { c.Spec.NodePools = nil }, "spec.nodePools", "at least one node pool"},
		{"pool without name", func(c *Cluster) { c.Spec.NodePools[0].Name = "" }, "spec.nodePools[0].name", "is required"},
		{"duplicate pool", func(c *Cluster) {
			c.Spec.NodePools = append(c.Spec.NodePools, NodePool{Name: DefaultNodePoolName, Count: ptr(int32(1))})
		}, "spec.nodePools[1].name", "already used by spec.nodePools[0]"},
		{"negative count", func(c *Cluster) { c.Spec.NodePools[0].Count = ptr(int32(-1)) }, "spec.nodePools[0].count", "negative"},
		{"zero nodes in total", func(c *Cluster) { c.Spec.NodePools[0].Count = ptr(int32(0)) }, "spec.nodePools", "at least one node in total"},
		{"disk too small", func(c *Cluster) { c.Spec.NodePools[0].DiskSizeGB = 5 }, "spec.nodePools[0].diskSizeGB", "at least 10"},
		{"platform config list", func(c *Cluster) { c.Spec.PlatformConfig = json.RawMessage(`["a"]`) }, "spec.platformConfig", "key: value"},
		{"platform config scalar", func(c *Cluster) { c.Spec.PlatformConfig = json.RawMessage(`"gke"`) }, "spec.platformConfig", "key: value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validCluster()
			tt.mutate(c)
			err := c.Validate()

			var verr ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Validate() = %v, want ValidationError", err)
			}
			for _, fe := range verr {
				if fe.Field == tt.wantField && strings.Contains(fe.Message, tt.wantMsg) {
					return
				}
			}
			t.Errorf("Validate() = %v, want an error on %s containing %q", err, tt.wantField, tt.wantMsg)
		})
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	c := validCluster()
	c.Metadata.Name = "Bad_Name"
	c.Spec.Platform = ""
	c.Spec.NodePools[0].DiskSizeGB = 1

	err := c.Validate()
	var verr ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Validate() = %v, want ValidationError", err)
	}
	if len(verr) != 3 {
		t.Errorf("got %d errors, want 3:\n%v", len(verr), err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "invalid cluster spec:\n  - metadata.name:") {
		t.Errorf("unexpected message format:\n%s", msg)
	}
}
