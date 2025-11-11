package imagebuilder

import (
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestNewContainerBuilder(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()

	cb := NewContainerBuilder(mockK8sClient, "test-namespace", "quay.io", log)

	require.NotNil(cb)
	require.Equal(mockK8sClient, cb.k8sClient)
	require.Equal("test-namespace", cb.namespace)
	require.Equal("quay.io", cb.defaultRegistry)
}

func TestGenerateImageName(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()

	cb := NewContainerBuilder(mockK8sClient, "test-namespace", "quay.io", log)

	tests := []struct {
		name             string
		imageBuild       *api.ImageBuild
		expectedContains string // String that should be in result
	}{
		{
			name: "registry URL with full image name",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test-build"; return &s }(),
				},
				Spec: api.ImageBuildSpec{
					ContainerRegistry: &api.ContainerRegistryConfig{
						Url: "localhost:5000/myimage:v1",
					},
				},
			},
			expectedContains: "localhost:5000/myimage:v1",
		},
		{
			name: "registry URL without tag",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test-build"; return &s }(),
				},
				Spec: api.ImageBuildSpec{
					ContainerRegistry: &api.ContainerRegistryConfig{
						Url: "localhost:5000/myimage",
					},
				},
			},
			expectedContains: "localhost:5000/myimage:",
		},
		{
			name: "registry URL with only host",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test-build"; return &s }(),
				},
				Spec: api.ImageBuildSpec{
					ContainerRegistry: &api.ContainerRegistryConfig{
						Url: "localhost:5000",
					},
				},
			},
			expectedContains: "localhost:5000/test-build:",
		},
		{
			name: "no registry specified",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test-build"; return &s }(),
				},
				Spec: api.ImageBuildSpec{},
			},
			expectedContains: "test-build:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cb.generateImageName(tt.imageBuild)
			require.NotEmpty(result)
			require.Contains(result, tt.expectedContains)
		})
	}
}

func TestContainerBuilder_GetBaseImageRegistry(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	log := logrus.New()

	cb := NewContainerBuilder(mockK8sClient, "test-namespace", "quay.io", log)

	tests := []struct {
		name      string
		baseImage string
		expected  string
	}{
		{
			name:      "quay.io image",
			baseImage: "quay.io/centos-bootc/centos-bootc:stream9",
			expected:  "quay.io",
		},
		{
			name:      "docker hub image",
			baseImage: "ubuntu:22.04",
			expected:  "docker.io",
		},
		{
			name:      "localhost with port",
			baseImage: "localhost:5000/myimage:latest",
			expected:  "localhost:5000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cb.getBaseImageRegistry(tt.baseImage)
			require.Equal(tt.expected, result)
		})
	}
}

