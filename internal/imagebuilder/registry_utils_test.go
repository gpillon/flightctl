package imagebuilder

import (
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

func TestNormalizeRegistryURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "with https prefix",
			input:    "https://registry.example.com",
			expected: "registry.example.com",
		},
		{
			name:     "with http prefix",
			input:    "http://registry.example.com",
			expected: "registry.example.com",
		},
		{
			name:     "with trailing slash",
			input:    "registry.example.com/",
			expected: "registry.example.com",
		},
		{
			name:     "with https and trailing slash",
			input:    "https://registry.example.com/",
			expected: "registry.example.com",
		},
		{
			name:     "localhost with port",
			input:    "localhost:5000",
			expected: "localhost:5000",
		},
		{
			name:     "already normalized",
			input:    "quay.io",
			expected: "quay.io",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeRegistryURL(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeRegistryURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetRegistryURL(t *testing.T) {
	tests := []struct {
		name     string
		build    *api.ImageBuild
		expected string
	}{
		{
			name: "with registry URL",
			build: &api.ImageBuild{
				Spec: api.ImageBuildSpec{
					ContainerRegistry: &api.ContainerRegistryConfig{
						Url: "https://registry.example.com/",
					},
				},
			},
			expected: "registry.example.com",
		},
		{
			name: "without registry",
			build: &api.ImageBuild{
				Spec: api.ImageBuildSpec{},
			},
			expected: DefaultRegistry,
		},
		{
			name: "with nil registry",
			build: &api.ImageBuild{
				Spec: api.ImageBuildSpec{
					ContainerRegistry: nil,
				},
			},
			expected: DefaultRegistry,
		},
		{
			name: "with empty URL",
			build: &api.ImageBuild{
				Spec: api.ImageBuildSpec{
					ContainerRegistry: &api.ContainerRegistryConfig{
						Url: "",
					},
				},
			},
			expected: DefaultRegistry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetRegistryURL(tt.build)
			if result != tt.expected {
				t.Errorf("GetRegistryURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGetBaseImageRegistry(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		expected string
	}{
		{
			name:     "quay.io with tag",
			imageRef: "quay.io/centos-bootc/centos-bootc:stream9",
			expected: "quay.io",
		},
		{
			name:     "localhost with port",
			imageRef: "localhost:5000/myimage:latest",
			expected: "localhost:5000",
		},
		{
			name:     "docker hub official image",
			imageRef: "ubuntu:22.04",
			expected: DockerHubRegistry,
		},
		{
			name:     "docker hub organization",
			imageRef: "centos/stream9",
			expected: DockerHubRegistry,
		},
		{
			name:     "gcr.io registry",
			imageRef: "gcr.io/project/image:tag",
			expected: "gcr.io",
		},
		{
			name:     "private registry with port and path",
			imageRef: "registry.example.com:443/org/repo/image:v1.0",
			expected: "registry.example.com:443",
		},
		{
			name:     "image with digest",
			imageRef: "quay.io/myorg/myimage@sha256:abcdef123456",
			expected: "quay.io",
		},
		{
			name:     "image without tag",
			imageRef: "docker.io/library/nginx",
			expected: "docker.io",
		},
		{
			name:     "localhost without port",
			imageRef: "localhost/myimage",
			expected: "localhost",
		},
		{
			name:     "simple image name",
			imageRef: "nginx",
			expected: DockerHubRegistry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetBaseImageRegistry(tt.imageRef)
			if result != tt.expected {
				t.Errorf("GetBaseImageRegistry(%q) = %q, want %q", tt.imageRef, result, tt.expected)
			}
		})
	}
}

func TestIsFullImageReference(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		expected bool
	}{
		{
			name:     "full reference with registry",
			imageRef: "quay.io/myorg/myimage:tag",
			expected: true,
		},
		{
			name:     "localhost with port",
			imageRef: "localhost:5000/myimage",
			expected: true,
		},
		{
			name:     "localhost without port",
			imageRef: "localhost/myimage",
			expected: true,
		},
		{
			name:     "docker hub official",
			imageRef: "ubuntu:22.04",
			expected: false,
		},
		{
			name:     "docker hub organization",
			imageRef: "centos/stream9",
			expected: false,
		},
		{
			name:     "registry with subdomain",
			imageRef: "registry.example.com/image",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsFullImageReference(tt.imageRef)
			if result != tt.expected {
				t.Errorf("IsFullImageReference(%q) = %v, want %v", tt.imageRef, result, tt.expected)
			}
		})
	}
}
