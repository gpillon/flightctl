package imagebuilder

import (
	"context"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// BuildManager handles the polling and lifecycle management of ImageBuilds
type BuildManager struct {
	store                store.Store
	orchestrator         *Orchestrator
	k8sClient            k8sclient.K8SClient
	cfg                  *config.Config
	log                  logrus.FieldLogger
	recentlyCancelled    map[string]time.Time // Track recently cancelled builds to avoid duplicate cancellations
}

// NewBuildManager creates a new BuildManager
func NewBuildManager(
	store store.Store,
	orchestrator *Orchestrator,
	k8sClient k8sclient.K8SClient,
	cfg *config.Config,
	log logrus.FieldLogger,
) *BuildManager {
	return &BuildManager{
		store:             store,
		orchestrator:      orchestrator,
		k8sClient:         k8sClient,
		cfg:               cfg,
		log:               log.WithField("component", "build-manager"),
		recentlyCancelled: make(map[string]time.Time),
	}
}

// Run starts the polling loop for pending builds
func (bm *BuildManager) Run(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	bm.log.Info("Build manager is running")

	// Process any pending builds immediately
	bm.ProcessPendingBuilds(ctx)

	for {
		select {
		case <-ctx.Done():
			bm.log.Info("Shutting down build manager")
			return nil
		case <-ticker.C:
			bm.ProcessPendingBuilds(ctx)
		}
	}
}

// ProcessPendingBuilds finds and processes ImageBuilds in Pending state
func (bm *BuildManager) ProcessPendingBuilds(ctx context.Context) {
	// Clean up old entries from recentlyCancelled (older than 2 minutes)
	bm.cleanupRecentlyCancelled()

	// Get all organizations (for multi-tenancy support)
	orgs, err := bm.store.Organization().List(ctx)
	if err != nil {
		bm.log.WithError(err).Error("Failed to list organizations")
		return
	}

	for _, org := range orgs {
		bm.processPendingBuildsForOrg(ctx, org.ID)
	}
}

// processPendingBuildsForOrg processes pending builds for a specific organization
func (bm *BuildManager) processPendingBuildsForOrg(ctx context.Context, orgID uuid.UUID) {
	// List all ImageBuilds for this org
	imageBuilds, err := bm.store.ImageBuild().List(ctx, orgID, store.ListParams{})
	if err != nil {
		bm.log.WithError(err).Errorf("Failed to list ImageBuilds for org %s", orgID)
		return
	}

	for _, imageBuild := range imageBuilds.Items {
		// Check if this build is pending
		if bm.shouldProcessBuild(&imageBuild) {
			bm.log.Infof("Found pending ImageBuild: %s", *imageBuild.Metadata.Name)
			go bm.ProcessBuild(context.Background(), &imageBuild)
		}

		// Handle cancellation requests
		if bm.shouldCancelBuild(&imageBuild) {
			bm.log.Infof("Found cancel request for active ImageBuild: %s (phase: %s)",
				*imageBuild.Metadata.Name, *imageBuild.Status.Phase)
			bm.handleCancellation(ctx, &imageBuild)
		}

		// Handle retry requests
		if bm.shouldRetryBuild(&imageBuild) {
			bm.log.Infof("Retrying failed ImageBuild: %s", *imageBuild.Metadata.Name)
			bm.handleRetry(ctx, &imageBuild)
		}
	}
}

// shouldProcessBuild checks if a build should be started
func (bm *BuildManager) shouldProcessBuild(imageBuild *api.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Phase == nil {
		return true
	}
	return *imageBuild.Status.Phase == "" || *imageBuild.Status.Phase == "Pending"
}

// shouldCancelBuild checks if a build should be cancelled
func (bm *BuildManager) shouldCancelBuild(imageBuild *api.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Phase == nil {
		return false
	}

	phase := *imageBuild.Status.Phase
	isActivePhase := phase == "Building" || phase == "Pushing" || phase == "GeneratingImages"

	if !isActivePhase {
		return false
	}

	if imageBuild.Metadata.Annotations == nil {
		return false
	}

	cancel, ok := (*imageBuild.Metadata.Annotations)["imagebuilder.flightctl.io/cancel"]
	return ok && cancel == "true"
}

// shouldRetryBuild checks if a build should be retried
func (bm *BuildManager) shouldRetryBuild(imageBuild *api.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Phase == nil {
		return false
	}

	if *imageBuild.Status.Phase != "Failed" {
		return false
	}

	if imageBuild.Metadata.Annotations == nil {
		return false
	}

	retry, ok := (*imageBuild.Metadata.Annotations)["imagebuilder.flightctl.io/retry"]
	return ok && retry == "true"
}

// handleCancellation cancels a running build by calling the orchestrator's CancelBuild method
// This will delete K8s resources, collect logs, remove the cancel annotation, and update the status to Cancelled
func (bm *BuildManager) handleCancellation(ctx context.Context, imageBuild *api.ImageBuild) {
	buildName := *imageBuild.Metadata.Name

	// Check if we recently cancelled this build (within last 2 minutes)
	if lastCancelled, exists := bm.recentlyCancelled[buildName]; exists {
		if time.Since(lastCancelled) < 2*time.Minute {
			bm.log.Debugf("Build %s was recently cancelled (%v ago), skipping duplicate cancellation",
				buildName, time.Since(lastCancelled))
			return
		}
	}

	// Record this cancellation attempt
	bm.recentlyCancelled[buildName] = time.Now()

	// Call the orchestrator to perform the complete cancellation process
	// This includes: deleting K8s resources, collecting logs, removing annotation, and updating status
	err := bm.orchestrator.CancelBuild(ctx, imageBuild, "Build cancelled by user request")
	if err != nil {
		bm.log.WithError(err).Debug("Cancellation completed with expected error")
	}
}

// cleanupRecentlyCancelled removes old entries from the recentlyCancelled map
func (bm *BuildManager) cleanupRecentlyCancelled() {
	now := time.Now()
	for buildName, cancelTime := range bm.recentlyCancelled {
		if now.Sub(cancelTime) > 2*time.Minute {
			delete(bm.recentlyCancelled, buildName)
		}
	}
}

// handleRetry handles retrying a failed build
func (bm *BuildManager) handleRetry(ctx context.Context, imageBuild *api.ImageBuild) {
	// Remove retry annotation
	delete(*imageBuild.Metadata.Annotations, "imagebuilder.flightctl.io/retry")
	go bm.ProcessBuild(context.Background(), imageBuild)
}

// ProcessBuild processes a single ImageBuild
func (bm *BuildManager) ProcessBuild(ctx context.Context, imageBuild *api.ImageBuild) {
	buildCtx, cancel := context.WithTimeout(ctx, 2*time.Hour) // 2 hour timeout for build
	defer cancel()

	bm.log.Infof("Processing ImageBuild %s", *imageBuild.Metadata.Name)

	err := bm.orchestrator.BuildImage(buildCtx, imageBuild)
	if err != nil {
		bm.log.WithError(err).Errorf("Failed to build image %s", *imageBuild.Metadata.Name)
	} else {
		bm.log.Infof("Successfully completed build for %s", *imageBuild.Metadata.Name)
	}
}
