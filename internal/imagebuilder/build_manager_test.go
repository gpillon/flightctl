package imagebuilder

import (
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestNewBuildManager(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Note: Store interface doesn't have a mock, so we'll test with nil
	// In real usage, a concrete Store implementation would be used
	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	mockOrchestrator := &Orchestrator{}
	cfg := &config.Config{}
	log := logrus.New()

	bm := NewBuildManager(mockStore, mockOrchestrator, mockK8sClient, cfg, log)

	require.NotNil(bm)
	require.Equal(mockStore, bm.store)
	require.Equal(mockOrchestrator, bm.orchestrator)
	require.Equal(mockK8sClient, bm.k8sClient)
	require.NotNil(bm.recentlyCancelled)
}

func TestShouldProcessBuild(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	mockOrchestrator := &Orchestrator{}
	cfg := &config.Config{}
	log := logrus.New()

	bm := NewBuildManager(mockStore, mockOrchestrator, mockK8sClient, cfg, log)

	tests := []struct {
		name      string
		imageBuild *api.ImageBuild
		expected  bool
	}{
		{
			name: "nil status should process",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
				},
				Status: nil,
			},
			expected: true,
		},
		{
			name: "nil phase should process",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
				},
				Status: &api.ImageBuildStatus{},
			},
			expected: true,
		},
		{
			name: "Pending phase should process",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Pending"); return &p }(),
				},
			},
			expected: true,
		},
		{
			name: "Building phase should not process",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Building"); return &p }(),
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bm.shouldProcessBuild(tt.imageBuild)
			require.Equal(tt.expected, result)
		})
	}
}

func TestShouldCancelBuild(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	mockOrchestrator := &Orchestrator{}
	cfg := &config.Config{}
	log := logrus.New()

	bm := NewBuildManager(mockStore, mockOrchestrator, mockK8sClient, cfg, log)

	cancelTrue := "true"
	cancelFalse := "false"

	tests := []struct {
		name      string
		imageBuild *api.ImageBuild
		expected  bool
	}{
		{
			name: "Building phase with cancel annotation should cancel",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
					Annotations: &map[string]string{
						"imagebuilder.flightctl.io/cancel": cancelTrue,
					},
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Building"); return &p }(),
				},
			},
			expected: true,
		},
		{
			name: "Pushing phase with cancel annotation should cancel",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
					Annotations: &map[string]string{
						"imagebuilder.flightctl.io/cancel": cancelTrue,
					},
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Pushing"); return &p }(),
				},
			},
			expected: true,
		},
		{
			name: "Completed phase with cancel annotation should not cancel",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
					Annotations: &map[string]string{
						"imagebuilder.flightctl.io/cancel": cancelTrue,
					},
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Completed"); return &p }(),
				},
			},
			expected: false,
		},
		{
			name: "Building phase without cancel annotation should not cancel",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
					Annotations: &map[string]string{
						"imagebuilder.flightctl.io/cancel": cancelFalse,
					},
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Building"); return &p }(),
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bm.shouldCancelBuild(tt.imageBuild)
			require.Equal(tt.expected, result)
		})
	}
}

func TestShouldRetryBuild(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	mockOrchestrator := &Orchestrator{}
	cfg := &config.Config{}
	log := logrus.New()

	bm := NewBuildManager(mockStore, mockOrchestrator, mockK8sClient, cfg, log)

	retryTrue := "true"
	retryFalse := "false"

	tests := []struct {
		name      string
		imageBuild *api.ImageBuild
		expected  bool
	}{
		{
			name: "Failed phase with retry annotation should retry",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
					Annotations: &map[string]string{
						"imagebuilder.flightctl.io/retry": retryTrue,
					},
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Failed"); return &p }(),
				},
			},
			expected: true,
		},
		{
			name: "Completed phase with retry annotation should not retry",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
					Annotations: &map[string]string{
						"imagebuilder.flightctl.io/retry": retryTrue,
					},
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Completed"); return &p }(),
				},
			},
			expected: false,
		},
		{
			name: "Failed phase without retry annotation should not retry",
			imageBuild: &api.ImageBuild{
				Metadata: api.ObjectMeta{
					Name: func() *string { s := "test"; return &s }(),
					Annotations: &map[string]string{
						"imagebuilder.flightctl.io/retry": retryFalse,
					},
				},
				Status: &api.ImageBuildStatus{
					Phase: func() *api.ImageBuildStatusPhase { p := api.ImageBuildStatusPhase("Failed"); return &p }(),
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bm.shouldRetryBuild(tt.imageBuild)
			require.Equal(tt.expected, result)
		})
	}
}

func TestCleanupRecentlyCancelled(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	mockOrchestrator := &Orchestrator{}
	cfg := &config.Config{}
	log := logrus.New()

	bm := NewBuildManager(mockStore, mockOrchestrator, mockK8sClient, cfg, log)

	// Add an old entry (more than 2 minutes old)
	bm.recentlyCancelled["old-build"] = time.Now().Add(-3 * time.Minute)
	// Add a recent entry (less than 2 minutes old)
	bm.recentlyCancelled["recent-build"] = time.Now().Add(-1 * time.Minute)

	bm.cleanupRecentlyCancelled()

	require.NotContains(bm.recentlyCancelled, "old-build")
	require.Contains(bm.recentlyCancelled, "recent-build")
}

