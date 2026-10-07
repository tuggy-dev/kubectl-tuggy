// Package version reports build information for the tuggy binary.
//
// Release builds set Version, Commit and Date with -ldflags. Builds made with
// plain "go build" or "go install" fall back to the module and VCS information
// the Go toolchain embeds.
package version

import (
	"runtime"
	"runtime/debug"
)

// Set at build time with -ldflags "-X github.com/tuggy-dev/kubectl-tuggy/internal/version.Version=...".
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

const devVersion = "v0.0.0-dev"

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`

	// TofuImage is the OpenTofu image this build runs. Filled in by the CLI.
	TofuImage string `json:"tofuImage,omitempty"`
}

// Get returns build information, filling gaps from the embedded build info.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			}
		}
	}

	if info.Version == "" {
		info.Version = devVersion
	}
	if len(info.Commit) > 12 {
		info.Commit = info.Commit[:12]
	}
	return info
}
