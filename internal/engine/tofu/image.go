// Package tofu runs OpenTofu for cloud platforms.
package tofu

import "os"

// ImageEnv overrides the OpenTofu image, for example to use an internal
// mirror. It will also be settable in ~/.tuggy/config.yaml.
const ImageEnv = "TUGGY_TOFU_IMAGE"

// DefaultImage is the official OpenTofu image, pinned by digest so every run
// of a tuggy release uses exactly the same OpenTofu even if the tag is
// re-published. The digest covers all architectures (amd64, arm64, ...).
//
// To upgrade: pick the new version, run
//
//	docker buildx imagetools inspect ghcr.io/opentofu/opentofu:<version>
//
// and copy the top-level Digest here together with the version tag.
const DefaultImage = "ghcr.io/opentofu/opentofu:1.13.1@sha256:1df996fca13806a20d5a3d3ad54acdcf76bcb1aa4ac30b5ba169b9e4cecdbcc8"

// Image returns the OpenTofu image to run: $TUGGY_TOFU_IMAGE if set,
// otherwise DefaultImage.
func Image() string {
	if image := os.Getenv(ImageEnv); image != "" {
		return image
	}
	return DefaultImage
}
