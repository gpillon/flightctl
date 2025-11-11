package imagebuilder

import (
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

const (
	// DefaultRegistry is the default registry used when none is specified
	DefaultRegistry = "localhost:5000"
	// DockerHubRegistry is the default registry for Docker Hub images
	DockerHubRegistry = "docker.io"
)

// GetRegistryURL extracts and normalizes the registry URL from an ImageBuild spec.
// It removes http(s):// prefixes and trailing slashes.
// Returns DefaultRegistry if no registry is specified.
func GetRegistryURL(imageBuild *api.ImageBuild) string {
	if imageBuild.Spec.ContainerRegistry == nil || imageBuild.Spec.ContainerRegistry.Url == "" {
		return DefaultRegistry
	}
	return NormalizeRegistryURL(imageBuild.Spec.ContainerRegistry.Url)
}

// NormalizeRegistryURL normalizes a registry URL by removing protocol prefixes and trailing slashes
func NormalizeRegistryURL(registryURL string) string {
	// Remove protocol prefixes
	registry := strings.TrimPrefix(registryURL, "https://")
	registry = strings.TrimPrefix(registry, "http://")
	// Remove trailing slash
	registry = strings.TrimSuffix(registry, "/")
	return registry
}

// GetBaseImageRegistry extracts the registry URL from a container image reference.
// Format: [registry/]repository[:tag|@digest]
// Examples:
//   - "quay.io/myorg/myimage:tag" -> "quay.io"
//   - "localhost:5000/myimage" -> "localhost:5000"
//   - "myimage:tag" -> "docker.io"
//   - "centos/stream9" -> "docker.io"
func GetBaseImageRegistry(imageRef string) string {
	// Remove tag or digest if present to simplify parsing
	// Split by @ first (for digest), then by : (for tag)
	imageWithoutDigest := strings.Split(imageRef, "@")[0]

	// Parse the image reference
	parts := strings.Split(imageWithoutDigest, "/")

	if len(parts) == 1 {
		// No registry specified (e.g., "centos:stream9" or "ubuntu")
		return DockerHubRegistry
	}

	// Check if first part looks like a registry (contains . or : or is "localhost")
	firstPart := parts[0]
	if strings.Contains(firstPart, ".") ||
		strings.Contains(firstPart, ":") ||
		strings.EqualFold(firstPart, "localhost") {
		return firstPart
	}

	// Otherwise it's likely a Docker Hub organization/user (e.g., "centos/stream9")
	return DockerHubRegistry
}

// IsFullImageReference checks if an image reference includes a registry
func IsFullImageReference(imageRef string) bool {
	parts := strings.Split(imageRef, "/")
	if len(parts) == 1 {
		return false
	}

	firstPart := parts[0]
	return strings.Contains(firstPart, ".") ||
		strings.Contains(firstPart, ":") ||
		strings.EqualFold(firstPart, "localhost")
}
