package imagebuilder

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CleanupManager handles cleanup of orphaned build resources
type CleanupManager struct {
	store     store.Store
	k8sClient k8sclient.K8SClient
	cfg       *config.Config
	log       logrus.FieldLogger
}

// NewCleanupManager creates a new CleanupManager
func NewCleanupManager(
	store store.Store,
	k8sClient k8sclient.K8SClient,
	cfg *config.Config,
	log logrus.FieldLogger,
) *CleanupManager {
	return &CleanupManager{
		store:     store,
		k8sClient: k8sClient,
		cfg:       cfg,
		log:       log.WithField("component", "cleanup-manager"),
	}
}

// PerformInitialCleanup performs cleanup of orphaned resources with distributed locking
func (cm *CleanupManager) PerformInitialCleanup(ctx context.Context) error {
	cm.log.Info("Starting initial cleanup of orphaned build resources")

	buildNamespace := cm.getBuildNamespace()
	lockName := "imagebuilder-cleanup-lock"

	// Try to acquire lock
	locked, unlock, err := cm.tryAcquireLock(ctx, buildNamespace, lockName, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to acquire cleanup lock: %w", err)
	}

	if !locked {
		cm.log.Info("Another instance is performing cleanup, skipping")
		return nil
	}

	defer unlock()

	cm.log.Info("Cleanup lock acquired, performing cleanup")

	// Get all active ImageBuilds
	activeBuilds := make(map[string]bool)
	orgs, err := cm.store.Organization().List(ctx)
	if err != nil {
		cm.log.WithError(err).Error("Failed to list organizations for cleanup")
		return err
	}

	for _, org := range orgs {
		imageBuilds, err := cm.store.ImageBuild().List(ctx, org.ID, store.ListParams{})
		if err != nil {
			cm.log.WithError(err).Errorf("Failed to list ImageBuilds for org %s", org.ID)
			continue
		}

		for _, imageBuild := range imageBuilds.Items {
			// Consider a build "active" only if it's in Building, Queued, or Pending state
			if imageBuild.Status != nil && imageBuild.Status.Phase != nil {
				phase := string(*imageBuild.Status.Phase)
				if phase == "Building" || phase == "Queued" || phase == "Pending" {
					activeBuilds[*imageBuild.Metadata.Name] = true
					cm.log.Debugf("ImageBuild %s is active (phase: %s)", *imageBuild.Metadata.Name, phase)
				}
			}
		}
	}

	cm.log.Infof("Found %d active builds", len(activeBuilds))

	// List all pods in build namespace
	pods, err := cm.k8sClient.ListPods(ctx, buildNamespace, "")
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// Track orphaned resources
	orphanedJobs := make(map[string]bool)
	orphanedConfigMaps := make(map[string]bool)

	// Check which jobs are orphaned (older than 1 hour and not for active builds)
	for _, pod := range pods.Items {
		jobName, hasLabel := pod.Labels["job-name"]
		if !hasLabel || !strings.HasPrefix(jobName, "build-") {
			continue
		}

		// Extract imageBuild name from job name
		imageBuildName := strings.TrimPrefix(jobName, "build-")

		// Check if build is active
		if activeBuilds[imageBuildName] {
			cm.log.Debugf("Job %s is for active build %s, skipping", jobName, imageBuildName)
			continue
		}

		// Check if pod is old enough (1 hour)
		creationTime := pod.CreationTimestamp.Time
		if time.Since(creationTime) < 1*time.Hour {
			cm.log.Debugf("Job %s is too recent (created %v ago), skipping", jobName, time.Since(creationTime))
			continue
		}

		orphanedJobs[jobName] = true
		orphanedConfigMaps["containerfile-"+imageBuildName] = true
	}

	cm.log.Infof("Found %d orphaned jobs and %d orphaned ConfigMaps", len(orphanedJobs), len(orphanedConfigMaps))

	// Delete orphaned jobs
	for jobName := range orphanedJobs {
		cm.log.Infof("Deleting orphaned job: %s", jobName)
		if err := cm.k8sClient.DeleteJob(ctx, buildNamespace, jobName); err != nil {
			cm.log.WithError(err).Warnf("Failed to delete orphaned job %s", jobName)
		}
	}

	// Delete orphaned ConfigMaps
	for configMapName := range orphanedConfigMaps {
		cm.log.Infof("Deleting orphaned ConfigMap: %s", configMapName)
		if err := cm.k8sClient.DeleteConfigMap(ctx, buildNamespace, configMapName); err != nil {
			cm.log.WithError(err).Warnf("Failed to delete orphaned ConfigMap %s", configMapName)
		}
	}

	cm.log.Infof("Cleanup completed: deleted %d jobs and %d ConfigMaps", len(orphanedJobs), len(orphanedConfigMaps))
	return nil
}

// tryAcquireLock tries to acquire a distributed lock using a ConfigMap
func (cm *CleanupManager) tryAcquireLock(ctx context.Context, namespace, lockName string, leaseDuration time.Duration) (bool, func(), error) {
	lockConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lockName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"holder":    os.Getenv("HOSTNAME"),
			"timestamp": time.Now().Format(time.RFC3339),
		},
	}

	// Try to create the ConfigMap (succeeds only if it doesn't exist)
	_, err := cm.k8sClient.CreateConfigMap(ctx, namespace, lockConfigMap)
	if err == nil {
		// Successfully acquired lock
		cm.log.Infof("Lock acquired by %s", os.Getenv("HOSTNAME"))

		unlock := func() {
			if err := cm.k8sClient.DeleteConfigMap(ctx, namespace, lockName); err != nil {
				cm.log.WithError(err).Warn("Failed to release lock")
			} else {
				cm.log.Info("Lock released")
			}
		}

		return true, unlock, nil
	}

	// ConfigMap already exists, check if lease has expired
	existing, err := cm.k8sClient.GetConfigMap(ctx, namespace, lockName)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get existing lock: %w", err)
	}

	timestampStr, ok := existing.Data["timestamp"]
	if !ok {
		// Invalid lock, delete and retry
		cm.log.Warn("Invalid lock found (no timestamp), deleting")
		_ = cm.k8sClient.DeleteConfigMap(ctx, namespace, lockName)
		return false, nil, fmt.Errorf("invalid lock")
	}

	lockTime, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		cm.log.WithError(err).Warn("Invalid lock timestamp, deleting")
		_ = cm.k8sClient.DeleteConfigMap(ctx, namespace, lockName)
		return false, nil, fmt.Errorf("invalid lock timestamp")
	}

	// Check if lock has expired
	if time.Since(lockTime) > leaseDuration {
		cm.log.Infof("Lock has expired (held for %v), taking over", time.Since(lockTime))
		_ = cm.k8sClient.DeleteConfigMap(ctx, namespace, lockName)
		// Retry acquiring
		return cm.tryAcquireLock(ctx, namespace, lockName, leaseDuration)
	}

	holder := existing.Data["holder"]
	cm.log.Infof("Lock is held by %s (acquired %v ago)", holder, time.Since(lockTime))
	return false, nil, nil
}

// getBuildNamespace returns the configured build namespace
func (cm *CleanupManager) getBuildNamespace() string {
	if cm.cfg.ImageBuilder != nil && cm.cfg.ImageBuilder.BuildNamespace != "" {
		return cm.cfg.ImageBuilder.BuildNamespace
	}
	return "flightctl-builds"
}
