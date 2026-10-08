// Package module embeds the GKE OpenTofu module into the tuggy binary.
//
// The .tf files here are the module itself; tests/ holds `tofu test` tests
// that run with a mock Google provider and are not embedded.
package module

import "embed"

// FS holds the module files and the provider lock file, which pins the
// Google provider version and checksums.
//
//go:embed *.tf .terraform.lock.hcl
var FS embed.FS
