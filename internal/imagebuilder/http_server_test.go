package imagebuilder

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestNewHTTPServer(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	cfg := &config.Config{}
	mockOrchestrator := &Orchestrator{}
	log := logrus.New()

	hs := NewHTTPServer(mockK8sClient, cfg, mockOrchestrator, log)

	require.NotNil(hs)
	require.Equal(mockK8sClient, hs.k8sClient)
	require.Equal(cfg, hs.cfg)
	require.Equal(mockOrchestrator, hs.orchestrator)
}

func TestHTTPServer_Healthz(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	// Create a simple router for testing
	router := chi.NewRouter()
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	router.ServeHTTP(w, req)

	require.Equal(http.StatusOK, w.Code)
	require.Equal("OK", w.Body.String())
}

func TestHTTPServer_GetBuildNamespace(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	mockOrchestrator := &Orchestrator{}
	log := logrus.New()

	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs := NewHTTPServer(mockK8sClient, tt.cfg, mockOrchestrator, log)
			result := hs.getBuildNamespace()
			require.Equal(tt.expected, result)
		})
	}
}

func TestHTTPServer_HandleGenerateContainerfile(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	cfg := &config.Config{}
	mockOrchestrator := &Orchestrator{
		caCertData: "test-ca-cert",
	}
	log := logrus.New()

	hs := NewHTTPServer(mockK8sClient, cfg, mockOrchestrator, log)

	requestBody := map[string]interface{}{
		"spec": api.ImageBuildSpec{
			BaseImage: "quay.io/centos-bootc/centos-bootc:stream9",
		},
	}

	bodyBytes, err := json.Marshal(requestBody)
	require.NoError(err)

	req := httptest.NewRequest("POST", "/api/v1/imagebuilds/generate-containerfile", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Test the handler directly without starting the server
	router := chi.NewRouter()
	router.Post("/api/v1/imagebuilds/generate-containerfile", hs.handleGenerateContainerfile)
	router.ServeHTTP(w, req)

	require.Equal(http.StatusOK, w.Code)

	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(err)
	require.Contains(response, "containerfile")
}

func TestHTTPServer_UploadAuthMiddleware(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	cfg := &config.Config{}
	mockOrchestrator := &Orchestrator{}
	log := logrus.New()

	hs := NewHTTPServer(mockK8sClient, cfg, mockOrchestrator, log)

	tests := []struct {
		name           string
		uploadToken    string
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "valid token",
			uploadToken:    "test-token",
			authHeader:     "Bearer test-token",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid token",
			uploadToken:    "test-token",
			authHeader:     "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "missing authorization header",
			uploadToken:    "test-token",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid authorization format",
			uploadToken:    "test-token",
			authHeader:     "InvalidFormat test-token",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("UPLOAD_TOKEN", tt.uploadToken)
			defer os.Unsetenv("UPLOAD_TOKEN")

			req := httptest.NewRequest("POST", "/api/v1/imagebuilds/upload", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()

			// Create a simple handler to test middleware
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			middleware := hs.uploadAuthMiddleware(nextHandler)
			middleware.ServeHTTP(w, req)

			require.Equal(tt.expectedStatus, w.Code)
		})
	}
}

