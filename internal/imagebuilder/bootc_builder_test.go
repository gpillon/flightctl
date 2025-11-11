package imagebuilder

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestNewBootcBuilder(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()

	bb := NewBootcBuilder(mockK8sClient, "test-namespace", "http://test:9090", "test-token", log)

	require.NotNil(bb)
	require.Equal(mockK8sClient, bb.k8sClient)
	require.Equal("test-namespace", bb.namespace)
	require.Equal("http://test:9090", bb.serviceURL)
	require.Equal("test-token", bb.uploadToken)
}

func TestBuildBootcImages_NoExports(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()

	bb := NewBootcBuilder(mockK8sClient, "test-namespace", "http://test:9090", "test-token", log)

	imageBuild := &api.ImageBuild{
		Metadata: api.ObjectMeta{
			Name: func() *string { s := "test-build"; return &s }(),
		},
		Spec: api.ImageBuildSpec{
			BootcExports: nil,
		},
	}

	ctx := context.Background()
	results, err := bb.BuildBootcImages(ctx, imageBuild, "test-image:latest")

	require.NoError(err)
	require.Nil(results)
}

func TestGetArchitecture(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name     string
		arch     *api.BootcExportArchitecture
		expected string
	}{
		{
			name:     "nil architecture defaults to x86_64",
			arch:     nil,
			expected: "x86_64",
		},
		{
			name:     "x86_64 architecture",
			arch:     func() *api.BootcExportArchitecture { a := api.BootcExportArchitecture("x86_64"); return &a }(),
			expected: "x86_64",
		},
		{
			name:     "aarch64 architecture",
			arch:     func() *api.BootcExportArchitecture { a := api.BootcExportArchitecture("aarch64"); return &a }(),
			expected: "aarch64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getArchitecture(tt.arch)
			require.Equal(tt.expected, result)
		})
	}
}

func TestGetImageExtensionFromString(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name     string
		imageType string
		expected string
	}{
		{
			name:      "iso extension",
			imageType: "iso",
			expected:  "iso",
		},
		{
			name:      "qcow2 extension",
			imageType: "qcow2",
			expected:  "qcow2",
		},
		{
			name:      "vmdk extension",
			imageType: "vmdk",
			expected:  "vmdk",
		},
		{
			name:      "raw extension",
			imageType: "raw",
			expected:  "raw",
		},
		{
			name:      "ami extension maps to raw",
			imageType: "ami",
			expected:  "raw",
		},
		{
			name:      "tar extension",
			imageType: "tar",
			expected:  "tar",
		},
		{
			name:      "unknown extension returns as-is",
			imageType: "unknown",
			expected:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getImageExtensionFromString(tt.imageType)
			require.Equal(tt.expected, result)
		})
	}
}

func TestCreateBootcJob(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()

	bb := NewBootcBuilder(mockK8sClient, "test-namespace", "http://test:9090", "test-token", log)

	imageBuild := &api.ImageBuild{
		Metadata: api.ObjectMeta{
			Name: func() *string { s := "test-build"; return &s }(),
		},
		Spec: api.ImageBuildSpec{
			BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
		},
	}

	job := bb.createBootcJob("bootc-test-build-iso", "test-image:latest", "iso", "x86_64", "/output/test-build/iso", imageBuild)

	require.NotNil(job)
	require.Equal("bootc-test-build-iso", job.Name)
	require.Equal("test-namespace", job.Namespace)
	require.Equal("flightctl-imagebuilder", job.Labels["app"])
	require.Equal("test-build", job.Labels["imagebuild"])
	require.Equal("bootc", job.Labels["type"])
	require.Len(job.Spec.Template.Spec.Containers, 1)
	require.Equal("bootc-builder", job.Spec.Template.Spec.Containers[0].Name)
}

func TestCreateBootcJob_WithRegistryCredentials(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()

	bb := NewBootcBuilder(mockK8sClient, "test-namespace", "http://test:9090", "test-token", log)

	username := "testuser"
	password := "testpass"
	imageBuild := &api.ImageBuild{
		Metadata: api.ObjectMeta{
			Name: func() *string { s := "test-build"; return &s }(),
		},
		Spec: api.ImageBuildSpec{
			BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
			ContainerRegistry: &api.ContainerRegistryConfig{
				Url: "https://registry.example.com",
				Credentials: &struct {
					Password *string `json:"password,omitempty"`
					Username *string `json:"username,omitempty"`
				}{
					Username: &username,
					Password: &password,
				},
			},
		},
	}

	job := bb.createBootcJob("bootc-test-build-iso", "test-image:latest", "iso", "x86_64", "/output/test-build/iso", imageBuild)

	require.NotNil(job)
	// Check that registry secret volume is mounted
	hasRegistryVolume := false
	for _, vol := range job.Spec.Template.Spec.Volumes {
		if vol.Name == "registry-auth" {
			hasRegistryVolume = true
			require.Equal("registry-test-build", vol.Secret.SecretName)
			break
		}
	}
	require.True(hasRegistryVolume, "registry-auth volume should be present")
}

