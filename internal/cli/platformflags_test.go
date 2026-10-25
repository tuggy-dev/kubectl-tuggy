package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
	"github.com/tuggy-dev/kubectl-tuggy/internal/platform/platformtest"
)

// flagFake is a fake platform with its own flag, like gke's --project.
type flagFake struct {
	*platformtest.Fake
	flag string
}

var _ platform.FlagBinder = (*flagFake)(nil)

func (f *flagFake) AddFlags(fs *pflag.FlagSet) {
	fs.String(f.flag, "", "a setting ("+f.Name()+")")
}

func (f *flagFake) PlatformConfigFromFlags(fs *pflag.FlagSet) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

// newTwoPlatformEnv is a test tuggy with two platforms: "cloud", which owns
// --project and --location, and "local", which owns --node-image.
func newTwoPlatformEnv(t *testing.T) (*testEnv, *platformtest.Fake, *platformtest.Fake) {
	t.Helper()
	e := newEnv(t)
	cloud := &flagFake{Fake: platformtest.New("cloud"), flag: "project"}
	local := &flagFake{Fake: platformtest.New("local"), flag: "node-image"}
	reg := platform.NewRegistry()
	reg.Register(cloud)
	reg.Register(local)
	e.app.Platforms = reg
	return e, cloud.Fake, local.Fake
}

func TestPlatformInference(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		platform string
		stderr   string
	}{
		{name: "owner of the flags", args: []string{"--project", "acme"}, platform: "cloud"},
		{name: "other owner", args: []string{"--node-image", "img"}, platform: "local"},
		{name: "explicit", args: []string{"--platform", "local"}, platform: "local"},
		{name: "nothing to go on", args: nil, wantCode: ExitUsage, stderr: "--platform is required (one of cloud, local)"},
		{name: "flags of two platforms", args: []string{"--project", "acme", "--node-image", "img"}, wantCode: ExitUsage, stderr: "flags for cloud and local were given"},
		{name: "another platform's flag", args: []string{"--platform", "local", "--project", "acme"}, wantCode: ExitUsage, stderr: "--project is a cloud flag; this cluster uses local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, cloud, local := newTwoPlatformEnv(t)
			code, out, errOut := e.run(t, append([]string{"create", "cluster", "dev"}, tt.args...)...)
			if code != tt.wantCode {
				t.Fatalf("exit %d, want %d\n%s%s", code, tt.wantCode, out, errOut)
			}
			if tt.stderr != "" && !strings.Contains(errOut, tt.stderr) {
				t.Errorf("stderr missing %q:\n%s", tt.stderr, errOut)
			}
			if tt.platform == "" {
				return
			}
			rec, err := e.app.Store.Get("dev")
			if err != nil || rec.Platform != tt.platform {
				t.Errorf("record = %+v (%v), want platform %s", rec, err, tt.platform)
			}
			used := map[string]*platformtest.Fake{"cloud": cloud, "local": local}[tt.platform]
			if len(used.Calls()) == 0 {
				t.Errorf("%s was not used", tt.platform)
			}
		})
	}
}

func TestPlatformFlagsRejectedWithFile(t *testing.T) {
	e, _, _ := newTwoPlatformEnv(t)
	file := filepath.Join(t.TempDir(), "cluster.yaml")
	writeFile(t, file, "apiVersion: tuggy.dev/v1alpha1\nkind: Cluster\nmetadata:\n  name: dev\nspec:\n  platform: local\n")
	code, _, errOut := e.run(t, "create", "cluster", "-f", file, "--node-image", "img")
	if code != ExitUsage || !strings.Contains(errOut, "--node-image cannot be combined with -f") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
}

func TestDoctorLocationDefaultOnlyForItsPlatform(t *testing.T) {
	e := newEnv(t)
	cloud := &locationFake{flagFake{Fake: platformtest.New("cloud"), flag: "project"}}
	local := &flagFake{Fake: platformtest.New("local"), flag: "node-image"}
	reg := platform.NewRegistry()
	reg.Register(cloud)
	reg.Register(local)
	e.app.Platforms = reg

	// The location default belongs to cloud, so it must not make doctor
	// for local fail as "a cloud flag".
	if code, out, errOut := e.run(t, "doctor", "--platform", "local"); code != ExitOK {
		t.Errorf("doctor --platform local: exit %d\n%s%s", code, out, errOut)
	}
	if code, out, errOut := e.run(t, "doctor", "--project", "acme"); code != ExitOK {
		t.Errorf("doctor --project acme: exit %d\n%s%s", code, out, errOut)
	}
}

// locationFake also owns --location, like gke.
type locationFake struct{ flagFake }

func (f *locationFake) AddFlags(fs *pflag.FlagSet) {
	f.flagFake.AddFlags(fs)
	fs.String("location", "", "where ("+f.Name()+")")
}
