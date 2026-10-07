package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// writeDockerConfig creates a Docker config directory with the given current
// context and contexts (name -> host).
func writeDockerConfig(t *testing.T, current string, contexts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if current != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{},"currentContext":"`+current+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, host := range contexts {
		sum := sha256.Sum256([]byte(name))
		metaDir := filepath.Join(dir, "contexts", "meta", hex.EncodeToString(sum[:]))
		if err := os.MkdirAll(metaDir, 0o700); err != nil {
			t.Fatal(err)
		}
		meta := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
		if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(meta), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolveEndpoint(t *testing.T) {
	contexts := map[string]string{
		"colima":        "unix:///Users/me/.colima/default/docker.sock",
		"desktop-linux": "unix:///Users/me/.docker/run/docker.sock",
	}

	tests := []struct {
		name    string
		env     map[string]string
		current string
		want    Endpoint
	}{
		{
			name:    "DOCKER_HOST wins over everything",
			env:     map[string]string{"DOCKER_HOST": "unix:///custom.sock", "DOCKER_CONTEXT": "colima"},
			current: "desktop-linux",
			want:    Endpoint{Host: "unix:///custom.sock", Source: SourceDockerHost},
		},
		{
			name:    "DOCKER_CONTEXT wins over current context",
			env:     map[string]string{"DOCKER_CONTEXT": "colima"},
			current: "desktop-linux",
			want:    Endpoint{Host: contexts["colima"], Context: "colima", Source: SourceDockerContext},
		},
		{
			name:    "current context from config.json",
			current: "desktop-linux",
			want:    Endpoint{Host: contexts["desktop-linux"], Context: "desktop-linux", Source: SourceCurrentCtx},
		},
		{
			name:    "context named default uses the OS default",
			current: "default",
			want:    Endpoint{Host: DefaultHost(), Source: SourceDefault},
		},
		{
			name: "no configuration uses the OS default",
			want: Endpoint{Host: DefaultHost(), Source: SourceDefault},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeDockerConfig(t, tt.current, contexts)
			got, err := ResolveEndpoint(env(tt.env), dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveEndpointErrors(t *testing.T) {
	t.Run("unknown context", func(t *testing.T) {
		dir := writeDockerConfig(t, "gone", nil)
		_, err := ResolveEndpoint(env(nil), dir)
		if err == nil || !strings.Contains(err.Error(), `docker context "gone" not found`) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("broken config.json", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveEndpoint(env(nil), dir); err == nil {
			t.Error("want error for unreadable Docker config")
		}
	})

	t.Run("missing config directory is fine", func(t *testing.T) {
		got, err := ResolveEndpoint(env(nil), filepath.Join(t.TempDir(), "missing"))
		if err != nil || got.Source != SourceDefault {
			t.Errorf("got %+v, %v", got, err)
		}
	})
}

func TestEndpointString(t *testing.T) {
	if s := (Endpoint{Host: "unix:///x.sock", Context: "colima"}).String(); s != `unix:///x.sock (context "colima")` {
		t.Errorf("String() = %q", s)
	}
	if s := (Endpoint{Host: "unix:///x.sock"}).String(); s != "unix:///x.sock" {
		t.Errorf("String() = %q", s)
	}
}

func TestDefaultConfigDir(t *testing.T) {
	if got := DefaultConfigDir(env(map[string]string{"DOCKER_CONFIG": "/etc/docker-cfg"})); got != "/etc/docker-cfg" {
		t.Errorf("with DOCKER_CONFIG: %q", got)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if got := DefaultConfigDir(env(nil)); got != filepath.Join(home, ".docker") {
			t.Errorf("default: %q", got)
		}
	}
}
