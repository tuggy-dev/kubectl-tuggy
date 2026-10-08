// Package platforms links every built-in platform into the binary.
//
// Each platform registers itself with platform.Register from its init
// function; importing it here for its side effect is all that is needed.
// This file is the only place that changes when a platform is added.
package platforms

import (
	_ "github.com/tuggy-dev/kubectl-tuggy/internal/platform/gke" // Google Kubernetes Engine
)
