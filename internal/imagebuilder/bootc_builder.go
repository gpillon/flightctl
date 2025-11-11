package imagebuilder

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BootcBuilder builds bootc disk images using bootc-image-builder in Kubernetes Jobs
type BootcBuilder struct {
	k8sClient   k8sclient.K8SClient
	namespace   string
	serviceURL  string
	uploadToken string
	log         logrus.FieldLogger
}

// NewBootcBuilder creates a new bootc image builder
func NewBootcBuilder(k8sClient k8sclient.K8SClient, namespace string, serviceURL string, uploadToken string, log logrus.FieldLogger) *BootcBuilder {
	return &BootcBuilder{
		k8sClient:   k8sClient,
		namespace:   namespace,
		serviceURL:  serviceURL,
		uploadToken: uploadToken,
		log:         log,
	}
}

// BootcImageResult contains the result of a bootc image build
type BootcImageResult struct {
	Type         string
	Architecture string
	Path         string
	Size         int64
}

// BuildBootcImages builds bootc disk images for the specified export types
func (bb *BootcBuilder) BuildBootcImages(ctx context.Context, imageBuild *api.ImageBuild, containerImageRef string) ([]BootcImageResult, error) {
	if imageBuild.Spec.BootcExports == nil || len(*imageBuild.Spec.BootcExports) == 0 {
		bb.log.Info("No bootc exports requested, skipping")
		return nil, nil
	}

	var results []BootcImageResult

	for _, export := range *imageBuild.Spec.BootcExports {
		bb.log.Infof("Building bootc image type=%s arch=%s for image %s",
			export.Type, getArchitecture(export.Architecture), *imageBuild.Metadata.Name)

		result, err := bb.buildBootcImage(ctx, imageBuild, containerImageRef, export)
		if err != nil {
			return nil, fmt.Errorf("failed to build bootc image type=%s: %w", export.Type, err)
		}

		results = append(results, result)
	}

	return results, nil
}

// buildBootcImage builds a single bootc disk image
func (bb *BootcBuilder) buildBootcImage(ctx context.Context, imageBuild *api.ImageBuild, containerImageRef string, export api.BootcExport) (BootcImageResult, error) {
	jobName := fmt.Sprintf("bootc-%s-%s", *imageBuild.Metadata.Name, export.Type)
	architecture := getArchitecture(export.Architecture)

	// Use EmptyDir for temporary storage, artifact will be uploaded to imagebuilder service
	bb.log.Infof("Using EmptyDir for temporary bootc output (will be uploaded to imagebuilder service)")

	// Create output directory path in EmptyDir
	outputPath := fmt.Sprintf("/output/%s/%s", *imageBuild.Metadata.Name, export.Type)

	// Create the bootc build job
	job := bb.createBootcJob(jobName, containerImageRef, string(export.Type), architecture, outputPath, imageBuild)

	createdJob, err := bb.k8sClient.CreateJob(ctx, bb.namespace, job)
	if err != nil {
		return BootcImageResult{}, fmt.Errorf("failed to create bootc job: %w", err)
	}

	bb.log.Infof("Created bootc job %s, waiting for completion", createdJob.Name)

	// Wait for the job to complete
	err = bb.k8sClient.WatchJob(ctx, bb.namespace, createdJob.Name)

	// Always retrieve logs after job completion (whether success or failure)
	logs := bb.getJobPodLogs(ctx, *imageBuild.Metadata.Name, string(export.Type))

	if err != nil {
		// Job failed - return error with logs
		bb.log.Errorf("Bootc job %s failed: %v", createdJob.Name, err)
		bb.log.Infof("Retrieved %d log lines from failed bootc job", len(logs))

		// Cleanup job before returning error
		_ = bb.k8sClient.DeleteJob(ctx, bb.namespace, createdJob.Name)

		return BootcImageResult{}, &bootcBuildError{
			err:  fmt.Errorf("bootc job failed: %w", err),
			logs: logs,
		}
	}

	bb.log.Infof("Successfully built bootc image type=%s", export.Type)

	// Cleanup job
	_ = bb.k8sClient.DeleteJob(ctx, bb.namespace, createdJob.Name)
	bb.log.Infof("Cleaned up bootc job %s", createdJob.Name)

	// Return result - artifact was uploaded to imagebuilder service
	// Path now indicates "uploaded" and the storage manager will retrieve it from storage backend
	result := BootcImageResult{
		Type:         string(export.Type),
		Architecture: architecture,
		Path:         fmt.Sprintf("uploaded:%s/%s", *imageBuild.Metadata.Name, export.Type), // Marker for uploaded artifact
	}

	return result, nil
}

// createBootcJob creates a Kubernetes Job that runs bootc-image-builder directly
func (bb *BootcBuilder) createBootcJob(jobName, containerImageRef, imageType, architecture, outputPath string, imageBuild *api.ImageBuild) *batchv1.Job {
	// bootc-image-builder command - run directly without Podman-in-Podman
	buildCmd := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf(`
			set -e
			
			# Ensure /sys is mounted as writable (required by bootc-image-builder, btw this is SO SAD THAT IT'S REQUIRED.....)
			echo "Checking /sys mount..."
			if mount | grep -q 'on /sys type sysfs.*ro'; then
				echo "/sys is mounted read-only, remounting as read-write..."
				mount -o remount,rw /sys || echo "Warning: Failed to remount /sys as rw"
			else
				echo "/sys is already writable or not mounted, attempting to ensure it's available..."
				mount -t sysfs sysfs /sys 2>/dev/null || echo "/sys already mounted"
			fi
			
			# Create Podman container storage structure (required by bootc-image-builder, btw this is SO SAD THAT IT'S REQUIRED.....)
			echo "Setting up Podman storage structure..."
			mkdir -p /var/lib/containers/storage/overlay
			mkdir -p /var/lib/containers/storage/overlay-images
			mkdir -p /var/lib/containers/storage/overlay-layers
			mkdir -p /var/lib/containers/storage/vfs
			mkdir -p /var/lib/containers/storage/vfs-images
			mkdir -p /var/lib/containers/storage/vfs-layers
			mkdir -p /var/lib/containers/cache
			mkdir -p /var/lib/containers/sigstore
			echo "Podman storage structure created"
			# Create output directory for this specific build
			mkdir -p %s
			
			# Configure Podman registries to handle short-name images
			echo "Configuring Podman registries..."
			mkdir -p /etc/containers
			cat > /etc/containers/registries.conf <<'EOF'
# Allow short-name resolution without prompting
unqualified-search-registries = ["localhost:5000", "docker.io"]
short-name-mode = "permissive"
EOF
			
			# Configure registry authentication if credentials are provided
			if [ -f /registry-auth/username ] && [ -f /registry-auth/password ]; then
				echo "Configuring registry authentication..."
				REGISTRY_URL="%s"
				USERNAME=$(cat /registry-auth/username)
				PASSWORD=$(cat /registry-auth/password)
				
				# Create auth.json for podman
				mkdir -p /run/containers/0
				cat > /run/containers/0/auth.json <<EOF
{
  "auths": {
    "${REGISTRY_URL}": {
      "auth": "$(echo -n ${USERNAME}:${PASSWORD} | base64 -w 0)"
    }
  }
}
EOF
				echo "Registry authentication configured for ${REGISTRY_URL}"
			fi
			
			# Pull the bootc container image
			echo "Pulling bootc container image: %s"
			podman pull --log-level=debug %s || {
				echo "ERROR: Failed to pull image %s"
				echo "Image reference format: %s"
				echo "Available images in local storage:"
				podman images || true
				exit 1
			}
			echo "Image pulled successfully"
			
			# Run bootc-image-builder directly
			bootc-image-builder build \
				--type %s \
				--output %s \
				%s
			
			echo "Bootc image build completed successfully"
			ls -lh %s
			
			# Upload the generated artifact to imagebuilder service
			echo "Uploading artifact to imagebuilder service..."
			ARTIFACT_FILE=$(find %s -type f \( -name "*.iso" -o -name "*.qcow2" -o -name "*.raw" -o -name "*.vmdk" -o -name "*.ami" -o -name "*.tar" \) | head -n 1)
			
			if [ -z "$ARTIFACT_FILE" ]; then
				echo "ERROR: No artifact file found in %s"
				exit 1
			fi
			
			echo "Found artifact: $ARTIFACT_FILE"
			ARTIFACT_SIZE=$(stat -c%%s "$ARTIFACT_FILE")
			echo "Artifact size: $ARTIFACT_SIZE bytes"
			
			# Upload to imagebuilder service using multipart form
			# Note: metadata fields MUST come before file for streaming upload
			curl -X POST \
				-H "Authorization: Bearer ${UPLOAD_TOKEN}" \
				-F "imageName=%s" \
				-F "imageType=%s" \
				-F "architecture=%s" \
				-F "file=@${ARTIFACT_FILE}" \
				-f \
				${IMAGEBUILDER_UPLOAD_URL}/api/v1/imagebuilds/upload || {
					echo "ERROR: Failed to upload artifact to imagebuilder service"
					exit 1
				}
			
			echo "Artifact uploaded successfully"
		`, outputPath, bb.getRegistryURL(imageBuild), containerImageRef, containerImageRef, containerImageRef, containerImageRef, imageType, outputPath, containerImageRef, outputPath, outputPath, outputPath, *imageBuild.Metadata.Name, imageType, architecture),
	}

	backoffLimit := int32(2)
	ttlSecondsAfterFinished := int32(3600)
	privileged := true
	allowPrivilegeEscalation := true
	runAsUser := int64(0)
	runAsGroup := int64(0)
	fsGroup := int64(0)

	// Check if registry credentials are provided
	secretName := ""
	if imageBuild.Spec.ContainerRegistry != nil &&
		imageBuild.Spec.ContainerRegistry.Credentials != nil &&
		imageBuild.Spec.ContainerRegistry.Credentials.Username != nil {
		secretName = fmt.Sprintf("registry-%s", *imageBuild.Metadata.Name)
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: bb.namespace,
			Labels: map[string]string{
				"app":        "flightctl-imagebuilder",
				"imagebuild": *imageBuild.Metadata.Name,
				"type":       "bootc",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						// Disable seccomp and AppArmor to allow bootc-image-builder full system access
						"container.apparmor.security.beta.kubernetes.io/bootc-builder": "unconfined",
						"seccomp.security.alpha.kubernetes.io/pod":                     "unconfined",
					},
				},
				Spec: corev1.PodSpec{
					// HostNetwork:   true, // Enable host network to access internet for pulling images
					RestartPolicy: corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  &runAsUser,
						RunAsGroup: &runAsGroup,
						FSGroup:    &fsGroup,
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeUnconfined,
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "bootc-builder",
							Image:   "quay.io/centos-bootc/bootc-image-builder:latest",
							Command: buildCmd,
							Env: []corev1.EnvVar{
								{
									Name:  "UPLOAD_TOKEN",
									Value: bb.uploadToken,
								},
								{
									Name:  "IMAGEBUILDER_UPLOAD_URL",
									Value: bb.serviceURL,
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged:               &privileged,
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								ReadOnlyRootFilesystem:   func() *bool { b := false; return &b }(),
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{
										"SYS_ADMIN",  // Required for mount operations
										"MKNOD",      // Required for creating device nodes
										"SYS_CHROOT", // Required for chroot
										"SETFCAP",    // Required for file capabilities
										"SYS_MODULE", // Required for loading kernel modules
										"NET_ADMIN",  // Required for network operations
										"MAC_ADMIN",  // Required for SELinux chcon operations
									},
								},
								SELinuxOptions: &corev1.SELinuxOptions{
									Type: "unconfined_t",
								},
							},
							VolumeMounts: func() []corev1.VolumeMount {
								mounts := []corev1.VolumeMount{
									{
										Name:      "output",
										MountPath: "/output",
									},
									{
										Name:      "containers-storage",
										MountPath: "/var/lib/containers",
									},
									{
										Name:      "sys",
										MountPath: "/sys",
									},
								}
								// Add registry secret mount if credentials are provided
								if secretName != "" {
									mounts = append(mounts, corev1.VolumeMount{
										Name:      "registry-auth",
										MountPath: "/registry-auth",
										ReadOnly:  true,
									})
								}
								return mounts
							}(),
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("8"),
									corev1.ResourceMemory: resource.MustParse("16Gi"),
								},
							},
						},
					},
					Volumes: func() []corev1.Volume {
						volumes := []corev1.Volume{
							{
								Name: "output",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{
										SizeLimit: func() *resource.Quantity { q := resource.MustParse("20Gi"); return &q }(),
									},
								},
							},
							{
								Name: "containers-storage",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							},
							{
								Name: "sys",
								VolumeSource: corev1.VolumeSource{
									HostPath: &corev1.HostPathVolumeSource{
										Path: "/sys",
										Type: func() *corev1.HostPathType { t := corev1.HostPathDirectory; return &t }(),
									},
								},
							},
						}
						// Add registry secret volume if credentials are provided
						if secretName != "" {
							volumes = append(volumes, corev1.Volume{
								Name: "registry-auth",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName: secretName,
									},
								},
							})
						}
						return volumes
					}(),
				},
			},
		},
	}

	return job
}

// getRegistryURL extracts registry URL from ImageBuild spec
func (bb *BootcBuilder) getRegistryURL(imageBuild *api.ImageBuild) string {
	return GetRegistryURL(imageBuild)
}

// getArchitecture returns the architecture string, defaulting to x86_64
func getArchitecture(arch *api.BootcExportArchitecture) string {
	if arch == nil {
		return "x86_64"
	}
	return string(*arch)
}

// getImageExtensionFromString returns the file extension for a string image type
func getImageExtensionFromString(imageType string) string {
	extensions := map[string]string{
		"iso":   "iso",
		"qcow2": "qcow2",
		"vmdk":  "vmdk",
		"raw":   "raw",
		"ami":   "raw",
		"tar":   "tar",
	}

	if ext, ok := extensions[imageType]; ok {
		return ext
	}
	return imageType
}

// bootcBuildError wraps an error with associated logs from bootc job
type bootcBuildError struct {
	err  error
	logs []string
}

func (e *bootcBuildError) Error() string {
	return e.err.Error()
}

func (e *bootcBuildError) Logs() []string {
	return e.logs
}

func (e *bootcBuildError) Unwrap() error {
	return e.err
}

// getJobPodLogs retrieves logs from pods associated with a bootc build job
func (bb *BootcBuilder) getJobPodLogs(ctx context.Context, imageBuildName string, exportType string) []string {
	// Find pods for this job
	labelSelector := fmt.Sprintf("job-name=bootc-%s-%s", imageBuildName, exportType)
	pods, err := bb.k8sClient.ListPods(ctx, bb.namespace, labelSelector)
	if err != nil {
		bb.log.WithError(err).Warn("Failed to list pods for failed bootc job")
		return []string{fmt.Sprintf("Failed to retrieve bootc build logs: %v", err)}
	}

	if len(pods.Items) == 0 {
		bb.log.Warn("No pods found for bootc job")
		return []string{"No pods found for this bootc build job"}
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
				if containerStatus.State.Terminated.Message != "" {
					podStatus += fmt.Sprintf("\n  Message: %s", containerStatus.State.Terminated.Message)
				}
			} else if containerStatus.State.Waiting != nil {
				podStatus = fmt.Sprintf("%s (waiting: %s)",
					podStatus,
					containerStatus.State.Waiting.Reason)
				if containerStatus.State.Waiting.Message != "" {
					podStatus += fmt.Sprintf("\n  Message: %s", containerStatus.State.Waiting.Message)
				}
			}
		}

		allLogs = append(allLogs, fmt.Sprintf("=== Bootc Pod %s (Status: %s) ===", pod.Name, podStatus))

		// Add pod conditions for more debug info
		if len(pod.Status.Conditions) > 0 {
			allLogs = append(allLogs, "Pod Conditions:")
			for _, cond := range pod.Status.Conditions {
				if cond.Status != "True" {
					allLogs = append(allLogs, fmt.Sprintf("  - %s: %s (Reason: %s, Message: %s)",
						cond.Type, cond.Status, cond.Reason, cond.Message))
				}
			}
		}

		// Add container status details if available
		if len(pod.Status.ContainerStatuses) > 0 {
			allLogs = append(allLogs, "Container Status:")
			for _, cs := range pod.Status.ContainerStatuses {
				allLogs = append(allLogs, fmt.Sprintf("  - %s: Ready=%v, RestartCount=%d",
					cs.Name, cs.Ready, cs.RestartCount))
			}
		}

		// Retrieve logs with higher limit for failed builds
		podLog, err := bb.k8sClient.GetPodLogs(ctx, bb.namespace, pod.Name, 2000)
		if err != nil {
			bb.log.WithError(err).Warnf("Failed to get logs for bootc pod %s", pod.Name)
			allLogs = append(allLogs, fmt.Sprintf("Failed to retrieve logs: %v", err))
			continue
		}

		if len(podLog) > 0 {
			allLogs = append(allLogs, "\nContainer Logs:")
			allLogs = append(allLogs, podLog)
		} else {
			allLogs = append(allLogs, "\n(no container logs available - pod likely failed before container started)")
		}
		allLogs = append(allLogs, "") // Empty line between pods
	}

	return allLogs
}
