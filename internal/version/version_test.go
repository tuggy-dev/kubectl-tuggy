package version

import (
	"runtime"
	"testing"
)

func TestGetDefaults(t *testing.T) {
	info := Get()
	if info.Version == "" {
		t.Error("Version should never be empty")
	}
	if info.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; info.Platform != want {
		t.Errorf("Platform = %q, want %q", info.Platform, want)
	}
}

func TestGetUsesLdflags(t *testing.T) {
	saved := [3]string{Version, Commit, Date}
	t.Cleanup(func() { Version, Commit, Date = saved[0], saved[1], saved[2] })

	Version, Commit, Date = "v1.2.3", "0123456789abcdef", "2026-10-06T00:00:00Z"
	info := Get()
	if info.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", info.Version)
	}
	if info.Commit != "0123456789ab" {
		t.Errorf("Commit = %q, want it shortened to 12 characters", info.Commit)
	}
	if info.Date != Date {
		t.Errorf("Date = %q, want %q", info.Date, Date)
	}
}
