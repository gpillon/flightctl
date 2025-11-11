package imagebuilder

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestNewOrchestrator(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	cfg := &config.Config{}
	log := logrus.New()

	// Create a test HTTP server for the API client
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	apiClient, err := apiclient.NewClientWithResponses(server.URL)
	require.NoError(err)

	// Mock GetSecret call that is made during NewOrchestrator
	mockK8sClient.EXPECT().
		GetSecret(gomock.Any(), "flightctl-external", "flightctl-ca-secret").
		Return(nil, errors.New("secret not found"))

	o := NewOrchestrator(apiClient, mockK8sClient, cfg, log)

	require.NotNil(o)
	require.Equal(apiClient, o.client)
	require.Equal(mockK8sClient, o.k8sClient)
	require.Equal(cfg, o.config)
	require.NotNil(o.certManager)
	require.NotNil(o.containerBuilder)
	require.NotNil(o.bootcBuilder)
	require.NotNil(o.storageManager)
}

func TestGetCACertData(t *testing.T) {
	require := require.New(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	cfg := &config.Config{}
	log := logrus.New()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	apiClient, err := apiclient.NewClientWithResponses(server.URL)
	require.NoError(err)

	// Mock GetSecret call that is made during NewOrchestrator
	mockK8sClient.EXPECT().
		GetSecret(gomock.Any(), "flightctl-external", "flightctl-ca-secret").
		Return(nil, errors.New("secret not found"))

	o := NewOrchestrator(apiClient, mockK8sClient, cfg, log)

	// Initially should be empty (no secret found)
	caCert := o.GetCACertData()
	require.Empty(caCert)
}

