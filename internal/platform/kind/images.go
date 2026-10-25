package kind

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/kind/pkg/apis/config/defaults"
)

// nodeImages maps exact Kubernetes versions to the digest-pinned node images
// published for the pinned kind release (see go.mod). Copy them from that
// release's notes ("Images pre-built for this release") when bumping kind;
// TestDefaultImageIsListed checks the release's default image is here.
var nodeImages = map[string]string{
	"1.37.0": "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5",
}

// imageFor returns the node image for a Kubernetes version: a minor version
// such as "1.37" picks its newest patch, an exact version such as "1.37.0"
// must be listed, and an empty version uses the kind release's default.
func imageFor(version string) (string, error) {
	if version == "" {
		return defaults.Image, nil
	}
	version = strings.TrimPrefix(version, "v")
	if img, ok := nodeImages[version]; ok {
		return img, nil
	}

	best, bestPatch := "", -1
	for v, img := range nodeImages {
		minor, patch, ok := splitPatch(v)
		if ok && minor == version && patch > bestPatch {
			best, bestPatch = img, patch
		}
	}
	if best != "" {
		return best, nil
	}
	return "", fmt.Errorf("no kind node image for Kubernetes %s in this tuggy; supported versions: %s (or set an exact image with nodeImage / --node-image)",
		version, strings.Join(supportedVersions(), ", "))
}

// splitPatch splits "1.37.2" into "1.37" and 2.
func splitPatch(v string) (minor string, patch int, ok bool) {
	i := strings.LastIndex(v, ".")
	if i < 0 || strings.Count(v, ".") != 2 {
		return "", 0, false
	}
	patch, err := strconv.Atoi(v[i+1:])
	if err != nil {
		return "", 0, false
	}
	return v[:i], patch, true
}

// supportedVersions lists the versions in nodeImages, oldest first.
func supportedVersions() []string {
	versions := make([]string, 0, len(nodeImages))
	for v := range nodeImages {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[i], versions[j]) })
	return versions
}

func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}
