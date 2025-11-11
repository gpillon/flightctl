package imagebuilder

import (
	"context"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerBuilder builds container images using buildah in Kubernetes Jobs
type ContainerBuilder struct {
	k8sClient       k8sclient.K8SClient
	namespace       string
	defaultRegistry string
	log             logrus.FieldLogger
}

// NewContainerBuilder creates a new container image builder
func NewContainerBuilder(k8sClient k8sclient.K8SClient, namespace, defaultRegistry string, log logrus.FieldLogger) *ContainerBuilder {
	return &ContainerBuilder{
		k8sClient:       k8sClient,
		namespace:       namespace,
		defaultRegistry: defaultRegistry,
		log:             log,
	}
}

// BuildAndPushImage builds a container image from a Containerfile and optionally pushes it to a registry
func (cb *ContainerBuilder) BuildAndPushImage(ctx context.Context, imageBuild *api.ImageBuild, containerfile string) (string, error) {
	imageName := cb.generateImageName(imageBuild)
	jobName := fmt.Sprintf("build-%s", *imageBuild.Metadata.Name)

	cb.log.Infof("Building container image %s using job %s", imageName, jobName)

	// Create a ConfigMap with the Containerfile
	configMapName, err := cb.createContainerfileConfigMap(ctx, imageBuild, containerfile)
	if err != nil {
		return "", fmt.Errorf("failed to create Containerfile configmap: %w", err)
	}

	// Create registry credentials secret for destination registry if needed
	var destRegistrySecretName string
	if imageBuild.Spec.ContainerRegistry != nil &&
		imageBuild.Spec.ContainerRegistry.Credentials != nil &&
		imageBuild.Spec.ContainerRegistry.Credentials.Username != nil {
		destRegistrySecretName, err = cb.createRegistrySecret(ctx, imageBuild)
		if err != nil {
			return "", fmt.Errorf("failed to create registry secret: %w", err)
		}
	}

	// Create base image registry credentials secret if needed
	var baseRegistrySecretName string
	if imageBuild.Spec.BaseImageRegistryCredentials != nil &&
		imageBuild.Spec.BaseImageRegistryCredentials.Username != nil &&
		imageBuild.Spec.BaseImageRegistryCredentials.Password != nil {
		baseRegistrySecretName, err = cb.createBaseImageRegistrySecret(ctx, imageBuild)
		if err != nil {
			return "", fmt.Errorf("failed to create base image registry secret: %w", err)
		}
	}

	// Create the build job
	job := cb.createBuildJob(jobName, imageName, configMapName, destRegistrySecretName, baseRegistrySecretName, imageBuild)

	createdJob, err := cb.k8sClient.CreateJob(ctx, cb.namespace, job)
	if err != nil {
		return "", fmt.Errorf("failed to create build job: %w", err)
	}

	cb.log.Infof("Created build job %s, waiting for completion", createdJob.Name)

	// Wait for the job to complete
	err = cb.k8sClient.WatchJob(ctx, cb.namespace, createdJob.Name)

	// Always try to retrieve logs, even if job succeeded (for debugging)
	logs := cb.getJobPodLogs(ctx, *imageBuild.Metadata.Name)

	if err != nil {
		// Job failed - return error with logs
		cb.log.Errorf("Build job %s failed: %v", createdJob.Name, err)
		cb.log.Infof("Retrieved %d log lines from failed job", len(logs))

		// Cleanup job and ConfigMap before returning error
		_ = cb.k8sClient.DeleteJob(ctx, cb.namespace, createdJob.Name)
		_ = cb.k8sClient.DeleteConfigMap(ctx, cb.namespace, configMapName)
		// NOTE: We do NOT delete the secret here because bootc-builder may still need it
		// The secret should be cleaned up by the orchestrator after all steps complete

		return "", &buildError{
			err:  fmt.Errorf("build job failed: %w", err),
			logs: logs,
		}
	}

	cb.log.Infof("Successfully built container image %s", imageName)

	// Cleanup job and ConfigMap
	_ = cb.k8sClient.DeleteJob(ctx, cb.namespace, createdJob.Name)
	_ = cb.k8sClient.DeleteConfigMap(ctx, cb.namespace, configMapName)
	// NOTE: We do NOT delete the secret here because bootc-builder may still need it
	// The secret should be cleaned up by the orchestrator after all steps complete
	cb.log.Infof("Cleaned up job %s and ConfigMap %s", createdJob.Name, configMapName)

	return imageName, nil
}

// buildError wraps an error with associated logs
type buildError struct {
	err  error
	logs []string
}

func (e *buildError) Error() string {
	return e.err.Error()
}

func (e *buildError) Logs() []string {
	return e.logs
}

// getJobPodLogs retrieves logs from pods associated with a build job
func (cb *ContainerBuilder) getJobPodLogs(ctx context.Context, imageBuildName string) []string {
	// Find pods for this job
	labelSelector := fmt.Sprintf("job-name=build-%s", imageBuildName)
	pods, err := cb.k8sClient.ListPods(ctx, cb.namespace, labelSelector)
	if err != nil {
		cb.log.WithError(err).Warn("Failed to list pods for failed job")
		return []string{fmt.Sprintf("Failed to retrieve build logs: %v", err)}
	}

	if len(pods.Items) == 0 {
		cb.log.Warn("No pods found for job")
		return []string{"No pods found for this build job"}
	}

	var allLogs []string
	for _, pod := range pods.Items {
		// Add pod status information
		podStatus := string(pod.Status.Phase)
		if len(pod.Status.ContainerStatuses) > 0 {
			containerStatus := pod.Status.ContainerStatuses[0]
			if containerStatus.State.Terminated != nil {
				podStatus = fmt.Sprintf("%s (exit code: %d, reason: %s)",
					podStatus,
					containerStatus.State.Terminated.ExitCode,
					containerStatus.State.Terminated.Reason)
			} else if containerStatus.State.Waiting != nil {
				podStatus = fmt.Sprintf("%s (waiting: %s)",
					podStatus,
					containerStatus.State.Waiting.Reason)
			}
		}

		allLogs = append(allLogs, fmt.Sprintf("=== Pod %s (Status: %s) ===", pod.Name, podStatus))

		// Retrieve logs with higher limit for failed builds
		podLog, err := cb.k8sClient.GetPodLogs(ctx, cb.namespace, pod.Name, 2000)
		if err != nil {
			cb.log.WithError(err).Warnf("Failed to get logs for pod %s", pod.Name)
			allLogs = append(allLogs, fmt.Sprintf("Failed to retrieve logs: %v", err))
			continue
		}

		if len(podLog) > 0 {
			// Split by lines
			lines := strings.Split(podLog, "\n")
			allLogs = append(allLogs, lines...)
		} else {
			allLogs = append(allLogs, "(no logs available)")
		}
		allLogs = append(allLogs, "") // Empty line between pods
	}

	return allLogs
}

// getRegistryURL extracts registry URL from ImageBuild spec
func (cb *ContainerBuilder) getRegistryURL(imageBuild *api.ImageBuild) string {
	return GetRegistryURL(imageBuild)
}

// generateImageName creates a full image reference with registry, name, and tag
func (cb *ContainerBuilder) generateImageName(imageBuild *api.ImageBuild) string {
	// Check if user provided a full image name in registry URL
	if imageBuild.Spec.ContainerRegistry != nil && imageBuild.Spec.ContainerRegistry.Url != "" {
		registryURL := imageBuild.Spec.ContainerRegistry.Url
		// Remove https:// or http:// prefix if present
		registryURL = strings.TrimPrefix(registryURL, "https://")
		registryURL = strings.TrimPrefix(registryURL, "http://")
		registryURL = strings.TrimSuffix(registryURL, "/")

		// If URL contains "/" it means user specified a full image name (e.g. "localhost:5000/myimage" or "localhost:5000/myimage:v1")
		// Use it as-is, optionally adding a tag if not present
		if strings.Contains(registryURL, "/") {
			// User specified full image name
			if strings.Contains(registryURL, ":") && strings.Index(registryURL, ":") > strings.LastIndex(registryURL, "/") {
				// Already has a tag after the last "/" (e.g. "localhost:5000/myimage:v1")
				return registryURL
			}
			// No tag, add timestamp-based tag
			tag := "latest"
			if imageBuild.Metadata.CreationTimestamp != nil {
				tag = imageBuild.Metadata.CreationTimestamp.Format("20060102-150405")
			}
			return fmt.Sprintf("%s:%s", registryURL, tag)
		}

		// URL is just registry host (e.g. "localhost:5000"), build full name
		registry := registryURL
		imageName := *imageBuild.Metadata.Name
		tag := "latest"
		if imageBuild.Metadata.CreationTimestamp != nil {
			tag = imageBuild.Metadata.CreationTimestamp.Format("20060102-150405")
		}
		return fmt.Sprintf("%s/%s:%s", registry, imageName, tag)
	}

	// No registry specified, just return image:tag
	imageName := *imageBuild.Metadata.Name
	tag := "latest"
	if imageBuild.Metadata.CreationTimestamp != nil {
		tag = imageBuild.Metadata.CreationTimestamp.Format("20060102-150405")
	}
	return fmt.Sprintf("%s:%s", imageName, tag)
}

// createContainerfileConfigMap creates a ConfigMap containing the Containerfile
func (cb *ContainerBuilder) createContainerfileConfigMap(ctx context.Context, imageBuild *api.ImageBuild, containerfile string) (string, error) {
	configMapName := fmt.Sprintf("containerfile-%s", *imageBuild.Metadata.Name)

	// Check if ConfigMap already exists
	_, err := cb.k8sClient.GetConfigMap(ctx, cb.namespace, configMapName)
	if err == nil {
		// ConfigMap already exists, we can reuse it or delete and recreate
		cb.log.Infof("ConfigMap %s already exists, reusing it", configMapName)
		return configMapName, nil
	}

	// Create the ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: cb.namespace,
			Labels: map[string]string{
				"app": "flightctl-imagebuilder",
			},
		},
		Data: map[string]string{
			"Containerfile": containerfile,
		},
	}

	_, err = cb.k8sClient.CreateConfigMap(ctx, cb.namespace, configMap)
	if err != nil {
		return "", fmt.Errorf("failed to create ConfigMap %s: %w", configMapName, err)
	}

	cb.log.Infof("Created ConfigMap %s with Containerfile", configMapName)
	return configMapName, nil
}

// createRegistrySecret creates a Secret with registry credentials
func (cb *ContainerBuilder) createRegistrySecret(ctx context.Context, imageBuild *api.ImageBuild) (string, error) {
	secretName := fmt.Sprintf("registry-%s", *imageBuild.Metadata.Name)

	registry := imageBuild.Spec.ContainerRegistry
	if registry == nil || registry.Credentials == nil ||
		registry.Credentials.Username == nil || registry.Credentials.Password == nil {
		return "", fmt.Errorf("registry credentials not provided")
	}

	// Check if Secret already exists
	_, err := cb.k8sClient.GetSecret(ctx, cb.namespace, secretName)
	if err == nil {
		cb.log.Infof("Secret %s already exists, reusing it", secretName)
		return secretName, nil
	}

	// Create the Secret with registry credentials
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cb.namespace,
			Labels: map[string]string{
				"app":        "flightctl-imagebuilder",
				"imagebuild": *imageBuild.Metadata.Name,
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"username": *registry.Credentials.Username,
			"password": *registry.Credentials.Password,
		},
	}

	_, err = cb.k8sClient.CreateSecret(ctx, cb.namespace, secret)
	if err != nil {
		return "", fmt.Errorf("failed to create Secret %s: %w", secretName, err)
	}

	cb.log.Infof("Created registry secret %s in namespace %s", secretName, cb.namespace)
	return secretName, nil
}

// createBaseImageRegistrySecret creates a Secret with base image registry credentials
func (cb *ContainerBuilder) createBaseImageRegistrySecret(ctx context.Context, imageBuild *api.ImageBuild) (string, error) {
	secretName := fmt.Sprintf("base-registry-%s", *imageBuild.Metadata.Name)

	credentials := imageBuild.Spec.BaseImageRegistryCredentials
	if credentials == nil || credentials.Username == nil || credentials.Password == nil {
		return "", fmt.Errorf("base image registry credentials not provided")
	}

	// Check if Secret already exists
	_, err := cb.k8sClient.GetSecret(ctx, cb.namespace, secretName)
	if err == nil {
		cb.log.Infof("Secret %s already exists, reusing it", secretName)
		return secretName, nil
	}

	// Create the Secret with base image registry credentials
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cb.namespace,
			Labels: map[string]string{
				"app":        "flightctl-imagebuilder",
				"imagebuild": *imageBuild.Metadata.Name,
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"username": *credentials.Username,
			"password": *credentials.Password,
		},
	}

	_, err = cb.k8sClient.CreateSecret(ctx, cb.namespace, secret)
	if err != nil {
		return "", fmt.Errorf("failed to create Secret %s: %w", secretName, err)
	}

	cb.log.Infof("Created base image registry secret %s in namespace %s", secretName, cb.namespace)
	return secretName, nil
}

// getBaseImageRegistry extracts registry URL from base image reference
func (cb *ContainerBuilder) getBaseImageRegistry(baseImage string) string {
	return GetBaseImageRegistry(baseImage)
}

// createBuildJob creates a Kubernetes Job that builds the container image using buildah
func (cb *ContainerBuilder) createBuildJob(jobName, imageName, configMapName, destRegistrySecretName, baseRegistrySecretName string, imageBuild *api.ImageBuild) *batchv1.Job {
	// Buildah build command with authentication for both base image and destination registries
	buildCmd := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf(`
			set -e
			
			# Configure registry authentication
			mkdir -p /run/containers/0
			echo '{' > /run/containers/0/auth.json
			echo '  "auths": {' >> /run/containers/0/auth.json
			
			AUTH_ENTRIES=""
			
			# Configure base image registry authentication if credentials are provided
			if [ -f /base-registry-auth/username ] && [ -f /base-registry-auth/password ]; then
				echo "Configuring base image registry authentication..."
				BASE_REGISTRY_URL="%s"
				BASE_USERNAME=$(cat /base-registry-auth/username)
				BASE_PASSWORD=$(cat /base-registry-auth/password)
				BASE_AUTH=$(echo -n ${BASE_USERNAME}:${BASE_PASSWORD} | base64 -w 0)
				
				AUTH_ENTRIES="\"${BASE_REGISTRY_URL}\": {\"auth\": \"${BASE_AUTH}\"}"
				echo "Base image registry authentication configured for ${BASE_REGISTRY_URL}"
			fi
			
			# Configure destination registry authentication if credentials are provided
			if [ -f /dest-registry-auth/username ] && [ -f /dest-registry-auth/password ]; then
				echo "Configuring destination registry authentication..."
				DEST_REGISTRY_URL="%s"
				DEST_USERNAME=$(cat /dest-registry-auth/username)
				DEST_PASSWORD=$(cat /dest-registry-auth/password)
				DEST_AUTH=$(echo -n ${DEST_USERNAME}:${DEST_PASSWORD} | base64 -w 0)
				
				if [ -n "$AUTH_ENTRIES" ]; then
					AUTH_ENTRIES="${AUTH_ENTRIES},"
				fi
				AUTH_ENTRIES="${AUTH_ENTRIES}\"${DEST_REGISTRY_URL}\": {\"auth\": \"${DEST_AUTH}\"}"
				echo "Destination registry authentication configured for ${DEST_REGISTRY_URL}"
			fi
			
			# Write auth entries to auth.json
			if [ -n "$AUTH_ENTRIES" ]; then
				echo "    ${AUTH_ENTRIES}" >> /run/containers/0/auth.json
			fi
			echo '  }' >> /run/containers/0/auth.json
			echo '}' >> /run/containers/0/auth.json
			
			# echo "Auth configuration:"
			# cat /run/containers/0/auth.json
			
			mkdir -p /workspace
			cd /workspace
			
			# Build the image with retry logic (up to 5 attempts)
			MAX_RETRIES=5
			RETRY_COUNT=0
			until buildah bud --format=docker --layers --retry 5 --retry-delay 10s -f /containerfile/Containerfile -t %s . ; do
				RETRY_COUNT=$((RETRY_COUNT+1))
				if [ $RETRY_COUNT -ge $MAX_RETRIES ]; then
					echo "Build failed after $MAX_RETRIES attempts"
					exit 1
				fi
				echo "Build attempt $RETRY_COUNT failed, retrying in 15 seconds..."
				sleep 15
			done
			
			# Push to registry if requested
			if [ "%s" = "true" ]; then
				buildah push --retry 5 --retry-delay 10s %s
			fi
		`,
			cb.getBaseImageRegistry(imageBuild.Spec.BaseImage),
			cb.getRegistryURL(imageBuild),
			imageName,
			func() string {
				if imageBuild.Spec.PushToRegistry != nil && *imageBuild.Spec.PushToRegistry {
					return "true"
				}
				return "false"
			}(),
			imageName),
	}

	backoffLimit := int32(2)               // Reduced since we have internal retries
	ttlSecondsAfterFinished := int32(3600) // Keep job for 1 hour after completion

	volumes := []corev1.Volume{
		{
			Name: "containerfile",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName,
					},
				},
			},
		},
		{
			Name: "varlibcontainers",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "containerfile",
			MountPath: "/containerfile",
		},
		{
			Name:      "varlibcontainers",
			MountPath: "/var/lib/containers",
		},
	}

	// Add destination registry secret volume if provided
	if destRegistrySecretName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "dest-registry-auth",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: destRegistrySecretName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "dest-registry-auth",
			MountPath: "/dest-registry-auth",
			ReadOnly:  true,
		})
	}

	// Add base image registry secret volume if provided
	if baseRegistrySecretName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "base-registry-auth",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: baseRegistrySecretName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "base-registry-auth",
			MountPath: "/base-registry-auth",
			ReadOnly:  true,
		})
	}

	privileged := true

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cb.namespace,
			Labels: map[string]string{
				"app":        "flightctl-imagebuilder",
				"imagebuild": *imageBuild.Metadata.Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "buildah",
							Image:   "quay.io/buildah/stable:latest",
							Command: buildCmd,
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{"NET_ADMIN", "SYS_ADMIN"},
								},
							},
							VolumeMounts: volumeMounts,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{ //TODO: Make these configurable, OR REMOVE THIS WHOLE SECTION!
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("4"),
									corev1.ResourceMemory: resource.MustParse("8Gi"),
								},
							},
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	return job
}
