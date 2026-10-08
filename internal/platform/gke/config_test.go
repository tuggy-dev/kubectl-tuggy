package gke

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tuggy-dev/kubectl-tuggy/internal/input/specfile/v1alpha1"
)

func specWith(config string) *v1alpha1.ClusterSpec {
	return &v1alpha1.ClusterSpec{Platform: Name, PlatformConfig: json.RawMessage(config)}
}

func TestDecodeConfigDefaults(t *testing.T) {
	cfg, err := decodeConfig(specWith(`{"project":"acme-dev","location":"us-central1-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReleaseChannel != "REGULAR" || cfg.Network != "default" || cfg.Subnetwork != "" || cfg.ImpersonateServiceAccount != "" {
		t.Errorf("defaults = %+v", cfg)
	}
	if cfg.Regional() {
		t.Error("us-central1-a is a zone")
	}
}

func TestDecodeConfigAllFields(t *testing.T) {
	cfg, err := decodeConfig(specWith(`{
		"project": "acme-dev",
		"location": "europe-west4",
		"releaseChannel": "stable",
		"network": "tuggy-vpc",
		"subnetwork": "tuggy-subnet",
		"impersonateServiceAccount": "opentofu@acme-dev.iam.gserviceaccount.com"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReleaseChannel != "STABLE" {
		t.Errorf("release channel should be upper-cased, got %q", cfg.ReleaseChannel)
	}
	if !cfg.Regional() {
		t.Error("europe-west4 is a region")
	}
}

func TestDecodeConfigErrors(t *testing.T) {
	tests := []struct {
		name, config, field, msg string
	}{
		{"missing project", `{"location":"us-central1-a"}`, "spec.platformConfig.project", "is required"},
		{"bad project", `{"project":"Acme_Dev","location":"us-central1-a"}`, "spec.platformConfig.project", "not a valid project ID"},
		{"missing location", `{"project":"acme-dev"}`, "spec.platformConfig.location", "is required"},
		{"bad location", `{"project":"acme-dev","location":"Iowa"}`, "spec.platformConfig.location", "not a zone"},
		{"bad channel", `{"project":"acme-dev","location":"us-central1","releaseChannel":"FAST"}`, "spec.platformConfig.releaseChannel", "not one of"},
		{"bad service account", `{"project":"acme-dev","location":"us-central1","impersonateServiceAccount":"me@gmail.com"}`, "spec.platformConfig.impersonateServiceAccount", "not a service account"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeConfig(specWith(tt.config))
			var verr v1alpha1.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
			for _, fe := range verr {
				if fe.Field == tt.field && strings.Contains(fe.Message, tt.msg) {
					return
				}
			}
			t.Errorf("err = %v, want %s: %s", err, tt.field, tt.msg)
		})
	}
}

func TestDecodeConfigRejectsUnknownFields(t *testing.T) {
	_, err := decodeConfig(specWith(`{"project":"acme-dev","location":"us-central1","zone":"x"}`))
	if err == nil || !strings.Contains(err.Error(), `unknown field "zone"`) {
		t.Errorf("err = %v", err)
	}
}
