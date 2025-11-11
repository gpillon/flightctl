package imagebuilder

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

// Orchestrator coordinates the entire image build process
type Orchestrator struct {
	certManager      *CertificateManager
	containerBuilder *ContainerBuilder
	bootcBuilder     *BootcBuilder
	storageManager   *StorageManager
	client           *apiclient.ClientWithResponses
	k8sClient        k8sclient.K8SClient
	config           *config.Config
	log              logrus.FieldLogger
	caCertData       string // CA certificate for enrollment service
}

// NewOrchestrator creates a new build orchestrator
func NewOrchestrator(
	apiClient *apiclient.ClientWithResponses,
	k8sClient k8sclient.K8SClient,
	cfg *config.Config,
	log logrus.FieldLogger,
) *Orchestrator {
	namespace := "default"
	defaultRegistry := ""
	serviceURL := "http://flightctl-imagebuilder.flightctl-internal.svc.cluster.local:9090" // Default service URL
	uploadToken := os.Getenv("UPLOAD_TOKEN")                                                // Read token from environment

	if cfg.ImageBuilder != nil {
		if cfg.ImageBuilder.BuildNamespace != "" {
			namespace = cfg.ImageBuilder.BuildNamespace
		}
		if cfg.ImageBuilder.DefaultRegistry != "" {
			defaultRegistry = cfg.ImageBuilder.DefaultRegistry
		}
		if cfg.ImageBuilder.ServiceURL != "" {
			serviceURL = cfg.ImageBuilder.ServiceURL
		}
	}

	orchestrator := &Orchestrator{
		certManager:      NewCertificateManager(apiClient, log.WithField("component", "cert-manager")),
		containerBuilder: NewContainerBuilder(k8sClient, namespace, defaultRegistry, log.WithField("component", "container-builder")),
		bootcBuilder:     NewBootcBuilder(k8sClient, namespace, serviceURL, uploadToken, log.WithField("component", "bootc-builder")),
		storageManager:   NewStorageManager(cfg, log.WithField("component", "storage-manager")),
		client:           apiClient,
		k8sClient:        k8sClient,
		config:           cfg,
		log:              log,
	}

	// Load CA certificate from secret
	orchestrator.loadCACertificate()

	return orchestrator
}

// GetCACertData returns the CA certificate data for enrollment service
func (o *Orchestrator) GetCACertData() string {
	return o.caCertData
}

// loadCACertificate loads the CA certificate from the flightctl-ca-secret
func (o *Orchestrator) loadCACertificate() {
	ctx := context.Background()

	// Try to get CA certificate from flightctl-external namespace
	secret, err := o.k8sClient.GetSecret(ctx, "flightctl-external", "flightctl-ca-secret")
	if err != nil {
		o.log.WithError(err).Warn("Failed to load CA certificate from flightctl-ca-secret, enrollment service CA will not be set")
		return
	}

	// Extract ca.crt from secret
	if caCert, ok := secret.Data["ca.crt"]; ok {
		// The CA cert in the secret is already base64 encoded (it's in Data not StringData)
		// We need the base64-encoded string for the agent config.yaml
		o.caCertData = base64.StdEncoding.EncodeToString(caCert)
		o.log.Info("Successfully loaded CA certificate for enrollment service")
	} else {
		o.log.Warn("CA certificate not found in flightctl-ca-secret")
	}
}

// BuildImage orchestrates the complete image build process
func (o *Orchestrator) BuildImage(ctx context.Context, imageBuild *api.ImageBuild) error {
	o.log.Infof("Starting build process for ImageBuild %s", *imageBuild.Metadata.Name)

	// Check if build is already cancelled before starting
	if o.isCancelled(imageBuild) {
		o.log.Infof("Build %s is already cancelled, skipping", *imageBuild.Metadata.Name)
		return o.cancelBuild(ctx, imageBuild, "Build cancelled before starting")
	}

	// Update status to Building
	if err := o.updateStatus(ctx, imageBuild, "Building", "Starting image build process"); err != nil {
		o.log.WithError(err).Error("Failed to update status to Building")
	}

	// Step 1: Request enrollment certificate if flightctl config is present
	var enrollmentCert, enrollmentKey string
	if imageBuild.Spec.FlightctlConfig != nil {
		// Check cancellation before long operation
		if o.isCancelled(imageBuild) {
			return o.cancelBuild(ctx, imageBuild, "Build cancelled during certificate request")
		}

		o.log.Info("Step 1: Requesting enrollment certificate")
		cert, key, err := o.certManager.RequestEnrollmentCertificate(ctx, *imageBuild.Metadata.Name, 365*24*3600)
		if err != nil {
			return o.failBuild(ctx, imageBuild, fmt.Errorf("failed to request enrollment certificate: %w", err))
		}
		enrollmentCert = cert
		enrollmentKey = key
		o.log.Info("Successfully obtained enrollment certificate and key")
	}

	// Step 2: Generate Containerfile
	if o.isCancelled(imageBuild) {
		return o.cancelBuild(ctx, imageBuild, "Build cancelled before Containerfile generation")
	}

	o.log.Info("Step 2: Generating Containerfile from specifications")
	generator := NewContainerfileGenerator(imageBuild.Spec, enrollmentCert, enrollmentKey)

	// Set default enrollment config from deployment if available
	if o.config.Service != nil {
		generator.WithDefaultEnrollmentConfig(
			o.caCertData,
			o.config.Service.BaseAgentEndpointUrl,
			o.config.Service.BaseUIUrl,
		)
	}

	containerfile, err := generator.Generate()
	if err != nil {
		return o.failBuild(ctx, imageBuild, fmt.Errorf("failed to generate Containerfile: %w", err))
	}
	o.log.Infof("Generated Containerfile (%d bytes)", len(containerfile))
	o.log.Debugf("Containerfile content:\n%s", containerfile)

	// Step 3: Build container image
	if o.isCancelled(imageBuild) {
		return o.cancelBuild(ctx, imageBuild, "Build cancelled before container image build")
	}

	o.log.Info("Step 3: Building container image")
	containerImageRef, err := o.containerBuilder.BuildAndPushImage(ctx, imageBuild, containerfile)
	if err != nil {
		return o.failBuild(ctx, imageBuild, fmt.Errorf("failed to build container image: %w", err))
	}
	o.log.Infof("Successfully built container image: %s", containerImageRef)

	// Update status with container image reference
	if err := o.updateStatusWithContainerImage(ctx, imageBuild, containerImageRef); err != nil {
		o.log.WithError(err).Error("Failed to update status with container image reference")
	}

	// Step 4: Build bootc disk images if requested
	var bootcRefs []api.BootcImageRef
	if imageBuild.Spec.BootcExports != nil && len(*imageBuild.Spec.BootcExports) > 0 {
		// Check cancellation before bootc image generation
		if o.isCancelled(imageBuild) {
			return o.cancelBuild(ctx, imageBuild, "Build cancelled before bootc image generation")
		}

		o.log.Info("Step 4: Generating bootc disk images")

		if err := o.updateStatus(ctx, imageBuild, "GeneratingImages", "Building bootc disk images"); err != nil {
			o.log.WithError(err).Error("Failed to update status to GeneratingImages")
		}

		bootcResults, err := o.bootcBuilder.BuildBootcImages(ctx, imageBuild, containerImageRef)
		if err != nil {
			return o.failBuild(ctx, imageBuild, fmt.Errorf("failed to build bootc images: %w", err))
		}

		// Step 5: Store bootc images to configured storage
		o.log.Info("Step 5: Storing bootc images to storage backend")
		for _, result := range bootcResults {
			storageRef, err := o.storageManager.StoreBootcImage(ctx, *imageBuild.Metadata.Name, result.Path, result.Type)
			if err != nil {
				o.log.WithError(err).Errorf("Failed to store bootc image type=%s", result.Type)
				// Continue with other images
				continue
			}

			bootcRefs = append(bootcRefs, api.BootcImageRef{
				Type:         result.Type,
				Architecture: &result.Architecture,
				StorageRef:   storageRef.Path,
			})

			o.log.Infof("Stored bootc image type=%s to %s", result.Type, storageRef.Path)
		}
	}

	// Step 6: Mark build as completed
	o.log.Info("Build process completed successfully")
	return o.completeBuild(ctx, imageBuild, containerImageRef, bootcRefs)
}

// updateStatus updates the ImageBuild status
func (o *Orchestrator) updateStatus(ctx context.Context, imageBuild *api.ImageBuild, phase, message string) error {
	now := time.Now()

	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	phaseVal := api.ImageBuildStatusPhase(phase)
	imageBuild.Status.Phase = &phaseVal
	imageBuild.Status.Message = &message

	if phase == "Building" && imageBuild.Status.StartTime == nil {
		imageBuild.Status.StartTime = &now
	}

	// Update via API
	resp, err := o.client.ReplaceImageBuildStatusWithResponse(ctx, *imageBuild.Metadata.Name, api.ReplaceImageBuildStatusJSONRequestBody(*imageBuild))
	if err != nil {
		return fmt.Errorf("failed to update ImageBuild status: %w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("failed to update ImageBuild status, status code: %d", resp.StatusCode())
	}

	return nil
}

// updateStatusWithContainerImage updates the status with container image reference
func (o *Orchestrator) updateStatusWithContainerImage(ctx context.Context, imageBuild *api.ImageBuild, containerImageRef string) error {
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	imageBuild.Status.ContainerImageRef = &containerImageRef

	phase := "Pushing"
	message := "Container image built successfully"
	phaseVal := api.ImageBuildStatusPhase(phase)
	imageBuild.Status.Phase = &phaseVal
	imageBuild.Status.Message = &message

	resp, err := o.client.ReplaceImageBuildStatusWithResponse(ctx, *imageBuild.Metadata.Name, api.ReplaceImageBuildStatusJSONRequestBody(*imageBuild))
	if err != nil {
		return fmt.Errorf("failed to update ImageBuild status: %w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("failed to update ImageBuild status, status code: %d", resp.StatusCode())
	}

	return nil
}

// completeBuild marks the build as completed
func (o *Orchestrator) completeBuild(ctx context.Context, imageBuild *api.ImageBuild, containerImageRef string, bootcRefs []api.BootcImageRef) error {
	now := time.Now()

	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	phase := "Completed"
	message := "Image build completed successfully"
	phaseVal := api.ImageBuildStatusPhase(phase)

	imageBuild.Status.Phase = &phaseVal
	imageBuild.Status.Message = &message
	imageBuild.Status.ContainerImageRef = &containerImageRef
	imageBuild.Status.CompletionTime = &now

	if len(bootcRefs) > 0 {
		imageBuild.Status.BootcImageRefs = &bootcRefs
	}

	resp, err := o.client.ReplaceImageBuildStatusWithResponse(ctx, *imageBuild.Metadata.Name, api.ReplaceImageBuildStatusJSONRequestBody(*imageBuild))
	if err != nil {
		return fmt.Errorf("failed to update ImageBuild status to completed: %w", err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("failed to update ImageBuild status, status code: %d", resp.StatusCode())
	}

	o.log.Infof("ImageBuild %s completed successfully", *imageBuild.Metadata.Name)
	return nil
}

// failBuild marks the build as failed
func (o *Orchestrator) failBuild(ctx context.Context, imageBuild *api.ImageBuild, buildErr error) error {
	o.log.WithError(buildErr).Errorf("Build failed for ImageBuild %s", *imageBuild.Metadata.Name)

	now := time.Now()

	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	phase := "Failed"
	message := fmt.Sprintf("Build failed: %v", buildErr)
	phaseVal := api.ImageBuildStatusPhase(phase)

	imageBuild.Status.Phase = &phaseVal
	imageBuild.Status.Message = &message
	imageBuild.Status.CompletionTime = &now

	// Check if the error contains logs (from build job failure)
	// Use errors.As to unwrap the error and check if it's a buildError
	type logsProvider interface {
		Logs() []string
	}

	var logsErr logsProvider
	if errors.As(buildErr, &logsErr) {
		logs := logsErr.Logs()
		if len(logs) > 0 {
			imageBuild.Status.Logs = &logs
			o.log.Infof("Captured %d log lines from failed build, saving to status", len(logs))
			// Log first few lines for debugging
			if len(logs) > 0 {
				lastLines := len(logs)
				if lastLines > 30 {
					lastLines = 30
				}
				o.log.Debugf("Last %d log lines: %v", lastLines, logs[len(logs)-lastLines:])
			}
		} else {
			o.log.Warn("buildError implements Logs() but returned empty logs")
		}
	} else {
		o.log.Debug("buildErr does not implement Logs() interface, no logs to capture")
	}

	// Log the status before sending to verify logs are included
	if imageBuild.Status.Logs != nil {
		o.log.Infof("Status.Logs count before API call: %d", len(*imageBuild.Status.Logs))
	} else {
		o.log.Debug("Status.Logs is nil before API call")
	}

	resp, err := o.client.ReplaceImageBuildStatusWithResponse(ctx, *imageBuild.Metadata.Name, api.ReplaceImageBuildStatusJSONRequestBody(*imageBuild))
	if err != nil {
		o.log.WithError(err).Error("Failed to update ImageBuild status to failed")
	} else if resp.StatusCode() != 200 {
		o.log.Errorf("Failed to update ImageBuild status, status code: %d", resp.StatusCode())
		if resp.Body != nil {
			o.log.Errorf("Response body: %s", string(resp.Body))
		}
	} else {
		o.log.Info("Successfully updated ImageBuild status to failed with logs")
	}

	return buildErr
}

// isCancelled checks if the build has been marked for cancellation by fetching latest state from API
func (o *Orchestrator) isCancelled(imageBuild *api.ImageBuild) bool {
	// Fetch the latest ImageBuild from API to check for cancel annotation
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	latestBuild, err := o.client.GetImageBuildWithResponse(ctx, *imageBuild.Metadata.Name)
	if err != nil {
		o.log.WithError(err).Warn("Failed to fetch latest ImageBuild state for cancel check")
		// Fall back to checking the local copy
		if imageBuild.Metadata.Annotations == nil {
			return false
		}
		cancelValue, exists := (*imageBuild.Metadata.Annotations)["imagebuilder.flightctl.io/cancel"]
		return exists && cancelValue == "true"
	}

	if latestBuild.StatusCode() != 200 || latestBuild.JSON200 == nil {
		o.log.Warnf("Unexpected response status %d when fetching ImageBuild for cancel check", latestBuild.StatusCode())
		// Fall back to checking the local copy
		if imageBuild.Metadata.Annotations == nil {
			return false
		}
		cancelValue, exists := (*imageBuild.Metadata.Annotations)["imagebuilder.flightctl.io/cancel"]
		return exists && cancelValue == "true"
	}

	// Check the latest annotations
	if latestBuild.JSON200.Metadata.Annotations == nil {
		return false
	}
	cancelValue, exists := (*latestBuild.JSON200.Metadata.Annotations)["imagebuilder.flightctl.io/cancel"]
	return exists && cancelValue == "true"
}

// CancelBuild handles build cancellation - stops job, collects logs, removes annotation, and updates status
// This is a public method that can be called by the build manager when a cancel request is detected
func (o *Orchestrator) CancelBuild(ctx context.Context, imageBuild *api.ImageBuild, reason string) error {
	o.log.Infof("Cancelling build for ImageBuild %s: %s", *imageBuild.Metadata.Name, reason)

	// Try to stop and cleanup any running jobs/resources
	configMapName := fmt.Sprintf("containerfile-%s", *imageBuild.Metadata.Name)

	buildNamespace := "default"
	if o.config.ImageBuilder != nil && o.config.ImageBuilder.BuildNamespace != "" {
		buildNamespace = o.config.ImageBuilder.BuildNamespace
	}

	// Use label selector to find ALL jobs for this build (build-* and bootc-*-*)
	labelSelector := fmt.Sprintf("imagebuild=%s", *imageBuild.Metadata.Name)

	// Try to collect logs from any running/failed pods before deleting
	var logs []string
	pods, err := o.k8sClient.ListPods(ctx, buildNamespace, labelSelector)
	if err == nil && len(pods.Items) > 0 {
		o.log.Infof("Found %d pod(s) for cancelled build, collecting logs", len(pods.Items))
		for _, pod := range pods.Items {
			podLog, err := o.k8sClient.GetPodLogs(ctx, buildNamespace, pod.Name, 1000)
			if err != nil {
				o.log.WithError(err).Warnf("Failed to get logs for pod %s", pod.Name)
				continue
			}
			if len(podLog) > 0 {
				logs = append(logs, fmt.Sprintf("=== Logs from pod %s (before cancellation) ===", pod.Name))
				lines := strings.Split(podLog, "\n")
				logs = append(logs, lines...)
			}
		}
	}

	// Delete ALL jobs with the imagebuild label using ListJobs
	jobList, err := o.k8sClient.ListJobs(ctx, buildNamespace, labelSelector)
	deletedCount := 0
	if err != nil {
		o.log.WithError(err).Warnf("Failed to list jobs with selector %s in namespace %s", labelSelector, buildNamespace)
	} else if len(jobList.Items) > 0 {
		o.log.Infof("Found %d job(s) to delete in namespace %s", len(jobList.Items), buildNamespace)
		for _, job := range jobList.Items {
			if err := o.k8sClient.DeleteJob(ctx, buildNamespace, job.Name); err != nil {
				o.log.WithError(err).Warnf("Failed to delete job %s", job.Name)
			} else {
				o.log.Infof("Deleted job %s", job.Name)
				deletedCount++
			}
		}
	}

	if deletedCount == 0 {
		o.log.Warnf("No jobs found to delete in namespace %s (may have already been cleaned up or wrong namespace)", buildNamespace)
	} else {
		o.log.Infof("Deleted %d job(s) for cancelled build", deletedCount)
	}

	// Delete ConfigMap
	if err := o.k8sClient.DeleteConfigMap(ctx, buildNamespace, configMapName); err != nil {
		o.log.WithError(err).Warnf("Failed to delete ConfigMap %s (may not exist)", configMapName)
	} else {
		o.log.Infof("Deleted ConfigMap %s", configMapName)
	}

	// First, remove the cancel annotation using PATCH (required for imagebuilder.* annotations)
	// Use JSON Patch format - note: "/" in the key must be escaped as "~1"
	patchOps := api.PatchRequest{
		{
			Op:   "remove",
			Path: "/metadata/annotations/imagebuilder.flightctl.io~1cancel",
		},
	}

	patchResp, err := o.client.PatchImageBuildWithApplicationJSONPatchPlusJSONBodyWithResponse(
		ctx,
		*imageBuild.Metadata.Name,
		patchOps,
	)
	if err != nil {
		o.log.WithError(err).Warn("Failed to remove cancel annotation via PATCH")
	} else if patchResp.StatusCode() != 200 {
		o.log.Warnf("Failed to remove cancel annotation, status code: %d", patchResp.StatusCode())
		if patchResp.Body != nil {
			o.log.Warnf("Response body: %s", string(patchResp.Body))
		}
	} else {
		o.log.Info("Successfully removed cancel annotation from ImageBuild")
	}

	// Now update the status to Cancelled
	now := time.Now()
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	phase := "Cancelled"
	message := fmt.Sprintf("Build cancelled: %s", reason)
	phaseVal := api.ImageBuildStatusPhase(phase)

	imageBuild.Status.Phase = &phaseVal
	imageBuild.Status.Message = &message
	imageBuild.Status.CompletionTime = &now

	// Add collected logs if any
	if len(logs) > 0 {
		imageBuild.Status.Logs = &logs
		o.log.Infof("Captured %d log lines from cancelled build", len(logs))
	}

	// Update status via API
	statusResp, err := o.client.ReplaceImageBuildStatusWithResponse(ctx, *imageBuild.Metadata.Name, api.ReplaceImageBuildStatusJSONRequestBody(*imageBuild))
	if err != nil {
		o.log.WithError(err).Error("Failed to update ImageBuild status to cancelled")
		return fmt.Errorf("failed to update ImageBuild status: %w", err)
	}
	if statusResp.StatusCode() != 200 {
		o.log.Errorf("Failed to update ImageBuild status, status code: %d", statusResp.StatusCode())
		if statusResp.Body != nil {
			o.log.Errorf("Response body: %s", string(statusResp.Body))
		}
		return fmt.Errorf("failed to update ImageBuild status, status code: %d", statusResp.StatusCode())
	}

	o.log.Info("Successfully updated ImageBuild to cancelled state")
	return fmt.Errorf("build cancelled: %s", reason)
}

// cancelBuild is a private wrapper that calls CancelBuild (for backward compatibility)
func (o *Orchestrator) cancelBuild(ctx context.Context, imageBuild *api.ImageBuild, reason string) error {
	return o.CancelBuild(ctx, imageBuild, reason)
}

// RebuildImage rebuilds an existing ImageBuild
func (o *Orchestrator) RebuildImage(ctx context.Context, imageBuildName string) error {
	o.log.Infof("Rebuilding ImageBuild %s", imageBuildName)

	// Get the ImageBuild - use List as workaround since Read may not exist
	listResp, err := o.client.ListImageBuildsWithResponse(ctx, &api.ListImageBuildsParams{})
	if err != nil {
		return fmt.Errorf("failed to list ImageBuilds: %w", err)
	}
	if listResp.StatusCode() != 200 {
		return fmt.Errorf("failed to list ImageBuilds, status code: %d", listResp.StatusCode())
	}

	// Find the specific ImageBuild
	var imageBuild *api.ImageBuild
	if listResp.JSON200 != nil {
		for i := range listResp.JSON200.Items {
			item := &listResp.JSON200.Items[i]
			if item.Metadata.Name != nil && *item.Metadata.Name == imageBuildName {
				imageBuild = item
				break
			}
		}
	}
	if imageBuild == nil {
		return fmt.Errorf("ImageBuild %s not found", imageBuildName)
	}

	// Reset status
	if imageBuild.Status != nil {
		imageBuild.Status.Phase = nil
		imageBuild.Status.Message = nil
		imageBuild.Status.ContainerImageRef = nil
		imageBuild.Status.BootcImageRefs = nil
		imageBuild.Status.CompletionTime = nil
	}

	// Trigger rebuild
	return o.BuildImage(ctx, imageBuild)
}
