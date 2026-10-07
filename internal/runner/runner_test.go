package runner

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSpecValidate(t *testing.T) {
	abs, err := filepath.Abs("workspace")
	if err != nil {
		t.Fatal(err)
	}
	valid := Spec{
		Image: "busybox",
		Mounts: []Mount{
			{Type: MountBind, Source: abs, Target: "/workspace"},
			{Type: MountVolume, Source: "tuggy-plugin-cache", Target: "/cache"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid spec: %v", err)
	}

	tests := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{"no image", Spec{}, "image is required"},
		{"relative bind source", Spec{Image: "x", Mounts: []Mount{{Type: MountBind, Source: "workspace", Target: "/w"}}}, "absolute host path"},
		{"relative target", Spec{Image: "x", Mounts: []Mount{{Type: MountBind, Source: abs, Target: "w"}}}, "inside the container"},
		{"windows-style target", Spec{Image: "x", Mounts: []Mount{{Type: MountBind, Source: abs, Target: `C:\w`}}}, "inside the container"},
		{"volume without name", Spec{Image: "x", Mounts: []Mount{{Type: MountVolume, Target: "/c"}}}, "volume name"},
		{"unknown type", Spec{Image: "x", Mounts: []Mount{{Type: "tmpfs", Source: "x", Target: "/c"}}}, "unknown mount type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCallerUser(t *testing.T) {
	got := CallerUser()
	if runtime.GOOS != "linux" {
		if got != "" {
			t.Errorf("CallerUser() = %q on %s, want empty", got, runtime.GOOS)
		}
		return
	}
	if parts := strings.Split(got, ":"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Errorf("CallerUser() = %q, want uid:gid", got)
	}
}
