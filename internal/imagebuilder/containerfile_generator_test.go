package imagebuilder

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/stretchr/testify/require"
)

// generateTestCertAndKey generates a valid test certificate and private key in PEM format
func generateTestCertAndKey(t *testing.T) (certPEM string, keyPEM string) {
	// Generate private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-cert",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	// Create self-signed certificate
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	// Encode certificate to PEM
	certPEMBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})
	require.NotNil(t, certPEMBlock)

	// Encode private key to PEM
	keyPEMBytes, err := fccrypto.PEMEncodeKey(privateKey)
	require.NoError(t, err)

	return string(certPEMBlock), string(keyPEMBytes)
}

func TestGetFlightctlConfigCommands(t *testing.T) {
	require := require.New(t)

	t.Run("with enrollment cert and key", func(t *testing.T) {
		certPEM, keyPEM := generateTestCertAndKey(t)

		generator := &ContainerfileGenerator{
			spec: api.ImageBuildSpec{
				FlightctlConfig: &api.FlightctlAgentConfig{},
			},
			enrollmentCert: certPEM,
			enrollmentKey:  keyPEM,
		}

		cmds, err := generator.getFlightctlConfigCommands()
		require.NoError(err)
		require.NotEmpty(cmds)

		// Check that both cert and key files are created
		hasCertCmd := false
		hasKeyCmd := false
		hasChmodCmd := false

		for _, cmd := range cmds {
			if containsStr(cmd, "enrollment-cert.pem") {
				hasCertCmd = true
			}
			if containsStr(cmd, "enrollment-key.pem") {
				hasKeyCmd = true
			}
			if containsStr(cmd, "chmod 600 /etc/flightctl/enrollment-key.pem") {
				hasChmodCmd = true
			}
		}

		require.True(hasCertCmd, "expected command to create enrollment-cert.pem")
		require.True(hasKeyCmd, "expected command to create enrollment-key.pem")
		require.True(hasChmodCmd, "expected command to chmod enrollment-key.pem")
	})

	t.Run("without enrollment cert and key", func(t *testing.T) {
		generator := &ContainerfileGenerator{
			spec: api.ImageBuildSpec{
				FlightctlConfig: &api.FlightctlAgentConfig{},
			},
			enrollmentCert: "",
			enrollmentKey:  "",
		}

		cmds, err := generator.getFlightctlConfigCommands()
		require.NoError(err)
		require.NotEmpty(cmds)

		// Check that cert and key files are NOT created
		hasCertCmd := false
		hasKeyCmd := false

		for _, cmd := range cmds {
			if containsStr(cmd, "enrollment-cert.pem") {
				hasCertCmd = true
			}
			if containsStr(cmd, "enrollment-key.pem") {
				hasKeyCmd = true
			}
		}

		require.False(hasCertCmd, "should not create enrollment-cert.pem when not provided")
		require.False(hasKeyCmd, "should not create enrollment-key.pem when not provided")
	})
}

func TestBuildFlightctlConfigYaml(t *testing.T) {
	require := require.New(t)

	t.Run("with enrollment cert and key", func(t *testing.T) {
		certPEM, keyPEM := generateTestCertAndKey(t)

		generator := &ContainerfileGenerator{
			spec: api.ImageBuildSpec{
				FlightctlConfig: &api.FlightctlAgentConfig{},
			},
			enrollmentCert:         certPEM,
			enrollmentKey:          keyPEM,
			defaultEnrollmentURL:   "https://example.com:7443",
			defaultEnrollmentCA:    "dummy-ca",
			defaultEnrollmentUIURL: "https://ui.example.com:8080",
		}

		config := generator.buildFlightctlConfigYaml()
		require.Contains(config, "enrollment-service:")
		require.Contains(config, "client-certificate: /etc/flightctl/enrollment-cert.pem")
		require.Contains(config, "client-key: /etc/flightctl/enrollment-key.pem")
		require.NotContains(config, "client-certificate-data:")
		require.NotContains(config, "client-key-data:")
	})

	t.Run("without enrollment cert and key (preview mode)", func(t *testing.T) {
		generator := &ContainerfileGenerator{
			spec: api.ImageBuildSpec{
				FlightctlConfig: &api.FlightctlAgentConfig{},
			},
			enrollmentCert:         "",
			enrollmentKey:          "",
			defaultEnrollmentURL:   "https://example.com:7443",
			defaultEnrollmentCA:    "dummy-ca",
			defaultEnrollmentUIURL: "https://ui.example.com:8080",
		}

		config := generator.buildFlightctlConfigYaml()
		require.Contains(config, "enrollment-service:")
		require.Contains(config, "client-certificate-data: <ENROLLMENT_CERTIFICATE_WILL_BE_GENERATED_DURING_BUILD>")
		require.Contains(config, "client-key-data: <ENROLLMENT_KEY_WILL_BE_GENERATED_DURING_BUILD>")
	})
}

// Helper function to check if a string contains a substring
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
