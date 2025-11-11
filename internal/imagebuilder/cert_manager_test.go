package imagebuilder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestNewCertificateManager(t *testing.T) {
	require := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	client, err := apiclient.NewClientWithResponses(server.URL)
	require.NoError(err)

	log := logrus.New()
	cm := NewCertificateManager(client, log)

	require.NotNil(cm)
	require.Equal(client, cm.client)
	require.Equal(log, cm.log)
}

func TestGenerateCSRData(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		commonName  string
		expectError bool
	}{
		{
			name:        "valid common name",
			commonName:  "imagebuild-test-abc123",
			expectError: false,
		},
		{
			name:        "common name with special chars",
			commonName:  "imagebuild-test-xyz-789",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			csrPEM, keyPEM, err := generateCSRData(tt.commonName)

			if tt.expectError {
				require.Error(err)
				require.Nil(csrPEM)
				require.Nil(keyPEM)
			} else {
				require.NoError(err)
				require.NotEmpty(csrPEM)
				require.NotEmpty(keyPEM)
				require.Contains(string(csrPEM), "BEGIN CERTIFICATE REQUEST")
				require.Contains(string(keyPEM), "BEGIN")
			}
		})
	}
}

func TestRequestEnrollmentCertificate(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name           string
		imageBuildName string
		expirationDays int32
		setupServer    func() *httptest.Server
		expectError    bool
		expectCert     bool
		expectKey      bool
	}{
		{
			name:           "successful certificate request",
			imageBuildName: "test-build",
			expirationDays: 365,
			setupServer: func() *httptest.Server {
				csrName := "imagebuild-test-build-abc123"
				csrCreated := &api.CertificateSigningRequest{
					ApiVersion: api.CertificateSigningRequestAPIVersion,
					Kind:       api.CertificateSigningRequestKind,
					Metadata: api.ObjectMeta{
						Name: &csrName,
					},
					Status: &api.CertificateSigningRequestStatus{},
				}

				csrList := &api.CertificateSigningRequestList{
					Items: []api.CertificateSigningRequest{
						{
							Metadata: api.ObjectMeta{
								Name: &csrName,
							},
							Status: &api.CertificateSigningRequestStatus{
								Certificate: func() *[]byte { b := []byte("-----BEGIN CERTIFICATE-----\ntest-cert\n-----END CERTIFICATE-----"); return &b }(),
							},
						},
					},
				}

				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" && strings.Contains(r.URL.Path, "certificatesigningrequests") {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusCreated)
						json.NewEncoder(w).Encode(csrCreated)
					} else if r.Method == "GET" && strings.Contains(r.URL.Path, "certificatesigningrequests") {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						json.NewEncoder(w).Encode(csrList)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			},
			expectError: false,
			expectCert: true,
			expectKey:  true,
		},
		{
			name:           "CSR creation fails",
			imageBuildName: "test-build",
			expirationDays: 365,
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" {
						w.WriteHeader(http.StatusBadRequest)
						w.Write([]byte(`{"message": "Invalid CSR"}`))
					}
				}))
			},
			expectError: true,
			expectCert: false,
			expectKey:  false,
		},
		{
			name:           "CSR denied",
			imageBuildName: "test-build",
			expirationDays: 365,
			setupServer: func() *httptest.Server {
				csrName := "imagebuild-test-build-abc123"
				csrCreated := &api.CertificateSigningRequest{
					Metadata: api.ObjectMeta{
						Name: &csrName,
					},
					Status: &api.CertificateSigningRequestStatus{},
				}

				csrDenied := &api.CertificateSigningRequestList{
					Items: []api.CertificateSigningRequest{
						{
							Metadata: api.ObjectMeta{
								Name: &csrName,
							},
							Status: &api.CertificateSigningRequestStatus{
								Conditions: []api.Condition{
									{
										Type:    api.ConditionType("Denied"),
										Status:  api.ConditionStatus("True"),
										Message: "CSR was denied",
									},
								},
							},
						},
					},
				}

				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" && strings.Contains(r.URL.Path, "certificatesigningrequests") {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusCreated)
						json.NewEncoder(w).Encode(csrCreated)
					} else if r.Method == "GET" && strings.Contains(r.URL.Path, "certificatesigningrequests") {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						json.NewEncoder(w).Encode(csrDenied)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			},
			expectError: true,
			expectCert: false,
			expectKey:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.setupServer()
			defer server.Close()

			client, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(err)

			log := logrus.New()
			cm := NewCertificateManager(client, log)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cert, key, err := cm.RequestEnrollmentCertificate(ctx, tt.imageBuildName, tt.expirationDays)

			if tt.expectError {
				require.Error(err)
				require.Empty(cert)
				require.Empty(key)
			} else {
				require.NoError(err)
				if tt.expectCert {
					require.NotEmpty(cert)
				}
				if tt.expectKey {
					require.NotEmpty(key)
				}
			}
		})
	}
}

func TestWaitForCertificate(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name        string
		csrName     string
		setupServer func() *httptest.Server
		expectError bool
		expectCert  bool
	}{
		{
			name:    "certificate issued immediately",
			csrName: "test-csr",
			setupServer: func() *httptest.Server {
				cert := "-----BEGIN CERTIFICATE-----\ntest-cert\n-----END CERTIFICATE-----"
				csrList := &api.CertificateSigningRequestList{
					Items: []api.CertificateSigningRequest{
						{
							Metadata: api.ObjectMeta{
								Name: func() *string { s := "test-csr"; return &s }(),
							},
							Status: &api.CertificateSigningRequestStatus{
								Certificate: func() *[]byte { b := []byte(cert); return &b }(),
							},
						},
					},
				}

				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(csrList)
				}))
			},
			expectError: false,
			expectCert: true,
		},
		{
			name:    "certificate not found",
			csrName: "test-csr",
			setupServer: func() *httptest.Server {
				csrList := &api.CertificateSigningRequestList{
					Items: []api.CertificateSigningRequest{},
				}

				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(csrList)
				}))
			},
			expectError: true,
			expectCert:  false,
		},
		{
			name:    "timeout waiting for certificate",
			csrName: "test-csr",
			setupServer: func() *httptest.Server {
				csrList := &api.CertificateSigningRequestList{
					Items: []api.CertificateSigningRequest{
						{
							Metadata: api.ObjectMeta{
								Name: func() *string { s := "test-csr"; return &s }(),
							},
							Status: &api.CertificateSigningRequestStatus{
								Certificate: nil,
							},
						},
					},
				}

				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(csrList)
				}))
			},
			expectError: true,
			expectCert:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.setupServer()
			defer server.Close()

			client, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(err)

			log := logrus.New()
			cm := NewCertificateManager(client, log)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cert, err := cm.waitForCertificate(ctx, tt.csrName, 10*time.Second)

			if tt.expectError {
				require.Error(err)
				require.Empty(cert)
			} else {
				require.NoError(err)
				if tt.expectCert {
					require.NotEmpty(cert)
				}
			}
		})
	}
}

func TestCleanupCertificate(t *testing.T) {
	require := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	client, err := apiclient.NewClientWithResponses(server.URL)
	require.NoError(err)

	log := logrus.New()
	cm := NewCertificateManager(client, log)

	ctx := context.Background()
	err = cm.CleanupCertificate(ctx, "test-csr")
	require.NoError(err) // Currently just logs, doesn't actually delete
}


