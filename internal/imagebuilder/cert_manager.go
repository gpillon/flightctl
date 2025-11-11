package imagebuilder

import (
	"context"
	"crypto"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// CertificateManager handles enrollment certificate requests
type CertificateManager struct {
	client *apiclient.ClientWithResponses
	log    logrus.FieldLogger
}

// NewCertificateManager creates a new certificate manager
func NewCertificateManager(client *apiclient.ClientWithResponses, log logrus.FieldLogger) *CertificateManager {
	return &CertificateManager{
		client: client,
		log:    log,
	}
}

// RequestEnrollmentCertificate requests an enrollment certificate from the flightctl API
// This mimics what the CLI does with: flightctl certificate request --signer=enrollment --expiration=365d --output=embedded
// Returns the certificate and the private key in PEM format as separate strings
func (cm *CertificateManager) RequestEnrollmentCertificate(ctx context.Context, imageBuildName string, expirationDays int32) (cert string, key string, err error) {
	cm.log.Infof("Requesting enrollment certificate for image build %s", imageBuildName)

	// Generate a unique name for the CSR (this will be used as metadata.name and must match the CSR CN)
	csrName := fmt.Sprintf("imagebuild-%s-%s", imageBuildName, uuid.New().String()[:8])

	// Generate CSR data using the CSR name as CommonName (must match metadata.name for enrollment signer)
	csrData, privateKeyPEM, err := generateCSRData(csrName)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate CSR data: %w", err)
	}

	// Create a certificate signing request
	csr := &api.CertificateSigningRequest{
		ApiVersion: api.CertificateSigningRequestAPIVersion,
		Kind:       api.CertificateSigningRequestKind,
		Metadata: api.ObjectMeta{
			Name: &csrName,
		},
		Spec: api.CertificateSigningRequestSpec{
			SignerName:        "flightctl.io/enrollment", // Enrollment signer (auto-approved for CSR service)
			Request:           csrData,
			ExpirationSeconds: &expirationDays,
			Usages:            &[]string{"clientAuth", "CA:false"}, // Required usages for enrollment certificate
		},
	}

	// Submit the CSR
	resp, err := cm.client.CreateCertificateSigningRequestWithResponse(ctx, api.CreateCertificateSigningRequestJSONRequestBody(*csr))
	if err != nil {
		return "", "", fmt.Errorf("failed to create certificate signing request: %w", err)
	}
	if resp.StatusCode() != 201 {
		errorMsg := fmt.Sprintf("failed to create CSR, status code: %d", resp.StatusCode())
		if resp.JSON400 != nil {
			errorMsg = fmt.Sprintf("%s: %s", errorMsg, resp.JSON400.Message)
		} else if resp.Body != nil {
			errorMsg = fmt.Sprintf("%s, body: %s", errorMsg, string(resp.Body))
		}
		return "", "", fmt.Errorf("%w: %s", err, errorMsg)
	}

	createdCSR := resp.JSON201
	cm.log.Infof("Created CSR %s, waiting for approval and signing", *createdCSR.Metadata.Name)

	// Wait for the certificate to be issued
	certificate, err := cm.waitForCertificate(ctx, *createdCSR.Metadata.Name, 5*time.Minute)
	if err != nil {
		return "", "", fmt.Errorf("failed to get certificate: %w", err)
	}

	cm.log.Infof("Successfully obtained enrollment certificate for image build %s", imageBuildName)

	// Return certificate and private key as separate strings
	return certificate, string(privateKeyPEM), nil
}

// waitForCertificate polls the CSR until a certificate is issued or timeout occurs
func (cm *CertificateManager) waitForCertificate(ctx context.Context, csrName string, timeout time.Duration) (string, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeoutCh := time.After(timeout)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeoutCh:
			return "", fmt.Errorf("timeout waiting for certificate for CSR %s", csrName)
		case <-ticker.C:
			// Use the List endpoint with name filter as a workaround
			resp, err := cm.client.ListCertificateSigningRequestsWithResponse(ctx, &api.ListCertificateSigningRequestsParams{})
			if err != nil {
				cm.log.WithError(err).Warnf("Failed to read CSR %s", csrName)
				continue
			}
			if resp.StatusCode() != 200 {
				cm.log.Warnf("Failed to list CSRs, status code: %d", resp.StatusCode())
				continue
			}

			// Find the CSR by name
			var csr *api.CertificateSigningRequest
			if resp.JSON200 != nil {
				for i := range resp.JSON200.Items {
					item := &resp.JSON200.Items[i]
					if item.Metadata.Name != nil && *item.Metadata.Name == csrName {
						csr = item
						break
					}
				}
			}
			if csr == nil {
				cm.log.Warnf("CSR %s not found", csrName)
				continue
			}

			// Check if certificate is available
			if csr.Status != nil && csr.Status.Certificate != nil && len(*csr.Status.Certificate) > 0 {
				return string(*csr.Status.Certificate), nil
			}

			// Check if CSR was denied or failed
			if csr.Status != nil && csr.Status.Conditions != nil {
				for _, condition := range csr.Status.Conditions {
					if condition.Type == "Denied" && condition.Status == "True" {
						return "", fmt.Errorf("CSR %s was denied: %s", csrName, condition.Message)
					}
					if condition.Type == "Failed" && condition.Status == "True" {
						return "", fmt.Errorf("CSR %s failed: %s", csrName, condition.Message)
					}
				}
			}

			cm.log.Debugf("CSR %s not yet signed, waiting...", csrName)
		}
	}
}

// generateCSRData generates a valid CSR PEM for enrollment certificate
// The commonName parameter should be the CSR metadata.name (e.g., "imagebuild-xxx-yyy")
// The signer will add the appropriate prefix (client-enrollment-) during signing
// Returns both the CSR and the private key in PEM format
func generateCSRData(commonName string) ([]byte, []byte, error) {
	// Generate a private key
	_, privateKey, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Convert to crypto.Signer interface
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("private key does not implement crypto.Signer")
	}

	// Generate CSR with the CommonName matching the metadata.name
	// The signer will add the client-enrollment- prefix during signing if needed
	csrPEM, err := fccrypto.MakeCSR(signer, commonName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate CSR: %w", err)
	}

	// Encode the private key to PEM format
	privateKeyPEM, err := fccrypto.PEMEncodeKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to encode private key to PEM: %w", err)
	}

	return csrPEM, privateKeyPEM, nil
}

// CleanupCertificate deletes the CSR after use
func (cm *CertificateManager) CleanupCertificate(ctx context.Context, csrName string) error {
	// Note: In production you would implement CSR deletion
	// For now, we just log that we would clean it up
	cm.log.Infof("Would cleanup CSR %s (deletion not implemented yet)", csrName)
	return nil
}
