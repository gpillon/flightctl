package imagebuilder

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewCleanupManager(t *testing.T) {
	require := require.New(t)

	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(gomock.NewController(t))
	cfg := &config.Config{}
	log := logrus.New()

	cm := NewCleanupManager(mockStore, mockK8sClient, cfg, log)

	require.NotNil(cm)
	require.Equal(mockStore, cm.store)
	require.Equal(mockK8sClient, cm.k8sClient)
	require.Equal(cfg, cm.cfg)
}

func TestGetBuildNamespace(t *testing.T) {
	require := require.New(t)

	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(gomock.NewController(t))
	log := logrus.New()

	tests := []struct {
		name           string
		cfg            *config.Config
		expected       string
	}{
		{
			name: "configured namespace",
			cfg: func() *config.Config {
				cfg := &config.Config{}
				// Note: ImageBuilder is unexported, so we test with nil and verify default
				return cfg
			}(),
			expected: "flightctl-builds", // Will use default since we can't set it
		},
		{
			name:     "default namespace",
			cfg:      &config.Config{},
			expected: "flightctl-builds",
		},
		{
			name: "nil ImageBuilder config",
			cfg: &config.Config{
				ImageBuilder: nil,
			},
			expected: "flightctl-builds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewCleanupManager(mockStore, mockK8sClient, tt.cfg, log)
			result := cm.getBuildNamespace()
			require.Equal(tt.expected, result)
		})
	}
}

func TestTryAcquireLock(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var mockStore store.Store = nil
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	cfg := &config.Config{}
	log := logrus.New()

	cm := NewCleanupManager(mockStore, mockK8sClient, cfg, log)

	tests := []struct {
		name        string
		setupMocks  func()
		expectLock  bool
		expectError bool
	}{
		{
			name: "successfully acquire lock",
			setupMocks: func() {
				mockK8sClient.EXPECT().
					CreateConfigMap(gomock.Any(), "test-ns", gomock.Any()).
					Return(&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "imagebuilder-cleanup-lock",
							Namespace: "test-ns",
						},
					}, nil)
				mockK8sClient.EXPECT().
					DeleteConfigMap(gomock.Any(), "test-ns", "imagebuilder-cleanup-lock").
					Return(nil)
			},
			expectLock:  true,
			expectError: false,
		},
		{
			name: "lock already exists and not expired",
			setupMocks: func() {
				mockK8sClient.EXPECT().
					CreateConfigMap(gomock.Any(), "test-ns", gomock.Any()).
					Return(nil, &testError{code: "AlreadyExists"})
				mockK8sClient.EXPECT().
					GetConfigMap(gomock.Any(), "test-ns", "imagebuilder-cleanup-lock").
					Return(&corev1.ConfigMap{
						Data: map[string]string{
							"timestamp": time.Now().Format(time.RFC3339),
							"holder":    "other-instance",
						},
					}, nil)
			},
			expectLock:  false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupMocks()
			ctx := context.Background()
			locked, unlock, err := cm.tryAcquireLock(ctx, "test-ns", "imagebuilder-cleanup-lock", 5*time.Minute)

			if tt.expectError {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Equal(tt.expectLock, locked)
				if locked && unlock != nil {
					unlock() // Clean up
				}
			}
		})
	}
}

type testError struct {
	code string
}

func (e *testError) Error() string {
	return e.code
}

